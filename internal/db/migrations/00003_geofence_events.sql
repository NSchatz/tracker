-- +goose Up
-- S5 — server-side geofencing ("Places") enter/exit events (roadmap §5.3, risk path #5).
--
-- The Places themselves already exist: `geofences` (geography(Polygon,4326), GiST, a validity
-- CHECK) landed in S1. What lands here is the EVENT LOG that the stream evaluator writes when a
-- device crosses one — the thing S6 will deliver as a push, and the family can already read back.
--
-- The log is APPEND-ONLY and it is a DETERMINISTIC PROJECTION of the fix history: the evaluator
-- (store.EvaluateDeviceGeofences) is its sole writer, and it derives each transition from the
-- device's fixes in `ts` order. Two consequences shape the schema below, and both are load-bearing:
--
--   * A transition is identified by the fix that caused it — (device_id, geofence_id, fix_ts). That
--     tuple is UNIQUE, which is what makes a replayed or re-evaluated fix an idempotent no-op rather
--     than a second event. "No duplicate events for a replayed fix" (§8/S5 acceptance) is enforced
--     here, in the schema, not left to the writer to remember.
--   * `fix_ts` is the DEVICE event-time of the crossing fix; `created_at` is the server's own append
--     time. They are kept separate for the same reason `fixes.ts` and `fixes.received_at` are: an
--     offline phone's buffered crossing has an old fix_ts and a fresh created_at, and which one you
--     lost by collapsing them is not recoverable. Ordering of the log is by fix_ts (§5.3: "derive
--     from ts order, not arrival").

-- enter | exit — the only two transitions a boundary crossing can be. An enum, not a free-text
-- column, so a typo'd 'entre' cannot land and every consumer can switch on a closed set.
-- +goose StatementBegin
CREATE TYPE geofence_transition AS ENUM ('enter', 'exit');
-- +goose StatementEnd

CREATE TABLE geofence_events (
    id          uuid                PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id   uuid                NOT NULL REFERENCES devices(id)   ON DELETE CASCADE,
    geofence_id uuid                NOT NULL REFERENCES geofences(id) ON DELETE CASCADE,
    transition  geofence_transition NOT NULL,

    -- The device event-time of the fix that caused the crossing (§5.3 "derive from ts order"). It
    -- is the transition's identity together with (device_id, geofence_id): a fix marks at most one
    -- transition for a given Place, so a second event for the same crossing fix is a duplicate.
    fix_ts      timestamptz         NOT NULL,

    -- The server's append clock, distinct from fix_ts. How stale the event is, not when it happened.
    created_at  timestamptz         NOT NULL DEFAULT now(),

    -- Idempotency, in the schema. A replayed fix re-evaluates to the same (device, geofence, fix_ts)
    -- and is absorbed by ON CONFLICT DO NOTHING (see store.EvaluateDeviceGeofences). At most ONE
    -- transition per crossing fix, which is also physically true: a single fix cannot both enter and
    -- exit the same Place.
    UNIQUE (device_id, geofence_id, fix_ts)
);

-- The evaluator's hot read is "the latest event for this (device, geofence)" — the prior state it
-- evaluates the next fix against. The UNIQUE index above serves it, scanned backward on fix_ts, so
-- there is deliberately no second index for that lookup.

-- Postgres does not index foreign keys automatically. Both are needed: the geofence-scoped read
-- (GET /v1/geofence-events, and a Place's own history) filters by geofence_id, and both FKs are ON
-- DELETE CASCADE — an un-indexed FK turns "delete this Place" into a sequential scan of the whole
-- event log to find the children.
CREATE INDEX geofence_events_device_idx   ON geofence_events (device_id,   fix_ts DESC);
CREATE INDEX geofence_events_geofence_idx ON geofence_events (geofence_id, fix_ts DESC);

-- +goose Down
-- The table first, then the type it depends on. Dropping the table takes its indexes with it; the
-- enum can only go once nothing references it. As with 00002's Down, "the objects are gone" is the
-- assertion that matters, not that the version counter moved.
DROP TABLE IF EXISTS geofence_events;
DROP TYPE IF EXISTS geofence_transition;
