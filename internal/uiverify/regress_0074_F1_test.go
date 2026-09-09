package uiverify

// Regression artifact for impl-gate finding F1 of S0074-tracker-android-a11y-operability.
//
// AC14: "... and SHALL let the server URL and token be entered and saved to the same effect a touch
// has, with the effect asserted on what the screen then shows."
//
// The instrumented case AC14_operable_without_a_pointer types a valid URL and token, reaches the
// save control by directional navigation, activates it with DPAD_CENTER, and then asserts the
// effect. The predicate it asserts is
//
//	verdict.contains("Saved", ignoreCase = true) || verdict.contains("Not saved", ignoreCase = true)
//
// which is satisfied by BOTH of the two verdicts the screen can render: config_saved ("Saved") and
// config_not_saved ("Not saved") - the second disjunct says so out loud, and Kotlin's
// contains(ignoreCase = true) makes the first one say it as well, because "Not saved" contains
// "saved". So the claim passes whether the keyboard save SAVED or was REFUSED: the effect is not
// asserted, only the presence of some verdict.
//
// This test models the predicate exactly as the suite writes it and runs both verdicts through it.
// It goes GREEN when the predicate rejects the refusal verdict.
//
// It is a source scan for the same reason internal/uiverify already scans this file in
// unmutatedClaimProblems and parkedSuiteProblems: it grades no rendered property, it answers "what
// does this case assert", which is a fact about the suite.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	regress0074SaveAssertion = "saving from the keyboard produced"
	regress0074Contains      = regexp.MustCompile(`contains\("([^"]*)", ignoreCase = true\)`)
	regress0074AndroidString = func(name string) *regexp.Regexp {
		return regexp.MustCompile(`<string name="` + name + `">([^<]*)</string>`)
	}
)

func TestRegress0074F1KeyboardSaveEffectIsAsserted(t *testing.T) {
	root := regress0074RepoRoot(t)

	suite := regress0074Read(t, filepath.Join(root, parkedSuite))
	at := strings.Index(suite, regress0074SaveAssertion)
	if at < 0 {
		t.Fatalf("could not find the AC14 keyboard-save assertion (looked for %q) in %s; this artifact "+
			"models that assertion and cannot report on a suite that no longer carries it",
			regress0074SaveAssertion, parkedSuite)
	}
	end := strings.Index(suite[at:], "\n        )")
	if end < 0 {
		t.Fatalf("could not find the end of the AC14 keyboard-save assertion in %s", parkedSuite)
	}
	block := suite[at : at+end]

	needles := regress0074Contains.FindAllStringSubmatch(block, -1)
	if len(needles) == 0 {
		t.Fatalf("the AC14 keyboard-save assertion tests the verdict against nothing:\n%s", block)
	}

	strs := regress0074Read(t, filepath.Join(root, "android", "app", "src", "main", "res", "values", "strings.xml"))
	saved := regress0074String(t, strs, "config_saved")
	refused := regress0074String(t, strs, "config_not_saved") + ": the URL must be https"

	// The predicate as the suite writes it: an OR of case-insensitive substring tests.
	accepts := func(verdict string) bool {
		for _, m := range needles {
			if strings.Contains(strings.ToLower(verdict), strings.ToLower(m[1])) {
				return true
			}
		}
		return false
	}

	if !accepts(saved) {
		t.Errorf("the AC14 keyboard-save assertion rejects the SAVED verdict %q, so the claim could not "+
			"pass on the effect AC14 requires", saved)
	}
	if accepts(refused) {
		var quoted []string
		for _, m := range needles {
			quoted = append(quoted, `"`+m[1]+`"`)
		}
		t.Errorf(`AC14 requires the URL and token to be "entered and saved to the same effect a touch has, `+
			`with the effect asserted on what the screen then shows".

%s asserts the effect with:
    verdict.contains(%s, ignoreCase = true)  (OR-ed)

and that predicate ACCEPTS the refusal verdict %q as well as the saved verdict %q. The claim
AC14_operable_without_a_pointer therefore passes whether the keyboard save saved the configuration
or the screen refused it, so its green run is not evidence of the effect AC14 names. Kotlin's
contains(ignoreCase = true) also makes the first disjunct alone accept the refusal, because
%q contains %q.`,
			parkedSuite, strings.Join(quoted, ", "), refused, saved, refused, saved)
	}
}

func regress0074RepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find the repository root from the test's working directory")
		}
		dir = parent
	}
}

func regress0074Read(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(body)
}

func regress0074String(t *testing.T, xml, name string) string {
	t.Helper()
	m := regress0074AndroidString(name).FindStringSubmatch(xml)
	if m == nil {
		t.Fatalf("no android string resource named %q", name)
	}
	return m[1]
}
