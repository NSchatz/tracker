// The S5 geofence-evaluator tests — the correctness bedrock of server-side geofencing, run against a
// REAL PostGIS like the rest of this package. Containment is ST_Covers on a geography polygon, whose
// on-boundary and geodesic-edge behaviour a mock cannot reproduce (see TestGeofenceBoundaryContainment
// in spatial_test.go); the enter/exit debounce and out-of-order handling are computed in Go but pinned
// here end-to-end, fix stream in → geofence_events out.
//
// The roadmap's §8/S5 acceptance is spread across these subtests: enter/exit transitions, on-boundary,
// jitter-flap debounce, out-of-order derivation from ts, no duplicates on replay, and a fixture track
// that crosses a Place producing exactly one enter and one exit.
package store

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The standard test Place: a 1°×1° square around Rome, drawn on the lat/lon grid (west 12°, east 13°,
// south 41°, north 42°). Points used against it:
const (
	insideLon, insideLat   = 12.5, 41.5 // dead centre — unambiguously inside
	outsideLon, outsideLat = 11.0, 41.5 // a degree west of the box — unambiguously outside
	// The west edge runs along the 12°E MERIDIAN, which IS a geodesic, so a point on it is exactly on
	// the boundary — where ST_Covers (inclusive) says inside and ST_Contains would say outside. This is
	// the on-boundary case §5.3 turns on. (The south/north edges are parallels and bow ~120 m; those
	// are deliberately not used as "on boundary" here — see TestGeofenceBoundaryContainment.)
	boundaryLon, boundaryLat = 12.0, 41.5
)

// testDebounce is the dwell these tests pin the jitter boundary against. Small (relative to the fixture
// spacing below) so the tests are fast and legible; the server uses the production GeofenceDebounce.
const testDebounce = 30 * time.Second

// newSquarePlace creates the standard Rome square for a family and returns its id.
func newSquarePlace(ctx context.Context, t *testing.T, pool *pgxpool.Pool, familyID string) string {
	t.Helper()
	id, err := CreateGeofence(ctx, pool, familyID, "square", []Point{
		{Lon: 12, Lat: 41}, {Lon: 13, Lat: 41}, {Lon: 13, Lat: 42}, {Lon: 12, Lat: 42},
	})
	if err != nil {
		t.Fatalf("CreateGeofence: %v", err)
	}
	return id
}

// step is one fixture fix: an offset from a base time, and the point reported.
type step struct {
	offset   time.Duration
	lon, lat float64
}

// ev is a recorded transition, as the assertions compare them: direction and device event-time offset.
type ev struct {
	kind   string // "enter" | "exit"
	offset time.Duration
}

// ingestAndEval stores one fix through the real ingest path (provisioning its partition on demand) and
// then runs the evaluator against it — exactly what the server does per incoming fix.
func ingestAndEval(ctx context.Context, t *testing.T, pool *pgxpool.Pool, deviceID string, ts time.Time, lon, lat float64) {
	t.Helper()
	if _, err := IngestFix(ctx, pool, Fix{DeviceID: deviceID, TS: ts, Lon: lon, Lat: lat}, ts); err != nil {
		t.Fatalf("IngestFix at %s: %v", ts.Format(time.RFC3339), err)
	}
	if _, err := EvaluateDeviceGeofences(ctx, pool, deviceID, ts, testDebounce); err != nil {
		t.Fatalf("EvaluateDeviceGeofences at %s: %v", ts.Format(time.RFC3339), err)
	}
}

// recordedEvents reads the (device, place) transitions in ts order — the log as a consumer sees it.
func recordedEvents(ctx context.Context, t *testing.T, pool *pgxpool.Pool, deviceID, geofenceID string) []ev {
	t.Helper()
	rows, err := pool.Query(ctx,
		`SELECT transition, fix_ts FROM geofence_events
		 WHERE device_id = $1 AND geofence_id = $2 ORDER BY fix_ts ASC, id ASC`, deviceID, geofenceID)
	if err != nil {
		t.Fatalf("read geofence events: %v", err)
	}
	defer rows.Close()
	var out []ev
	for rows.Next() {
		var kind string
		var ts time.Time
		if err := rows.Scan(&kind, &ts); err != nil {
			t.Fatalf("scan event: %v", err)
		}
		out = append(out, ev{kind: kind, offset: ts.Sub(evalBase)})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read geofence events: %v", err)
	}
	return out
}

// assertEvents compares the recorded log against the exact expected sequence.
func assertEvents(t *testing.T, got, want []ev) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("recorded %d events, want %d\n got: %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event[%d] = %+v, want %+v\n got: %v\nwant: %v", i, got[i], want[i], got, want)
		}
	}
}

var evalBase = time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

func s(offsetSec int, lon, lat float64) step {
	return step{offset: time.Duration(offsetSec) * time.Second, lon: lon, lat: lat}
}

func TestGeofenceEvaluator(t *testing.T) {
	ctx := context.Background()
	pool := setup(ctx, t)

	// A clean crossing: outside, dwell inside, dwell outside → exactly one enter and one exit, each at
	// the onset of its run. This is the headline S5 acceptance (a fixture track crossing a Place).
	t.Run("a clean crossing produces exactly one enter and one exit", func(t *testing.T) {
		familyID, deviceID := seedDevice(ctx, t, pool, "crossing")
		place := newSquarePlace(ctx, t, pool, familyID)

		track := []step{
			s(0, outsideLon, outsideLat), s(30, outsideLon, outsideLat),
			s(60, insideLon, insideLat), s(90, insideLon, insideLat),
			s(120, insideLon, insideLat), s(150, insideLon, insideLat),
			s(180, outsideLon, outsideLat), s(210, outsideLon, outsideLat),
			s(240, outsideLon, outsideLat),
		}
		for _, st := range track {
			ingestAndEval(ctx, t, pool, deviceID, evalBase.Add(st.offset), st.lon, st.lat)
		}

		assertEvents(t, recordedEvents(ctx, t, pool, deviceID, place), []ev{
			{"enter", 60 * time.Second}, {"exit", 180 * time.Second},
		})
	})

	// On the meridian boundary, ST_Covers is inclusive: a device that stops exactly on the west edge is
	// INSIDE, so dwelling there is an enter. Pins §5.3's on-boundary rule through the evaluator.
	t.Run("a fix on the boundary counts as inside (ST_Covers)", func(t *testing.T) {
		familyID, deviceID := seedDevice(ctx, t, pool, "boundary")
		place := newSquarePlace(ctx, t, pool, familyID)

		for _, st := range []step{
			s(0, outsideLon, outsideLat), s(30, outsideLon, outsideLat),
			s(60, boundaryLon, boundaryLat), s(90, boundaryLon, boundaryLat),
			s(120, boundaryLon, boundaryLat),
		} {
			ingestAndEval(ctx, t, pool, deviceID, evalBase.Add(st.offset), st.lon, st.lat)
		}

		assertEvents(t, recordedEvents(ctx, t, pool, deviceID, place), []ev{
			{"enter", 60 * time.Second},
		})
	})

	// A single fix flipping across the boundary and back — within the debounce — is GPS jitter, not a
	// crossing. Neither an outside-sitting flap-inside nor an inside-sitting flap-outside produces an
	// event.
	t.Run("jitter across the boundary does not flap (debounce holds)", func(t *testing.T) {
		familyID, deviceID := seedDevice(ctx, t, pool, "jitter")
		place := newSquarePlace(ctx, t, pool, familyID)

		// Outside, with one fix jittering inside for 15 s (< 30 s debounce) then back out.
		for _, st := range []step{
			s(0, outsideLon, outsideLat), s(30, outsideLon, outsideLat),
			s(60, insideLon, insideLat), // the flap
			s(75, outsideLon, outsideLat), s(120, outsideLon, outsideLat),
		} {
			ingestAndEval(ctx, t, pool, deviceID, evalBase.Add(st.offset), st.lon, st.lat)
		}
		assertEvents(t, recordedEvents(ctx, t, pool, deviceID, place), nil)

		// Now establish a real inside dwell (enter), then jitter OUTSIDE for one fix — no exit.
		for _, st := range []step{
			s(200, insideLon, insideLat), s(230, insideLon, insideLat), s(260, insideLon, insideLat),
			s(290, outsideLon, outsideLat), // the flap out
			s(305, insideLon, insideLat), s(360, insideLon, insideLat),
		} {
			ingestAndEval(ctx, t, pool, deviceID, evalBase.Add(st.offset), st.lon, st.lat)
		}
		// Exactly one enter (at 200), and no exit — the outside flap at 290 was debounced away.
		assertEvents(t, recordedEvents(ctx, t, pool, deviceID, place), []ev{
			{"enter", 200 * time.Second},
		})
	})

	// The same clean crossing as the first subtest, but the fixes ARRIVE in reverse ts order. The log
	// must still be exactly one enter and one exit, at the true onset ts — events derive from ts order,
	// not arrival (§5.3).
	t.Run("out-of-order arrivals derive from ts order, not arrival", func(t *testing.T) {
		familyID, deviceID := seedDevice(ctx, t, pool, "out-of-order")
		place := newSquarePlace(ctx, t, pool, familyID)

		track := []step{
			s(0, outsideLon, outsideLat), s(30, outsideLon, outsideLat),
			s(60, insideLon, insideLat), s(90, insideLon, insideLat),
			s(120, insideLon, insideLat), s(150, insideLon, insideLat),
			s(180, outsideLon, outsideLat), s(210, outsideLon, outsideLat),
			s(240, outsideLon, outsideLat),
		}
		// Deliver newest-first.
		for i := len(track) - 1; i >= 0; i-- {
			ingestAndEval(ctx, t, pool, deviceID, evalBase.Add(track[i].offset), track[i].lon, track[i].lat)
		}

		assertEvents(t, recordedEvents(ctx, t, pool, deviceID, place), []ev{
			{"enter", 60 * time.Second}, {"exit", 180 * time.Second},
		})
	})

	// Replaying every fix — and re-running the evaluator directly — appends nothing: the log is an
	// idempotent projection keyed by the crossing fix. This is the "no duplicate events on replay"
	// acceptance clause.
	t.Run("replaying fixes produces no duplicate events", func(t *testing.T) {
		familyID, deviceID := seedDevice(ctx, t, pool, "replay")
		place := newSquarePlace(ctx, t, pool, familyID)

		track := []step{
			s(0, outsideLon, outsideLat), s(30, outsideLon, outsideLat),
			s(60, insideLon, insideLat), s(90, insideLon, insideLat), s(120, insideLon, insideLat),
			s(150, outsideLon, outsideLat), s(180, outsideLon, outsideLat),
		}
		for _, st := range track {
			ingestAndEval(ctx, t, pool, deviceID, evalBase.Add(st.offset), st.lon, st.lat)
		}
		want := []ev{{"enter", 60 * time.Second}, {"exit", 150 * time.Second}}
		assertEvents(t, recordedEvents(ctx, t, pool, deviceID, place), want)

		// Replay the whole track (idempotent ingest + re-evaluation), then re-evaluate directly a few
		// more times. Not one extra event may appear.
		for round := 0; round < 2; round++ {
			for _, st := range track {
				ingestAndEval(ctx, t, pool, deviceID, evalBase.Add(st.offset), st.lon, st.lat)
			}
		}
		for _, st := range track {
			if _, err := EvaluateDeviceGeofences(ctx, pool, deviceID, evalBase.Add(st.offset), testDebounce); err != nil {
				t.Fatalf("re-evaluate: %v", err)
			}
		}
		assertEvents(t, recordedEvents(ctx, t, pool, deviceID, place), want)
	})

	// A device only ever SEEN inside a Place — tracker never observed it outside — gets no fabricated
	// enter. A crossing is recorded only once its "from" state has been witnessed (§5.3 fail-safe: a
	// missed fix delays but never fabricates).
	t.Run("a device only ever seen inside is not given a fabricated enter", func(t *testing.T) {
		familyID, deviceID := seedDevice(ctx, t, pool, "always-inside")
		place := newSquarePlace(ctx, t, pool, familyID)

		for _, st := range []step{
			s(0, insideLon, insideLat), s(30, insideLon, insideLat),
			s(60, insideLon, insideLat), s(90, insideLon, insideLat),
		} {
			ingestAndEval(ctx, t, pool, deviceID, evalBase.Add(st.offset), st.lon, st.lat)
		}
		assertEvents(t, recordedEvents(ctx, t, pool, deviceID, place), nil)
	})
}
