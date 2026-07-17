-- +goose Up
-- S7 — token expiry (roadmap §7: "per-device SHORT-LIVED bearer token … rotation/expiry").
--
-- Until now a device or viewer token was valid forever: the only way to revoke one was to delete the
-- account. §7 wants tokens that can be given a lifetime and rotated, so a leaked credential stops
-- working on its own and a compromised phone can be cut off without losing its history.
--
-- The mechanism is one nullable column per credential table. NULL means "never expires" — the
-- existing behaviour, so this migration changes nothing about already-issued tokens — and a non-null
-- timestamp is the instant after which the token no longer authenticates. Enforcement lives in the
-- authentication lookups (store.AuthenticateDevice / AuthenticateViewer), which now match only a row
-- whose expiry is NULL or still in the future, using the DATABASE's clock (now()) rather than the
-- app's: expiry is a server-side decision a client cannot influence by lying about the time.
--
-- Rotation reuses this column: store.RotateDeviceToken / RotateViewerToken write a fresh token_hash
-- and a new expires_at in one statement, so the old token is dead the instant the new one is minted.

ALTER TABLE devices ADD COLUMN expires_at timestamptz;
ALTER TABLE viewers ADD COLUMN expires_at timestamptz;

-- +goose Down
-- Drop the columns. Reverting to "tokens never expire" is safe: every token that authenticated
-- before still authenticates, and no other object depends on these columns.
ALTER TABLE viewers DROP COLUMN expires_at;
ALTER TABLE devices DROP COLUMN expires_at;
