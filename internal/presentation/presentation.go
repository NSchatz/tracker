// Package presentation turns "how long ago did this phone last reach us" into the one word every
// tracker surface publishes for a device. It is SERVER-COMPUTED and computed HERE ALONE - a pure
// function of (evaluation instant, last contact, windows) - so a browser with a wrong clock still
// renders what the server said and the read API, the stream and the resume sweep cannot disagree.
//
// "Last contact" is max(received_at) across ALL of a device's fixes, NOT the received_at of the
// current position, which the DEVICE's clock selects. A backlog flush or a fast device clock makes
// them differ, and an age off the current position would call a live device `stale`, so it rides the
// wire as `last_contact_at`, distinct from `received_at`.
package presentation

import "time"

// Value is a device's presentation state. The token spellings ARE the wire contract (SPEC.md).
type Value string

const (
	// NoPosition: NO fix is held. Such a device is NEVER `stale`, however long ago it was enrolled.
	NoPosition Value = "no-position"
	// Live: reached us within the live window.
	Live Value = "live"
	// Recent: past the live window, inside the staleness window.
	Recent Value = "recent"
	// Stale: past the staleness window.
	Stale Value = "stale"
)

// Values is the complete vocabulary, in precedence order, so no caller repeats the literals.
var Values = []Value{NoPosition, Live, Recent, Stale}

// Valid reports whether v is one of the four. Evaluate returns only these, so this is for the tests.
func Valid(v Value) bool {
	for _, known := range Values {
		if v == known {
			return true
		}
	}
	return false
}

// Windows is the operator-configured pair, in SECONDS: the documented range runs to 2^63-1 seconds,
// which a nanosecond time.Duration cannot hold. Live must be strictly less than Stale or `recent` is
// unreachable; config refuses to start otherwise, so Evaluate does not re-check it.
type Windows struct {
	LiveSeconds  int64
	StaleSeconds int64
}

// Evaluate assigns a device its one presentation value. Boundaries are half-open toward freshness.
//
//  1. no fix                      -> no-position
//  2. age <= LiveSeconds          -> live
//  3. LiveSeconds < age <= Stale  -> recent
//  4. age > StaleSeconds          -> stale
//
// A nil lastContact terminates before any age comparison, which is why a never-reported device and
// a device whose last fix retention has purged are `no-position` rather than aging into `stale`.
func Evaluate(evaluationInstant time.Time, lastContact *time.Time, w Windows) Value {
	if lastContact == nil {
		return NoPosition
	}
	age := AgeSeconds(evaluationInstant, *lastContact)
	switch {
	case age <= w.LiveSeconds:
		return Live
	case age <= w.StaleSeconds:
		return Recent
	default:
		return Stale
	}
}

// AgeSeconds is the device's age in WHOLE SECONDS: the evaluation instant minus last contact,
// truncated toward zero, floored at 0.
//
//   - The truncation is of the DIFFERENCE, not of each instant. 120.4 seconds ago is age 120 and
//     `live` under the defaults; rounding the two ends independently hands back 121 for the same
//     instant pair, flipping the boundary case the tests pin.
//   - A negative age - clock skew - is ZERO, never a negative that sails past every comparison.
//   - Seconds, not time.Duration: a Duration saturates at about 292 years, a staleness window may
//     legally be larger.
func AgeSeconds(evaluationInstant, lastContact time.Time) int64 {
	secs := evaluationInstant.Unix() - lastContact.Unix()
	if nanos := evaluationInstant.Nanosecond() - lastContact.Nanosecond(); nanos < 0 {
		secs-- // borrow: the sub-second parts make the whole-second difference one smaller.
	}
	if secs < 0 {
		return 0
	}
	return secs
}

// SweepBound is B: how often a surface publishing time-driven transitions must re-evaluate, in
// seconds, so a device cannot cross a boundary and sit there unannounced.
//
//	B = max(1, floor(min(Live, Stale-Live) / 2))
//
// The min is what makes it safe under a NARROW `recent` band: with windows (5, 10) a device is
// `recent` for five seconds, which a 60 second sweep steps straight over. The floor at 1 is also the
// stream's poll cadence, so the stream meets the bound by construction for every configuration.
func SweepBound(w Windows) int64 {
	band := w.StaleSeconds - w.LiveSeconds
	smaller := w.LiveSeconds
	if band < smaller {
		smaller = band
	}
	if b := smaller / 2; b > 1 {
		return b
	}
	return 1
}
