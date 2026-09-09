package uiverify

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// RecordFile is the committed record AC20 checks: every clause of the frontend conventions, for each
// of the two surfaces, mapped to the rendered assertion that proves it or to a named exemption.
const RecordFile = "FRONTEND-CONVENTIONS-RECORD.md"

// runsDir holds what each route actually ran, written by the route itself. It is build output, not a
// committed artefact: a record that named an assertion nobody had run would be a claim, and the
// point of AC20 is that the record cannot become one.
const runsDir = "build/uiverify"

// Surfaces are the two user interfaces tracker ships. Every clause is answered on both.
var Surfaces = []string{"browser map", "android screen"}

// AndroidSurface is the record's name for the emulator-graded surface.
const AndroidSurface = "android screen"

// DemonstrationSuffix pairs an instrumented claim with the case that shows it going red.
//
// AC18 wants "the same measuring code shown going red against a surface mutated to break exactly one
// claim". On the browser surface that pairing is a struct field ([Check].Mutation) and the count is
// enforced in [Summarise]. On the Android surface the two halves are separate JUnit cases, so the
// NAME is the pairing: `X` is the claim and `X_demonstration` is the same assertion re-run against a
// mutated screen, passing only when the assertion failed there. That convention is load-bearing, not
// decorative - [CheckAndroidRun] and [CheckRecord] both refuse a claim with no passing demonstration
// beside it.
const DemonstrationSuffix = "_demonstration"

// Clauses are the frontend conventions, F1 through F11.
var Clauses = []string{"F1", "F2", "F3", "F4", "F5", "F6", "F7", "F8", "F9", "F10", "F11"}

// RunRecord is what one invocation of a grading route did.
type RunRecord struct {
	Surface      string    `json:"surface"`
	At           time.Time `json:"at"`
	Ran          []string  `json:"ran"`
	Demonstrated []string  `json:"demonstrated"`
}

// RecordRun writes what the browser route just ran, so the record check can refuse a record naming
// an assertion that did not.
func RecordRun(root, surface string, results []Result) error {
	rec := RunRecord{Surface: surface, At: time.Now().UTC()}
	for _, r := range results {
		if r.Passed {
			rec.Ran = append(rec.Ran, r.ID)
		}
		if r.Demonstrated {
			rec.Demonstrated = append(rec.Demonstrated, r.ID)
		}
	}
	dir := filepath.Join(root, runsDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "last-run-"+surface+".json"), append(b, '\n'), 0o644)
}

// recordRow is one clause/surface pair as the committed record spells it.
type recordRow struct {
	Clause     string
	Surface    string
	Assertions []string
	Exemption  string
	Line       int
}

var rowPattern = regexp.MustCompile(`^\|\s*(F\d+)\s*\|\s*([^|]+?)\s*\|\s*([^|]*?)\s*\|\s*([^|]*?)\s*\|\s*$`)

// joinRecordPath locates the committed record beneath a repository root.
func joinRecordPath(root string) string { return filepath.Join(root, RecordFile) }

// ParseRecord reads the committed record.
func ParseRecord(path string) ([]recordRow, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rows []recordRow
	for i, line := range strings.Split(string(body), "\n") {
		m := rowPattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		row := recordRow{Clause: m[1], Surface: strings.ToLower(strings.TrimSpace(m[2])), Line: i + 1}
		for _, a := range strings.Split(m[3], ",") {
			a = strings.TrimSpace(strings.Trim(a, "`"))
			if a != "" && a != "-" {
				row.Assertions = append(row.Assertions, a)
			}
		}
		ex := strings.TrimSpace(m[4])
		if ex != "-" && ex != "" {
			row.Exemption = ex
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// CheckRecord is AC20. It refuses a record that is incomplete, doubly mapped, or claiming an
// assertion that did not run in the last recorded invocation of its route — and it refuses the F11
// Android exemption the moment the app gains a web view.
//
// It also carries AC18's no-vacuous-pass count into the record, on BOTH surfaces: an assertion the
// record names must not only have RUN, it must have been shown going red. Without that, a suite that
// lost every demonstration would still be reported green here, and a check that was never shown
// going red is not evidence.
func CheckRecord(w io.Writer, root string) error {
	path := joinRecordPath(root)
	rows, err := ParseRecord(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", RecordFile, err)
	}
	fmt.Fprintf(w, "record:  %s (%d rows)\n", RecordFile, len(rows))

	ran, err := assertionsThatRan(root)
	if err != nil {
		return err
	}
	for _, surface := range Surfaces {
		run := ran[surface]
		fmt.Fprintf(w, "ran:     %s -> %d assertions, %d demonstrated\n", surface, len(run.Ran), len(run.Demonstrated))
	}

	byPair := map[string]recordRow{}
	var problems []string
	for _, row := range rows {
		key := row.Clause + "|" + row.Surface
		if _, dup := byPair[key]; dup {
			problems = append(problems, fmt.Sprintf("%s on the %s is recorded twice (line %d)", row.Clause, row.Surface, row.Line))
			continue
		}
		byPair[key] = row
	}

	for _, clause := range Clauses {
		for _, surface := range Surfaces {
			key := clause + "|" + surface
			row, ok := byPair[key]
			if !ok {
				problems = append(problems, fmt.Sprintf("%s on the %s is not in the record at all", clause, surface))
				continue
			}
			switch {
			case len(row.Assertions) == 0 && row.Exemption == "":
				problems = append(problems, fmt.Sprintf("%s on the %s (line %d) names neither an assertion nor an exemption", clause, surface, row.Line))
			case len(row.Assertions) > 0 && row.Exemption != "":
				problems = append(problems, fmt.Sprintf("%s on the %s (line %d) carries BOTH an assertion and an exemption", clause, surface, row.Line))
			}
			run := ran[surface]
			for _, a := range row.Assertions {
				if !run.Ran[a] {
					problems = append(problems, fmt.Sprintf(
						"%s on the %s (line %d) names the assertion %q, which did not run in the last recorded invocation of that route",
						clause, surface, row.Line, a))
					continue
				}
				if !run.Demonstrated[a] {
					problems = append(problems, fmt.Sprintf(
						"%s on the %s (line %d) names the assertion %q, which ran but carries no demonstration in the last recorded invocation of that route (AC18): a check that was never shown going red is not evidence%s",
						clause, surface, row.Line, a, demonstrationHint(surface, a)))
				}
			}
		}
	}

	// The one exemption in the whole record lapses the moment its reason stops being true.
	if row, ok := byPair["F11|android screen"]; ok && row.Exemption != "" {
		if found, where, err := androidRendersAWebView(root); err != nil {
			problems = append(problems, "could not check whether the Android client renders a web view: "+err.Error())
		} else if found {
			problems = append(problems, fmt.Sprintf(
				"the F11 Android exemption stands on the app having no browser surface, but %s renders one; the exemption has lapsed and a policy is now required", where))
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("the F1-F11 record does not hold:\n  - %s", strings.Join(problems, "\n  - "))
	}
	fmt.Fprintf(w, "\nrecord:  %d clause/surface pairs, each mapped exactly once, every assertion ran\n", len(Clauses)*len(Surfaces))
	return nil
}

// demonstrationHint says, in the route's own vocabulary, what a missing demonstration would look
// like, so the refusal names something the reader can go and find.
func demonstrationHint(surface, assertion string) string {
	if surface == AndroidSurface {
		return fmt.Sprintf(" (the instrumented suite reported no passing case named %q)", assertion+DemonstrationSuffix)
	}
	return ""
}

// surfaceRun is what the last recorded invocation of one grading route actually did on one surface:
// which assertions ran green, and which of those were also shown going RED against a surface mutated
// to break exactly that claim.
type surfaceRun struct {
	Ran          map[string]bool
	Demonstrated map[string]bool
}

func newSurfaceRun() surfaceRun {
	return surfaceRun{Ran: map[string]bool{}, Demonstrated: map[string]bool{}}
}

// assertionsThatRan reads what each route last did: the browser route's own run record, and the
// instrumented suite's JUnit results, which are the emulator's own report of what executed.
func assertionsThatRan(root string) (map[string]surfaceRun, error) {
	out := map[string]surfaceRun{
		"browser map":  newSurfaceRun(),
		AndroidSurface: newSurfaceRun(),
	}

	webPath := filepath.Join(root, runsDir, "last-run-web.json")
	body, err := os.ReadFile(webPath)
	if err != nil {
		return nil, fmt.Errorf("no browser-route run record at %s: run `make verify-ui` before the record check, so the record is checked against what actually ran (%w)", webPath, err)
	}
	var rec RunRecord
	if err := json.Unmarshal(body, &rec); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", webPath, err)
	}
	for _, id := range rec.Ran {
		out["browser map"].Ran[id] = true
	}
	for _, id := range rec.Demonstrated {
		out["browser map"].Demonstrated[id] = true
	}

	android, err := androidRun(root)
	if err != nil {
		return nil, err
	}
	out[AndroidSurface] = android
	return out, nil
}

type junitSuite struct {
	XMLName   xml.Name `xml:"testsuite"`
	TestCases []struct {
		Name    string `xml:"name,attr"`
		Class   string `xml:"classname,attr"`
		Failure *struct {
			Message string `xml:"message,attr"`
		} `xml:"failure"`
		Skipped *struct{} `xml:"skipped"`
	} `xml:"testcase"`
}

// androidRun reads the connected-test results the emulator produced and splits them into the
// rendered claims that ran green and the demonstrations that ran green.
//
// A skipped or failed case counts as neither: F2's whole point is that a clause is proved by the
// runtime that drew it, and a case the emulator skipped drew nothing. A demonstration case passes
// exactly when the claim's assertion FAILED against the mutated screen, so a green
// `X_demonstration` is the emulator's own report that `X` can go red.
func androidRun(root string) (surfaceRun, error) {
	out := newSurfaceRun()
	dir := filepath.Join(root, "android", "app", "build", "outputs", "androidTest-results", "connected")
	entries, err := os.Stat(dir)
	if err != nil || !entries.IsDir() {
		return out, fmt.Errorf("no instrumented-test results under %s: the emulator has not reported what it ran, so nothing on the %s can be counted. "+
			"Run `make verify-ui-android`; that route needs a booted Android emulator and refuses loudly rather than skipping when there is none", dir, AndroidSurface)
	}
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".xml") {
			return nil
		}
		body, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		var suite junitSuite
		if xerr := xml.Unmarshal(body, &suite); xerr != nil {
			return nil // not a JUnit file
		}
		for _, tc := range suite.TestCases {
			if tc.Failure != nil || tc.Skipped != nil {
				continue
			}
			if claim, ok := strings.CutSuffix(tc.Name, DemonstrationSuffix); ok {
				out.Demonstrated[claim] = true
				continue
			}
			out.Ran[tc.Name] = true
		}
		return nil
	})
	return out, err
}

// androidRendersAWebView is the trigger that makes the one exemption in the record lapse.
//
// This is a SOURCE scan and that is deliberate: it is not grading a rendered property (F2's rule),
// it is answering "does this client contain a browser surface at all", which is a fact about the
// code and about the dependency set. It errs toward finding one: a false positive costs a record
// row, a false negative would leave a browser surface with no policy.
func androidRendersAWebView(root string) (bool, string, error) {
	needles := []string{"WebView", "CustomTabsIntent", "androidx.browser", "webkit"}
	var found string
	err := filepath.WalkDir(filepath.Join(root, "android"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "build" || d.Name() == ".gradle" {
				return fs.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(p)
		if ext != ".kt" && ext != ".java" && ext != ".xml" && ext != ".kts" && ext != ".toml" {
			return nil
		}
		body, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		for _, n := range needles {
			if strings.Contains(string(body), n) {
				found = p + " (contains " + n + ")"
				return fs.SkipAll
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return false, "", err
	}
	return found != "", found, nil
}

// --- the repository documents the surfaces link to ----------------------------------------------

// documentedStrings are the Android string resources whose paragraphs moved off the screen. Each one
// must exist as an explanation in tracker's own repository documentation, or the claim was dropped
// rather than moved (F8: "the claims are not dropped; they move").
var documentedStrings = []string{
	"permission_step_foreground_body",
	"permission_step_background_dialog_body",
	"permission_step_background_settings_body",
	"permission_step_denied_body",
	"permission_ready_body",
	"warning_foreground_only",
	"warning_approximate",
	"warning_notifications_blocked",
	"config_plaintext_note",
	"collection_limitation",
	"counter_delivered",
	"counter_queued",
	"counter_dropped",
	"counter_not_recorded",
	"collection_freshness",
}

// CheckDocuments asserts the explanation documents exist and carry every claim that left a surface.
func CheckDocuments(w io.Writer, root string) error {
	var problems []string
	for _, doc := range []string{
		filepath.Join("internal", "server", "static", "map-explained.html"),
		filepath.Join("internal", "server", "static", "app-explained.html"),
	} {
		p := filepath.Join(root, doc)
		body, err := os.ReadFile(p)
		if err != nil {
			problems = append(problems, doc+" is missing: "+err.Error())
			continue
		}
		if len(body) < 1000 {
			problems = append(problems, fmt.Sprintf("%s is %d bytes; that is not the explanation the labels' claims moved into", doc, len(body)))
		}
		fmt.Fprintf(w, "doc:     %s (%d bytes)\n", doc, len(body))
	}

	appDoc, err := os.ReadFile(filepath.Join(root, "internal", "server", "static", "app-explained.html"))
	if err == nil {
		for _, id := range documentedStrings {
			if !strings.Contains(string(appDoc), `id="`+id+`"`) {
				problems = append(problems, fmt.Sprintf("app-explained.html carries no section for %q; that claim left the screen without landing anywhere", id))
			}
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("the repository explanation documents do not hold:\n  - %s", strings.Join(problems, "\n  - "))
	}
	fmt.Fprintln(w, "docs:    both explanation documents present and complete")
	return nil
}
