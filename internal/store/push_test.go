package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/NSchatz/tracker/internal/db"
)

// TestPushSubscriptions covers the S6 registry: idempotent registration keyed on the endpoint, the
// family-scoped fan-out the sender reads, and rejection of an unknown provider. Real PostGIS, isolated
// by family like the rest of the package.
func TestPushSubscriptions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool := setup(ctx, t)

	famA, _ := seedDevice(ctx, t, pool, "push-a")
	famB, _ := seedDevice(ctx, t, pool, "push-b")

	viewerA1 := mustViewer(ctx, t, pool, famA, "a1")
	viewerA2 := mustViewer(ctx, t, pool, famA, "a2")
	viewerB := mustViewer(ctx, t, pool, famB, "b1")

	t.Run("register is idempotent on (provider, token)", func(t *testing.T) {
		id1, err := RegisterPushSubscription(ctx, pool, viewerA1, PushProviderFCM, "token-xyz")
		if err != nil {
			t.Fatalf("register: %v", err)
		}
		// Same endpoint again — even under a different viewer — must UPDATE, not duplicate.
		id2, err := RegisterPushSubscription(ctx, pool, viewerA2, PushProviderFCM, "token-xyz")
		if err != nil {
			t.Fatalf("re-register: %v", err)
		}
		if id1 != id2 {
			t.Fatalf("re-registering the same endpoint made a new row (%s != %s)", id1, id2)
		}
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM push_subscriptions WHERE token = $1`, "token-xyz").Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		if n != 1 {
			t.Fatalf("endpoint has %d rows, want exactly 1 (idempotent upsert)", n)
		}
	})

	t.Run("fan-out is family-scoped", func(t *testing.T) {
		// Family A: two endpoints across two viewers, one of each provider. Family B: one endpoint.
		if _, err := RegisterPushSubscription(ctx, pool, viewerA1, PushProviderFCM, "a-fcm"); err != nil {
			t.Fatalf("register a-fcm: %v", err)
		}
		if _, err := RegisterPushSubscription(ctx, pool, viewerA2, PushProviderUnifiedPush, "https://ntfy.example/a-up"); err != nil {
			t.Fatalf("register a-up: %v", err)
		}
		if _, err := RegisterPushSubscription(ctx, pool, viewerB, PushProviderFCM, "b-fcm"); err != nil {
			t.Fatalf("register b-fcm: %v", err)
		}

		subs, err := PushSubscriptionsForFamily(ctx, pool, famA)
		if err != nil {
			t.Fatalf("fan-out A: %v", err)
		}
		tokens := map[string]string{}
		for _, s := range subs {
			tokens[s.Token] = s.Provider
		}
		if _, ok := tokens["a-fcm"]; !ok {
			t.Errorf("family A fan-out missing a-fcm: %v", subs)
		}
		if _, ok := tokens["https://ntfy.example/a-up"]; !ok {
			t.Errorf("family A fan-out missing the UnifiedPush endpoint: %v", subs)
		}
		if _, ok := tokens["b-fcm"]; ok {
			t.Fatalf("family A fan-out leaked family B's endpoint: %v", subs)
		}

		// Family B sees only its own.
		bSubs, err := PushSubscriptionsForFamily(ctx, pool, famB)
		if err != nil {
			t.Fatalf("fan-out B: %v", err)
		}
		if len(bSubs) != 1 || bSubs[0].Token != "b-fcm" {
			t.Fatalf("family B fan-out = %v, want only b-fcm", bSubs)
		}
	})

	t.Run("an unknown provider is refused, nothing stored", func(t *testing.T) {
		if _, err := RegisterPushSubscription(ctx, pool, viewerA1, "telegram", "whatever"); !errors.Is(err, ErrInvalidPushProvider) {
			t.Fatalf("register unknown provider = %v, want ErrInvalidPushProvider", err)
		}
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM push_subscriptions WHERE token = $1`, "whatever").Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		if n != 0 {
			t.Fatalf("an unknown-provider registration stored %d rows, want 0", n)
		}
	})
}

// mustViewer creates a viewer in a family and returns its id, failing the test on error.
func mustViewer(ctx context.Context, t *testing.T, pool db.Querier, familyID, label string) string {
	t.Helper()
	hash := sha256.Sum256([]byte(familyID + "/" + label))
	id, err := CreateViewer(ctx, pool, familyID, label+"@example.test", label, hash[:])
	if err != nil {
		t.Fatalf("CreateViewer %s: %v", label, err)
	}
	return id
}
