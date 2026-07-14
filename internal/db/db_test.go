package db_test

import (
	"context"
	"testing"
	"time"

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

// schemaTables are the tables migration 00002 owns. Every one of them must exist after it is
// applied and be GONE after it is rolled back.
var schemaTables = []string{"families", "devices", "viewers", "fixes", "geofences"}

// TestSchemaDownDropsItsTables proves migration 00002's rollback actually destroys the schema
// it created.
//
// # Why this test does not have the same shape as TestMigrationsUpDown
//
// S0's up/down test proves the migration RUNNER executes SQL, by rolling back and observing
// that PostGIS disappears. That was the right assertion when the only migration created an
// extension. It is the WRONG assertion to copy now, because it is satisfied by a Down that
// drops the extension and nothing else — and 00002's Down has five tables to lose.
//
// So the assertion here is not "the version went back" (goose's bookkeeping can rewind while
// the SQL does nothing) and not "the migration runner runs SQL" (already proven). It is the
// only one that bites: THE TABLES ARE GONE. Gut every DROP out of 00002's Down section and
// this test fails; leave one behind and it fails naming it.
//
// The rollback stops at version 1, not 0: unwinding to 0 would also drop PostGIS, and then
// "the tables are gone" would be true for a reason that has nothing to do with 00002's Down.
func TestSchemaDownDropsItsTables(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dsn := testsupport.NewPostGIS(t)

	if err := db.Up(ctx, dsn); err != nil {
		t.Fatalf("Up: %v", err)
	}

	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer pool.Close()

	for _, table := range schemaTables {
		if !exists(ctx, t, pool, table) {
			t.Fatalf("%s does not exist after Up — migration 00002 did not create it", table)
		}
	}

	// Give the rollback something real to destroy: a partition holding an actual fix. A Down
	// tested against an empty schema is a Down that has never met a row it had to drop, and
	// `DROP TABLE` on a partitioned parent with live children is exactly the case that would
	// fail if the DROP order were wrong.
	familyID, deviceID := seedDevice(ctx, t, pool, "rollback")
	ts := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	if _, err := db.EnsureMonthlyPartition(ctx, pool, ts); err != nil {
		t.Fatalf("EnsureMonthlyPartition: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO fixes (device_id, ts, location) VALUES ($1, $2, ST_Point(12.5, 41.9, 4326)::geography)`,
		deviceID, ts); err != nil {
		t.Fatalf("insert a fix: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO geofences (family_id, name, area) VALUES ($1, 'home',
		     ST_GeogFromText('SRID=4326;POLYGON((12.0 41.0, 13.0 41.0, 13.0 42.0, 12.0 42.0, 12.0 41.0))'))`,
		familyID); err != nil {
		t.Fatalf("insert a geofence: %v", err)
	}

	// Roll back 00002 only.
	if err := db.DownTo(ctx, dsn, 1); err != nil {
		t.Fatalf("DownTo(1): %v", err)
	}
	if v := version(ctx, t, dsn); v != 1 {
		t.Fatalf("after DownTo(1) the database is at version %d, want 1", v)
	}

	// THE assertion.
	for _, table := range schemaTables {
		if exists(ctx, t, pool, table) {
			t.Fatalf("%s still exists after rolling 00002 back. The Down migration updated goose's "+
				"version and left the schema in place — a rollback that reports success while "+
				"changing nothing.", table)
		}
	}
	// The partition went with its parent. That is the same mechanism retention uses to purge
	// history, so if it did not work here it would not work there.
	if exists(ctx, t, pool, "fixes_2026_07") {
		t.Fatal("the monthly partition fixes_2026_07 outlived its parent table")
	}

	// PostGIS is untouched: we rolled back 00002, not 00001.
	if !postgisEnabled(ctx, t, dsn) {
		t.Fatal("rolling back 00002 also removed PostGIS; it must only drop what it created")
	}

	// And the schema comes back. A Down that cannot be followed by an Up is a one-way door.
	if err := db.Up(ctx, dsn); err != nil {
		t.Fatalf("Up after rolling 00002 back: %v", err)
	}
	for _, table := range schemaTables {
		if !exists(ctx, t, pool, table) {
			t.Fatalf("%s did not come back after re-applying 00002", table)
		}
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
