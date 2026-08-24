package push

// REGRESSION ARTIFACT for S0013-tracker-alert-2, impl verdict 1, finding F1.
//
// This test DOCUMENTS a defect; it is not a fix. It is expected to FAIL against the branch as
// reviewed.
//
// A7: "WHEN the server is asked what became of a crossing it handed over THE SYSTEM SHALL make the
// PER-ENDPOINT delivery outcome readable to an operator without a debugger".
//
// The spec's Definitions fix what an endpoint is: "one registered push subscription: a provider
// PLUS the routing address for one phone. The server keys a registration on (provider, routing
// address)". `DeliveryLedger.Record` writes only half of that key - the provider - so two phones
// registered in the same family under the same provider produce outcome records that are
// byte-identical apart from the outcome itself. The operator can read that ONE of two phones lost
// the alert past the collapse bound and the other did not; they cannot read WHICH.
//
// The scenario below is the one android/README.md's own device check step 7 tells an operator to
// run ("grep push.delivery.outcome to show beyond-collapse-bound for the surplus"), with the second
// phone a family normally has.
//
// Fixing it does NOT require logging the routing address: a stable digest or short prefix of it
// would identify the endpoint in the log without putting a family's delivery address there.

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// TestRegress0013F1OutcomeRecordsCannotBeAttributedToAnEndpoint drives one crossing out to TWO fcm
// endpoints in one family, where one endpoint is already past the collapse-key bound and the other
// is not, and then reads the operator-readable output the way an operator would.
func TestRegress0013F1OutcomeRecordsCannotBeAttributedToAnEndpoint(t *testing.T) {
	sink := &syncBuffer{}
	logger := slog.New(slog.NewJSONHandler(sink, &slog.HandlerOptions{Level: slog.LevelInfo}))

	disp := NewDispatcher(map[string]Sender{
		PushProviderKeyFCM: senderFunc(func(context.Context, Delivery) error { return nil }),
	}, logger, WithRetryBackoff(0))

	// One offline phone that has already accumulated the full collapse-key bound, and one phone that
	// has just re-registered and so has nothing pending. Both are fcm endpoints in the same family.
	const offlinePhone = "address-offline-phone"
	const freshPhone = "address-fresh-phone"
	for _, place := range []string{"place-1", "place-2", "place-3", "place-4"} {
		disp.Ledger().Classify(endpointKey{provider: PushProviderKeyFCM, token: offlinePhone}, "gf:dev-1:"+place)
	}

	// The crossing both phones should be told about. The offline one is past the bound; the fresh one
	// is not - so exactly one of these two people loses this alert.
	note := buildNotificationForRegress()
	disp.Enqueue(
		Delivery{
			Sub:   Subscription{Provider: PushProviderKeyFCM, Token: offlinePhone},
			Note:  note,
			Bound: disp.Ledger().Classify(endpointKey{provider: PushProviderKeyFCM, token: offlinePhone}, note.CollapseKey),
		},
		Delivery{
			Sub:   Subscription{Provider: PushProviderKeyFCM, Token: freshPhone},
			Note:  note,
			Bound: disp.Ledger().Classify(endpointKey{provider: PushProviderKeyFCM, token: freshPhone}, note.CollapseKey),
		},
	)
	disp.Close()

	recs := rawOutcomeRecords(t, sink)
	if len(recs) != 2 {
		t.Fatalf("recorded %d outcome records for one crossing at two endpoints, want 2: %+v", len(recs), recs)
	}

	// Sanity: the two endpoints genuinely got different outcomes, so the operator has a real question
	// to ask ("which phone lost it?") rather than a hypothetical one.
	outcomeA, _ := recs[0]["outcome"].(string)
	outcomeB, _ := recs[1]["outcome"].(string)
	if outcomeA == outcomeB {
		t.Fatalf("both endpoints recorded %q; the scenario did not put one past the bound", outcomeA)
	}

	// A7. Strip the outcome and compare what is left: if the remainder is identical, the record says
	// nothing about WHICH endpoint the outcome belongs to, and the per-endpoint outcome is not
	// readable - only the family-wide multiset of outcomes is.
	restA := withoutOutcome(t, recs[0])
	restB := withoutOutcome(t, recs[1])
	if restA == restB {
		t.Fatalf(
			"A7 violated: the two outcome records for one crossing at two DIFFERENT endpoints are identical "+
				"apart from the outcome itself, so an operator cannot tell which phone lost the alert.\n"+
				"  endpoint %q recorded %q\n  endpoint %q recorded %q\n"+
				"  both records, outcome removed: %s\n"+
				"An endpoint is (provider, routing address) per the spec's Definitions; only the provider is recorded.",
			offlinePhone, outcomeA, freshPhone, outcomeB, restA)
	}
}

// buildNotificationForRegress is the notification the fan-out would build for one crossing. It is
// constructed here rather than via buildNotification so this artifact does not depend on the
// evaluator's types.
func buildNotificationForRegress() Notification {
	ts := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	return Notification{
		Title:       "Alice's phone",
		Body:        "Alice's phone arrived at School",
		CollapseKey: "gf:dev-1:place-9",
		Data: map[string]string{
			"type":       "geofence",
			"device_id":  "dev-1",
			"place_id":   "place-9",
			"place_name": "School",
			"transition": "enter",
			"ts":         ts.Format("2006-01-02T15:04:05Z"),
		},
	}
}

// rawOutcomeRecords reads every delivery-outcome record out of the captured log as a raw map, the
// way an operator greps for it - no struct in between, so a field this test does not know about is
// still seen.
func rawOutcomeRecords(t *testing.T, sink *syncBuffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(sink.String(), "\n") {
		if !strings.Contains(line, DeliveryOutcomeMessage) {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("delivery-outcome log line is not readable JSON: %v (%q)", err, line)
		}
		if msg, _ := rec["msg"].(string); msg != DeliveryOutcomeMessage {
			continue
		}
		out = append(out, rec)
	}
	return out
}

// withoutOutcome renders a record with the outcome and the slog timestamp removed, so two records
// can be compared for everything that could identify the endpoint they belong to.
func withoutOutcome(t *testing.T, rec map[string]any) string {
	t.Helper()
	copied := make(map[string]any, len(rec))
	for k, v := range rec {
		if k == "outcome" || k == "time" {
			continue
		}
		copied[k] = v
	}
	encoded, err := json.Marshal(copied)
	if err != nil {
		t.Fatalf("re-encode outcome record: %v", err)
	}
	return string(encoded)
}
