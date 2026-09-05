package vulngate

import (
	"reflect"
	"strings"
	"testing"
)

// realStream is a verbatim excerpt of the JSON stream the pinned govulncheck emitted in this repo,
// trimmed to the config object and the findings. It carries all THREE levels at once — a symbol
// trace, a package finding and a module finding — because the whole point of reading the stream
// instead of the text report is that the gate's verdict does not depend on which section govulncheck
// prints a thing under.
const realStream = `{
  "config": {
    "protocol_version": "v1.0.0",
    "scanner_name": "govulncheck",
    "scanner_version": "v1.1.4",
    "db": "https://vuln.go.dev",
    "db_last_modified": "2026-09-02T19:12:04Z",
    "go_version": "go1.26.8",
    "scan_level": "symbol",
    "scan_mode": "source"
  }
}
{
  "osv": {
    "id": "GO-2026-5932",
    "summary": "The golang.org/x/crypto/openpgp package is unmaintained, unsafe by design, and has known security issues"
  }
}
{
  "osv": {
    "id": "GO-2026-5158",
    "summary": "Opentelemetry-go's baggage parsing no longer caps raw header length in go.opentelemetry.io/otel"
  }
}
{
  "finding": {
    "osv": "GO-2026-5158",
    "fixed_version": "v1.44.0",
    "trace": [
      {
        "module": "go.opentelemetry.io/otel",
        "version": "v1.43.0"
      }
    ]
  }
}
{
  "finding": {
    "osv": "GO-2026-5932",
    "trace": [
      {
        "module": "golang.org/x/crypto",
        "version": "v0.56.0"
      }
    ]
  }
}
{
  "finding": {
    "osv": "GO-2026-5158",
    "fixed_version": "v1.44.0",
    "trace": [
      {
        "module": "go.opentelemetry.io/otel",
        "version": "v1.43.0",
        "package": "go.opentelemetry.io/otel/baggage"
      }
    ]
  }
}
{
  "finding": {
    "osv": "GO-2026-6253",
    "fixed_version": "v0.3.0",
    "trace": [
      {
        "module": "github.com/moby/go-archive",
        "version": "v0.2.0",
        "package": "github.com/moby/go-archive/compression",
        "function": "CompressStream",
        "position": {"filename": "compression/compression.go", "line": 176, "column": 6}
      },
      {
        "module": "github.com/NSchatz/tracker",
        "package": "github.com/NSchatz/tracker/internal/secretscan",
        "function": "Scan",
        "position": {"filename": "internal/secretscan/secretscan.go", "line": 84, "column": 25}
      }
    ]
  }
}
`

// cleanStream is what a repo with nothing to answer produces: a config object and no findings.
const cleanStream = `{"config":{"scanner_name":"govulncheck","scanner_version":"v1.1.4","db":"https://vuln.go.dev","go_version":"go1.26.8","scan_level":"symbol","scan_mode":"source"}}
{"progress":{"message":"Scanning your code and 57 packages across 1 dependent module for known vulnerabilities..."}}
`

func TestParseReportReadsEveryLevelTheToolReports(t *testing.T) {
	report, err := ParseReport(realStream)
	if err != nil {
		t.Fatalf("ParseReport: %v", err)
	}

	if got, want := report.IDs(), []string{"GO-2026-5158", "GO-2026-5932", "GO-2026-6253"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ids: got %v, want %v", got, want)
	}

	byID := map[string]Advisory{}
	for _, a := range report.Advisories {
		byID[a.ID] = a
	}

	// The advisory govulncheck reported twice — once as a module finding, once as a package finding —
	// collapses to the DEEPEST level, and keeps the fixed version that appeared on either.
	pkgLevel := byID["GO-2026-5158"]
	if pkgLevel.Level != LevelPackage {
		t.Errorf("GO-2026-5158: got %s level, want package", pkgLevel.Level)
	}
	if pkgLevel.FixedVersion != "v1.44.0" {
		t.Errorf("GO-2026-5158: got fixed version %q, want v1.44.0", pkgLevel.FixedVersion)
	}
	if !pkgLevel.HasFix() {
		t.Error("GO-2026-5158 has a published fix and the report says it does not; R4 turns on this answer")
	}

	modLevel := byID["GO-2026-5932"]
	if modLevel.Level != LevelModule {
		t.Errorf("GO-2026-5932: got %s level, want module", modLevel.Level)
	}
	if modLevel.HasFix() {
		t.Errorf("GO-2026-5932: got fixed version %q, want none — govulncheck reported `Fixed in: N/A`", modLevel.FixedVersion)
	}
	if modLevel.Module != "golang.org/x/crypto" || modLevel.Version != "v0.56.0" {
		t.Errorf("GO-2026-5932: got %s@%s, want golang.org/x/crypto@v0.56.0", modLevel.Module, modLevel.Version)
	}
	if !strings.Contains(modLevel.Summary, "openpgp") {
		t.Errorf("GO-2026-5932: summary lost: %q", modLevel.Summary)
	}

	symLevel := byID["GO-2026-6253"]
	if symLevel.Level != LevelSymbol {
		t.Errorf("GO-2026-6253: got %s level, want symbol", symLevel.Level)
	}
	// The trace reads entry point first, so a reachability argument can be written straight off it.
	if !strings.Contains(symLevel.Trace, "secretscan.Scan -> compression.CompressStream") {
		t.Errorf("GO-2026-6253: trace does not read entry point first: %q", symLevel.Trace)
	}
	if !strings.Contains(symLevel.Trace, "internal/secretscan/secretscan.go:84:25") {
		t.Errorf("GO-2026-6253: trace drops the position: %q", symLevel.Trace)
	}
}

// THE regression this gate was refuted for: a package-level advisory alone, with nothing at symbol
// level. The tool reports it; the gate's verdict must contain it.
func TestParseReportSeesAPackageLevelAdvisoryOnItsOwn(t *testing.T) {
	report, err := ParseReport(streamOf(finding{ID: "GO-2026-5158", Fixed: "v1.44.0", Module: "go.opentelemetry.io/otel", Version: "v1.43.0", Package: "go.opentelemetry.io/otel/baggage"}))
	if err != nil {
		t.Fatalf("ParseReport: %v", err)
	}
	if got, want := report.IDs(), []string{"GO-2026-5158"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("a package-level advisory was dropped from the verdict: got %v, want %v", got, want)
	}
	if report.Advisories[0].Level != LevelPackage {
		t.Errorf("got %s level, want package", report.Advisories[0].Level)
	}
}

// The same, one level shallower: a module-level advisory alone.
func TestParseReportSeesAModuleLevelAdvisoryOnItsOwn(t *testing.T) {
	report, err := ParseReport(streamOf(finding{ID: "GO-2026-5932", Module: "golang.org/x/crypto", Version: "v0.56.0"}))
	if err != nil {
		t.Fatalf("ParseReport: %v", err)
	}
	if got, want := report.IDs(), []string{"GO-2026-5932"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("a module-level advisory was dropped from the verdict: got %v, want %v", got, want)
	}
	if report.Advisories[0].Level != LevelModule {
		t.Errorf("got %s level, want module", report.Advisories[0].Level)
	}
}

func TestParseReportOnACleanScan(t *testing.T) {
	report, err := ParseReport(cleanStream)
	if err != nil {
		t.Fatalf("ParseReport: %v", err)
	}
	if len(report.Advisories) != 0 {
		t.Errorf("got %v, want no advisories", report.IDs())
	}
	if report.GoVersion != "go1.26.8" || report.ScannerVersion != "v1.1.4" {
		t.Errorf("the config was not read: %+v", report)
	}
	if !strings.Contains(report.String(), "no advisories reported at any level") {
		t.Errorf("a clean report does not say so:\n%s", report.String())
	}
}

func TestReportStringNamesEveryAdvisoryAndItsLevel(t *testing.T) {
	report, err := ParseReport(realStream)
	if err != nil {
		t.Fatalf("ParseReport: %v", err)
	}
	rendered := report.String()
	for _, want := range []string{
		"GO-2026-5158", "[package level]", "fixed in v1.44.0",
		"GO-2026-5932", "[module level]", "NO FIX AVAILABLE",
		"GO-2026-6253", "[symbol level]",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the rendered report does not carry %q:\n%s", want, rendered)
		}
	}
}

func TestParseReportRefusesOutputThatIsNotAReport(t *testing.T) {
	cases := map[string]struct{ out, wants string }{
		"empty":                   {"", "no config object"},
		"tool download failure":   {"go: downloading golang.org/x/vuln v1.1.4\ngo: module lookup disabled\n", "not the JSON stream"},
		"text report":             {"=== Symbol Results ===\n\nNo vulnerabilities found.\n", "not the JSON stream"},
		"truncated stream":        {`{"config":{"scanner_name":"govulncheck"`, "not the JSON stream"},
		"findings with no config": {`{"finding":{"osv":"GO-2026-5932","trace":[{"module":"golang.org/x/crypto","version":"v0.56.0"}]}}` + "\n", "no config object"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseReport(tc.out)
			if err == nil {
				t.Fatalf("ParseReport accepted %q as a report", tc.out)
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("failure does not say what was wrong (want %q): %v", tc.wants, err)
			}
		})
	}
}

// A run that asked govulncheck for LESS than the deepest scan reports fewer advisories. That is the
// narrowing C3/R5 reserves to the suppression file, so the gate refuses the stream rather than
// believing a verdict taken at a shallower depth.
func TestParseReportRefusesANarrowedScan(t *testing.T) {
	for name, cfg := range map[string]string{
		"module-level scan":  `{"config":{"scanner_name":"govulncheck","scan_level":"module","scan_mode":"source"}}`,
		"package-level scan": `{"config":{"scanner_name":"govulncheck","scan_level":"package","scan_mode":"source"}}`,
		"binary scan":        `{"config":{"scanner_name":"govulncheck","scan_level":"symbol","scan_mode":"binary"}}`,
		"another scanner":    `{"config":{"scanner_name":"something-else","scan_level":"symbol","scan_mode":"source"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseReport(cfg + "\n"); err == nil {
				t.Fatalf("ParseReport accepted a narrowed scan: %s", cfg)
			}
		})
	}
}

func TestParseReportRefusesAFindingItCouldNeverAnswer(t *testing.T) {
	for name, body := range map[string]string{
		"id outside the OSV pattern": `{"finding":{"osv":"CVE-2026-1234","trace":[{"module":"example.com/m","version":"v1.0.0"}]}}`,
		"no id at all":               `{"finding":{"trace":[{"module":"example.com/m","version":"v1.0.0"}]}}`,
		"empty trace":                `{"finding":{"osv":"GO-2026-1234","trace":[]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			stream := `{"config":{"scanner_name":"govulncheck","scan_level":"symbol","scan_mode":"source"}}` + "\n" + body + "\n"
			if _, err := ParseReport(stream); err == nil {
				t.Fatalf("ParseReport accepted a finding no record could ever cover: %s", body)
			}
		})
	}
}
