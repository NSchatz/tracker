-- +goose Up
-- The one thing tracker cannot work without. Every spatial column, index and query in
-- the phases that follow (roadmap §5.1) is provided by this extension, so it is the
-- first migration and nothing may precede it.
--
-- IF NOT EXISTS because the postgis/postgis image already installs it into the default
-- database from its initdb scripts — but a database created any other way (a test
-- container's fresh DB, a managed Postgres with the extension merely *available*) will
-- not have it. Doing it here means the schema is self-sufficient and does not depend on
-- how the database was provisioned.
CREATE EXTENSION IF NOT EXISTS postgis;

-- +goose Down
-- Deliberately NOT `DROP EXTENSION postgis`. Down-migrating to zero should undo what the
-- schema added, and dropping the extension would take every geography column in the
-- database with it — including any a later migration created. The extension is
-- infrastructure, not schema; removing it is an operator's decision, not a rollback's.
SELECT 1;
