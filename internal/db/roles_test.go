package db_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/NSchatz/tracker/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestLeastPrivilegeRoles proves the S7 role model (migration 00005) actually constrains what a
// credential can do — a role test, per the acceptance. It uses SET ROLE to run queries AS the group
// role: a superuser that SET ROLEs to a non-superuser role is subject to that role's privileges for
// the duration, so this exercises the real grant matrix without needing a second login/password.
//
// Everything runs on ONE pooled connection so the SET ROLE / RESET ROLE bracket applies to the same
// session the assertions run on.
func TestLeastPrivilegeRoles(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool := migrated(ctx, t)

	// Seed a readable row and a real `fixes` partition (as the owner) so the restricted roles have
	// something to read and something they must NOT be able to drop.
	if _, err := pool.Exec(ctx, `INSERT INTO families (name) VALUES ('role-test')`); err != nil {
		t.Fatalf("seed family: %v", err)
	}
	if _, err := db.EnsureMonthlyPartition(ctx, pool, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("provision a partition to guard: %v", err)
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire a connection: %v", err)
	}
	defer conn.Release()

	t.Run("tracker_readonly can read but not write or drop", func(t *testing.T) {
		asRole(ctx, t, conn, "tracker_readonly", func() {
			// SELECT is granted.
			var n int
			if err := conn.QueryRow(ctx, `SELECT count(*) FROM families`).Scan(&n); err != nil {
				t.Fatalf("readonly SELECT families: %v, want it permitted", err)
			}
			// INSERT is not.
			if _, err := conn.Exec(ctx, `INSERT INTO families (name) VALUES ('nope')`); !isPermissionDenied(err) {
				t.Fatalf("readonly INSERT families: %v, want permission denied", err)
			}
			// Neither is dropping a partition — the mechanism retention uses, which a read-only
			// credential must never be able to trigger.
			if _, err := conn.Exec(ctx, `DROP TABLE fixes_2026_07`); !isPermissionDenied(err) {
				t.Fatalf("readonly DROP partition: %v, want permission denied", err)
			}
		})
	})

	t.Run("tracker_writer can read and write rows but do no DDL", func(t *testing.T) {
		asRole(ctx, t, conn, "tracker_writer", func() {
			// DML is granted.
			if _, err := conn.Exec(ctx, `INSERT INTO families (name) VALUES ('writer-added')`); err != nil {
				t.Fatalf("writer INSERT families: %v, want it permitted", err)
			}
			var n int
			if err := conn.QueryRow(ctx, `SELECT count(*) FROM families`).Scan(&n); err != nil {
				t.Fatalf("writer SELECT families: %v, want it permitted", err)
			}
			// DDL is not: no CREATE on the schema (PUBLIC's CREATE was revoked and the writer was
			// granted only USAGE + DML), so it cannot add a table…
			if _, err := conn.Exec(ctx, `CREATE TABLE writer_should_not_create (x int)`); !isPermissionDenied(err) {
				t.Fatalf("writer CREATE TABLE: %v, want permission denied", err)
			}
			// …nor drop one (it does not own the partition).
			if _, err := conn.Exec(ctx, `DROP TABLE fixes_2026_07`); !isPermissionDenied(err) {
				t.Fatalf("writer DROP partition: %v, want permission denied", err)
			}
		})
	})
}

// asRole runs fn with the session's role set to role, restoring it afterwards even if fn fails. The
// RESET is critical: the connection returns to the pool, and leaving it stuck as a restricted role
// would break every later test that borrows it.
func asRole(ctx context.Context, t *testing.T, conn *pgxpool.Conn, role string, fn func()) {
	t.Helper()
	if _, err := conn.Exec(ctx, `SET ROLE `+role); err != nil {
		t.Fatalf("SET ROLE %s: %v", role, err)
	}
	defer func() {
		if _, err := conn.Exec(ctx, `RESET ROLE`); err != nil {
			t.Fatalf("RESET ROLE: %v", err)
		}
	}()
	fn()
}

func isPermissionDenied(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	// Postgres phrases the two DDL/DML refusals differently: "permission denied for table …" and
	// "must be owner of table …". Both are the privilege system doing its job.
	return strings.Contains(msg, "permission denied") || strings.Contains(msg, "must be owner")
}
