// Package store is tracker's data-access layer: the Go surface over the schema in
// internal/db/migrations, and the home of the §5.1 spatial query templates.
//
// EVERY SPATIAL QUERY IN TRACKER GOES THROUGH HERE, and that is the point of the package. §5.1's
// rules - longitude first, geography not geometry, ST_DWithin to pre-filter then ST_Distance to
// rank, ST_Covers for containment - are rules about how the SQL is WRITTEN, and such a rule is only
// as good as the number of places SQL is written. Keeping it here, as named constants the tests
// EXPLAIN and execute directly, is what makes them enforceable rather than aspirational.
package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/NSchatz/tracker/internal/db"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrCoordinateOutOfRange is returned for a longitude outside [-180, 180], a latitude outside
// [-90, 90], or a non-finite coordinate.
//
// PostGIS does not reject an out-of-range coordinate. It COERCES it, with a NOTICE nobody reads, and
// stores the result. Measured against postgis/postgis:16-3.4:
//
//	INSERT ... ST_Point(47.6062, -122.3321, 4326)   -- Seattle's lat/lon, swapped
//	NOTICE: Coordinate values were coerced into range [-180 -90, 180 90] for GEOGRAPHY
//	=> stored at longitude 47.6062, latitude -57.6679
//
// The row lands, in range, every CHECK passing, ST_SRID 4326, and the family's phone is now in the
// South Atlantic. No constraint can catch it after the fact, because the corruption happens inside
// the cast and by the time a constraint sees the value it is a legal point. The only place to catch
// it is BEFORE the cast, which is here. DELETING THIS DOES NOT FAIL A TEST LOUDLY; it makes tracker
// quietly wrong.
var ErrCoordinateOutOfRange = errors.New("coordinate out of range")

// ValidateLonLat rejects anything ST_Point would silently coerce. Longitude first, always: the
// argument order of this function is itself the convention it defends.
func ValidateLonLat(lon, lat float64) error {
	switch {
	case math.IsNaN(lon) || math.IsNaN(lat) || math.IsInf(lon, 0) || math.IsInf(lat, 0):
		return fmt.Errorf("%w: lon=%v lat=%v is not a finite coordinate", ErrCoordinateOutOfRange, lon, lat)
	case lon < -180 || lon > 180:
		return fmt.Errorf("%w: longitude %v is outside [-180, 180]", ErrCoordinateOutOfRange, lon)
	case lat < -90 || lat > 90:
		// The overwhelmingly likely cause is a lon/lat swap: latitudes past ±90 do not exist,
		// but longitudes past ±90 are half the planet.
		return fmt.Errorf("%w: latitude %v is outside [-90, 90] — are lon and lat swapped?", ErrCoordinateOutOfRange, lat)
	}
	return nil
}

// Fix is one reported position. Optional fields are pointers because absent must stay absent: a
// missing battery reading is unknown, not 0% (§5.2).
type Fix struct {
	DeviceID string
	TS       time.Time // the DEVICE's clock: event time, used for ordering and dedup
	Lon      float64
	Lat      float64

	AccuracyM     *float64
	BatteryPct    *int16
	SpeedMPS      *float64
	TriggerReason *string
	MsgID         *string
}

// upsertFixSQL writes one fix, idempotently on its (device_id, ts) identity.
//
// ST_Point(lon, lat, 4326) - LONGITUDE FIRST, and with an explicit SRID. Both halves matter:
//
//   - ST_MakePoint assigns NO SRID (you get 0), and casting an SRID-0 geometry to geography does not
//     fail: PostGIS assumes 4326 and carries on. ST_Point(..., 4326) asserts in one call what
//     ST_SetSRID would otherwise only assume.
//   - The axis order is the classic silent bug, guarded permanently by TestAxisOrderRegression.
//
// ON CONFLICT (device_id, ts) DO NOTHING is the idempotency §5.2 requires: the client's offline queue
// (C2) retries on any uncertain outcome, so a replayed fix must be a no-op rather than a second row
// or an error. received_at is deliberately not in the column list - the database stamps its own
// clock, distinct from the device's ts, and a replay does not move it.
const upsertFixSQL = `
	INSERT INTO fixes (device_id, ts, location, accuracy_m, battery_pct, speed_mps, trigger_reason, msg_id)
	VALUES ($1, $2, ST_Point($3, $4, 4326)::geography, $5, $6, $7, $8, $9)
	ON CONFLICT (device_id, ts) DO NOTHING`

// UpsertFix validates the coordinate and stores the fix idempotently, reporting whether it was a new
// row (true) or an absorbed replay (false). It validates FIRST and stores nothing on failure - see
// ErrCoordinateOutOfRange for why the database cannot be trusted to do this. It does NOT provision
// partitions: an unprovisioned month fails with a missing-partition error, which is IngestFix's job.
func UpsertFix(ctx context.Context, q db.Querier, f Fix) (inserted bool, err error) {
	if err := ValidateLonLat(f.Lon, f.Lat); err != nil {
		return false, err
	}
	tag, err := q.Exec(ctx, upsertFixSQL,
		f.DeviceID, f.TS, f.Lon, f.Lat,
		f.AccuracyM, f.BatteryPct, f.SpeedMPS, f.TriggerReason, f.MsgID)
	if err != nil {
		return false, fmt.Errorf("insert fix for device %s: %w", f.DeviceID, err)
	}
	return tag.RowsAffected() == 1, nil
}

// ErrTimestampOutOfWindow is returned for a fix whose ts is too far from the server's clock to be
// accepted - see IngestFix for why the window exists and what it bounds.
var ErrTimestampOutOfWindow = errors.New("fix timestamp outside the acceptable ingest window")

// The ingest window (see IngestFix). Deliberately generous and deliberately finite.
const (
	// MaxIngestFutureSkew is how far ahead of the server clock a fix's ts may be. A fix from the
	// future is a bad device clock, and 24h absorbs timezone and NTP confusion without admitting one
	// that could provision a partition months ahead of anything real.
	MaxIngestFutureSkew = 24 * time.Hour

	// MaxIngestBacklog is how far into the past a fix's ts may be. A phone offline for a while
	// replays genuinely old fixes (C2), so this is generous; it is finite because an unbounded past
	// would let one hostile device carpet the catalog with a partition per month back to the epoch.
	MaxIngestBacklog = 90 * 24 * time.Hour
)

// IngestFix is the entry point for a fix arriving from the network: UpsertFix plus the two things an
// untrusted, network-facing writer needs that the raw upsert does not.
//
// 1. THE PARTITION-LOOKAHEAD MITIGATION. `fixes` has no default partition and the server provisions
// only a small lookahead at start-up, so a process that stays up longer than the lookahead crosses
// into an unprovisioned month and every INSERT fails with "no partition of relation" - silent, total
// data loss at a month boundary until someone restarts. When the upsert fails that way, IngestFix
// creates the month's partition and retries once, so a long-lived process self-heals on the next fix.
//
// 2. THE TIMESTAMP WINDOW, which is what makes the first one safe. The mitigation creates a partition
// from a client-supplied ts, so an unbounded ts would be an abuse vector: a partition per month,
// forever, from one hostile device. A ts outside [now-MaxIngestBacklog, now+MaxIngestFutureSkew] is
// rejected with a typed error and stored nowhere. `now` is a parameter, not time.Now(), so the
// boundary is testable without waiting for a month to turn over.
func IngestFix(ctx context.Context, q db.Querier, f Fix, now time.Time) (inserted bool, err error) {
	if err := validateFixTimestamp(f.TS, now); err != nil {
		return false, err
	}

	inserted, err = UpsertFix(ctx, q, f)
	if err != nil && isMissingPartitionErr(err) {
		// A DUPLICATE-relation error here is benign and expected under concurrency: another in-flight
		// fix for the same new month may have created the partition between our failed insert and
		// this call, since CREATE TABLE IF NOT EXISTS is not atomic against a concurrent creator. The
		// month is ready either way, so the retry below is the real arbiter.
		if _, perr := db.EnsureMonthlyPartition(ctx, q, f.TS); perr != nil && !isDuplicateRelationErr(perr) {
			return false, fmt.Errorf("provision partition for a fix at %s: %w", f.TS.Format(time.RFC3339), perr)
		}
		inserted, err = UpsertFix(ctx, q, f)
	}
	return inserted, err
}

// validateFixTimestamp enforces the ingest window. A zero or garbage ts (e.g. epoch 0 from a
// missing field that slipped through) lands far in the past and is rejected here too.
func validateFixTimestamp(ts, now time.Time) error {
	switch {
	case ts.After(now.Add(MaxIngestFutureSkew)):
		return fmt.Errorf("%w: %s is more than %s ahead of the server clock", ErrTimestampOutOfWindow, ts.UTC().Format(time.RFC3339), MaxIngestFutureSkew)
	case ts.Before(now.Add(-MaxIngestBacklog)):
		return fmt.Errorf("%w: %s is more than %s in the past", ErrTimestampOutOfWindow, ts.UTC().Format(time.RFC3339), MaxIngestBacklog)
	}
	return nil
}

// isMissingPartitionErr reports whether err is Postgres refusing an INSERT because no partition of
// `fixes` covers the row's month. It matches on the error MESSAGE rather than the SQLSTATE alone,
// because Postgres raises this as a check_violation (23514), the SAME code as a real CHECK failure,
// and retrying THOSE by provisioning a partition would be nonsense.
func isMissingPartitionErr(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && strings.Contains(pgErr.Message, "no partition of relation")
}

// isDuplicateRelationErr reports whether err is Postgres's duplicate_table (42P07). In the mitigation
// path it means a racing ingest already provisioned the month, which is a success. Matching the
// SQLSTATE rather than a message is precise here: 42P07 has exactly one meaning.
func isDuplicateRelationErr(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42P07"
}

// NearbyFix is one row of the "who is near X" answer, with the distance that ranked it.
type NearbyFix struct {
	DeviceID   string
	DeviceName string
	TS         time.Time
	Lon        float64
	Lat        float64
	DistanceM  float64 // metres on the spheroid, because `location` is geography
}

// fixesNearSQL is §5.1's proximity template. TestProximityUsesGiSTIndex EXPLAINs this exact
// constant, so the plan it asserts is the plan production gets.
//
// The two-step shape is not stylistic. ST_DWithin folds in a bounding-box test GiST can answer, so it
// is the filter; ST_Distance is NOT index-accelerated at all, so it is only ever the ranking over the
// handful of rows ST_DWithin let through. Ordering by ST_Distance alone would read every fix in the
// family's history.
//
// Both distances are computed on geography, so `radius_m` is METRES. On geometry the same call would
// mean 100 DEGREES, wrong by a factor of about 111,000.
const fixesNearSQL = `
	SELECT f.device_id, d.name, f.ts,
	       ST_X(f.location::geometry), ST_Y(f.location::geometry),
	       ST_Distance(f.location, ST_Point($2, $3, 4326)::geography)
	FROM fixes f
	JOIN devices d ON d.id = f.device_id
	WHERE d.family_id = $1
	  AND ST_DWithin(f.location, ST_Point($2, $3, 4326)::geography, $4)
	ORDER BY ST_Distance(f.location, ST_Point($2, $3, 4326)::geography)`

// FixesNear answers "who is within radiusM metres of this point", nearest first, scoped to one
// family. THE FAMILY SCOPING IS IN THE SQL, not left to the caller: the worst failure this product
// has (§4, risk path #1) is one family reading another's location, and a query that returns
// everything and trusts its caller to filter is one forgotten line away from being that breach.
func FixesNear(ctx context.Context, q db.Querier, familyID string, lon, lat, radiusM float64) ([]NearbyFix, error) {
	if err := ValidateLonLat(lon, lat); err != nil {
		return nil, err
	}
	// NaN and +Inf both have to go: neither is a number of metres, and both would reach ST_DWithin
	// as a distance rather than being rejected as the caller bug they are.
	if radiusM < 0 || math.IsNaN(radiusM) || math.IsInf(radiusM, 0) {
		return nil, fmt.Errorf("radius must be a finite, non-negative number of metres, got %v", radiusM)
	}

	rows, err := q.Query(ctx, fixesNearSQL, familyID, lon, lat, radiusM)
	if err != nil {
		return nil, fmt.Errorf("query fixes near (%v, %v): %w", lon, lat, err)
	}
	defer rows.Close()

	var out []NearbyFix
	for rows.Next() {
		var n NearbyFix
		if err := rows.Scan(&n.DeviceID, &n.DeviceName, &n.TS, &n.Lon, &n.Lat, &n.DistanceM); err != nil {
			return nil, fmt.Errorf("scan nearby fix: %w", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query fixes near (%v, %v): %w", lon, lat, err)
	}
	return out, nil
}

// Geofence is a server-side "Place".
type Geofence struct {
	ID   string
	Name string
}

// geofencesContainingSQL is §5.3's containment template.
//
// ST_COVERS, NOT ST_CONTAINS, and the difference is a product decision rather than a taste:
// ST_Contains is FALSE for a point exactly on the boundary, so a fix landing precisely on the edge of
// "school" would be reported as NOT at school. ST_Covers is inclusive, so the edge counts as inside
// (§5.3). ST_Contains is not even an option here - PostGIS does not define it for geography at all,
// and reaching for it means casting to geometry, which silently drops you back into degree-space.
//
// ST_Covers folds in the same indexable bounding-box test ST_DWithin does, so this is served by
// geofences_area_gist.
const geofencesContainingSQL = `
	SELECT g.id, g.name
	FROM geofences g
	WHERE g.family_id = $1
	  AND ST_Covers(g.area, ST_Point($2, $3, 4326)::geography)
	ORDER BY g.name`

// GeofencesContaining returns the family's Places that contain the point, boundary inclusive.
func GeofencesContaining(ctx context.Context, q db.Querier, familyID string, lon, lat float64) ([]Geofence, error) {
	if err := ValidateLonLat(lon, lat); err != nil {
		return nil, err
	}

	rows, err := q.Query(ctx, geofencesContainingSQL, familyID, lon, lat)
	if err != nil {
		return nil, fmt.Errorf("query geofences containing (%v, %v): %w", lon, lat, err)
	}
	defer rows.Close()

	var out []Geofence
	for rows.Next() {
		var g Geofence
		if err := rows.Scan(&g.ID, &g.Name); err != nil {
			return nil, fmt.Errorf("scan geofence: %w", err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query geofences containing (%v, %v): %w", lon, lat, err)
	}
	return out, nil
}
