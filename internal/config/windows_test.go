// The presentation windows' configuration contract: the two variables, their seconds grammar, the
// empty-means-default rule, the representable range, the one-configured case and the ordering check.
//
// These are pure environment tests — no database, no container — because start-up validation must be
// decidable before anything is opened. Every case sets BOTH variables explicitly (usually to ""), so
// the developer's ambient environment can never decide the outcome: a gate that reads whatever
// happens to be exported is not a gate.
package config

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// clearWindows puts the process into the "nothing configured" state a fresh deployment starts in.
func clearWindows(t *testing.T) {
	t.Helper()
	t.Setenv(EnvPrefix+"DATABASE_URL", validDSN)
	t.Setenv(EnvLiveWindowSeconds, "")
	t.Setenv(EnvStaleWindowSeconds, "")
	// Hygiene: these subtests are about the windows, so nothing else may fail the Load.
	t.Setenv(EnvPrefix+"ADDR", "")
	t.Setenv(EnvPrefix+"LOG_LEVEL", "")
	t.Setenv(EnvPrefix+"PUSH_PROVIDER", "")
	t.Setenv(EnvPrefix+"ALLOW_PLAINTEXT", "")
	t.Setenv(EnvPrefix+"TLS_CERT_FILE", "")
	t.Setenv(EnvPrefix+"TLS_KEY_FILE", "")
	t.Setenv(EnvPrefix+"RETENTION_DAYS", "")
}

// TestWindowsDefaultWhenNeitherIsConfigured is AC1: unset, empty and whitespace-only are ONE case —
// not configured — and not configured means 120/900. A compose file rendered with an unset shell
// default hands the process an empty string, so this is the ordinary deployment, not an edge case.
func TestWindowsDefaultWhenNeitherIsConfigured(t *testing.T) {
	for _, unset := range []string{"", "   ", "\t\n "} {
		t.Run("unconfigured="+strings.ReplaceAll(unset, "\t", "\\t"), func(t *testing.T) {
			clearWindows(t)
			t.Setenv(EnvLiveWindowSeconds, unset)
			t.Setenv(EnvStaleWindowSeconds, unset)

			c, err := Load()
			if err != nil {
				t.Fatalf("Load with neither window configured: %v", err)
			}
			if c.LiveWindowSeconds != DefaultLiveWindowSeconds || c.StaleWindowSeconds != DefaultStaleWindowSeconds {
				t.Fatalf("effective windows = (%d, %d) seconds, want the defaults (%d, %d)",
					c.LiveWindowSeconds, c.StaleWindowSeconds, DefaultLiveWindowSeconds, DefaultStaleWindowSeconds)
			}
		})
	}
}

// TestWindowsApplyAConfiguredPair is AC2: a legal pair is applied verbatim, in seconds.
func TestWindowsApplyAConfiguredPair(t *testing.T) {
	clearWindows(t)
	t.Setenv(EnvLiveWindowSeconds, "5")
	t.Setenv(EnvStaleWindowSeconds, "10")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load with (5, 10): %v", err)
	}
	if c.LiveWindowSeconds != 5 || c.StaleWindowSeconds != 10 {
		t.Fatalf("effective windows = (%d, %d), want (5, 10) seconds", c.LiveWindowSeconds, c.StaleWindowSeconds)
	}
}

// TestOneConfiguredWindowDefaultsTheOther is AC3, including the worked case the spec names: setting
// only the stale window to 60 yields the EFFECTIVE pair (120, 60), which violates the ordering rule
// and must refuse the start rather than boot with a state no device could ever reach.
func TestOneConfiguredWindowDefaultsTheOther(t *testing.T) {
	t.Run("only live configured: stale takes its default", func(t *testing.T) {
		clearWindows(t)
		t.Setenv(EnvLiveWindowSeconds, "30")

		c, err := Load()
		if err != nil {
			t.Fatalf("Load with only the live window set: %v", err)
		}
		if c.LiveWindowSeconds != 30 || c.StaleWindowSeconds != DefaultStaleWindowSeconds {
			t.Fatalf("effective windows = (%d, %d), want (30, %d)",
				c.LiveWindowSeconds, c.StaleWindowSeconds, DefaultStaleWindowSeconds)
		}
	})

	t.Run("only stale configured to 60: the effective pair (120, 60) refuses the start", func(t *testing.T) {
		clearWindows(t)
		t.Setenv(EnvStaleWindowSeconds, "60")

		got, err := Load()
		if err == nil {
			t.Fatalf("effective pair (120, 60) started; want a refusal. Config: %+v", got)
		}
		if got != nil {
			t.Fatalf("returned a Config alongside an error: %+v", got)
		}
		// AC5: BOTH effective values, as a number of seconds, so the operator can see the pair that
		// was actually applied rather than only the one they typed.
		for _, want := range []string{EnvLiveWindowSeconds, EnvStaleWindowSeconds, "120", "60", "seconds"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal %q does not name %q", err, want)
			}
		}
	})
}

// TestWindowGrammarRefusals is AC4's refusing side: every witness the spec names, on both variables.
// A window is a base-10 whole number of SECONDS — a duration string is not this grammar, and being
// coerced to something plausible is the silent-wrong-default the fail-safe exists to prevent.
func TestWindowGrammarRefusals(t *testing.T) {
	witnesses := []struct {
		value string
		why   string
	}{
		{"2m", "a unit suffix is not the grammar"},
		{"120s", "a unit suffix is not the grammar, even the right unit"},
		{"1.5", "a window is whole seconds"},
		{"abc", "not a number at all"},
		{"0", "not positive"},
		{"-5", "not positive"},
		{"99999999999999999999", "whole and positive but outside the representable range"},
	}

	for _, w := range witnesses {
		for _, key := range []string{EnvLiveWindowSeconds, EnvStaleWindowSeconds} {
			t.Run(key+"="+w.value, func(t *testing.T) {
				clearWindows(t)
				t.Setenv(key, w.value)

				got, err := Load()
				if err == nil {
					t.Fatalf("%s=%q started (%s); want a refusal. Config: %+v", key, w.value, w.why, got)
				}
				if got != nil {
					t.Fatalf("returned a Config alongside an error: %+v", got)
				}
				if !strings.Contains(err.Error(), key) {
					t.Errorf("refusal %q does not name the offending variable %s", err, key)
				}
				// The constraint violated must be named too, so the message is actionable without
				// reading the source.
				if !strings.Contains(err.Error(), "seconds") {
					t.Errorf("refusal %q does not name the constraint (a whole number of seconds)", err)
				}
			})
		}
	}
}

// TestWindowGrammarAcceptances is AC4's starting side: a bare number, a number with surrounding
// whitespace, the empty string, and a whitespace-only value. The last two take the default — the F21
// ruling — and must never be read as an invalid value.
func TestWindowGrammarAcceptances(t *testing.T) {
	accepted := []struct {
		value    string
		wantLive int64
	}{
		{"120", 120},
		{"  120  ", 120},
		{"", DefaultLiveWindowSeconds},
		{"   ", DefaultLiveWindowSeconds},
	}

	for _, a := range accepted {
		t.Run("live="+strings.ReplaceAll(a.value, " ", "_"), func(t *testing.T) {
			clearWindows(t)
			t.Setenv(EnvLiveWindowSeconds, a.value)

			c, err := Load()
			if err != nil {
				t.Fatalf("%s=%q refused the start: %v", EnvLiveWindowSeconds, a.value, err)
			}
			if c.LiveWindowSeconds != a.wantLive {
				t.Fatalf("%s=%q gave %d seconds, want %d", EnvLiveWindowSeconds, a.value, c.LiveWindowSeconds, a.wantLive)
			}
		})
	}
}

// TestWindowOrderingRefusal is AC5 on an explicitly configured pair, including the equal case: equal
// windows leave `recent` unreachable just as surely as an inverted pair does.
func TestWindowOrderingRefusal(t *testing.T) {
	for _, pair := range [][2]string{{"900", "120"}, {"300", "300"}} {
		t.Run(pair[0]+"/"+pair[1], func(t *testing.T) {
			clearWindows(t)
			t.Setenv(EnvLiveWindowSeconds, pair[0])
			t.Setenv(EnvStaleWindowSeconds, pair[1])

			got, err := Load()
			if err == nil {
				t.Fatalf("windows (%s, %s) started; want a refusal. Config: %+v", pair[0], pair[1], got)
			}
			for _, want := range []string{pair[0], pair[1], "seconds"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal %q does not report %q", err, want)
				}
			}
		})
	}
}

// TestLogEffectiveWindows is AC6: a started server records both EFFECTIVE values, in seconds, under
// the variable names an operator would set. The defaulted case is the one that matters — an operator
// who set nothing still has to be able to read which windows are running.
func TestLogEffectiveWindows(t *testing.T) {
	clearWindows(t)
	t.Setenv(EnvLiveWindowSeconds, "45")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	var buf bytes.Buffer
	c.LogEffectiveWindows(slog.New(slog.NewJSONHandler(&buf, nil)))

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("start-up log line is not JSON: %v (%q)", err, buf.String())
	}
	if got, ok := rec[EnvLiveWindowSeconds].(float64); !ok || int64(got) != 45 {
		t.Errorf("start-up log %s = %v, want 45", EnvLiveWindowSeconds, rec[EnvLiveWindowSeconds])
	}
	if got, ok := rec[EnvStaleWindowSeconds].(float64); !ok || int64(got) != DefaultStaleWindowSeconds {
		t.Errorf("start-up log %s = %v, want the default %d",
			EnvStaleWindowSeconds, rec[EnvStaleWindowSeconds], DefaultStaleWindowSeconds)
	}
	if rec["unit"] != "seconds" {
		t.Errorf("start-up log does not say the unit is seconds: %q", buf.String())
	}
}
