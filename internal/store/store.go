// Package store is tracker's data-access layer: the Go surface over the schema in
// internal/db/migrations, and the home of the §5.1 spatial query templates.
//
// # Every spatial query in tracker goes through here
//
// That is the point of the package. The rules in the roadmap's §5.1 — longitude first,
// geography not geometry, ST_DWithin to pre-filter then ST_Distance to rank, ST_Covers for
// containment — are rules about how the SQL is WRITTEN, and a rule about how SQL is written
// is only as good as the number of places SQL is written. Keeping the SQL in this package
// (as named constants the tests EXPLAIN and execute directly) is what makes those rules
// enforceable instead of aspirational.
//
// S1 builds the model and its queries; nothing serves them over the network yet. S2 brings
// the ingestion endpoint and S3 the read API, and both are expected to call in here rather
// than write their own spatial SQL.
package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/NSchatz/tracker/internal/db"
)

// ErrCoordinateOutOfRange is returned for a longitude outside [-180, 180], a latitude outside
// [-90, 90], or a non-finite coordinate.
//
// # This error is the whole reason the validation exists, and it is not defensive padding
//
// PostGIS does not reject an out-of-range coordinate. It COERCES it, with a NOTICE nobody
// reads, and stores the result. Measured against postgis/postgis:16-3.4:
//
//	INSERT ... ST_Point(47.6062, -122.3321, 4326)   -- Seattle's lat/lon, swapped
//	NOTICE: Coordinate values were coerced into range [-180 -90, 180 90] for GEOGRAPHY
//	=> stored at longitude 47.6062, latitude -57.6679
//
// The row lands. It is in range. Every CHECK constraint passes, ST_SRID is 4326, and the
// family's phone is now, according to the database, in the South Atlantic. No constraint can
// catch this after the fact, because by the time a constraint sees the value it is already a
// legal point — the corruption happens inside the cast. The only place it can be caught is
// BEFORE the cast, which is here.
//
// So this is the fail-safe §5.1 requires ("a point with unknown SRID or swapped axis fails …
// never lands"), and deleting it does not make the tests fail loudly — it makes tracker
// quietly wrong, which is the failure mode this project fears most.
var ErrCoordinateOutOfRange = errors.New("coordinate out of range")

// ValidateLonLat rejects anything ST_Point would silently coerce. Longitude first, always —
// the argument order of this function is itself the convention it defends.
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

// Fix is one reported position. Optional fields are pointers because absent must stay absent:
// a missing battery reading is unknown, not 0% (§5.2 — "a missing optional field is absent,
// never fabricated").
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

// insertFixSQL writes one fix.
//
// ST_Point(lon, lat, 4326) — LONGITUDE FIRST, and with an explicit SRID. Both halves matter:
//
//   - ST_MakePoint assigns NO SRID (you get 0), and casting an SRID-0 geometry to geography
//     does not fail — PostGIS assumes 4326 and carries on. So the "safe" habit of wrapping in
//     ST_SetSRID is not a nicety, it is the difference between an assumption and an assertion,
//     and ST_Point(..., 4326) makes it in one call.
//   - The axis order is the classic silent bug, guarded permanently by
//     TestAxisOrderRegression.
const insertFixSQL = `
	INSERT INTO fixes (device_id, ts, location, accuracy_m, battery_pct, speed_mps, trigger_reason, msg_id)
	VALUES ($1, $2, ST_Point($3, $4, 4326)::geography, $5, $6, $7, $8, $9)`

// InsertFix validates the coordinate and stores the fix.
//
// It validates FIRST and returns a typed error, storing nothing — see ErrCoordinateOutOfRange
// for why the database cannot be trusted to do this for us.
func InsertFix(ctx context.Context, q db.Querier, f Fix) error {
	if err := ValidateLonLat(f.Lon, f.Lat); err != nil {
		return err
	}
	_, err := q.Exec(ctx, insertFixSQL,
		f.DeviceID, f.TS, f.Lon, f.Lat,
		f.AccuracyM, f.BatteryPct, f.SpeedMPS, f.TriggerReason, f.MsgID)
	if err != nil {
		return fmt.Errorf("insert fix for device %s: %w", f.DeviceID, err)
	}
	return nil
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

// fixesNearSQL is §5.1's proximity template, and the query TestProximityUsesGiSTIndex runs
// EXPLAIN against — the test EXPLAINs this exact constant, so the plan it asserts is the plan
// production gets.
//
// The two-step shape is not stylistic. ST_DWithin folds in a bounding-box test that GiST can
// answer, so it is the filter; ST_Distance is NOT index-accelerated at all, so it is only ever
// the ranking, applied to the handful of rows ST_DWithin already let through. Ordering by
// ST_Distance without the ST_DWithin pre-filter would read every fix in the family's history
// to answer "who is nearby".
//
// Both distances are computed on geography, so `radius_m` is METRES. On geometry the same
// call would mean 100 DEGREES, and would be wrong by a factor of about 111,000.
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
// family.
//
// Family scoping is in the SQL, not left to the caller: the worst failure this product has
// (§4, risk path #1) is one family reading another's location, and a query that returns
// everything and trusts its caller to filter is one forgotten line away from being that
// breach.
func FixesNear(ctx context.Context, q db.Querier, familyID string, lon, lat, radiusM float64) ([]NearbyFix, error) {
	if err := ValidateLonLat(lon, lat); err != nil {
		return nil, err
	}
	if radiusM < 0 || math.IsNaN(radiusM) {
		return nil, fmt.Errorf("radius must be a non-negative number of metres, got %v", radiusM)
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
// ST_Covers, not ST_Contains, and the difference is a product decision rather than a taste:
// ST_Contains is FALSE for a point exactly on the boundary, so a fix landing precisely on the
// edge of "school" would be reported as NOT at school. ST_Covers is inclusive, so the edge
// counts as inside, and the rule is deterministic and documented (§5.3).
//
// ST_Contains is not even an option here — PostGIS does not define it for geography at all
// (`function st_contains(geography, geography) does not exist`). Reaching for it means casting
// to geometry, which silently drops you back into degree-space.
//
// ST_Covers folds in the same indexable bounding-box test ST_DWithin does, so this is served
// by geofences_area_gist.
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
