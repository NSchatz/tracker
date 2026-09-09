package uiverify

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// CheckAndroidRun is AC18 on the Android surface: the count [Summarise] applies to the browser route,
// applied to the emulator's own report of what it ran.
//
// The browser route can hold this in memory - it owns [Checks] and it watches each one go red inside
// the same process. The emulator route cannot: the claims and their demonstrations are separate JUnit
// cases that ran in another process on another machine, and `gradlew connectedDebugAndroidTest` is
// green whenever the cases that DID run passed. So the count has to be recovered afterwards from the
// results the emulator wrote, and that is what this does:
//
//   - every rendered claim the committed record names on the Android screen must have run green,
//     which is the anti-vacuity floor: a suite that quietly stopped launching a claim fails here,
//     not silently;
//   - every claim that ran must carry a passing `<claim>_demonstration`, which is the same measuring
//     code shown going red against a screen mutated to break exactly that claim;
//   - and the demonstration count must not fall below the claim count.
//
// Without this, deleting, renaming or @Ignore-ing every `*_demonstration` case leaves the whole
// Android route green with no evidence that any of its twelve assertions can fail at all.
func CheckAndroidRun(w io.Writer, root string) error {
	expected, err := recordedAssertions(root, AndroidSurface)
	if err != nil {
		return err
	}
	if len(expected) == 0 {
		return fmt.Errorf("%s names no rendered assertion on the %s, so this route would report green having graded nothing",
			RecordFile, AndroidSurface)
	}

	run, err := androidRun(root)
	if err != nil {
		return err
	}

	var problems []string
	for _, a := range expected {
		if !run.Ran[a] {
			problems = append(problems, fmt.Sprintf(
				"%s names the assertion %q on the %s, and the emulator did not report it passing: a claim that did not run is not evidence",
				RecordFile, a, AndroidSurface))
		}
	}

	claims := make([]string, 0, len(run.Ran))
	for a := range run.Ran {
		claims = append(claims, a)
	}
	sort.Strings(claims)
	for _, a := range claims {
		if !run.Demonstrated[a] {
			problems = append(problems, fmt.Sprintf(
				"the claim %q ran, but no passing case named %q did: it was never shown going red, so its pass is not evidence (AC18)",
				a, a+DemonstrationSuffix))
		}
	}

	if len(run.Demonstrated) < len(run.Ran) {
		problems = append(problems, fmt.Sprintf(
			"%s: %d rendered claims but only %d demonstrations (AC18): a check that was never shown going red is not evidence",
			AndroidSurface, len(run.Ran), len(run.Demonstrated)))
	}

	// AC18 binds the ROUTE ("WHEN either grading route runs"), so the fence around a parked case is
	// applied here too and not only by `make verify-ui-record` afterwards. Without it, @Ignore-ing a
	// case AND striking its name from the record would leave this route green having quietly graded
	// one clause fewer.
	deferred, derr := deferredAssertions(root)
	if derr != nil {
		return derr
	}
	problems = append(problems, parkedSuiteProblems(root, deferred)...)

	fmt.Fprintf(w, "\n%s: %d/%d clause assertions ran, %d/%d demonstrated able to fail\n",
		AndroidSurface, len(run.Ran), len(expected), len(run.Demonstrated), len(run.Ran))

	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("the %s route did not grade what it claims to have graded:\n  - %s",
			AndroidSurface, strings.Join(problems, "\n  - "))
	}
	return nil
}

// recordedAssertions is the set of rendered assertions the committed record names on one surface,
// de-duplicated and sorted. It is the list of "rendered claims for that surface" AC18 counts against,
// and it lives in the record rather than in Go so that the record, the route and the emulator cannot
// disagree about what was supposed to run.
func recordedAssertions(root, surface string) ([]string, error) {
	rows, err := ParseRecord(joinRecordPath(root))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", RecordFile, err)
	}
	seen := map[string]bool{}
	var out []string
	for _, row := range rows {
		if row.Surface != surface {
			continue
		}
		for _, a := range row.Assertions {
			if seen[a] {
				continue
			}
			seen[a] = true
			out = append(out, a)
		}
	}
	sort.Strings(out)
	return out, nil
}
