package server

import (
	"sort"
	"strings"
	"time"

	"github.com/NSchatz/tracker/internal/presentation"
	"github.com/NSchatz/tracker/internal/store"
)

// The wire shapes every presentation-carrying surface shares, and the one total order they are
// served in. GET /v1/positions and GET /v1/stream both build their payloads here, so a device is
// described identically whichever surface a viewer reads — which is the property AC32 asserts and the
// reason a browser can trust either one.

// positionResponse is a LOCATED entry: one device that holds at least one fix.
//
// The first six keys are exactly what this endpoint has always emitted, with unchanged names, types,
// units and meaning. This change is purely ADDITIVE — a decoder that requires today's six keys and
// tolerates unknown ones still decodes it — and the two new keys are the addition.
//
// `received_at` and `last_contact_at` are DIFFERENT FACTS and both are here on purpose:
//
//   - `received_at` is the current position row's own arrival, unchanged and still in whatever unit
//     it has always carried (epoch seconds).
//   - `last_contact_at` is max(received_at) over ALL the device's fixes, in epoch SECONDS. It is what
//     `presentation` is computed from.
//
// They differ exactly when a fix arrived that did not become the current position — an offline
// backlog flush, or a phone whose clock runs fast — and that difference is the entire point: the
// device is alive even though the row on display is old.
type positionResponse struct {
	DeviceID   string  `json:"device_id"`
	DeviceName string  `json:"device_name"`
	Lat        float64 `json:"lat"`
	Lon        float64 `json:"lon"`
	TS         int64   `json:"ts"`
	ReceivedAt int64   `json:"received_at"`

	Presentation  presentation.Value `json:"presentation"`
	LastContactAt int64              `json:"last_contact_at"`
}

// unlocatedEntry is the WHOLE wire shape for a device tracker holds no fix for: three keys and
// nothing else.
//
// No `lat`, no `lon`, no `ts`, no `received_at`, no `last_contact_at` — and above all no zeros. A
// struct with omitempty would silently drop a real 0.0 longitude (the Greenwich meridian) and would
// still let a future edit reintroduce a fabricated coordinate; a separate type makes `0,0` for an
// unlocated device UNREPRESENTABLE rather than merely unused. That is what lets the map trust that a
// device it must not plot cannot arrive carrying somewhere to plot it.
type unlocatedEntry struct {
	DeviceID     string             `json:"device_id"`
	DeviceName   string             `json:"device_name"`
	Presentation presentation.Value `json:"presentation"`
}

// presentationUpdate is the payload of a `presentation` event about a device that still HOLDS a
// position: a partial update saying only that its state changed, deliberately carrying no
// coordinates.
//
// It is not a position and must never be treated as one: the device has not moved, the server has
// simply re-evaluated how fresh it is (time passed, or a backlogged fix refreshed its last contact).
// Sending coordinates here would invite a consumer to treat a liveness transition as a new fix.
type presentationUpdate struct {
	DeviceID      string             `json:"device_id"`
	DeviceName    string             `json:"device_name"`
	Presentation  presentation.Value `json:"presentation"`
	LastContactAt int64              `json:"last_contact_at"`
}

// entryFor renders one device as the full entry both GET /v1/positions and the stream's `position`
// event carry: a located entry, or the three-key unlocated one.
//
// The presentation value is evaluated HERE, at the instant the response or event is produced, from
// the device's own last contact — never cached, never persisted, never carried over from a previous
// response.
func entryFor(s store.DeviceState, at time.Time, w presentation.Windows) any {
	value := presentation.Evaluate(at, s.LastContact, w)
	if s.Current == nil || value == presentation.NoPosition {
		// Both halves of the precedence ruling meet here: a device with no fix is `no-position`, and
		// a `no-position` device has no coordinates to publish. Requiring both conditions means a
		// disagreement between them could never emit a located entry labelled `no-position`.
		return unlocatedEntry{
			DeviceID:     s.DeviceID,
			DeviceName:   s.DeviceName,
			Presentation: presentation.NoPosition,
		}
	}
	return positionResponse{
		DeviceID:      s.DeviceID,
		DeviceName:    s.DeviceName,
		Lat:           s.Current.Lat,
		Lon:           s.Current.Lon,
		TS:            s.Current.TS.Unix(),
		ReceivedAt:    s.Current.ReceivedAt.Unix(),
		Presentation:  value,
		LastContactAt: s.LastContact.Unix(),
	}
}

// presentationPayloadFor renders the data of a `presentation` event: the partial update for a device
// that still holds a position, or the same three-key unlocated entry entryFor would emit.
//
// The unlocated branch returning the IDENTICAL shape is what makes a purge-driven transition legible:
// the map receives exactly the object it would get from a fresh read, so "this device no longer has a
// position" needs no second encoding.
func presentationPayloadFor(s store.DeviceState, at time.Time, w presentation.Windows) any {
	value := presentation.Evaluate(at, s.LastContact, w)
	if s.Current == nil || value == presentation.NoPosition {
		return unlocatedEntry{
			DeviceID:     s.DeviceID,
			DeviceName:   s.DeviceName,
			Presentation: presentation.NoPosition,
		}
	}
	return presentationUpdate{
		DeviceID:      s.DeviceID,
		DeviceName:    s.DeviceName,
		Presentation:  value,
		LastContactAt: s.LastContact.Unix(),
	}
}

// sortDeviceStates puts a family's devices into the ONE total order every entry is served in —
// located and unlocated together, never partitioned so that the phones nobody has set up yet trail
// the ones that work.
//
// The order is:
//
//  1. device name, compared case-INSENSITIVELY under Unicode simple lowercase mapping, so `APPLE`
//     and `apple` sit together rather than in two separate ASCII runs;
//  2. for equal folded names, the RAW name in UTF-8 byte order — a deterministic tiebreak, so
//     `APPLE` always precedes `apple` and the answer never depends on which row the planner
//     returned first;
//  3. `device_id` ascending, byte order — the final tiebreak, which two devices sharing a name
//     reach, and which no two devices can tie on.
//
// It is done in Go rather than in the SQL because `lower()` and `ORDER BY` in Postgres are decided by
// the database's COLLATION, a property of the deployment. Two tracker instances must serve the same
// family in the same order, and a total order defined in the process is how that is guaranteed —
// which is also what makes two consecutive requests byte-identical.
func sortDeviceStates(states []store.DeviceState) {
	sort.SliceStable(states, func(i, j int) bool {
		a, b := states[i], states[j]
		if folded, other := strings.ToLower(a.DeviceName), strings.ToLower(b.DeviceName); folded != other {
			return folded < other
		}
		if a.DeviceName != b.DeviceName {
			return a.DeviceName < b.DeviceName
		}
		return a.DeviceID < b.DeviceID
	})
}

// sortByArrival orders devices by when their CURRENT POSITION arrived, oldest first, with the device
// id breaking a tie.
//
// This is the stream's order and it is not cosmetic: each `position` event's id is its arrival in
// microseconds, so emitting them in arrival order is what makes the id a non-decreasing cursor a
// client can resume from. Devices holding no position sort first and are never given an id.
func sortByArrival(states []store.DeviceState) {
	sort.SliceStable(states, func(i, j int) bool {
		a, b := states[i], states[j]
		switch {
		case a.Current == nil && b.Current == nil:
			return a.DeviceID < b.DeviceID
		case a.Current == nil:
			return true
		case b.Current == nil:
			return false
		}
		if !a.Current.ReceivedAt.Equal(b.Current.ReceivedAt) {
			return a.Current.ReceivedAt.Before(b.Current.ReceivedAt)
		}
		return a.DeviceID < b.DeviceID
	})
}
