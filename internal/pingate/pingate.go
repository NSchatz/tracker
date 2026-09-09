// Package pingate is the repo's supply-chain PIN gate: it reads every pinnable reference in the
// working tree and refuses one that does not name exactly what it will get.
//
// The normative text is the umbrella's `documentation/pinning-conventions.md`, decided by the
// operator on 2026-09-07, and every refusal below quotes the clause it breaks by name so the fix
// is a lookup rather than an argument:
//
//	P1  a container image is pinned by TAG AND DIGEST
//	P2  base images (`FROM`) follow the same rule
//	P3  actions are pinned to a commit SHA, version in a trailing comment
//	P4  dependency manifests are locked; node lifecycle scripts off unless opted back in
//	P6  there is never a fallback - a build that cannot get exactly what it pinned stops
//
// # Why it is shaped like internal/toolchain
//
// It is a package with self-tests, so it runs inside `make test`, therefore inside `make check`,
// therefore inside CI, with no second copy in `ci.yml` to drift away from the gate a human runs.
// `make pin-check` is the same code behind a command, for the case where you want the pin verdict
// alone: it needs no Docker daemon, no Android SDK, no credentials and no network.
//
// # It refuses to pass on nothing
//
// Every category records HOW MANY references it examined, and a category that examined zero is a
// refusal in its own right (AC-14), borrowed straight from `toolchain.CollectPins`: a check that
// silently passes because a file moved or a pattern stopped matching is worse than no check,
// because it reports a compliance it never looked for. The node category is the interesting case -
// this repo has no node in it, so "zero manifests" is the ANSWER, not an absence of looking, and
// the category says so explicitly rather than counting nothing.
//
// # It proves it can go red
//
// `RunDemonstrations` runs the same scanner over five committed trees under
// `internal/pingate/testdata/refusals`, one per shape the conventions forbid, and `CheckRepo`
// fails if fewer than five of them go red. Those trees are the ONE directory the repository scan
// skips, and the skip is itself asserted: the directory must hold exactly the five named cases and
// nothing else, so it cannot become a place to hide an unpinned reference from the check.
//
// # P7, honestly
//
// P7's failure-mode taxonomy - expired pin, unreachable upstream, digest mismatch, unusable
// archive, missing capability - was written for a build step that FETCHES a pinned artifact.
// This gate fetches nothing: it reads files. So what it owes P7 is the other half of that clause,
// "a pin failure names itself": every refusal names the file, the line, the offending reference
// and the broken clause.
package pingate

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
)

// ConventionsFile is the normative text. Every refusal points at it by path and by clause, because
// "this is unpinned" without a clause is an opinion and the clause makes it a rule.
const ConventionsFile = "documentation/pinning-conventions.md"

// Clause is one clause of ConventionsFile, carried into the failure message so a reader never has
// to guess which rule bit them.
type Clause struct {
	ID      string
	Summary string
}

// The clauses this gate enforces. P5 (pin to something the publisher keeps) and P8 (rot is
// discovered when a build fails) are deliberately NOT enforced here: P5 is a judgement about
// retention made when a pin is chosen and recorded in README.md, and P8 is satisfied by this gate
// existing inside the build rather than as a scheduled liveness workflow.
var (
	P1 = Clause{"P1", "a container image is pinned by tag AND digest"}
	P2 = Clause{"P2", "base images follow the same rule: a FROM line is an image reference like any other"}
	P3 = Clause{"P3", "actions are pinned to a commit SHA with the human-readable version in a trailing comment"}
	P4 = Clause{"P4", "dependency manifests are locked: lockfile committed, no latest, no unbounded range, node lifecycle scripts disabled unless the repo opts back in with a committed reason"}
	P6 = Clause{"P6", "there is never a fallback: a build that cannot get exactly what it pinned stops"}
)

// Violation is one reference that does not name what it will get, located precisely enough to fix
// without searching for it.
type Violation struct {
	File      string // path relative to the scanned root, slash-separated
	Line      int    // 1-based
	Reference string // the offending reference, verbatim
	Clause    Clause
	Why       string
}

// String is the refusal AC-12 requires: the file, the line number, the offending reference, and the
// clause of documentation/pinning-conventions.md it breaks, on one greppable line.
func (v Violation) String() string {
	return fmt.Sprintf("%s:%d: %s: breaks %s (%s) of %s: %s",
		v.File, v.Line, v.Reference, v.Clause.ID, v.Clause.Summary, ConventionsFile, v.Why)
}

// Category is one class of reference the gate examines, with the count that makes "nothing found"
// distinguishable from "nothing wrong".
type Category struct {
	Name     string
	Examined int
	Detail   string
}

// Report is the whole verdict for one scanned root.
type Report struct {
	Root       string
	Categories []Category
	Violations []Violation
}

// EmptyCategories are the categories that examined nothing. On the real repository each one is a
// refusal: the file it reads moved, or its pattern stopped matching, and either way the gate has
// quietly stopped looking at something it is supposed to guard.
func (r *Report) EmptyCategories() []Category {
	var empty []Category
	for _, c := range r.Categories {
		if c.Examined == 0 {
			empty = append(empty, c)
		}
	}
	return empty
}

// Examined totals every reference the scan actually looked at.
func (r *Report) Examined() int {
	n := 0
	for _, c := range r.Categories {
		n += c.Examined
	}
	return n
}

// Err is the verdict: every violation and every empty category, together. All findings are returned
// at once - fixing one and rediscovering the next on the following run is how a half-done pinning
// pass stays half done.
func (r *Report) Err() error {
	var problems []error
	for _, v := range r.Violations {
		problems = append(problems, errors.New(v.String()))
	}
	for _, c := range r.EmptyCategories() {
		problems = append(problems, fmt.Errorf(
			"category %q examined 0 references (%s): a pin check that has stopped looking is not a compliant tree, it is an unguarded one - fix the pattern or the path in internal/pingate, never the count",
			c.Name, c.Detail))
	}
	return errors.Join(problems...)
}

// WriteSummary prints what was examined. It runs on SUCCESS, because the only way to believe a
// green pin gate is to see the counts it reached that verdict with.
func (r *Report) WriteSummary(w io.Writer) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "category\texamined\tdetail\n")
	for _, c := range r.Categories {
		fmt.Fprintf(tw, "%s\t%d\t%s\n", c.Name, c.Examined, c.Detail)
	}
	tw.Flush()
}

// scanner is one category's reader. It appends to the report and returns an error only for an I/O
// failure it could not attribute - an unpinned reference is a Violation, not an error, and a file
// that is simply absent leaves its category empty, which the empty-category rule then refuses.
type scanner func(root string, r *Report) error

var scanners = []scanner{
	scanDockerfiles,
	scanComposeFiles,
	scanGoSources,
	scanWorkflows,
	scanGoModule,
	scanMakefile,
	scanGradleWrapper,
	scanGradleCatalogs,
	scanGradleScripts,
	scanNodeManifests,
}

// Scan reads every pinnable reference under root. root is a repository root or, for a
// demonstration, one of the deliberately broken trees under DemonstrationsDir.
func Scan(root string) (*Report, error) {
	r := &Report{Root: root}
	for _, s := range scanners {
		if err := s(root, r); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(r.Violations, func(i, j int) bool {
		if r.Violations[i].File != r.Violations[j].File {
			return r.Violations[i].File < r.Violations[j].File
		}
		return r.Violations[i].Line < r.Violations[j].Line
	})
	return r, nil
}

// CheckRepo is the whole gate as `make pin-check` and the package's own tests run it: the working
// tree is compliant, no category has stopped looking, the five committed refusals still go red,
// and the directory they live in still holds only them.
func CheckRepo(root string) error {
	var problems []error

	// The go.sum assertion below asks git whether the lockfile is tracked and un-ignored, which is
	// only answerable inside a work tree. Refuse rather than skip: a gate that silently drops half
	// an assertion because of where it was run is the failure mode this repo names by hand.
	if !isGitWorkTree(root) {
		problems = append(problems, fmt.Errorf(
			"%s is not a git work tree: the pin gate asserts that go.sum is TRACKED and not gitignored, which cannot be answered outside one, and skipping that assertion silently is not an option", root))
	}

	if err := verifyDemonstrationTree(root); err != nil {
		problems = append(problems, err)
	}

	report, err := Scan(root)
	if err != nil {
		problems = append(problems, err)
	} else if err := report.Err(); err != nil {
		problems = append(problems, fmt.Errorf("the working tree carries unpinned references:\n%w", err))
	}

	red, err := RunDemonstrations(root)
	if err != nil {
		problems = append(problems, err)
	}
	if red < len(Demonstrations) {
		problems = append(problems, fmt.Errorf(
			"only %d of %d committed refusal demonstrations went red: a pin gate that cannot be shown failing is not evidence of anything",
			red, len(Demonstrations)))
	}

	return errors.Join(problems...)
}

// ---------------------------------------------------------------------------
// tree walking
// ---------------------------------------------------------------------------

// skipDirs are never descended into. They hold third-party code, VCS metadata or build output -
// none of it this repository's pins to state. DemonstrationsDir is skipped separately, by path
// rather than by name, and that skip is asserted by verifyDemonstrationTree.
var skipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	"build":        true,
	".gradle":      true,
	".idea":        true,
}

// walkFiles returns every regular file under root whose relative slash path satisfies keep, sorted,
// so a failure list is stable between runs.
func walkFiles(root string, keep func(rel string) bool) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && path == root {
				return nil
			}
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == DemonstrationsDir || skipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if keep(rel) {
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking %s: %w", root, err)
	}
	sort.Strings(out)
	return out, nil
}

// readFile reports whether rel exists under root and, when it does, its bytes. A missing file is
// not an error here: an absent file leaves its category empty, and the empty-category rule is what
// turns that into a refusal with a name attached.
func readFile(root, rel string) ([]byte, bool, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("reading %s: %w", rel, err)
	}
	return data, true, nil
}

// readLines is readFile split into 1-based-addressable lines, which is what a refusal that must
// name a line number needs.
func readLines(root, rel string) ([]string, bool, error) {
	data, ok, err := readFile(root, rel)
	if err != nil || !ok {
		return nil, ok, err
	}
	return strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n"), true, nil
}

func exists(root, rel string) bool {
	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil
}

// ---------------------------------------------------------------------------
// git
// ---------------------------------------------------------------------------

func isGitWorkTree(root string) bool {
	git, err := exec.LookPath("git")
	if err != nil {
		return false
	}
	out, err := exec.Command(git, "-C", root, "rev-parse", "--is-inside-work-tree").Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

// gitTracked reports whether rel is committed to the index of the repository at root.
func gitTracked(root, rel string) bool {
	git, err := exec.LookPath("git")
	if err != nil {
		return false
	}
	return exec.Command(git, "-C", root, "ls-files", "--error-unmatch", "--", rel).Run() == nil
}

// gitIgnored reports whether rel is excluded by a .gitignore rule. `git check-ignore` exits 0 when
// the path IS ignored, 1 when it is not.
func gitIgnored(root, rel string) bool {
	git, err := exec.LookPath("git")
	if err != nil {
		return false
	}
	return exec.Command(git, "-C", root, "check-ignore", "-q", "--", rel).Run() == nil
}
