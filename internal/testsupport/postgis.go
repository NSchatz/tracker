// Package testsupport provides the real PostGIS that tracker's tests run against.
//
// # Why this exists, and why it may never skip
//
// tracker's correctness lives in spatial SQL (roadmap §5.1 — "the correctness bedrock"):
// geography-vs-geometry units, lon/lat axis order, on-boundary containment, GiST index
// selection. None of that can be tested against a mock or an in-memory stand-in, because
// the thing under test IS PostGIS's behaviour. A fake would only ever assert what we
// already believed.
//
// So the tests need a real PostGIS, and this package starts one per run with
// testcontainers-go.
//
// The one rule: IF THE DATABASE WILL NOT COME UP, THE TEST FAILS. It never skips.
// A gate that quietly skips its correctness bedrock when the database is missing reports
// green while proving nothing, which is the "advisory gate" failure this project
// explicitly refuses. A missing Docker daemon is an unknown, and an unknown is not a
// pass. That is why every failure path below is t.Fatalf and there is not a single
// t.Skip in this package.
package testsupport

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// PostGISImage is the image every test runs against, pinned by TAG AND DIGEST (P1 of the
// org's pinning conventions) and identical to the `postgis` service in docker-compose.yml.
//
// The tag stays here so a human can read which PostGIS this is; the digest is what actually
// resolves, and it is the half that matters. Every spatial assertion tracker makes is an
// assertion about a specific PostGIS build's behaviour (geography units, on-boundary
// containment, planner index selection), so a floating tag would let the thing under test
// change without a single test changing, and would let the database the tests measure
// drift away from the database the stack runs. Moving it is a two-minute job with the
// provenance table in README.md; it must move in both places at once.
//
// internal/pingate reads this constant and refuses it if it ever loses either half.
const PostGISImage = "postgis/postgis:16-3.4@sha256:44126d872ac91993766c341e369c539e8196614321765d36a6f1bab0419a5fa5"

// These credentials are synthetic and local to a throwaway container that is never
// published to a port on the host. They are not, and must never become, a real secret.
const (
	testUser = "tracker"
	testPass = "tracker" //nolint:gosec // synthetic; a per-run throwaway container
	testDB   = "tracker_test"
)

// cleanDB is the database tests actually run against. It is created from `template0`,
// NOT from the container's default database.
//
// That distinction is the whole point, and getting it wrong once already produced a gate
// that proved nothing. The postgis/postgis image's initdb scripts pre-create the postgis
// extension (plus postgis_topology and postgis_tiger_geocoder) in POSTGRES_DB. A test
// running there could assert "postgis is enabled" all day and never learn whether the
// MIGRATION enabled it — the image did. Gut the migration's SQL and such a test still
// passes green.
//
// `template0` is pristine by definition: no extensions, nothing pre-installed. So in this
// database the schema has to stand on its own, and every assertion about it is an
// assertion about our migrations rather than about somebody's Dockerfile.
const cleanDB = "tracker_clean"

// NewPostGIS starts a PostGIS container and returns a DSN pointing at a pristine database
// inside it (see cleanDB).
//
// The container is terminated when the test ends. Each call gets its OWN container, so
// tests cannot leak schema or rows into one another — which is what lets them run in
// parallel and what makes a failure mean what it says.
func NewPostGIS(t testing.TB) string {
	t.Helper()

	ctx := context.Background()

	container, err := postgres.Run(ctx, PostGISImage,
		postgres.WithDatabase(testDB),
		postgres.WithUsername(testUser),
		postgres.WithPassword(testPass),
		testcontainers.WithWaitStrategy(
			// Postgres's entrypoint starts the server, shuts it down to run initdb
			// scripts, then starts it again — so "port is listening" alone can catch the
			// first, transient server. Waiting for the readiness log twice is the
			// documented way to wait for the real one.
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(2*time.Minute),
		),
	)
	if err != nil {
		// FAIL, never skip. See the package doc.
		t.Fatalf("could not start %s: %v\n\n"+
			"This is a FAILURE, not a skip: tracker's spatial correctness cannot be "+
			"proven without a real PostGIS, and a gate that skips when the database is "+
			"missing reports green while proving nothing. A Docker daemon must be "+
			"reachable to run these tests.", PostGISImage, err)
	}

	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			t.Errorf("terminate %s: %v", PostGISImage, err)
		}
	})

	adminDSN, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("could not build a DSN for %s: %v", PostGISImage, err)
	}

	return createCleanDB(ctx, t, adminDSN)
}

// createCleanDB makes the pristine `template0`-derived database and returns its DSN.
func createCleanDB(ctx context.Context, t testing.TB, adminDSN string) string {
	t.Helper()

	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatalf("connect to %s to create the clean database: %v", PostGISImage, err)
	}
	defer func() { _ = admin.Close(ctx) }()

	// TEMPLATE template0 is what guarantees no extensions come along for the ride. The
	// identifier is a compile-time constant, not user input, so there is nothing to inject.
	if _, err := admin.Exec(ctx, `CREATE DATABASE `+cleanDB+` TEMPLATE template0`); err != nil {
		t.Fatalf("create the clean database %q: %v", cleanDB, err)
	}

	u, err := url.Parse(adminDSN)
	if err != nil {
		t.Fatalf("parse the container DSN: %v", err)
	}
	u.Path = "/" + cleanDB
	return u.String()
}
