-- +goose Up
-- S6 — push alerts. A geofence crossing (the S5 event log) becomes a notification on a family's
-- watchers' phones. This table is the REGISTRY of where to send them: one row per (viewer, push
-- endpoint), so on a `geofence_event` the sender can fan out to every phone the family has enrolled
-- for alerts.
--
-- Who owns a subscription: a VIEWER, not a device. A device REPORTS location; a viewer WATCHES it,
-- and it is the watcher who wants "Alice's phone left School" on their own phone. So the credential
-- that registers a push endpoint is the viewer token (roadmap §7 read/write split — a viewer's
-- surface), and the fan-out for a family is "every push endpoint of every viewer in it".
--
-- Two provider shapes share this one table (roadmap §1 — FCM by default, UnifiedPush/ntfy for a
-- degoogled deployment). What `token` MEANS depends on `provider`:
--
--   * fcm         — an FCM registration token (opaque device id the app obtained from Firebase).
--   * unifiedpush — the distributor-issued endpoint URL the sender POSTs to directly.
--
-- The value is not location data and not a long-term account secret — it is a routing address that
-- the phone can rotate at will — so it is stored in the clear, unlike a token_hash. Losing it leaks
-- "this endpoint can be pushed to", not a family's whereabouts.

-- fcm | unifiedpush — the two delivery backends S6 ships. An enum, not free text, so a typo'd
-- 'ntfy' cannot land and the sender can switch on a closed set exactly as it does for
-- geofence_transition.
-- +goose StatementBegin
CREATE TYPE push_provider AS ENUM ('fcm', 'unifiedpush');
-- +goose StatementEnd

CREATE TABLE push_subscriptions (
    id         uuid          PRIMARY KEY DEFAULT gen_random_uuid(),
    viewer_id  uuid          NOT NULL REFERENCES viewers(id) ON DELETE CASCADE,
    provider   push_provider NOT NULL,

    -- The routing address (see the header): an FCM registration token, or a UnifiedPush endpoint
    -- URL. Non-empty — an empty routing address is not a subscription, it is a silent black hole.
    token      text          NOT NULL CHECK (length(trim(token)) > 0),

    created_at timestamptz   NOT NULL DEFAULT now(),

    -- A push endpoint identifies one app install, so (provider, token) is its identity: the same
    -- phone re-registering (a fresh app launch, a rotated FCM token re-POSTed) must UPDATE the owning
    -- viewer, never accumulate duplicate rows that would push the same phone N times per crossing.
    -- The registration upsert (store.RegisterPushSubscription) keys on this.
    UNIQUE (provider, token)
);

-- The fan-out reads "every subscription for viewers in this family" (store.PushSubscriptionsForFamily)
-- by joining viewers on viewer_id. Postgres does not index a foreign key automatically, and the FK is
-- ON DELETE CASCADE — an un-indexed viewer_id turns both the fan-out and "delete this viewer" into a
-- sequential scan of the whole registry.
CREATE INDEX push_subscriptions_viewer_idx ON push_subscriptions (viewer_id);

-- +goose Down
-- The table first, then the enum it depends on — the enum can only drop once no column references it,
-- exactly as 00003 unwinds geofence_transition. "The objects are gone" is the assertion that matters.
DROP TABLE IF EXISTS push_subscriptions;
DROP TYPE IF EXISTS push_provider;
