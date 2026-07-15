// The S2 ingestion contract, driven through the real HTTP handler against a real PostGIS. These are
// the §6 "ingestion (fail-safe)" tests: required-field acceptance, malformed → typed error and no
// row, dedup, cross-device authz, and the OwnTracks adapter end to end. They cannot run against a
// mock — the dedup is a partitioned ON CONFLICT and the store is real — so, like the rest of the
// repo, they FAIL rather than skip when Docker is missing.
//
// Unlike the spatial bedrock tests in internal/store (which take a pristine container each, because
// they EXPLAIN plans and count partitions and must not see one another's schema), these subtests
// share ONE container and isolate by FAMILY: each enrolls its own family and asserts only against
// its own devices. That is both correct — family is the authorization boundary, so distinct
// families cannot interfere — and deliberate about resource: one PostGIS for the whole HTTP suite,
// not one per case.
package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/NSchatz/tracker/internal/auth"
	"github.com/NSchatz/tracker/internal/db"
	"github.com/NSchatz/tracker/internal/server"
	"github.com/NSchatz/tracker/internal/store"
	"github.com/NSchatz/tracker/internal/testsupport"
	"github.com/jackc/pgx/v5/pgxpool"
)

// harness is a migrated PostGIS, a live handler over it, and helpers to enroll devices and post.
type harness struct {
	t       *testing.T
	pool    *pgxpool.Pool
	handler http.Handler
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()

	dsn := testsupport.NewPostGIS(t)
	if err := db.Up(ctx, dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(pool.Close)

	return &harness{t: t, pool: pool, handler: server.New(pool, discardLogger())}
}

// enroll creates a family and a device in it, returning the device id and its freshly issued token —
// exactly what the operator CLI does. The token is what the client will present. The family name is
// derived from the subtest name so each case is isolated in its own authorization boundary.
func (h *harness) enroll(t *testing.T, label string) (deviceID string, token auth.Token) {
	t.Helper()
	ctx := context.Background()

	familyID, err := store.CreateFamily(ctx, h.pool, t.Name()+"/"+label)
	if err != nil {
		t.Fatalf("CreateFamily: %v", err)
	}
	token, err = auth.Generate()
	if err != nil {
		t.Fatalf("Generate token: %v", err)
	}
	hash := token.Hash()
	deviceID, err = store.CreateDevice(ctx, h.pool, familyID, label, hash[:])
	if err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}
	return deviceID, token
}

// post sends a POST with a Bearer token and returns the recorder.
func (h *harness) post(t *testing.T, path string, token auth.Token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+string(token))
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

func (h *harness) fixCount(t *testing.T, deviceID string) int64 {
	t.Helper()
	var n int64
	if err := h.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM fixes WHERE device_id = $1`, deviceID).Scan(&n); err != nil {
		t.Fatalf("count fixes: %v", err)
	}
	return n
}

// nowTS is an epoch-seconds timestamp near the real clock, so it sits inside the ingest window.
func nowTS() int64 { return time.Now().Unix() }

// TestIngestionHTTP drives every ingestion scenario through the real handler against one shared
// PostGIS, each subtest isolated in its own family.
func TestIngestionHTTP(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	t.Run("a valid first-party report is stored", func(t *testing.T) {
		deviceID, token := h.enroll(t, "alice")
		ts := nowTS()
		body := fmt.Sprintf(`{"lat":41.9028,"lon":12.4964,"ts":%d,"accuracy":5.0,"battery":88,"speed":1.4,"trigger":"u","msg_id":"m-1"}`, ts)
		rec := h.post(t, "/v1/fixes", token, body)

		if rec.Code != http.StatusCreated {
			t.Fatalf("first POST = %d (%s), want 201", rec.Code, rec.Body.String())
		}
		if h.fixCount(t, deviceID) != 1 {
			t.Fatalf("stored %d fixes, want 1", h.fixCount(t, deviceID))
		}

		// The stored geometry is longitude-first and metres-true: assert the point is on Rome,
		// proving the axis order survived the wire → Go → SQL path; and that received_at is the
		// SERVER clock, distinct from the device ts (§5.2 liveness).
		var lon, lat float64
		var battery int16
		var received, event time.Time
		if err := h.pool.QueryRow(context.Background(), `
			SELECT ST_X(location::geometry), ST_Y(location::geometry), battery_pct, received_at, ts
			FROM fixes WHERE device_id = $1`, deviceID).Scan(&lon, &lat, &battery, &received, &event); err != nil {
			t.Fatalf("read stored fix: %v", err)
		}
		if lon < 12.49 || lon > 12.50 || lat < 41.90 || lat > 41.91 {
			t.Fatalf("stored (lon,lat) = (%v,%v), want ~ (12.4964, 41.9028) — axis order or units are wrong", lon, lat)
		}
		if battery != 88 {
			t.Fatalf("stored battery = %d, want 88", battery)
		}
		if received.Equal(event) {
			t.Fatal("received_at equals the device ts; the server must stamp its own receive-time")
		}
	})

	t.Run("re-POSTing the same (device,ts) is an idempotent no-op", func(t *testing.T) {
		deviceID, token := h.enroll(t, "bob")
		ts := nowTS()
		body := fmt.Sprintf(`{"lat":41.9,"lon":12.5,"ts":%d}`, ts)

		if rec := h.post(t, "/v1/fixes", token, body); rec.Code != http.StatusCreated {
			t.Fatalf("first POST = %d (%s), want 201", rec.Code, rec.Body.String())
		}
		rec := h.post(t, "/v1/fixes", token, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("replayed POST = %d (%s), want 200 (idempotent no-op)", rec.Code, rec.Body.String())
		}
		var got struct {
			Deduped bool `json:"deduped"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("replay body not JSON: %v", err)
		}
		if !got.Deduped {
			t.Fatal("the replay response does not report deduped=true")
		}
		if h.fixCount(t, deviceID) != 1 {
			t.Fatalf("after a replay the device has %d fixes, want 1", h.fixCount(t, deviceID))
		}

		// A distinct ts is a distinct fix, even arriving second (out of order).
		if rec := h.post(t, "/v1/fixes", token, fmt.Sprintf(`{"lat":41.9,"lon":12.5,"ts":%d}`, ts-3600)); rec.Code != http.StatusCreated {
			t.Fatalf("out-of-order distinct ts = %d, want 201", rec.Code)
		}
		if h.fixCount(t, deviceID) != 2 {
			t.Fatalf("after a distinct fix the device has %d fixes, want 2", h.fixCount(t, deviceID))
		}
	})

	t.Run("every malformed payload is a typed 400 and stores nothing", func(t *testing.T) {
		deviceID, token := h.enroll(t, "carol")
		ts := nowTS()
		cases := []struct{ name, body string }{
			{"missing lat", fmt.Sprintf(`{"lon":12.5,"ts":%d}`, ts)},
			{"missing lon", fmt.Sprintf(`{"lat":41.9,"ts":%d}`, ts)},
			{"missing ts", `{"lat":41.9,"lon":12.5}`},
			{"not JSON", `this is not json`},
			{"trailing data", fmt.Sprintf(`{"lat":41.9,"lon":12.5,"ts":%d} EXTRA`, ts)},
			{"unknown field (a typo the server must not silently ignore)", fmt.Sprintf(`{"latitude":41.9,"lon":12.5,"ts":%d}`, ts)},
			{"swapped axes with an out-of-range latitude", fmt.Sprintf(`{"lat":-122.33,"lon":47.6,"ts":%d}`, ts)},
			{"battery over 100", fmt.Sprintf(`{"lat":41.9,"lon":12.5,"ts":%d,"battery":150}`, ts)},
			{"negative accuracy", fmt.Sprintf(`{"lat":41.9,"lon":12.5,"ts":%d,"accuracy":-1}`, ts)},
			{"ts far in the future", fmt.Sprintf(`{"lat":41.9,"lon":12.5,"ts":%d}`, time.Now().Add(72*time.Hour).Unix())},
		}
		for _, c := range cases {
			rec := h.post(t, "/v1/fixes", token, c.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s: status = %d (%s), want 400", c.name, rec.Code, rec.Body.String())
			}
		}
		if n := h.fixCount(t, deviceID); n != 0 {
			t.Fatalf("malformed payloads left %d rows behind; a rejected fix must never be stored", n)
		}
	})

	t.Run("a device writes only its own fixes", func(t *testing.T) {
		deviceA, tokenA := h.enroll(t, "device-a")
		deviceB, tokenB := h.enroll(t, "device-b")
		ts := nowTS()

		// A tries to smuggle B's id in the body: an unknown field → 400, and it could never
		// re-target the write anyway (identity comes from the token).
		if rec := h.post(t, "/v1/fixes", tokenA, fmt.Sprintf(`{"lat":41.9,"lon":12.5,"ts":%d,"device_id":%q}`, ts, deviceB)); rec.Code != http.StatusBadRequest {
			t.Fatalf("a body with a device_id field = %d, want 400 (unknown field)", rec.Code)
		}
		// A's own write lands under A; B untouched.
		if rec := h.post(t, "/v1/fixes", tokenA, fmt.Sprintf(`{"lat":41.9,"lon":12.5,"ts":%d}`, ts)); rec.Code != http.StatusCreated {
			t.Fatalf("A's own write = %d, want 201", rec.Code)
		}
		if h.fixCount(t, deviceA) != 1 || h.fixCount(t, deviceB) != 0 {
			t.Fatalf("fix landed under the wrong device: A=%d B=%d, want A=1 B=0", h.fixCount(t, deviceA), h.fixCount(t, deviceB))
		}
		if rec := h.post(t, "/v1/fixes", tokenB, fmt.Sprintf(`{"lat":41.9,"lon":12.5,"ts":%d}`, ts)); rec.Code != http.StatusCreated {
			t.Fatalf("B's own write = %d, want 201", rec.Code)
		}
		// Garbage and absent tokens are 401, and store nothing.
		if rec := h.post(t, "/v1/fixes", auth.Token("not-a-real-token"), fmt.Sprintf(`{"lat":41.9,"lon":12.5,"ts":%d}`, ts)); rec.Code != http.StatusUnauthorized {
			t.Fatalf("an unknown token = %d, want 401", rec.Code)
		}
		if rec := h.post(t, "/v1/fixes", "", fmt.Sprintf(`{"lat":41.9,"lon":12.5,"ts":%d}`, ts)); rec.Code != http.StatusUnauthorized {
			t.Fatalf("no token = %d, want 401", rec.Code)
		}
		if h.fixCount(t, deviceA) != 1 || h.fixCount(t, deviceB) != 1 {
			t.Fatalf("an unauthenticated write changed the data: A=%d B=%d, want A=1 B=1", h.fixCount(t, deviceA), h.fixCount(t, deviceB))
		}
	})

	t.Run("the OwnTracks adapter stores a location and honours its contract", func(t *testing.T) {
		deviceID, token := h.enroll(t, "owntracks")
		ts := nowTS()
		// A realistic OwnTracks location: many extra fields the adapter must tolerate, vel in km/h.
		body := fmt.Sprintf(`{"_type":"location","lat":41.9028,"lon":12.4964,"tst":%d,"acc":8,"batt":73,"vel":36,"t":"u","tid":"OT","alt":21,"vac":3,"cog":180,"conn":"w"}`, ts)
		rec := h.post(t, "/owntracks", token, body)

		if rec.Code != http.StatusOK {
			t.Fatalf("OwnTracks location POST = %d (%s), want 200", rec.Code, rec.Body.String())
		}
		if got := rec.Body.String(); got != "[]" {
			t.Fatalf("OwnTracks response body = %q, want the empty array []", got)
		}
		if h.fixCount(t, deviceID) != 1 {
			t.Fatalf("the OwnTracks location was not stored: %d fixes", h.fixCount(t, deviceID))
		}
		// 36 km/h is 10 m/s — a wrong factor here is a silently wrong speed forever.
		var speed float64
		if err := h.pool.QueryRow(context.Background(),
			`SELECT speed_mps FROM fixes WHERE device_id = $1`, deviceID).Scan(&speed); err != nil {
			t.Fatalf("read speed: %v", err)
		}
		if speed < 9.99 || speed > 10.01 {
			t.Fatalf("stored speed = %v m/s, want 10 (36 km/h ÷ 3.6)", speed)
		}

		// A non-location message: acknowledged (2xx + []), stored nowhere.
		rec = h.post(t, "/owntracks", token, `{"_type":"transition","event":"enter","desc":"home"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("OwnTracks transition = %d, want 200 (acknowledged, not retried)", rec.Code)
		}
		if h.fixCount(t, deviceID) != 1 {
			t.Fatalf("a non-location OwnTracks message was stored as a fix: %d fixes, want 1", h.fixCount(t, deviceID))
		}
		// A location missing required fields is a 400 (never stored); the app's retry is its own concern.
		rec = h.post(t, "/owntracks", token, `{"_type":"location","lat":41.9}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("OwnTracks location missing lon/tst = %d, want 400", rec.Code)
		}
		if h.fixCount(t, deviceID) != 1 {
			t.Fatalf("a malformed OwnTracks location was stored: %d fixes, want 1", h.fixCount(t, deviceID))
		}
	})
}
