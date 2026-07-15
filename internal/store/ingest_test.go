// The S2 ingestion tests. Like the rest of this package they run against a REAL PostGIS: the
// idempotency is enforced by a partitioned-table ON CONFLICT and the mitigation creates a real
// partition, neither of which a mock could prove.
//
// They share ONE container across subtests, and TestIngestStore is deliberately NOT t.Parallel():
// it runs in the sequential phase, so it adds a single container to this package's footprint rather
// than one per case, while the spatial bedrock tests keep their per-test isolation. The subtests
// cannot interfere because each uses its own device (family-scoped) and its own DISTINCT month —
// the partition assertions name specific months, never a global count.
package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/NSchatz/tracker/internal/db"
)

func TestIngestStore(t *testing.T) {
	ctx := context.Background()
	pool := setup(ctx, t)

	t.Run("upsert is idempotent on (device_id, ts)", func(t *testing.T) {
		_, deviceID := seedDevice(ctx, t, pool, "idempotent")
		base := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
		mustEnsurePartition(ctx, t, pool, base)

		fix := Fix{DeviceID: deviceID, TS: base, Lon: romeLon, Lat: romeLat}

		inserted, err := UpsertFix(ctx, pool, fix)
		if err != nil {
			t.Fatalf("first UpsertFix: %v", err)
		}
		if !inserted {
			t.Fatal("the first UpsertFix reported the fix already existed")
		}

		// The server's receive-time is stamped, and it is NOT the device's ts.
		received := scalar[time.Time](ctx, t, pool, `SELECT received_at FROM fixes WHERE device_id = $1`, deviceID)
		if received.Equal(base) {
			t.Fatal("received_at equals the device ts; the server must stamp its own clock (§5.2)")
		}

		// The replay: same (device_id, ts), even with a DIFFERENT payload, is absorbed. The first
		// report wins; the second changes nothing and — critically — is not an error.
		replay := fix
		replay.Lon, replay.Lat = laLon, laLat
		inserted, err = UpsertFix(ctx, pool, replay)
		if err != nil {
			t.Fatalf("replayed UpsertFix returned an error; a replay must be a silent no-op: %v", err)
		}
		if inserted {
			t.Fatal("the replay reported itself as a new row; (device_id, ts) dedup is not holding")
		}
		if n := scalar[int64](ctx, t, pool, `SELECT count(*) FROM fixes WHERE device_id = $1`, deviceID); n != 1 {
			t.Fatalf("after a replay the device has %d fixes, want 1 — the replay was stored as a duplicate", n)
		}
		gotLon := scalar[float64](ctx, t, pool, `SELECT ST_X(location::geometry) FROM fixes WHERE device_id = $1`, deviceID)
		assertClose(t, "the stored longitude after a replay", gotLon, romeLon, 1e-9)

		// An OUT-OF-ORDER report (an earlier ts arriving after a later one) is a distinct fix.
		earlier := fix
		earlier.TS = base.Add(-time.Hour)
		inserted, err = UpsertFix(ctx, pool, earlier)
		if err != nil {
			t.Fatalf("out-of-order UpsertFix: %v", err)
		}
		if !inserted {
			t.Fatal("an earlier, distinct ts was treated as a duplicate")
		}
		if n := scalar[int64](ctx, t, pool, `SELECT count(*) FROM fixes WHERE device_id = $1`, deviceID); n != 2 {
			t.Fatalf("after an out-of-order fix the device has %d fixes, want 2", n)
		}
	})

	t.Run("ingest provisions a missing partition (constraint #1 mitigation)", func(t *testing.T) {
		// A DISTINCT month (September) that no other subtest provisions, so its partition is
		// genuinely absent here. This is S1's partition-lookahead trap: a fix for a month the
		// server never provisioned would otherwise be rejected — silent, total data loss for a
		// long-lived process — and the mitigation must recover it.
		_, deviceID := seedDevice(ctx, t, pool, "missing-partition")
		ts := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

		if partitionExists(ctx, t, pool, "fixes_2026_09") {
			t.Fatal("September is already provisioned; this test needs it absent to prove the mitigation")
		}
		// A bare UpsertFix here fails — that IS the trap. Prove it, so the mitigation is not tested
		// against an already-open door.
		if _, err := UpsertFix(ctx, pool, Fix{DeviceID: deviceID, TS: ts, Lon: romeLon, Lat: romeLat}); !isMissingPartitionErr(err) {
			t.Fatalf("UpsertFix into an unprovisioned month = %v; expected a missing-partition error (the trap)", err)
		}

		inserted, err := IngestFix(ctx, pool, Fix{DeviceID: deviceID, TS: ts, Lon: romeLon, Lat: romeLat}, ts)
		if err != nil {
			t.Fatalf("IngestFix did not recover from a missing partition: %v", err)
		}
		if !inserted {
			t.Fatal("IngestFix reported the fix already existed")
		}
		if !partitionExists(ctx, t, pool, "fixes_2026_09") {
			t.Fatal("IngestFix stored the fix but did not create the month's partition")
		}
		if n := scalar[int64](ctx, t, pool, `SELECT count(*) FROM fixes WHERE device_id = $1`, deviceID); n != 1 {
			t.Fatalf("after the mitigation the device has %d fixes, want 1", n)
		}
		// Still idempotent through the mitigation path — the partition now exists, so the replay dedups.
		inserted, err = IngestFix(ctx, pool, Fix{DeviceID: deviceID, TS: ts, Lon: romeLon, Lat: romeLat}, ts)
		if err != nil {
			t.Fatalf("second IngestFix: %v", err)
		}
		if inserted {
			t.Fatal("a replay through the mitigation path was stored as a new row")
		}
	})

	t.Run("ingest rejects a ts outside the window and provisions nothing for it", func(t *testing.T) {
		_, deviceID := seedDevice(ctx, t, pool, "ts-window")
		now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

		for _, c := range []struct {
			name string
			ts   time.Time
		}{
			{"far in the future", now.Add(48 * time.Hour)},
			{"far in the past", now.Add(-MaxIngestBacklog - 24*time.Hour)},
			{"the epoch (a missing/garbage ts)", time.Unix(0, 0)},
		} {
			_, err := IngestFix(ctx, pool, Fix{DeviceID: deviceID, TS: c.ts, Lon: romeLon, Lat: romeLat}, now)
			if !errors.Is(err, ErrTimestampOutOfWindow) {
				t.Fatalf("IngestFix(%s, ts=%s) = %v; want ErrTimestampOutOfWindow", c.name, c.ts, err)
			}
		}
		// Nothing landed for this device, and — the point of the bound — the epoch's month (which no
		// legitimate path would ever touch) was NOT provisioned by the rejected fix.
		if n := scalar[int64](ctx, t, pool, `SELECT count(*) FROM fixes WHERE device_id = $1`, deviceID); n != 0 {
			t.Fatalf("a rejected-timestamp fix left %d rows behind", n)
		}
		if partitionExists(ctx, t, pool, "fixes_1970_01") {
			t.Fatal("a rejected far-past fix provisioned a 1970 partition; the window must reject before any DDL")
		}

		// The near edge of the backlog window is accepted (and provisions its own distinct month).
		edge := now.Add(-MaxIngestBacklog + time.Hour) // ~2026-04-15
		if _, err := IngestFix(ctx, pool, Fix{DeviceID: deviceID, TS: edge, Lon: romeLon, Lat: romeLat}, now); err != nil {
			t.Fatalf("IngestFix at the near edge of the backlog window = %v; want it accepted", err)
		}
	})

	t.Run("authenticate device resolves the token to its device and family", func(t *testing.T) {
		familyID, err := CreateFamily(ctx, pool, "auth-family")
		if err != nil {
			t.Fatalf("CreateFamily: %v", err)
		}
		digest := sha256.Sum256([]byte("synthetic-device-token-not-a-secret"))
		deviceID, err := CreateDevice(ctx, pool, familyID, "phone", digest[:])
		if err != nil {
			t.Fatalf("CreateDevice: %v", err)
		}

		dev, err := AuthenticateDevice(ctx, pool, digest[:])
		if err != nil {
			t.Fatalf("AuthenticateDevice: %v", err)
		}
		if dev.ID != deviceID {
			t.Fatalf("authenticated device id = %s, want %s", dev.ID, deviceID)
		}
		if dev.FamilyID != familyID {
			t.Fatalf("authenticated device family = %s, want %s — authz scoping depends on this", dev.FamilyID, familyID)
		}

		other := sha256.Sum256([]byte("a-different-token"))
		if _, err := AuthenticateDevice(ctx, pool, other[:]); !errors.Is(err, ErrUnknownToken) {
			t.Fatalf("AuthenticateDevice(unknown) = %v; want ErrUnknownToken", err)
		}
		// A raw token (wrong length) is refused before any SQL — a nil Querier proves it.
		if _, err := AuthenticateDevice(ctx, nil, []byte("a-raw-43-char-token-not-a-32-byte-digest!!!")); !errors.Is(err, ErrInvalidTokenHash) {
			t.Fatalf("AuthenticateDevice(raw token) = %v; want ErrInvalidTokenHash", err)
		}
	})
}

// partitionExists reports whether a partition of `fixes` with the given name is present.
func partitionExists(ctx context.Context, t *testing.T, pool db.Querier, name string) bool {
	t.Helper()
	var present bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&present); err != nil {
		t.Fatalf("check whether %s exists: %v", name, err)
	}
	return present
}
