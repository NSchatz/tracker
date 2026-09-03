package vulngate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// The gate reads govulncheck's JSON stream rather than its text report, and it does so for one
// reason: the text report is written for a human and SEGMENTS the verdict into `=== Symbol Results
// ===`, `=== Package Results ===` and `=== Module Results ===`. Anything that reads one of those
// sections is reading part of the verdict, and C3/R5 says `.govulncheck-suppressions.yaml` is the
// only thing in this repo allowed to make govulncheck's verdict smaller. The JSON stream has no
// sections: every advisory, at whatever depth the tool traced it, arrives as a `finding` object. So
// this parser is level-agnostic by construction rather than by remembering to list the levels.
//
// The stream also carries what the text report only implies — `fixed_version` per finding, which is
// what R4 needs to refuse a record for an advisory that HAS a fix — and a `config` object that says
// which tool, which database and which scan depth produced it.

// scannerName is the only scanner whose stream this gate will read.
const scannerName = "govulncheck"

// wantScanLevel and wantScanMode are the DEEPEST scan govulncheck offers. A run that asked for less
// would report fewer advisories, which is exactly the narrowing R5 reserves to the suppression file,
// so a stream whose config says it asked for less is refused rather than believed.
const (
	wantScanLevel = "symbol"
	wantScanMode  = "source"
)

// Level is how deep govulncheck traced an advisory: it found the module in the graph, it found a
// vulnerable package imported, or it found a call path to a vulnerable symbol. All three are
// advisories govulncheck reports, and the gate's verdict covers all three.
type Level int

const (
	// LevelModule: the module is in the graph. No vulnerable package is imported.
	LevelModule Level = iota
	// LevelPackage: a vulnerable package is imported. No call path to a vulnerable symbol was found.
	LevelPackage
	// LevelSymbol: there is a call path from this repo's code to a vulnerable symbol.
	LevelSymbol
)

func (l Level) String() string {
	switch l {
	case LevelSymbol:
		return "symbol"
	case LevelPackage:
		return "package"
	default:
		return "module"
	}
}

// Advisory is one advisory govulncheck reported, collapsed to the deepest level it reported it at.
type Advisory struct {
	// ID is the OSV identifier, matching GO-[0-9]{4}-[0-9]+.
	ID string
	// Level is the deepest level any finding for this id reached.
	Level Level
	// Module is the module (or "stdlib") the advisory is against.
	Module string
	// Version is the version of that module currently in the graph.
	Version string
	// FixedVersion is the version govulncheck names as carrying the fix. EMPTY means govulncheck
	// reported no fix anywhere — the one case C3 allows a record for.
	FixedVersion string
	// Summary is the advisory's one-line description, for the human reading a red gate.
	Summary string
	// Trace is the call path govulncheck reported, entry point first, for the reachability argument
	// a C3 record has to make.
	Trace string
}

// HasFix reports whether govulncheck named a version carrying the fix. R4 forbids a record for one.
func (a Advisory) HasFix() bool { return a.FixedVersion != "" }

// Report is one govulncheck run, read whole.
type Report struct {
	ScannerVersion string
	DB             string
	DBLastModified string
	GoVersion      string
	ScanLevel      string
	ScanMode       string
	// Advisories is every advisory reported at every level, sorted by id.
	Advisories []Advisory
}

// streamMessage is one object of govulncheck's JSON stream. Exactly one field is set per object.
type streamMessage struct {
	Config  *streamConfig  `json:"config"`
	OSV     *streamOSV     `json:"osv"`
	Finding *streamFinding `json:"finding"`
}

type streamConfig struct {
	ScannerName    string `json:"scanner_name"`
	ScannerVersion string `json:"scanner_version"`
	DB             string `json:"db"`
	DBLastModified string `json:"db_last_modified"`
	GoVersion      string `json:"go_version"`
	ScanLevel      string `json:"scan_level"`
	ScanMode       string `json:"scan_mode"`
}

// streamOSV is the advisory record itself. govulncheck emits one for every advisory in the database
// that could touch a scanned module, so these are a LOOKUP for summaries, never the set of findings.
type streamOSV struct {
	ID      string `json:"id"`
	Summary string `json:"summary"`
}

type streamFinding struct {
	OSV          string  `json:"osv"`
	FixedVersion string  `json:"fixed_version"`
	Trace        []frame `json:"trace"`
}

// frame is one step of a finding's trace. The FIRST frame is the vulnerable thing; the last is this
// repo's entry point. A frame carrying a function is a symbol-level trace, one carrying only a
// package is package-level, one carrying only a module is module-level.
type frame struct {
	Module   string    `json:"module"`
	Version  string    `json:"version"`
	Package  string    `json:"package"`
	Function string    `json:"function"`
	Receiver string    `json:"receiver"`
	Position *position `json:"position"`
}

type position struct {
	Filename string `json:"filename"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
}

// ParseReport reads govulncheck's whole JSON stream into a Report.
//
// It fails rather than returning an empty report whenever the output is not one this gate can read
// end to end: a tool whose output cannot be parsed has not told us there are no vulnerabilities, and
// a stream with no config object is not a govulncheck report at all.
func ParseReport(stdout string) (*Report, error) {
	dec := json.NewDecoder(strings.NewReader(stdout))

	var cfg *streamConfig
	summaries := map[string]string{}
	deepest := map[string]Advisory{}
	var order []string

	for {
		var msg streamMessage
		err := dec.Decode(&msg)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("govulncheck's output is not the JSON stream this gate reads (%w); an output that cannot be parsed is never read as a clean report", err)
		}

		switch {
		case msg.Config != nil:
			cfg = msg.Config
		case msg.OSV != nil:
			summaries[msg.OSV.ID] = msg.OSV.Summary
		case msg.Finding != nil:
			a, err := advisoryOf(*msg.Finding)
			if err != nil {
				return nil, err
			}
			prev, seen := deepest[a.ID]
			if !seen {
				order = append(order, a.ID)
				deepest[a.ID] = a
				continue
			}
			// govulncheck emits a finding per level for the same advisory. Keep the deepest trace,
			// and keep a fixed version wherever it appeared: an advisory has a fix if ANY finding
			// for it names one, and R4 turns on that answer.
			if a.Level > prev.Level {
				if a.FixedVersion == "" {
					a.FixedVersion = prev.FixedVersion
				}
				deepest[a.ID] = a
			} else if prev.FixedVersion == "" && a.FixedVersion != "" {
				prev.FixedVersion = a.FixedVersion
				deepest[a.ID] = prev
			}
		}
	}

	if cfg == nil {
		return nil, errors.New("govulncheck's output carries no config object, so it is not a report this gate can read; treating an absent report as a clean one is how a gate goes quiet")
	}
	if cfg.ScannerName != scannerName {
		return nil, fmt.Errorf("the report was produced by scanner %q, not %q; the gate reads govulncheck's own verdict and nothing else's", cfg.ScannerName, scannerName)
	}
	if cfg.ScanLevel != wantScanLevel {
		return nil, fmt.Errorf("govulncheck scanned at level %q, not %q; a run that asks for less than the deepest level reports fewer advisories, and %s is the only thing in this repo allowed to make the verdict smaller", cfg.ScanLevel, wantScanLevel, SuppressionsFile)
	}
	if cfg.ScanMode != wantScanMode {
		return nil, fmt.Errorf("govulncheck scanned in mode %q, not %q; a binary scan cannot see the call paths a source scan does, and a narrower verdict is not this gate's to take", cfg.ScanMode, wantScanMode)
	}

	report := &Report{
		ScannerVersion: cfg.ScannerVersion,
		DB:             cfg.DB,
		DBLastModified: cfg.DBLastModified,
		GoVersion:      cfg.GoVersion,
		ScanLevel:      cfg.ScanLevel,
		ScanMode:       cfg.ScanMode,
	}
	sort.Strings(order)
	for _, id := range order {
		a := deepest[id]
		a.Summary = summaries[id]
		report.Advisories = append(report.Advisories, a)
	}
	return report, nil
}

// IDs is every reported advisory id, sorted.
func (r *Report) IDs() []string {
	ids := make([]string, 0, len(r.Advisories))
	for _, a := range r.Advisories {
		ids = append(ids, a.ID)
	}
	return ids
}

// String renders the whole report for the human looking at a gate that just went red. Every advisory
// appears with the level it was reported at, so nothing the tool said is invisible here.
func (r *Report) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "govulncheck %s scanned %s (level %s, mode %s) against %s, database last modified %s\n",
		r.ScannerVersion, r.GoVersion, r.ScanLevel, r.ScanMode, r.DB, r.DBLastModified)
	if len(r.Advisories) == 0 {
		b.WriteString("no advisories reported at any level: no vulnerable symbol called, no vulnerable package imported, no vulnerable module required\n")
		return b.String()
	}
	fmt.Fprintf(&b, "%d advisory(ies) reported across symbol, package and module level:\n", len(r.Advisories))
	for _, a := range r.Advisories {
		fix := "NO FIX AVAILABLE anywhere"
		if a.HasFix() {
			fix = "fixed in " + a.FixedVersion
		}
		fmt.Fprintf(&b, "  %s  [%s level]  %s@%s  %s\n", a.ID, a.Level, a.Module, a.Version, fix)
		if a.Summary != "" {
			fmt.Fprintf(&b, "      %s\n", a.Summary)
		}
		if a.Trace != "" {
			fmt.Fprintf(&b, "      trace: %s\n", a.Trace)
		}
		fmt.Fprintf(&b, "      https://pkg.go.dev/vuln/%s\n", a.ID)
	}
	return b.String()
}

// advisoryOf turns one finding into an Advisory, refusing a finding whose id could never be matched
// to a record: an id outside the pattern the suppression file validates against is an id no C3 entry
// could ever cover, so the gate would be permanently unanswerable rather than red.
func advisoryOf(f streamFinding) (Advisory, error) {
	if !reAdvisoryID.MatchString(f.OSV) {
		return Advisory{}, fmt.Errorf("govulncheck reported a finding for %q, which is not an OSV id matching GO-[0-9]{4}-[0-9]+; no record could ever be written for it, so the report is refused rather than partly read", f.OSV)
	}
	if len(f.Trace) == 0 {
		return Advisory{}, fmt.Errorf("govulncheck reported %s with an empty trace, so neither the module it implicates nor its depth can be read", f.OSV)
	}
	vulnerable := f.Trace[0]
	return Advisory{
		ID:           f.OSV,
		Level:        levelOf(f.Trace),
		Module:       vulnerable.Module,
		Version:      firstNonEmpty(vulnerable.Version, "(no version)"),
		FixedVersion: f.FixedVersion,
		Trace:        renderTrace(f.Trace),
	}, nil
}

func levelOf(trace []frame) Level {
	level := LevelModule
	for _, f := range trace {
		switch {
		case f.Function != "":
			if LevelSymbol > level {
				level = LevelSymbol
			}
		case f.Package != "":
			if LevelPackage > level {
				level = LevelPackage
			}
		}
	}
	return level
}

// renderTrace prints the trace entry point FIRST, the way a reader follows it: this repo's code on
// the left, the vulnerable symbol on the right.
func renderTrace(trace []frame) string {
	if len(trace) == 0 {
		return ""
	}
	if levelOf(trace) == LevelModule {
		// Nothing to trace: the module is merely required. That absence IS the reachability argument
		// a record would make, so say it rather than printing a one-element chain.
		return "required by the module graph; no vulnerable package imported and no call path found"
	}
	names := make([]string, 0, len(trace))
	for i := len(trace) - 1; i >= 0; i-- {
		names = append(names, frameName(trace[i]))
	}
	line := strings.Join(names, " -> ")
	if entry := trace[len(trace)-1]; entry.Position != nil && entry.Position.Filename != "" {
		line = fmt.Sprintf("%s:%d:%d: %s", entry.Position.Filename, entry.Position.Line, entry.Position.Column, line)
	}
	return line
}

func frameName(f frame) string {
	pkg := shortPackage(f.Package)
	switch {
	case f.Function != "" && f.Receiver != "":
		return pkg + "." + f.Receiver + "." + f.Function
	case f.Function != "":
		return pkg + "." + f.Function
	case f.Package != "":
		return f.Package
	default:
		return f.Module
	}
}

func shortPackage(path string) string {
	if path == "" {
		return ""
	}
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}
