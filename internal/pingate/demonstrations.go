package pingate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DemonstrationsDir is the ONE path the repository scan does not descend into, and the only one.
//
// The five trees under it are deliberately, committedly broken: they exist so the gate can be SHOWN
// going red rather than merely asserted to. They therefore cannot sit in the tree the gate grades,
// or the gate would refuse its own repository forever. Excluding a directory is normally a hole,
// so this one is nailed shut from the other side: verifyDemonstrationTree requires it to contain
// exactly the cases named in Demonstrations and nothing else, which means it cannot quietly become
// a place to park an unpinned reference where the check will not look.
//
// It lives under `testdata/` on purpose too: the go tool ignores that directory entirely, so a
// deliberately broken Dockerfile or package.json in there is inert to every other tool in the repo.
const DemonstrationsDir = "internal/pingate/testdata/refusals"

// Demonstration is one committed instance of a shape the conventions forbid, plus the refusal it
// must produce. Matching on the clause AND the file AND the reason is the point: "something went
// red" would pass even if the gate red-flagged the wrong thing for the wrong reason.
type Demonstration struct {
	Name   string // directory under DemonstrationsDir
	Shape  string // the forbidden shape, in words
	Clause Clause // the clause the refusal must cite
	File   string // the file inside the case the refusal must name

	// WhyContains is a substring of the refusal's own explanation, and it is what makes a
	// demonstration evidence for THIS rule rather than for any rule that happens to fire on the
	// same file under the same clause. The node case is why it exists: that fixture has no .npmrc
	// AND no lockfile beside it, so it earns two P4 refusals naming package.json, and matching on
	// the clause and the file alone would keep the case green if the lifecycle-scripts rule went
	// quiet tomorrow. A demonstration that cannot tell which rule bit it is not a demonstration.
	WhyContains string

	// CommittedAs maps the name a fixture file is COMMITTED under to the name the scanner has to
	// see. Exactly one case needs it, and for a reason worth the machinery: a file committed as
	// `package.json` would make THIS repository a node repository. `git ls-files '*package.json'`
	// would find it, and tracker's truthful answer to "is there any node here" is no - which is
	// the whole basis on which the node category asserts absence. So the manifest is committed
	// inert, under a name nothing reads, and materialised into a scratch tree at check time. The
	// artefact stays committed and reviewable; the claim stays true.
	CommittedAs map[string]string
}

// Demonstrations are the five shapes. They are the five the spec names, and the count is asserted:
// a sixth case, or a missing one, fails the gate rather than silently narrowing what it proves.
var Demonstrations = []Demonstration{
	{
		Name:        "dockerfile-tag-only",
		Shape:       "a Dockerfile FROM carrying a tag and no digest",
		Clause:      P2,
		File:        "Dockerfile",
		WhyContains: "takes BOTH halves",
	},
	{
		Name:        "compose-image-no-digest",
		Shape:       "a compose image: for a service the file does not build, with no digest",
		Clause:      P1,
		File:        "docker-compose.yml",
		WhyContains: "so it pulls a published image",
	},
	{
		Name:        "workflow-mutable-tag",
		Shape:       "a workflow uses: naming a mutable tag instead of a commit SHA",
		Clause:      P3,
		File:        ".github/workflows/ci.yml",
		WhyContains: "names a tag or a branch",
	},
	{
		Name:        "manifest-dynamic-version",
		Shape:       "a dependency manifest carrying a dynamic version",
		Clause:      P4,
		File:        "android/gradle/libs.versions.toml",
		WhyContains: "Gradle's dynamic version",
	},
	{
		Name:        "node-lifecycle-scripts",
		Shape:       "a node manifest with lifecycle scripts enabled and no committed reason",
		Clause:      P4,
		File:        "package.json",
		CommittedAs: map[string]string{"package.json.committed": "package.json"},
		WhyContains: "npm runs dependency lifecycle scripts by default",
	},
}

// CaseTree returns the directory to scan for one demonstration, and the cleanup to run afterwards.
// For four of the five that is the committed directory itself. For the node case it is a scratch
// copy with the manifest under the name npm would read - see Demonstration.CommittedAs.
func CaseTree(root string, d Demonstration) (string, func(), error) {
	src := filepath.Join(root, filepath.FromSlash(DemonstrationsDir), d.Name)
	if len(d.CommittedAs) == 0 {
		return src, func() {}, nil
	}

	tmp, err := os.MkdirTemp("", "pingate-demo-"+d.Name+"-")
	if err != nil {
		return "", func() {}, fmt.Errorf("materialising demonstration %q: %w", d.Name, err)
	}
	cleanup := func() { os.RemoveAll(tmp) }

	err = filepath.WalkDir(src, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if renamed, ok := d.CommittedAs[filepath.ToSlash(rel)]; ok {
			rel = filepath.FromSlash(renamed)
		}
		dst := filepath.Join(tmp, rel)
		if e.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	})
	if err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("materialising demonstration %q: %w", d.Name, err)
	}
	return tmp, cleanup, nil
}

// RunDemonstrations scans each committed refusal tree and reports how many produced the refusal
// they exist to produce. The returned error names every case that did NOT, which is the failure
// that matters: a gate that has stopped biting looks exactly like a compliant repository.
func RunDemonstrations(root string) (int, error) {
	var problems []error
	red := 0

	for _, d := range Demonstrations {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(DemonstrationsDir), d.Name)); err != nil {
			problems = append(problems, fmt.Errorf("refusal demonstration %q (%s) is missing from %s: %v", d.Name, d.Shape, DemonstrationsDir, err))
			continue
		}

		caseRoot, cleanup, err := CaseTree(root, d)
		if err != nil {
			problems = append(problems, err)
			continue
		}

		report, err := Scan(caseRoot)
		cleanup()
		if err != nil {
			problems = append(problems, fmt.Errorf("scanning refusal demonstration %q: %w", d.Name, err))
			continue
		}

		// Only the violations are graded, never the empty categories: a case tree deliberately
		// holds ONE broken shape, so most categories are legitimately empty there. The
		// empty-category rule is about the repository, where an empty category means the gate
		// stopped looking at something that exists.
		if matched := matchingViolation(report.Violations, d); matched != nil {
			red++
			continue
		}
		problems = append(problems, fmt.Errorf(
			"refusal demonstration %q (%s) did NOT produce a %s refusal naming %s and explaining %q - the gate has stopped catching this shape.\n  violations it did produce: %s",
			d.Name, d.Shape, d.Clause.ID, d.File, d.WhyContains, describeViolations(report.Violations)))
	}

	return red, errors.Join(problems...)
}

// matchingViolation finds the refusal a demonstration exists to produce. All three of the clause,
// the file and the reason must match: a case whose fixture happens to break a second rule in the
// same file under the same clause would otherwise stay green after the rule it demonstrates went
// quiet, and the gate would go on reporting five red demonstrations while proving four.
func matchingViolation(violations []Violation, d Demonstration) *Violation {
	for i := range violations {
		if violations[i].Clause.ID != d.Clause.ID || violations[i].File != d.File {
			continue
		}
		if d.WhyContains != "" && !strings.Contains(violations[i].Why, d.WhyContains) {
			continue
		}
		return &violations[i]
	}
	return nil
}

func describeViolations(violations []Violation) string {
	if len(violations) == 0 {
		return "none at all"
	}
	parts := make([]string, 0, len(violations))
	for _, v := range violations {
		parts = append(parts, v.String())
	}
	return "\n    " + strings.Join(parts, "\n    ")
}

// verifyDemonstrationTree closes the hole that excluding a directory from the scan would otherwise
// open. The excluded tree may hold the five committed cases and nothing else - not a sixth case, not
// a stray Dockerfile, not a real build input parked somewhere the gate does not look.
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
			// The one file allowed here is the README that says what this directory is for and why
			// nothing may be fixed in it. Naming it explicitly keeps the allowance from widening.
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
			"%s is the one directory the pin scan skips, so it may hold ONLY the %d committed refusal cases and their README.md; it also holds %s. Anything parked there is invisible to the gate, which is exactly what the exclusion must not become",
			DemonstrationsDir, len(Demonstrations), strings.Join(stray, ", ")))
	}
	for _, d := range Demonstrations {
		if d.WhyContains == "" {
			problems = append(problems, fmt.Errorf(
				"refusal demonstration %q declares no WhyContains, so ANY %s refusal naming %s would satisfy it, whichever rule produced it; name a substring of the explanation the rule under demonstration writes",
				d.Name, d.Clause.ID, d.File))
		}
		if !found[d.Name] {
			problems = append(problems, fmt.Errorf("refusal demonstration %q (%s) is not committed under %s", d.Name, d.Shape, DemonstrationsDir))
			continue
		}
		// A materialised case is only a demonstration while the file it materialises FROM is
		// actually committed. Without this, deleting the fixture would turn the case into an empty
		// directory that quietly stops proving anything.
		for committed, as := range d.CommittedAs {
			path := filepath.Join(dir, d.Name, filepath.FromSlash(committed))
			if _, err := os.Stat(path); err != nil {
				problems = append(problems, fmt.Errorf(
					"refusal demonstration %q materialises %s as %s, but %s is not committed: %v",
					d.Name, committed, as, committed, err))
			}
		}
	}
	return errors.Join(problems...)
}
