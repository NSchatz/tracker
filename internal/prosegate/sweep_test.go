package prosegate

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot is two levels up from internal/prosegate, where the tests run.
const repoRoot = "../.."

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// filler is a body of exactly n code lines and no prose, for building files
// either side of the minimum.
func filler(pkg string, n int) string {
	var b strings.Builder
	b.WriteString("package " + pkg + "\n\n")
	for i := 0; i < n-1; i++ {
		b.WriteString("var _ = 1\n")
	}
	return b.String()
}

func measure(t *testing.T, root string, th Thresholds) *Report {
	t.Helper()
	report, err := Measure(root, th)
	if err != nil {
		t.Fatalf("measuring %s: %v", root, err)
	}
	return report
}

func TestTestdataAndVendoredTreesAreSkipped(t *testing.T) {
	root := t.TempDir()
	write(t, root, "internal/kept/kept.go", filler("kept", 40))
	write(t, root, "internal/kept/testdata/fixture.go", filler("fixture", 40))
	write(t, root, "vendor/example.invalid/dep/dep.go", filler("dep", 40))

	report := measure(t, root, Enforced)
	if len(report.Files) != 1 || report.Files[0].Path != "internal/kept/kept.go" {
		t.Fatalf("measured %v, want internal/kept/kept.go alone: a fixture is nobody's to trim and vendored source is somebody else's", report.Files)
	}
}

func TestAFileBelowTheMinimumIsReportedAndNotGraded(t *testing.T) {
	root := t.TempDir()
	write(t, root, "big.go", filler("big", 40))
	write(t, root, "small.go", "package small\n\n// A four line doc comment.\n//\n// On a file this size that is a third prose\n// and it says nothing at all.\nfunc Small() int { return 1 }\n")

	report := measure(t, root, Enforced)
	if len(report.Files) != 2 {
		t.Fatalf("measured %d file(s), want 2: a small file is reported in the table", len(report.Files))
	}
	var small FileReport
	for _, f := range report.Files {
		if f.Path == "small.go" {
			small = f
		}
	}
	if small.Eligible {
		t.Errorf("small.go has %d counted lines, under the minimum of %d, so it carries no threshold", small.Counted(), Enforced.MinimumLines)
	}
	if _, err := CheckRepo(root, Enforced); err != nil {
		t.Errorf("a below-minimum file must not red the gate: %v", err)
	}
	var table bytes.Buffer
	report.WriteTable(&table)
	if !strings.Contains(table.String(), "small.go") {
		t.Errorf("the per-file table must still name it:\n%s", table.String())
	}
}

// narrated is a body whose prose can be dialled to a chosen ratio.
func narrated(pkg string, prose, code int) string {
	var b strings.Builder
	b.WriteString("package " + pkg + "\n\n")
	for i := 0; i < prose; i++ {
		b.WriteString("// This line is narration and nothing else.\n")
	}
	b.WriteString("var _ = 1\n")
	for i := 0; i < code-2; i++ {
		b.WriteString("var _ = 2\n")
	}
	return b.String()
}

func TestAFileOverTheCeilingIsRefusedByName(t *testing.T) {
	root := t.TempDir()
	write(t, root, "loud.go", narrated("loud", 30, 30))

	_, err := CheckRepo(root, Enforced)
	refusals := RefusalsIn(err)
	if len(refusals) != 1 {
		t.Fatalf("want one refusal, got %v", err)
	}
	got := refusals[0]
	if got.Category != OverCeiling || got.File != "loud.go" {
		t.Fatalf("want an over-ceiling refusal naming loud.go, got %v", got)
	}
	if !strings.Contains(got.Why, "50.0%") || !strings.Contains(got.Why, "45% ceiling") {
		t.Errorf("the refusal names the file, its measured ratio and the ceiling; got %q", got.Why)
	}
}

func TestAFileInTheBandIsNamedAndStillExitsZero(t *testing.T) {
	root := t.TempDir()
	write(t, root, "quiet.go", filler("quiet", 60))
	write(t, root, "warned.go", narrated("warned", 20, 30))

	report, err := CheckRepo(root, Enforced)
	if err != nil {
		t.Fatalf("the band warns, it does not refuse: %v", err)
	}
	near := report.Near()
	if len(near) != 1 || near[0].Path != "warned.go" {
		t.Fatalf("want warned.go in the band, got %v", near)
	}
	var out bytes.Buffer
	report.WriteSummary(&out)
	if !strings.Contains(out.String(), "warned.go") || !strings.Contains(out.String(), "40.0%") {
		t.Errorf("a banded file is named with its ratio in the output:\n%s", out.String())
	}
}

func TestASweepThatMeasuredNothingIsRefused(t *testing.T) {
	root := t.TempDir()
	write(t, root, "README.md", "no Go here\n")

	_, err := Measure(root, Enforced)
	refusals := RefusalsIn(err)
	if len(refusals) != 1 || refusals[0].Category != EmptySweep {
		t.Fatalf("want an empty-sweep refusal, got %v", err)
	}
	if !strings.Contains(refusals[0].Why, "measured nothing") {
		t.Errorf("it has to say it measured nothing rather than report a clean tree; got %q", refusals[0].Why)
	}
}

func TestAFileThatCannotBeReadIsRefusedAndNotDropped(t *testing.T) {
	root := t.TempDir()
	write(t, root, "kept.go", filler("kept", 40))
	if err := os.Symlink("there-is-no-such-file", filepath.Join(root, "gone.go")); err != nil {
		t.Fatalf("building the unreadable case: %v", err)
	}

	_, err := Measure(root, Enforced)
	refusals := RefusalsIn(err)
	if len(refusals) != 1 || refusals[0].Category != Unreadable || refusals[0].File != "gone.go" {
		t.Fatalf("want an unreadable refusal naming gone.go, got %v", err)
	}
	if !strings.Contains(refusals[0].Why, "dropped from the denominator") {
		t.Errorf("a file that vanished from the denominator is a quieter gate, not a cleaner tree; got %q", refusals[0].Why)
	}
}

func TestAFileThatCannotBeParsedIsRefusedByName(t *testing.T) {
	root := t.TempDir()
	write(t, root, "broken.go", "package broken\n\nfunc Broken( {\n")

	_, err := Measure(root, Enforced)
	refusals := RefusalsIn(err)
	if len(refusals) != 1 || refusals[0].Category != Unparseable || refusals[0].File != "broken.go" {
		t.Fatalf("want an unparseable refusal naming broken.go, got %v", err)
	}
	if !strings.Contains(refusals[0].Why, "expected") {
		t.Errorf("the parser's own error is quoted rather than summarised; got %q", refusals[0].Why)
	}
}

func TestASweptSetWithNoCommentsIsZeroAndClean(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", filler("a", 40))
	write(t, root, "b.go", filler("b", 40))

	report, err := CheckRepo(root, Enforced)
	if err != nil {
		t.Fatalf("no comments anywhere is a clean tree: %v", err)
	}
	if report.Overall().Prose != 0 {
		t.Errorf("repository prose = %d, want 0", report.Overall().Prose)
	}
	var out bytes.Buffer
	report.WriteSummary(&out)
	if !strings.Contains(out.String(), "repository-wide: 0.0% prose") {
		t.Errorf("a zero repository ratio is reported as one:\n%s", out.String())
	}
}

func TestTheCeilingIsDerivedAndNotChosen(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prose   int
		code    int
		ceiling int
		ok      bool
	}{
		{"a 31% worst file gets a 35% ceiling", 31, 69, 35, true},
		{"a 35% worst file gets a 35% ceiling", 35, 65, 35, true},
		{"a 36% worst file gets a 40% ceiling", 36, 64, 40, true},
		{"a 49% worst file gets a 50% ceiling", 49, 51, 50, true},
		{"a 51% worst file has no ceiling to set", 51, 49, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, "worst.go", narrated("worst", tc.prose, tc.code))
			report := measure(t, root, Enforced)
			ceiling, ok := report.DeriveCeiling()
			if ok != tc.ok || (ok && ceiling != tc.ceiling) {
				t.Fatalf("derived %d%% (ok=%v) from %+v, want %d%% (ok=%v)", ceiling, ok, report.Files[0].Count, tc.ceiling, tc.ok)
			}
			if tc.ok && Band(ceiling) != ceiling-10 {
				t.Errorf("the band is ten points below the ceiling, got %d%%", Band(ceiling))
			}
		})
	}
}

// The repository itself, measured against the numbers it records. This is the
// tree-level assertion: the gate is a ratchet only while the tree it guards is
// under it.
func TestTheRepositoryIsUnderItsOwnCeiling(t *testing.T) {
	report, err := CheckRepo(repoRoot, Enforced)
	if err != nil {
		t.Fatalf("the repository is over its own ceiling: %v", err)
	}
	if report.Listing != "git-tracked" {
		t.Errorf("the repository sweep lists tracked files, got %q", report.Listing)
	}
	if len(report.MajorityProse()) != 0 {
		t.Errorf("no eligible file may have prose lines reaching its code lines: %v", report.MajorityProse())
	}
	if len(report.Eligible()) < 50 {
		t.Errorf("only %d eligible file(s) measured; a sweep that quietly stopped looking reports the same clean tree", len(report.Eligible()))
	}
}
