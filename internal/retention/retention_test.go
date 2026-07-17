package retention

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/NSchatz/tracker/internal/db"
	"github.com/NSchatz/tracker/internal/testsupport"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fixed clock helpers so the purge's cutoff is deterministic — a wall-clock now() would make "is
// January older than the window" depend on when the test runs.
var (
	oldMonth = time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC)
	current  = time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC)
	today    = time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
)

// TestRunOncePurgesExpiredKeepsRecent is the S7 acceptance test: an old partition is dropped and a
// recent one is retained, by the retention job driving DropPartitionsBefore off its window.
func TestRunOncePurgesExpiredKeepsRecent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool := migrated(ctx, t)

	// Provision January and July and put a fix in each, so the purge has real history to take and
	// real history to keep.
	for _, m := range []time.Time{oldMonth, current} {
		if _, err := db.EnsureMonthlyPartition(ctx, pool, m); err != nil {
			t.Fatalf("EnsureMonthlyPartition(%s): %v", m, err)
		}
	}
	deviceID := seedDevice(ctx, t, pool)
	for _, ts := range []time.Time{oldMonth, current} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO fixes (device_id, ts, location) VALUES ($1, $2, ST_Point(12.5, 41.9, 4326)::geography)`,
			deviceID, ts); err != nil {
			t.Fatalf("insert fix at %s: %v", ts, err)
		}
	}

	// A 90-day window on 16 July 2026 puts the cutoff in mid-April: January (ends 1 Feb) is wholly
	// past it and must go; July (ends 1 Aug) straddles the future and must stay.
	job := NewJob(pool, 90*24*time.Hour, time.Hour, nil)
	job.now = func() time.Time { return today }

	dropped, err := job.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(dropped) != 1 || dropped[0] != "fixes_2026_01" {
		t.Fatalf("RunOnce dropped %v, want [fixes_2026_01]", dropped)
	}

	if exists(ctx, t, pool, "fixes_2026_01") {
		t.Error("fixes_2026_01 still exists after the purge dropped it")
	}
	if !exists(ctx, t, pool, "fixes_2026_07") {
		t.Error("the purge took fixes_2026_07 — a partition still inside the window must be kept whole")
	}
	if n := fixCount(ctx, t, pool, deviceID); n != 1 {
		t.Fatalf("device has %d fixes after the purge, want 1 (January dropped, July kept)", n)
	}

	// Idempotent: a second pass with nothing newly expired drops nothing and does not error.
	dropped, err = job.RunOnce(ctx)
	if err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	if len(dropped) != 0 {
		t.Fatalf("second RunOnce dropped %v, want nothing", dropped)
	}
}

// TestRunLoopsAndStops proves the timer path actually purges and that cancelling the context stops
// the goroutine — the two behaviours main.go relies on.
func TestRunLoopsAndStops(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool := migrated(ctx, t)

	if _, err := db.EnsureMonthlyPartition(ctx, pool, oldMonth); err != nil {
		t.Fatalf("EnsureMonthlyPartition(old): %v", err)
	}

	job := NewJob(pool, 90*24*time.Hour, 10*time.Millisecond, nil)
	job.now = func() time.Time { return today }

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { job.Run(runCtx); close(done) }()

	// Run purges once immediately at start, so the old partition should be gone almost at once.
	deadline := time.After(5 * time.Second)
	for exists(ctx, t, pool, "fixes_2026_01") {
		select {
		case <-deadline:
			t.Fatal("Run did not purge fixes_2026_01 within 5s")
		case <-time.After(20 * time.Millisecond):
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}

// --- helpers -------------------------------------------------------------------------------

func migrated(ctx context.Context, t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := testsupport.NewPostGIS(t)
	if err := db.Up(ctx, dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func seedDevice(ctx context.Context, t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var familyID string
	if err := pool.QueryRow(ctx, `INSERT INTO families (name) VALUES ('retention-test') RETURNING id`).Scan(&familyID); err != nil {
		t.Fatalf("create family: %v", err)
	}
	hash := sha256.Sum256([]byte("retention-device"))
	var deviceID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO devices (family_id, name, token_hash) VALUES ($1, 'phone', $2) RETURNING id`,
		familyID, hash[:]).Scan(&deviceID); err != nil {
		t.Fatalf("create device: %v", err)
	}
	return deviceID
}

func exists(ctx context.Context, t *testing.T, pool *pgxpool.Pool, relation string) bool {
	t.Helper()
	var present bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, relation).Scan(&present); err != nil {
		t.Fatalf("check whether %s exists: %v", relation, err)
	}
	return present
}

func fixCount(ctx context.Context, t *testing.T, pool *pgxpool.Pool, deviceID string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM fixes WHERE device_id = $1`, deviceID).Scan(&n); err != nil {
		t.Fatalf("count fixes: %v", err)
	}
	return n
}
