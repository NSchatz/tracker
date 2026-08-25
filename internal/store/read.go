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

// The "latest fix per device" read and the stream's cursor query used to live here as two separate
// SQL statements. Both are gone: FamilyDeviceStates (presentation.go) is now the single query behind
// GET /v1/positions AND GET /v1/stream, because it has to enumerate devices that hold NO fix — which
// a query driven by the fixes table cannot do — and because two statements that each decide "which
// fix is current" are two statements that can disagree about it. The current position is still the
// newest fix by the device's event-time `ts`; that definition did not change with this work.

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
