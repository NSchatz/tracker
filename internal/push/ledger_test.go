package push

// The ALERT-2 delivery-honesty acceptance, driven through the WHOLE path a real crossing takes:
// NotifyGeofenceEvents → classify against the endpoint's pending keys → enqueue → the dispatcher's
// worker → a fake backend → the operator-readable outcome record.
//
// The fake backend ACCEPTS every message, which is the point: A5 says acceptance is not delivery,
// so a suite whose sender only ever failed would never catch an implementation that treated a
// successful Send as proof the phone got it.
//
// There is no database here on purpose. The accounting is process state (D4: no schema, no
// migration), so a real PostGIS would prove nothing about it. The half of this acceptance that IS
// about the database - a crossing recorded undelivered still comes back from /v1/geofence-events
// unchanged (A6) - is proved against a real PostGIS in internal/server.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NSchatz/tracker/internal/store"
)

// syncBuffer is a bytes.Buffer safe for the dispatcher's worker and the test goroutine to write to
// at once. Without it `go test -race` would flag the log sink itself rather than anything under test.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// outcomeRecord is one `push.delivery.outcome` line as an operator would read it back out of the
// deployment's log stream.
type outcomeRecord struct {
	Msg         string `json:"msg"`
	Outcome     string `json:"outcome"`
	Provider    string `json:"provider"`
	CollapseKey string `json:"collapse_key"`
	DeviceID    string `json:"device_id"`
	PlaceID     string `json:"place_id"`
	Transition  string `json:"transition"`
	TS          string `json:"ts"`
	Reason      string `json:"reason"`
}

// outcomes parses every delivery-outcome record out of a captured log. This is deliberately the
// same operation an operator performs (`... | grep push.delivery.outcome`): A7 asks for an outcome
// readable without a debugger, and the test reads it the way a human would rather than reaching
// into memory.
func outcomes(t *testing.T, sink *syncBuffer) []outcomeRecord {
	t.Helper()
	var out []outcomeRecord
	for _, line := range strings.Split(sink.String(), "\n") {
		if !strings.Contains(line, DeliveryOutcomeMessage) {
			continue
		}
		var rec outcomeRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("delivery-outcome log line is not readable JSON: %v (%q)", err, line)
		}
		if rec.Msg != DeliveryOutcomeMessage {
			continue
		}
		out = append(out, rec)
	}
	return out
}

// countByOutcome tallies the outcomes, and fails on any outcome that is not one of the three A7
// permits - the assertion that forbids a fourth, delivery-asserting outcome from ever appearing.
func countByOutcome(t *testing.T, recs []outcomeRecord) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, r := range recs {
		switch DeliveryOutcome(r.Outcome) {
		case OutcomeHandedOver, OutcomeBeyondCollapseBound, OutcomeDropped:
		default:
			t.Fatalf("outcome %q is not one of the three the system may name (%q, %q, %q) - nothing may assert delivery",
				r.Outcome, OutcomeHandedOver, OutcomeBeyondCollapseBound, OutcomeDropped)
		}
		counts[r.Outcome]++
	}
	return counts
}

// accountingHarness is a notifier over a fixed endpoint list and a fake backend, with the outcome
// log captured.
type accountingHarness struct {
	notifier *EventNotifier
	disp     *Dispatcher
	sink     *syncBuffer
	sent     chan Delivery
}

func newAccountingHarness(t *testing.T, subs []store.PushSubscription, send func(Delivery) error) *accountingHarness {
	t.Helper()

	sink := &syncBuffer{}
	logger := slog.New(slog.NewJSONHandler(sink, &slog.HandlerOptions{Level: slog.LevelInfo}))

	sent := make(chan Delivery, 64)
	sender := senderFunc(func(_ context.Context, d Delivery) error {
		if send != nil {
			if err := send(d); err != nil {
				return err
			}
		}
		sent <- d
		return nil
	})

	disp := NewDispatcher(map[string]Sender{
		PushProviderKeyFCM:         sender,
		PushProviderKeyUnifiedPush: sender,
	}, logger, WithRetryBackoff(0))

	n := NewEventNotifier(nil, disp, logger)
	n.subs = func(context.Context, string) ([]store.PushSubscription, error) { return subs, nil }

	return &accountingHarness{notifier: n, disp: disp, sink: sink, sent: sent}
}

// crossing builds one recorded crossing for a (device, Place) pair.
func crossing(deviceID, placeID, placeName, transition string, at time.Time) store.GeofenceEvent {
	return store.GeofenceEvent{
		DeviceID:     deviceID,
		GeofenceID:   placeID,
		GeofenceName: placeName,
		Transition:   transition,
		FixTS:        at,
	}
}

var accountingDevice = store.Device{ID: "dev-1", FamilyID: "fam-1", Name: "Alice's phone"}

// TestSurplusPastTheCollapseBoundIsNotCountedDelivered is A4 and A19, and it is the test that goes
// RED if the bound is not built: an implementation that records every crossing `handed-over`
// produces ZERO `beyond-collapse-bound` outcomes and the surplus assertion below fails.
//
// Six distinct (device, Place) pairs cross for one endpoint that has not re-registered. FCM stores
// four collapsible messages per device, one per collapse key, so the surplus past four cannot be
// held alongside them. The four within the bound are `handed-over` - which is not "delivered"
// either - and the surplus two are recorded as their own outcome. Nothing anywhere claims delivery,
// even though the fake backend accepted all six (A5).
func TestSurplusPastTheCollapseBoundIsNotCountedDelivered(t *testing.T) {
	t.Parallel()

	h := newAccountingHarness(t, []store.PushSubscription{
		{Provider: PushProviderKeyFCM, Token: "phone-1"},
	}, nil)

	at := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	for i, place := range []string{"place-1", "place-2", "place-3", "place-4", "place-5", "place-6"} {
		h.notifier.NotifyGeofenceEvents(context.Background(),
			accountingDevice,
			[]store.GeofenceEvent{crossing(accountingDevice.ID, place, "Place "+place, "enter", at.Add(time.Duration(i)*time.Minute))})
	}
	h.disp.Close() // drains the worker, so every outcome is in the sink before it is read

	recs := outcomes(t, h.sink)
	if len(recs) != 6 {
		t.Fatalf("recorded %d outcomes, want one per crossing (6): %+v", len(recs), recs)
	}
	counts := countByOutcome(t, recs)
	if counts[string(OutcomeBeyondCollapseBound)] < 2 {
		t.Errorf("%d crossings recorded %s, want at least the surplus 2 past the bound of %d - the bound is not being applied: %+v",
			counts[string(OutcomeBeyondCollapseBound)], OutcomeBeyondCollapseBound, CollapseKeyBound, recs)
	}
	if counts[string(OutcomeHandedOver)] != CollapseKeyBound {
		t.Errorf("%d crossings recorded %s, want exactly the %d within the bound: %+v",
			counts[string(OutcomeHandedOver)], OutcomeHandedOver, CollapseKeyBound, recs)
	}
	if counts[string(OutcomeDropped)] != 0 {
		t.Errorf("%d crossings recorded %s, want none - the fake backend accepted every one: %+v",
			counts[string(OutcomeDropped)], OutcomeDropped, recs)
	}

	// The first four crossings, in order, are the ones within the bound; the surplus is the tail.
	// Asserting the ORDER as well as the counts is what stops a shuffled implementation passing.
	for i, r := range recs {
		want := OutcomeHandedOver
		if i >= CollapseKeyBound {
			want = OutcomeBeyondCollapseBound
		}
		if DeliveryOutcome(r.Outcome) != want {
			t.Errorf("crossing %d recorded %q, want %q", i+1, r.Outcome, want)
		}
	}

	// A5 and A7 restated as a text assertion over the operator-readable output: no record may say,
	// or contain, anything that reads as a delivery claim.
	for _, forbidden := range []string{"deliver", "arrived", "received", "sent"} {
		if strings.Contains(strings.ToLower(h.sink.String()), `"outcome":"`+forbidden) {
			t.Errorf("an outcome reads as a delivery claim (%q) - the backend accepting a message is not delivery", forbidden)
		}
	}
}

// TestBackendAcceptanceIsNeverDelivery is A5 on its own: the sender succeeds, and the outcome
// recorded is still only the hand-over classification. Nothing observes arrival, so nothing may
// record it.
func TestBackendAcceptanceIsNeverDelivery(t *testing.T) {
	t.Parallel()

	h := newAccountingHarness(t, []store.PushSubscription{
		{Provider: PushProviderKeyFCM, Token: "phone-1"},
	}, nil)

	h.notifier.NotifyGeofenceEvents(context.Background(), accountingDevice,
		[]store.GeofenceEvent{crossing(accountingDevice.ID, "place-1", "School", "enter", time.Unix(1770000000, 0).UTC())})
	h.disp.Close()

	recs := outcomes(t, h.sink)
	if len(recs) != 1 {
		t.Fatalf("recorded %d outcomes, want 1: %+v", len(recs), recs)
	}
	if DeliveryOutcome(recs[0].Outcome) != OutcomeHandedOver {
		t.Errorf("outcome = %q after the backend accepted the message, want %q", recs[0].Outcome, OutcomeHandedOver)
	}
	if recs[0].CollapseKey != "gf:dev-1:place-1" {
		t.Errorf("outcome record collapse_key = %q, want the per-(device,Place) key", recs[0].CollapseKey)
	}
	// The record is per ENDPOINT: an operator asking what became of this crossing for this phone
	// gets the provider back, not a family-wide summary.
	if recs[0].Provider != PushProviderKeyFCM {
		t.Errorf("outcome record provider = %q, want %q", recs[0].Provider, PushProviderKeyFCM)
	}
}

// TestRegistrationResetsThePendingKeys is A20: an endpoint re-presenting itself through registration
// is the only evidence tracker gets that the receiving app has RUN, so its distinct-key count
// returns to zero and the next four crossings are within the bound again.
func TestRegistrationResetsThePendingKeys(t *testing.T) {
	t.Parallel()

	h := newAccountingHarness(t, []store.PushSubscription{
		{Provider: PushProviderKeyFCM, Token: "phone-1"},
	}, nil)

	at := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	for i, place := range []string{"place-1", "place-2", "place-3", "place-4"} {
		h.notifier.NotifyGeofenceEvents(context.Background(), accountingDevice,
			[]store.GeofenceEvent{crossing(accountingDevice.ID, place, "Place", "enter", at.Add(time.Duration(i)*time.Minute))})
	}
	if got := h.disp.Ledger().PendingKeys(PushProviderKeyFCM, "phone-1"); got != CollapseKeyBound {
		t.Fatalf("pending keys = %d after four distinct crossings, want %d", got, CollapseKeyBound)
	}

	// The phone came back and registered. That is evidence its app has run.
	h.notifier.EndpointRegistered(context.Background(), PushProviderKeyFCM, "phone-1")
	if got := h.disp.Ledger().PendingKeys(PushProviderKeyFCM, "phone-1"); got != 0 {
		t.Fatalf("pending keys = %d after re-registration, want 0 (A20)", got)
	}

	for i, place := range []string{"place-5", "place-6", "place-7", "place-8"} {
		h.notifier.NotifyGeofenceEvents(context.Background(), accountingDevice,
			[]store.GeofenceEvent{crossing(accountingDevice.ID, place, "Place", "enter", at.Add(time.Duration(10+i)*time.Minute))})
	}
	h.disp.Close()

	recs := outcomes(t, h.sink)
	if len(recs) != 8 {
		t.Fatalf("recorded %d outcomes, want 8: %+v", len(recs), recs)
	}
	counts := countByOutcome(t, recs)
	if counts[string(OutcomeBeyondCollapseBound)] != 0 {
		t.Errorf("%d crossings recorded %s, want 0 - the re-registration reset the count: %+v",
			counts[string(OutcomeBeyondCollapseBound)], OutcomeBeyondCollapseBound, recs)
	}
	if counts[string(OutcomeHandedOver)] != 8 {
		t.Errorf("%d crossings recorded %s, want 8", counts[string(OutcomeHandedOver)], OutcomeHandedOver)
	}
}

// TestSupersededEndpointIsForgotten covers D8's other half of the ledger: an endpoint the server has
// removed because a rotated address superseded it holds no accounting afterwards.
func TestSupersededEndpointIsForgotten(t *testing.T) {
	t.Parallel()

	h := newAccountingHarness(t, []store.PushSubscription{
		{Provider: PushProviderKeyFCM, Token: "old-address"},
	}, nil)
	defer h.disp.Close()

	h.notifier.NotifyGeofenceEvents(context.Background(), accountingDevice,
		[]store.GeofenceEvent{crossing(accountingDevice.ID, "place-1", "School", "enter", time.Unix(1770000000, 0).UTC())})
	if got := h.disp.Ledger().PendingKeys(PushProviderKeyFCM, "old-address"); got != 1 {
		t.Fatalf("pending keys = %d, want 1", got)
	}

	h.notifier.EndpointRemoved(context.Background(), PushProviderKeyFCM, "old-address")
	if got := h.disp.Ledger().PendingKeys(PushProviderKeyFCM, "old-address"); got != 0 {
		t.Errorf("pending keys = %d after the endpoint was superseded and removed, want 0", got)
	}
}

// TestRestartLosesTowardHandedOverAndNeverTowardDelivery is A21, in both directions it names.
//
// A restart forgets the pending-key accounting (it is process state, D4). The permitted consequence
// is that a later crossing is reported `handed-over` where an unrestarted server would have said
// `beyond-collapse-bound` - a move toward knowing LESS. The forbidden consequence is any move
// toward a delivery claim, and there is none available: `handed-over` is not delivery.
//
// The second half of A21 is that an outcome already made operator-readable is still readable
// afterwards. It is, because the record is a log line in the deployment's log stream, which outlives
// the process that wrote it - the sink here stands in for that stream and is asserted to still hold
// every pre-restart record.
func TestRestartLosesTowardHandedOverAndNeverTowardDelivery(t *testing.T) {
	t.Parallel()

	h := newAccountingHarness(t, []store.PushSubscription{
		{Provider: PushProviderKeyFCM, Token: "phone-1"},
	}, nil)

	at := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	for i, place := range []string{"place-1", "place-2", "place-3", "place-4"} {
		h.notifier.NotifyGeofenceEvents(context.Background(), accountingDevice,
			[]store.GeofenceEvent{crossing(accountingDevice.ID, place, "Place", "enter", at.Add(time.Duration(i)*time.Minute))})
	}
	before := len(outcomes(t, h.sink))

	// Without a restart the next crossing would be beyond the bound. Prove that first, so the
	// comparison after the restart is against a real difference and not an assumption.
	if got := h.disp.Ledger().Classify(endpointKey{provider: PushProviderKeyFCM, token: "phone-1"}, "gf:dev-1:place-9"); got != OutcomeBeyondCollapseBound {
		t.Fatalf("classification with four keys pending = %q, want %q", got, OutcomeBeyondCollapseBound)
	}

	// The restart: the accounting is gone, the log stream is not.
	h.disp.Ledger().Reset()

	h.notifier.NotifyGeofenceEvents(context.Background(), accountingDevice,
		[]store.GeofenceEvent{crossing(accountingDevice.ID, "place-5", "Place", "enter", at.Add(time.Hour))})
	h.disp.Close()

	recs := outcomes(t, h.sink)
	if len(recs) < before {
		t.Fatalf("the log holds %d outcomes after the restart, fewer than the %d recorded before it - a recorded outcome must stay readable (A21)", len(recs), before)
	}
	counts := countByOutcome(t, recs)
	if counts[string(OutcomeBeyondCollapseBound)] != 0 {
		t.Errorf("an outcome past the bound survived a restart that forgot the pending keys: %+v", recs)
	}
	last := recs[len(recs)-1]
	if DeliveryOutcome(last.Outcome) != OutcomeHandedOver {
		t.Errorf("post-restart crossing recorded %q, want %q - losing the accounting may only move an outcome toward knowing less",
			last.Outcome, OutcomeHandedOver)
	}
}

// TestDropPathsAreRecordedAsDropped covers the third outcome in all three ways the definition names
// it: no sender for the provider, retries exhausted, and the pending queue full. None of them was
// ever handed over, so none of them may read as anything else.
func TestDropPathsAreRecordedAsDropped(t *testing.T) {
	t.Parallel()

	t.Run("no sender configured for the provider", func(t *testing.T) {
		t.Parallel()
		sink := &syncBuffer{}
		logger := slog.New(slog.NewJSONHandler(sink, nil))
		// Only fcm is configured; the endpoint is unifiedpush.
		disp := NewDispatcher(map[string]Sender{PushProviderKeyFCM: senderFunc(func(context.Context, Delivery) error { return nil })},
			logger, WithRetryBackoff(0))
		disp.Enqueue(Delivery{
			Sub:  Subscription{Provider: PushProviderKeyUnifiedPush, Token: "up-endpoint"},
			Note: Notification{CollapseKey: "gf:dev-1:place-1", Data: map[string]string{"device_id": "dev-1"}},
		})
		disp.Close()

		recs := outcomes(t, sink)
		if len(recs) != 1 || DeliveryOutcome(recs[0].Outcome) != OutcomeDropped {
			t.Fatalf("outcomes = %+v, want exactly one %q", recs, OutcomeDropped)
		}
	})

	t.Run("retries exhausted", func(t *testing.T) {
		t.Parallel()
		sink := &syncBuffer{}
		logger := slog.New(slog.NewJSONHandler(sink, nil))
		disp := NewDispatcher(map[string]Sender{
			PushProviderKeyFCM: senderFunc(func(context.Context, Delivery) error { return errors.New("backend refused") }),
		}, logger, WithMaxAttempts(2), WithRetryBackoff(0))
		disp.Enqueue(Delivery{
			Sub:   Subscription{Provider: PushProviderKeyFCM, Token: "phone-1"},
			Note:  Notification{CollapseKey: "gf:dev-1:place-1", Data: map[string]string{"device_id": "dev-1"}},
			Bound: OutcomeHandedOver,
		})
		disp.Close()

		recs := outcomes(t, sink)
		if len(recs) != 1 || DeliveryOutcome(recs[0].Outcome) != OutcomeDropped {
			t.Fatalf("outcomes = %+v, want exactly one %q - a delivery that never landed is not handed over", recs, OutcomeDropped)
		}
	})

	t.Run("pending queue full", func(t *testing.T) {
		t.Parallel()
		sink := &syncBuffer{}
		logger := slog.New(slog.NewJSONHandler(sink, nil))
		release := make(chan struct{})
		entered := make(chan struct{}, 1)
		disp := NewDispatcher(map[string]Sender{
			PushProviderKeyFCM: senderFunc(func(context.Context, Delivery) error {
				select {
				case entered <- struct{}{}:
				default:
				}
				<-release
				return nil
			}),
		}, logger, WithMaxPending(1), WithRetryBackoff(0))

		disp.Enqueue(Delivery{Sub: Subscription{Provider: PushProviderKeyFCM, Token: "phone-1"},
			Note: Notification{CollapseKey: "gf:dev-1:blocker"}})
		<-entered
		for i := 0; i < 5; i++ {
			disp.Enqueue(Delivery{Sub: Subscription{Provider: PushProviderKeyFCM, Token: "phone-1"},
				Note: Notification{CollapseKey: "gf:dev-1:overflow"}})
		}
		close(release)
		disp.Close()

		var dropped int
		for _, r := range outcomes(t, sink) {
			if DeliveryOutcome(r.Outcome) == OutcomeDropped {
				dropped++
			}
		}
		if dropped == 0 {
			t.Fatal("a delivery refused by the full pending queue recorded no `dropped` outcome")
		}
	})
}

// TestPendingAccountingIsBoundedInMemory pins the leak guard, and the direction it fails in: an
// evicted endpoint reads as having no keys pending, which is the same safe direction a restart loses
// in. It never invents a bound where there was none.
func TestPendingAccountingIsBoundedInMemory(t *testing.T) {
	t.Parallel()

	l := NewDeliveryLedger(slog.New(slog.NewJSONHandler(&syncBuffer{}, nil)))
	first := endpointKey{provider: PushProviderKeyFCM, token: "phone-0"}
	l.Classify(first, "gf:dev-1:place-1")

	for i := 1; i <= maxTrackedEndpoints; i++ {
		l.Classify(endpointKey{provider: PushProviderKeyFCM, token: "phone-" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + itoa(i)}, "gf:dev:place")
	}

	if got := l.PendingKeys(PushProviderKeyFCM, first.token); got != 0 {
		t.Errorf("the oldest endpoint still holds %d pending keys past the %d-endpoint bound, want 0 (evicted)", got, maxTrackedEndpoints)
	}
}

// itoa avoids pulling strconv into a test that needs exactly one integer rendered.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
