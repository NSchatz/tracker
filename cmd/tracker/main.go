// Command tracker is the self-hosted family location tracker's server — and the small set of
// operator commands that stand up the accounts it serves.
//
// With no subcommand (or `serve`) it runs the HTTP server: config, migrations, a database pool,
// partition provisioning, and the S2 ingestion + S3 read surface. The `create-family`, `enroll`,
// and `add-viewer` subcommands are the OPERATOR path for issuing credentials: `enroll` mints a
// device's write token, `add-viewer` a human's read token.
//
// # Why enrollment is a CLI command, not an HTTP endpoint
//
// Issuing a device token is a privileged act: whoever can do it can add a phone to a family and
// write into its location history. S2 has no operator/admin authentication yet (viewer accounts
// exist as a table, but there is no login), so an HTTP enrollment endpoint would either be
// unauthenticated — anyone on the network could enroll a device — or would need an auth story this
// phase does not have. Running enrollment as a command the operator invokes against the database,
// out of band, sidesteps that entirely: the credential to enroll IS shell access to the deployment.
// When there is a real admin surface (later), enrollment can move onto it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/NSchatz/tracker/internal/auth"
	"github.com/NSchatz/tracker/internal/config"
	"github.com/NSchatz/tracker/internal/db"
	"github.com/NSchatz/tracker/internal/server"
	"github.com/NSchatz/tracker/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// partitionLookaheadMonths is how far ahead of today the server provisions monthly partitions of
// `fixes` at start-up: this month and the next. Two is enough for the month rollover to be a no-op
// for any server restarted at least monthly, and small enough that a long-lived process's need for a
// maintenance tick (S7) stays visible. From S2 on, ingestion ALSO provisions a fix's month on demand
// (store.IngestFix), so a process that outlives this lookahead self-heals instead of losing data.
const partitionLookaheadMonths = 2

func main() {
	// Dispatch on the first non-flag argument. `tracker`, `tracker serve`, and `tracker -flag` all
	// run the server; `tracker enroll ...` and `tracker create-family ...` are the operator tools.
	sub, args := "serve", os.Args[1:]
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		sub, args = args[0], args[1:]
	}

	var err error
	switch sub {
	case "serve":
		err = runServe()
	case "create-family":
		err = runCreateFamily(args)
	case "enroll":
		err = runEnroll(args)
	case "add-viewer":
		err = runAddViewer(args)
	default:
		err = fmt.Errorf("unknown command %q — expected serve, create-family, enroll, or add-viewer", sub)
	}

	if err != nil {
		// One line, on stderr, naming the fix. A start-up or command failure is read by a human
		// staring at `docker logs` or a terminal, not by a debugger.
		fmt.Fprintf(os.Stderr, "tracker: %v\n", err)
		os.Exit(1)
	}
}

func runServe() error {
	cfg, err := config.Load()
	if err != nil {
		// The fail-safe. We do not start with a default DSN, an empty DSN, or a guess: a tracker
		// pointed at the wrong database is worse than one that refused to boot.
		return err
	}

	logger := newLogger(cfg.LogLevel)
	logger.Info("starting", "addr", cfg.Addr, "database", cfg.Redacted())

	// Signals cancel the root context, which unwinds everything below.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Migrate BEFORE listening. Serving requests against a half-migrated schema is the failure this
	// ordering prevents — so a migration error is fatal, never a warning.
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

	// `fixes` is partitioned by month and has NO default partition on purpose, so a fix whose month
	// was never provisioned is rejected outright rather than landing somewhere retention can never
	// drop it. Provisioning is therefore a start-up responsibility, and the lookahead stops
	// ingestion breaking at midnight on the 1st. A process that outlives the lookahead is caught by
	// the on-ingest provisioning in store.IngestFix; the real fix (a ticker) is S7's.
	names, err := db.EnsurePartitions(ctx, pool, time.Now(), partitionLookaheadMonths)
	if err != nil {
		return err
	}
	logger.Info("fix partitions ready", "partitions", names)

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
		logger.Info("shutting down", "timeout", config.ShutdownTimeout)
	}

	// A fresh context: the root one is already cancelled, and Shutdown needs a live deadline to
	// drain in-flight requests against.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), config.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	logger.Info("stopped")
	return nil
}

// runCreateFamily creates a family and prints its id — the id `enroll` needs.
func runCreateFamily(args []string) error {
	fs := flag.NewFlagSet("create-family", flag.ContinueOnError)
	name := fs.String("name", "", "the family's name (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return errors.New("create-family needs -name")
	}

	ctx := context.Background()
	pool, err := openForCommand(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()

	id, err := store.CreateFamily(ctx, pool, *name)
	if err != nil {
		return err
	}
	fmt.Printf("family created\n  id:   %s\n  name: %s\n", id, *name)
	return nil
}

// runEnroll issues a per-device bearer token and prints it ONCE. The token is never stored — only
// its hash is — so this is the only moment it exists in readable form.
func runEnroll(args []string) error {
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	familyID := fs.String("family", "", "the family id to enroll into (see create-family) (required)")
	name := fs.String("name", "", "a label for the device, e.g. \"Alice's phone\" (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *familyID == "" || *name == "" {
		return errors.New("enroll needs -family and -name")
	}

	token, err := auth.Generate()
	if err != nil {
		return err
	}
	hash := token.Hash()

	ctx := context.Background()
	pool, err := openForCommand(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()

	deviceID, err := store.CreateDevice(ctx, pool, *familyID, *name, hash[:])
	if err != nil {
		return err
	}

	// Straight to stdout, deliberately NOT through the structured logger: a token must not land in
	// the server's logs. This is the one time it is shown.
	fmt.Printf("device enrolled\n  id:     %s\n  family: %s\n  name:   %s\n\n", deviceID, *familyID, *name)
	fmt.Printf("bearer token (store it now — it is not recoverable):\n  %s\n", token)
	return nil
}

// runAddViewer issues a per-viewer bearer token for a human who watches the map, and prints it
// ONCE. It is the read-side counterpart to runEnroll: a viewer token is what the S3 read API
// (GET /v1/positions, /v1/devices/{id}/history, /v1/near) authenticates, and — like a device token —
// it is issued by the operator out of band rather than over HTTP, because there is still no admin
// login (see the package doc), and the credential to mint one is shell access to the deployment.
func runAddViewer(args []string) error {
	fs := flag.NewFlagSet("add-viewer", flag.ContinueOnError)
	familyID := fs.String("family", "", "the family id whose map this viewer may read (required)")
	email := fs.String("email", "", "the viewer's email, unique within the deployment (required)")
	name := fs.String("name", "", "a display name for the viewer, e.g. \"Alice\" (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *familyID == "" || *email == "" || *name == "" {
		return errors.New("add-viewer needs -family, -email, and -name")
	}

	token, err := auth.Generate()
	if err != nil {
		return err
	}
	hash := token.Hash()

	ctx := context.Background()
	pool, err := openForCommand(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()

	viewerID, err := store.CreateViewer(ctx, pool, *familyID, *email, *name, hash[:])
	if err != nil {
		return err
	}

	// stdout only, never the structured logger: a token must not land in the server's logs.
	fmt.Printf("viewer added\n  id:     %s\n  family: %s\n  email:  %s\n  name:   %s\n\n", viewerID, *familyID, *email, *name)
	fmt.Printf("bearer token (store it now — it is not recoverable):\n  %s\n", token)
	return nil
}

// openForCommand loads config, applies migrations (so a command works against a fresh database), and
// opens a pool — the shared setup for the operator subcommands.
func openForCommand(ctx context.Context) (*pgxpool.Pool, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if err := db.Up(ctx, cfg.DatabaseURL); err != nil {
		return nil, err
	}
	return db.Open(ctx, cfg.DatabaseURL)
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
