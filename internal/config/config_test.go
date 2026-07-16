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
	// Clear the optional vars explicitly. Without this the test reads whatever the
	// developer happens to have exported and goes red on a clean tree — a gate that
	// depends on the ambient environment is not a gate.
	t.Setenv(EnvPrefix+"DATABASE_URL", validDSN)
	t.Setenv(EnvPrefix+"ADDR", "")
	t.Setenv(EnvPrefix+"LOG_LEVEL", "")

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

func TestLoadPushProvider(t *testing.T) {
	// Clears the push env for a subtest, so the ambient environment can never make this go red.
	clearPush := func(t *testing.T) {
		t.Setenv(EnvPrefix+"DATABASE_URL", validDSN)
		t.Setenv(EnvPrefix+"PUSH_PROVIDER", "")
		t.Setenv(EnvPrefix+"FCM_PROJECT_ID", "")
		t.Setenv(EnvPrefix+"FCM_CREDENTIALS_FILE", "")
	}

	t.Run("disabled by default", func(t *testing.T) {
		clearPush(t)
		c, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if c.PushProvider != PushProviderNone {
			t.Errorf("PushProvider = %q, want disabled by default", c.PushProvider)
		}
	})

	t.Run("unifiedpush needs no extra config", func(t *testing.T) {
		clearPush(t)
		t.Setenv(EnvPrefix+"PUSH_PROVIDER", "unifiedpush")
		if _, err := Load(); err != nil {
			t.Fatalf("Load unifiedpush: %v", err)
		}
	})

	t.Run("fcm requires project id and credentials file", func(t *testing.T) {
		clearPush(t)
		t.Setenv(EnvPrefix+"PUSH_PROVIDER", "fcm")

		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "FCM_PROJECT_ID") {
			t.Fatalf("fcm without project id: err = %v, want it to name FCM_PROJECT_ID", err)
		}

		t.Setenv(EnvPrefix+"FCM_PROJECT_ID", "my-project")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "FCM_CREDENTIALS_FILE") {
			t.Fatalf("fcm without credentials: err = %v, want it to name FCM_CREDENTIALS_FILE", err)
		}

		t.Setenv(EnvPrefix+"FCM_CREDENTIALS_FILE", "/etc/tracker/fcm.json")
		c, err := Load()
		if err != nil {
			t.Fatalf("fcm fully configured: %v", err)
		}
		if c.PushProvider != PushProviderFCM || c.FCMProjectID != "my-project" {
			t.Errorf("config = %+v, want fcm/my-project", c)
		}
	})

	t.Run("an unknown provider is refused", func(t *testing.T) {
		clearPush(t)
		t.Setenv(EnvPrefix+"PUSH_PROVIDER", "telegram")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "PUSH_PROVIDER") {
			t.Fatalf("unknown provider: err = %v, want a refusal naming PUSH_PROVIDER", err)
		}
	})
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
