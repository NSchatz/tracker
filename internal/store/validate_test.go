package store

import (
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
		if _, err := polygonWKT([]Point{{Lon: 12, Lat: 41}, {Lon: 13, Lat: 41}}); err == nil {
			t.Fatal("polygonWKT accepted a 2-point ring")
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
