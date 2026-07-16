package push

import (
	"strings"
	"testing"
	"time"

	"github.com/NSchatz/tracker/internal/store"
)

// TestBuildNotificationCarriesOnlyOptedInLabels pins the §5.3 PII boundary in the one place it is
// decided: a crossing alert carries the family's OWN labels (device name, Place name, direction) and
// NOTHING ELSE — never a coordinate, accuracy, or raw fix datum. It also pins the enter/exit wording,
// the per-(device,Place) collapse key, and that the structured data mirrors the human text.
func TestBuildNotificationCarriesOnlyOptedInLabels(t *testing.T) {
	t.Parallel()

	dev := store.Device{ID: "dev-1", FamilyID: "fam-1", Name: "Alice's phone"}
	ts := time.Date(2026, 7, 16, 9, 30, 0, 0, time.UTC)

	enter := buildNotification(dev, store.GeofenceEvent{
		DeviceID: "dev-1", GeofenceID: "place-1", GeofenceName: "School", Transition: "enter", FixTS: ts,
	})

	if enter.Title != "Alice's phone" {
		t.Errorf("title = %q, want the device name", enter.Title)
	}
	if enter.Body != "Alice's phone arrived at School" {
		t.Errorf("body = %q, want an arrival sentence in the family's own labels", enter.Body)
	}
	if enter.CollapseKey != "gf:dev-1:place-1" {
		t.Errorf("collapse key = %q, want per-(device,Place) so a rapid enter/exit collapses", enter.CollapseKey)
	}
	if enter.Data["transition"] != "enter" || enter.Data["place_name"] != "School" || enter.Data["device_id"] != "dev-1" {
		t.Errorf("data = %v, want the crossing's labels", enter.Data)
	}

	// The PII boundary: no coordinate/accuracy/location field may appear anywhere in the notification.
	haystack := strings.ToLower(enter.Title + " " + enter.Body)
	for k, v := range enter.Data {
		haystack += " " + strings.ToLower(k) + " " + strings.ToLower(v)
	}
	for _, forbidden := range []string{"lat", "lon", "coordinate", "accuracy", "meter", "12.", "41."} {
		if strings.Contains(haystack, forbidden) {
			t.Errorf("notification contains %q — a coordinate/location datum must never be in the body (§5.3 PII boundary): %q", forbidden, haystack)
		}
	}

	// Exit wording.
	exit := buildNotification(dev, store.GeofenceEvent{
		DeviceID: "dev-1", GeofenceID: "place-1", GeofenceName: "School", Transition: "exit", FixTS: ts,
	})
	if exit.Body != "Alice's phone left School" {
		t.Errorf("exit body = %q, want a departure sentence", exit.Body)
	}
}
