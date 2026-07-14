package db_test

import (
	"context"
	"testing"

	"github.com/NSchatz/tracker/internal/db"
	"github.com/NSchatz/tracker/internal/testsupport"
)

// TestMigrationsUpDown drives the full migration cycle against a real PostGIS.
//
// # Why the ORDER of these assertions matters
//
// The obvious version of this test — migrate up, then assert PostGIS is enabled — IS
// VACUOUS, and it took a review to catch it. The postgis/postgis image pre-creates the
// extension in POSTGRES_DB from its own initdb scripts, before goose ever runs. So
// "postgis exists after Up" is satisfied by the IMAGE, not by the migration: gut the
// migration's SQL entirely and that assertion still passes, leaving a green gate over a
// migration that does nothing. That is the "green while proving nothing" failure this
// project refuses, reached without a single t.Skip.
//
// The fix is the down-then-up cycle below. Rolling back DROPs the extension, which erases
// the image's pre-provisioning — so the assertion after the SECOND Up can only be
// satisfied by the migration's own CREATE EXTENSION actually executing. That single
// ordering is what makes this test bite, and it is why the Down is a real DROP rather than
// a no-op.
func TestMigrationsUpDown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dsn := testsupport.NewPostGIS(t)

	// A fresh database is at version 0.
	if v := version(ctx, t, dsn); v != 0 {
		t.Fatalf("fresh database is at version %d, want 0", v)
	}

	if err := db.Up(ctx, dsn); err != nil {
		t.Fatalf("Up: %v", err)
	}
	upVersion := version(ctx, t, dsn)
	if upVersion == 0 {
		t.Fatal("Up left the database at version 0 — no migration was applied")
	}

	// Up again must be a no-op, not an error: every start-up migrates, so the second boot
	// of an already-migrated database is the common case, not an edge one.
	if err := db.Up(ctx, dsn); err != nil {
		t.Fatalf("Up is not idempotent — a second run failed: %v", err)
	}
	if v := version(ctx, t, dsn); v != upVersion {
		t.Fatalf("the second Up moved the version from %d to %d", upVersion, v)
	}

	// Roll all the way back. This is the step that makes the rest of the test mean
	// something: it must actually DROP the extension, not merely update goose's
	// bookkeeping.
	if err := db.DownTo(ctx, dsn, 0); err != nil {
		t.Fatalf("DownTo(0): %v", err)
	}
	if v := version(ctx, t, dsn); v != 0 {
		t.Fatalf("after DownTo(0) the database is at version %d, want 0", v)
	}
	if postgisEnabled(ctx, t, dsn) {
		t.Fatal("DownTo(0) left PostGIS enabled — the Down migration's SQL did not run, " +
			"so nothing in this test proves the Up's SQL runs either")
	}

	// And back up. PostGIS is gone, so this assertion CANNOT be satisfied by the image's
	// initdb any more — only by migration 00001's own CREATE EXTENSION. This is the one
	// assertion in the file that proves the migration runner actually applies SQL.
	if err := db.Up(ctx, dsn); err != nil {
		t.Fatalf("Up after a full rollback: %v", err)
	}
	if !postgisEnabled(ctx, t, dsn) {
		t.Fatal("PostGIS is not enabled after re-applying the migration — the migration " +
			"runner is recording versions without executing their SQL")
	}
}

// TestOpenRejectsAnUnreachableDatabase proves Open pings rather than handing back a lazy
// pool. Without the ping, a wrong DSN would first surface at an application request —
// long after start-up had reported success.
func TestOpenRejectsAnUnreachableDatabase(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Port 1 on loopback: syntactically valid, reliably nothing listening. Synthetic.
	pool, err := db.Open(ctx, "postgres://tracker:tracker@127.0.0.1:1/tracker_test?sslmode=disable&connect_timeout=2")
	if err == nil {
		pool.Close()
		t.Fatal("Open returned a pool for an unreachable database; it must ping and fail")
	}
}

func version(ctx context.Context, t *testing.T, dsn string) int64 {
	t.Helper()

	v, err := db.Version(ctx, dsn)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	return v
}

// postgisEnabled reports whether the extension is present. It deliberately returns a bool
// rather than asserting: this test needs to prove PostGIS is ABSENT at one point and
// PRESENT at another, and only asserting presence is what made the old version vacuous.
func postgisEnabled(ctx context.Context, t *testing.T, dsn string) bool {
	t.Helper()

	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer pool.Close()

	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM pg_extension WHERE extname = 'postgis'").Scan(&n); err != nil {
		t.Fatalf("query pg_extension: %v", err)
	}
	return n > 0
}
