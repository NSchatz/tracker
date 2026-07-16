// The S5 geofencing HTTP surface, driven through the real handler against a real PostGIS: a fix
// stream POSTed to /v1/fixes drives the evaluator, and a viewer reads the resulting Places and
// enter/exit log back over /v1/places and /v1/geofence-events. Like the rest of the server suite this
// shares one container and isolates by FAMILY.
package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/NSchatz/tracker/internal/auth"
	"github.com/NSchatz/tracker/internal/store"
)

// makePlace creates a Rome-square Place for a family (the operator CLI path, in-process) and returns
// its id.
func (h *harness) makePlace(t *testing.T, familyID, name string) string {
	t.Helper()
	id, err := store.CreateGeofence(context.Background(), h.pool, familyID, name, []store.Point{
		{Lon: 12, Lat: 41}, {Lon: 13, Lat: 41}, {Lon: 13, Lat: 42}, {Lon: 12, Lat: 42},
	})
	if err != nil {
		t.Fatalf("CreateGeofence: %v", err)
	}
	return id
}

// postFixAt POSTs one first-party fix at an explicit ts, asserting it stored — driving the geofence
// evaluator exactly as a real report would.
func (h *harness) postFixAt(t *testing.T, token auth.Token, lat, lon float64, ts int64) {
	t.Helper()
	body := fmt.Sprintf(`{"lat":%f,"lon":%f,"ts":%d}`, lat, lon, ts)
	if rec := h.post(t, "/v1/fixes", token, body); rec.Code != http.StatusCreated {
		t.Fatalf("post fix at %d = %d (%s), want 201", ts, rec.Code, rec.Body.String())
	}
}

type wireEvent struct {
	DeviceID   string `json:"device_id"`
	PlaceID    string `json:"place_id"`
	PlaceName  string `json:"place_name"`
	Transition string `json:"transition"`
	TS         int64  `json:"ts"`
}

func (h *harness) geofenceEvents(t *testing.T, token auth.Token) []wireEvent {
	t.Helper()
	rec := h.get(t, "/v1/geofence-events", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/geofence-events = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var out []wireEvent
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("geofence-events body not JSON: %v (%q)", err, rec.Body.String())
	}
	return out
}

func TestGeofencingHTTP(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	// A crossing POSTed through /v1/fixes produces exactly one enter and one exit in the log a viewer
	// reads back. Fixes are spaced so the inside and outside runs both exceed the production
	// GeofenceDebounce (they must, since the server evaluates with it).
	t.Run("a posted crossing yields one enter and one exit, readable by the viewer", func(t *testing.T) {
		deviceID, deviceTok := h.enroll(t, "crossing")
		famID := h.familyOf(t, deviceID)
		place := h.makePlace(t, famID, "Home")
		viewerTok := h.addViewer(t, famID, "watcher")

		// Base well in the past but inside the ingest window; step by 60 s so the inside run
		// (120..240 = 120 s) and outside run (300..420) each clear the 90 s debounce.
		b := time.Now().Add(-3 * time.Hour).Unix()
		step := int64(store.GeofenceDebounce.Seconds() * 2 / 3) // 60 s for a 90 s debounce
		out := func(n int64) { h.postFixAt(t, deviceTok, 41.5, 11.0, b+n*step) }
		in := func(n int64) { h.postFixAt(t, deviceTok, 41.5, 12.5, b+n*step) }
		out(0)
		out(1)
		in(2)
		in(3)
		in(4)
		out(5)
		out(6)
		out(7)

		events := h.geofenceEvents(t, viewerTok)
		if len(events) != 2 {
			t.Fatalf("recorded %d events, want exactly one enter + one exit: %+v", len(events), events)
		}
		// Newest-first from the API: exit then enter.
		if events[0].Transition != "exit" || events[1].Transition != "enter" {
			t.Fatalf("events = [%s, %s], want [exit, enter] (newest first)", events[0].Transition, events[1].Transition)
		}
		if events[1].PlaceName != "Home" || events[1].PlaceID != place {
			t.Fatalf("enter event place = %q/%s, want Home/%s", events[1].PlaceName, events[1].PlaceID, place)
		}
		if events[1].TS != b+2*step {
			t.Fatalf("enter ts = %d, want the onset %d", events[1].TS, b+2*step)
		}
	})

	// GET /v1/places lists the family's Places with their GeoJSON, viewer-scoped.
	t.Run("places list is viewer-scoped", func(t *testing.T) {
		devA, _ := h.enroll(t, "places-a")
		famA := h.familyOf(t, devA)
		h.makePlace(t, famA, "Home")
		h.makePlace(t, famA, "School")
		viewerA := h.addViewer(t, famA, "viewer-a")

		devB, _ := h.enroll(t, "places-b")
		famB := h.familyOf(t, devB)
		h.makePlace(t, famB, "OtherFamilyPlace")
		viewerB := h.addViewer(t, famB, "viewer-b")

		rec := h.get(t, "/v1/places", viewerA)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /v1/places = %d, want 200", rec.Code)
		}
		var places []struct {
			ID   string          `json:"id"`
			Name string          `json:"name"`
			Area json.RawMessage `json:"area"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &places); err != nil {
			t.Fatalf("places body not JSON: %v (%q)", err, rec.Body.String())
		}
		if len(places) != 2 {
			t.Fatalf("family A sees %d places, want 2 (never B's)", len(places))
		}
		if places[0].Name != "Home" || places[1].Name != "School" {
			t.Fatalf("places not name-ordered: %q, %q", places[0].Name, places[1].Name)
		}
		if len(places[0].Area) == 0 || places[0].Area[0] != '{' {
			t.Fatalf("place area is not GeoJSON: %q", string(places[0].Area))
		}

		// Family B sees only its own.
		recB := h.get(t, "/v1/places", viewerB)
		var placesB []struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(recB.Body.Bytes(), &placesB)
		if len(placesB) != 1 || placesB[0].Name != "OtherFamilyPlace" {
			t.Fatalf("family B sees %+v, want only OtherFamilyPlace", placesB)
		}
	})

	// The geofencing read routes are viewer-only and family-scoped, and an empty log/list is [].
	t.Run("read routes require a viewer and are family-scoped", func(t *testing.T) {
		deviceID, deviceTok := h.enroll(t, "authz")
		famID := h.familyOf(t, deviceID)
		h.makePlace(t, famID, "Home")
		viewerTok := h.addViewer(t, famID, "authz-viewer")

		for _, path := range []string{"/v1/places", "/v1/geofence-events"} {
			// A device (write) token cannot read.
			if rec := h.get(t, path, deviceTok); rec.Code != http.StatusUnauthorized {
				t.Fatalf("device token on GET %s = %d, want 401", path, rec.Code)
			}
			// No token at all.
			if rec := h.get(t, path, ""); rec.Code != http.StatusUnauthorized {
				t.Fatalf("no token on GET %s = %d, want 401", path, rec.Code)
			}
		}

		// A family with no crossings yet: an empty JSON array, never null.
		if got := h.geofenceEvents(t, viewerTok); len(got) != 0 {
			t.Fatalf("a family with no crossings has %d events, want 0", len(got))
		}
		if rec := h.get(t, "/v1/geofence-events", viewerTok); rec.Body.String() != "[]\n" {
			t.Fatalf("empty geofence-events body = %q, want \"[]\"", rec.Body.String())
		}
	})
}
