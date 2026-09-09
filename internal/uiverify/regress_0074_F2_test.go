package uiverify

// Regression artifact for impl-gate finding F2 of S0074-tracker-android-a11y-operability.
//
// AC14: "WHEN the Android screen is driven by keyboard or directional navigation on an emulator,
// THE SYSTEM SHALL make every control reachable and activatable ..."
//
// The instrumented suite drives the directional traversal at exactly ONE control - it calls
// focusByDirection("action-save", ...) and asserts that it returned true. No assertion in the suite
// requires any other control on the home screen to be reachable, so the criterion's "every control"
// is graded for one control out of nine and the route cannot go red for the other eight. That is the
// same shape as impl-gate finding F20 itself: a control off the focus order that nothing measures.
//
// This test collects the home screen's own interactive controls out of MainActivity.kt - the
// RingedButton call sites, the ExplainAffordance call sites, and every tag on a modifier chain
// carrying focusRing(), which is the modifier this screen puts on exactly the things that take
// focus - and requires each of them to be named in a directional-traversal assertion.
//
// It goes GREEN when the traversal asserts reachability for every control the screen declares.
//
// It is a source scan for the same reason internal/uiverify already scans these files in
// unmutatedClaimProblems and parkedSuiteProblems: it grades no rendered property, it answers "which
// controls does this suite assert are reachable", which is a fact about the suite.

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var (
	regress0074RingedButton     = regexp.MustCompile(`RingedButton\(`)
	regress0074TagArgument      = regexp.MustCompile(`tag = "([a-z0-9-]+)"`)
	regress0074ExplainAffordanc = regexp.MustCompile(`ExplainAffordance\([^,]+,[^,]+, "([a-z0-9-]+)"`)
	regress0074TestTag          = regexp.MustCompile(`testTag\("([a-z0-9-]+)"\)`)
	regress0074FocusByDirection = regexp.MustCompile(`focusByDirection\("([a-z0-9-]+)"`)
)

// regress0074SecondScreen are tags that belong to the explanation destination rather than to the one
// screen AC14 is written about. AC14 is graded on "the Android screen"; this artifact holds the
// suite to that screen's controls and no more.
var regress0074SecondScreen = map[string]bool{"explanation-back": true}

func TestRegress0074F2EveryControlIsAssertedReachable(t *testing.T) {
	root := regress0074RepoRoot(t)

	screen := regress0074Read(t, filepath.Join(root, "android", "app", "src", "main", "java",
		"com", "nschatz", "tracker", "ui", "MainActivity.kt"))
	suite := regress0074Read(t, filepath.Join(root, parkedSuite))

	controls := map[string]bool{}

	// Every RingedButton names the control it draws.
	for _, at := range regress0074RingedButton.FindAllStringIndex(screen, -1) {
		window := screen[at[1]:min(at[1]+1500, len(screen))]
		if m := regress0074TagArgument.FindStringSubmatch(window); m != nil {
			controls[m[1]] = true
		}
	}
	// Every card's explanation affordance.
	for _, m := range regress0074ExplainAffordanc.FindAllStringSubmatch(screen, -1) {
		controls[m[1]] = true
	}
	// Everything else that takes focus: focusRing() is the modifier this screen puts on exactly the
	// controls a person can focus, and the tag sits in the same chain.
	for _, at := range regress0074TestTag.FindAllStringSubmatchIndex(screen, -1) {
		start := max(0, at[0]-260)
		if strings.Contains(screen[start:at[0]], "focusRing(") {
			controls[screen[at[2]:at[3]]] = true
		}
	}
	for tag := range regress0074SecondScreen {
		delete(controls, tag)
	}

	if len(controls) < 5 {
		t.Fatalf("found only %d controls in MainActivity.kt, so this artifact would report on almost "+
			"nothing: %v", len(controls), regress0074Sorted(controls))
	}

	asserted := map[string]bool{}
	for _, m := range regress0074FocusByDirection.FindAllStringSubmatch(suite, -1) {
		asserted[m[1]] = true
	}

	// The traversal names action-save inside a call whose onClick body is long enough that the scan
	// above does not reach its tag; it is a control either way, so the population is the union.
	all := map[string]bool{}
	for t := range controls {
		all[t] = true
	}
	for t := range asserted {
		all[t] = true
	}

	var uncovered []string
	for tag := range controls {
		if !asserted[tag] {
			uncovered = append(uncovered, tag)
		}
	}
	sort.Strings(uncovered)

	if len(uncovered) > 0 {
		t.Errorf(`AC14: "THE SYSTEM SHALL make every control reachable and activatable".

The home screen declares %d controls: %v
%s drives a directional traversal at %v only.

No assertion requires these %d controls to be reachable by directional navigation:
  %s

So `+"`make verify-ui-android`"+` cannot go red for any of them, and AC14's first SHALL is graded for
one control out of %d. F20 - the finding this item exists to close - was exactly a control that the
focus order did not reach, so an ungraded control here is the same defect one step away.`,
			len(all), regress0074Sorted(all), parkedSuite, regress0074Sorted(asserted),
			len(uncovered), strings.Join(uncovered, "\n  "), len(all))
	}
}

func regress0074Sorted(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
