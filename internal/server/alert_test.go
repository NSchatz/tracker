// The ALERT-2 server surface, driven through the real handler against a real PostGIS: the three
// additive /v1 changes (the configured provider on an accepted registration, the supersede of a
// rotated routing address, and the device name on every crossing row) and the honesty rule that
// delivery accounting never touches the crossing log.
//
// Like the rest of the server suite these share one container per top-level test and isolate by
// FAMILY, and - like the rest of the repo - they FAIL rather than skip when Docker is missing.
package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/NSchatz/tracker/internal/auth"
	"github.com/NSchatz/tracker/internal/server"
	"github.com/NSchatz/tracker/internal/store"
)

// registrationResponse is the accepted-registration body, including ALERT-2's additive
// configured_provider. It is decoded into a map as well as this struct in the A25 case, because
// there the question is what the response BYTES are, not what one client happens to read out of them.
type registrationResponse struct {
	ID                 string `json:"id"`
	Provider           string `json:"provider"`
	ConfiguredProvider string `json:"configured_provider"`
}

func decodeRegistration(t *testing.T, body []byte) registrationResponse {
	t.Helper()
	var got registrationResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("registration response not JSON: %v (%q)", err, string(body))
	}
	return got
}

// TestRegistrationNamesTheConfiguredProvider is A22 and D7: an accepted registration says which push
// backend THIS DEPLOYMENT configured, or reports that it configured none - and is accepted and
// stored either way, because a deployment can configure a backend after a phone has registered.
//
// It matters because the app cannot infer this. The server answers 201 to a registration whether or
// not it has a backend, so without this field "registered and armed" and "registered into a
// deployment that will never send" are the same response, and an app showing armed for the second
// one is the silent failure this phase exists to close.
func TestRegistrationNamesTheConfiguredProvider(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withServerOptions(server.WithConfiguredPushProvider(store.PushProviderFCM)))

	deviceID, _ := h.enroll(t, "provider-report")
	famID := h.familyOf(t, deviceID)
	viewerTok := h.addViewer(t, famID, "provider-viewer")

	t.Run("a deployment with fcm configured says so", func(t *testing.T) {
		rec := h.post(t, "/v1/push-subscriptions", viewerTok, `{"provider":"fcm","token":"phone-configured"}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("register = %d (%s), want 201", rec.Code, rec.Body.String())
		}
		got := decodeRegistration(t, rec.Body.Bytes())
		if got.ConfiguredProvider != store.PushProviderFCM {
			t.Errorf("configured_provider = %q, want %q", got.ConfiguredProvider, store.PushProviderFCM)
		}
		// The echo of what the caller registered is unchanged - this field is additive, not a rename.
		if got.Provider != store.PushProviderFCM || got.ID == "" {
			t.Errorf("response = %+v, want the stored id and the registered provider unchanged", got)
		}
	})

	t.Run("a deployment with NO backend configured reports none, and still stores the registration", func(t *testing.T) {
		// The same pool and the same push pipeline; only the deployment's configured backend differs.
		unconfigured := h.handlerWith()

		rec := h.postVia(t, unconfigured, "/v1/push-subscriptions", viewerTok, `{"provider":"fcm","token":"phone-unconfigured"}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("register against an unconfigured deployment = %d (%s), want 201 - registration is accepted either way", rec.Code, rec.Body.String())
		}
		got := decodeRegistration(t, rec.Body.Bytes())
		if got.ConfiguredProvider != "" {
			t.Errorf("configured_provider = %q, want \"\" (this deployment configured none)", got.ConfiguredProvider)
		}
		if got.ID == "" {
			t.Error("registration was not stored: no id came back")
		}

		// The field must be PRESENT and empty, never absent. An absent key is how a client detects a
		// server OLDER than A22, and the two must not be confusable.
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
			t.Fatalf("response not a JSON object: %v", err)
		}
		if _, ok := raw["configured_provider"]; !ok {
			t.Error("configured_provider is absent from the response; it must be present and empty, so an app can tell \"this deployment configured none\" from \"this server does not report\"")
		}
	})
}

// TestRotatedRoutingAddressSupersedesItsPredecessor is A24 (and D8): a registration that names the
// routing address it replaces removes exactly that endpoint, so the phone whose address rotated gets
// one crossing once and not twice.
//
// The old address IS still deliverable until something removes it - that is the whole defect. So the
// assertion is on the fan-out: after the supersede, one crossing produces exactly one delivery.
func TestRotatedRoutingAddressSupersedesItsPredecessor(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withServerOptions(server.WithConfiguredPushProvider(store.PushProviderFCM)))

	deviceID, deviceTok := h.enroll(t, "rotation")
	famID := h.familyOf(t, deviceID)
	h.makePlace(t, famID, "Home")
	viewerTok := h.addViewer(t, famID, "rotation-viewer")

	if rec := h.post(t, "/v1/push-subscriptions", viewerTok, `{"provider":"fcm","token":"address-old"}`); rec.Code != http.StatusCreated {
		t.Fatalf("register the first address = %d (%s), want 201", rec.Code, rec.Body.String())
	}
	// The FCM registration token rotated. The phone registers the new address and names the old one.
	if rec := h.post(t, "/v1/push-subscriptions", viewerTok,
		`{"provider":"fcm","token":"address-new","replaces_token":"address-old"}`); rec.Code != http.StatusCreated {
		t.Fatalf("register the rotated address = %d (%s), want 201", rec.Code, rec.Body.String())
	}

	if got := h.endpointTokens(t, famID); len(got) != 1 || got[0] != "address-new" {
		t.Fatalf("endpoints after the supersede = %v, want exactly [address-new] - the replaced address must be gone", got)
	}

	// One crossing, one delivery. Two would mean the superseded row survived and the phone buzzes
	// twice for one arrival.
	h.driveCrossing(t, deviceTok)
	first := h.nextPush(t)
	if first.Sub.Token != "address-new" {
		t.Fatalf("the crossing was delivered to %q, want address-new", first.Sub.Token)
	}
	select {
	case extra := <-h.pushes.ch:
		t.Fatalf("one crossing produced a SECOND delivery, to %q - the superseded endpoint was not removed", extra.Sub.Token)
	case <-time.After(500 * time.Millisecond):
	}
}

// TestSupersedeCannotReachAnotherViewersEndpoint is A25, and it is the authorization on A24.
//
// A registration naming a routing address that is not registered under the AUTHENTICATED viewer must
// remove nothing and must answer exactly as the succeeding case does. Otherwise the route is an
// oracle: a caller could learn whether an address is registered anywhere, and could unregister
// somebody else's phone by guessing its address.
func TestSupersedeCannotReachAnotherViewersEndpoint(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withServerOptions(server.WithConfiguredPushProvider(store.PushProviderFCM)))

	deviceID, _ := h.enroll(t, "supersede-authz")
	famID := h.familyOf(t, deviceID)
	victimTok := h.addViewer(t, famID, "victim")
	attackerTok := h.addViewer(t, famID, "attacker")

	if rec := h.post(t, "/v1/push-subscriptions", victimTok, `{"provider":"fcm","token":"victim-address"}`); rec.Code != http.StatusCreated {
		t.Fatalf("victim register = %d (%s), want 201", rec.Code, rec.Body.String())
	}

	// The attacker registers its own address and names the victim's as replaced.
	refused := h.post(t, "/v1/push-subscriptions", attackerTok,
		`{"provider":"fcm","token":"attacker-address","replaces_token":"victim-address"}`)
	if refused.Code != http.StatusCreated {
		t.Fatalf("the registration itself = %d (%s), want 201 - A25 refuses the REMOVAL, not the registration", refused.Code, refused.Body.String())
	}

	// Nothing was removed.
	tokens := h.endpointTokens(t, famID)
	if !contains(tokens, "victim-address") {
		t.Fatalf("endpoints = %v - naming another viewer's routing address removed it", tokens)
	}

	// And the answer is indistinguishable from the succeeding case. The attacker then supersedes an
	// address it genuinely holds; the two responses must differ in nothing but the stored id, which
	// is a fresh row's identity in both and says nothing about whether a removal happened.
	succeeded := h.post(t, "/v1/push-subscriptions", attackerTok,
		`{"provider":"fcm","token":"attacker-address-2","replaces_token":"attacker-address"}`)
	if succeeded.Code != refused.Code {
		t.Fatalf("status codes differ: refused-removal %d vs successful-removal %d", refused.Code, succeeded.Code)
	}
	if got, want := normaliseRegistrationBody(t, refused.Body.Bytes()), normaliseRegistrationBody(t, succeeded.Body.Bytes()); got != want {
		t.Fatalf("responses differ once the fresh row's id is set aside:\n  removal refused:   %s\n  removal succeeded: %s\nA25 requires them to be the same answer", got, want)
	}
	// Sanity: the case that was SUPPOSED to remove something did.
	if contains(h.endpointTokens(t, famID), "attacker-address") {
		t.Error("the attacker's own previous address survived its supersede; the comparison above proved nothing")
	}
}

// TestCrossingLogIsNeverFilteredByDeliveryAccounting is A6 with A27 riding along, and it is the
// fail-safe the whole phase rests on: the crossing log is the record, and delivery accounting is a
// commentary on it that may never edit it.
//
// The harness has NO push senders, so every delivery is refused before hand-over and recorded
// `dropped` - the "recorded undelivered for every endpoint" precondition, reached without a network.
// Six distinct (device, Place) pairs cross, which is also past the four-key collapse bound, so both
// the surplus and the drops are exercised. All six must come back from the route, unchanged, each
// naming the device.
func TestCrossingLogIsNeverFilteredByDeliveryAccounting(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withNoPushSenders(), withServerOptions(server.WithConfiguredPushProvider(store.PushProviderFCM)))

	deviceID, deviceTok := h.enroll(t, "undelivered")
	famID := h.familyOf(t, deviceID)
	viewerTok := h.addViewer(t, famID, "undelivered-viewer")
	if rec := h.post(t, "/v1/push-subscriptions", viewerTok, `{"provider":"fcm","token":"offline-phone"}`); rec.Code != http.StatusCreated {
		t.Fatalf("register = %d (%s), want 201", rec.Code, rec.Body.String())
	}

	// Six Places, all containing the same point, so one dwelling run crosses into all six at once:
	// six distinct (device, Place) pairs for one endpoint, which is past the bound of four.
	const places = 6
	for i := 0; i < places; i++ {
		h.makePlace(t, famID, "Place "+string(rune('A'+i)))
	}
	h.driveCrossingEnterOnly(t, deviceTok)

	events := h.geofenceEvents(t, viewerTok)
	if len(events) != places {
		t.Fatalf("the crossings route returned %d rows, want all %d - delivery accounting must never filter the log: %+v",
			len(events), places, events)
	}
	for _, e := range events {
		if e.Transition != "enter" {
			t.Errorf("row %+v: transition = %q, want enter - the log must come back unchanged", e, e.Transition)
		}
		if e.DeviceID != deviceID {
			t.Errorf("row %+v: device_id = %q, want %q", e, e.DeviceID, deviceID)
		}
		// A27: the family's own name for the device, on the same row.
		if e.DeviceName != "undelivered" {
			t.Errorf("row %+v: device_name = %q, want the family's own name for the device", e, e.DeviceName)
		}
		if e.PlaceName == "" || e.TS == 0 {
			t.Errorf("row %+v: the crossing came back missing its Place name or its ts", e)
		}
	}
}

// --- helpers -----------------------------------------------------------------------------------

// endpointTokens reads the routing addresses currently registered for a family, straight out of the
// table. There is no route that lists them (deliberately - see store/push.go), so the assertion goes
// to the store.
func (h *harness) endpointTokens(t *testing.T, familyID string) []string {
	t.Helper()
	subs, err := store.PushSubscriptionsForFamily(context.Background(), h.pool, familyID)
	if err != nil {
		t.Fatalf("PushSubscriptionsForFamily: %v", err)
	}
	out := make([]string, 0, len(subs))
	for _, s := range subs {
		out = append(out, s.Token)
	}
	return out
}

// driveCrossing posts a fix stream that dwells OUTSIDE the Rome-square Place and then INSIDE it,
// which the evaluator confirms as exactly one enter once each run clears the production debounce.
// It is the same shape TestCrossingProducesPush uses, factored so the ALERT-2 cases do not restate
// the fix spacing.
func (h *harness) driveCrossing(t *testing.T, deviceTok auth.Token) {
	t.Helper()
	h.driveCrossingEnterOnly(t, deviceTok)
}

// driveCrossingEnterOnly is driveCrossing under its honest name: it produces enters, and no exit.
func (h *harness) driveCrossingEnterOnly(t *testing.T, deviceTok auth.Token) {
	t.Helper()
	b := time.Now().Add(-3 * time.Hour).Unix()
	step := int64(store.GeofenceDebounce.Seconds() * 2 / 3) // 60 s for a 90 s debounce
	// Two fixes well outside the square, then a dwelling run inside it.
	h.postFixAt(t, deviceTok, 41.5, 11.0, b)
	h.postFixAt(t, deviceTok, 41.5, 11.0, b+step)
	h.postFixAt(t, deviceTok, 41.5, 12.5, b+2*step)
	h.postFixAt(t, deviceTok, 41.5, 12.5, b+3*step)
	h.postFixAt(t, deviceTok, 41.5, 12.5, b+4*step)
}

// contains reports whether a routing address is in the list.
func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// normaliseRegistrationBody renders a registration response with the stored row's id blanked, so two
// responses can be compared for everything that could betray whether a removal happened. The id
// itself cannot: it is a fresh row's identity in both cases.
func normaliseRegistrationBody(t *testing.T, body []byte) string {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("registration response not a JSON object: %v (%q)", err, string(body))
	}
	if _, ok := raw["id"]; !ok {
		t.Fatalf("registration response has no id: %q", string(body))
	}
	raw["id"] = "<fresh row id>"
	out, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("re-encode registration response: %v", err)
	}
	return string(out)
}
