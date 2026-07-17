// Package config loads tracker's server configuration from the environment.
//
// The server is configured by env vars only (no config file): it is a single static
// binary meant to run in a container, and the one value it cannot live without — the
// database DSN — carries a password. Keeping that on the environment and out of a file
// means there is no config artifact to accidentally commit.
//
// The governing rule is roadmap S0's fail-safe: the app REFUSES TO START on missing or
// invalid required config, with a typed error. It never boots with a silent default for
// a secret. A tracker that comes up healthy while pointed at nothing is worse than one
// that will not come up at all — the first is discovered in production.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// EnvPrefix namespaces every variable this package reads.
const EnvPrefix = "TRACKER_"

// ErrMissingDatabaseURL is returned when TRACKER_DATABASE_URL is absent or empty.
// It is a sentinel so main can report the one actionable fix rather than a stack trace.
var ErrMissingDatabaseURL = errors.New(EnvPrefix + "DATABASE_URL is not set: refusing to start without a database")

// Config is the server's validated configuration. A Config that exists has been
// validated — Load is the only way to make one.
type Config struct {
	// DatabaseURL is the PostgreSQL DSN. Required; no default, ever.
	DatabaseURL string

	// Addr is the listen address for the HTTP server.
	Addr string

	// LogLevel is one of debug|info|warn|error.
	LogLevel string

	// PushProvider selects the S6 push backend: "" (disabled — the default), "fcm", or "unifiedpush".
	// Push is OPTIONAL: a deployment that sets nothing still ingests, evaluates and serves the geofence
	// event log — it just delivers no alerts. This mirrors roadmap §10 open question #1: the default
	// backend is a human choice, so the default here is "no backend", never a silent one.
	PushProvider string

	// FCMProjectID is the Firebase project id the FCM v1 endpoint is scoped to. Required when
	// PushProvider is "fcm"; ignored otherwise.
	FCMProjectID string

	// FCMCredentialsFile is the path to the Google service-account JSON key the FCM sender mints access
	// tokens from. Required when PushProvider is "fcm"; ignored otherwise. The file is read at start-up
	// (the token source fails loudly if it is missing or malformed), never a secret in the repo.
	FCMCredentialsFile string

	// TLSCertFile and TLSKeyFile are the PEM certificate and private key the server terminates HTTPS
	// with. Both set → the server serves TLS (roadmap §7: HTTPS-only, no plaintext endpoint). Both
	// empty is only allowed with AllowPlaintext; one without the other is a start-up error, because a
	// half-configured TLS pair is a deploy that THINKS it is encrypted and is not. The key file is a
	// secret and lives on disk / in a secret store, never in the repo.
	TLSCertFile string
	TLSKeyFile  string

	// AllowPlaintext is the EXPLICIT opt-out from TLS. Location is maximally sensitive (§7), so the
	// fail-safe is that the server refuses to start with neither TLS nor this flag: a plaintext
	// deployment must be a decision someone made on purpose (local dev, or a trusted reverse proxy
	// terminating TLS upstream), never an accident of an unset variable. `config-lint` fails on it
	// regardless — it is a start-up escape hatch, not a production-blessed configuration.
	AllowPlaintext bool

	// RetentionDays is the history retention window: the background purge (internal/retention) drops
	// monthly `fixes` partitions whose entire range is older than this many days (§7, GDPR Art. 5(1)(e)
	// storage-limitation as design guidance). 0 — the default — DISABLES the purge: keep history
	// forever. That default is deliberate and honest: silently deleting a family's location history on
	// a window nobody chose would be worse than keeping it, so purging is opt-in with a number the
	// operator sets, not a guess the server makes.
	RetentionDays int
}

// Push backend identifiers. These match internal/store's push_provider values and internal/push's
// senders; a config that names a backend the deployment cannot build is refused at Load, not
// discovered at the first crossing.
const (
	PushProviderNone        = ""
	PushProviderFCM         = "fcm"
	PushProviderUnifiedPush = "unifiedpush"
)

// ShutdownTimeout bounds how long a graceful shutdown may take before in-flight requests
// are abandoned. A constant, not a knob: nothing in S0's acceptance asked for it to be
// tunable, and an env var nobody has a reason to set is surface that has to be validated,
// tested and documented forever. Make it configurable when something actually needs to
// configure it.
const ShutdownTimeout = 15 * time.Second

// Load reads the configuration from the environment and validates it.
//
// It returns a typed error on the first problem it finds and NEVER returns a partially
// populated Config alongside one: a caller that ignores the error must not be handed
// something that looks usable.
func Load() (*Config, error) {
	dsn := strings.TrimSpace(os.Getenv(EnvPrefix + "DATABASE_URL"))
	if dsn == "" {
		return nil, ErrMissingDatabaseURL
	}

	retentionDays, err := envInt(EnvPrefix+"RETENTION_DAYS", 0)
	if err != nil {
		return nil, err
	}

	c := &Config{
		DatabaseURL:        dsn,
		Addr:               envOr(EnvPrefix+"ADDR", ":8080"),
		LogLevel:           envOr(EnvPrefix+"LOG_LEVEL", "info"),
		PushProvider:       strings.TrimSpace(os.Getenv(EnvPrefix + "PUSH_PROVIDER")),
		FCMProjectID:       strings.TrimSpace(os.Getenv(EnvPrefix + "FCM_PROJECT_ID")),
		FCMCredentialsFile: strings.TrimSpace(os.Getenv(EnvPrefix + "FCM_CREDENTIALS_FILE")),
		TLSCertFile:        strings.TrimSpace(os.Getenv(EnvPrefix + "TLS_CERT_FILE")),
		TLSKeyFile:         strings.TrimSpace(os.Getenv(EnvPrefix + "TLS_KEY_FILE")),
		AllowPlaintext:     envBool(EnvPrefix + "ALLOW_PLAINTEXT"),
		RetentionDays:      retentionDays,
	}

	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// ServesTLS reports whether the server terminates HTTPS itself — i.e. both the certificate and key
// files are configured. When false, the server runs plaintext, which RequireServable only permits
// under AllowPlaintext.
func (c *Config) ServesTLS() bool {
	return c.TLSCertFile != "" && c.TLSKeyFile != ""
}

func (c *Config) validate() error {
	// Parse the DSN rather than pattern-matching it. A typo'd scheme (`postgress://`)
	// otherwise surfaces as a connection failure at first query — long after start-up
	// reported healthy.
	u, err := url.Parse(c.DatabaseURL)
	if err != nil {
		return fmt.Errorf("%sDATABASE_URL is not a valid URL: %w", EnvPrefix, err)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return fmt.Errorf("%sDATABASE_URL scheme %q is not postgres:// or postgresql://", EnvPrefix, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("%sDATABASE_URL has no host", EnvPrefix)
	}

	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("%sLOG_LEVEL %q is not one of debug|info|warn|error", EnvPrefix, c.LogLevel)
	}

	if c.Addr == "" {
		return fmt.Errorf("%sADDR is empty", EnvPrefix)
	}

	// Push config: the fail-safe applies to the CHOSEN backend. Disabled is fine (no push); a named
	// backend that is missing what it needs to send refuses to start, rather than booting and silently
	// dropping every alert. An unknown provider is likewise a start-up error, not a guess.
	switch c.PushProvider {
	case PushProviderNone, PushProviderUnifiedPush:
		// Disabled, or UnifiedPush (whose endpoint is per-subscription, so it needs no global config).
	case PushProviderFCM:
		if c.FCMProjectID == "" {
			return fmt.Errorf("%sPUSH_PROVIDER=fcm needs %sFCM_PROJECT_ID", EnvPrefix, EnvPrefix)
		}
		if c.FCMCredentialsFile == "" {
			return fmt.Errorf("%sPUSH_PROVIDER=fcm needs %sFCM_CREDENTIALS_FILE", EnvPrefix, EnvPrefix)
		}
	default:
		return fmt.Errorf("%sPUSH_PROVIDER %q is not one of \"\" (disabled), %q, or %q",
			EnvPrefix, c.PushProvider, PushProviderFCM, PushProviderUnifiedPush)
	}

	// TLS PAIR consistency is always a misconfiguration wherever it appears — a cert without its key
	// (or vice versa) is a deploy that believes it is encrypted and is not — so it is refused at Load,
	// for every command. The stronger "must actually serve TLS or explicitly allow plaintext" policy is
	// a SERVING concern (see RequireServable): it belongs to `serve`, not to the operator DB commands
	// (enroll, rotate, …) that share this config but expose no endpoint.
	if err := validateTLSPair(c.TLSCertFile, c.TLSKeyFile); err != nil {
		return err
	}

	if c.RetentionDays < 0 {
		return fmt.Errorf("%sRETENTION_DAYS %d is negative; use 0 to keep history forever or a positive number of days", EnvPrefix, c.RetentionDays)
	}
	return nil
}

// ErrTLSRequired is the server start-up refusal when neither a TLS certificate/key pair nor the
// explicit AllowPlaintext opt-out is set. It is a sentinel so main and the tests can match the exact
// fail-safe rather than a substring.
var ErrTLSRequired = errors.New(EnvPrefix + "TLS_CERT_FILE/" + EnvPrefix + "TLS_KEY_FILE are not set: refusing to serve a family location tracker in plaintext — set both, or set " + EnvPrefix + "ALLOW_PLAINTEXT=1 to run without TLS behind a trusted terminating proxy")

// RequireServable is the §7 serving fail-safe: the HTTP server refuses to start with neither TLS nor
// the explicit AllowPlaintext opt-out. It is separate from Load so that only the `serve` path enforces
// it — the operator DB commands (enroll, rotate, add-place, …) share this config but expose no
// endpoint, and requiring a TLS cert to enroll a phone would be a fail-safe pointed at the wrong thing.
func (c *Config) RequireServable() error {
	if !c.ServesTLS() && !c.AllowPlaintext {
		return ErrTLSRequired
	}
	return nil
}

// validateTLSPair refuses a certificate without a key or a key without a certificate — a
// half-configured pair is the dangerous case, because it reads as "TLS is set up" while the server
// would actually fall through to plaintext.
func validateTLSPair(certFile, keyFile string) error {
	switch {
	case certFile != "" && keyFile == "":
		return fmt.Errorf("%sTLS_CERT_FILE is set but %sTLS_KEY_FILE is not: a certificate without its key cannot serve TLS", EnvPrefix, EnvPrefix)
	case keyFile != "" && certFile == "":
		return fmt.Errorf("%sTLS_KEY_FILE is set but %sTLS_CERT_FILE is not: a key without its certificate cannot serve TLS", EnvPrefix, EnvPrefix)
	}
	return nil
}

// LintTLS is the STRICTER production check `config-lint` runs, distinct from validate()'s start-up
// rule. validate() lets AllowPlaintext through as a deliberate escape hatch; LintTLS does not — a
// linted (production) configuration must terminate TLS, full stop, so "there is a plaintext endpoint"
// is always a failure here whatever ALLOW_PLAINTEXT says. It reads the same two files as the server.
func LintTLS(certFile, keyFile string) error {
	certFile, keyFile = strings.TrimSpace(certFile), strings.TrimSpace(keyFile)
	if err := validateTLSPair(certFile, keyFile); err != nil {
		return err
	}
	if certFile == "" && keyFile == "" {
		return fmt.Errorf("plaintext endpoint: %sTLS_CERT_FILE and %sTLS_KEY_FILE are not set, so the server would serve location data unencrypted. A production deployment must terminate TLS", EnvPrefix, EnvPrefix)
	}
	return nil
}

// Redacted returns the DSN with its password replaced, safe to log.
//
// The DSN is the one config value that carries a credential, and start-up logging is
// exactly where it would leak. url.URL.Redacted() does this for us; the fallback for an
// unparseable DSN is to say nothing rather than guess, because a DSN we could not parse
// is a DSN whose password we cannot reliably find.
func (c *Config) Redacted() string {
	u, err := url.Parse(c.DatabaseURL)
	if err != nil {
		return "(unparseable DSN)"
	}
	return u.Redacted()
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// envBool reads a boolean-ish flag: "1", "true", "yes", "on" (any case) are true; anything else,
// including unset, is false. It is deliberately permissive on the true side and defaults false so an
// unset flag is never accidentally "on".
func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// envInt reads an integer with a default when unset/blank. A present-but-unparseable value is a
// typed error, not a silent fallback to the default: a deployment that set RETENTION_DAYS=thirty
// means to retain for a bounded window, and quietly treating that as "keep forever" is exactly the
// silent-wrong-default the fail-safe forbids.
func envInt(key string, def int) (int, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s %q is not an integer: %w", key, v, err)
	}
	return n, nil
}
