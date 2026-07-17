// Token expiry and rotation (S7, roadmap §7). These run against a REAL PostGIS because the
// enforcement lives in SQL — the (expires_at IS NULL OR expires_at > now()) predicate in the
// authentication lookup, decided by the DATABASE's clock — and a mock would only assert the Go we
// already wrote, not that Postgres applies the predicate.
package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"
)

// zeroUUID is a well-formed uuid that no row ever has, for the "rotate an id that does not exist"
// case — it must report not-found, never silently succeed over zero rows.
const zeroUUID = "00000000-0000-0000-0000-000000000000"

func TestDeviceTokenExpiryAndRotation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool := setup(ctx, t)

	name := "expiry-device"
	_, deviceID := seedDevice(ctx, t, pool, name)
	origHash := sha256.Sum256([]byte(name)) // seedDevice hashes the name

	// The freshly enrolled token (no expiry) authenticates.
	if _, err := AuthenticateDevice(ctx, pool, origHash[:]); err != nil {
		t.Fatalf("fresh token: AuthenticateDevice = %v, want ok", err)
	}

	// Rotate to a token that is ALREADY expired.
	expiredHash := sha256.Sum256([]byte("rotated-expired"))
	past := time.Now().Add(-time.Hour)
	found, err := RotateDeviceToken(ctx, pool, deviceID, expiredHash[:], &past)
	if err != nil || !found {
		t.Fatalf("RotateDeviceToken(past) = (found=%v, %v), want (true, nil)", found, err)
	}
	// The expired token does not authenticate — and is indistinguishable from an unknown one.
	if _, err := AuthenticateDevice(ctx, pool, expiredHash[:]); !errors.Is(err, ErrUnknownToken) {
		t.Fatalf("expired token: AuthenticateDevice = %v, want ErrUnknownToken", err)
	}
	// The ORIGINAL token is dead too: rotation replaced it, so it matches no row.
	if _, err := AuthenticateDevice(ctx, pool, origHash[:]); !errors.Is(err, ErrUnknownToken) {
		t.Fatalf("rotated-away token: AuthenticateDevice = %v, want ErrUnknownToken", err)
	}

	// Rotate to a token valid for another hour: it authenticates and resolves to the same device.
	liveHash := sha256.Sum256([]byte("rotated-live"))
	future := time.Now().Add(time.Hour)
	if found, err := RotateDeviceToken(ctx, pool, deviceID, liveHash[:], &future); err != nil || !found {
		t.Fatalf("RotateDeviceToken(future) = (found=%v, %v), want (true, nil)", found, err)
	}
	dev, err := AuthenticateDevice(ctx, pool, liveHash[:])
	if err != nil {
		t.Fatalf("unexpired token: AuthenticateDevice = %v, want ok", err)
	}
	if dev.ID != deviceID {
		t.Fatalf("authenticated device id = %s, want %s", dev.ID, deviceID)
	}

	// Rotate to a never-expiring token (nil expiry).
	foreverHash := sha256.Sum256([]byte("rotated-forever"))
	if found, err := RotateDeviceToken(ctx, pool, deviceID, foreverHash[:], nil); err != nil || !found {
		t.Fatalf("RotateDeviceToken(nil) = (found=%v, %v), want (true, nil)", found, err)
	}
	if _, err := AuthenticateDevice(ctx, pool, foreverHash[:]); err != nil {
		t.Fatalf("never-expiring token: AuthenticateDevice = %v, want ok", err)
	}

	// Rotating an id that does not exist reports not-found rather than succeeding over nothing.
	if found, err := RotateDeviceToken(ctx, pool, zeroUUID, foreverHash[:], nil); err != nil || found {
		t.Fatalf("RotateDeviceToken(unknown id) = (found=%v, %v), want (false, nil)", found, err)
	}

	// A raw token (wrong length) where the hash belongs is refused, exactly as CreateDevice refuses it.
	if _, err := RotateDeviceToken(ctx, pool, deviceID, []byte("a-raw-43-char-token-not-a-32-byte-digest!!!"), nil); !errors.Is(err, ErrInvalidTokenHash) {
		t.Fatalf("RotateDeviceToken(raw token) = %v, want ErrInvalidTokenHash", err)
	}
}

func TestViewerTokenExpiryAndRotation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	pool := setup(ctx, t)

	familyID, _ := seedDevice(ctx, t, pool, "viewer-expiry")
	vHash := sha256.Sum256([]byte("viewer-token"))
	viewerID, err := CreateViewer(ctx, pool, familyID, "v@example.test", "V", vHash[:])
	if err != nil {
		t.Fatalf("CreateViewer: %v", err)
	}

	// Fresh viewer token authenticates.
	if _, err := AuthenticateViewer(ctx, pool, vHash[:]); err != nil {
		t.Fatalf("fresh viewer token: AuthenticateViewer = %v, want ok", err)
	}

	// Rotate to an expired token: it stops authenticating, and so does the old one.
	expiredHash := sha256.Sum256([]byte("viewer-expired"))
	past := time.Now().Add(-time.Hour)
	if found, err := RotateViewerToken(ctx, pool, viewerID, expiredHash[:], &past); err != nil || !found {
		t.Fatalf("RotateViewerToken(past) = (found=%v, %v), want (true, nil)", found, err)
	}
	if _, err := AuthenticateViewer(ctx, pool, expiredHash[:]); !errors.Is(err, ErrUnknownToken) {
		t.Fatalf("expired viewer token: AuthenticateViewer = %v, want ErrUnknownToken", err)
	}
	if _, err := AuthenticateViewer(ctx, pool, vHash[:]); !errors.Is(err, ErrUnknownToken) {
		t.Fatalf("rotated-away viewer token: AuthenticateViewer = %v, want ErrUnknownToken", err)
	}

	// Rotate to a live token: authenticates and resolves to the same viewer/family.
	liveHash := sha256.Sum256([]byte("viewer-live"))
	future := time.Now().Add(time.Hour)
	if found, err := RotateViewerToken(ctx, pool, viewerID, liveHash[:], &future); err != nil || !found {
		t.Fatalf("RotateViewerToken(future) = (found=%v, %v), want (true, nil)", found, err)
	}
	v, err := AuthenticateViewer(ctx, pool, liveHash[:])
	if err != nil {
		t.Fatalf("unexpired viewer token: AuthenticateViewer = %v, want ok", err)
	}
	if v.ID != viewerID || v.FamilyID != familyID {
		t.Fatalf("authenticated viewer = %+v, want id=%s family=%s", v, viewerID, familyID)
	}
}
