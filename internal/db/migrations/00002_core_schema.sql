-- +goose Up
-- The core data model (roadmap §5.1 — "the correctness bedrock"). Nothing ingests or serves
-- these tables yet; S2 brings the network surface. What lands here is the shape the rest of
-- the product is built on, and the spatial decisions below are the ones that are expensive
-- to reverse later.

-- A family is the authorization boundary. Every device, viewer and geofence hangs off one,
-- and every read in S3 will be scoped by it — so it is the first table, not an afterthought
-- bolted on when multi-family support is needed.
CREATE TABLE families (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text        NOT NULL CHECK (length(trim(name)) > 0),
    created_at timestamptz NOT NULL DEFAULT now()
);

-- A phone that reports fixes.
--
-- token_hash stores a SHA-256 of the bearer token, NEVER the token. A tracker database that
-- is read (backup, dump, compromised replica) must not hand the reader the ability to
-- impersonate a family's phone and write fixes into their history. The CHECK pins the digest
-- length so a future caller cannot quietly store something shorter — a truncated hash, or,
-- catastrophically, the raw token.
CREATE TABLE devices (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    family_id  uuid        NOT NULL REFERENCES families(id) ON DELETE CASCADE,
    name       text        NOT NULL CHECK (length(trim(name)) > 0),
    token_hash bytea       NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX devices_family_idx ON devices (family_id);

-- A human who watches the map. Same credential shape as a device and deliberately a separate
-- table: §7 requires that a device token can only WRITE its own fixes while a viewer token
-- can only READ its family, and one table with a "kind" column is how that distinction gets
-- eroded into a single privilege by accident. Token issuance, rotation and expiry are S2/S7;
-- this is only where the credential lives.
CREATE TABLE viewers (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    family_id    uuid        NOT NULL REFERENCES families(id) ON DELETE CASCADE,
    email        text        NOT NULL CHECK (length(trim(email)) > 0),
    display_name text        NOT NULL CHECK (length(trim(display_name)) > 0),
    token_hash   bytea       NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX viewers_family_idx ON viewers (family_id);
-- Case-insensitive uniqueness without pulling in citext: two accounts differing only in the
-- case of their email are the same person, and letting both exist is an authz hazard.
CREATE UNIQUE INDEX viewers_email_lower_key ON viewers (lower(email));

-- The location history. This is the table the whole product exists to fill, and every choice
-- below is load-bearing.
--
-- `location` is geography(Point,4326), NOT geometry. On lat/lon, geometry measures in DEGREES
-- and returns confident nonsense: LA→Paris is 9,124,665 m as geography and "121.90" as
-- geometry — a number that still looks like an answer. The typmod also refuses any other SRID
-- outright, so a projected point (3857, say) cannot land here.
--
-- Partitioned monthly by `ts` because retention is a DROP, not a DELETE (§7, S7): expiring a
-- month of a family's location history must be an instant, transactional, all-or-nothing
-- operation, not a bulk delete that half-finishes and leaves the trail corrupted.
--
-- There is deliberately NO DEFAULT partition. A default partition would silently swallow
-- fixes for months nobody provisioned, and those rows then cannot be expired by dropping a
-- partition — retention would quietly leak. Without one, an unprovisioned month is a loud
-- error at INSERT ("no partition of relation ... found"), which is the failure we want:
-- see EnsurePartitions in internal/db/partitions.go, which the server calls on start-up.
CREATE TABLE fixes (
    device_id      uuid                  NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    ts             timestamptz           NOT NULL,
    location       geography(Point,4326) NOT NULL,
    accuracy_m     double precision,
    battery_pct    smallint,
    speed_mps      double precision,
    trigger_reason text,
    msg_id         text,
    -- The server's own clock, kept separate from `ts` (the device's). A phone that was
    -- offline for six hours reports six-hour-old fixes: `ts` orders the trail, received_at
    -- tells you how stale the trail is. Collapsing them into one column loses one of those
    -- two facts, and which one you lost is not recoverable.
    received_at    timestamptz           NOT NULL DEFAULT now(),

    -- (device_id, ts) is both the identity of a fix and the dedup key §5.2 specifies: a
    -- replayed report is the same fix, not a second one. The partition key `ts` must be part
    -- of any unique constraint on a partitioned table, and here that is not a compromise —
    -- it is exactly the key we wanted.
    PRIMARY KEY (device_id, ts),

    CONSTRAINT fixes_accuracy_m_nonneg  CHECK (accuracy_m  IS NULL OR accuracy_m >= 0),
    CONSTRAINT fixes_speed_mps_nonneg   CHECK (speed_mps   IS NULL OR speed_mps  >= 0),
    CONSTRAINT fixes_battery_pct_range  CHECK (battery_pct IS NULL OR battery_pct BETWEEN 0 AND 100)
) PARTITION BY RANGE (ts);

-- Indexes are created on the PARENT, which is what makes them per-partition: Postgres clones
-- every parent index onto each partition as it is created, existing and future. That is the
-- only sane way to satisfy §5.1's "GiST + btree per partition" — CREATE INDEX CONCURRENTLY is
-- unsupported on a partitioned parent, and hand-rolling indexes per partition means the day
-- somebody forgets is the day a month of history has no spatial index.
--
-- GiST is what makes proximity work: ST_DWithin folds in an indexable bounding-box test.
-- ST_Distance does NOT — it is never index-accelerated, so the query template is always
-- "pre-filter with ST_DWithin, then rank by ST_Distance" (§5.1).
CREATE INDEX fixes_location_gist ON fixes USING GIST (location);

-- The btree §5.1 asks for is the PRIMARY KEY's, and there is deliberately no second index on
-- (device_id, ts DESC). A btree is symmetric: Postgres scans the PK's (device_id, ts) index
-- BACKWARD to satisfy ORDER BY ts DESC at identical cost, so a dedicated descending index
-- would be a duplicate — paid for on every single INSERT, on the highest-write table in the
-- system, forever. The EXPLAIN test in partitions_test.go asserts the backward scan actually
-- happens rather than trusting this comment.

-- Server-side "Places" (§5.3). geography(Polygon,4326) for the same reason as fixes, and GiST
-- for the same reason: ST_Covers folds in the indexable bounding-box test.
--
-- Containment is decided with ST_Covers, which is INCLUSIVE of the boundary. ST_Contains is
-- FALSE for a point exactly on the edge, and it does not even exist for geography — a fix
-- landing precisely on the edge of "school" must count as at school, so the inclusive
-- operator is the documented rule (§5.3). Be aware of what "the edge" means here: on
-- geography, polygon edges are GEODESICS, not straight lines in lat/lon. See
-- TestGeofenceBoundaryContainment.
CREATE TABLE geofences (
    id         uuid                    PRIMARY KEY DEFAULT gen_random_uuid(),
    family_id  uuid                    NOT NULL REFERENCES families(id) ON DELETE CASCADE,
    name       text                    NOT NULL CHECK (length(trim(name)) > 0),
    area       geography(Polygon,4326) NOT NULL,
    created_at timestamptz             NOT NULL DEFAULT now()
);
CREATE INDEX geofences_family_idx ON geofences (family_id);
CREATE INDEX geofences_area_gist  ON geofences USING GIST (area);

-- +goose Down
-- These DROPs are real, and the test asserts the tables are actually gone afterwards.
--
-- A rollback that only rewinds goose's bookkeeping is worse than no rollback: it reports
-- success while leaving the schema exactly where it was. S0's Down could only drop an
-- extension; this one has tables to lose, so "the version went back to 1" is NOT the
-- assertion that matters — "the tables are gone" is.
--
-- Order is reverse-dependency: fixes references devices, and devices/viewers/geofences
-- reference families. Dropping `fixes` takes every monthly partition with it, which is the
-- same mechanism retention uses.
DROP TABLE IF EXISTS geofences;
DROP TABLE IF EXISTS fixes;
DROP TABLE IF EXISTS viewers;
DROP TABLE IF EXISTS devices;
DROP TABLE IF EXISTS families;
