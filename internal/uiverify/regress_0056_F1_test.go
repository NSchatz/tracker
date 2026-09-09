// Regression artefact for S0056 impl-gate finding F1 (refuter, impl ordinal 1).
//
// AC18 of work/specs/S0056-tracker-frontend-conventions/spec.md:
//
//	"WHEN either grading route runs, THE SYSTEM SHALL grade every claim above from a live
//	 rendering on the runtime that draws it, and SHALL fail if fewer demonstrations ran than
//	 there are rendered claims for that surface [...] so that no assertion here can pass
//	 vacuously."
//
// The browser route implements that count in uiverify.Summarise (run.go:108-114): it compares the
// number of demonstrations against len(Checks()) and returns an error when they disagree.
//
// The ANDROID surface has no such count anywhere. `make verify-ui-android` is
// `uiverify docs` + `scripts/android-emulator.sh` + `gradlew connectedDebugAndroidTest`; none of
// those three counts demonstrations. The only Go-side gate that reads the emulator's results is
// CheckRecord, and it consults them solely to confirm that each assertion NAMED IN
// FRONTEND-CONVENTIONS-RECORD.md ran. That record names the twelve claim cases and not one of the
// twelve *_demonstration cases, so a suite whose demonstrations were all deleted, renamed or
// @Ignore'd still reports green.
//
// This test asserts the behaviour AC18 requires and FAILS on the branch as shipped.
//
// It is a REFUTER artefact: it documents the defect. Fixing it is upstream's job, and the fix is
// not "delete this file" - it is a count on the Android surface equal in force to Summarise's.
package uiverify

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// theAndroidClaims are exactly the assertion names FRONTEND-CONVENTIONS-RECORD.md maps the eleven
// clauses to on the android screen.
//
// It is READ FROM THE RECORD rather than hand-copied. The list was twelve names when this artefact
// was written; the item was later narrowed and two clauses moved to another repository item, and a
// hand-copied list would then have been testing a set the repository no longer has. The defect this
// case pins is "the route counts no demonstrations", which is a property of the count and not of any
// particular twelve names, so the fixture follows the record and the assertion below stays exactly
// as strong.
func theAndroidClaims(t *testing.T) []string {
	t.Helper()
	names, err := recordedAssertions(filepath.Join("..", ".."), AndroidSurface)
	if err != nil {
		t.Fatalf("reading the committed record: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("the committed record names no assertion on the android screen, so this case would " +
			"drive the check against an empty set and could not fail for the reason it exists to catch")
	}
	return names
}

func TestRegressS0056F1AndroidRouteCountsNoDemonstrations(t *testing.T) {
	root := t.TempDir()
	claims := theAndroidClaims(t)

	// The committed record, verbatim.
	real, err := os.ReadFile(filepath.Join("..", "..", RecordFile))
	if err != nil {
		t.Fatalf("reading the committed record: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, RecordFile), real, 0o644); err != nil {
		t.Fatal(err)
	}

	// The instrumented suite, verbatim: the record defers two clauses to another item and the check
	// compares those deferrals against the suite's own @Ignore'd cases, so a root without the suite
	// would refuse for a reason unrelated to the count this case pins.
	suite, err := os.ReadFile(filepath.Join("..", "..", parkedSuite))
	if err != nil {
		t.Fatalf("reading the committed instrumented suite: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, parkedSuite)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, parkedSuite), suite, 0o644); err != nil {
		t.Fatal(err)
	}

	// The browser route's run record: every web claim passed and every one was demonstrated, so
	// nothing on that surface can be what makes the check refuse.
	var web []Result
	for _, c := range Checks() {
		web = append(web, Result{ID: c.ID, Passed: true, Demonstrated: true})
	}
	if err := RecordRun(root, "web", web); err != nil {
		t.Fatal(err)
	}

	// An android source tree with no web view in it, so the F11 exemption stands and cannot be
	// what makes the check refuse either.
	srcDir := filepath.Join(root, "android", "app", "src", "main", "java")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "Screen.kt"), []byte("package com.nschatz.tracker\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The emulator's own report of what it ran: every rendered claim the record names, ALL PASSING,
	// and not one demonstration. This is what a suite whose *_demonstration cases had been deleted,
	// renamed or @Ignore'd would produce, and `gradlew connectedDebugAndroidTest` is green on it.
	resultsDir := filepath.Join(root, "android", "app", "build", "outputs", "androidTest-results", "connected")
	if err := os.MkdirAll(resultsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><testsuite name="UiClaimTest" failures="0" skipped="0">`)
	for _, name := range claims {
		b.WriteString(`<testcase name="` + name + `" classname="com.nschatz.tracker.ui.UiClaimTest"/>`)
	}
	b.WriteString(`</testsuite>`)
	junit := b.String()
	if strings.Contains(junit, "_demonstration") {
		t.Fatal("the fixture carries a demonstration, so this case would not test what it says it does")
	}
	if err := os.WriteFile(filepath.Join(resultsDir, "results.xml"), []byte(junit), 0o644); err != nil {
		t.Fatal(err)
	}

	// AC18: twelve rendered claims on the android screen, zero demonstrations. The route that
	// grades that surface, and the record check that is the only thing standing behind it, must
	// refuse - "a check that was never shown going red is not evidence" is the whole point of the
	// criterion, and it is written for BOTH surfaces ("either grading route").
	err = CheckRecord(io.Discard, root)
	if err == nil {
		t.Fatalf("AC18 (spec.md): the android surface reported %d rendered claims and 0 demonstrations, "+
			"and nothing refused.\n"+
			"uiverify.Summarise enforces demonstrations == claims for the browser map (run.go:108-114); "+
			"there is no equivalent for the android screen. `make verify-ui-android` is "+
			"`uiverify docs` + scripts/android-emulator.sh + `gradlew connectedDebugAndroidTest`, none of "+
			"which counts, and FRONTEND-CONVENTIONS-RECORD.md names only the twelve claim cases, so "+
			"CheckRecord never looks for a demonstration at all. A suite that lost every "+
			"*_demonstration case still reports green.",
			len(claims))
	}
	if !strings.Contains(err.Error(), "demonstration") {
		t.Fatalf("the record check refused, but for a reason unrelated to the missing demonstrations, "+
			"so the AC18 count is still not enforced on the android surface: %v", err)
	}
}
