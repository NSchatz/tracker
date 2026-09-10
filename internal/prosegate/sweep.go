package prosegate

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// skipDirs are the directory names the sweep never descends into. `testdata` is
// inert to the go tool and holds fixtures nobody writes for a reader; `vendor`
// is somebody else's source.
var skipDirs = map[string]bool{
	".git":     true,
	"testdata": true,
	"vendor":   true,
}

// FileReport is one measured file. A file below the minimum is reported in the
// table and excluded from the band and the ceiling: a twelve-line file with a
// four-line doc comment is a third prose and says nothing.
type FileReport struct {
	Path string
	Count
	Eligible bool
}

// Report is one sweep.
type Report struct {
	Root       string
	Thresholds Thresholds
	Files      []FileReport
	Generated  []string
	Listing    string
}

// Measure sweeps a tree and counts every Go file in it. It refuses rather than
// scoring a file it could not read or parse, and refuses a sweep that measured
// nothing at all: an empty sweep and a clean tree produce the same silence, and
// only one of them is good news.
func Measure(root string, th Thresholds) (*Report, error) {
	paths, listing, err := listGoFiles(root)
	if err != nil {
		return nil, err
	}

	report := &Report{Root: root, Thresholds: th, Listing: listing}
	var problems []error
	for _, rel := range paths {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			problems = append(problems, Refusal{Category: Unreadable, File: rel,
				Why: fmt.Sprintf("cannot be read, so it cannot be counted and will not be dropped from the denominator instead: %v", err)})
			continue
		}
		count, generated, err := CountSource(rel, src)
		if err != nil {
			problems = append(problems, Refusal{Category: Unparseable, File: rel,
				Why: fmt.Sprintf("cannot be parsed, so it is neither clean nor skipped: %v", err)})
			continue
		}
		if generated {
			report.Generated = append(report.Generated, rel)
			continue
		}
		report.Files = append(report.Files, FileReport{
			Path:     rel,
			Count:    count,
			Eligible: count.Counted() >= th.MinimumLines,
		})
	}
	if len(problems) > 0 {
		return nil, errors.Join(problems...)
	}
	if len(report.Files) == 0 {
		return nil, Refusal{Category: EmptySweep,
			Why: fmt.Sprintf("measured nothing: %s under %q holds no Go file to count, which is not a clean tree, it is a sweep that found nothing", listing, root)}
	}
	sort.Slice(report.Files, func(i, j int) bool { return report.Files[i].Path < report.Files[j].Path })
	sort.Strings(report.Generated)
	return report, nil
}

// CheckRepo measures the tree and returns the refusals it earns.
func CheckRepo(root string, th Thresholds) (*Report, error) {
	report, err := Measure(root, th)
	if err != nil {
		return nil, err
	}
	var problems []error
	for _, f := range report.Over() {
		problems = append(problems, Refusal{Category: OverCeiling, File: f.Path,
			Why: fmt.Sprintf("%s of its counted lines are prose (%d prose, %d code), over the %d%% ceiling. Say why once and delete the restatements",
				percent(f.Count), f.Prose, f.Code, th.CeilingPercent)})
	}
	return report, errors.Join(problems...)
}

// Eligible are the files the band and the ceiling apply to.
func (r *Report) Eligible() []FileReport {
	var out []FileReport
	for _, f := range r.Files {
		if f.Eligible {
			out = append(out, f)
		}
	}
	return out
}

// Over are the eligible files above the ceiling, worst first.
func (r *Report) Over() []FileReport {
	return r.rank(func(f FileReport) bool { return f.Above(r.Thresholds.CeilingPercent) })
}

// Near are the eligible files above the band but at or below the ceiling: named
// in the output, and not a refusal.
func (r *Report) Near() []FileReport {
	return r.rank(func(f FileReport) bool {
		return f.Above(r.Thresholds.BandPercent) && !f.Above(r.Thresholds.CeilingPercent)
	})
}

// MajorityProse are the eligible files whose prose lines reach their code
// lines, whatever the ceiling happens to be.
func (r *Report) MajorityProse() []FileReport {
	return r.rank(func(f FileReport) bool { return f.Count.MajorityProse() })
}

func (r *Report) rank(want func(FileReport) bool) []FileReport {
	var out []FileReport
	for _, f := range r.Eligible() {
		if want(f) {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Prose*b.Counted() != b.Prose*a.Counted() {
			return a.Prose*b.Counted() > b.Prose*a.Counted()
		}
		return a.Path < b.Path
	})
	return out
}

// Overall is the repository-wide count: every measured file, below-minimum ones
// included, because they are part of the repository even when they are too
// small to carry a threshold.
func (r *Report) Overall() Count {
	var total Count
	for _, f := range r.Files {
		total.Prose += f.Prose
		total.Code += f.Code
	}
	return total
}

// Worst is the highest-ratio eligible file.
func (r *Report) Worst() (FileReport, bool) {
	ranked := r.rank(func(FileReport) bool { return true })
	if len(ranked) == 0 {
		return FileReport{}, false
	}
	return ranked[0], true
}

// DeriveCeiling is the arithmetic the record writes out: the smallest whole
// multiple of five percentage points that no eligible file exceeds, never above
// fifty. It reports false when some eligible file is still over fifty, which
// means the trimming floor has not been reached and there is no ceiling to set.
func (r *Report) DeriveCeiling() (int, bool) {
	for candidate := 0; candidate <= 50; candidate += 5 {
		clean := true
		for _, f := range r.Eligible() {
			if f.Above(candidate) {
				clean = false
				break
			}
		}
		if clean {
			return candidate, true
		}
	}
	return 0, false
}

// Band is ten percentage points below the ceiling.
func Band(ceiling int) int {
	if ceiling < 10 {
		return 0
	}
	return ceiling - 10
}

// WriteSummary reports what was measured and the thresholds it was measured
// against, so a passing run is inspectable rather than merely quiet.
func (r *Report) WriteSummary(w io.Writer) {
	overall := r.Overall()
	fmt.Fprintf(w, "thresholds: error ceiling %d%%, warning band %d%%, minimum counted lines %d (%s)\n",
		r.Thresholds.CeilingPercent, r.Thresholds.BandPercent, r.Thresholds.MinimumLines, RecordFile)
	fmt.Fprintf(w, "%s: %d file(s) measured, %d eligible, %d generated and excluded\n",
		r.Listing, len(r.Files), len(r.Eligible()), len(r.Generated))
	fmt.Fprintf(w, "repository-wide: %s prose (%d prose, %d code)\n", percent(overall), overall.Prose, overall.Code)
	if worst, ok := r.Worst(); ok {
		fmt.Fprintf(w, "highest eligible file: %s at %s (%d prose, %d code)\n", worst.Path, percent(worst.Count), worst.Prose, worst.Code)
	}
	near := r.Near()
	if len(near) == 0 {
		fmt.Fprintf(w, "no file is above the %d%% warning band\n", r.Thresholds.BandPercent)
		return
	}
	fmt.Fprintf(w, "%d file(s) above the %d%% warning band:\n", len(near), r.Thresholds.BandPercent)
	for _, f := range near {
		fmt.Fprintf(w, "  %s %s (%d prose, %d code)\n", f.Path, percent(f.Count), f.Prose, f.Code)
	}
}

// WriteTable prints the per-file table RecordFile carries, so the record is
// regenerated from a run rather than transcribed by hand.
func (r *Report) WriteTable(w io.Writer) {
	overall := r.Overall()
	fmt.Fprintf(w, "| file | prose | code | counted | ratio | eligible |\n|---|---:|---:|---:|---:|---|\n")
	for _, f := range r.Files {
		eligible := "yes"
		if !f.Eligible {
			eligible = fmt.Sprintf("no (under %d)", r.Thresholds.MinimumLines)
		}
		fmt.Fprintf(w, "| %s | %d | %d | %d | %s | %s |\n", f.Path, f.Prose, f.Code, f.Counted(), percent(f.Count), eligible)
	}
	fmt.Fprintf(w, "\nfiles measured: %d\nfiles eligible: %d\nfiles generated and excluded: %d\n",
		len(r.Files), len(r.Eligible()), len(r.Generated))
	fmt.Fprintf(w, "repository-wide prose lines: %d\nrepository-wide code lines: %d\nrepository-wide ratio: %s\n",
		overall.Prose, overall.Code, percent(overall))
	if worst, ok := r.Worst(); ok {
		fmt.Fprintf(w, "highest eligible per-file ratio: %s (%s, %d prose, %d code)\n",
			percent(worst.Count), worst.Path, worst.Prose, worst.Code)
	}
	if ceiling, ok := r.DeriveCeiling(); ok {
		fmt.Fprintf(w, "smallest whole multiple of five percentage points no eligible file exceeds: %d%%\n\n%s",
			ceiling, RecordSection(ceiling, r.Thresholds.MinimumLines))
	}
	if majority := r.MajorityProse(); len(majority) > 0 {
		fmt.Fprintf(w, "eligible files whose prose lines reach their code lines: %d\n", len(majority))
		for _, f := range majority {
			fmt.Fprintf(w, "  %s %s (%d prose, %d code)\n", f.Path, percent(f.Count), f.Prose, f.Code)
		}
	}
}

// listGoFiles resolves the swept set. In a git work tree that is the tracked
// files, which is what the definition of an eligible file says and what keeps a
// scratch file dropped in the tree out of the numbers. A tree with no git
// directory - every committed refusal demonstration - is walked instead. The
// mode is reported rather than assumed, because a sweep that silently changed
// what it looks at is the one failure a green result cannot show.
func listGoFiles(root string) ([]string, string, error) {
	if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
		paths, err := gitTrackedGoFiles(root)
		return paths, "git-tracked", err
	}
	paths, err := walkGoFiles(root)
	return paths, "walked", err
}

func gitTrackedGoFiles(root string) ([]string, error) {
	cmd := exec.Command("git", "-C", root, "ls-files", "-z", "--", "*.go")
	out, err := cmd.Output()
	if err != nil {
		return nil, Refusal{Category: EmptySweep,
			Why: fmt.Sprintf("listing the tracked Go files of %q failed, so nothing was measured: %v", root, err)}
	}
	var paths []string
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" || skipped(rel) {
			continue
		}
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	return paths, nil
}

func walkGoFiles(root string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			if p == root {
				return nil
			}
			if skipDirs[e.Name()] || strings.HasPrefix(e.Name(), ".") || strings.HasPrefix(e.Name(), "_") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(e.Name(), ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, Refusal{Category: EmptySweep,
			Why: fmt.Sprintf("walking %q for Go files failed, so nothing was measured: %v", root, err)}
	}
	sort.Strings(paths)
	return paths, nil
}

func skipped(rel string) bool {
	for _, part := range strings.Split(path.Dir(rel), "/") {
		if skipDirs[part] {
			return true
		}
	}
	return false
}
