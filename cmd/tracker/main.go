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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/NSchatz/tracker/internal/auth"
	"github.com/NSchatz/tracker/internal/config"
	"github.com/NSchatz/tracker/internal/db"
	"github.com/NSchatz/tracker/internal/push"
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
	case "add-place":
		err = runAddPlace(args)
	case "list-places":
		err = runListPlaces(args)
	case "remove-place":
		err = runRemovePlace(args)
	default:
		err = fmt.Errorf("unknown command %q — expected serve, create-family, enroll, add-viewer, "+
			"add-place, list-places, or remove-place", sub)
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

	// S6 push delivery. Disabled unless a backend is configured (§10 open question #1 — the default is
	// a human choice, so the default is none). When enabled, the dispatcher owns a worker goroutine
	// that must be drained on shutdown; a nil notifier tells server.New to substitute a no-op.
	notifier, closeNotifier, err := buildPushNotifier(cfg, pool, logger)
	if err != nil {
		return err
	}
	defer closeNotifier()

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           server.New(pool, notifier, logger),
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

// buildPushNotifier assembles the S6 push pipeline from config: a per-provider sender, a bounded
// retrying dispatcher in front of it, and the EventNotifier the server calls on each crossing. It
// returns a nil notifier (server.New substitutes a no-op) and a no-op closer when push is disabled, so
// the caller can always `defer closeNotifier()` unconditionally.
//
// The closer drains the dispatcher's worker on shutdown. Building the FCM token source here — reading
// the service-account key at start-up — is deliberate: a push backend that cannot authenticate should
// fail the boot (the fail-safe), not the first alert hours later.
func buildPushNotifier(cfg *config.Config, pool *pgxpool.Pool, logger *slog.Logger) (server.Notifier, func(), error) {
	noop := func() {}

	var sender push.Sender
	switch cfg.PushProvider {
	case config.PushProviderNone:
		logger.Info("push delivery disabled (no TRACKER_PUSH_PROVIDER set)")
		return nil, noop, nil
	case config.PushProviderFCM:
		tokens, err := push.NewServiceAccountTokenSourceFromFile(cfg.FCMCredentialsFile, nil)
		if err != nil {
			return nil, noop, fmt.Errorf("configure FCM push: %w", err)
		}
		sender = push.NewFCMSender(cfg.FCMProjectID, tokens, nil)
		logger.Info("push delivery enabled", "provider", "fcm", "project", cfg.FCMProjectID)
	case config.PushProviderUnifiedPush:
		sender = push.NewUnifiedPushSender(nil)
		logger.Info("push delivery enabled", "provider", "unifiedpush")
	default:
		// config.validate already rejected an unknown provider; this is defence in depth.
		return nil, noop, fmt.Errorf("unknown push provider %q", cfg.PushProvider)
	}

	disp := push.NewDispatcher(map[string]push.Sender{cfg.PushProvider: sender}, logger)
	notifier := push.NewEventNotifier(pool, disp, logger)
	return notifier, disp.Close, nil
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

// pointRing collects repeated -point lon,lat flags into a ring, longitude-first — the same axis order
// as everywhere else in tracker, enforced here at the CLI boundary so a Place cannot be defined with
// its coordinates swapped. It implements flag.Value.
type pointRing []store.Point

func (p *pointRing) String() string { return fmt.Sprintf("%d point(s)", len(*p)) }

func (p *pointRing) Set(v string) error {
	lonStr, latStr, ok := strings.Cut(v, ",")
	if !ok {
		return fmt.Errorf("point %q is not \"lon,lat\"", v)
	}
	lon, err := strconv.ParseFloat(strings.TrimSpace(lonStr), 64)
	if err != nil {
		return fmt.Errorf("point %q: longitude is not a number", v)
	}
	lat, err := strconv.ParseFloat(strings.TrimSpace(latStr), 64)
	if err != nil {
		return fmt.Errorf("point %q: latitude is not a number", v)
	}
	*p = append(*p, store.Point{Lon: lon, Lat: lat})
	return nil
}

// runAddPlace creates a family-scoped Place from a ring of -point lon,lat vertices. It is the operator
// path for "Places CRUD" create, alongside enroll/add-viewer — Places are privileged family config,
// not something a viewer token writes over HTTP (§7). The ring is validated (a bowtie or out-of-range
// vertex is refused) by store.CreateGeofence before anything is stored.
//
// Longitude first, as everywhere: `-point 12.4964,41.9028` is (lon=12.4964, lat=41.9028). The ring is
// closed automatically if the last point is not the first.
func runAddPlace(args []string) error {
	fs := flag.NewFlagSet("add-place", flag.ContinueOnError)
	familyID := fs.String("family", "", "the family id this Place belongs to (required)")
	name := fs.String("name", "", "a label for the Place, e.g. \"Home\" (required)")
	var ring pointRing
	fs.Var(&ring, "point", "a ring vertex as lon,lat — repeat at least 3 times (longitude FIRST)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *familyID == "" || *name == "" {
		return errors.New("add-place needs -family and -name")
	}
	if len(ring) < 3 {
		return fmt.Errorf("add-place needs at least 3 -point lon,lat vertices, got %d", len(ring))
	}

	ctx := context.Background()
	pool, err := openForCommand(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()

	id, err := store.CreateGeofence(ctx, pool, *familyID, *name, ring)
	if err != nil {
		return err
	}
	fmt.Printf("place created\n  id:     %s\n  family: %s\n  name:   %s\n  points: %d\n", id, *familyID, *name, len(ring))
	return nil
}

// runListPlaces prints a family's Places — the read side of the operator CRUD path.
func runListPlaces(args []string) error {
	fs := flag.NewFlagSet("list-places", flag.ContinueOnError)
	familyID := fs.String("family", "", "the family id whose Places to list (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *familyID == "" {
		return errors.New("list-places needs -family")
	}

	ctx := context.Background()
	pool, err := openForCommand(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()

	places, err := store.ListGeofences(ctx, pool, *familyID)
	if err != nil {
		return err
	}
	if len(places) == 0 {
		fmt.Printf("no places for family %s\n", *familyID)
		return nil
	}
	fmt.Printf("places for family %s:\n", *familyID)
	for _, p := range places {
		fmt.Printf("  %s  %s\n", p.ID, p.Name)
	}
	return nil
}

// runRemovePlace deletes a Place, family-scoped so a mistyped id in another family removes nothing.
// Its geofence_events go with it (ON DELETE CASCADE) — removing a Place forgets its crossings.
func runRemovePlace(args []string) error {
	fs := flag.NewFlagSet("remove-place", flag.ContinueOnError)
	familyID := fs.String("family", "", "the family id the Place belongs to (required)")
	id := fs.String("id", "", "the Place id to remove (see list-places) (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *familyID == "" || *id == "" {
		return errors.New("remove-place needs -family and -id")
	}

	ctx := context.Background()
	pool, err := openForCommand(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()

	removed, err := store.DeleteGeofence(ctx, pool, *familyID, *id)
	if err != nil {
		return err
	}
	if !removed {
		return fmt.Errorf("no place %s in family %s", *id, *familyID)
	}
	fmt.Printf("place removed\n  id:     %s\n  family: %s\n", *id, *familyID)
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
