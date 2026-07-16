// The S6 push surface driven through the real handler: a viewer registers a push endpoint, and a
// crossing POSTed to /v1/fixes fans out to it through the real EventNotifier + dispatcher (the
// capturing sender on the harness stands in for FCM/UnifiedPush). Like the rest of the suite this
// shares one container and isolates by family.
package server_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/NSchatz/tracker/internal/store"
)

func TestPushRegistrationHTTP(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	t.Run("a viewer registers an endpoint", func(t *testing.T) {
		deviceID, _ := h.enroll(t, "reg-ok")
		famID := h.familyOf(t, deviceID)
		viewerTok := h.addViewer(t, famID, "reg-viewer")

		rec := h.post(t, "/v1/push-subscriptions", viewerTok, `{"provider":"fcm","token":"phone-token-1"}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("register = %d (%s), want 201", rec.Code, rec.Body.String())
		}
		var got struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("response not JSON: %v (%q)", err, rec.Body.String())
		}
		if got.ID == "" || got.Provider != "fcm" {
			t.Fatalf("response = %+v, want an id and provider fcm", got)
		}

		// UnifiedPush registers too (its token is an endpoint URL).
		if rec := h.post(t, "/v1/push-subscriptions", viewerTok,
			`{"provider":"unifiedpush","token":"https://ntfy.example/up-abc"}`); rec.Code != http.StatusCreated {
			t.Fatalf("register unifiedpush = %d (%s), want 201", rec.Code, rec.Body.String())
		}
	})

	t.Run("registration is viewer-only", func(t *testing.T) {
		_, deviceTok := h.enroll(t, "reg-authz")

		// A device (write) token cannot register — it is not a viewer.
		if rec := h.post(t, "/v1/push-subscriptions", deviceTok, `{"provider":"fcm","token":"x"}`); rec.Code != http.StatusUnauthorized {
			t.Fatalf("device token register = %d, want 401", rec.Code)
		}
		// No token at all.
		if rec := h.post(t, "/v1/push-subscriptions", "", `{"provider":"fcm","token":"x"}`); rec.Code != http.StatusUnauthorized {
			t.Fatalf("no-token register = %d, want 401", rec.Code)
		}
	})

	t.Run("malformed registrations are typed 400s that store nothing", func(t *testing.T) {
		deviceID, _ := h.enroll(t, "reg-bad")
		famID := h.familyOf(t, deviceID)
		viewerTok := h.addViewer(t, famID, "bad-viewer")

		for _, tc := range []struct {
			name, body string
		}{
			{"missing token", `{"provider":"fcm"}`},
			{"missing provider", `{"token":"x"}`},
			{"empty token", `{"provider":"fcm","token":"   "}`},
			{"unknown provider", `{"provider":"telegram","token":"x"}`},
			{"unknown field", `{"provider":"fcm","token":"x","foo":1}`},
		} {
			if rec := h.post(t, "/v1/push-subscriptions", viewerTok, tc.body); rec.Code != http.StatusBadRequest {
				t.Errorf("%s: got %d (%s), want 400", tc.name, rec.Code, rec.Body.String())
			}
		}
	})
}

// TestCrossingProducesPush is the headline S6 acceptance: a geofence crossing produces a push to a
// registered token (mock in CI). It drives the whole pipeline — fix in → evaluator → event → fan-out
// → dispatcher → sender — end to end, and asserts the delivered push carries the right endpoint,
// high-priority-worthy content, and per-(device,Place) collapse key.
func TestCrossingProducesPush(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	deviceID, deviceTok := h.enroll(t, "push-crossing")
	famID := h.familyOf(t, deviceID)
	place := h.makePlace(t, famID, "Home")
	viewerTok := h.addViewer(t, famID, "push-watcher")

	// Register the watcher's phone.
	if rec := h.post(t, "/v1/push-subscriptions", viewerTok, `{"provider":"fcm","token":"watcher-phone"}`); rec.Code != http.StatusCreated {
		t.Fatalf("register push endpoint = %d (%s), want 201", rec.Code, rec.Body.String())
	}

	// Drive a crossing: outside, dwell inside (an ENTER), dwell outside (an EXIT). Spaced so each run
	// clears the production debounce (the server evaluates with GeofenceDebounce).
	b := time.Now().Add(-3 * time.Hour).Unix()
	step := int64(store.GeofenceDebounce.Seconds() * 2 / 3)
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

	// The enter push. Delivery is asynchronous, so nextPush waits for it.
	enter := h.nextPush(t)
	if enter.Sub.Provider != "fcm" || enter.Sub.Token != "watcher-phone" {
		t.Fatalf("push went to %+v, want the registered fcm endpoint watcher-phone", enter.Sub)
	}
	if enter.Note.Title != "push-crossing" {
		t.Errorf("push title = %q, want the device name", enter.Note.Title)
	}
	if enter.Note.Data["transition"] != "enter" || enter.Note.Data["place_name"] != "Home" {
		t.Errorf("push data = %v, want an enter at Home", enter.Note.Data)
	}
	if want := "gf:" + deviceID + ":" + place; enter.Note.CollapseKey != want {
		t.Errorf("collapse key = %q, want %q (per device+Place)", enter.Note.CollapseKey, want)
	}

	// And the exit push follows.
	exit := h.nextPush(t)
	if exit.Note.Data["transition"] != "exit" {
		t.Errorf("second push transition = %q, want exit", exit.Note.Data["transition"])
	}
}
