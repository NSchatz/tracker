package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier is the subset of pgx a caller needs to run these helpers. Both *pgxpool.Pool and
// pgx.Tx satisfy it, so partition maintenance can join a transaction or stand alone.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// FixesTable is the partitioned parent every helper here operates on.
const FixesTable = "fixes"

// ErrUnrecognizedPartition reports a child of `fixes` whose range bound this package cannot
// read.
//
// It is an error rather than a shrug on purpose. DropPartitionsBefore is how a family's
// location history is actually deleted when its retention window expires (§7), and the one
// outcome worse than dropping too much is silently dropping too little: a partition we
// skipped because we could not parse it is history that outlives its retention, forever,
// with nothing reporting that it did. So an unreadable bound stops the purge loudly instead
// of letting it report success over a partial job.
var ErrUnrecognizedPartition = errors.New("unrecognized fixes partition")

// EnsureMonthlyPartition creates the monthly partition of `fixes` containing month, if it is
// not already there, and returns its name.
//
// The partition inherits the parent's GiST and primary-key indexes automatically — Postgres
// clones every index defined on a partitioned parent onto each new partition — so there is no
// way to end up with a month of history that has no spatial index.
//
// It is idempotent (CREATE TABLE IF NOT EXISTS), because the server calls it on every start.
// It is NOT safe against two processes creating the same partition at the same instant: IF
// NOT EXISTS races, and the loser gets a duplicate-relation error rather than silently
// succeeding. That is the same single-migrator assumption Up already documents, and it breaks
// at the same moment — when tracker is scaled to multiple replicas.
func EnsureMonthlyPartition(ctx context.Context, q Querier, month time.Time) (string, error) {
	start := monthStart(month)
	end := start.AddDate(0, 1, 0)
	name := partitionName(start)

	// DDL cannot take bind parameters, so the bounds are formatted in. Nothing here is user
	// input: the name and both bounds are derived from a time.Time, and the identifier is
	// sanitized regardless.
	stmt := fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM (%s) TO (%s)`,
		pgx.Identifier{name}.Sanitize(),
		pgx.Identifier{FixesTable}.Sanitize(),
		quoteTimestamptz(start),
		quoteTimestamptz(end),
	)
	if _, err := q.Exec(ctx, stmt); err != nil {
		return "", fmt.Errorf("create partition %s: %w", name, err)
	}

	// Verify the bounds we ended up with are the bounds we asked for.
	//
	// CREATE TABLE IF NOT EXISTS is SILENT when a relation of that name already exists — and it
	// does not care whether that relation covers the range we wanted, or is even a partition at
	// all. Without this check, a pre-existing `fixes_2026_07` covering some other range would be
	// reported as "provisioned", the server would log "fix partitions ready" and boot, and the
	// truth would surface only when an INSERT for July was rejected at ingestion time. This
	// function exists to catch that at start-up, so it has to actually look.
	var lower, upper *time.Time
	err := q.QueryRow(ctx, `
		SELECT (regexp_match(pg_get_expr(c.relpartbound, c.oid), 'FROM \(''([^'']+)''\) TO \(''([^'']+)''\)'))[1]::timestamptz,
		       (regexp_match(pg_get_expr(c.relpartbound, c.oid), 'FROM \(''([^'']+)''\) TO \(''([^'']+)''\)'))[2]::timestamptz
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname = $1 AND n.nspname = current_schema()`, name).Scan(&lower, &upper)
	if err != nil {
		return "", fmt.Errorf("read the bounds of partition %s: %w", name, err)
	}
	if lower == nil || upper == nil || !lower.Equal(start) || !upper.Equal(end) {
		return "", fmt.Errorf("%w: %s already exists but does not cover [%s, %s) — it covers [%v, %v), "+
			"so the month it is named for is NOT provisioned",
			ErrUnrecognizedPartition, name, start.Format(time.RFC3339), end.Format(time.RFC3339), lower, upper)
	}
	return name, nil
}

// EnsurePartitions provisions the partition for the month containing from, plus the next
// months-1 months, and returns their names.
//
// The server calls this at start-up with a small lookahead. The lookahead is the point: with
// no default partition (see 00002_core_schema.sql), an INSERT into an unprovisioned month is
// a hard error, so a server that only ever created the CURRENT month would break ingestion at
// midnight on the 1st. Provisioning ahead means the rollover is a no-op.
//
// It does not, on its own, save a process that runs for longer than the lookahead. That is a
// known limitation, recorded in the README: a maintenance ticker belongs with the retention
// job in S7, and inventing one here would be building S7 early.
func EnsurePartitions(ctx context.Context, q Querier, from time.Time, months int) ([]string, error) {
	if months < 1 {
		return nil, fmt.Errorf("months must be at least 1, got %d", months)
	}

	start := monthStart(from)
	names := make([]string, 0, months)
	for i := range months {
		name, err := EnsureMonthlyPartition(ctx, q, start.AddDate(0, i, 0))
		if err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, nil
}

// DropPartitionsBefore drops every monthly partition of `fixes` that ends at or before cutoff,
// and returns the names it dropped. This is how history is purged: a DROP, not a DELETE.
//
// A partition is dropped only when its ENTIRE range is older than cutoff (upper bound <=
// cutoff), so a partition straddling the cutoff is retained whole. Purging is therefore
// coarse — with monthly partitions, up to a month of history survives its retention date —
// and that is the deliberate trade: an all-or-nothing DROP can never half-delete a family's
// trail, while a row-precise DELETE can.
//
// The bounds come from the catalog (the database's own truth), not from the partition's name,
// so a partition whose name looks right but whose range is not what we would have created
// cannot be dropped on the strength of its name alone.
func DropPartitionsBefore(ctx context.Context, q Querier, cutoff time.Time) ([]string, error) {
	type child struct {
		name  string
		upper *time.Time
	}

	rows, err := q.Query(ctx, `
		SELECT c.relname,
		       (regexp_match(pg_get_expr(c.relpartbound, c.oid), 'TO \(''([^'']+)''\)'))[1]::timestamptz
		FROM pg_class parent
		JOIN pg_inherits i ON i.inhparent = parent.oid
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_namespace n ON n.oid = parent.relnamespace
		WHERE parent.relname = $1 AND n.nspname = current_schema()
		ORDER BY c.relname`, FixesTable)
	if err != nil {
		return nil, fmt.Errorf("list partitions of %s: %w", FixesTable, err)
	}

	// The whole list is read (and the rows closed) BEFORE any DROP runs: the drops below execute
	// on the same connection this cursor is using, and issuing DDL while it is still open is how
	// you deadlock a purge against itself.
	var children []child
	func() {
		defer rows.Close()
		for rows.Next() {
			var c child
			if err = rows.Scan(&c.name, &c.upper); err != nil {
				err = fmt.Errorf("scan partition of %s: %w", FixesTable, err)
				return
			}
			children = append(children, c)
		}
		if e := rows.Err(); e != nil {
			err = fmt.Errorf("list partitions of %s: %w", FixesTable, e)
		}
	}()
	if err != nil {
		return nil, err
	}

	var dropped []string
	for _, c := range children {
		if c.upper == nil {
			// A DEFAULT partition, or a bound in a shape this code does not understand. We
			// refuse rather than guess — see ErrUnrecognizedPartition.
			return dropped, fmt.Errorf("%w: %s has no readable upper bound, so it cannot be purged", ErrUnrecognizedPartition, c.name)
		}
		if c.upper.After(cutoff) {
			continue
		}
		if _, err := q.Exec(ctx, `DROP TABLE `+pgx.Identifier{c.name}.Sanitize()); err != nil {
			return dropped, fmt.Errorf("drop partition %s: %w", c.name, err)
		}
		dropped = append(dropped, c.name)
	}
	return dropped, nil
}

// monthStart truncates to the first instant of t's month, in UTC.
//
// UTC is not a detail. Partition bounds are timestamptz and `fixes.ts` is timestamptz, so if
// the boundary were computed in a local zone the partitions of a server in, say, Europe/Berlin
// would be offset by an hour or two from the months they are named after — and the retention
// drop would take an hour of the "wrong" month with it.
func monthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// partitionName is the naming convention: fixes_2026_07.
func partitionName(monthStart time.Time) string {
	return fmt.Sprintf("%s_%04d_%02d", FixesTable, monthStart.Year(), int(monthStart.Month()))
}

// quoteTimestamptz renders t as an unambiguous, explicitly-typed SQL literal.
//
// The explicit ::timestamptz and the RFC3339 offset together mean the value cannot be
// reinterpreted in the session's TimeZone — a bare '2026-07-01 00:00:00' literal would be,
// and the partition boundary would silently move with the server's locale.
func quoteTimestamptz(t time.Time) string {
	return "'" + t.UTC().Format(time.RFC3339) + "'::timestamptz"
}
