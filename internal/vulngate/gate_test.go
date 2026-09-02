package vulngate

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// This file is the gate's SELF-TEST. It runs inside `make test`, which is a prerequisite of
// `make check-go` and therefore of `make check`.
//
// Its whole job is to prove the vulnerability gate can still be turned RED on demand. A suppression
// mechanism that has quietly stopped biting looks exactly like a repo with no vulnerabilities, and the
// only difference visible from outside is whether a deliberately unrecorded advisory fails. So every
// test below asserts a FAILURE and fails the build when the gate returns nil.

// fakeRunner substitutes for govulncheck so the gate's verdict can be exercised against a synthetic
// report, with no network, no vulnerable dependency and no waiting.
type fakeRunner struct {
	res Result
	err error
}

func (f fakeRunner) Run(context.Context) (Result, error) { return f.res, f.err }

// report renders a govulncheck text report naming ids, in the shape the pinned tool emits.
func report(ids ...string) string {
	var b strings.Builder
	b.WriteString("=== Symbol Results ===\n\n")
	if len(ids) == 0 {
		b.WriteString("No vulnerabilities found.\n")
		return b.String()
	}
	for i, id := range ids {
		b.WriteString("Vulnerability #" + strconv.Itoa(i+1) + ": " + id + "\n")
		b.WriteString("    some advisory title\n  Standard library\n    Found in: net/http@go1.25.12\n    Fixed in: net/http@go1.25.13\n\n")
	}
	return b.String()
}

func writeSuppressions(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), SuppressionsFile)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write suppressions: %v", err)
	}
	return path
}

func check(t *testing.T, res Result, runErr error, suppressionsPath string) error {
	t.Helper()
	if suppressionsPath == "" {
		suppressionsPath = filepath.Join(t.TempDir(), SuppressionsFile) // deliberately absent
	}
	var out bytes.Buffer
	g := Gate{Runner: fakeRunner{res: res, err: runErr}, SuppressionsPath: suppressionsPath, Report: &out}
	return g.Check(context.Background())
}

const goodEntry = `- id: GO-2026-1234
  reason: no fixed release exists for example.com/mod
  reachability: mod.Vulnerable is reached only from a test helper that never runs in production
  recorded: "2026-09-02"
`

// --- the gate turns red ------------------------------------------------------------------------

func TestUnrecordedAdvisoryFailsTheGate(t *testing.T) {
	path := writeSuppressions(t, "[]\n")
	err := check(t, Result{Stdout: report("GO-2026-1234"), ExitCode: 3}, nil, path)
	if err == nil {
		t.Fatal("the gate PASSED an advisory that is neither remediated nor recorded; the suppression mechanism has stopped biting")
	}
	if !strings.Contains(err.Error(), "GO-2026-1234") {
		t.Errorf("failure does not name the advisory:\n%v", err)
	}
}

func TestUnrecordedAdvisoryFailsWithNoSuppressionFileAtAll(t *testing.T) {
	missing := filepath.Join(t.TempDir(), SuppressionsFile)
	err := check(t, Result{Stdout: report("GO-2026-1234"), ExitCode: 3}, nil, missing)
	if err == nil {
		t.Fatal("an absent suppression file let a reported advisory through; absent means NO suppressions, not all of them")
	}
	if !strings.Contains(err.Error(), "GO-2026-1234") {
		t.Errorf("failure does not name the advisory:\n%v", err)
	}
}

func TestStaleRecordFailsTheGate(t *testing.T) {
	path := writeSuppressions(t, goodEntry)
	err := check(t, Result{Stdout: report(), ExitCode: 0}, nil, path)
	if err == nil {
		t.Fatal("the gate PASSED a suppression for an advisory the run does not report; a record may not outlive its advisory")
	}
	if !strings.Contains(err.Error(), "GO-2026-1234") {
		t.Errorf("failure does not name the stale id:\n%v", err)
	}
}

func TestMalformedRecordFailsTheGateEvenWhenTheScanIsClean(t *testing.T) {
	cases := map[string]string{
		"missing key": `- id: GO-2026-1234
  reason: no fix
  recorded: "2026-09-02"
`,
		"unknown key": `- id: GO-2026-1234
  reason: no fix
  reachability: unreachable
  recorded: "2026-09-02"
  until: forever
`,
		"bad id": `- id: CVE-2026-1234
  reason: no fix
  reachability: unreachable
  recorded: "2026-09-02"
`,
		"empty value": `- id: GO-2026-1234
  reason: ""
  reachability: unreachable
  recorded: "2026-09-02"
`,
		"bad date": `- id: GO-2026-1234
  reason: no fix
  reachability: unreachable
  recorded: yesterday
`,
		"duplicate id": goodEntry + goodEntry,
		"not a sequence": `id: GO-2026-1234
reason: no fix
`,
		"scalar entry": "- GO-2026-1234\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeSuppressions(t, body)
			// A CLEAN scan: a malformed record must fail on its own, not only on the day it is needed.
			err := check(t, Result{Stdout: report(), ExitCode: 0}, nil, path)
			if err == nil {
				t.Fatalf("the gate PASSED a malformed record (%s); malformed is never skipped and never honoured", name)
			}
			if !strings.Contains(err.Error(), SuppressionsFile) {
				t.Errorf("failure does not name the offending file:\n%v", err)
			}
		})
	}
}

func TestMalformedRecordIsNeverHonoured(t *testing.T) {
	// The same malformed record, on a day when its advisory IS reported. It must still fail: honouring
	// a record the validator rejected is the one outcome that would make the file a blanket ignore.
	path := writeSuppressions(t, `- id: GO-2026-1234
  reason: no fix
  reachability: unreachable
  recorded: "2026-09-02"
  scope: everything
`)
	err := check(t, Result{Stdout: report("GO-2026-1234"), ExitCode: 3}, nil, path)
	if err == nil {
		t.Fatal("a malformed record silently suppressed its advisory")
	}
}

func TestToolThatCouldNotRunFailsTheGate(t *testing.T) {
	t.Run("non-verdict exit status", func(t *testing.T) {
		err := check(t, Result{Stderr: "go: golang.org/x/vuln/cmd/govulncheck@v1.1.4: module lookup disabled", ExitCode: 1}, nil, "")
		if err == nil {
			t.Fatal("the gate PASSED a govulncheck that exited 1; a tool that did not run is not a tool that found nothing")
		}
		if !strings.Contains(err.Error(), "module lookup disabled") {
			t.Errorf("failure does not carry the tool's own error:\n%v", err)
		}
	})

	t.Run("process never started", func(t *testing.T) {
		err := check(t, Result{}, errors.New("exec: \"go\": executable file not found in $PATH"), "")
		if err == nil {
			t.Fatal("the gate PASSED when govulncheck could not be started at all")
		}
		if !strings.Contains(err.Error(), "executable file not found") {
			t.Errorf("failure does not carry the tool's own error:\n%v", err)
		}
	})

	t.Run("unparseable output", func(t *testing.T) {
		err := check(t, Result{Stdout: "totally not a govulncheck report\n", ExitCode: 0}, nil, "")
		if err == nil {
			t.Fatal("the gate PASSED output it could not parse; an absent report is not a clean one")
		}
	})

	t.Run("vulnerabilities found but no id readable", func(t *testing.T) {
		err := check(t, Result{Stdout: "=== Symbol Results ===\n\nsomething changed shape\n", ExitCode: 3}, nil, "")
		if err == nil {
			t.Fatal("the gate PASSED a vulnerable verdict whose ids it could not read")
		}
	})

	t.Run("exit 0 contradicting its own output", func(t *testing.T) {
		err := check(t, Result{Stdout: report("GO-2026-1234"), ExitCode: 0}, nil, "")
		if err == nil {
			t.Fatal("the gate PASSED a report that names advisories while claiming success")
		}
	})
}

// --- the gate stays green for the cases that are legitimately green ----------------------------

func TestCleanScanWithNoRecordsPasses(t *testing.T) {
	if err := check(t, Result{Stdout: report(), ExitCode: 0}, nil, "/nonexistent/.govulncheck-suppressions.yaml"); err != nil {
		t.Fatalf("a clean scan with no suppression file failed: %v", err)
	}
}

func TestEmptySequenceIsNotAnError(t *testing.T) {
	for name, body := range map[string]string{
		"explicit empty sequence": "[]\n",
		"comments only":           "# nothing to suppress today\n",
		"empty file":              "",
	} {
		t.Run(name, func(t *testing.T) {
			path := writeSuppressions(t, body)
			if err := check(t, Result{Stdout: report(), ExitCode: 0}, nil, path); err != nil {
				t.Fatalf("a clean scan with %s failed: %v", name, err)
			}
		})
	}
}

func TestRecordedAdvisoryPasses(t *testing.T) {
	path := writeSuppressions(t, goodEntry)
	if err := check(t, Result{Stdout: report("GO-2026-1234"), ExitCode: 3}, nil, path); err != nil {
		t.Fatalf("a well-formed record for a reported advisory did not carry it: %v", err)
	}
}

func TestPartialRecordingStillFails(t *testing.T) {
	// One recorded, one not. The recorded one must not launder the other.
	path := writeSuppressions(t, goodEntry)
	err := check(t, Result{Stdout: report("GO-2026-1234", "GO-2026-9999"), ExitCode: 3}, nil, path)
	if err == nil {
		t.Fatal("one record carried an advisory it does not name")
	}
	if !strings.Contains(err.Error(), "GO-2026-9999") {
		t.Errorf("failure does not name the unrecorded advisory:\n%v", err)
	}
	if strings.Contains(err.Error(), "GO-2026-1234") {
		t.Errorf("failure wrongly implicates the recorded advisory:\n%v", err)
	}
}

// --- the repo's own suppression file ------------------------------------------------------------

func TestRepoSuppressionFileIsWellFormed(t *testing.T) {
	entries, err := LoadSuppressions(filepath.Join("..", "..", SuppressionsFile))
	if err != nil {
		t.Fatalf("%s in this repo is malformed: %v", SuppressionsFile, err)
	}
	for _, e := range entries {
		t.Logf("recorded suppression %s (recorded %s)", e.ID, e.Recorded)
	}
}
