package prosegate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func recordAt(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, RecordFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// The committed record and the enforced constants are two statements of the
// same number, and the ratchet is only a ratchet while they agree.
func TestTheRecordStatesWhatTheGateEnforces(t *testing.T) {
	if err := CheckRecord(repoRoot, Enforced); err != nil {
		t.Fatalf("%s and internal/prosegate disagree: %v", RecordFile, err)
	}
}

func TestTheThresholdsAreDerivedFromTheRepositoryItMeasures(t *testing.T) {
	report, err := Measure(repoRoot, Enforced)
	if err != nil {
		t.Fatalf("measuring the repository: %v", err)
	}
	ceiling, ok := report.DeriveCeiling()
	if !ok {
		t.Fatalf("some eligible file is still over fifty percent: %v", report.MajorityProse())
	}
	if ceiling != Enforced.CeilingPercent {
		t.Errorf("the ceiling is the smallest whole multiple of five percentage points no eligible file exceeds, which this tree makes %d%%; the gate enforces %d%%", ceiling, Enforced.CeilingPercent)
	}
	if Band(Enforced.CeilingPercent) != Enforced.BandPercent {
		t.Errorf("the band is ten points below the ceiling, which makes it %d%%; the gate enforces %d%%", Band(Enforced.CeilingPercent), Enforced.BandPercent)
	}
}

func TestARecordThatDisagreesWithTheGateIsRefused(t *testing.T) {
	root := recordAt(t, "- error ceiling: 50%\n- warning band: 25%\n- minimum counted lines: 30\n")

	err := CheckRecord(root, Enforced)
	refusals := RefusalsIn(err)
	if len(refusals) != 1 || refusals[0].Category != RecordDrift {
		t.Fatalf("want one record-drift refusal, got %v", err)
	}
	if !strings.Contains(refusals[0].Why, "states error ceiling 50, and the gate enforces 35") {
		t.Errorf("the refusal names both numbers; got %q", refusals[0].Why)
	}
}

func TestARecordThatSaysNothingIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"no thresholds at all", "# a record with no numbers in it\n", "states no \"error ceiling\" line"},
		{"one line missing", "- error ceiling: 35%\n- warning band: 25%\n", "states no \"minimum counted lines\" line"},
		{"two answers to one question", "- error ceiling: 35%\n- error ceiling: 40%\n- warning band: 25%\n- minimum counted lines: 30\n", "cannot hold the gate to two numbers"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckRecord(recordAt(t, tc.body), Enforced)
			if err == nil {
				t.Fatal("a record nobody can read cannot hold the gate to anything")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want a refusal containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestAMissingRecordIsRefused(t *testing.T) {
	err := CheckRecord(t.TempDir(), Enforced)
	refusals := RefusalsIn(err)
	if len(refusals) != 1 || refusals[0].Category != RecordDrift {
		t.Fatalf("want a record-drift refusal, got %v", err)
	}
}

func TestADerivationTheRecordCouldNotHaveProducedIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name     string
		enforced Thresholds
		want     string
	}{
		{"a band that is not ten below the ceiling", Thresholds{35, 30, 30}, "makes it 25%, not 30%"},
		{"a ceiling above fifty", Thresholds{55, 45, 30}, "never above fifty percent"},
		{"a ceiling that is not a multiple of five", Thresholds{37, 27, 30}, "whole multiple of five"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf("- error ceiling: %d%%\n- warning band: %d%%\n- minimum counted lines: %d\n",
				tc.enforced.CeilingPercent, tc.enforced.BandPercent, tc.enforced.MinimumLines)
			err := CheckRecord(recordAt(t, body), tc.enforced)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want a refusal containing %q, got %v", tc.want, err)
			}
		})
	}
}

// The record is regenerated from a run rather than transcribed, so the lines it
// carries have to be the lines the parser reads back.
func TestTheGeneratedRecordSectionParses(t *testing.T) {
	got, err := ReadRecordThresholds(recordAt(t, RecordSection(45, 30)))
	if err != nil {
		t.Fatalf("reading back what RecordSection wrote: %v", err)
	}
	if want := (Thresholds{CeilingPercent: 45, BandPercent: 35, MinimumLines: 30}); got != want {
		t.Errorf("read back %+v, want %+v", got, want)
	}
}
