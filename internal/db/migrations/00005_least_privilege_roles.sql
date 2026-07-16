-- +goose Up
-- S7 — least-privilege database roles (roadmap §7 "minimal DB roles"; risk path #1).
--
-- A tracker database holds a family's continuous location history — the most sensitive data in
-- the product. §7 asks for "minimal DB roles" so that a credential that is read, leaked, or
-- misused cannot do more than its job. This migration establishes two GROUP roles the schema
-- grants privileges to, and an operator creates concrete LOGIN roles as members of them:
--
--   * tracker_readonly — SELECT only. The shape of a reporting connection, a read replica
--     consumer, or an analyst who must never be able to change or delete a family's trail.
--   * tracker_writer   — SELECT + INSERT/UPDATE/DELETE, but NO DDL. It can read and write rows;
--     it cannot CREATE or DROP a table, so a compromised writer cannot drop a partition (which
--     is how history is purged) or reshape the schema.
--
-- Why GROUP (NOLOGIN) roles and not login roles here: a role is CLUSTER-global, but a password is
-- a deployment secret and must never live in a migration (§7 "no secrets in repo"). So the schema
-- ships the privilege TEMPLATES and the operator does:
--
--     CREATE ROLE tracker_app LOGIN PASSWORD '…' IN ROLE tracker_writer;   -- the server's own role
--     CREATE ROLE reports     LOGIN PASSWORD '…' IN ROLE tracker_readonly; -- a read-only consumer
--
-- and points each connection's DSN at the matching login role. THREAT-MODEL.md documents this.
--
-- A caveat this migration does NOT hide: the server's OWN connection needs more than tracker_writer.
-- It provisions monthly partitions on start-up and on ingest, and the retention job DROPs them
-- (internal/retention) — both DDL. So the server connects as the schema OWNER (or a role that owns
-- `fixes`), not as tracker_writer. tracker_writer is the least-privilege template for every OTHER
-- consumer, and the point of it is exactly that those consumers can never touch the schema.

-- Harden the cluster default. Out of the box PUBLIC (i.e. every role) may CREATE objects in the
-- `public` schema; revoke it so a new login role gets nothing it was not explicitly granted.
REVOKE CREATE ON SCHEMA public FROM PUBLIC;

-- CREATE ROLE is not idempotent and a role is cluster-global, so a second deployment against the
-- same cluster — or a database re-created in a cluster that already ran this — would error on a
-- bare CREATE. Guard on the catalog so re-running is a no-op, the same stance every other object
-- here takes with IF NOT EXISTS.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'tracker_readonly') THEN
        CREATE ROLE tracker_readonly NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'tracker_writer') THEN
        CREATE ROLE tracker_writer NOLOGIN;
    END IF;
END
$$;
-- +goose StatementEnd

GRANT USAGE ON SCHEMA public TO tracker_readonly, tracker_writer;

-- Existing tables. On a fresh migration this is the core schema; `fixes` partitions do not exist
-- yet (they are provisioned at runtime), so the ALTER DEFAULT PRIVILEGES below is what carries the
-- grant onto every partition the server creates later.
GRANT SELECT                         ON ALL TABLES IN SCHEMA public TO tracker_readonly;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO tracker_writer;

-- Future tables — critically, the monthly `fixes` partitions the server creates on the fly. These
-- defaults apply to tables created by the role that RUNS this migration, which is the same role the
-- server connects as, so every new partition inherits the same SELECT/DML grants without anyone
-- remembering to re-grant. Without this a reporting role would silently lose access to each new
-- month, and a writer would be unable to insert into it.
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT                         ON TABLES TO tracker_readonly;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO tracker_writer;

-- +goose Down
-- Unwind in reverse: drop the default-privilege entries first (a role cannot be dropped while it is
-- named in one), then the table grants, then the roles. IF EXISTS / guarded so an N-1 rollback is
-- clean. Restoring PUBLIC's CREATE returns the schema to its pre-migration default.
ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE SELECT                         ON TABLES FROM tracker_readonly;
ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE SELECT, INSERT, UPDATE, DELETE ON TABLES FROM tracker_writer;

REVOKE SELECT                         ON ALL TABLES IN SCHEMA public FROM tracker_readonly;
REVOKE SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public FROM tracker_writer;
REVOKE USAGE ON SCHEMA public FROM tracker_readonly, tracker_writer;

DROP ROLE IF EXISTS tracker_readonly;
DROP ROLE IF EXISTS tracker_writer;

GRANT CREATE ON SCHEMA public TO PUBLIC;
