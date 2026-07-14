package store

import (
	"context"
	"errors"
	"math"
	"testing"
)

// TestValidateLonLat is the one test in this package that needs no database — it is pure
// argument checking, and it runs in microseconds. Its job is to pin the BOUNDARIES, which the
// PostGIS-backed tests do not exercise exhaustively: the poles and the antimeridian are valid,
// and one ULP past them is not.
func TestValidateLonLat(t *testing.T) {
	t.Parallel()

	valid := []struct {
		name     string
		lon, lat float64
	}{
		{"null island", 0, 0},
		{"Rome", romeLon, romeLat},
		{"the north pole", 0, 90},
		{"the south pole", 0, -90},
		{"the antimeridian, east", 180, 0},
		{"the antimeridian, west", -180, 0},
	}
	for _, c := range valid {
		t.Run(c.name, func(t *testing.T) {
			if err := ValidateLonLat(c.lon, c.lat); err != nil {
				t.Fatalf("ValidateLonLat(%v, %v) = %v, want nil", c.lon, c.lat, err)
			}
		})
	}

	invalid := []struct {
		name     string
		lon, lat float64
	}{
		// The one that matters: Seattle's coordinates, swapped. This is the shape of the real
		// bug, not a contrived out-of-range number.
		{"a swapped lon/lat pair", 47.6062, -122.3321},
		{"latitude just past the pole", 0, 90.0000001},
		{"longitude just past the antimeridian", 180.0000001, 0},
		{"NaN longitude", math.NaN(), 0},
		{"NaN latitude", 0, math.NaN()},
		{"+Inf latitude", 0, math.Inf(1)},
		{"-Inf longitude", math.Inf(-1), 0},
	}
	for _, c := range invalid {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateLonLat(c.lon, c.lat)
			if !errors.Is(err, ErrCoordinateOutOfRange) {
				t.Fatalf("ValidateLonLat(%v, %v) = %v, want ErrCoordinateOutOfRange", c.lon, c.lat, err)
			}
		})
	}
}

// TestPolygonWKT pins the WKT the geofence writer produces — longitude first, ring closed, and
// out-of-range vertices refused rather than coerced into a differently-shaped Place.
func TestPolygonWKT(t *testing.T) {
	t.Parallel()

	t.Run("an open ring is closed", func(t *testing.T) {
		got, err := polygonWKT([]Point{{Lon: 12, Lat: 41}, {Lon: 13, Lat: 41}, {Lon: 13, Lat: 42}})
		if err != nil {
			t.Fatalf("polygonWKT: %v", err)
		}
		want := "SRID=4326;POLYGON((12 41, 13 41, 13 42, 12 41))"
		if got != want {
			t.Fatalf("polygonWKT =\n  %s\nwant\n  %s", got, want)
		}
	})

	t.Run("an already-closed ring is left alone", func(t *testing.T) {
		got, err := polygonWKT([]Point{{Lon: 12, Lat: 41}, {Lon: 13, Lat: 41}, {Lon: 13, Lat: 42}, {Lon: 12, Lat: 41}})
		if err != nil {
			t.Fatalf("polygonWKT: %v", err)
		}
		want := "SRID=4326;POLYGON((12 41, 13 41, 13 42, 12 41))"
		if got != want {
			t.Fatalf("polygonWKT double-closed the ring:\n  %s", got)
		}
	})

	t.Run("too few points is an error, not an empty Place", func(t *testing.T) {
		if _, err := polygonWKT([]Point{{Lon: 12, Lat: 41}, {Lon: 13, Lat: 41}}); !errors.Is(err, ErrInvalidGeofence) {
			t.Fatalf("polygonWKT with a 2-point ring = %v, want ErrInvalidGeofence", err)
		}
	})

	t.Run("three points but only two corners is an error", func(t *testing.T) {
		// {A, B, A} has three entries and is "already closed", so a naive len(ring) >= 3 waves
		// it through — and PostGIS then rejects it with a parse error from deep inside the
		// driver instead of the typed error the caller was promised.
		a := Point{Lon: 12, Lat: 41}
		b := Point{Lon: 13, Lat: 41}
		if _, err := polygonWKT([]Point{a, b, a}); !errors.Is(err, ErrInvalidGeofence) {
			t.Fatalf("polygonWKT({A, B, A}) = %v, want ErrInvalidGeofence — that ring encloses nothing", err)
		}
	})

	t.Run("an out-of-range vertex is refused", func(t *testing.T) {
		_, err := polygonWKT([]Point{{Lon: 12, Lat: 41}, {Lon: 13, Lat: 41}, {Lon: 13, Lat: 91}})
		if !errors.Is(err, ErrCoordinateOutOfRange) {
			t.Fatalf("polygonWKT with a latitude of 91 = %v, want ErrCoordinateOutOfRange — PostGIS "+
				"would have coerced it and stored a Place with a different shape than the caller drew", err)
		}
	})
}

// TestCreateDeviceRejectsAThingThatIsNotADigest pins the guard on the way in to devices.
//
// It needs no database: the check runs before any SQL, which is exactly the property being
// asserted — a nil Querier proves nothing was sent. The failure it guards is not a typo but a
// caller passing the RAW TOKEN where the hash belongs, which "works" (bytea takes anything) and
// turns the table into a file of live credentials.
func TestCreateDeviceRejectsAThingThatIsNotADigest(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name  string
		token []byte
	}{
		{"a raw token, not a digest", []byte("a-plausible-looking-bearer-token")[:31]},
		{"an empty credential", nil},
		{"a truncated digest", make([]byte, TokenHashLen-1)},
		{"an over-long digest", make([]byte, TokenHashLen+1)},
	} {
		t.Run(c.name, func(t *testing.T) {
			// A nil Querier: if the guard did not fire first, this would panic rather than
			// quietly pass.
			_, err := CreateDevice(context.Background(), nil, "family", "phone", c.token)
			if !errors.Is(err, ErrInvalidTokenHash) {
				t.Fatalf("CreateDevice with %s = %v, want ErrInvalidTokenHash", c.name, err)
			}
		})
	}
}
