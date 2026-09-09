package uiverify

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
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
// Android route green with no evidence that any of its assertions can fail at all. The count is not
// written down here: it is however many assertions the committed record names, so a clause that
// leaves this surface for another item takes its own number with it.
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
	problems = append(problems, unmutatedClaimProblems(root)...)
	problems = append(problems, gradingEvidenceProblems(root)...)

	fmt.Fprintf(w, "\n%s: %d/%d clause assertions ran, %d/%d demonstrated able to fail\n",
		AndroidSurface, len(run.Ran), len(expected), len(run.Demonstrated), len(run.Ran))

	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("the %s route did not grade what it claims to have graded:\n  - %s",
			AndroidSurface, strings.Join(problems, "\n  - "))
	}
	return nil
}

// EvidenceLog is where `make verify-ui-android` puts what the emulator measured, pulled off the
// device after the instrumented suite has run.
const EvidenceLog = "build/uiverify/android-grading.log"

// evidenceMarkers are the lines the committed documents PROMISE this file carries: one summary per
// claim AC13 makes, in each of the two themes, plus one per AC14 claim.
//
// There is no spoken-name marker because there is no spoken-name claim on this route: it went to
// S0076-tracker-android-spoken-name with the check that measured it wrongly, and a marker for a claim
// nothing grades would be this gate demanding evidence of a measurement that is not taken.
//
// android/README.md says "every number the sweep measured is written to build/uiverify/
// android-grading.log and printed by make verify-ui-android", and the Makefile's ANDROID_EVIDENCE_TAG
// comment says the same. Those sentences are checked here rather than trusted, because they were
// FALSE for the whole life of the mechanism they describe: impl-gate finding F3 found the file at
// zero bytes on every real emulator run, on a route that graded 36 cases green, because the write
// went through `UiAutomation.executeShellCommand` - which is `Runtime.exec`, splitting on whitespace
// with no shell and no quoting, so a `sh -c '... >> file'` redirect was never a redirect. The suite
// swallowed the failure by design ("evidence is an aid, never a gate"), so nothing turned red.
//
// It is a gate now. An aid that can stop working without anything noticing is how two committed
// documents came to describe a file that had never been written.
var evidenceMarkers = []string{
	"AC13 contrast [light]:",
	"AC13 contrast [dark]:",
	"AC13 target-size [light]:",
	"AC13 target-size [dark]:",
	"AC13 no-state-by-colour-alone [light]:",
	"AC13 no-state-by-colour-alone [dark]:",
	"AC14 operable without a pointer:",
	"AC14 focus indicator:",
}

// minimumMeasurementLines is the floor on the per-view lines - the ones carrying a measured contrast
// ratio and a measured size for one node. The sweep writes one per view per card per claim per
// theme, which is thousands; a floor two orders of magnitude below that catches a mechanism that
// wrote its summaries and lost the numbers without turning red on a device that was merely slow.
const minimumMeasurementLines = 100

// gradingEvidenceProblems refuses a run whose grading evidence is absent, empty, or missing the
// per-claim lines the committed documents say it carries.
func gradingEvidenceProblems(root string) []string {
	path := filepath.Join(root, EvidenceLog)
	body, err := os.ReadFile(path)
	if err != nil {
		return []string{fmt.Sprintf(
			"there is no grading evidence at %s: %v. android/README.md and the Makefile both state that "+
				"every number the sweep measured is written there and printed by `make verify-ui-android`, "+
				"so a route that reports green without it is reporting on a mechanism that did not run",
			EvidenceLog, err)}
	}
	text := string(body)
	if strings.TrimSpace(text) == "" {
		return []string{fmt.Sprintf(
			"%s is empty. The emulator ran and measured, and wrote none of it down; two committed "+
				"documents say it wrote all of it. Either the device log tag (`UiHarness.EVIDENCE_TAG`, "+
				"mirrored as ANDROID_EVIDENCE_TAG in the Makefile) stopped matching, or the suite stopped "+
				"calling UiHarness.evidence", EvidenceLog)}
	}

	var problems []string
	var missing []string
	for _, marker := range evidenceMarkers {
		if !strings.Contains(text, marker) {
			missing = append(missing, marker)
		}
	}
	if len(missing) > 0 {
		problems = append(problems, fmt.Sprintf(
			"%s carries no line for %d of the claims it is supposed to carry every measured number for: %q. "+
				"A claim whose numbers are absent was either not graded or graded silently, and either way "+
				"the documents describing this file are wrong about it",
			EvidenceLog, len(missing), strings.Join(missing, ", ")))
	}

	measured := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "contrast=") {
			measured++
		}
	}
	if measured < minimumMeasurementLines {
		problems = append(problems, fmt.Sprintf(
			"%s carries %d per-view measurements and the floor is %d: the sweep names every view it "+
				"looked at, so a file with the summaries and none of the numbers is not the file "+
				"android/README.md describes",
			EvidenceLog, measured, minimumMeasurementLines))
	}
	return problems
}

// unmutatedClaimProblems is the half of AC27 a JUnit result file cannot answer: that the CLAIMS were
// graded on the screen with NO mutation applied.
//
// The result file says a case named `AC13_contrast_light` passed. It cannot say what that case
// launched, and a claim quietly launched against a mutated screen would be grading something no
// person will ever see - while its demonstration, which needs a mutation, went red exactly as
// expected and reported the pair as sound. So the suite is read: a case that is not a
// `_demonstration` may not select a mutation at all.
//
// Like [parkedSuiteProblems] this reads Kotlin SOURCE, and for the same reason. It grades no
// rendered property - F2's rule is untouched - it answers "what did this case ASK the app to draw",
// which is a fact about the suite and leaves nothing behind on the emulator to inspect.
func unmutatedClaimProblems(root string) []string {
	path := filepath.Join(root, parkedSuite)
	body, err := os.ReadFile(path)
	if err != nil {
		return []string{fmt.Sprintf(
			"could not read the instrumented suite at %s to check that its claims are graded on an unmutated screen: %v",
			parkedSuite, err)}
	}

	var problems []string
	claims := 0
	blocks := strings.Split(string(body), "@Test")
	for _, block := range blocks[1:] {
		// A case's own body ends where the file's private helpers begin. Without that bound the
		// last @Test in the file would carry every helper below it, and a mutation named in one of
		// those would be read as the case having selected it.
		if cut := strings.Index(block, "\n    private "); cut >= 0 {
			block = block[:cut]
		}
		m := funDeclaration.FindStringSubmatch(block)
		if m == nil {
			for _, line := range strings.Split(block, "\n") {
				if n := funDeclaration.FindStringSubmatch(line); n != nil {
					m = n
					break
				}
			}
		}
		if m == nil {
			continue
		}
		name := m[1]
		if strings.HasSuffix(name, DemonstrationSuffix) {
			continue
		}
		claims++
		if strings.Contains(block, "UiMutation.") {
			problems = append(problems, fmt.Sprintf(
				"%s grades the claim %q against a mutated screen; a claim is graded on the screen with NO mutation applied, and only its %s may select one",
				parkedSuite, name, DemonstrationSuffix))
		}
	}
	if claims == 0 {
		problems = append(problems, fmt.Sprintf(
			"%s declares no claim case at all, so this check would pass over an empty set", parkedSuite))
	}
	return problems
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
