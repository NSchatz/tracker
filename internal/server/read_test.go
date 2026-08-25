// The S3 read API driven through the real HTTP handler against a real PostGIS — the authz matrix
// the roadmap's acceptance requires, plus the read-query correctness (ordering, windowing,
// pagination, scoping) beneath it. Every subtest here shares ONE container and isolates by FAMILY,
// exactly as ingest_test.go does: family is the authorization boundary, so distinct families cannot
// interfere, and one PostGIS serves the whole read suite rather than one per case.
package server_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/NSchatz/tracker/internal/auth"
	"github.com/NSchatz/tracker/internal/store"
)

// --- read-suite helpers on the shared harness ----------------------------------------------------

// addViewer enrolls a viewer into a family and returns its freshly issued read token — the
// operator's add-viewer path, in-process. The email is derived from the label so each is unique.
func (h *harness) addViewer(t *testing.T, familyID, label string) auth.Token {
	t.Helper()
	token, err := auth.Generate()
	if err != nil {
		t.Fatalf("Generate token: %v", err)
	}
	hash := token.Hash()
	if _, err := store.CreateViewer(context.Background(), h.pool, familyID, label+"@example.test", label, hash[:]); err != nil {
		t.Fatalf("CreateViewer: %v", err)
	}
	return token
}

// familyOf returns the family id a device was enrolled under — the harness's enroll makes a fresh
// family per device, and the read tests need that id to add a viewer to the same family.
func (h *harness) familyOf(t *testing.T, deviceID string) string {
	t.Helper()
	dev, err := store.DeviceByID(context.Background(), h.pool, deviceID)
	if err != nil {
		t.Fatalf("DeviceByID: %v", err)
	}
	return dev.FamilyID
}

// makeFamilyWithDevice creates a fresh family and a device in it, returning both ids. Fresh family =
// isolated authorization boundary, so subtests cannot see one another's rows on the shared pool.
func (h *harness) makeFamilyWithDevice(t *testing.T, label string) (familyID, deviceID string) {
	t.Helper()
	ctx := context.Background()
	familyID, err := store.CreateFamily(ctx, h.pool, t.Name()+"/"+label)
	if err != nil {
		t.Fatalf("CreateFamily: %v", err)
	}
	hash := sha256.Sum256([]byte(t.Name() + "/" + label))
	deviceID, err = store.CreateDevice(ctx, h.pool, familyID, label+"-phone", hash[:])
	if err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}
	return familyID, deviceID
}

// get sends a GET with an optional Bearer token and returns the recorder.
func (h *harness) get(t *testing.T, path string, token auth.Token) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+string(token))
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

// seedFix posts a valid fix through the real ingest path, asserting it stored, so an HTTP read test
// has data to read.
func (h *harness) seedFix(t *testing.T, token auth.Token, lat, lon float64, ts int64) {
	t.Helper()
	body := fmt.Sprintf(`{"lat":%f,"lon":%f,"ts":%d}`, lat, lon, ts)
	if rec := h.post(t, "/v1/fixes", token, body); rec.Code != http.StatusCreated {
		t.Fatalf("seed fix = %d (%s), want 201", rec.Code, rec.Body.String())
	}
}

// ingest stores one fix for a device at a chosen ts via the real ingest path (which provisions the
// month's partition on demand), so the query tests have controlled, timestamped data.
func (h *harness) ingest(t *testing.T, deviceID string, ts time.Time, lon, lat float64) {
	t.Helper()
	if _, err := store.IngestFix(context.Background(), h.pool, store.Fix{
		DeviceID: deviceID, TS: ts, Lon: lon, Lat: lat,
	}, time.Now()); err != nil {
		t.Fatalf("IngestFix at %s: %v", ts.Format(time.RFC3339), err)
	}
}

// TestReadAPI is the whole S3 read suite on one shared container: the authz matrix over HTTP, and
// the query-level correctness (ordering / windowing / pagination / scoping) beneath it.
func TestReadAPI(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	t.Run("authz matrix", func(t *testing.T) { readAuthzMatrix(t, h) })
	t.Run("positions query", func(t *testing.T) { positionsQuery(t, h) })
	t.Run("history query", func(t *testing.T) { historyQuery(t, h) })
	t.Run("CreateViewer rejects a raw token", func(t *testing.T) { createViewerRejectsRawToken(t, h) })
}

// readAuthzMatrix is the roadmap's acceptance: a viewer reads only its own family, a cross-family
// read is a 403, and the read/write credential separation holds in both directions.
func readAuthzMatrix(t *testing.T, h *harness) {
	// Two families, each with a device (that reports a fix) and a viewer.
	devA, tokADevice := h.enroll(t, "family-a-device")
	famA := h.familyOf(t, devA)
	viewerA := h.addViewer(t, famA, "viewer-a")

	devB, tokBDevice := h.enroll(t, "family-b-device")
	famB := h.familyOf(t, devB)
	viewerB := h.addViewer(t, famB, "viewer-b")

	ts := nowTS()
	h.seedFix(t, tokADevice, 41.9028, 12.4964, ts) // A near Rome
	h.seedFix(t, tokBDevice, 48.8566, 2.3522, ts)  // B near Paris

	t.Run("positions: a viewer sees only its own family", func(t *testing.T) {
		rec := h.get(t, "/v1/positions", viewerA)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /v1/positions (viewer A) = %d (%s), want 200", rec.Code, rec.Body.String())
		}
		var got []struct {
			DeviceID string `json:"device_id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("positions body not JSON: %v (%q)", err, rec.Body.String())
		}
		if len(got) != 1 || got[0].DeviceID != devA {
			t.Fatalf("viewer A's positions = %+v, want exactly device A (%s)", got, devA)
		}
		for _, p := range got {
			if p.DeviceID == devB {
				t.Fatal("viewer A saw family B's device in /v1/positions — a cross-family leak")
			}
		}
	})

	t.Run("positions: an empty family is [] not null", func(t *testing.T) {
		famC, err := store.CreateFamily(context.Background(), h.pool, t.Name()+"/empty")
		if err != nil {
			t.Fatalf("CreateFamily: %v", err)
		}
		viewerC := h.addViewer(t, famC, "viewer-c")
		rec := h.get(t, "/v1/positions", viewerC)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /v1/positions (empty family) = %d, want 200", rec.Code)
		}
		if got := rec.Body.String(); got != "[]\n" && got != "[]" {
			t.Fatalf("empty family positions body = %q, want an empty array", got)
		}
	})

	t.Run("positions: no token, unknown token, and a DEVICE token are all 401", func(t *testing.T) {
		if rec := h.get(t, "/v1/positions", ""); rec.Code != http.StatusUnauthorized {
			t.Fatalf("no token = %d, want 401", rec.Code)
		}
		if rec := h.get(t, "/v1/positions", auth.Token("not-a-real-token")); rec.Code != http.StatusUnauthorized {
			t.Fatalf("unknown token = %d, want 401", rec.Code)
		}
		// A device token authenticates writes, never reads: on a read route it matches no viewer and
		// is refused. This is §7's separation in the write→read direction.
		if rec := h.get(t, "/v1/positions", tokADevice); rec.Code != http.StatusUnauthorized {
			t.Fatalf("a device token on a read route = %d, want 401", rec.Code)
		}
	})

	t.Run("a VIEWER token cannot write fixes", func(t *testing.T) {
		// The other direction of the separation: a viewer's read token is not a device.
		body := fmt.Sprintf(`{"lat":41.9,"lon":12.5,"ts":%d}`, ts)
		if rec := h.post(t, "/v1/fixes", viewerA, body); rec.Code != http.StatusUnauthorized {
			t.Fatalf("a viewer token on POST /v1/fixes = %d, want 401", rec.Code)
		}
	})

	t.Run("history: a viewer reads its own device", func(t *testing.T) {
		rec := h.get(t, "/v1/devices/"+devA+"/history", viewerA)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET history (own device) = %d (%s), want 200", rec.Code, rec.Body.String())
		}
		var got []struct {
			Lon float64 `json:"lon"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("history body not JSON: %v (%q)", err, rec.Body.String())
		}
		if len(got) != 1 {
			t.Fatalf("own-device history returned %d fixes, want 1", len(got))
		}
		if got[0].Lon < 12.49 || got[0].Lon > 12.50 {
			t.Fatalf("history fix lon = %v, want ~12.4964 (axis order survived the read path)", got[0].Lon)
		}
	})

	t.Run("history: a cross-family read is 403, never the data", func(t *testing.T) {
		if rec := h.get(t, "/v1/devices/"+devA+"/history", viewerB); rec.Code != http.StatusForbidden {
			t.Fatalf("viewer B reading device A's history = %d (%s), want 403", rec.Code, rec.Body.String())
		}
		if rec := h.get(t, "/v1/devices/"+devB+"/history", viewerA); rec.Code != http.StatusForbidden {
			t.Fatalf("viewer A reading device B's history = %d, want 403", rec.Code)
		}
	})

	t.Run("history: an unknown device is 404 and a non-UUID id is 400", func(t *testing.T) {
		// A well-formed UUID that is no device: 404, distinct from the 403 for a real device in
		// another family. (Device ids are unguessable UUIDs, so this split leaks nothing usable.)
		const noSuchDevice = "00000000-0000-0000-0000-000000000000"
		if rec := h.get(t, "/v1/devices/"+noSuchDevice+"/history", viewerA); rec.Code != http.StatusNotFound {
			t.Fatalf("unknown device history = %d, want 404", rec.Code)
		}
		if rec := h.get(t, "/v1/devices/not-a-uuid/history", viewerA); rec.Code != http.StatusBadRequest {
			t.Fatalf("non-UUID device id = %d, want 400", rec.Code)
		}
	})

	t.Run("history: pagination and a garbage param", func(t *testing.T) {
		// A second, older fix so there are two to page through.
		h.seedFix(t, tokADevice, 41.90, 12.49, ts-600)
		rec := h.get(t, "/v1/devices/"+devA+"/history?limit=1", viewerA)
		if rec.Code != http.StatusOK {
			t.Fatalf("history?limit=1 = %d, want 200", rec.Code)
		}
		var page []struct {
			TS int64 `json:"ts"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatalf("history body not JSON: %v", err)
		}
		if len(page) != 1 || page[0].TS != ts {
			t.Fatalf("limit=1 first page = %+v, want exactly the newest fix (ts %d)", page, ts)
		}
		if rec := h.get(t, "/v1/devices/"+devA+"/history?limit=abc", viewerA); rec.Code != http.StatusBadRequest {
			t.Fatalf("history?limit=abc = %d, want 400", rec.Code)
		}
	})

	t.Run("near: a viewer finds its own family and not another's", func(t *testing.T) {
		rec := h.get(t, "/v1/near?lat=41.9028&lon=12.4964&m=1000", viewerA)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /v1/near = %d (%s), want 200", rec.Code, rec.Body.String())
		}
		var got []struct {
			DeviceID string `json:"device_id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("near body not JSON: %v (%q)", err, rec.Body.String())
		}
		if len(got) == 0 {
			t.Fatal("near Rome found nothing, but device A is there")
		}
		for _, n := range got {
			if n.DeviceID == devB {
				t.Fatal("viewer A's /v1/near returned family B's device — a cross-family leak")
			}
		}
		// Viewer B, searching the same Rome point, finds nothing of its own (its device is in Paris).
		recB := h.get(t, "/v1/near?lat=41.9028&lon=12.4964&m=1000", viewerB)
		if recB.Code != http.StatusOK {
			t.Fatalf("GET /v1/near (viewer B) = %d, want 200", recB.Code)
		}
		if body := recB.Body.String(); body != "[]\n" && body != "[]" {
			t.Fatalf("viewer B near Rome = %q, want [] (its device is in Paris)", body)
		}
	})

	t.Run("near: missing or malformed params are 400", func(t *testing.T) {
		for _, q := range []string{
			"/v1/near?lon=12.5&m=100",         // missing lat
			"/v1/near?lat=41.9&m=100",         // missing lon
			"/v1/near?lat=41.9&lon=12.5",      // missing m
			"/v1/near?lat=41.9&lon=12.5&m=-5", // negative radius
			"/v1/near?lat=abc&lon=12.5&m=100", // non-numeric lat
			"/v1/near?lat=200&lon=12.5&m=100", // out-of-range lat (PostGIS would coerce it)
		} {
			if rec := h.get(t, q, viewerA); rec.Code != http.StatusBadRequest {
				t.Fatalf("GET %s = %d, want 400", q, rec.Code)
			}
		}
	})
}

// positionsQuery pins the query behind GET /v1/positions — now store.FamilyDeviceStates, which
// enumerates DEVICES rather than fixes: which fix is current (the newest by the device's `ts`), that
// the server's receive-time rides along, that a device holding no fix still produces a row, and that
// no other family's device or fix can ever appear. Asserted directly against the store on the shared
// container.
func positionsQuery(t *testing.T, h *harness) {
	ctx := context.Background()
	familyID, d1 := h.makeFamilyWithDevice(t, "alice")
	base := time.Now().Truncate(time.Second).Add(-time.Hour) // inside the ingest window

	t.Run("a device with no fixes still appears, with neither half", func(t *testing.T) {
		got, err := store.FamilyDeviceStates(ctx, h.pool, familyID)
		if err != nil {
			t.Fatalf("FamilyDeviceStates: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("got %d device states, want 1 (the device exists; it has just never reported)", len(got))
		}
		if got[0].Current != nil || got[0].LastContact != nil {
			t.Fatalf("a never-reported device came back with a position or a last contact: %+v", got[0])
		}
	})

	// Three fixes for d1; the newest (base+2m, at Rome) must win — not the last inserted (base+1m).
	h.ingest(t, d1, base, 12.0, 41.0)
	h.ingest(t, d1, base.Add(2*time.Minute), 12.4964, 41.9028)
	h.ingest(t, d1, base.Add(time.Minute), 12.2, 41.2)

	t.Run("the latest fix by ts is the current position", func(t *testing.T) {
		got, err := store.FamilyDeviceStates(ctx, h.pool, familyID)
		if err != nil {
			t.Fatalf("FamilyDeviceStates: %v", err)
		}
		if len(got) != 1 || got[0].Current == nil {
			t.Fatalf("got %+v, want one device with a current position", got)
		}
		if !got[0].Current.TS.Equal(base.Add(2 * time.Minute)) {
			t.Fatalf("current ts = %s, want the newest fix %s", got[0].Current.TS, base.Add(2*time.Minute))
		}
		if got[0].Current.Lon < 12.49 || got[0].Current.Lon > 12.50 {
			t.Fatalf("current lon = %v, want ~12.4964 (axis order survived)", got[0].Current.Lon)
		}
		if got[0].Current.ReceivedAt.IsZero() {
			t.Fatal("received_at is zero; the server's receive-time must ride along")
		}
		if got[0].LastContact == nil || got[0].LastContact.IsZero() {
			t.Fatal("last contact is absent; a device holding fixes has a max(received_at)")
		}
	})

	// A second device in the SAME family.
	zhash := sha256.Sum256([]byte(t.Name() + "/zack"))
	d2, err := store.CreateDevice(ctx, h.pool, familyID, "zack-phone", zhash[:])
	if err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}
	h.ingest(t, d2, base, 12.5, 41.5)

	t.Run("every device in the family comes back", func(t *testing.T) {
		got, err := store.FamilyDeviceStates(ctx, h.pool, familyID)
		if err != nil {
			t.Fatalf("FamilyDeviceStates: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d device states, want 2", len(got))
		}
		ids := map[string]bool{}
		for _, s := range got {
			ids[s.DeviceID] = true
		}
		if !ids[d1] || !ids[d2] {
			t.Fatalf("device states %+v do not cover both devices", got)
		}
	})

	t.Run("another family's fixes never appear", func(t *testing.T) {
		otherFamily, other := h.makeFamilyWithDevice(t, "intruder")
		h.ingest(t, other, base, 12.9, 41.9)

		got, err := store.FamilyDeviceStates(ctx, h.pool, familyID)
		if err != nil {
			t.Fatalf("FamilyDeviceStates: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("another family got a device and this family now reports %d states, want 2 — scope leaked", len(got))
		}
		for _, s := range got {
			if s.DeviceID == other {
				t.Fatal("another family's device appeared in this family's device states")
			}
		}
		otherGot, err := store.FamilyDeviceStates(ctx, h.pool, otherFamily)
		if err != nil {
			t.Fatalf("FamilyDeviceStates(other): %v", err)
		}
		if len(otherGot) != 1 || otherGot[0].DeviceID != other {
			t.Fatalf("the other family sees %d states, want exactly its own device", len(otherGot))
		}
	})
}

// historyQuery pins GET /v1/devices/{id}/history's query: newest-first, half-open window, the
// pagination seam, the limit clamp, and family scoping as defence in depth.
func historyQuery(t *testing.T, h *harness) {
	ctx := context.Background()
	familyID, deviceID := h.makeFamilyWithDevice(t, "history")
	base := time.Now().Truncate(time.Second).Add(-2 * time.Hour)
	const n = 10
	ts := make([]time.Time, n)
	for i := range ts {
		ts[i] = base.Add(time.Duration(i) * time.Minute) // ts[0] oldest … ts[9] newest
		h.ingest(t, deviceID, ts[i], 12.0+float64(i)*0.001, 41.0)
	}
	var unbounded time.Time

	t.Run("full history is newest-first", func(t *testing.T) {
		got, err := store.DeviceHistory(ctx, h.pool, familyID, deviceID, unbounded, unbounded, 100, 0)
		if err != nil {
			t.Fatalf("DeviceHistory: %v", err)
		}
		if len(got) != n {
			t.Fatalf("got %d fixes, want %d", len(got), n)
		}
		for i := 1; i < len(got); i++ {
			if got[i-1].TS.Before(got[i].TS) {
				t.Fatalf("history not newest-first: %s before %s", got[i-1].TS, got[i].TS)
			}
		}
		if !got[0].TS.Equal(ts[n-1]) {
			t.Fatalf("first row ts = %s, want newest %s", got[0].TS, ts[n-1])
		}
	})

	t.Run("a window is half-open [from, to)", func(t *testing.T) {
		// [ts2, ts5) selects ts2, ts3, ts4 — inclusive at from, exclusive at to.
		got, err := store.DeviceHistory(ctx, h.pool, familyID, deviceID, ts[2], ts[5], 100, 0)
		if err != nil {
			t.Fatalf("DeviceHistory: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("window [ts2, ts5) returned %d fixes, want 3", len(got))
		}
		if !got[0].TS.Equal(ts[4]) || !got[2].TS.Equal(ts[2]) {
			t.Fatalf("window rows = [%s .. %s], want [ts4 .. ts2] newest-first", got[0].TS, got[2].TS)
		}
	})

	t.Run("pagination walks without gaps or overlap", func(t *testing.T) {
		page1, err := store.DeviceHistory(ctx, h.pool, familyID, deviceID, unbounded, unbounded, 4, 0)
		if err != nil {
			t.Fatalf("page1: %v", err)
		}
		page2, err := store.DeviceHistory(ctx, h.pool, familyID, deviceID, unbounded, unbounded, 4, 4)
		if err != nil {
			t.Fatalf("page2: %v", err)
		}
		if len(page1) != 4 || len(page2) != 4 {
			t.Fatalf("page sizes = %d, %d, want 4, 4", len(page1), len(page2))
		}
		// page1 = ts9..ts6, page2 = ts5..ts2: the seam must neither repeat nor skip a fix.
		if !page1[3].TS.Equal(ts[6]) || !page2[0].TS.Equal(ts[5]) {
			t.Fatalf("pagination seam wrong: page1 ends %s, page2 starts %s; want ts6 then ts5", page1[3].TS, page2[0].TS)
		}
	})

	t.Run("limit is clamped to [1, MaxHistoryLimit]", func(t *testing.T) {
		big, err := store.DeviceHistory(ctx, h.pool, familyID, deviceID, unbounded, unbounded, store.MaxHistoryLimit+9999, 0)
		if err != nil {
			t.Fatalf("huge limit: %v", err)
		}
		if len(big) != n {
			t.Fatalf("huge limit returned %d fixes, want %d", len(big), n)
		}
		one, err := store.DeviceHistory(ctx, h.pool, familyID, deviceID, unbounded, unbounded, 0, 0)
		if err != nil {
			t.Fatalf("limit 0: %v", err)
		}
		if len(one) != 1 {
			t.Fatalf("limit 0 returned %d fixes, want 1 (clamped, never the whole table)", len(one))
		}
	})

	t.Run("another family cannot read this device's history", func(t *testing.T) {
		otherFamily, _ := h.makeFamilyWithDevice(t, "history-intruder")
		// Right device id, wrong family: the family-scoped SQL returns nothing even though the device
		// and its fixes exist — the defence in depth the handler's 403 sits on top of.
		got, err := store.DeviceHistory(ctx, h.pool, otherFamily, deviceID, unbounded, unbounded, 100, 0)
		if err != nil {
			t.Fatalf("DeviceHistory(other family): %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("a device's history was readable under the wrong family: %d fixes leaked", len(got))
		}
	})
}

// createViewerRejectsRawToken pins that the viewer credential path shares the device path's guard: a
// raw token where the digest belongs is refused, so the viewers table cannot become a file of live
// read credentials.
func createViewerRejectsRawToken(t *testing.T, h *harness) {
	ctx := context.Background()
	famID, err := store.CreateFamily(ctx, h.pool, t.Name())
	if err != nil {
		t.Fatalf("CreateFamily: %v", err)
	}
	tok, err := auth.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// A 43-char token is not a 32-byte digest; CreateViewer must refuse it by length.
	if _, err := store.CreateViewer(ctx, h.pool, famID, "raw@example.test", "raw", []byte(tok)); err == nil {
		t.Fatal("CreateViewer stored a raw token as a hash; the length guard is not holding")
	}
	hash := sha256.Sum256([]byte(tok))
	if _, err := store.CreateViewer(ctx, h.pool, famID, "ok@example.test", "ok", hash[:]); err != nil {
		t.Fatalf("CreateViewer with a real digest: %v", err)
	}
}
