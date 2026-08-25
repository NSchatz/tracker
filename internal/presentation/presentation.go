// Package presentation turns "how long ago did this phone last reach us" into the one word every
// tracker surface publishes for a device.
//
// # Why the server computes it, and why exactly once
//
// A viewer cannot otherwise tell "this phone has never checked in" — a setup problem — from "this
// phone stopped checking in an hour ago", which is the only one worth worrying about. Both used to
// render as an absence. So every read surface (GET /v1/positions, the SSE stream, and the browser map
// they feed) carries a SERVER-COMPUTED presentation value, and no client re-derives liveness from a
// clock the server does not control: a browser whose clock is hours off must still render what the
// server said.
//
// That is only true if there is exactly one implementation, which is what this package is. It is a
// PURE FUNCTION of (evaluation instant, last contact, windows) and nothing else — no clock read, no
// database, no I/O — which is what makes the boundary cases constructible in a test instead of raced
// against a sleep, and what makes the read API, the stream and the resume sweep incapable of
// disagreeing about the same device.
//
// # The two facts that are easy to confuse
//
// "Last contact" is max(received_at) across ALL of a device's fixes — an aggregate over the whole
// set. It is NOT the received_at of the current position, which is one row selected by the DEVICE's
// clock (the newest `ts`). The two differ exactly when a fix arrived that did not become the current
// position: a phone flushing an offline backlog reports OLD `ts` values with a FRESH received_at, and
// a phone with a fast clock reports a future `ts` that pins the current position while later fixes
// keep arriving. In both cases the device is plainly alive, and an age measured off the current
// position's own receive time would call it `stale`. Age is a fact about the DEVICE, not about the
// row on display — which is why it rides the wire as `last_contact_at`, beside and distinct from the
// unchanged `received_at`.
package presentation

import "time"

// Value is a device's presentation state: exactly one of four, assigned at the moment a response or
// event is produced. The token spellings ARE the wire contract (SPEC.md) — lowercase ASCII, hyphen in
// `no-position`, no synonyms — so they are declared once, here, and every surface marshals this type
// rather than a string literal it typed itself.
type Value string

const (
	// NoPosition: tracker holds NO fix for this device. It has never reported, or every fix it ever
	// sent has since been purged. A device in this state is NEVER `stale`, however long ago it was
	// enrolled — "never checked in" and "went quiet" are different problems with different fixes.
	NoPosition Value = "no-position"
	// Live: the device reached us within the live window.
	Live Value = "live"
	// Recent: past the live window, still inside the staleness window.
	Recent Value = "recent"
	// Stale: past the staleness window. Something is wrong with this phone, and that is the whole
	// reason the four values exist.
	Stale Value = "stale"
)

// Values is the complete vocabulary, in precedence order. It exists so that "is this token one of the
// four?" is answerable from one place — the map page's guard and the server's own totality test both
// read the same list rather than each repeating a literal.
var Values = []Value{NoPosition, Live, Recent, Stale}

// Valid reports whether v is one of the four tokens. Nothing in the server can produce a token
// outside them (Evaluate returns only these), so this exists for the tests that PROVE that, and for
// any future decoder reading a value off the wire.
func Valid(v Value) bool {
	for _, known := range Values {
		if v == known {
			return true
		}
	}
	return false
}

// Windows is the operator-configured pair, in SECONDS. Seconds and int64 rather than time.Duration on
// purpose: the documented representable range runs to 2^63-1 SECONDS, which a nanosecond Duration
// cannot hold, and one unit end to end is one fewer conversion to lose a factor of a thousand in.
//
// Live must be strictly less than Stale; config refuses to start otherwise, because an inverted or
// equal pair leaves `recent` unreachable. Evaluate does not re-check it — a Windows that exists came
// through that gate — and would simply never return Recent if it somehow did not.
type Windows struct {
	LiveSeconds  int64
	StaleSeconds int64
}

// Evaluate assigns a device its one presentation value.
//
// lastContact is nil when tracker holds no fix for the device AT THE EVALUATION INSTANT. That is the
// first and terminating step of the ordered test, so no age comparison is ever performed for a device
// with no fix — which is exactly why a never-reported device cannot come out `stale`, and why a
// device whose last fix retention purging has just deleted becomes `no-position` from that instant
// onward rather than aging into `stale`.
//
// The order is total and the four are mutually exclusive: every device gets exactly one value, never
// two and never none.
//
//  1. no fix                      -> no-position
//  2. age <= LiveSeconds          -> live
//  3. LiveSeconds < age <= Stale  -> recent
//  4. age > StaleSeconds          -> stale
//
// Boundaries are half-open toward freshness: age exactly LiveSeconds is `live`, age exactly
// StaleSeconds is `recent`.
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
// Two details are load-bearing and neither is incidental:
//
//   - The truncation is of the DIFFERENCE, not of each instant. A device last heard from 120.4
//     seconds ago has age 120 and is `live` under the default windows; computing Unix() - Unix()
//     instead would round the two ends independently and hand back 121 for the same instant pair,
//     flipping the boundary case the tests pin.
//   - A negative age — the server clock behind the device's last contact, i.e. clock skew — is ZERO,
//     never a negative number that would sail past every comparison or surface on the wire. The
//     device just reached us; `live` is the honest answer.
//
// The arithmetic is deliberately on seconds rather than time.Duration: a Duration saturates at about
// 292 years, and a staleness window may legally be far larger than that, so a Duration-based
// subtraction could report an ancient fix as young under an extreme (if silly) configuration.
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

// SweepBound is B: how often a surface that publishes time-driven transitions must re-evaluate, in
// seconds, so a device cannot cross a boundary and sit there unannounced.
//
//	B = max(1, floor(min(Live, Stale-Live) / 2))
//
// The min is what makes it safe under a NARROW `recent` band: with windows (5, 10) a device is
// `recent` for only five seconds, so a sweep every 60 seconds could step straight over that state and
// never announce it. B is 2 there and 60 under the defaults. The floor at 1 keeps it a usable period
// for any legal pair, and 1 second is also the stream's poll cadence — so the stream satisfies the
// bound for every configuration by construction, which is the property StreamPollIntervalMeetsBound
// asserts rather than assumes.
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
