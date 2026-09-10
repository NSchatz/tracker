package server

// The S6 push-registration HTTP surface: how an endpoint gets onto the registry the fan-out reads.
// Delivery itself is driven off the ingestion path through the Notifier.
//
// It is VIEWER-scoped because a watcher is who wants the alert on their own phone (§5.3), and the
// viewer it registers under is THE AUTHENTICATED CALLER, never a field in the body. A viewer cannot
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
	// ReplacesToken names a routing address this registration SUPERSEDES (ALERT-2 D8). Optional and
	// additive. Registration is idempotent on (provider, token), which refreshes an UNCHANGED address
	// but cannot help a rotated one: a rotated FCM registration token is a new address, so the phone
	// would hold two deliverable rows and receive every crossing twice. Only the phone knows its own
	// previous address, so it names it.
	ReplacesToken *string `json:"replaces_token"`
}

// pushSubscriptionResponse echoes the stored subscription's id and provider so a client can confirm
// what landed. The token is not echoed — the client already holds it, and not reflecting it keeps it
// out of one more place.
type pushSubscriptionResponse struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	// ConfiguredProvider is the push backend THIS DEPLOYMENT has configured, or "" when it has none
	// (ALERT-2 D7/A22). Provider echoes what the caller registered; this says whether anything will
	// ever be sent through it, so an app can say "registered, but alerts are not being received"
	// instead of showing armed. It is always PRESENT and empty when there is no backend, so a client
	// can tell "configured nothing" from "older than A22 and does not report" (an absent key).
	ConfiguredProvider string `json:"configured_provider"`
}

// registerPushSubscription serves POST /v1/push-subscriptions: register (or refresh) the calling
// viewer's push endpoint. Strict decode — unknown fields and trailing data are rejected — so a
// malformed registration is a typed 400 that stores nothing, matching the ingestion path's stance.
//
// configuredProvider is the deployment's own backend, reported in the response (A22).
func registerPushSubscription(database DB, notifier Notifier, configuredProvider string, logger *slog.Logger) http.HandlerFunc {
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

		// An endpoint that re-presents itself has proved its app is running, which is the only
		// evidence tracker ever gets that what was pending for it has been seen. Reset its
		// collapse-key accounting (A20).
		notifier.EndpointRegistered(r.Context(), *req.Provider, token)

		// The supersede (A24), ordered AFTER the register on purpose: a store failure between the two
		// leaves the phone with two rows (a crossing twice, noisy) rather than none (the crossing
		// silently lost).
		//
		// A25: the removal is scoped to the AUTHENTICATED viewer in the SQL and nothing about it
		// reaches the response, so naming an address registered to another viewer, or to nobody,
		// removes nothing and is byte-identical to the succeeding case. The route cannot be used to
		// discover whether an address exists or to unregister somebody else's phone.
		if req.ReplacesToken != nil {
			replaced := strings.TrimSpace(*req.ReplacesToken)
			// Replacing an address with itself is the idempotent re-registration, not a supersede;
			// deleting here would remove the row that was just stored.
			if replaced != "" && replaced != token {
				removed, err := store.RemovePushSubscriptionForViewer(r.Context(), database, v.ID, *req.Provider, replaced)
				if err != nil {
					// Logged, never surfaced: a failed supersede leaves a stale row that wastes a send
					// to a dead address, which is strictly better than failing a registration that
					// already succeeded. The token is NOT logged - it is a routing address for a
					// family's alerts.
					logger.ErrorContext(r.Context(), "remove superseded push subscription", "error", err, "viewer_id", v.ID)
				} else if removed {
					notifier.EndpointRemoved(r.Context(), *req.Provider, replaced)
				}
			}
		}

		writeJSON(w, logger, http.StatusCreated, pushSubscriptionResponse{
			ID:                 id,
			Provider:           *req.Provider,
			ConfiguredProvider: configuredProvider,
		})
	}
}
