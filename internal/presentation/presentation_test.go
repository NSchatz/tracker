// The presentation decision, table-driven with BOTH instants supplied as inputs.
//
// Nothing here sleeps and nothing here reads a clock: "last contact precedes the evaluation instant
// by exactly 900 seconds" is a fact a test constructs, not a race it runs. That is the whole reason
// the evaluation is a pure function — the boundary cases are the ones a real deployment hits at 3am,
// and they have to be exactly reproducible.
package presentation

import (
	"testing"
	"time"
)

// defaults is the pair a deployment that configures nothing runs with.
var defaults = Windows{LiveSeconds: 120, StaleSeconds: 900}

// at returns the instant `ageSeconds` before `now`, with a sub-second offset so the truncation rule
// is exercised rather than assumed: an age of 120.4 seconds must read as 120.
func at(now time.Time, ageSeconds int64, extraMillis int) *time.Time {
	t := now.Add(-time.Duration(ageSeconds)*time.Second - time.Duration(extraMillis)*time.Millisecond)
	return &t
}

// TestEvaluateBoundaries is AC9: the five exact ages the spec names, under the default windows, with
// both instants supplied. Boundaries are half-open toward freshness — age exactly 120 is `live`, age
// exactly 900 is `recent` — so the two "off by one" cases either side are here too.
func TestEvaluateBoundaries(t *testing.T) {
	now := time.Date(2026, 3, 14, 15, 9, 26, 535_000_000, time.UTC)

	cases := []struct {
		ageSeconds int64
		want       Value
	}{
		{0, Live},
		{120, Live},   // exactly the live window: still live.
		{121, Recent}, // one second past it.
		{900, Recent}, // exactly the staleness window: still recent.
		{901, Stale},  // one second past it.
	}
	for _, c := range cases {
		if got := Evaluate(now, at(now, c.ageSeconds, 0), defaults); got != c.want {
			t.Errorf("age %ds -> %q, want %q", c.ageSeconds, got, c.want)
		}
	}
}

// TestAgeTruncatesTowardZero pins the resolution rule: age is WHOLE seconds, truncated, so 120.4
// seconds is age 120 and `live`. Rounding instead of truncating would flip this case, and rounding
// each instant separately would flip it in the other direction — both are pinned here.
func TestAgeTruncatesTowardZero(t *testing.T) {
	now := time.Date(2026, 3, 14, 15, 9, 26, 100_000_000, time.UTC)

	// 120.4 seconds ago: the whole-second age is 120, which is `live`.
	if got := AgeSeconds(now, *at(now, 120, 400)); got != 120 {
		t.Errorf("age of a 120.4s-old contact = %d, want 120 (truncated toward zero)", got)
	}
	if got := Evaluate(now, at(now, 120, 400), defaults); got != Live {
		t.Errorf("a device last heard from 120.4s ago is %q, want live", got)
	}

	// The trap: now has a SMALLER sub-second part than last contact (now .1, contact .9), so the
	// real difference is 9.2 seconds. Truncating each end separately would say 10.
	contact := now.Add(-10 * time.Second).Add(800 * time.Millisecond)
	if got := AgeSeconds(now, contact); got != 9 {
		t.Errorf("age across a sub-second borrow = %d, want 9 (the DIFFERENCE is truncated, not each instant)", got)
	}
}

// TestNegativeAgeIsLive is AC11: the device's last contact is AHEAD of the server clock (skew). The
// answer is `live` with an age of zero — never a negative age, never an error, never an omission.
func TestNegativeAgeIsLive(t *testing.T) {
	now := time.Date(2026, 3, 14, 15, 9, 26, 0, time.UTC)
	future := now.Add(2 * time.Hour)

	if got := AgeSeconds(now, future); got != 0 {
		t.Errorf("age with a future last contact = %d, want 0 (floored, never negative)", got)
	}
	if got := Evaluate(now, &future, defaults); got != Live {
		t.Errorf("a device whose last contact is in the future is %q, want live", got)
	}
}

// TestNoFixIsNeverStale is the standing precedence ruling: step 1 TERMINATES the test. A device that
// has never reported is `no-position` however long ago it was enrolled — the distinction the whole
// spec exists to make — and a device that holds a fix is never `no-position`.
func TestNoFixIsNeverStale(t *testing.T) {
	now := time.Date(2026, 3, 14, 15, 9, 26, 0, time.UTC)

	if got := Evaluate(now, nil, defaults); got != NoPosition {
		t.Fatalf("a device with no fix is %q, want no-position", got)
	}
	// Ten years of not reporting is still no-position, not stale.
	if got := Evaluate(now.Add(10*365*24*time.Hour), nil, defaults); got != NoPosition {
		t.Fatalf("a device enrolled ten years ago and never seen is %q, want no-position", got)
	}
	// And the symmetric half: any device holding a fix gets an age-based value, never no-position.
	for _, age := range []int64{0, 120, 121, 900, 901, 10_000_000} {
		if got := Evaluate(now, at(now, age, 0), defaults); got == NoPosition {
			t.Errorf("a device holding a fix aged %ds came out no-position", age)
		}
	}
}

// TestValuesAreTotalAndMutuallyExclusive is the invariant a later refactor would silently break: over
// a device set holding one of each kind, every device gets exactly one value, all four occur, and
// nothing outside the vocabulary is ever produced.
func TestValuesAreTotalAndMutuallyExclusive(t *testing.T) {
	now := time.Date(2026, 3, 14, 15, 9, 26, 0, time.UTC)

	kinds := map[Value]*time.Time{
		NoPosition: nil,
		Live:       at(now, 30, 0),
		Recent:     at(now, 500, 0),
		Stale:      at(now, 5_000, 0),
	}

	seen := map[Value]int{}
	for want, lastContact := range kinds {
		got := Evaluate(now, lastContact, defaults)
		if got != want {
			t.Errorf("device seeded as %q evaluated %q", want, got)
		}
		if !Valid(got) {
			t.Errorf("evaluation produced %q, which is not one of the four tokens", got)
		}
		seen[got]++
	}
	if len(seen) != len(Values) {
		t.Fatalf("a device set holding one of each kind produced %d distinct values, want %d — the four are not total",
			len(seen), len(Values))
	}
	for _, v := range Values {
		if seen[v] != 1 {
			t.Errorf("value %q was assigned %d times over a set of one device per kind, want exactly 1", v, seen[v])
		}
	}
}

// TestValidRejectsEverythingElse pins the vocabulary itself: casing, spacing and plausible synonyms
// are NOT the wire tokens, so a value that ever appeared in one of these spellings would be caught.
func TestValidRejectsEverythingElse(t *testing.T) {
	for _, bad := range []Value{"", "No-Position", "noposition", "no_position", "LIVE", "fresh", "offline", "stale "} {
		if Valid(bad) {
			t.Errorf("%q is treated as a valid presentation token; the vocabulary is exactly %v", bad, Values)
		}
	}
}

// TestSweepBound pins B for the two pairs the spec names and the reason the min is there: with a
// narrow `recent` band the bound must shrink, or a sweep would step over the whole state.
func TestSweepBound(t *testing.T) {
	cases := []struct {
		w    Windows
		want int64
	}{
		{defaults, 60},                                  // min(120, 780)/2
		{Windows{LiveSeconds: 5, StaleSeconds: 10}, 2},  // min(5, 5)/2 — the narrow band
		{Windows{LiveSeconds: 1, StaleSeconds: 2}, 1},   // floored at 1, never 0
		{Windows{LiveSeconds: 10, StaleSeconds: 11}, 1}, // min(10, 1)/2 = 0, floored
	}
	for _, c := range cases {
		if got := SweepBound(c.w); got != c.want {
			t.Errorf("SweepBound(%+v) = %d, want %d", c.w, got, c.want)
		}
	}
}

// TestSweepBoundIsAlwaysAtLeastOneSecond is the property the stream leans on: its poll cadence is one
// second, so if B can never be less than one second, the stream meets AC17's bound for EVERY legal
// window pair without the cadence having to know what the windows are.
func TestSweepBoundIsAlwaysAtLeastOneSecond(t *testing.T) {
	for live := int64(1); live <= 40; live++ {
		for stale := live + 1; stale <= live+40; stale++ {
			if got := SweepBound(Windows{LiveSeconds: live, StaleSeconds: stale}); got < 1 {
				t.Fatalf("SweepBound(%d, %d) = %d; a sweep period below one second is not a period", live, stale, got)
			}
		}
	}
}
