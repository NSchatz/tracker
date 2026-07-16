package server

// The S6 push-registration HTTP surface. A viewer registers the push endpoint of its phone so a
// family's geofence crossings can be delivered to it. Delivery itself is not here — it is driven off
// the ingestion path through the Notifier (see ingest) — this is only how an endpoint gets onto the
// registry the fan-out reads.
//
// Why VIEWER-scoped, and a WRITE: a watcher (a viewer) is who wants the alert on their own phone
// (§5.3), so the credential that registers an endpoint is the viewer token, and the viewer it
// registers under is the authenticated caller — never a field in the body. A viewer therefore cannot
// register an endpoint under another viewer, the same authz-by-construction the ingestion path uses
// for the device identity.

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/NSchatz/tracker/internal/store"
)

// pushSubscriptionRequest is the registration payload: which backend, and the routing address for it
// (an FCM registration token, or a UnifiedPush endpoint URL). Both are pointers so ABSENT is
// distinguishable from an empty string — a missing field is a typed 400, not a silently-stored blank.
type pushSubscriptionRequest struct {
	Provider *string `json:"provider"`
	Token    *string `json:"token"`
}

// pushSubscriptionResponse echoes the stored subscription's id and provider so a client can confirm
// what landed. The token is not echoed — the client already holds it, and not reflecting it keeps it
// out of one more place.
type pushSubscriptionResponse struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
}

// registerPushSubscription serves POST /v1/push-subscriptions: register (or refresh) the calling
// viewer's push endpoint. Strict decode — unknown fields and trailing data are rejected — so a
// malformed registration is a typed 400 that stores nothing, matching the ingestion path's stance.
func registerPushSubscription(database DB, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v := viewerFrom(r.Context())

		var req pushSubscriptionRequest
		if err := decodeStrict(w, r, &req); err != nil {
			writeError(w, logger, http.StatusBadRequest, "malformed", err.Error())
			return
		}
		if req.Provider == nil || req.Token == nil {
			writeError(w, logger, http.StatusBadRequest, "malformed", "provider and token are required")
			return
		}
		token := strings.TrimSpace(*req.Token)
		if token == "" {
			writeError(w, logger, http.StatusBadRequest, "malformed", "token must not be empty")
			return
		}
		if !store.ValidPushProvider(*req.Provider) {
			writeError(w, logger, http.StatusBadRequest, "malformed",
				"provider must be one of \"fcm\", \"unifiedpush\"")
			return
		}

		id, err := store.RegisterPushSubscription(r.Context(), database, v.ID, *req.Provider, token)
		if err != nil {
			// A bad provider is the caller's to fix (a 400); anything else is ours. The provider was
			// already validated above, so this branch is defence in depth, not the primary path.
			if errors.Is(err, store.ErrInvalidPushProvider) {
				writeError(w, logger, http.StatusBadRequest, "malformed", err.Error())
				return
			}
			logger.ErrorContext(r.Context(), "register push subscription", "error", err, "viewer_id", v.ID)
			writeError(w, logger, http.StatusInternalServerError, "internal", "could not register the push subscription")
			return
		}

		writeJSON(w, logger, http.StatusCreated, pushSubscriptionResponse{ID: id, Provider: *req.Provider})
	}
}
