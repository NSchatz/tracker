// The S4 live-map surface, driven through the real HTTP handler against a real PostGIS. These are
// the roadmap's S4 acceptance tests: the SSE contract (event format, per-event id, family scoping),
// the reconnect/resume path (Last-Event-ID delivers exactly what arrived while away — no gaps, no
// cross-family bleed), and the auth that guards it. Like the rest of the suite they FAIL rather than
// skip when Docker is missing, because the position query under test is real spatial SQL.
//
// The stream is long-lived, so these use a real httptest.Server and read the response body
// incrementally, rather than the one-shot ResponseRecorder the request/response tests use. The poll
// interval is shrunk to keep the suite fast; see SetStreamPollInterval.
package server_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NSchatz/tracker/internal/auth"
	"github.com/NSchatz/tracker/internal/server"
	"github.com/NSchatz/tracker/internal/store"
)

// sseEvent is one parsed Server-Sent Event: the id (our received-at cursor), the event name, and the
// data payload. Comments (`: keep-alive`) are dropped by the reader, not surfaced here.
type sseEvent struct {
	id    string
	event string
	data  string
}

// openStream GETs an SSE endpoint and returns a channel of parsed events, a cancel that tears the
// connection down, and the initial response (for its status code and headers). The reader goroutine
// runs until the body closes; cancelling the returned context is how a test stops it.
func openStream(t *testing.T, rawURL, lastEventID string, token auth.Token) (<-chan sseEvent, context.CancelFunc, *http.Response) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())

	if token != "" {
		sep := "?"
		if strings.Contains(rawURL, "?") {
			sep = "&"
		}
		rawURL += sep + "token=" + string(token)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		cancel()
		t.Fatalf("new request: %v", err)
	}
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("open stream: %v", err)
	}

	ch := make(chan sseEvent, 32)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		var cur sseEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if cur != (sseEvent{}) {
					select {
					case ch <- cur:
					case <-ctx.Done():
						return
					}
				}
				cur = sseEvent{}
			case strings.HasPrefix(line, ":"):
				// A heartbeat comment. Ignore it.
			case strings.HasPrefix(line, "id:"):
				cur.id = strings.TrimSpace(line[len("id:"):])
			case strings.HasPrefix(line, "event:"):
				cur.event = strings.TrimSpace(line[len("event:"):])
			case strings.HasPrefix(line, "data:"):
				cur.data = strings.TrimSpace(line[len("data:"):])
			}
		}
	}()
	return ch, cancel, resp
}

// waitEvent returns the next event or fails the test on timeout — a stream that should have pushed
// and did not is a failure, not a thing to wait on forever.
func waitEvent(t *testing.T, ch <-chan sseEvent, within time.Duration) sseEvent {
	t.Helper()
	select {
	case e, ok := <-ch:
		if !ok {
			t.Fatal("stream closed before delivering an event")
		}
		return e
	case <-time.After(within):
		t.Fatal("timed out waiting for an SSE event")
		return sseEvent{}
	}
}

// expectNoEvent asserts the stream stays quiet for a beat — used to prove a resumed cursor does NOT
// re-deliver the boundary row it resumed from, and that a cross-family watcher gets nothing.
func expectNoEvent(t *testing.T, ch <-chan sseEvent, within time.Duration) {
	t.Helper()
	select {
	case e, ok := <-ch:
		if !ok {
			return // stream closed; nothing delivered, which is what we wanted.
		}
		t.Fatalf("expected no event, but the stream pushed one: %+v", e)
	case <-time.After(within):
	}
}

// streamPos is the position payload the stream carries — the same wire shape as GET /v1/positions.
type streamPos struct {
	DeviceID   string  `json:"device_id"`
	DeviceName string  `json:"device_name"`
	Lat        float64 `json:"lat"`
	Lon        float64 `json:"lon"`
	TS         int64   `json:"ts"`
	ReceivedAt int64   `json:"received_at"`
}

func decodePos(t *testing.T, e sseEvent) streamPos {
	t.Helper()
	var p streamPos
	if err := json.Unmarshal([]byte(e.data), &p); err != nil {
		t.Fatalf("event data is not a position JSON: %v (%q)", err, e.data)
	}
	return p
}

// TestStreamSSE is the whole S4 stream suite on one shared container, each subtest isolated by family.
func TestStreamSSE(t *testing.T) {
	t.Parallel()
	// Shrink the poll cadence so the suite does not wait a production second per tick. Set once,
	// before any stream starts, and restored at cleanup — the poll goroutine reads the interval only
	// at handler start, so there is no race with this write.
	t.Cleanup(server.SetStreamPollInterval(20 * time.Millisecond))

	h := newHarness(t)
	srv := httptest.NewServer(h.handler)
	t.Cleanup(srv.Close)

	t.Run("contract: event format, snapshot, family scoping", func(t *testing.T) {
		streamContract(t, h, srv)
	})
	t.Run("resume: Last-Event-ID delivers only what arrived while away", func(t *testing.T) {
		streamResume(t, h, srv)
	})
	t.Run("auth: query token works, no/unknown/device token is 401", func(t *testing.T) {
		streamAuth(t, h, srv)
	})
	t.Run("PositionsSince: exclusive cursor, received_at order, family scope", func(t *testing.T) {
		positionsSinceQuery(t, h)
	})
}

// positionsSinceQuery pins the stream's cursor query directly against the store: `since` is
// exclusive, results are ordered by received_at ascending (the property that makes received_at a
// resumable id), and another family's fixes never appear.
func positionsSinceQuery(t *testing.T, h *harness) {
	ctx := context.Background()
	familyID, d1 := h.makeFamilyWithDevice(t, "since-1")
	base := time.Now().Truncate(time.Second).Add(-time.Hour) // inside the ingest window

	var zero time.Time
	if got, err := store.PositionsSince(ctx, h.pool, familyID, zero); err != nil {
		t.Fatalf("PositionsSince(empty family): %v", err)
	} else if len(got) != 0 {
		t.Fatalf("empty family returned %d positions, want 0", len(got))
	}

	h.ingest(t, d1, base, 12.4964, 41.9028)

	// Read back the row's received_at so we can probe the cursor boundary exactly.
	var recv1 time.Time
	if err := h.pool.QueryRow(ctx, `SELECT received_at FROM fixes WHERE device_id = $1`, d1).Scan(&recv1); err != nil {
		t.Fatalf("read received_at: %v", err)
	}

	// since == the row's own received_at → excluded (the cursor is exclusive, so re-passing the last
	// id never re-delivers the row that produced it).
	if got, err := store.PositionsSince(ctx, h.pool, familyID, recv1); err != nil {
		t.Fatalf("PositionsSince(== received_at): %v", err)
	} else if len(got) != 0 {
		t.Fatalf("an exclusive cursor at the row's own received_at returned %d rows, want 0", len(got))
	}
	// since just before it → included.
	if got, err := store.PositionsSince(ctx, h.pool, familyID, recv1.Add(-time.Microsecond)); err != nil {
		t.Fatalf("PositionsSince(< received_at): %v", err)
	} else if len(got) != 1 || got[0].DeviceID != d1 {
		t.Fatalf("cursor just before the row returned %+v, want exactly device d1", got)
	}

	// A second device, ingested later, must sort AFTER d1 by received_at (not by name or id). The
	// sleep guarantees a distinct, later received_at so the ordering assertion is not a coin flip.
	time.Sleep(3 * time.Millisecond)
	d2hash := sha256.Sum256([]byte(t.Name() + "/since-2"))
	d2, err := store.CreateDevice(ctx, h.pool, familyID, "since-2-phone", d2hash[:])
	if err != nil {
		t.Fatalf("CreateDevice d2: %v", err)
	}
	h.ingest(t, d2, base.Add(time.Minute), 9.19, 45.4642)

	got, err := store.PositionsSince(ctx, h.pool, familyID, zero)
	if err != nil {
		t.Fatalf("PositionsSince(both): %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d positions, want 2", len(got))
	}
	if got[0].DeviceID != d1 || got[1].DeviceID != d2 {
		t.Fatalf("positions not ordered by received_at ascending: %s then %s, want d1 then d2", got[0].DeviceID, got[1].DeviceID)
	}
	if got[0].ReceivedAt.After(got[1].ReceivedAt) {
		t.Fatalf("received_at not non-decreasing: %s then %s", got[0].ReceivedAt, got[1].ReceivedAt)
	}

	// Another family's fix never appears in this family's stream cursor.
	otherFamily, other := h.makeFamilyWithDevice(t, "since-intruder")
	h.ingest(t, other, base, 12.9, 41.9)
	after, err := store.PositionsSince(ctx, h.pool, familyID, zero)
	if err != nil {
		t.Fatalf("PositionsSince(after intruder): %v", err)
	}
	if len(after) != 2 {
		t.Fatalf("another family's fix leaked into this family's stream: %d positions, want 2", len(after))
	}
	for _, p := range after {
		if p.DeviceID == other {
			t.Fatal("another family's device appeared in this family's stream cursor")
		}
	}
	if got, err := store.PositionsSince(ctx, h.pool, otherFamily, zero); err != nil {
		t.Fatalf("PositionsSince(other family): %v", err)
	} else if len(got) != 1 || got[0].DeviceID != other {
		t.Fatalf("the other family sees %+v, want exactly its own device", got)
	}
}

// streamContract pins the SSE contract: a fresh watcher gets its family's current position as a
// well-formed event (id + `event: position` + JSON data), and it never sees another family's device.
func streamContract(t *testing.T, h *harness, srv *httptest.Server) {
	devA, tokADevice := h.enroll(t, "contract-a")
	famA := h.familyOf(t, devA)
	viewerA := h.addViewer(t, famA, "contract-viewer-a")

	devB, tokBDevice := h.enroll(t, "contract-b")
	_ = devB
	ts := nowTS()
	h.seedFix(t, tokADevice, 41.9028, 12.4964, ts) // A near Rome
	h.seedFix(t, tokBDevice, 48.8566, 2.3522, ts)  // B near Paris, a DIFFERENT family

	ch, cancel, resp := openStream(t, srv.URL+"/v1/stream", "", viewerA)
	defer cancel()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream open = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	e := waitEvent(t, ch, 3*time.Second)
	if e.event != "position" {
		t.Fatalf("event name = %q, want %q", e.event, "position")
	}
	if _, err := strconv.ParseInt(e.id, 10, 64); err != nil || e.id == "" {
		t.Fatalf("event id = %q, want a numeric received-at cursor", e.id)
	}
	p := decodePos(t, e)
	if p.DeviceID != devA {
		t.Fatalf("snapshot device = %s, want the viewer's own device A (%s)", p.DeviceID, devA)
	}
	if p.Lon < 12.49 || p.Lon > 12.50 || p.Lat < 41.90 || p.Lat > 41.91 {
		t.Fatalf("snapshot (lon,lat) = (%v,%v), want ~(12.4964, 41.9028) — axis order survived the stream", p.Lon, p.Lat)
	}

	// Family scoping: viewer A must NEVER be pushed family B's device, however long we watch.
	expectNoEvent(t, ch, 300*time.Millisecond)
}

// streamResume is the reconnect/resume acceptance: a watcher drops after seeing a position, the device
// moves, the watcher reconnects with Last-Event-ID, and it receives EXACTLY the new position — the
// gap-free property — and not a re-delivery of the one it already had.
func streamResume(t *testing.T, h *harness, srv *httptest.Server) {
	devA, tokADevice := h.enroll(t, "resume-a")
	famA := h.familyOf(t, devA)
	viewerA := h.addViewer(t, famA, "resume-viewer-a")

	ts := nowTS()
	h.seedFix(t, tokADevice, 41.9028, 12.4964, ts) // first position: Rome

	// First connection: capture the snapshot event and its id (the resume cursor).
	ch1, cancel1, resp1 := openStream(t, srv.URL+"/v1/stream", "", viewerA)
	if resp1.StatusCode != http.StatusOK {
		cancel1()
		t.Fatalf("first stream open = %d, want 200", resp1.StatusCode)
	}
	first := waitEvent(t, ch1, 3*time.Second)
	firstPos := decodePos(t, first)
	if firstPos.Lon < 12.49 || firstPos.Lon > 12.50 {
		cancel1()
		t.Fatalf("first event lon = %v, want ~12.4964", firstPos.Lon)
	}
	lastID := first.id
	cancel1() // the connection drops.

	// While disconnected, the device moves: a newer fix at a new place.
	h.seedFix(t, tokADevice, 45.4642, 9.1900, ts+60) // moved to Milan

	// Reconnect from Last-Event-ID. The resume must deliver the Milan position (arrived while away)
	// and must NOT re-deliver the Rome one (the boundary the cursor sat on).
	ch2, cancel2, resp2 := openStream(t, srv.URL+"/v1/stream", lastID, viewerA)
	defer cancel2()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("resumed stream open = %d, want 200", resp2.StatusCode)
	}

	resumed := waitEvent(t, ch2, 3*time.Second)
	resumedPos := decodePos(t, resumed)
	if resumedPos.Lon < 9.18 || resumedPos.Lon > 9.20 || resumedPos.Lat < 45.46 || resumedPos.Lat > 45.47 {
		t.Fatalf("resumed (lon,lat) = (%v,%v), want ~(9.19, 45.4642) — the move made while away was skipped (a gap)", resumedPos.Lon, resumedPos.Lat)
	}
	// The resumed id must be strictly greater than the one we resumed from — a monotonic cursor.
	prev, _ := strconv.ParseInt(lastID, 10, 64)
	cur, _ := strconv.ParseInt(resumed.id, 10, 64)
	if cur <= prev {
		t.Fatalf("resumed id %d is not greater than the resume cursor %d — the cursor is not monotonic", cur, prev)
	}
	// And the Rome boundary row must not have been replayed: no second, older event follows.
	expectNoEvent(t, ch2, 300*time.Millisecond)
}

// streamAuth pins the stream's authentication: the browser's query-string token works, and a missing,
// unknown, or DEVICE token is a 401 — a write credential cannot open a read stream (§7).
func streamAuth(t *testing.T, h *harness, srv *httptest.Server) {
	devA, tokADevice := h.enroll(t, "auth-a")
	famA := h.familyOf(t, devA)
	viewerA := h.addViewer(t, famA, "auth-viewer-a")

	// No token at all → 401.
	_, cancel, resp := openStream(t, srv.URL+"/v1/stream", "", "")
	cancel()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-token stream = %d, want 401", resp.StatusCode)
	}

	// An unknown token → 401.
	_, cancel, resp = openStream(t, srv.URL+"/v1/stream", "", auth.Token("not-a-real-token"))
	cancel()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown-token stream = %d, want 401", resp.StatusCode)
	}

	// A DEVICE (write) token is not a viewer → 401, in the query-string form too.
	_, cancel, resp = openStream(t, srv.URL+"/v1/stream", "", tokADevice)
	cancel()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("device-token stream = %d, want 401 (a write credential cannot read)", resp.StatusCode)
	}

	// The viewer token in the query string authenticates and opens the stream.
	_, cancel, resp = openStream(t, srv.URL+"/v1/stream", "", viewerA)
	cancel()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer-token stream = %d, want 200", resp.StatusCode)
	}

	// The viewer token in the Authorization header authenticates too (a programmatic caller).
	ctx, cancelReq := context.WithCancel(context.Background())
	defer cancelReq()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/v1/stream", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+string(viewerA))
	hdrResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("header-auth stream: %v", err)
	}
	defer hdrResp.Body.Close()
	if hdrResp.StatusCode != http.StatusOK {
		t.Fatalf("header-auth stream = %d, want 200", hdrResp.StatusCode)
	}
}

// TestMapPageAndAssets pins that the minimal Leaflet viewer and its vendored assets are served — the
// static shell the browser end-to-end check drives. No database is needed: these are static files, so
// a stub DB and a one-shot recorder suffice.
func TestMapPageAndAssets(t *testing.T) {
	t.Parallel()
	handler := server.New(stubDB{}, nil, discardLogger())

	t.Run("GET /map is the HTML page", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/map", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /map = %d, want 200", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Fatalf("map Content-Type = %q, want text/html", ct)
		}
		if body := rec.Body.String(); !strings.Contains(body, "EventSource") || !strings.Contains(body, "/static/leaflet.js") {
			t.Fatal("map page does not wire up the SSE stream and the vendored Leaflet")
		}
	})

	t.Run("GET /static/leaflet.js is the vendored library", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/leaflet.js", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /static/leaflet.js = %d, want 200", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "Leaflet") {
			t.Fatal("/static/leaflet.js did not serve the Leaflet library")
		}
	})
}
