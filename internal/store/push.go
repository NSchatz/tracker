package store

// S6 — the push-subscription registry (schema in 00004_push_subscriptions.sql). Two things live here:
// registering a viewer's push endpoint, and the fan-out read the sender uses to find every endpoint a
// family's crossing should reach. Both are family-scoped in the same spirit as every other read/write
// in this package — a viewer registers only its own endpoint, and a family's fan-out never returns
// another family's endpoints.

import (
	"context"
	"errors"
	"fmt"

	"github.com/NSchatz/tracker/internal/db"
)

// PushProviderFCM and PushProviderUnifiedPush are the two delivery backends S6 ships (roadmap §1).
// They are the exact string values of the push_provider enum in the schema — the Go constant and the
// SQL enum are one closed set, so a value that would be rejected by the column is rejected here first
// with a sentence rather than a driver error.
const (
	PushProviderFCM         = "fcm"
	PushProviderUnifiedPush = "unifiedpush"
)

// ErrInvalidPushProvider is returned when a registration names a provider that is not one of the
// known backends. It is the typed, fail-safe rejection §6 requires: an unknown provider is a caller
// error the sender could never route, so it is refused at registration, not stored to fail silently
// at delivery.
var ErrInvalidPushProvider = errors.New("unknown push provider")

// ValidPushProvider reports whether p is one of the providers this deployment can deliver to. The
// registration path checks it before the INSERT so the caller learns "unknown provider" rather than a
// constraint violation from three layers down.
func ValidPushProvider(p string) bool {
	return p == PushProviderFCM || p == PushProviderUnifiedPush
}

// PushSubscription is one registered push endpoint on the fan-out side: which backend to send through
// and the routing address to send to (an FCM registration token or a UnifiedPush endpoint URL — see
// the schema header). It is deliberately NOT the whole row: the sender needs only where to deliver,
// not who owns it or when it was registered.
type PushSubscription struct {
	Provider string
	Token    string
}

// registerPushSubscriptionSQL upserts a viewer's push endpoint, keyed on the endpoint's identity
// (provider, token). A phone re-registering — a fresh launch, or an FCM token that rotated and was
// re-POSTed — must MOVE the endpoint to the presenting viewer and refresh its timestamp, never add a
// second row that would push the same phone twice per crossing. That is exactly the idempotent
// re-registration the (provider, token) UNIQUE constraint makes possible.
const registerPushSubscriptionSQL = `
	INSERT INTO push_subscriptions (viewer_id, provider, token)
	VALUES ($1, $2::push_provider, $3)
	ON CONFLICT (provider, token)
	DO UPDATE SET viewer_id = EXCLUDED.viewer_id, created_at = now()
	RETURNING id`

// RegisterPushSubscription records (or refreshes) a viewer's push endpoint and returns its id. The
// provider is validated in Go first — an unknown backend is ErrInvalidPushProvider, never a stored
// row the sender cannot route. The viewer is the authenticated caller (its id comes from the viewer
// token, never the request body), so a viewer can only ever register an endpoint under itself.
func RegisterPushSubscription(ctx context.Context, q db.Querier, viewerID, provider, token string) (string, error) {
	if !ValidPushProvider(provider) {
		return "", fmt.Errorf("%w: %q is not one of %q, %q", ErrInvalidPushProvider, provider, PushProviderFCM, PushProviderUnifiedPush)
	}
	var id string
	if err := q.QueryRow(ctx, registerPushSubscriptionSQL, viewerID, provider, token).Scan(&id); err != nil {
		return "", fmt.Errorf("register push subscription for viewer %s: %w", viewerID, err)
	}
	return id, nil
}

// pushSubscriptionsForFamilySQL is the fan-out read: every push endpoint belonging to a viewer in the
// family. Scoped by family through the viewer join — a crossing in one family never fans out to
// another family's phones, the same boundary every read in this package holds. Ordered for a stable,
// deterministic delivery order (it makes the tests legible; delivery itself does not depend on it).
const pushSubscriptionsForFamilySQL = `
	SELECT ps.provider::text, ps.token
	FROM push_subscriptions ps
	JOIN viewers v ON v.id = ps.viewer_id
	WHERE v.family_id = $1
	ORDER BY ps.provider, ps.token`

// PushSubscriptionsForFamily returns every push endpoint the family's crossings should reach. A
// family with no registered endpoints yields an empty slice, never another family's endpoints — so a
// caller iterating the result sends nothing rather than leaking a stranger's routing address.
func PushSubscriptionsForFamily(ctx context.Context, q db.Querier, familyID string) ([]PushSubscription, error) {
	rows, err := q.Query(ctx, pushSubscriptionsForFamilySQL, familyID)
	if err != nil {
		return nil, fmt.Errorf("list push subscriptions for family %s: %w", familyID, err)
	}
	defer rows.Close()

	var out []PushSubscription
	for rows.Next() {
		var s PushSubscription
		if err := rows.Scan(&s.Provider, &s.Token); err != nil {
			return nil, fmt.Errorf("scan push subscription: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list push subscriptions for family %s: %w", familyID, err)
	}
	return out, nil
}
