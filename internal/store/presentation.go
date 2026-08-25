package store

import (
	"context"
	"fmt"
	"time"

	"github.com/NSchatz/tracker/internal/db"
)

// The reads behind the presentation surface: every device in a family — including the ones that have
// never reported — with its current position (if any) and its LAST CONTACT.
//
// Like every other query in this package the family scope lives IN THE SQL rather than in the caller.
// It matters more here than anywhere: enumerating never-reported devices is a NEW disclosure surface
// (a device id and a human-chosen name for a phone that has produced no data at all), and the worst
// failure this product has is one family reading another's. A query that returned every family's
// devices and left the filtering to a handler would be one forgotten line away from being that.

// DeviceState is one device's whole presentation input: who it is, where it is if anywhere, and when
// it last reached the server.
//
// Both optional halves are POINTERS because absent must stay absent (§5.2). A device that has never
// reported has no position and no last contact — not a zero coordinate, not the epoch — and the wire
// shape for it carries no location keys at all. `0,0` is Null Island, a real place off the coast of
// Ghana, and a map that plots a marker there because a struct field defaulted is the exact failure
// this shape makes unrepresentable.
type DeviceState struct {
	DeviceID   string
	DeviceName string

	// Current is the device's current position — the fix with the newest device event-time (`ts`),
	// the same "latest fix per device" GET /v1/positions has always served. nil when tracker holds no
	// fix for this device.
	Current *Position

	// LastContact is max(received_at) across ALL the device's fixes: the LARGEST server receive time,
	// an aggregate over the whole set. It is NOT Current.ReceivedAt, which is one row selected by the
	// device's own clock. They differ exactly when a fix arrived that did not become the current
	// position — an offline backlog flush, or a phone whose clock runs fast — and in those cases
	// Current.ReceivedAt is old while the device is plainly alive. nil when the device holds no fix.
	LastContact *time.Time
}

// familyDeviceStatesSQL enumerates a family's devices and left-joins each one's current position and
// last contact.
//
// LEFT JOIN LATERAL, not a JOIN: the devices table drives the result, so a device with no fixes at
// all still produces a row (with NULLs), which is the behavioural change this whole surface rests on
// — a never-reported phone used to be invisible here.
//
// The two laterals answer two different questions and cannot be collapsed into one:
//
//   - `cur` is ORDER BY f.ts DESC LIMIT 1 — the newest fix by the DEVICE's clock. That is the
//     definition of "current position" and it is unchanged by this work; the coordinates a viewer
//     sees do not move. The (device_id, ts) primary key serves it by a backward index walk.
//   - `lc` is max(f.received_at) — the newest fix by the SERVER's clock, over the whole set. This is
//     the one liveness may be computed from.
//
// Ordering is deliberately NOT in the SQL: see FamilyDeviceStates.
const familyDeviceStatesSQL = `
	SELECT d.id,
	       d.name,
	       cur.ts, cur.lon, cur.lat, cur.received_at,
	       lc.last_contact
	FROM devices d
	LEFT JOIN LATERAL (
		SELECT f.ts,
		       ST_X(f.location::geometry) AS lon,
		       ST_Y(f.location::geometry) AS lat,
		       f.received_at
		FROM fixes f
		WHERE f.device_id = d.id
		ORDER BY f.ts DESC
		LIMIT 1
	) cur ON true
	LEFT JOIN LATERAL (
		SELECT max(f.received_at) AS last_contact
		FROM fixes f
		WHERE f.device_id = d.id
	) lc ON true
	WHERE d.family_id = $1`

// FamilyDeviceStates returns every device in the family, each with its current position and last
// contact when it has any. A family with no devices returns no rows; a family whose devices have
// never reported returns one row per device with both optional halves nil.
//
// The rows come back UNORDERED on purpose. The API's total order is defined over Unicode simple
// lowercase folding with a raw-name byte-order tiebreak, and expressing that in SQL would make the
// output depend on the database's collation — which is a property of the deployment, not of the
// contract. Ordering in Go (see the handler) is what makes two consecutive requests byte-identical on
// any host.
func FamilyDeviceStates(ctx context.Context, q db.Querier, familyID string) ([]DeviceState, error) {
	rows, err := q.Query(ctx, familyDeviceStatesSQL, familyID)
	if err != nil {
		return nil, fmt.Errorf("query device states for family %s: %w", familyID, err)
	}
	defer rows.Close()

	var out []DeviceState
	for rows.Next() {
		var (
			s           DeviceState
			ts          *time.Time
			lon, lat    *float64
			receivedAt  *time.Time
			lastContact *time.Time
		)
		if err := rows.Scan(&s.DeviceID, &s.DeviceName, &ts, &lon, &lat, &receivedAt, &lastContact); err != nil {
			return nil, fmt.Errorf("scan device state: %w", err)
		}
		// All four position columns come from one lateral row, so they are NULL together or present
		// together; requiring all of them is defence against a future query shape that is not.
		if ts != nil && lon != nil && lat != nil && receivedAt != nil {
			s.Current = &Position{
				DeviceID:   s.DeviceID,
				DeviceName: s.DeviceName,
				TS:         *ts,
				Lon:        *lon,
				Lat:        *lat,
				ReceivedAt: *receivedAt,
			}
		}
		s.LastContact = lastContact
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query device states for family %s: %w", familyID, err)
	}
	return out, nil
}

// familyLastContactAsOfSQL is the same "last contact" aggregate, but as it stood at a past instant:
// max(received_at) over only the fixes that had ARRIVED by then.
//
// A device with no fix at or before that instant produces NO ROW — not a zero time — which is the
// distinction the resume sweep is built on: "this device had no state yet when you disconnected" is
// not the same fact as "this device was no-position", and only the first one means the client has
// never been told anything about it.
const familyLastContactAsOfSQL = `
	SELECT f.device_id, max(f.received_at)
	FROM fixes f
	JOIN devices d ON d.id = f.device_id
	WHERE d.family_id = $1
	  AND f.received_at <= $2
	GROUP BY f.device_id`

// FamilyLastContactAsOf returns each device's last contact AS IT STOOD at `instant`, keyed by device
// id, for the family. Devices holding no fix at or before that instant are absent from the map.
//
// It is recomputed from the stored fixes on every call and remembers nothing about any client, which
// is what makes a resume idempotent: two resumes on the same cursor read the same rows and therefore
// emit the same set.
func FamilyLastContactAsOf(ctx context.Context, q db.Querier, familyID string, instant time.Time) (map[string]time.Time, error) {
	rows, err := q.Query(ctx, familyLastContactAsOfSQL, familyID, instant.UTC())
	if err != nil {
		return nil, fmt.Errorf("query last contact as of %s for family %s: %w",
			instant.UTC().Format(time.RFC3339Nano), familyID, err)
	}
	defer rows.Close()

	out := make(map[string]time.Time)
	for rows.Next() {
		var (
			deviceID string
			last     time.Time
		)
		if err := rows.Scan(&deviceID, &last); err != nil {
			return nil, fmt.Errorf("scan last contact: %w", err)
		}
		out[deviceID] = last
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query last contact as of %s for family %s: %w",
			instant.UTC().Format(time.RFC3339Nano), familyID, err)
	}
	return out, nil
}
