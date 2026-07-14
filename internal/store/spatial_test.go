// Package store's tests are the correctness bedrock (roadmap §5.1). They run against a REAL
// PostGIS — never a mock — because the thing under test IS PostGIS's behaviour: a fake would
// only ever assert what we already believed, and what we believed is exactly what these tests
// keep catching.
//
// They are internal (package store, not store_test) for one reason: the EXPLAIN tests assert
// the plan of the SQL CONSTANTS the production helpers execute. Retyping the query in the test
// would prove that some query uses the index, not that ours does.
package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/NSchatz/tracker/internal/db"
	"github.com/NSchatz/tracker/internal/testsupport"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Golden values. Every one of these is an INDEPENDENTLY KNOWN quantity — a published WGS84
// constant or the figure in PostGIS's own documentation — not a number this test learned by
// asking the database what it thought. A golden test that records the answer it was given
// proves only that the answer is stable, including when it is stably wrong.
const (
	// The PostGIS documentation's own worked example (and the one in this repo's CLAUDE.md):
	// Los Angeles to Paris, on the spheroid, is 9,124,665 metres.
	laLon, laLat       = -118.4079, 33.9434
	parisLon, parisLat = 2.5559, 49.0083
	laToParisMetres    = 9124665.27

	// WGS84: one degree of LATITUDE at the equator is ~110,574 m, one degree of LONGITUDE is
	// ~111,319 m. The fact that these DIFFER is the signature of the spheroid — on a sphere
	// they are necessarily identical. That asymmetry is what the model assertion below rests
	// on, and it is also why they make such a good axis-order fixture.
	degreeOfLatitudeMetres  = 110574.39
	degreeOfLongitudeMetres = 111319.49

	// On a perfect sphere, both of the above collapse to the same number.
	degreeOnSphereMetres = 111195.08
)

// Rome, and Rome with its coordinates swapped. The swap is not a small error: it lands in
// Sudan, 4,327 km away. That distance is the entire value of an asymmetric fixture — a swap
// cannot hide inside a rounding tolerance.
const (
	romeLon, romeLat = 12.4964, 41.9028
	romeSwapDistance = 4327768.17
)

// TestGoldenDistances pins the distance model itself: metres on the spheroid, via geography.
func TestGoldenDistances(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool := setup(ctx, t)

	t.Run("geography returns metres on the spheroid", func(t *testing.T) {
		got := scalar[float64](ctx, t, pool,
			`SELECT ST_Distance(ST_Point($1,$2,4326)::geography, ST_Point($3,$4,4326)::geography)`,
			laLon, laLat, parisLon, parisLat)

		assertClose(t, "LA→Paris on the spheroid", got, laToParisMetres, 1)
	})

	t.Run("geometry returns DEGREES, which is why we never use it", func(t *testing.T) {
		// The trap, pinned. The same two points through `geometry` come back as 121.90 —
		// not metres, not kilometres, not anything you can act on, but still a number that
		// looks like an answer. If somebody ever "optimises" a query by dropping ::geography,
		// this is the test that explains what they just did.
		got := scalar[float64](ctx, t, pool,
			`SELECT ST_Distance(ST_Point($1,$2,4326), ST_Point($3,$4,4326))`,
			laLon, laLat, parisLon, parisLat)

		if got > 1000 {
			t.Fatalf("ST_Distance on geometry returned %v; expected a value in DEGREES (~121.9). "+
				"If this now returns metres, the geometry/geography distinction has changed and "+
				"every spatial query in this package needs re-reading.", got)
		}
		assertClose(t, "LA→Paris on geometry (degrees!)", got, 121.898, 0.01)

		// And the thing that makes it dangerous: as a distance in metres it is off by a
		// factor of ~75,000, but as a bare float it is perfectly plausible.
		if math.Abs(got-laToParisMetres) < 1 {
			t.Fatal("geometry and geography agree, which is impossible if the units differ")
		}
	})

	t.Run("the model is the SPHEROID, not a sphere", func(t *testing.T) {
		// This is the assertion §6 asks for ("assert the model you use"), and it does not
		// depend on any single magic number: on the spheroid a degree of latitude and a degree
		// of longitude are DIFFERENT lengths (110,574 m vs 111,319 m — the planet is not
		// round). On a sphere they are identical, by definition. So the asymmetry itself
		// proves which model answered.
		lat := scalar[float64](ctx, t, pool,
			`SELECT ST_Distance(ST_Point(0,0,4326)::geography, ST_Point(0,1,4326)::geography)`)
		lon := scalar[float64](ctx, t, pool,
			`SELECT ST_Distance(ST_Point(0,0,4326)::geography, ST_Point(1,0,4326)::geography)`)

		assertClose(t, "one degree of latitude", lat, degreeOfLatitudeMetres, 0.5)
		assertClose(t, "one degree of longitude at the equator", lon, degreeOfLongitudeMetres, 0.5)

		if math.Abs(lon-lat) < 500 {
			t.Fatalf("a degree of latitude (%v m) and a degree of longitude (%v m) are within "+
				"500 m of each other — that is SPHERE behaviour. The default must be the "+
				"spheroid; use_spheroid has been turned off somewhere.", lat, lon)
		}

		// The control: ask for the sphere explicitly and the asymmetry vanishes.
		latSphere := scalar[float64](ctx, t, pool,
			`SELECT ST_Distance(ST_Point(0,0,4326)::geography, ST_Point(0,1,4326)::geography, false)`)
		lonSphere := scalar[float64](ctx, t, pool,
			`SELECT ST_Distance(ST_Point(0,0,4326)::geography, ST_Point(1,0,4326)::geography, false)`)

		assertClose(t, "one degree on the sphere (lat)", latSphere, degreeOnSphereMetres, 0.5)
		assertClose(t, "one degree on the sphere (lon)", lonSphere, degreeOnSphereMetres, 0.5)
		if math.Abs(lonSphere-latSphere) > 0.001 {
			t.Fatalf("on use_spheroid=false a degree of latitude (%v) and longitude (%v) must be "+
				"equal; they are not, so this is not the spherical model", latSphere, lonSphere)
		}
	})
}

// TestAxisOrderRegression is the permanent guard §5.1 demands. Never delete it.
//
// A lon/lat swap is the classic silent bug in this domain: it does not crash, it does not warn,
// it just puts the family somewhere else. Rome (12.50 E, 41.90 N) swapped becomes (41.90 E,
// 12.50 N) — Sudan, 4,327 km away — and the only thing that ever notices is a test like this.
func TestAxisOrderRegression(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool := setup(ctx, t)
	familyID, deviceID := seedDevice(ctx, t, pool, "axis-order")

	ts := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	mustEnsurePartition(ctx, t, pool, ts)

	if err := InsertFix(ctx, pool, Fix{DeviceID: deviceID, TS: ts, Lon: romeLon, Lat: romeLat}); err != nil {
		t.Fatalf("InsertFix: %v", err)
	}

	// 1. The stored point's X is the LONGITUDE and its Y is the LATITUDE. In PostGIS, X is
	//    always the first ordinate — so if InsertFix ever wrote ST_Point(lat, lon), X would
	//    come back as 41.9.
	gotLon := scalar[float64](ctx, t, pool, `SELECT ST_X(location::geometry) FROM fixes WHERE device_id = $1`, deviceID)
	gotLat := scalar[float64](ctx, t, pool, `SELECT ST_Y(location::geometry) FROM fixes WHERE device_id = $1`, deviceID)

	assertClose(t, "stored longitude (X)", gotLon, romeLon, 1e-9)
	assertClose(t, "stored latitude (Y)", gotLat, romeLat, 1e-9)

	// 2. The distance assertion, which is the one that would fail LOUDLY on a swap. The stored
	//    fix must be on top of Rome (0 m) and 4,327 km from swapped-Rome. A swap inverts both.
	toRome := scalar[float64](ctx, t, pool,
		`SELECT ST_Distance(location, ST_Point($2,$3,4326)::geography) FROM fixes WHERE device_id = $1`,
		deviceID, romeLon, romeLat)
	toSwapped := scalar[float64](ctx, t, pool,
		`SELECT ST_Distance(location, ST_Point($2,$3,4326)::geography) FROM fixes WHERE device_id = $1`,
		deviceID, romeLat, romeLon) // deliberately swapped

	if toRome > 0.001 {
		t.Fatalf("the fix stored for Rome is %v m from Rome — lon and lat have been swapped somewhere "+
			"in InsertFix or in the schema", toRome)
	}
	assertClose(t, "distance from Rome to swapped-Rome", toSwapped, romeSwapDistance, 1)

	// 3. And through the production query path: a 100 m search at Rome finds it, a 100 m search
	//    at swapped-Rome does not.
	near, err := FixesNear(ctx, pool, familyID, romeLon, romeLat, 100)
	if err != nil {
		t.Fatalf("FixesNear(Rome): %v", err)
	}
	if len(near) != 1 {
		t.Fatalf("FixesNear at Rome found %d fixes, want 1 — the query's axis order disagrees with the writer's", len(near))
	}

	nearSwapped, err := FixesNear(ctx, pool, familyID, romeLat, romeLon, 100)
	if err != nil {
		t.Fatalf("FixesNear(swapped): %v", err)
	}
	if len(nearSwapped) != 0 {
		t.Fatalf("FixesNear at SWAPPED Rome found %d fixes, want 0 — a swapped query is finding the "+
			"fix, which means the write side is swapped too and the two bugs are cancelling out", len(nearSwapped))
	}
}

// TestOutOfRangeCoordinatesNeverLand proves the fail-safe: a swapped-axis coordinate whose
// latitude is out of range is rejected with a typed error and stores NOTHING.
//
// The second half of this test is the important half. It proves that PostGIS would NOT have
// stopped us — it silently coerces the bad coordinate into range and stores a wrong position —
// which is the entire justification for ValidateLonLat existing. Without this control, the
// validation looks like belt-and-braces that a future cleanup could delete "because the
// database checks it anyway". The database does not check it. It launders it.
func TestOutOfRangeCoordinatesNeverLand(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool := setup(ctx, t)
	_, deviceID := seedDevice(ctx, t, pool, "out-of-range")

	ts := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	mustEnsurePartition(ctx, t, pool, ts)

	// Seattle (-122.3321, 47.6062) with the axes swapped: latitude -122.3321 cannot exist.
	const swappedLon, swappedLat = 47.6062, -122.3321

	err := InsertFix(ctx, pool, Fix{DeviceID: deviceID, TS: ts, Lon: swappedLon, Lat: swappedLat})
	if !errors.Is(err, ErrCoordinateOutOfRange) {
		t.Fatalf("InsertFix with latitude %v returned %v; want ErrCoordinateOutOfRange", swappedLat, err)
	}

	if n := scalar[int64](ctx, t, pool, `SELECT count(*) FROM fixes WHERE device_id = $1`, deviceID); n != 0 {
		t.Fatalf("a rejected fix left %d rows behind; a typed error must never store a guess", n)
	}

	// The control. Hand the same coordinate straight to PostGIS and watch it NOT fail: the
	// latitude is silently folded back into range, and the "position" that lands is 47.6E,
	// 57.7S — the South Atlantic, ~11,000 km from Seattle.
	coercedLat := scalar[float64](ctx, t, pool,
		`SELECT ST_Y(ST_Point($1,$2,4326)::geography::geometry)`, swappedLon, swappedLat)

	if coercedLat == swappedLat {
		t.Fatalf("PostGIS preserved the out-of-range latitude %v; this test's premise is wrong", swappedLat)
	}
	if coercedLat < -90 || coercedLat > 90 {
		t.Fatalf("coerced latitude %v is still out of range", coercedLat)
	}
	assertClose(t, "the latitude PostGIS silently invented", coercedLat, -57.6679, 0.001)

	// Non-finite coordinates are rejected before they can reach the cast at all.
	for _, bad := range []struct {
		name     string
		lon, lat float64
	}{
		{"NaN latitude", romeLon, math.NaN()},
		{"Inf longitude", math.Inf(1), romeLat},
		{"longitude past the antimeridian", 180.5, romeLat},
	} {
		if err := InsertFix(ctx, pool, Fix{DeviceID: deviceID, TS: ts, Lon: bad.lon, Lat: bad.lat}); !errors.Is(err, ErrCoordinateOutOfRange) {
			t.Fatalf("InsertFix(%s) returned %v; want ErrCoordinateOutOfRange", bad.name, err)
		}
	}
}

// TestSameSRIDAssertion covers §5.1's "same-SRID assertion", and the trap next door to it.
func TestSameSRIDAssertion(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool := setup(ctx, t)
	_, deviceID := seedDevice(ctx, t, pool, "srid")

	ts := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	mustEnsurePartition(ctx, t, pool, ts)

	if err := InsertFix(ctx, pool, Fix{DeviceID: deviceID, TS: ts, Lon: romeLon, Lat: romeLat}); err != nil {
		t.Fatalf("InsertFix: %v", err)
	}

	// Everything stored is 4326. Nothing else can be.
	if srid := scalar[int32](ctx, t, pool, `SELECT ST_SRID(location) FROM fixes WHERE device_id = $1`, deviceID); srid != 4326 {
		t.Fatalf("stored fix has SRID %d, want 4326", srid)
	}

	// A PROJECTED point (Web Mercator, 3857) is refused by the column outright — metres-east-of
	// -Greenwich cannot masquerade as a longitude.
	_, err := pool.Exec(ctx,
		`INSERT INTO fixes (device_id, ts, location) VALUES ($1, $2, ST_Transform(ST_Point($3,$4,4326), 3857)::geography)`,
		deviceID, ts.Add(time.Minute), romeLon, romeLat)
	if err == nil {
		t.Fatal("a 3857 point was accepted into a geography(Point,4326) column; the SRID is not being enforced")
	}
	if !strings.Contains(err.Error(), "lon/lat") {
		t.Fatalf("a 3857 point was rejected, but not for the reason expected: %v", err)
	}

	// The trap, pinned so nobody "simplifies" ST_Point(lon,lat,4326) into ST_MakePoint(lon,lat):
	// ST_MakePoint produces SRID 0, and casting SRID 0 to geography does NOT fail — PostGIS
	// just ASSUMES 4326. So the unsafe call is indistinguishable from the safe one right up
	// until the day the input is not actually 4326, and then it is silently wrong.
	if srid := scalar[int32](ctx, t, pool, `SELECT ST_SRID(ST_MakePoint($1,$2))`, romeLon, romeLat); srid != 0 {
		t.Fatalf("ST_MakePoint now assigns SRID %d; it has always assigned 0, and insertFixSQL's "+
			"use of ST_Point(...,4326) is written around that", srid)
	}
	if srid := scalar[int32](ctx, t, pool, `SELECT ST_SRID(ST_MakePoint($1,$2)::geography)`, romeLon, romeLat); srid != 4326 {
		t.Fatalf("casting an SRID-0 geometry to geography produced SRID %d; expected PostGIS to "+
			"silently assume 4326 (that assumption is the trap this test documents)", srid)
	}
}

// TestGeofenceBoundaryContainment covers §5.1's on-edge containment (ST_Intersects/ST_Covers vs
// ST_Contains) — and pins a finding that the roadmap did not anticipate.
//
// # What "the edge" means on a geography
//
// A geography polygon's edges are GEODESICS (great-circle arcs), not straight lines in lon/lat.
// For a "square" drawn on a lat/lon grid, that has a consequence with real, silent, metre-scale
// teeth: the north/south edges follow PARALLELS, and a parallel is NOT a great circle. The
// geodesic between two points on the 41°N parallel bulges NORTH of it — here by ~120 m at the
// midpoint. So a fix sitting exactly on the drawn parallel edge is genuinely OUTSIDE the
// geography polygon, and PostGIS is right to say so.
//
// The east/west edges are meridians, and a meridian IS a great circle, so a point on one of
// those is exactly on the boundary — which is where the inclusive/exclusive distinction the
// roadmap asks about actually shows up.
//
// S5 (geofence enter/exit) inherits this. A "Place" drawn as a lat/lon rectangle is not the
// region its corners imply, and the discrepancy grows with the box's east-west span.
func TestGeofenceBoundaryContainment(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool := setup(ctx, t)
	familyID, _ := seedDevice(ctx, t, pool, "boundary")

	// A 1° × 1° "square" around Rome, drawn on the lat/lon grid.
	const (
		west, east   = 12.0, 13.0
		south, north = 41.0, 42.0
	)
	fenceID, err := CreateGeofence(ctx, pool, familyID, "square", []Point{
		{Lon: west, Lat: south}, {Lon: east, Lat: south},
		{Lon: east, Lat: north}, {Lon: west, Lat: north},
	})
	if err != nil {
		t.Fatalf("CreateGeofence: %v", err)
	}

	t.Run("a point strictly inside is contained", func(t *testing.T) {
		assertFences(ctx, t, pool, familyID, 12.5, 41.5, []string{fenceID})
	})

	t.Run("a point strictly outside is not", func(t *testing.T) {
		assertFences(ctx, t, pool, familyID, 11.0, 41.5, nil)
	})

	t.Run("on a MERIDIAN edge: ST_Covers says inside, ST_Contains says outside", func(t *testing.T) {
		// The west edge runs along a meridian, so it IS a geodesic, so this point is exactly
		// on the boundary. This is the case §5.1 is about.
		const onEdgeLon, onEdgeLat = west, 41.5

		// The production rule (ST_Covers, inclusive): the fix counts as inside the Place.
		assertFences(ctx, t, pool, familyID, onEdgeLon, onEdgeLat, []string{fenceID})

		// The same point through the OTHER operators, to pin exactly what differs:
		covers := scalar[bool](ctx, t, pool,
			`SELECT ST_Covers(area, ST_Point($2,$3,4326)::geography) FROM geofences WHERE id = $1`,
			fenceID, onEdgeLon, onEdgeLat)
		intersects := scalar[bool](ctx, t, pool,
			`SELECT ST_Intersects(area, ST_Point($2,$3,4326)::geography) FROM geofences WHERE id = $1`,
			fenceID, onEdgeLon, onEdgeLat)
		// ST_Contains does not exist for geography at all, so this is the geometry cast — the
		// one a well-meaning developer reaches for, and the one that gets the answer wrong.
		contains := scalar[bool](ctx, t, pool,
			`SELECT ST_Contains(area::geometry, ST_Point($2,$3,4326)) FROM geofences WHERE id = $1`,
			fenceID, onEdgeLon, onEdgeLat)

		if !covers {
			t.Error("ST_Covers is FALSE on the boundary; the inclusive rule (§5.3) is not holding")
		}
		if !intersects {
			t.Error("ST_Intersects is FALSE on the boundary")
		}
		if contains {
			t.Error("ST_Contains is TRUE on the boundary — PostGIS's documented behaviour is FALSE, " +
				"and the whole reason this project uses ST_Covers is that difference")
		}
	})

	t.Run("ST_Contains(geography, geography) does not exist", func(t *testing.T) {
		// Pinned because it is the reason the rule is not simply "use ST_Contains carefully":
		// there is no such function, so using it REQUIRES a cast to geometry, which is a
		// one-way door back into degree-space.
		var b bool
		err := pool.QueryRow(ctx,
			`SELECT ST_Contains(area, ST_Point(12.5,41.5,4326)::geography) FROM geofences WHERE id = $1`,
			fenceID).Scan(&b)
		if err == nil {
			t.Fatal("ST_Contains(geography, geography) resolved; this test's premise (and a comment " +
				"in store.go) is now wrong")
		}
		if !strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("expected an undefined-function error, got: %v", err)
		}
	})

	t.Run("on a PARALLEL edge: the point is OUTSIDE, because the edge is a geodesic", func(t *testing.T) {
		// The finding. The drawn south edge runs along the 41°N parallel; the actual geodesic
		// edge bows north of it. A fix on the drawn line is therefore ~120 m outside the Place.
		//
		// This is not a bug to fix — it is what a geography polygon MEANS — but it is a
		// property S5 must build on knowingly, so it is pinned here rather than discovered in
		// production when somebody's house on the south edge of "home" never triggers an
		// arrival alert.
		const onParallelLon, onParallelLat = 12.5, south

		assertFences(ctx, t, pool, familyID, onParallelLon, onParallelLat, nil)

		gap := scalar[float64](ctx, t, pool,
			`SELECT ST_Distance(area, ST_Point($2,$3,4326)::geography) FROM geofences WHERE id = $1`,
			fenceID, onParallelLon, onParallelLat)
		if gap < 1 {
			t.Fatalf("the midpoint of the drawn parallel edge is %v m from the polygon; expected the "+
				"geodesic to bow measurably north of the parallel", gap)
		}
		assertClose(t, "how far the geodesic edge bows north of the drawn parallel", gap, 119.96, 1)

		// And just north of it, inside again — the discrepancy is metre-scale, not degree-scale.
		assertFences(ctx, t, pool, familyID, onParallelLon, south+0.01, []string{fenceID})
	})
}

// TestProximityUsesGiSTIndex is §5.1's "EXPLAIN asserts GiST usage on the proximity query".
//
// It EXPLAINs fixesNearSQL — the actual constant FixesNear executes — because an index that
// exists is not an index that is USED. Without the GiST scan, "who is near the school" reads
// the family's entire location history, and it does so silently: correct answers, growing
// slower every month, until the map stops loading.
func TestProximityUsesGiSTIndex(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool := setup(ctx, t)
	familyID, deviceID := seedDevice(ctx, t, pool, "proximity")

	month := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	partition := mustEnsurePartition(ctx, t, pool, month)

	// Enough rows that a sequential scan is not simply the cheapest honest plan. A GiST
	// assertion against a 5-row table proves nothing: the planner would (correctly) seq-scan
	// it, and we would have "fixed" that by disabling seqscan, which asserts the index CAN be
	// used, not that it IS.
	seedGrid(ctx, t, pool, deviceID, month, 2000)

	// The planner needs statistics before it can prefer anything. ANALYZE on the parent
	// cascades to the partitions.
	if _, err := pool.Exec(ctx, `ANALYZE fixes`); err != nil {
		t.Fatalf("ANALYZE: %v", err)
	}

	gistIndex := partitionIndex(ctx, t, pool, partition, "gist")

	// A point the grid actually covers (seedGrid lays fixes on a 0.01° lattice from 12.00E to
	// 12.99E and 41.00N to 41.20N), with a radius wide enough to catch its east/west neighbours
	// — 0.01° of longitude at this latitude is ~840 m — so the ordering assertion below has
	// more than one row to order.
	const (
		targetLon, targetLat = 12.5, 41.1
		radiusM              = 1000.0
	)

	plan := explain(ctx, t, pool, fixesNearSQL, familyID, targetLon, targetLat, radiusM)

	if !strings.Contains(plan, gistIndex) {
		t.Fatalf("the proximity query does not use the GiST index %q. Plan:\n%s", gistIndex, plan)
	}
	if !strings.Contains(plan, "Index Scan") && !strings.Contains(plan, "Bitmap Heap Scan") {
		t.Fatalf("the proximity query is not using an index scan at all. Plan:\n%s", plan)
	}
	if strings.Contains(plan, "Seq Scan on fixes") {
		t.Fatalf("the proximity query sequentially scans fixes. Plan:\n%s", plan)
	}

	// The plan is only half the claim; the ANSWER has to be right too. A fast wrong answer is
	// the failure this project ranks second-worst (§4).
	near, err := FixesNear(ctx, pool, familyID, targetLon, targetLat, radiusM)
	if err != nil {
		t.Fatalf("FixesNear: %v", err)
	}
	if len(near) < 2 {
		t.Fatalf("FixesNear found %d fixes within %v m of a point the grid covers, want at least 2",
			len(near), radiusM)
	}
	// The grid has a fix exactly on the target, so the nearest hit must be ~0 m away. If the
	// radius were being read as DEGREES (the geometry trap), this would sweep in the whole grid.
	if near[0].DistanceM > 1 {
		t.Fatalf("the nearest fix is %v m away, but the grid places one exactly on the target",
			near[0].DistanceM)
	}
	if len(near) > 10 {
		t.Fatalf("FixesNear returned %d fixes within %v METRES of the target; the grid is 0.01° "+
			"(~840 m) apart, so a result set this large means the radius is not being applied in metres",
			len(near), radiusM)
	}
	for i, n := range near {
		if n.DistanceM > radiusM {
			t.Fatalf("FixesNear returned a fix %v m away, outside the %v m radius", n.DistanceM, radiusM)
		}
		if i > 0 && near[i-1].DistanceM > n.DistanceM {
			t.Fatalf("results are not ordered nearest-first: %v m came before %v m", near[i-1].DistanceM, n.DistanceM)
		}
		if n.DeviceName == "" {
			t.Fatal("FixesNear returned a fix with no device name; the join is not producing the device")
		}
	}
}

// TestLatestFixUsesTheKeyIndexBackwards proves the claim 00002_core_schema.sql makes when it
// declines to create a second, descending index: the primary key's btree on (device_id, ts)
// already serves "the newest fix for this device", scanned BACKWARD, so a dedicated
// (device_id, ts DESC) index would be a duplicate paid for on every INSERT forever.
//
// If this test ever fails, the comment in the migration is wrong and the index belongs back.
func TestLatestFixUsesTheKeyIndexBackwards(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool := setup(ctx, t)
	_, deviceID := seedDevice(ctx, t, pool, "latest")

	month := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	partition := mustEnsurePartition(ctx, t, pool, month)
	seedGrid(ctx, t, pool, deviceID, month, 2000)

	if _, err := pool.Exec(ctx, `ANALYZE fixes`); err != nil {
		t.Fatalf("ANALYZE: %v", err)
	}

	pkIndex := partitionIndex(ctx, t, pool, partition, "btree")

	const latestSQL = `SELECT ts FROM fixes WHERE device_id = $1 ORDER BY ts DESC LIMIT 1`
	plan := explain(ctx, t, pool, latestSQL, deviceID)

	if !strings.Contains(plan, "Backward") {
		t.Fatalf("ORDER BY ts DESC is not served by a BACKWARD index scan, so the primary key's "+
			"ascending btree is not doing the job the migration says it does. Plan:\n%s", plan)
	}
	if !strings.Contains(plan, pkIndex) {
		t.Fatalf("the latest-fix query does not use the primary key index %q. Plan:\n%s", pkIndex, plan)
	}
	if strings.Contains(plan, "Sort") {
		t.Fatalf("the latest-fix query is sorting rather than walking the index. Plan:\n%s", plan)
	}
}

// --- helpers -------------------------------------------------------------------------------

// setup starts a real PostGIS, migrates it, and returns a pool. One container per top-level
// test: the tests are parallel and must not be able to see each other's rows.
func setup(ctx context.Context, t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := testsupport.NewPostGIS(t)
	if err := db.Up(ctx, dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// seedDevice creates a family with one device in it and returns both ids.
func seedDevice(ctx context.Context, t *testing.T, pool *pgxpool.Pool, name string) (familyID, deviceID string) {
	t.Helper()

	familyID, err := CreateFamily(ctx, pool, name)
	if err != nil {
		t.Fatalf("CreateFamily: %v", err)
	}
	// A synthetic 32-byte digest, distinct per device: the SHA-256 of the test's own name.
	// Not derived from any real token — nothing here is a secret. Distinct because
	// devices.token_hash is UNIQUE, and it is UNIQUE because two devices sharing a credential
	// means either phone can write the other's history.
	hash := sha256.Sum256([]byte(name))
	deviceID, err = CreateDevice(ctx, pool, familyID, name+"-phone", hash[:])
	if err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}
	return familyID, deviceID
}

func mustEnsurePartition(ctx context.Context, t *testing.T, pool *pgxpool.Pool, month time.Time) string {
	t.Helper()

	name, err := db.EnsureMonthlyPartition(ctx, pool, month)
	if err != nil {
		t.Fatalf("EnsureMonthlyPartition: %v", err)
	}
	return name
}

// seedGrid inserts n fixes spread over a ~1° × 0.2° grid around Rome, one second apart.
//
// It writes them in a single statement rather than n InsertFix calls: this is fixture volume
// for the planner, and the write path itself is asserted by the tests that care about it.
// Longitude is still first — even the fixtures obey the rule.
func seedGrid(ctx context.Context, t *testing.T, pool *pgxpool.Pool, deviceID string, start time.Time, n int) {
	t.Helper()

	_, err := pool.Exec(ctx, `
		INSERT INTO fixes (device_id, ts, location)
		SELECT $1,
		       $2::timestamptz + make_interval(secs => g),
		       ST_Point(12.0 + (g % 100) * 0.01, 41.0 + (g / 100) * 0.01, 4326)::geography
		FROM generate_series(1, $3) g`, deviceID, start, n)
	if err != nil {
		t.Fatalf("seed %d fixes: %v", n, err)
	}
}

// partitionIndex returns the name of the partition's index of the given access method (gist or
// btree). It reads the catalog rather than assuming Postgres's index-naming convention, so the
// EXPLAIN assertions cannot pass or fail on a naming detail.
func partitionIndex(ctx context.Context, t *testing.T, pool *pgxpool.Pool, partition, method string) string {
	t.Helper()

	name := scalar[string](ctx, t, pool, `
		SELECT i.relname
		FROM pg_class c
		JOIN pg_index x ON x.indrelid = c.oid
		JOIN pg_class i ON i.oid = x.indexrelid
		JOIN pg_am am ON am.oid = i.relam
		WHERE c.relname = $1 AND am.amname = $2`, partition, method)
	if name == "" {
		t.Fatalf("partition %s has no %s index — the parent's indexes are not being cloned onto it", partition, method)
	}
	return name
}

// explain returns the text plan for a query, with its real parameters bound.
func explain(ctx context.Context, t *testing.T, pool *pgxpool.Pool, sql string, args ...any) string {
	t.Helper()

	rows, err := pool.Query(ctx, "EXPLAIN "+sql, args...)
	if err != nil {
		t.Fatalf("EXPLAIN: %v", err)
	}
	defer rows.Close()

	var b strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("EXPLAIN: %v", err)
	}
	return b.String()
}

// assertFences checks exactly which of the family's Places contain a point.
func assertFences(ctx context.Context, t *testing.T, pool *pgxpool.Pool, familyID string, lon, lat float64, want []string) {
	t.Helper()

	got, err := GeofencesContaining(ctx, pool, familyID, lon, lat)
	if err != nil {
		t.Fatalf("GeofencesContaining(%v, %v): %v", lon, lat, err)
	}
	if len(got) != len(want) {
		t.Fatalf("GeofencesContaining(%v, %v) returned %d fences, want %d", lon, lat, len(got), len(want))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("GeofencesContaining(%v, %v)[%d] = %s, want %s", lon, lat, i, got[i].ID, id)
		}
	}
}

// scalar runs a query returning exactly one value.
func scalar[T any](ctx context.Context, t *testing.T, pool *pgxpool.Pool, sql string, args ...any) T {
	t.Helper()

	var v T
	if err := pool.QueryRow(ctx, sql, args...).Scan(&v); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return v
}

// assertClose compares against a golden value with an explicit tolerance.
func assertClose(t *testing.T, what string, got, want, tolerance float64) {
	t.Helper()

	if math.Abs(got-want) > tolerance {
		t.Fatalf("%s = %v, want %v (±%v) — off by %v",
			what, got, want, tolerance, math.Abs(got-want))
	}
}
