package prosegate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// The record states the numbers the gate enforces on three lines of exactly
// this shape, so the two statements can be compared rather than believed.
var recordLine = regexp.MustCompile(`(?m)^- (error ceiling|warning band|minimum counted lines): ([0-9]+)%?$`)

// ReadRecordThresholds parses the thresholds RecordFile states. A missing or
// malformed line is an error, never a guess: a record nobody can read cannot
// hold the gate to anything.
func ReadRecordThresholds(root string) (Thresholds, error) {
	path := filepath.Join(root, RecordFile)
	body, err := os.ReadFile(path)
	if err != nil {
		return Thresholds{}, Refusal{Category: RecordDrift, File: RecordFile,
			Why: fmt.Sprintf("cannot be read, so the thresholds the gate enforces answer to nothing: %v", err)}
	}

	seen := map[string][]int{}
	for _, m := range recordLine.FindAllStringSubmatch(string(body), -1) {
		n, err := strconv.Atoi(m[2])
		if err != nil {
			return Thresholds{}, Refusal{Category: RecordDrift, File: RecordFile,
				Why: fmt.Sprintf("states %q for %q, which is not a whole number", m[2], m[1])}
		}
		seen[m[1]] = append(seen[m[1]], n)
	}

	var problems []error
	value := func(key string) int {
		switch len(seen[key]) {
		case 1:
			return seen[key][0]
		case 0:
			problems = append(problems, Refusal{Category: RecordDrift, File: RecordFile,
				Why: fmt.Sprintf("states no %q line, so the number %s enforces is written down nowhere", key, RecordFile)})
		default:
			problems = append(problems, Refusal{Category: RecordDrift, File: RecordFile,
				Why: fmt.Sprintf("states %q %d times; one record cannot hold the gate to two numbers", key, len(seen[key]))})
		}
		return 0
	}
	th := Thresholds{
		CeilingPercent: value("error ceiling"),
		BandPercent:    value("warning band"),
		MinimumLines:   value("minimum counted lines"),
	}
	return th, errors.Join(problems...)
}

// CheckRecord compares the numbers the record states against the numbers the
// gate enforces. The ratchet is only a ratchet while the two agree: a ceiling
// raised in source and left un-recorded is a threshold nobody voted for.
func CheckRecord(root string, enforced Thresholds) error {
	recorded, err := ReadRecordThresholds(root)
	if err != nil {
		return err
	}
	var problems []error
	compare := func(key string, want, got int) {
		if want == got {
			return
		}
		problems = append(problems, Refusal{Category: RecordDrift, File: RecordFile,
			Why: fmt.Sprintf("states %s %d, and the gate enforces %d", key, got, want)})
	}
	compare("error ceiling", enforced.CeilingPercent, recorded.CeilingPercent)
	compare("warning band", enforced.BandPercent, recorded.BandPercent)
	compare("minimum counted lines", enforced.MinimumLines, recorded.MinimumLines)
	if Band(enforced.CeilingPercent) != enforced.BandPercent {
		problems = append(problems, Refusal{Category: RecordDrift, File: RecordFile,
			Why: fmt.Sprintf("the band is ten points below the ceiling by derivation, so a %d%% ceiling makes it %d%%, not %d%%",
				enforced.CeilingPercent, Band(enforced.CeilingPercent), enforced.BandPercent)})
	}
	if enforced.CeilingPercent > 50 {
		problems = append(problems, Refusal{Category: RecordDrift, File: RecordFile,
			Why: fmt.Sprintf("the ceiling is never above fifty percent, and %d%% is", enforced.CeilingPercent)})
	}
	if enforced.CeilingPercent%5 != 0 {
		problems = append(problems, Refusal{Category: RecordDrift, File: RecordFile,
			Why: fmt.Sprintf("the ceiling is a whole multiple of five percentage points, and %d%% is not", enforced.CeilingPercent)})
	}
	return errors.Join(problems...)
}

// RecordSection is the block of threshold lines the record carries, written
// from a run so the record is regenerated rather than transcribed.
func RecordSection(ceiling, minimum int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- error ceiling: %d%%\n", ceiling)
	fmt.Fprintf(&b, "- warning band: %d%%\n", Band(ceiling))
	fmt.Fprintf(&b, "- minimum counted lines: %d\n", minimum)
	return b.String()
}
