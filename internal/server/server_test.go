package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/NSchatz/tracker/internal/db"
	"github.com/NSchatz/tracker/internal/server"
)

// stubDB satisfies server.DB for the health tests. Its Ping is stubbed; its query surface is a nil
// db.Querier, which never runs because /healthz does not touch it — and a health test that DID reach
// the database would panic loudly rather than pass on a stub, which is the property we want.
type stubDB struct {
	db.Querier
	err error
}

func (s stubDB) Ping(context.Context) error { return s.err }

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestHealthzReportsOKWhenTheDatabaseIsReachable(t *testing.T) {
	t.Parallel()

	h := server.New(stubDB{err: nil}, discardLogger())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var got struct {
		Status   string `json:"status"`
		Database string `json:"database"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not JSON: %v (%q)", err, rec.Body.String())
	}
	if got.Status != "ok" || got.Database != "up" {
		t.Errorf("body = %+v, want status=ok database=up", got)
	}
}

// The branch that matters. A /healthz that returns 200 while the database is unreachable
// tells an orchestrator to keep sending traffic to a process that fails every real
// request. 503 is the honest answer, and this is the test that holds it to it.
func TestHealthzReports503WhenTheDatabaseIsDown(t *testing.T) {
	t.Parallel()

	h := server.New(stubDB{err: errors.New("connection refused")}, discardLogger())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d — a health check that ignores the database is a lie",
			rec.Code, http.StatusServiceUnavailable)
	}

	var got struct {
		Status   string `json:"status"`
		Database string `json:"database"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not JSON: %v (%q)", err, rec.Body.String())
	}
	if got.Status != "unavailable" || got.Database != "down" {
		t.Errorf("body = %+v, want status=unavailable database=down", got)
	}
}

func TestUnknownRouteIs404(t *testing.T) {
	t.Parallel()

	h := server.New(stubDB{}, discardLogger())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/nonexistent", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestWriteRoutesRequireAuth pins that the S2 write surface is gated: a request with no credential
// is rejected at the middleware, BEFORE any handler or database touch, with a 401 and a
// WWW-Authenticate header. The stub's nil Querier is the proof it never reached the store — if the
// gate let an unauthenticated request through, authenticating it would panic rather than 401.
func TestWriteRoutesRequireAuth(t *testing.T) {
	t.Parallel()

	h := server.New(stubDB{}, discardLogger())
	for _, path := range []string{"/v1/fixes", "/owntracks"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, http.NoBody))

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("POST %s with no token = %d, want %d", path, rec.Code, http.StatusUnauthorized)
			}
			if got := rec.Header().Get("WWW-Authenticate"); got == "" {
				t.Errorf("a 401 must advertise how to authenticate; WWW-Authenticate is empty")
			}
		})
	}
}

// TestGetOnAWriteRouteIsMethodNotAllowed: /v1/fixes exists now (as POST), so a GET is a 405, not a
// 404. Pinning it documents that the route is real and write-only.
func TestGetOnAWriteRouteIsMethodNotAllowed(t *testing.T) {
	t.Parallel()

	h := server.New(stubDB{}, discardLogger())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/fixes", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /v1/fixes = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}
