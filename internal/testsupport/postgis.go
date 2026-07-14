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
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// PostGISImage is the image every test runs against. Pinned: the spatial assertions are
// assertions about a specific PostGIS version's behaviour, so floating this tag would let
// the thing under test change without the tests changing.
const PostGISImage = "postgis/postgis:16-3.4"

// These credentials are synthetic and local to a throwaway container that is never
// published to a port on the host. They are not, and must never become, a real secret.
const (
	testUser = "tracker"
	testPass = "tracker" //nolint:gosec // synthetic; a per-run throwaway container
	testDB   = "tracker_test"
)

// NewPostGIS starts a PostGIS container and returns a DSN pointing at it.
//
// The container is terminated when the test ends. Each call gets its OWN database, so
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

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("could not build a DSN for %s: %v", PostGISImage, err)
	}
	return dsn
}
