// The record check, checked.
//
// AC20 asks the record to REFUSE four things: a clause/surface pair that is missing, a pair carrying
// both an assertion and an exemption, a pair naming an assertion that did not run, and the F11
// Android exemption once the client renders a web view. AC18 asks both grading routes to refuse a
// fifth: an assertion that ran but was never shown going red. A check that could not detect those
// would be a check whose green means nothing, so each refusal is driven here against a synthesised
// repository and asserted to fire.
//
// This is not a rendered claim and deliberately does not pretend to be one: it grades the RECORD,
// which is a file, and files are graded by reading them. The rendered claims the record points at
// are graded by the browser engine and the emulator, and nothing here stands in for either.
package uiverify

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// a minimal repository: the record, a browser run record, and an instrumented result file.
type fakeRepo struct {
	root string
}

// theRepoRoot is where the REAL committed record and explanation documents live, relative to this
// package.
const theRepoRoot = "../.."

// claimsInTheCommittedRecord is the set of assertions the shipped record names on one surface. The
// fixtures are derived from it rather than from a hand-copied list, so a claim added to the record
// without a fixture cannot drift past these tests unnoticed.
func claimsInTheCommittedRecord(t *testing.T, surface string) []string {
	t.Helper()
	names, err := recordedAssertions(theRepoRoot, surface)
	if err != nil {
		t.Fatalf("reading the committed record: %v", err)
	}
	if len(names) == 0 {
		t.Fatalf("the committed record names no assertion on the %s", surface)
	}
	return names
}

func newFakeRepo(t *testing.T) *fakeRepo {
	t.Helper()
	r := &fakeRepo{root: t.TempDir()}
	r.writeAndroidResults(t, withDemonstrations(claimsInTheCommittedRecord(t, AndroidSurface))...)
	r.writeWebRun(t, claimsInTheCommittedRecord(t, "browser map")...)
	r.writeAndroidSource(t, "package com.nschatz.tracker\n\nclass Nothing\n")
	r.writeParkedSuite(t, nil)
	return r
}

// writeParkedSuite copies the REAL instrumented suite, optionally edited, so the deferral fence is
// driven against the file this repository actually ships rather than a convenient stand-in.
func (r *fakeRepo) writeParkedSuite(t *testing.T, edits ...func(string) string) {
	t.Helper()
	real, err := os.ReadFile(filepath.Join(theRepoRoot, parkedSuite))
	if err != nil {
		t.Fatalf("reading the committed instrumented suite: %v", err)
	}
	body := string(real)
	for _, edit := range edits {
		if edit == nil {
			continue
		}
		before := body
		body = edit(body)
		if body == before {
			t.Fatal("the edit changed nothing, so this case would not test what it says it does")
		}
	}
	path := filepath.Join(r.root, parkedSuite)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// withDemonstrations is what a green instrumented run reports: every claim, and beside each one the
// case that showed it going red.
func withDemonstrations(claims []string) []string {
	out := make([]string, 0, len(claims)*2)
	for _, c := range claims {
		out = append(out, c, c+DemonstrationSuffix)
	}
	return out
}

func (r *fakeRepo) writeWebRun(t *testing.T, ids ...string) {
	t.Helper()
	var results []Result
	for _, id := range ids {
		results = append(results, Result{ID: id, Passed: true, Demonstrated: true})
	}
	if err := RecordRun(r.root, "web", results); err != nil {
		t.Fatal(err)
	}
}

func (r *fakeRepo) writeAndroidResults(t *testing.T, names ...string) {
	t.Helper()
	dir := filepath.Join(r.root, "android", "app", "build", "outputs", "androidTest-results", "connected")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><testsuite name="UiClaimTest">`)
	for _, n := range names {
		b.WriteString(`<testcase name="` + n + `" classname="com.nschatz.tracker.ui.UiClaimTest"/>`)
	}
	b.WriteString(`</testsuite>`)
	if err := os.WriteFile(filepath.Join(dir, "results.xml"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (r *fakeRepo) writeAndroidSource(t *testing.T, body string) {
	t.Helper()
	dir := filepath.Join(r.root, "android", "app", "src", "main", "java")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Screen.kt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeRecord copies the REAL committed record and applies an optional edit, so these tests are
// driven against the record this repository actually ships rather than a convenient stand-in.
func (r *fakeRepo) writeRecord(t *testing.T, edit func(string) string) {
	t.Helper()
	real, err := os.ReadFile(filepath.Join("..", "..", RecordFile))
	if err != nil {
		t.Fatalf("reading the committed record: %v", err)
	}
	body := string(real)
	if edit != nil {
		before := body
		body = edit(body)
		if body == before {
			t.Fatal("the edit changed nothing, so this case would not test what it says it does")
		}
	}
	if err := os.WriteFile(filepath.Join(r.root, RecordFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestTheCommittedRecordIsWellFormed: the file this repository ships parses, covers all
// twenty-two pairs, and maps each exactly once. It is checked against a SYNTHESISED set of run
// records, because the real ones come from the two grading routes and this is `make check-go`.
func TestTheCommittedRecordIsWellFormed(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo(t)
	repo.writeRecord(t, nil)
	if err := CheckRecord(io.Discard, repo.root); err != nil {
		t.Fatalf("the committed record does not hold:\n%v", err)
	}
}

func TestARecordMissingAPairIsRefused(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo(t)
	repo.writeRecord(t, func(s string) string {
		return removeLineContaining(s, "| F6 | android screen |")
	})
	err := CheckRecord(io.Discard, repo.root)
	if err == nil {
		t.Fatal("a record missing the F6 Android pair was accepted")
	}
	if !strings.Contains(err.Error(), "F6 on the android screen is not in the record") {
		t.Fatalf("the refusal does not name the missing pair: %v", err)
	}
}

func TestAPairWithBothAnAssertionAndAnExemptionIsRefused(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo(t)
	repo.writeRecord(t, func(s string) string {
		return strings.Replace(s,
			"| F5 | browser map | AC7-one-bad-event | - |",
			"| F5 | browser map | AC7-one-bad-event | also exempt because reasons |", 1)
	})
	err := CheckRecord(io.Discard, repo.root)
	if err == nil {
		t.Fatal("a pair carrying both an assertion and an exemption was accepted")
	}
	if !strings.Contains(err.Error(), "carries BOTH") {
		t.Fatalf("the refusal does not name the double mapping: %v", err)
	}
}

// The one that matters most: a record may not name an assertion nobody ran. Otherwise the record
// becomes a claim, which is the thing it exists to stop being.
func TestAnAssertionThatDidNotRunIsRefused(t *testing.T) {
	t.Parallel()

	t.Run("browser", func(t *testing.T) {
		repo := newFakeRepo(t)
		repo.writeRecord(t, nil)
		repo.writeWebRun(t, "AC1-keyboard") // everything else is now unrun
		err := CheckRecord(io.Discard, repo.root)
		if err == nil {
			t.Fatal("a record naming browser assertions that did not run was accepted")
		}
		if !strings.Contains(err.Error(), "did not run in the last recorded invocation") {
			t.Fatalf("the refusal does not say the assertion did not run: %v", err)
		}
	})

	t.Run("android", func(t *testing.T) {
		repo := newFakeRepo(t)
		repo.writeRecord(t, nil)
		repo.writeAndroidResults(t, "AC12_labels_stay_short")
		err := CheckRecord(io.Discard, repo.root)
		if err == nil {
			t.Fatal("a record naming Android assertions that did not run was accepted")
		}
		if !strings.Contains(err.Error(), "AC17_stopped_reads_last_known") {
			t.Fatalf("the refusal does not name the assertion that did not run: %v", err)
		}
	})

	t.Run("a failed case does not count as having run", func(t *testing.T) {
		repo := newFakeRepo(t)
		repo.writeRecord(t, nil)
		dir := filepath.Join(repo.root, "android", "app", "build", "outputs", "androidTest-results", "connected")
		body := `<?xml version="1.0"?><testsuite><testcase name="AC17_stopped_reads_last_known">` +
			`<failure message="the counters still read as current"/></testcase></testsuite>`
		if err := os.WriteFile(filepath.Join(dir, "results.xml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		err := CheckRecord(io.Discard, repo.root)
		if err == nil {
			t.Fatal("a FAILED instrumented case was counted as having run")
		}
	})
}

// AC18 on BOTH surfaces, which is the hole impl-gate finding F1 named: an assertion may not be
// recorded as evidence unless the route also showed it going red. Before this, deleting, renaming or
// @Ignore-ing every `*_demonstration` case left the whole Android route green.
func TestAnAssertionThatWasNeverShownGoingRedIsRefused(t *testing.T) {
	t.Parallel()

	t.Run("android: not one demonstration ran", func(t *testing.T) {
		repo := newFakeRepo(t)
		repo.writeRecord(t, nil)
		repo.writeAndroidResults(t, claimsInTheCommittedRecord(t, AndroidSurface)...) // claims only
		err := CheckRecord(io.Discard, repo.root)
		if err == nil {
			t.Fatal("the record was accepted with twelve Android claims and no demonstration at all")
		}
		if !strings.Contains(err.Error(), "demonstration") {
			t.Fatalf("the refusal does not say a demonstration is missing: %v", err)
		}
	})

	t.Run("android: exactly one demonstration is missing, and it is named", func(t *testing.T) {
		repo := newFakeRepo(t)
		repo.writeRecord(t, nil)
		var names []string
		for _, c := range claimsInTheCommittedRecord(t, AndroidSurface) {
			names = append(names, c)
			if c != "AC17_stopped_reads_last_known" {
				names = append(names, c+DemonstrationSuffix)
			}
		}
		repo.writeAndroidResults(t, names...)
		err := CheckRecord(io.Discard, repo.root)
		if err == nil {
			t.Fatal("the record was accepted with one Android claim that was never shown going red")
		}
		if !strings.Contains(err.Error(), "AC17_stopped_reads_last_known") {
			t.Fatalf("the refusal does not name the claim with no demonstration: %v", err)
		}
	})

	t.Run("android: a FAILED demonstration does not count", func(t *testing.T) {
		repo := newFakeRepo(t)
		repo.writeRecord(t, nil)
		dir := filepath.Join(repo.root, "android", "app", "build", "outputs", "androidTest-results", "connected")
		var b strings.Builder
		b.WriteString(`<?xml version="1.0"?><testsuite>`)
		for _, c := range claimsInTheCommittedRecord(t, AndroidSurface) {
			b.WriteString(`<testcase name="` + c + `"/>`)
			b.WriteString(`<testcase name="` + c + DemonstrationSuffix + `">` +
				`<failure message="the mutated screen did not make the assertion fail"/></testcase>`)
		}
		b.WriteString(`</testsuite>`)
		if err := os.WriteFile(filepath.Join(dir, "results.xml"), []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := CheckRecord(io.Discard, repo.root); err == nil {
			t.Fatal("a FAILED demonstration was counted as evidence that the claim can go red")
		}
	})

	t.Run("browser: an assertion that ran but was not demonstrated", func(t *testing.T) {
		repo := newFakeRepo(t)
		repo.writeRecord(t, nil)
		var results []Result
		for _, id := range claimsInTheCommittedRecord(t, "browser map") {
			results = append(results, Result{ID: id, Passed: true, Demonstrated: id != "AC8-stale"})
		}
		if err := RecordRun(repo.root, "web", results); err != nil {
			t.Fatal(err)
		}
		err := CheckRecord(io.Discard, repo.root)
		if err == nil {
			t.Fatal("the record was accepted with a browser assertion that was never shown going red")
		}
		if !strings.Contains(err.Error(), "AC8-stale") {
			t.Fatalf("the refusal does not name the undemonstrated assertion: %v", err)
		}
	})
}

// The same count, at the ROUTE rather than at the record: AC18 says "WHEN either grading route runs
// ... SHALL fail", so `make verify-ui-android` must refuse on its own, without waiting for
// `make verify-ui-record` to be run afterwards.
func TestTheAndroidRouteCountsItsOwnDemonstrations(t *testing.T) {
	t.Parallel()

	t.Run("a green run passes", func(t *testing.T) {
		repo := newFakeRepo(t)
		repo.writeRecord(t, nil)
		if err := CheckAndroidRun(io.Discard, repo.root); err != nil {
			t.Fatalf("a run reporting every claim and every demonstration was refused: %v", err)
		}
	})

	t.Run("no demonstrations at all", func(t *testing.T) {
		repo := newFakeRepo(t)
		repo.writeRecord(t, nil)
		repo.writeAndroidResults(t, claimsInTheCommittedRecord(t, AndroidSurface)...)
		err := CheckAndroidRun(io.Discard, repo.root)
		if err == nil {
			t.Fatal("the Android route reported green with twelve claims and no demonstration")
		}
		if !strings.Contains(err.Error(), "never shown going red") {
			t.Fatalf("the refusal does not say the claims were never shown going red: %v", err)
		}
	})

	t.Run("a claim the record names did not run", func(t *testing.T) {
		repo := newFakeRepo(t)
		repo.writeRecord(t, nil)
		repo.writeAndroidResults(t, "AC12_labels_stay_short", "AC12_labels_stay_short"+DemonstrationSuffix)
		err := CheckAndroidRun(io.Discard, repo.root)
		if err == nil {
			t.Fatal("the Android route reported green having run one of the twelve claims")
		}
		if !strings.Contains(err.Error(), "AC17_stopped_reads_last_known") {
			t.Fatalf("the refusal does not name the claim that did not run: %v", err)
		}
	})

	t.Run("no results at all", func(t *testing.T) {
		repo := &fakeRepo{root: t.TempDir()}
		repo.writeRecord(t, nil)
		repo.writeParkedSuite(t, nil)
		err := CheckAndroidRun(io.Discard, repo.root)
		if err == nil {
			t.Fatal("the Android route reported green with no instrumented results at all")
		}
		if !strings.Contains(err.Error(), "verify-ui-android") {
			t.Fatalf("the refusal does not say the route had not run: %v", err)
		}
	})
}

// --- the deferral fence -------------------------------------------------------------------------
//
// A deferral is the weakest of the record's three dispositions: it answers a clause with the item
// that owns it rather than with evidence or with a reason no evidence is needed. That makes it the
// one an author could reach for to make a red clause stop being red, so every side of it is fenced
// and every fence is driven here. If any of these cases stops failing, "deferred" has become a way
// to write a clause off by editing a table.

const theSplitItem = "S0074-tracker-android-a11y-operability"

const f1AndroidRow = "| F1 | android screen | deferred to " + theSplitItem +
	": AC13_platform_checks_pass_light, AC13_platform_checks_pass_dark, AC13_no_state_by_colour_alone, AC14_operable_without_a_pointer | - |"

// ignoreOn puts a well-formed @Ignore on one case of the instrumented suite.
func ignoreOn(caseName, reason string) func(string) string {
	return func(s string) string {
		return strings.Replace(s,
			"    fun "+caseName+"(",
			"    @Ignore(\""+reason+"\")\n    fun "+caseName+"(", 1)
	}
}

func TestADeferralOnAPairTheSpecDoesNotSplitIsRefused(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo(t)
	repo.writeRecord(t, func(s string) string {
		return strings.Replace(s,
			"| F6 | android screen | AC17_stopped_reads_last_known | - |",
			"| F6 | android screen | deferred to "+theSplitItem+": AC17_stopped_reads_last_known | - |", 1)
	})
	err := CheckRecord(io.Discard, repo.root)
	if err == nil {
		t.Fatal("a clause was deferred on a pair the narrowed spec does not mark SPLIT, and the record was accepted")
	}
	if !strings.Contains(err.Error(), "only legal on the clause/surface pairs") {
		t.Fatalf("the refusal does not say the pair may not be deferred: %v", err)
	}
}

func TestADeferralNamingAnotherItemIsRefused(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo(t)
	repo.writeRecord(t, func(s string) string {
		return strings.Replace(s, "| F10 | android screen | deferred to "+theSplitItem+":",
			"| F10 | android screen | deferred to S0099-somebody-elses-item:", 1)
	})
	err := CheckRecord(io.Discard, repo.root)
	if err == nil {
		t.Fatal("a deferral pointing at an item that does not carry the clause was accepted")
	}
	if !strings.Contains(err.Error(), "this pair is carried by") {
		t.Fatalf("the refusal does not name the item that actually carries the pair: %v", err)
	}
}

func TestADeferralBesideAnotherDispositionIsRefused(t *testing.T) {
	t.Parallel()

	t.Run("beside an exemption", func(t *testing.T) {
		repo := newFakeRepo(t)
		repo.writeRecord(t, func(s string) string {
			return strings.Replace(s, f1AndroidRow,
				strings.TrimSuffix(f1AndroidRow, "- |")+"and also exempt, because reasons |", 1)
		})
		err := CheckRecord(io.Discard, repo.root)
		if err == nil {
			t.Fatal("a row carrying both a deferral and an exemption was accepted")
		}
		if !strings.Contains(err.Error(), "BOTH a deferral and an exemption") {
			t.Fatalf("the refusal does not name the double disposition: %v", err)
		}
	})

	t.Run("beside an assertion", func(t *testing.T) {
		repo := newFakeRepo(t)
		repo.writeRecord(t, func(s string) string {
			return strings.Replace(s,
				"| F10 | android screen | deferred to "+theSplitItem+":",
				"| F10 | android screen | AC12_nothing_clipped_at_360dp, deferred to "+theSplitItem+":", 1)
		})
		err := CheckRecord(io.Discard, repo.root)
		if err == nil {
			t.Fatal("a row claiming an assertion AND deferring the clause was accepted")
		}
		// The cell no longer opens with the deferral, so it parses as assertions; either refusal is
		// the right one, and what must not happen is silence.
		if !strings.Contains(err.Error(), "did not run") && !strings.Contains(err.Error(), "BOTH a deferral") {
			t.Fatalf("the refusal does not name the double disposition: %v", err)
		}
	})
}

func TestADeferralThatNamesNoAssertionIsRefused(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo(t)
	repo.writeRecord(t, func(s string) string {
		return strings.Replace(s, "| F10 | android screen | deferred to "+theSplitItem+
			": AC13_platform_checks_pass_light, AC13_platform_checks_pass_dark | - |",
			"| F10 | android screen | deferred to "+theSplitItem+": | - |", 1)
	})
	err := CheckRecord(io.Discard, repo.root)
	if err == nil {
		t.Fatal("a clause was deferred without naming what moved with it, and the record was accepted")
	}
	if !strings.Contains(err.Error(), "names no assertion that moved with it") {
		t.Fatalf("the refusal does not say the deferral pins nothing: %v", err)
	}
}

// A clause cannot be deferred AND graded. If the assertion is running green, the clause is answered
// here and the deferral is a lie about who owes the work.
func TestADeferredAssertionThatRanIsRefused(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo(t)
	repo.writeRecord(t, nil)
	names := withDemonstrations(claimsInTheCommittedRecord(t, AndroidSurface))
	names = append(names, "AC13_no_state_by_colour_alone", "AC13_no_state_by_colour_alone"+DemonstrationSuffix)
	repo.writeAndroidResults(t, names...)
	err := CheckRecord(io.Discard, repo.root)
	if err == nil {
		t.Fatal("a clause was deferred while the assertion carrying it ran green, and the record was accepted")
	}
	if !strings.Contains(err.Error(), "reports it PASSING") {
		t.Fatalf("the refusal does not say the deferred assertion actually ran: %v", err)
	}
}

// The two directions of the fence between the record and the suite.
func TestTheParkedSuiteAndTheRecordMustAgree(t *testing.T) {
	t.Parallel()

	t.Run("a case is ignored with no deferral behind it", func(t *testing.T) {
		repo := newFakeRepo(t)
		repo.writeRecord(t, nil)
		repo.writeParkedSuite(t,
			ignoreOn("AC17_stopped_reads_last_known", theSplitItem),
			ignoreOn("AC17_stopped_reads_last_known"+DemonstrationSuffix, theSplitItem))
		err := CheckRecord(io.Discard, repo.root)
		if err == nil {
			t.Fatal("a case stopped running with nothing in the record saying who owns the clause it carried")
		}
		if !strings.Contains(err.Error(), "and no row of") {
			t.Fatalf("the refusal does not say the ignored case is undeferred: %v", err)
		}
	})

	t.Run("a clause is deferred with no ignored case behind it", func(t *testing.T) {
		repo := newFakeRepo(t)
		repo.writeRecord(t, func(s string) string {
			return strings.Replace(s, f1AndroidRow,
				strings.Replace(f1AndroidRow, "AC14_operable_without_a_pointer",
					"AC14_operable_without_a_pointer, AC99_an_assertion_still_in_the_suite", 1), 1)
		})
		err := CheckRecord(io.Discard, repo.root)
		if err == nil {
			t.Fatal("a clause was written off while its assertion was still in the suite")
		}
		if !strings.Contains(err.Error(), "does not @Ignore it") {
			t.Fatalf("the refusal does not say the deferred assertion is still in the suite: %v", err)
		}
	})

	// The dodge AC18 exists to forbid, wearing a deferral as a disguise: park the demonstration and
	// leave the claim running, and the claim is green having never been shown able to go red.
	t.Run("only half a claim/demonstration pair is ignored", func(t *testing.T) {
		repo := newFakeRepo(t)
		repo.writeRecord(t, nil)
		repo.writeParkedSuite(t, ignoreOn("AC16_three_states_are_distinct"+DemonstrationSuffix, theSplitItem))
		err := CheckRecord(io.Discard, repo.root)
		if err == nil {
			t.Fatal("a demonstration was parked beside a claim that kept running, and the record was accepted")
		}
		if !strings.Contains(err.Error(), "but not") {
			t.Fatalf("the refusal does not name the half-parked pair: %v", err)
		}
	})

	t.Run("an ignored case names no owning item", func(t *testing.T) {
		repo := newFakeRepo(t)
		repo.writeRecord(t, nil)
		repo.writeParkedSuite(t,
			ignoreOn("AC16_three_states_are_distinct", "flaky on CI"),
			ignoreOn("AC16_three_states_are_distinct"+DemonstrationSuffix, "flaky on CI"))
		err := CheckRecord(io.Discard, repo.root)
		if err == nil {
			t.Fatal("a case was parked anonymously and the record was accepted")
		}
		if !strings.Contains(err.Error(), "without naming the item that owns it") {
			t.Fatalf("the refusal does not say the parked case has no owner: %v", err)
		}
	})

	// AC18 binds the ROUTE, not only the record check that may be run after it.
	t.Run("the android route refuses it too, on its own", func(t *testing.T) {
		repo := newFakeRepo(t)
		repo.writeRecord(t, nil)
		repo.writeParkedSuite(t,
			ignoreOn("AC15_counters_state_their_set", theSplitItem),
			ignoreOn("AC15_counters_state_their_set"+DemonstrationSuffix, theSplitItem))
		err := CheckAndroidRun(io.Discard, repo.root)
		if err == nil {
			t.Fatal("`make verify-ui-android` reported green with a case parked behind no deferral")
		}
		if !strings.Contains(err.Error(), "and no row of") {
			t.Fatalf("the route's refusal does not say the ignored case is undeferred: %v", err)
		}
	})
}

// AC20's last sentence: the one exemption in the whole record lapses the moment its reason stops
// being true.
func TestTheAndroidF11ExemptionLapsesWhenTheClientGainsAWebView(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo(t)
	repo.writeRecord(t, nil)
	if err := CheckRecord(io.Discard, repo.root); err != nil {
		t.Fatalf("the record should hold before the client gains a web view: %v", err)
	}

	repo.writeAndroidSource(t, "package com.nschatz.tracker\n\nimport android.webkit.WebView\n\nclass Help(val v: WebView)\n")
	err := CheckRecord(io.Discard, repo.root)
	if err == nil {
		t.Fatal("the F11 Android exemption still stood after the client gained a WebView")
	}
	if !strings.Contains(err.Error(), "exemption has lapsed") {
		t.Fatalf("the refusal does not say the exemption lapsed: %v", err)
	}
}

// A record check with no run records at all must refuse rather than pass over an empty set.
func TestNoRunRecordsAtAllIsRefused(t *testing.T) {
	t.Parallel()
	repo := &fakeRepo{root: t.TempDir()}
	repo.writeRecord(t, nil)
	repo.writeParkedSuite(t, nil)
	err := CheckRecord(io.Discard, repo.root)
	if err == nil {
		t.Fatal("the record check passed with no evidence that either route had ever run")
	}
	if !strings.Contains(err.Error(), "run record") && !strings.Contains(err.Error(), "results") {
		t.Fatalf("the refusal does not say the routes had not run: %v", err)
	}
}

// The repository explanation documents carry every claim that left a surface.
func TestTheExplanationDocumentsCarryEveryMovedClaim(t *testing.T) {
	t.Parallel()
	if err := CheckDocuments(io.Discard, filepath.Join("..", "..")); err != nil {
		t.Fatalf("%v", err)
	}
}

func TestAMissingExplanationSectionIsRefused(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	src := filepath.Join("..", "..", "internal", "server", "static")
	dst := filepath.Join(root, "internal", "server", "static")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"map-explained.html", "app-explained.html"} {
		body, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "app-explained.html" {
			body = []byte(strings.Replace(string(body), `id="collection_limitation"`, `id="something_else"`, 1))
		}
		if err := os.WriteFile(filepath.Join(dst, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	err := CheckDocuments(io.Discard, root)
	if err == nil {
		t.Fatal("a document missing a moved claim was accepted")
	}
	if !strings.Contains(err.Error(), "collection_limitation") {
		t.Fatalf("the refusal does not name the claim that went missing: %v", err)
	}
}

func removeLineContaining(s, needle string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, needle) {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
