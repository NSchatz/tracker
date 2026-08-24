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
	// ReplacesToken names a routing address this registration SUPERSEDES - the one the phone was
	// registered under before its address rotated (ALERT-2 D8). Optional and additive: a client that
	// omits it behaves exactly as before.
	//
	// It exists because registration is idempotent on (provider, token), which refreshes an
	// UNCHANGED address but cannot help a rotated one: an FCM registration token that rotates is a
	// new address, so the phone would hold two deliverable rows and receive every crossing twice.
	// The phone is the only party that knows its own previous address, so it names it.
	ReplacesToken *string `json:"replaces_token"`
}

// pushSubscriptionResponse echoes the stored subscription's id and provider so a client can confirm
// what landed. The token is not echoed — the client already holds it, and not reflecting it keeps it
// out of one more place.
type pushSubscriptionResponse struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	// ConfiguredProvider is the push backend THIS DEPLOYMENT has configured, or "" when it has
	// configured none (ALERT-2 D7/A22). It is not the same thing as Provider above: Provider echoes
	// what the caller registered, ConfiguredProvider says whether anything will ever be sent through
	// it. The registration is accepted and stored either way - a deployment can configure a backend
	// after a phone has registered - so this is the one honest way for an app to say "registered,
	// but alerts are not being received" instead of showing armed.
	//
	// The field is always PRESENT, empty when there is no backend, so a client can tell "this server
	// configured nothing" from "this server is older than A22 and does not report" (an absent key).
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

		// The supersede (A24). Ordered AFTER the register on purpose: if the store failed between the
		// two, the phone would hold two rows (one crossing twice, which is noisy) rather than none
		// (the crossing silently lost, which is the failure this whole phase exists to close).
		//
		// A25 is the shape of what follows: the removal is scoped to the AUTHENTICATED viewer in the
		// SQL, its result is deliberately not branched on, and nothing about it reaches the response.
		// Naming an address that is registered to another viewer, or to nobody, therefore removes
		// nothing and is byte-identical to the succeeding case - the route cannot be used to discover
		// whether a routing address exists or to unregister somebody else's phone.
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
