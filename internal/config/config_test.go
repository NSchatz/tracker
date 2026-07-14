package config

import (
	"errors"
	"strings"
	"testing"
)

// validDSN is synthetic — a local-only placeholder, never a real credential.
const validDSN = "postgres://tracker:tracker@localhost:5432/tracker?sslmode=disable"

func TestLoadRefusesWithoutDatabaseURL(t *testing.T) {
	// The S0 fail-safe: no DSN => refuse to start. Not a default, not an empty
	// string that fails later at first query — a typed error, up front.
	for _, unset := range []string{"", "   "} {
		t.Setenv(EnvPrefix+"DATABASE_URL", unset)

		got, err := Load()
		if err == nil {
			t.Fatalf("DATABASE_URL=%q: expected refusal, got config %+v", unset, got)
		}
		if !errors.Is(err, ErrMissingDatabaseURL) {
			t.Fatalf("DATABASE_URL=%q: want ErrMissingDatabaseURL, got %v", unset, err)
		}
		if got != nil {
			t.Fatalf("DATABASE_URL=%q: returned a Config alongside an error: %+v", unset, got)
		}
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv(EnvPrefix+"DATABASE_URL", validDSN)

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Addr != ":8080" {
		t.Errorf("Addr = %q, want :8080", c.Addr)
	}
	if c.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", c.LogLevel)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string // substring the error must name, so the message stays actionable
	}{
		{
			name: "DSN with a typo'd scheme",
			env:  map[string]string{"DATABASE_URL": "postgress://u:p@localhost:5432/db"},
			want: "is not postgres:// or postgresql://",
		},
		{
			name: "DSN with no host",
			env:  map[string]string{"DATABASE_URL": "postgres:///db"},
			want: "has no host",
		},
		{
			name: "unknown log level",
			env:  map[string]string{"DATABASE_URL": validDSN, "LOG_LEVEL": "loud"},
			want: "not one of debug|info|warn|error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvPrefix+"DATABASE_URL", "")
			t.Setenv(EnvPrefix+"LOG_LEVEL", "")
			for k, v := range tt.env {
				t.Setenv(EnvPrefix+k, v)
			}

			got, err := Load()
			if err == nil {
				t.Fatalf("expected refusal, got config %+v", got)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not name %q", err, tt.want)
			}
			if got != nil {
				t.Errorf("returned a Config alongside an error: %+v", got)
			}
		})
	}
}

func TestRedactedHidesThePassword(t *testing.T) {
	// The DSN is the only config value carrying a credential, and start-up logging is
	// where it would leak. This test is the tripwire on that.
	t.Setenv(EnvPrefix+"DATABASE_URL", "postgres://tracker:hunter2@localhost:5432/tracker")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	red := c.Redacted()
	if strings.Contains(red, "hunter2") {
		t.Fatalf("Redacted() leaked the password: %q", red)
	}
	// It masks the password rather than dropping it, so the rest of the DSN still reads
	// as a DSN — which is the point of logging it at all: an operator has to be able to
	// see WHICH database was refused.
	if !strings.Contains(red, "localhost:5432/tracker") {
		t.Errorf("Redacted() = %q, want it to keep the host and database", red)
	}
}
