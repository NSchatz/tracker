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

	"github.com/NSchatz/tracker/internal/server"
)

type stubPinger struct{ err error }

func (s stubPinger) Ping(context.Context) error { return s.err }

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestHealthzReportsOKWhenTheDatabaseIsReachable(t *testing.T) {
	t.Parallel()

	h := server.New(stubPinger{err: nil}, discardLogger())
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

	h := server.New(stubPinger{err: errors.New("connection refused")}, discardLogger())
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

	h := server.New(stubPinger{}, discardLogger())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/fixes", nil))

	// S2 adds this route. Until then it must not exist — asserting that keeps a
	// half-built endpoint from being mistaken for a working one.
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
