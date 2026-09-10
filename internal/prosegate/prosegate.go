// Package prosegate is the `prose-check` half of the gate: a comment-density
// ceiling that counts prose from the Go token stream rather than from line
// patterns.
//
// Pattern matching is the documented failure this exists to avoid. A comment
// marker inside a string literal, or the closing delimiter of a raw string
// spanning several lines, reads exactly like a comment opening one, and a
// counter that guesses scores files that are mostly code as mostly narration.
// Trimming to that number guts them. Every count here comes from go/parser,
// go/scanner and go/ast.
//
// Like the pin gate this one READS FILES and asks nothing of any daemon, SDK,
// credential or registry, so it reaches the same verdict on an airgapped laptop
// as it does in CI. The thresholds it enforces are this repository's own
// measured baseline, written down in RecordFile and asserted against these
// constants by the package's own tests. Raising one is a source change with a
// name on it.
package prosegate

import (
	"fmt"
	"sort"
	"strings"
)

// RecordFile holds the measurement the thresholds below were derived from.
const RecordFile = "COMMENT-DENSITY-RECORD.md"

// Thresholds are whole percentage points so that every comparison is exact
// integer arithmetic. A float ceiling read from one place and computed in
// another is a gate that disagrees with itself in the last bit.
type Thresholds struct {
	CeilingPercent int
	BandPercent    int
	MinimumLines   int
}

// Enforced is what `make prose-check` applies. The derivation is in RecordFile:
// the ceiling is the smallest whole multiple of five percentage points that no
// eligible file exceeds, capped at fifty, and the band is ten points below it.
var Enforced = Thresholds{CeilingPercent: 35, BandPercent: 25, MinimumLines: 30}

// Category names one shape of refusal. A category that has stopped finding
// anything looks exactly like a compliant repository, so the committed
// demonstrations assert every one of them still fires.
type Category string

const (
	OverCeiling Category = "over-ceiling"
	Unparseable Category = "unparseable"
	Unreadable  Category = "unreadable"
	EmptySweep  Category = "empty-sweep"
	RecordDrift Category = "record-drift"
)

// Categories is the closed set, in reporting order.
var Categories = []Category{OverCeiling, Unparseable, Unreadable, EmptySweep, RecordDrift}

// Refusal is one reason the gate exits non-zero. It carries the category and
// the file so a demonstration can assert which rule bit it, not merely that
// something went red.
type Refusal struct {
	Category Category
	File     string
	Why      string
}

func (r Refusal) Error() string {
	if r.File == "" {
		return fmt.Sprintf("[%s] %s", r.Category, r.Why)
	}
	return fmt.Sprintf("[%s] %s: %s", r.Category, r.File, r.Why)
}

// RefusalsIn pulls every Refusal out of a possibly joined error.
func RefusalsIn(err error) []Refusal {
	var out []Refusal
	var walk func(error)
	walk = func(e error) {
		switch v := e.(type) {
		case nil:
			return
		case Refusal:
			out = append(out, v)
		case interface{ Unwrap() []error }:
			for _, inner := range v.Unwrap() {
				walk(inner)
			}
		case interface{ Unwrap() error }:
			walk(v.Unwrap())
		}
	}
	walk(err)
	return out
}

// CategoriesIn is the set of categories a refusal set fired, sorted.
func CategoriesIn(err error) []string {
	seen := map[string]bool{}
	for _, r := range RefusalsIn(err) {
		seen[string(r.Category)] = true
	}
	out := make([]string, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

func percent(count Count) string {
	if count.Counted() == 0 {
		return "0.0%"
	}
	return strings.TrimSpace(fmt.Sprintf("%.1f%%", 100*float64(count.Prose)/float64(count.Counted())))
}
