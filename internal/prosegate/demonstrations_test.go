package prosegate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A gate that has stopped biting looks exactly like a compliant repository, so
// every committed refusal tree is scanned and required to go red for its own
// reason: the category, the file it names, and the explanation it gives.
func TestEveryCommittedDemonstrationStillGoesRed(t *testing.T) {
	run, err := RunDemonstrations(repoRoot, Enforced)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if run.Red != len(Demonstrations) {
		t.Errorf("%d of %d demonstrations went red", run.Red, len(Demonstrations))
	}
}

// Every refusal category the gate has is covered by a committed case, RecordDrift
// excepted: that one is a disagreement between two committed statements rather
// than a property of a tree, and it is demonstrated in record_test.go.
func TestEveryRefusalCategoryIsDemonstrated(t *testing.T) {
	covered := map[Category]bool{}
	for _, d := range Demonstrations {
		covered[d.Category] = true
	}
	for _, c := range Categories {
		if c == RecordDrift {
			continue
		}
		if !covered[c] {
			t.Errorf("no committed demonstration covers the %s category", c)
		}
	}
	if !strings.Contains(strings.Join(CategoriesIn(recordDriftRefusal(t)), ","), string(RecordDrift)) {
		t.Errorf("the record-drift category has stopped finding anything")
	}
}

// recordDriftRefusal is the RecordDrift demonstration: a record that states a
// number the gate does not enforce.
func recordDriftRefusal(t *testing.T) error {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, RecordFile), []byte("- error ceiling: 15%\n- warning band: 5%\n- minimum counted lines: 30\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := CheckRecord(root, Enforced)
	if err == nil {
		t.Fatal("a record stating a ceiling the gate does not enforce is a threshold nobody voted for")
	}
	return err
}

// Skipping a directory is normally a hole, so this one is nailed shut from the
// other side: nothing may be parked where the sweep does not look.
func TestTheDemonstrationTreeHoldsOnlyItsCases(t *testing.T) {
	if err := verifyDemonstrationTree(repoRoot); err != nil {
		t.Fatalf("%v", err)
	}
}

// The mutation that proves the check above can fail: a stray file in the
// excluded tree has to be found.
func TestAStrayFileInTheDemonstrationTreeIsFound(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, filepath.FromSlash(DemonstrationsDir))
	for _, d := range Demonstrations {
		if err := os.MkdirAll(filepath.Join(dir, d.Name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := verifyDemonstrationTree(root); err != nil {
		t.Fatalf("the committed cases alone are legal here: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "parked.go"), []byte("package parked\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := verifyDemonstrationTree(root)
	if err == nil || !strings.Contains(err.Error(), "parked.go") {
		t.Fatalf("a file parked in the excluded tree is invisible to the gate, which is what the exclusion must not become; got %v", err)
	}
}

// The demonstrations are only evidence while a fixture cannot pass by accident,
// so each one is required to declare the explanation it matches on.
func TestEveryDemonstrationNamesTheReasonItMatches(t *testing.T) {
	for _, d := range Demonstrations {
		if d.WhyContains == "" {
			t.Errorf("demonstration %q declares no WhyContains, so any %s refusal would satisfy it", d.Name, d.Category)
		}
	}
}
