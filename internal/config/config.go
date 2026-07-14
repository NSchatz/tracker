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

	// ShutdownTimeout bounds how long a graceful shutdown may take before
	// in-flight requests are abandoned.
	ShutdownTimeout time.Duration
}

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

	c := &Config{
		DatabaseURL:     dsn,
		Addr:            envOr(EnvPrefix+"ADDR", ":8080"),
		LogLevel:        envOr(EnvPrefix+"LOG_LEVEL", "info"),
		ShutdownTimeout: 15 * time.Second,
	}

	if raw := strings.TrimSpace(os.Getenv(EnvPrefix + "SHUTDOWN_TIMEOUT_SEC")); raw != "" {
		secs, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("%sSHUTDOWN_TIMEOUT_SEC %q is not an integer: %w", EnvPrefix, raw, err)
		}
		if secs <= 0 {
			return nil, fmt.Errorf("%sSHUTDOWN_TIMEOUT_SEC %d must be > 0", EnvPrefix, secs)
		}
		c.ShutdownTimeout = time.Duration(secs) * time.Second
	}

	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
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
