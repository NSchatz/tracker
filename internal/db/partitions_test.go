package db_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/NSchatz/tracker/internal/db"
	"github.com/NSchatz/tracker/internal/testsupport"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestPartitions covers the monthly partitioning of `fixes` end to end: provisioning, the
// per-partition indexes, the deliberate absence of a default partition, and the drop-to-purge
// that retention (S7) will be built on.
//
// The subtests share one database and run in order, because they describe a lifecycle: you
// cannot purge partitions you never created.
func TestPartitions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool := migrated(ctx, t)

	// A month long past and a "current" month, so the purge has something to take and something
	// to keep. The lookahead provisions the month after `current` on its own.
	var (
		old     = time.Date(2026, 1, 15, 9, 30, 0, 0, time.UTC)
		current = time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	)

	t.Run("a fresh schema has no partitions at all", func(t *testing.T) {
		if n := partitionCount(ctx, t, pool); n != 0 {
			t.Fatalf("the migration created %d partitions; it must create none — provisioning is "+
				"EnsurePartitions' job, and a partition baked into a migration is a partition "+
				"whose month is whenever that migration happened to be written", n)
		}
	})

	t.Run("an unprovisioned month is a LOUD failure, not a silent one", func(t *testing.T) {
		// The consequence of having no DEFAULT partition, and the reason we do not have one: a
		// fix for a month nobody provisioned is rejected outright. A default partition would
		// have accepted it — and then that row could never be expired by dropping a partition,
		// so a family's location history would outlive its retention window silently.
		familyID, deviceID := seedDevice(ctx, t, pool, "unprovisioned")
		_ = familyID

		_, err := pool.Exec(ctx,
			`INSERT INTO fixes (device_id, ts, location) VALUES ($1, $2, ST_Point(12.5, 41.9, 4326)::geography)`,
			deviceID, current)
		if err == nil {
			t.Fatal("a fix landed in a month with no partition; either a DEFAULT partition exists " +
				"(it must not) or the table is not partitioned")
		}
		if !strings.Contains(err.Error(), "no partition of relation") {
			t.Fatalf("expected a missing-partition error, got: %v", err)
		}
	})

	t.Run("EnsurePartitions provisions the month and a lookahead", func(t *testing.T) {
		names, err := db.EnsurePartitions(ctx, pool, current, 2)
		if err != nil {
			t.Fatalf("EnsurePartitions: %v", err)
		}
		want := []string{"fixes_2026_07", "fixes_2026_08"}
		if len(names) != len(want) {
			t.Fatalf("EnsurePartitions created %v, want %v", names, want)
		}
		for i := range want {
			if names[i] != want[i] {
				t.Fatalf("EnsurePartitions created %v, want %v", names, want)
			}
		}

		// Idempotent: the server runs this on every start.
		if _, err := db.EnsurePartitions(ctx, pool, current, 2); err != nil {
			t.Fatalf("EnsurePartitions is not idempotent: %v", err)
		}
		if n := partitionCount(ctx, t, pool); n != 2 {
			t.Fatalf("a second EnsurePartitions left %d partitions, want 2", n)
		}
	})

	t.Run("every partition inherits the GiST and btree indexes", func(t *testing.T) {
		// §5.1 requires "GiST + btree per partition". They are declared once, on the parent,
		// and Postgres clones them onto each new partition — which is the only way to be sure
		// no month ever ends up unindexed.
		for _, p := range []string{"fixes_2026_07", "fixes_2026_08"} {
			methods := indexMethods(ctx, t, pool, p)
			if !methods["gist"] {
				t.Errorf("partition %s has no GiST index — proximity queries over that month would "+
					"scan every row in it", p)
			}
			if !methods["btree"] {
				t.Errorf("partition %s has no btree index — the (device_id, ts) key is missing", p)
			}
		}
	})

	t.Run("a fix is routed to the partition for its month", func(t *testing.T) {
		_, deviceID := seedDevice(ctx, t, pool, "routing")

		if _, err := pool.Exec(ctx,
			`INSERT INTO fixes (device_id, ts, location) VALUES ($1, $2, ST_Point(12.5, 41.9, 4326)::geography)`,
			deviceID, current); err != nil {
			t.Fatalf("insert into a provisioned month: %v", err)
		}

		var partition string
		if err := pool.QueryRow(ctx,
			`SELECT tableoid::regclass::text FROM fixes WHERE device_id = $1`, deviceID).Scan(&partition); err != nil {
			t.Fatalf("read the fix's partition: %v", err)
		}
		if partition != "fixes_2026_07" {
			t.Fatalf("a fix at %s landed in %s, want fixes_2026_07", current, partition)
		}
	})

	t.Run("DropPartitionsBefore purges whole months and keeps the rest", func(t *testing.T) {
		// Provision an old month and put a fix in it, so the purge has real history to destroy.
		if _, err := db.EnsureMonthlyPartition(ctx, pool, old); err != nil {
			t.Fatalf("EnsureMonthlyPartition(old): %v", err)
		}
		_, deviceID := seedDevice(ctx, t, pool, "purge")
		for _, ts := range []time.Time{old, current} {
			if _, err := pool.Exec(ctx,
				`INSERT INTO fixes (device_id, ts, location) VALUES ($1, $2, ST_Point(12.5, 41.9, 4326)::geography)`,
				deviceID, ts); err != nil {
				t.Fatalf("insert fix at %s: %v", ts, err)
			}
		}
		if n := fixCount(ctx, t, pool, deviceID); n != 2 {
			t.Fatalf("seeded %d fixes, want 2", n)
		}

		// Purge everything that ended before July. January goes; July stays.
		cutoff := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
		dropped, err := db.DropPartitionsBefore(ctx, pool, cutoff)
		if err != nil {
			t.Fatalf("DropPartitionsBefore: %v", err)
		}
		if len(dropped) != 1 || dropped[0] != "fixes_2026_01" {
			t.Fatalf("DropPartitionsBefore dropped %v, want [fixes_2026_01]", dropped)
		}

		// The January fix is GONE — this is the purge, and it is a DROP, not a DELETE.
		if n := fixCount(ctx, t, pool, deviceID); n != 1 {
			t.Fatalf("after the purge the device has %d fixes, want 1 (January dropped, July kept)", n)
		}
		if exists(ctx, t, pool, "fixes_2026_01") {
			t.Fatal("fixes_2026_01 still exists after being dropped")
		}
		if !exists(ctx, t, pool, "fixes_2026_07") {
			t.Fatal("the purge took fixes_2026_07 with it; a partition whose range straddles or " +
				"follows the cutoff must be retained whole")
		}

		// A partition is only dropped when its ENTIRE range is older than the cutoff. July ends
		// on 1 August, so a cutoff mid-July must not take it.
		midJuly := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
		dropped, err = db.DropPartitionsBefore(ctx, pool, midJuly)
		if err != nil {
			t.Fatalf("DropPartitionsBefore(midJuly): %v", err)
		}
		if len(dropped) != 0 {
			t.Fatalf("a cutoff inside July dropped %v; a partition that still holds retained history "+
				"must survive whole", dropped)
		}
	})

	t.Run("a same-named partition with the WRONG bounds is not reported as provisioned", func(t *testing.T) {
		// CREATE TABLE IF NOT EXISTS is silent when the name is taken, and does not care what
		// range the existing relation covers. Left unchecked, that turns "provisioned" into a
		// claim about a NAME rather than about a MONTH — and the lie only surfaces later, when
		// an INSERT for that month is rejected at ingestion time.
		//
		// Hand-make a partition called fixes_2026_09 that actually covers a single day.
		if _, err := pool.Exec(ctx, `
			CREATE TABLE fixes_2026_09 PARTITION OF fixes
			FOR VALUES FROM ('2026-09-01T00:00:00Z'::timestamptz) TO ('2026-09-02T00:00:00Z'::timestamptz)`); err != nil {
			t.Fatalf("hand-make a mis-bounded partition: %v", err)
		}

		sept := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
		_, err := db.EnsureMonthlyPartition(ctx, pool, sept)
		if !errors.Is(err, db.ErrUnrecognizedPartition) {
			t.Fatalf("EnsureMonthlyPartition over a mis-bounded partition = %v; want ErrUnrecognizedPartition. "+
				"It must not report a month as ready when the partition of that name covers a different range.", err)
		}

		if _, err := pool.Exec(ctx, `DROP TABLE fixes_2026_09`); err != nil {
			t.Fatalf("clean up the mis-bounded partition: %v", err)
		}
	})

	t.Run("a partition whose bound cannot be read stops the purge", func(t *testing.T) {
		// Runs last: it leaves a DEFAULT partition behind.
		//
		// Refusing here is the fail-safe. DropPartitionsBefore is how location history is
		// actually deleted when its retention expires, and the failure that matters is not
		// dropping too much — it is silently dropping too LITTLE and reporting success, which
		// leaves a family's history alive past the date it was promised to be gone.
		if _, err := pool.Exec(ctx, `CREATE TABLE fixes_default PARTITION OF fixes DEFAULT`); err != nil {
			t.Fatalf("create a default partition: %v", err)
		}

		_, err := db.DropPartitionsBefore(ctx, pool, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
		if !errors.Is(err, db.ErrUnrecognizedPartition) {
			t.Fatalf("DropPartitionsBefore over a DEFAULT partition returned %v; want ErrUnrecognizedPartition", err)
		}
	})
}

// TestEnsurePartitionsRejectsAnEmptyRange keeps the lookahead honest: asking for zero months
// is a caller bug, not a no-op that quietly provisions nothing.
func TestEnsurePartitionsRejectsAnEmptyRange(t *testing.T) {
	t.Parallel()

	if _, err := db.EnsurePartitions(context.Background(), nil, time.Now(), 0); err == nil {
		t.Fatal("EnsurePartitions(months=0) succeeded; it must refuse rather than provision nothing")
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

// seedDevice creates a family and a device directly in SQL. The db package's tests deliberately
// do not import internal/store: these are assertions about the SCHEMA, and routing them through
// the very helpers whose queries are under test elsewhere would couple the two.
func seedDevice(ctx context.Context, t *testing.T, pool *pgxpool.Pool, name string) (familyID, deviceID string) {
	t.Helper()

	if err := pool.QueryRow(ctx, `INSERT INTO families (name) VALUES ($1) RETURNING id`, name).Scan(&familyID); err != nil {
		t.Fatalf("create family: %v", err)
	}
	// A synthetic 32-byte digest, distinct per device. It is the SHA-256 of the test's own
	// name — not of any real token, and never a credential.
	//
	// It has to be distinct because devices.token_hash is UNIQUE, and that constraint is not
	// bureaucracy: two devices sharing a credential means either phone can write the other's
	// history. Reusing one digest across the fixtures is how this helper found that out.
	hash := sha256.Sum256([]byte(name))
	if err := pool.QueryRow(ctx,
		`INSERT INTO devices (family_id, name, token_hash) VALUES ($1, $2, $3) RETURNING id`,
		familyID, name+"-phone", hash[:]).Scan(&deviceID); err != nil {
		t.Fatalf("create device: %v", err)
	}
	return familyID, deviceID
}

func partitionCount(ctx context.Context, t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()

	var n int
	err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM pg_inherits i
		JOIN pg_class parent ON parent.oid = i.inhparent
		WHERE parent.relname = 'fixes'`).Scan(&n)
	if err != nil {
		t.Fatalf("count partitions: %v", err)
	}
	return n
}

// indexMethods reports which access methods index a partition (gist, btree, …).
func indexMethods(ctx context.Context, t *testing.T, pool *pgxpool.Pool, partition string) map[string]bool {
	t.Helper()

	rows, err := pool.Query(ctx, `
		SELECT am.amname
		FROM pg_class c
		JOIN pg_index x ON x.indrelid = c.oid
		JOIN pg_class i ON i.oid = x.indexrelid
		JOIN pg_am am ON am.oid = i.relam
		WHERE c.relname = $1`, partition)
	if err != nil {
		t.Fatalf("read indexes of %s: %v", partition, err)
	}
	defer rows.Close()

	methods := map[string]bool{}
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			t.Fatalf("scan index method: %v", err)
		}
		methods[m] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read indexes of %s: %v", partition, err)
	}
	return methods
}

func fixCount(ctx context.Context, t *testing.T, pool *pgxpool.Pool, deviceID string) int {
	t.Helper()

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM fixes WHERE device_id = $1`, deviceID).Scan(&n); err != nil {
		t.Fatalf("count fixes: %v", err)
	}
	return n
}

func exists(ctx context.Context, t *testing.T, pool *pgxpool.Pool, relation string) bool {
	t.Helper()

	var present bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, relation).Scan(&present); err != nil {
		t.Fatalf("check whether %s exists: %v", relation, err)
	}
	return present
}
