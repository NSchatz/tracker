package vulngate

import (
	"reflect"
	"strings"
	"testing"
)

// realReport is a verbatim excerpt of what the pinned govulncheck emitted in this repo before the
// remediation, kept so the parser is tested against the tool's actual output shape rather than a
// hand-drawn approximation of it.
const realReport = `=== Symbol Results ===

Vulnerability #1: GO-2026-6253
    moby/go-archive: Crafted tar archive can write outside the extraction
    directory in github.com/moby/go-archive
  More info: https://pkg.go.dev/vuln/GO-2026-6253
  Module: github.com/moby/go-archive
    Found in: github.com/moby/go-archive@v0.2.0
    Fixed in: github.com/moby/go-archive@v0.3.0
    Example traces found:
      #1: internal/secretscan/secretscan.go:84:25: secretscan.Scan calls filepath.WalkDir

Vulnerability #2: GO-2026-6218
    Avoid quadratic complexity in resolvePath in net/url
  More info: https://pkg.go.dev/vuln/GO-2026-6218
  Standard library
    Found in: net/url@go1.25.12
    Fixed in: net/url@go1.25.13

Your code is affected by 2 vulnerabilities from 1 module and the Go standard library.
This scan also found 2 vulnerabilities in packages you import and 3
vulnerabilities in modules you require, but your code doesn't appear to call
these vulnerabilities.
`

func TestParseReportReadsTheToolsOwnOutput(t *testing.T) {
	got, err := ParseReport(realReport)
	if err != nil {
		t.Fatalf("ParseReport: %v", err)
	}
	want := []string{"GO-2026-6218", "GO-2026-6253"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseReportOnACleanScan(t *testing.T) {
	got, err := ParseReport("=== Symbol Results ===\n\nNo vulnerabilities found.\n\nYour code is affected by 0 vulnerabilities.\n")
	if err != nil {
		t.Fatalf("ParseReport: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want no advisories", got)
	}
}

// A verbose run lists advisories the code does not call under later sections. The gate's verdict is
// about the symbol section only, so ids below the next section header must not be picked up — reading
// them would fail the gate for advisories govulncheck did not report against this code.
func TestParseReportIgnoresLaterSections(t *testing.T) {
	out := `=== Symbol Results ===

Vulnerability #1: GO-2026-1111
  Standard library

=== Package Results ===

Vulnerability #1: GO-2026-2222
  Module: example.com/other

=== Module Results ===

Vulnerability #1: GO-2026-3333
`
	got, err := ParseReport(out)
	if err != nil {
		t.Fatalf("ParseReport: %v", err)
	}
	want := []string{"GO-2026-1111"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseReportRefusesOutputThatIsNotAReport(t *testing.T) {
	for name, out := range map[string]string{
		"empty":            "",
		"go run failure":   "go: downloading golang.org/x/vuln v1.1.4\ngo: module lookup disabled\n",
		"truncated report": "Vulnerability #1: GO-2026-1111\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseReport(out); err == nil {
				t.Fatalf("ParseReport accepted %q as a report", out)
			} else if !strings.Contains(err.Error(), symbolResultsMarker) {
				t.Errorf("failure does not say what was missing: %v", err)
			}
		})
	}
}
