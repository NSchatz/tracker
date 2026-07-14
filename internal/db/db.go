// Package db owns tracker's PostgreSQL connection pool and its schema migrations.
//
// The schema is applied by goose from SQL files embedded into the binary (see
// migrations/), so a deployed tracker carries the exact schema it was built against and
// cannot be run against a database migrated by some other copy of the files.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver, which goose needs
	"github.com/pressly/goose/v3"
)

// migrationsFS carries the schema into the binary. Embedding is what makes the binary
// self-sufficient: no migrations directory has to ship alongside it.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrationsDir is the path within migrationsFS. goose is given the embedded FS as its
// base, so this is relative to the module, not the filesystem.
const migrationsDir = "migrations"

// Open returns a connection pool and verifies it can actually reach the database.
//
// It pings before returning: a pgxpool is lazy, so without this the first evidence of a
// wrong DSN or an unreachable database would be a failing request — long after start-up
// had already reported success.
func Open(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// Up applies every pending migration, in order, and blocks until they are done.
//
// It runs on the way up, before the server listens. A tracker serving requests against a
// half-migrated schema is the failure this prevents.
//
// NOT SAFE against concurrent migrators. goose only locks when a Provider is built with
// WithSessionLocker, and this uses the legacy package-level API, which takes no lock at
// all. Today that is fine — the compose stack runs one replica. It stops being fine the
// moment tracker is scaled to the stateless replicas roadmap §1 plans for, because every
// instance migrates on boot: two starting together race on CREATE EXTENSION and on the
// goose_db_version insert. Whoever does that scaling owns fixing this, and this comment
// is here so they find out from the code rather than from production.
func Up(ctx context.Context, dsn string) error {
	sqlDB, err := openForMigration(dsn)
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	if err := goose.UpContext(ctx, sqlDB, migrationsDir); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

// DownTo rolls migrations back to the given version (0 = undo everything).
//
// It exists because S0's acceptance requires migrations to go up *and down* cleanly: a
// Down that has never been run is a Down that is a guess.
func DownTo(ctx context.Context, dsn string, version int64) error {
	sqlDB, err := openForMigration(dsn)
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	if err := goose.DownToContext(ctx, sqlDB, migrationsDir, version); err != nil {
		return fmt.Errorf("roll back migrations to %d: %w", version, err)
	}
	return nil
}

// Version reports the currently applied schema version.
func Version(ctx context.Context, dsn string) (int64, error) {
	sqlDB, err := openForMigration(dsn)
	if err != nil {
		return 0, err
	}
	defer sqlDB.Close()

	v, err := goose.GetDBVersionContext(ctx, sqlDB)
	if err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return v, nil
}

// gooseSetup configures goose's PACKAGE-LEVEL globals, exactly once.
//
// SetBaseFS and SetDialect mutate process-wide state. Calling them on every migration —
// as this used to — is a data race the moment two goroutines migrate at once, which the
// parallel tests in this package are one `t.Parallel()` away from doing. The values are
// constant, so doing it once is both correct and sufficient.
var gooseSetup = sync.OnceValue(func() error {
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}
	return nil
})

// openForMigration opens the short-lived database/sql handle goose speaks through. It is
// separate from the pgxpool the server serves requests on, and closed as soon as the
// migration finishes.
func openForMigration(dsn string) (*sql.DB, error) {
	if err := gooseSetup(); err != nil {
		return nil, err
	}

	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open migration connection: %w", err)
	}
	return sqlDB, nil
}
