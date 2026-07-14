// Command tracker is the self-hosted family location tracker's server.
//
// S0 stands up the spine: config, migrations, a database pool, and /healthz. The
// ingestion, read, stream and geofencing surfaces arrive in later phases.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/NSchatz/tracker/internal/config"
	"github.com/NSchatz/tracker/internal/db"
	"github.com/NSchatz/tracker/internal/server"
)

func main() {
	if err := run(); err != nil {
		// One line, on stderr, naming the fix. A start-up failure is read by a human
		// staring at `docker logs`, not by a debugger.
		fmt.Fprintf(os.Stderr, "tracker: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		// The fail-safe. We do not start with a default DSN, an empty DSN, or a guess:
		// a tracker pointed at the wrong database is worse than one that refused to boot,
		// because the first is discovered in production.
		return err
	}

	logger := newLogger(cfg.LogLevel)
	logger.Info("starting", "addr", cfg.Addr, "database", cfg.Redacted())

	// Signals cancel the root context, which unwinds everything below.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Migrate BEFORE listening. Serving requests against a half-migrated schema is the
	// failure this ordering prevents — so a migration error is fatal, never a warning.
	logger.Info("applying migrations")
	if err := db.Up(ctx, cfg.DatabaseURL); err != nil {
		return err
	}
	version, err := db.Version(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	logger.Info("migrations applied", "schema_version", version)

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           server.New(pool, logger),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("listen: %w", err)
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("shutting down", "timeout", cfg.ShutdownTimeout)
	}

	// A fresh context: the root one is already cancelled, and Shutdown needs a live
	// deadline to drain in-flight requests against.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	logger.Info("stopped")
	return nil
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}
