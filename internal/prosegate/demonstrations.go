package prosegate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DemonstrationsDir holds trees that are deliberately, committedly broken. They
// exist so the gate can be SHOWN going red rather than merely asserted to.
//
// The sweep skips every `testdata` directory, so these cannot red the
// repository they live in. That exclusion is nailed shut from the other side:
// verifyDemonstrationTree requires this directory to hold exactly the cases
// named in Demonstrations and nothing else, so it cannot quietly become a place
// to park a narrated file where the gate will not look.
const DemonstrationsDir = "internal/prosegate/testdata/refusals"

// A fixture is committed under a suffix no Go tool reads and materialised under
// the name the sweep has to see: `gofmt -l .` walks `testdata`, so a
// deliberately unparseable file committed as `.go` would be reported by
// `make fmt`.
//
// symlinkSuffix names the second shape. Its committed content is the link
// target rather than a file body, which is how a `.go` path that CANNOT be read
// by any process - root included - is committed as an ordinary reviewable text
// file.
const (
	committedSuffix = ".committed"
	symlinkSuffix   = ".symlink"
)

// Demonstration is one committed instance of a shape the gate must refuse, plus
// the refusal it has to produce. Matching on the category AND the file AND the
// reason is the point: "something went red" would pass while the gate refused
// the wrong thing for the wrong reason.
type Demonstration struct {
	Name     string
	Shape    string
	Category Category
	File     string

	// WhyContains is a substring of the refusal's own explanation, and it is
	// what makes a case evidence for THIS rule rather than for any rule that
	// happens to fire on the same file in the same category.
	WhyContains string
}

// Demonstrations are the four shapes the gate refuses on a tree. RecordDrift is
// the fifth category and is demonstrated in the suite rather than here: it is a
// disagreement between two committed statements, not a property of a tree.
var Demonstrations = []Demonstration{
	{
		Name:        "over-ceiling",
		Shape:       "an eligible file with more narration than code",
		Category:    OverCeiling,
		File:        "narrated.go",
		WhyContains: "of its counted lines are prose",
	},
	{
		Name:        "unparseable",
		Shape:       "a Go file inside the swept set that does not parse",
		Category:    Unparseable,
		File:        "broken.go",
		WhyContains: "neither clean nor skipped",
	},
	{
		Name:        "unreadable",
		Shape:       "a Go file the sweep expected to read and cannot",
		Category:    Unreadable,
		File:        "gone.go",
		WhyContains: "dropped from the denominator",
	},
	{
		Name:        "empty-sweep",
		Shape:       "a tree holding no Go file at all",
		Category:    EmptySweep,
		File:        "",
		WhyContains: "measured nothing",
	},
}

// CaseTree materialises one demonstration into a scratch tree and returns it
// with the cleanup to run afterwards.
func CaseTree(root string, d Demonstration) (string, func(), error) {
	src := filepath.Join(root, filepath.FromSlash(DemonstrationsDir), d.Name)
	tmp, err := os.MkdirTemp("", "prosegate-demo-"+d.Name+"-")
	if err != nil {
		return "", func() {}, fmt.Errorf("materialising demonstration %q: %w", d.Name, err)
	}
	cleanup := func() { os.RemoveAll(tmp) }

	err = filepath.WalkDir(src, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if e.IsDir() {
			return os.MkdirAll(filepath.Join(tmp, rel), 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.HasSuffix(rel, symlinkSuffix) {
			return os.Symlink(strings.TrimSpace(string(data)), filepath.Join(tmp, strings.TrimSuffix(rel, symlinkSuffix)))
		}
		return os.WriteFile(filepath.Join(tmp, strings.TrimSuffix(rel, committedSuffix)), data, 0o644)
	})
	if err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("materialising demonstration %q: %w", d.Name, err)
	}
	return tmp, cleanup, nil
}

// DemonstrationRun is what a sweep of the committed refusal trees produced.
type DemonstrationRun struct {
	Red   int
	Fired []string
}

// RunDemonstrations scans each committed refusal tree and reports which
// produced the refusal it exists to produce. The returned error names every
// case that did NOT, which is the failure that matters: a gate that has stopped
// biting looks exactly like a compliant repository.
func RunDemonstrations(root string, th Thresholds) (DemonstrationRun, error) {
	var problems []error
	var run DemonstrationRun
	fired := map[string]bool{}

	for _, d := range Demonstrations {
		caseRoot, cleanup, err := CaseTree(root, d)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		_, err = CheckRepo(caseRoot, th)
		cleanup()

		if matchingRefusal(RefusalsIn(err), d) != nil {
			run.Red++
			fired[string(d.Category)] = true
			continue
		}
		problems = append(problems, fmt.Errorf(
			"refusal demonstration %q (%s) did NOT produce a %s refusal naming %q and explaining %q - the gate has stopped catching this shape.\n  what it did produce: %s",
			d.Name, d.Shape, d.Category, d.File, d.WhyContains, describe(err)))
	}
	for c := range fired {
		run.Fired = append(run.Fired, c)
	}
	sort.Strings(run.Fired)
	if missing := missingCategories(run.Fired); len(missing) > 0 {
		problems = append(problems, fmt.Errorf(
			"no committed demonstration produced a refusal in category %s; a category that has stopped finding anything is a category that has stopped being a gate",
			strings.Join(missing, ", ")))
	}
	return run, errors.Join(problems...)
}

func matchingRefusal(refusals []Refusal, d Demonstration) *Refusal {
	for i := range refusals {
		r := refusals[i]
		if r.Category != d.Category {
			continue
		}
		if d.File != "" && filepath.ToSlash(r.File) != d.File {
			continue
		}
		if d.WhyContains != "" && !strings.Contains(r.Why, d.WhyContains) {
			continue
		}
		return &refusals[i]
	}
	return nil
}

func describe(err error) string {
	refusals := RefusalsIn(err)
	if len(refusals) == 0 {
		if err == nil {
			return "no refusal at all"
		}
		return err.Error()
	}
	parts := make([]string, 0, len(refusals))
	for _, r := range refusals {
		parts = append(parts, r.Error())
	}
	return "\n    " + strings.Join(parts, "\n    ")
}

// verifyDemonstrationTree closes the hole that skipping a directory would
// otherwise open. The excluded tree may hold the committed cases and nothing
// else: not a fifth case, not a narrated file parked where the gate does not
// look.
func verifyDemonstrationTree(root string) error {
	dir := filepath.Join(root, filepath.FromSlash(DemonstrationsDir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("reading the refusal demonstrations at %s: %w", DemonstrationsDir, err)
	}

	want := map[string]bool{}
	for _, d := range Demonstrations {
		want[d.Name] = true
	}

	var stray []string
	found := map[string]bool{}
	for _, e := range entries {
		switch {
		case !e.IsDir():
			if e.Name() != "README.md" {
				stray = append(stray, e.Name())
			}
		case want[e.Name()]:
			found[e.Name()] = true
		default:
			stray = append(stray, e.Name())
		}
	}

	var problems []error
	if len(stray) > 0 {
		sort.Strings(stray)
		problems = append(problems, fmt.Errorf(
			"%s is skipped by the sweep, so it may hold ONLY the %d committed refusal cases and their README.md; it also holds %s",
			DemonstrationsDir, len(Demonstrations), strings.Join(stray, ", ")))
	}
	for _, d := range Demonstrations {
		if d.WhyContains == "" {
			problems = append(problems, fmt.Errorf(
				"refusal demonstration %q declares no WhyContains, so ANY %s refusal would satisfy it, whichever rule produced it",
				d.Name, d.Category))
		}
		if !found[d.Name] {
			problems = append(problems, fmt.Errorf("refusal demonstration %q (%s) is not committed under %s", d.Name, d.Shape, DemonstrationsDir))
		}
	}
	return errors.Join(problems...)
}

// missingCategories are the refusal categories no committed demonstration
// covers. A category that has quietly stopped being exercised is a category
// that has quietly stopped being a gate.
func missingCategories(demonstrated []string) []string {
	covered := map[string]bool{}
	for _, c := range demonstrated {
		covered[c] = true
	}
	var missing []string
	for _, c := range Categories {
		if c == RecordDrift {
			continue
		}
		if !covered[string(c)] {
			missing = append(missing, string(c))
		}
	}
	return missing
}
