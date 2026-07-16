package store

import (
	"context"
	"fmt"
	"time"

	"github.com/NSchatz/tracker/internal/db"
)

// This file is S3's read side: the family-scoped queries behind GET /v1/positions and
// GET /v1/devices/{id}/history. Like FixesNear and GeofencesContaining in store.go, every query
// here carries its family scope IN THE SQL rather than trusting the caller to filter — the worst
// failure this product has (§4, risk path #1) is one family reading another's location, and a
// query that returned everything and left scoping to its caller would be one forgotten line away
// from being that breach. The proximity read reuses FixesNear directly; it is already family-scoped.

// Position is one device's most recent whereabouts: the latest fix tracker holds for it.
type Position struct {
	DeviceID   string
	DeviceName string
	TS         time.Time // the device's event-time clock
	Lon        float64
	Lat        float64
	ReceivedAt time.Time // the server's receive-time — how STALE this position is (§5.2 liveness)
}

// latestPositionsSQL returns the newest fix per device in one family.
//
// DISTINCT ON (f.device_id) with ORDER BY (f.device_id, f.ts DESC) is Postgres's idiom for
// "one row per device, the latest": for each device_id it keeps the first row the sort produces,
// and the sort puts the newest ts first. The (device_id, ts) primary key serves that ordering by a
// backward index walk (the same property TestLatestFixUsesTheKeyIndexBackwards pins), so this does
// not sort the family's whole history.
//
// The outer query re-orders by device name so the API's output is stable and human-meaningful
// rather than in physical device_id order. received_at rides along so a caller can tell a fresh
// position from a stale one.
//
// A device with no fixes yet simply does not appear: it has no latest position. That is the honest
// answer, not a fabricated one, and a family with no fixes at all yields zero rows (an empty list),
// never another family's data.
const latestPositionsSQL = `
	SELECT device_id, name, ts, lon, lat, received_at
	FROM (
		SELECT DISTINCT ON (f.device_id)
		       f.device_id,
		       d.name AS name,
		       f.ts,
		       ST_X(f.location::geometry) AS lon,
		       ST_Y(f.location::geometry) AS lat,
		       f.received_at
		FROM fixes f
		JOIN devices d ON d.id = f.device_id
		WHERE d.family_id = $1
		ORDER BY f.device_id, f.ts DESC
	) latest
	ORDER BY name, device_id`

// LatestPositions returns the most recent fix for every device in the family that has one, ordered
// by device name. Scoped to one family in the SQL (see the file comment).
func LatestPositions(ctx context.Context, q db.Querier, familyID string) ([]Position, error) {
	rows, err := q.Query(ctx, latestPositionsSQL, familyID)
	if err != nil {
		return nil, fmt.Errorf("query latest positions for family %s: %w", familyID, err)
	}
	defer rows.Close()

	var out []Position
	for rows.Next() {
		var p Position
		if err := rows.Scan(&p.DeviceID, &p.DeviceName, &p.TS, &p.Lon, &p.Lat, &p.ReceivedAt); err != nil {
			return nil, fmt.Errorf("scan position: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query latest positions for family %s: %w", familyID, err)
	}
	return out, nil
}

// positionsSinceSQL is the S4 stream's cursor query: the latest position per device in one family
// whose current position ARRIVED after `since`, ordered by that arrival time.
//
// It is latestPositionsSQL with two deliberate differences, and both are what make it a stream
// cursor rather than a snapshot:
//
//   - It filters the latest-per-device result by received_at > $2. received_at is the SERVER's
//     receive-time (§5.2 liveness), and it is the fix's ARRIVAL, not the device's event `ts`. That
//     is the right cursor for a live stream: a phone flushing an offline backlog reports fixes with
//     old `ts` but a fresh received_at, and the stream should push the device's newly-known position
//     when it arrives — keyed on when the server learned it, monotonically.
//   - It orders by received_at ASC (device_id breaks a tie), so a watcher consuming the rows in order
//     can use each row's received_at as a resumable, non-decreasing Last-Event-ID: after a drop it
//     asks for everything strictly newer than the last id it saw, and misses nothing that arrived
//     while it was gone.
//
// received_at here is the received_at OF the latest-by-ts row — a device's CURRENT position — so a
// backlogged older-ts fix arriving later does not spuriously re-emit an unchanged position (its
// received_at is not this row's). The stream therefore pushes a device only when its current
// position actually changes.
//
// Scoped to one family in the SQL, exactly as latestPositionsSQL is: the worst failure this product
// has (§4, risk path #1) is one family seeing another's location, and a stream that trusted its
// caller to filter would be one forgotten line from being that breach — across every open watcher.
const positionsSinceSQL = `
	SELECT device_id, name, ts, lon, lat, received_at
	FROM (
		SELECT DISTINCT ON (f.device_id)
		       f.device_id,
		       d.name AS name,
		       f.ts,
		       ST_X(f.location::geometry) AS lon,
		       ST_Y(f.location::geometry) AS lat,
		       f.received_at
		FROM fixes f
		JOIN devices d ON d.id = f.device_id
		WHERE d.family_id = $1
		ORDER BY f.device_id, f.ts DESC
	) latest
	WHERE latest.received_at > $2
	ORDER BY latest.received_at ASC, device_id ASC`

// PositionsSince returns the latest position per device in the family whose current position arrived
// (received_at) strictly after `since`, ordered by received_at ascending. It is the query the S4 SSE
// stream polls: pass the zero time for a fresh watcher (the current snapshot of everyone) and the
// last received_at delivered to resume. `since` is EXCLUSIVE, so re-passing the last id never
// re-delivers the row that produced it — the property that makes received_at a clean stream cursor.
func PositionsSince(ctx context.Context, q db.Querier, familyID string, since time.Time) ([]Position, error) {
	rows, err := q.Query(ctx, positionsSinceSQL, familyID, since.UTC())
	if err != nil {
		return nil, fmt.Errorf("query positions since %s for family %s: %w", since.UTC().Format(time.RFC3339Nano), familyID, err)
	}
	defer rows.Close()

	var out []Position
	for rows.Next() {
		var p Position
		if err := rows.Scan(&p.DeviceID, &p.DeviceName, &p.TS, &p.Lon, &p.Lat, &p.ReceivedAt); err != nil {
			return nil, fmt.Errorf("scan position: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query positions since %s for family %s: %w", since.UTC().Format(time.RFC3339Nano), familyID, err)
	}
	return out, nil
}

// HistoryFix is one row of a device's location history. The optional metrics are pointers because
// absent must stay absent (§5.2): a missing battery reading is unknown, not 0%, and it is emitted
// as JSON null / omitted rather than fabricated.
type HistoryFix struct {
	TS            time.Time
	Lon           float64
	Lat           float64
	AccuracyM     *float64
	BatteryPct    *int16
	SpeedMPS      *float64
	TriggerReason *string
	ReceivedAt    time.Time
}

// MaxHistoryLimit caps how many fixes one history request may return. Pagination is offset-based, so
// an uncapped limit would let a single request read a device's entire history into memory; the cap
// bounds that. The handler applies a smaller default when the caller does not ask.
const MaxHistoryLimit = 1000

// deviceHistorySQL reads one device's fixes within an optional time window, newest first, paginated.
//
// It is scoped by BOTH family_id and device_id. The handler has already checked that the device is
// in the caller's family (returning 403 otherwise), so the family_id term is defence in depth: even
// if that check were bypassed, this query cannot return another family's fixes. Family scoping lives
// in the SQL, not in the caller.
//
// The window bounds are nullable: a NULL $3 means "no lower bound" and a NULL $4 means "no upper
// bound", so an open-ended request reads from the beginning or up to now without a special query.
// The interval is half-open [from, to) so adjacent windows tile without double-counting a fix on
// the boundary.
//
// ORDER BY ts DESC is the newest-first order a history/log view expects, served by the primary
// key's btree scanned backward (no sort). LIMIT/OFFSET is the pagination §8/S3 asks for; it is
// simple and correct for family-scale history, where a device records at most a fix every few
// seconds.
const deviceHistorySQL = `
	SELECT f.ts,
	       ST_X(f.location::geometry),
	       ST_Y(f.location::geometry),
	       f.accuracy_m, f.battery_pct, f.speed_mps, f.trigger_reason,
	       f.received_at
	FROM fixes f
	JOIN devices d ON d.id = f.device_id
	WHERE d.family_id = $1
	  AND f.device_id = $2
	  AND ($3::timestamptz IS NULL OR f.ts >= $3)
	  AND ($4::timestamptz IS NULL OR f.ts <  $4)
	ORDER BY f.ts DESC
	LIMIT $5 OFFSET $6`

// DeviceHistory returns a page of one device's fixes in [from, to), newest first, scoped to the
// family. A zero from or to means that side is unbounded. limit is clamped to [1, MaxHistoryLimit]
// and a negative offset is treated as 0, so a caller cannot ask for an unbounded or nonsensical page.
func DeviceHistory(ctx context.Context, q db.Querier, familyID, deviceID string, from, to time.Time, limit, offset int) ([]HistoryFix, error) {
	switch {
	case limit < 1:
		limit = 1
	case limit > MaxHistoryLimit:
		limit = MaxHistoryLimit
	}
	if offset < 0 {
		offset = 0
	}

	// A zero time.Time means "unbounded" on that side; pass it to SQL as NULL, not as the year-1
	// zero instant, which would exclude every real fix on the lower bound and every fix on the upper.
	var fromArg, toArg *time.Time
	if !from.IsZero() {
		fromArg = &from
	}
	if !to.IsZero() {
		toArg = &to
	}

	rows, err := q.Query(ctx, deviceHistorySQL, familyID, deviceID, fromArg, toArg, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("query history for device %s: %w", deviceID, err)
	}
	defer rows.Close()

	var out []HistoryFix
	for rows.Next() {
		var h HistoryFix
		if err := rows.Scan(&h.TS, &h.Lon, &h.Lat, &h.AccuracyM, &h.BatteryPct, &h.SpeedMPS, &h.TriggerReason, &h.ReceivedAt); err != nil {
			return nil, fmt.Errorf("scan history fix: %w", err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query history for device %s: %w", deviceID, err)
	}
	return out, nil
}
