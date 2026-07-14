package db_test

import (
	"context"
	"testing"

	"github.com/NSchatz/tracker/internal/db"
	"github.com/NSchatz/tracker/internal/testsupport"
)

// TestMigrationsUpDown is S0's acceptance: "migrations up/down clean".
//
// It runs against a real PostGIS (testsupport starts one; it FAILS rather than skips if
// it cannot). A migration whose Down has never been executed is a Down that is a guess,
// so this drives the full cycle rather than only the happy direction.
func TestMigrationsUpDown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dsn := testsupport.NewPostGIS(t)

	// A fresh database is at version 0.
	if v, err := db.Version(ctx, dsn); err != nil {
		t.Fatalf("Version on a fresh database: %v", err)
	} else if v != 0 {
		t.Fatalf("fresh database is at version %d, want 0", v)
	}

	// Up.
	if err := db.Up(ctx, dsn); err != nil {
		t.Fatalf("Up: %v", err)
	}
	upVersion, err := db.Version(ctx, dsn)
	if err != nil {
		t.Fatalf("Version after Up: %v", err)
	}
	if upVersion == 0 {
		t.Fatal("Up left the database at version 0 — no migration was applied")
	}

	// The migration runner is wired only if the schema it applied is actually there.
	// 00001 enables PostGIS, so that is what we assert — not the goose bookkeeping table,
	// which would pass even if the SQL inside the migration did nothing.
	assertPostGISEnabled(ctx, t, dsn)

	// Up again must be a no-op, not an error. Every start-up runs migrations, so the
	// second boot of an already-migrated database is the common case, not an edge one.
	if err := db.Up(ctx, dsn); err != nil {
		t.Fatalf("Up is not idempotent — a second run failed: %v", err)
	}
	if v, err := db.Version(ctx, dsn); err != nil {
		t.Fatalf("Version after the second Up: %v", err)
	} else if v != upVersion {
		t.Fatalf("the second Up moved the version from %d to %d", upVersion, v)
	}

	// Down to zero.
	if err := db.DownTo(ctx, dsn, 0); err != nil {
		t.Fatalf("DownTo(0): %v", err)
	}
	if v, err := db.Version(ctx, dsn); err != nil {
		t.Fatalf("Version after DownTo(0): %v", err)
	} else if v != 0 {
		t.Fatalf("after DownTo(0) the database is at version %d, want 0", v)
	}

	// And back up, to prove the down-migration left the database in a state the
	// up-migration can still run against. A Down that "succeeds" but corrupts the schema
	// would otherwise pass the check above.
	if err := db.Up(ctx, dsn); err != nil {
		t.Fatalf("Up after a full rollback: %v", err)
	}
	assertPostGISEnabled(ctx, t, dsn)
}

// TestOpenRejectsAnUnreachableDatabase proves Open pings rather than returning a lazy
// pool. Without the ping, a wrong DSN would first surface at an application request —
// after start-up had already reported success.
func TestOpenRejectsAnUnreachableDatabase(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Port 1 on localhost: syntactically valid, reliably nothing listening. Synthetic.
	pool, err := db.Open(ctx, "postgres://tracker:tracker@127.0.0.1:1/tracker_test?sslmode=disable&connect_timeout=2")
	if err == nil {
		pool.Close()
		t.Fatal("Open returned a pool for an unreachable database; it must ping and fail")
	}
}

func assertPostGISEnabled(ctx context.Context, t *testing.T, dsn string) {
	t.Helper()

	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer pool.Close()

	var version string
	if err := pool.QueryRow(ctx, "SELECT extversion FROM pg_extension WHERE extname = 'postgis'").Scan(&version); err != nil {
		t.Fatalf("PostGIS is not enabled after migrating: %v", err)
	}
	if version == "" {
		t.Fatal("PostGIS reports an empty version")
	}
	t.Logf("PostGIS %s enabled by the migration", version)
}
