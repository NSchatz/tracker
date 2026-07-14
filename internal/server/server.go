// Package server holds tracker's HTTP surface.
//
// S0 deliberately serves exactly one route: /healthz. The ingestion, read and stream
// endpoints arrive in S2–S4, against a data model that does not exist yet — building
// their scaffolding now would be guessing at a contract the roadmap has not fixed.
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Pinger is the health check's view of the database.
//
// It is an interface, not a *pgxpool.Pool, for one reason: it lets the "database is
// down" branch of /healthz be tested. That branch is the entire point of the endpoint,
// and with a concrete pool it would be the one path no test could reach.
type Pinger interface {
	Ping(ctx context.Context) error
}

// healthTimeout bounds the health check's database ping. A /healthz that hangs is worse
// than one that fails: an orchestrator waiting on it cannot tell "slow" from "wedged",
// so it keeps waiting. Bounded, it gets a clear answer.
const healthTimeout = 2 * time.Second

// New builds the HTTP handler.
func New(pinger Pinger, logger *slog.Logger) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)

	// Deliberately NOT middleware.RealIP. It rewrites r.RemoteAddr from X-Forwarded-For /
	// X-Real-IP / True-Client-IP whether or not the infrastructure in front of tracker
	// actually sets them, so any client can claim any address (GHSA-3fxj-6jh8-hvhx).
	// Nothing in S0 consumes the client IP. When something does — rate limiting or an
	// audit log — it needs a trusted-proxy configuration, not this.

	r.Get("/healthz", healthz(pinger, logger))

	return r
}

type healthResponse struct {
	Status   string `json:"status"`
	Database string `json:"database"`
}

// healthz reports whether tracker can actually serve.
//
// It PINGS THE DATABASE rather than returning a bare 200. tracker cannot do anything
// useful without PostGIS, so a health check that ignores it would report healthy on a
// process that fails every real request — and an orchestrator would leave it in the load
// balancer. Unreachable database => 503, which is the honest answer.
func healthz(pinger Pinger, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), healthTimeout)
		defer cancel()

		resp := healthResponse{Status: "ok", Database: "up"}
		code := http.StatusOK

		if err := pinger.Ping(ctx); err != nil {
			logger.WarnContext(ctx, "health check: database unreachable", "error", err)
			resp = healthResponse{Status: "unavailable", Database: "down"}
			code = http.StatusServiceUnavailable
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			logger.ErrorContext(ctx, "health check: write response", "error", err)
		}
	}
}
