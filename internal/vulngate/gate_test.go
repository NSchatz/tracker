package vulngate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
//
// The cases at PACKAGE and MODULE level are not decoration. An earlier version of this gate read only
// govulncheck's `=== Symbol Results ===` section and silently passed two advisories the tool reported
// under the other two headings — one with a published fix nobody took. Those cases are the guard
// against that ever being true again.

// fakeRunner substitutes for govulncheck so the gate's verdict can be exercised against a synthetic
// report, with no network, no vulnerable dependency and no waiting.
type fakeRunner struct {
	res Result
	err error
}

func (f fakeRunner) Run(context.Context) (Result, error) { return f.res, f.err }

// finding describes one govulncheck finding for the stream builder below. The deepest field set
// decides the level the tool would have reported it at: a Function makes it symbol level, a Package
// makes it package level, neither makes it module level.
type finding struct {
	ID       string
	Fixed    string
	Module   string
	Version  string
	Package  string
	Function string
	Summary  string
}

// streamOf renders govulncheck's JSON stream for the given findings, in the shape the pinned tool
// emits: a config object, an osv record per advisory, then one finding object each.
func streamOf(findings ...finding) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)

	write := func(v any) {
		if err := enc.Encode(v); err != nil {
			panic(err)
		}
	}

	write(map[string]any{"config": map[string]any{
		"protocol_version": "v1.0.0",
		"scanner_name":     scannerName,
		"scanner_version":  "v1.1.4",
		"db":               "https://vuln.go.dev",
		"db_last_modified": "2026-09-02T19:12:04Z",
		"go_version":       "go1.26.8",
		"scan_level":       wantScanLevel,
		"scan_mode":        wantScanMode,
	}})

	seen := map[string]bool{}
	for _, f := range findings {
		if seen[f.ID] {
			continue
		}
		seen[f.ID] = true
		summary := f.Summary
		if summary == "" {
			summary = "an advisory against " + f.Module
		}
		write(map[string]any{"osv": map[string]any{"id": f.ID, "summary": summary}})
	}

	for _, f := range findings {
		frame := map[string]any{
			"module":  firstNonEmpty(f.Module, "example.com/mod"),
			"version": firstNonEmpty(f.Version, "v1.0.0"),
		}
		if f.Package != "" {
			frame["package"] = f.Package
		}
		if f.Function != "" {
			frame["function"] = f.Function
			frame["position"] = map[string]any{"filename": "mod.go", "line": 12, "column": 3}
		}
		body := map[string]any{"osv": f.ID, "trace": []any{frame}}
		if f.Fixed != "" {
			body["fixed_version"] = f.Fixed
		}
		write(map[string]any{"finding": body})
	}
	return b.String()
}

// symbolFinding is the shape most of these cases use: an advisory reached by a call path, with a
// published fix, which is what a repo normally sees.
func symbolFinding(id string) finding {
	return finding{ID: id, Fixed: "go1.25.13", Module: "stdlib", Version: "go1.25.12",
		Package: "net/http", Function: "ReadRequest"}
}

// noFixFinding is the ONE case C3 allows a record for: govulncheck reports it and names no fix.
func noFixFinding(id string) finding {
	return finding{ID: id, Module: "example.com/mod", Version: "v1.0.0",
		Package: "example.com/mod/legacy", Function: "Vulnerable"}
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
	g := Gate{Runner: fakeRunner{res: res, err: runErr}, SuppressionsPath: suppressionsPath, Out: &out}
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
	err := check(t, Result{Stdout: streamOf(symbolFinding("GO-2026-1234"))}, nil, path)
	if err == nil {
		t.Fatal("the gate PASSED an advisory that is neither remediated nor recorded; the suppression mechanism has stopped biting")
	}
	if !strings.Contains(err.Error(), "GO-2026-1234") {
		t.Errorf("failure does not name the advisory:\n%v", err)
	}
}

// govulncheck reports advisories at THREE depths and the gate's verdict covers all three. These two
// cases are the regression guard: each advisory is reported at exactly one level, with nothing at
// symbol level at all, which is precisely the report an earlier gate discarded before deciding.
func TestUnrecordedPackageLevelAdvisoryFailsTheGate(t *testing.T) {
	path := writeSuppressions(t, "[]\n")
	err := check(t, Result{Stdout: streamOf(finding{
		ID: "GO-2026-5158", Fixed: "v1.44.0",
		Module: "go.opentelemetry.io/otel", Version: "v1.43.0",
		Package: "go.opentelemetry.io/otel/baggage",
	})}, nil, path)
	if err == nil {
		t.Fatal("the gate PASSED a PACKAGE-level advisory: govulncheck reported a vulnerable package this module imports and the verdict did not include it")
	}
	if !strings.Contains(err.Error(), "GO-2026-5158") {
		t.Errorf("failure does not name the advisory:\n%v", err)
	}
	if !strings.Contains(err.Error(), "v1.44.0") {
		t.Errorf("failure does not name the fix that should have been taken:\n%v", err)
	}
}

func TestUnrecordedModuleLevelAdvisoryFailsTheGate(t *testing.T) {
	path := writeSuppressions(t, "[]\n")
	err := check(t, Result{Stdout: streamOf(finding{
		ID: "GO-2026-5932", Module: "golang.org/x/crypto", Version: "v0.56.0",
	})}, nil, path)
	if err == nil {
		t.Fatal("the gate PASSED a MODULE-level advisory: govulncheck reported a vulnerable module this repo requires and the verdict did not include it")
	}
	if !strings.Contains(err.Error(), "GO-2026-5932") {
		t.Errorf("failure does not name the advisory:\n%v", err)
	}
}

// The exact report that slipped through: no symbol-level finding at all, one package-level advisory
// with a published fix, one module-level advisory with none. Both must be answered.
func TestTheReportThatSlippedThroughNowFailsTheGate(t *testing.T) {
	path := writeSuppressions(t, "[]\n")
	err := check(t, Result{Stdout: streamOf(
		finding{ID: "GO-2026-5158", Fixed: "v1.44.0", Module: "go.opentelemetry.io/otel", Version: "v1.43.0", Package: "go.opentelemetry.io/otel/baggage"},
		finding{ID: "GO-2026-5932", Module: "golang.org/x/crypto", Version: "v0.56.0"},
	)}, nil, path)
	if err == nil {
		t.Fatal("the gate PASSED a report whose only advisories are below symbol level; that is the whole verdict being read in part")
	}
	for _, id := range []string{"GO-2026-5158", "GO-2026-5932"} {
		if !strings.Contains(err.Error(), id) {
			t.Errorf("failure does not name %s:\n%v", id, err)
		}
	}
}

func TestUnrecordedAdvisoryFailsWithNoSuppressionFileAtAll(t *testing.T) {
	missing := filepath.Join(t.TempDir(), SuppressionsFile)
	err := check(t, Result{Stdout: streamOf(symbolFinding("GO-2026-1234"))}, nil, missing)
	if err == nil {
		t.Fatal("an absent suppression file let a reported advisory through; absent means NO suppressions, not all of them")
	}
	if !strings.Contains(err.Error(), "GO-2026-1234") {
		t.Errorf("failure does not name the advisory:\n%v", err)
	}
}

func TestStaleRecordFailsTheGate(t *testing.T) {
	path := writeSuppressions(t, goodEntry)
	err := check(t, Result{Stdout: streamOf()}, nil, path)
	if err == nil {
		t.Fatal("the gate PASSED a suppression for an advisory the run does not report; a record may not outlive its advisory")
	}
	if !strings.Contains(err.Error(), "GO-2026-1234") {
		t.Errorf("failure does not name the stale id:\n%v", err)
	}
}

// C3/R4: a record covers an advisory with NO fix available anywhere, and nothing else. An advisory
// govulncheck itself says is fixed somewhere is remediated at source — bump the module, raise the
// toolchain — so a record for it is refused rather than honoured, however well-formed it is.
func TestRecordForAnAdvisoryThatHasAFixFailsTheGate(t *testing.T) {
	path := writeSuppressions(t, goodEntry)
	err := check(t, Result{Stdout: streamOf(symbolFinding("GO-2026-1234"))}, nil, path)
	if err == nil {
		t.Fatal("the gate HONOURED a record for an advisory govulncheck says is fixed in go1.25.13; R4 sends that case to the toolchain, never to the record file")
	}
	if !strings.Contains(err.Error(), "GO-2026-1234") {
		t.Errorf("failure does not name the advisory:\n%v", err)
	}
	if !strings.Contains(err.Error(), "go1.25.13") {
		t.Errorf("failure does not name the fix that makes the record illegal:\n%v", err)
	}
}

// The same rule one level down: the advisory is only reported against a MODULE, and still has a fix.
func TestRecordForAFixablePackageLevelAdvisoryFailsTheGate(t *testing.T) {
	path := writeSuppressions(t, goodEntry)
	err := check(t, Result{Stdout: streamOf(finding{
		ID: "GO-2026-1234", Fixed: "v1.44.0", Module: "example.com/mod", Version: "v1.43.0",
		Package: "example.com/mod/baggage",
	})}, nil, path)
	if err == nil {
		t.Fatal("the gate HONOURED a record for a package-level advisory that has a published fix")
	}
	if !strings.Contains(err.Error(), "v1.44.0") {
		t.Errorf("failure does not name the fix:\n%v", err)
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
			err := check(t, Result{Stdout: streamOf()}, nil, path)
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
	err := check(t, Result{Stdout: streamOf(noFixFinding("GO-2026-1234"))}, nil, path)
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
		err := check(t, Result{Stdout: "totally not a govulncheck report\n"}, nil, "")
		if err == nil {
			t.Fatal("the gate PASSED output it could not parse; an absent report is not a clean one")
		}
	})

	t.Run("the text report the JSON stream replaced", func(t *testing.T) {
		err := check(t, Result{Stdout: "=== Symbol Results ===\n\nNo vulnerabilities found.\n"}, nil, "")
		if err == nil {
			t.Fatal("the gate PASSED govulncheck's TEXT report; that report shows package and module advisories only under -show verbose, so reading it is reading part of the verdict")
		}
	})

	t.Run("a report with no config object", func(t *testing.T) {
		err := check(t, Result{Stdout: `{"progress":{"message":"scanning"}}` + "\n"}, nil, "")
		if err == nil {
			t.Fatal("the gate PASSED a stream that never said which tool, database or scan depth produced it")
		}
	})

	t.Run("findings the report cannot answer", func(t *testing.T) {
		stream := `{"config":{"scanner_name":"govulncheck","scan_level":"symbol","scan_mode":"source"}}` + "\n" +
			`{"finding":{"osv":"CVE-2026-1234","trace":[{"module":"example.com/m","version":"v1.0.0"}]}}` + "\n"
		err := check(t, Result{Stdout: stream}, nil, "")
		if err == nil {
			t.Fatal("the gate PASSED a finding whose id no record could ever cover")
		}
	})

	// The status contradicting the output: a perfectly clean JSON report, delivered with the text
	// mode's "vulnerabilities found" status. `-format json` exits 0 when it produced a report, so a
	// non-zero status here means the tool's conventions moved under the gate. Refuse, do not guess.
	t.Run("exit status contradicting its own output", func(t *testing.T) {
		err := check(t, Result{Stdout: streamOf(), ExitCode: 3}, nil, "")
		if err == nil {
			t.Fatal("the gate PASSED a clean report carried by a non-zero status; the tool's status conventions changed and the gate believed the half it liked")
		}
	})
}

// --- the gate stays green for the cases that are legitimately green ----------------------------

func TestCleanScanWithNoRecordsPasses(t *testing.T) {
	if err := check(t, Result{Stdout: streamOf()}, nil, "/nonexistent/.govulncheck-suppressions.yaml"); err != nil {
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
			if err := check(t, Result{Stdout: streamOf()}, nil, path); err != nil {
				t.Fatalf("a clean scan with %s failed: %v", name, err)
			}
		})
	}
}

// The one shape a record is FOR: govulncheck reports the advisory and names no fix for it anywhere.
func TestRecordedAdvisoryWithNoFixPasses(t *testing.T) {
	path := writeSuppressions(t, goodEntry)
	if err := check(t, Result{Stdout: streamOf(noFixFinding("GO-2026-1234"))}, nil, path); err != nil {
		t.Fatalf("a well-formed record for a reported advisory with no fix did not carry it: %v", err)
	}
}

// The same at module level, which is where a "no fix anywhere" advisory usually lands: the module is
// required, nothing imports the vulnerable package, and there is no release that fixes it.
func TestRecordedModuleLevelAdvisoryWithNoFixPasses(t *testing.T) {
	path := writeSuppressions(t, goodEntry)
	if err := check(t, Result{Stdout: streamOf(finding{
		ID: "GO-2026-1234", Module: "example.com/mod", Version: "v1.0.0",
	})}, nil, path); err != nil {
		t.Fatalf("a well-formed record for a module-level advisory with no fix did not carry it: %v", err)
	}
}

func TestPartialRecordingStillFails(t *testing.T) {
	// One recorded, one not. The recorded one must not launder the other.
	path := writeSuppressions(t, goodEntry)
	err := check(t, Result{Stdout: streamOf(noFixFinding("GO-2026-1234"), noFixFinding("GO-2026-9999"))}, nil, path)
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

// The whole verdict reaches the human, not just the part that decided it.
func TestTheReportIsEchoedWholeIncludingLevelsThatDidNotDecideIt(t *testing.T) {
	var out bytes.Buffer
	g := Gate{
		Runner: fakeRunner{res: Result{Stdout: streamOf(
			finding{ID: "GO-2026-5158", Fixed: "v1.44.0", Module: "go.opentelemetry.io/otel", Version: "v1.43.0", Package: "go.opentelemetry.io/otel/baggage"},
			finding{ID: "GO-2026-5932", Module: "golang.org/x/crypto", Version: "v0.56.0"},
		)}},
		SuppressionsPath: writeSuppressions(t, "[]\n"),
		Out:              &out,
	}
	_ = g.Check(context.Background())
	for _, want := range []string{"GO-2026-5158", "[package level]", "GO-2026-5932", "[module level]"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the echoed report does not carry %q:\n%s", want, out.String())
		}
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
