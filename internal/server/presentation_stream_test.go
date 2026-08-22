// The live stream after the presentation change: the snapshot's composition, the events a device's
// state change produces when its POSITION did not change, the resume sweep, and the honesty of a
// stream whose datastore has gone away.
//
// These tests are deliberately NOT parallel. They shrink the stream's poll cadence, which is a
// process-wide var, and the parallel S4 suite (TestStreamSSE) shrinks it too; running sequentially is
// what keeps the two out of each other's way under `go test -race`. Go runs the non-parallel tests to
// completion — cleanups included — before the parallel ones resume, so the restore lands first.
package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NSchatz/tracker/internal/auth"
	"github.com/NSchatz/tracker/internal/db"
	"github.com/NSchatz/tracker/internal/presentation"
	"github.com/NSchatz/tracker/internal/server"
	"github.com/NSchatz/tracker/internal/store"
)

// tightWindows makes an age boundary reachable inside a test: a device is `live` for one second,
// `recent` for the next, and `stale` after that. The sweep bound B for this pair is 1 second, which
// is what the timing assertions below are written against.
var tightWindows = presentation.Windows{LiveSeconds: 1, StaleSeconds: 2}

// roomyWindows are wide enough that a device stays `live` for the whole of a subtest, so a case about
// something OTHER than ageing cannot fail because a second went by.
var roomyWindows = presentation.Windows{LiveSeconds: 600, StaleSeconds: 1200}

// streamServer stands up a handler with the given presentation windows over the shared pool, so each
// case picks the windows its property needs instead of every case sharing one compromise.
func streamServer(t *testing.T, h *harness, w presentation.Windows) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(server.New(h.pool, nil, w, discardLogger()))
	t.Cleanup(srv.Close)
	return srv
}

// waitFor returns the next event satisfying match, ignoring the others, or fails at the deadline.
// Ignoring rather than asserting on the first event is what lets a test about one device's state
// tolerate an unrelated heartbeat or another device's event without becoming order-dependent.
func waitFor(t *testing.T, ch <-chan sseEvent, within time.Duration, what string, match func(sseEvent) bool) sseEvent {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				t.Fatalf("stream closed while waiting for %s", what)
			}
			if match(e) {
				return e
			}
		case <-deadline:
			t.Fatalf("timed out after %s waiting for %s", within, what)
			return sseEvent{}
		}
	}
}

// payload is the tolerant decode of any event's data: presentation payloads (four keys), unlocated
// entries (three) and located entries (eight) all land here, with absence distinguishable from zero.
type payload struct {
	DeviceID      string   `json:"device_id"`
	DeviceName    string   `json:"device_name"`
	Presentation  string   `json:"presentation"`
	Lat           *float64 `json:"lat"`
	Lon           *float64 `json:"lon"`
	TS            *int64   `json:"ts"`
	ReceivedAt    *int64   `json:"received_at"`
	LastContactAt *int64   `json:"last_contact_at"`
}

func decodePayload(t *testing.T, e sseEvent) payload {
	t.Helper()
	var p payload
	if err := json.Unmarshal([]byte(e.data), &p); err != nil {
		t.Fatalf("event data is not JSON: %v (%q)", err, e.data)
	}
	return p
}

// jsonKeys returns the keys of a JSON object, so "exactly three keys" is an assertion about the wire
// and not about a struct that quietly dropped what it did not know.
func jsonKeys(t *testing.T, raw string) map[string]bool {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatalf("not a JSON object: %v (%q)", err, raw)
	}
	keys := make(map[string]bool, len(obj))
	for k := range obj {
		keys[k] = true
	}
	return keys
}

// newFamilyWithViewer is the setup every case here starts from: a fresh authorization boundary and a
// credential that may read it.
func newFamilyWithViewer(t *testing.T, h *harness, label string) (familyID string, viewer auth.Token) {
	t.Helper()
	familyID, err := store.CreateFamily(context.Background(), h.pool, t.Name()+"/"+label)
	if err != nil {
		t.Fatalf("CreateFamily: %v", err)
	}
	return familyID, h.addViewer(t, familyID, label+"-viewer")
}

// TestStreamPresentation is the stream-side suite on one shared container, each case isolated by
// family.
func TestStreamPresentation(t *testing.T) {
	t.Cleanup(server.SetStreamPollInterval(20 * time.Millisecond))
	h := newHarness(t)

	t.Run("a fresh snapshot describes every device", func(t *testing.T) { snapshotDescribesEveryone(t, h) })
	t.Run("both surfaces emit the identical unlocated object", func(t *testing.T) { unlocatedObjectMatchesAcrossSurfaces(t, h) })
	t.Run("time alone moves a device through the states", func(t *testing.T) { timeDrivenTransitions(t, h) })
	t.Run("a backlog fix refreshes last contact without moving the marker", func(t *testing.T) { backlogRefreshesWithoutPosition(t, h) })
	t.Run("a device enrolled mid-stream is not announced until it reports", func(t *testing.T) { midStreamEnrollment(t, h) })
	t.Run("a resume sweeps the whole family against the cursor instant", func(t *testing.T) { resumeSweep(t, h) })
	t.Run("an unanchorable cursor is a fresh snapshot, not a resume", func(t *testing.T) { unanchorableCursor(t, h) })
	t.Run("a position-only consumer sees exactly what it always did", func(t *testing.T) { positionOnlyConsumer(t, h) })
	t.Run("an unreadable datastore is reported, not hidden", func(t *testing.T) { datastoreLossOnAnOpenStream(t, h) })
}

// snapshotDescribesEveryone is AC15: a fresh connection receives one `position` event per device
// holding a fix and one `presentation` event carrying the three-key unlocated entry per device
// holding none — every device in the family, each with a value computed at delivery time.
func snapshotDescribesEveryone(t *testing.T, h *harness) {
	familyID, viewer := newFamilyWithViewer(t, h, "snapshot")
	located := h.addDevice(t, familyID, "located-phone", "snapshot-located")
	unlocated := h.addDevice(t, familyID, "unset-phone", "snapshot-unset")
	now := time.Now()
	h.seedFixAt(t, located, now.Add(-time.Second), now.Add(-time.Second), 12.4964, 41.9028)

	srv := streamServer(t, h, roomyWindows)
	ch, cancel, resp := openStream(t, srv.URL+"/v1/stream", "", viewer)
	defer cancel()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream open = %d, want 200", resp.StatusCode)
	}

	pos := waitFor(t, ch, 3*time.Second, "the located device's position event", func(e sseEvent) bool {
		return e.event == "position" && decodePayload(t, e).DeviceID == located
	})
	if pos.id == "" {
		t.Error("a position event carries no id; the resume cursor depends on it")
	}
	p := decodePayload(t, pos)
	if p.Presentation != "live" {
		t.Errorf("snapshot position presentation = %q, want live", p.Presentation)
	}
	if p.LastContactAt == nil {
		t.Error("a located entry must carry last_contact_at")
	}

	un := waitFor(t, ch, 3*time.Second, "the unlocated device's presentation event", func(e sseEvent) bool {
		return e.event == "presentation" && decodePayload(t, e).DeviceID == unlocated
	})
	if un.id != "" {
		t.Errorf("a presentation event carried id %q; it must never advance Last-Event-ID", un.id)
	}
	keys := jsonKeys(t, un.data)
	if len(keys) != 3 {
		t.Errorf("the unlocated payload carries %d keys (%v), want exactly 3", len(keys), keys)
	}
	for _, forbidden := range []string{"lat", "lon", "ts", "received_at", "last_contact_at"} {
		if keys[forbidden] {
			t.Errorf("the unlocated payload carries %q — a no-position device has no coordinate to plot", forbidden)
		}
	}
	if decodePayload(t, un).Presentation != "no-position" {
		t.Errorf("unlocated presentation = %q, want no-position", decodePayload(t, un).Presentation)
	}
}

// unlocatedObjectMatchesAcrossSurfaces is AC32: the same never-reported device read through
// GET /v1/positions and through a fresh stream snapshot produces the IDENTICAL object. If the two
// surfaces could disagree, a map would have to know which one it was reading.
func unlocatedObjectMatchesAcrossSurfaces(t *testing.T, h *harness) {
	familyID, viewer := newFamilyWithViewer(t, h, "identical")
	device := h.addDevice(t, familyID, "never-seen-phone", "identical-device")

	srv := streamServer(t, h, roomyWindows)
	ch, cancel, _ := openStream(t, srv.URL+"/v1/stream", "", viewer)
	defer cancel()
	streamed := waitFor(t, ch, 3*time.Second, "the unlocated snapshot event", func(e sseEvent) bool {
		return e.event == "presentation" && decodePayload(t, e).DeviceID == device
	})

	body := h.get(t, "/v1/positions", viewer).Body.Bytes()
	var read []json.RawMessage
	if err := json.Unmarshal(body, &read); err != nil {
		t.Fatalf("positions body: %v (%q)", err, string(body))
	}
	if len(read) != 1 {
		t.Fatalf("positions returned %d entries, want 1", len(read))
	}

	var fromRead, fromStream map[string]any
	if err := json.Unmarshal(read[0], &fromRead); err != nil {
		t.Fatalf("read entry: %v", err)
	}
	if err := json.Unmarshal([]byte(streamed.data), &fromStream); err != nil {
		t.Fatalf("stream entry: %v", err)
	}
	if len(fromRead) != len(fromStream) {
		t.Fatalf("the two surfaces disagree on shape:\nread:   %v\nstream: %v", fromRead, fromStream)
	}
	for k, v := range fromRead {
		if fromStream[k] != v {
			t.Errorf("key %q is %v on the read surface and %v on the stream", k, v, fromStream[k])
		}
	}
	for _, forbidden := range []string{"lat", "lon", "ts", "received_at", "last_contact_at"} {
		if _, ok := fromRead[forbidden]; ok {
			t.Errorf("the read surface's unlocated entry carries %q", forbidden)
		}
		if _, ok := fromStream[forbidden]; ok {
			t.Errorf("the stream's unlocated entry carries %q", forbidden)
		}
	}
}

// timeDrivenTransitions is AC17's first cause and AC18's guarantee: a device crosses `live` to
// `recent` to `stale` with NO fix arriving, and each crossing is announced as a `presentation` event
// that carries no id.
//
// The bound is real, not incidental: with these windows B is 1 second, and each event is required
// within B of the crossing instant the seeded arrival time fixes.
func timeDrivenTransitions(t *testing.T, h *harness) {
	familyID, viewer := newFamilyWithViewer(t, h, "ageing")
	device := h.addDevice(t, familyID, "ageing-phone", "ageing")
	arrived := time.Now()
	h.seedFixAt(t, device, arrived, arrived, 12.4964, 41.9028)

	srv := streamServer(t, h, tightWindows)
	ch, cancel, _ := openStream(t, srv.URL+"/v1/stream", "", viewer)
	defer cancel()

	waitFor(t, ch, 3*time.Second, "the snapshot position", func(e sseEvent) bool {
		return e.event == "position" && decodePayload(t, e).DeviceID == device
	})

	// live -> recent happens when the age ticks to 2 (past the 1s live window), i.e. at arrived+2s.
	// B is 1s, so it is owed by arrived+3s.
	recent := waitFor(t, ch, time.Until(arrived.Add(3*time.Second))+time.Second, "the live->recent transition",
		func(e sseEvent) bool {
			return e.event == "presentation" && decodePayload(t, e).Presentation == "recent"
		})
	if recent.id != "" {
		t.Errorf("the transition event carried id %q; a presentation event must not advance the cursor", recent.id)
	}
	if p := decodePayload(t, recent); p.DeviceID != device || p.LastContactAt == nil {
		t.Errorf("transition payload = %+v, want this device with its last contact", p)
	}
	if keys := jsonKeys(t, recent.data); keys["lat"] || keys["lon"] {
		t.Error("a presentation event about a device that has not moved carries coordinates; it is not a new fix")
	}

	// recent -> stale at arrived+3s, owed by arrived+4s.
	stale := waitFor(t, ch, time.Until(arrived.Add(4*time.Second))+time.Second, "the recent->stale transition",
		func(e sseEvent) bool {
			return e.event == "presentation" && decodePayload(t, e).Presentation == "stale"
		})
	if stale.id != "" {
		t.Errorf("the stale transition carried id %q", stale.id)
	}
}

// backlogRefreshesWithoutPosition is AC17's second cause, in the freshening direction: a fix arrives
// with an OLDER event-time than the current position, so the marker does not move — but it refreshes
// last contact, and a device the map was calling `stale` is alive again.
func backlogRefreshesWithoutPosition(t *testing.T, h *harness) {
	familyID, viewer := newFamilyWithViewer(t, h, "backlog")
	device := h.addDevice(t, familyID, "backlog-stream-phone", "backlog-stream")

	now := time.Now()
	current := now.Add(-time.Hour) // ts AND arrival an hour ago: comfortably stale.
	h.seedFixAt(t, device, current, current, 12.4964, 41.9028)

	srv := streamServer(t, h, tightWindows)
	ch, cancel, _ := openStream(t, srv.URL+"/v1/stream", "", viewer)
	defer cancel()

	first := waitFor(t, ch, 3*time.Second, "the snapshot position", func(e sseEvent) bool {
		return e.event == "position" && decodePayload(t, e).DeviceID == device
	})
	if p := decodePayload(t, first); p.Presentation != "stale" {
		t.Fatalf("snapshot presentation = %q, want stale", p.Presentation)
	}

	// The gap-fill: an even older event-time, arriving NOW. It cannot become the current position.
	h.seedFixAt(t, device, now.Add(-2*time.Hour), time.Now(), 9.19, 45.4642)

	back := waitFor(t, ch, 3*time.Second, "the stale->live transition", func(e sseEvent) bool {
		return e.event == "presentation" && decodePayload(t, e).Presentation == "live"
	})
	if back.id != "" {
		t.Errorf("the backlog transition carried id %q", back.id)
	}
	if keys := jsonKeys(t, back.data); keys["lat"] || keys["lon"] {
		t.Error("the backlog transition carried coordinates; the device has not moved")
	}

	// And the current position really did not move: the read surface still serves the newest-ts fix.
	got := decodeEntries(t, h.get(t, "/v1/positions", viewer).Body.Bytes())
	if len(got) != 1 || got[0].Lon == nil || *got[0].Lon < 12.49 || *got[0].Lon > 12.50 {
		t.Errorf("current position = %+v, want the newest-ts fix at ~12.4964", got)
	}
}

// midStreamEnrollment is AC20: a device enrolled AFTER a connection opened, and not yet reporting, is
// not announced on that connection — the set announced as unlocated is fixed at snapshot time. When
// it reports, its position IS delivered on that same open connection, which is AC16's inclusion of a
// device's first ever fix.
func midStreamEnrollment(t *testing.T, h *harness) {
	familyID, viewer := newFamilyWithViewer(t, h, "midstream")
	existing := h.addDevice(t, familyID, "existing-phone", "midstream-existing")
	now := time.Now()
	h.seedFixAt(t, existing, now.Add(-time.Second), now.Add(-time.Second), 12.4964, 41.9028)

	srv := streamServer(t, h, roomyWindows)
	ch, cancel, _ := openStream(t, srv.URL+"/v1/stream", "", viewer)
	defer cancel()
	waitFor(t, ch, 3*time.Second, "the snapshot position", func(e sseEvent) bool {
		return e.event == "position" && decodePayload(t, e).DeviceID == existing
	})

	late := h.addDevice(t, familyID, "late-phone", "midstream-late")
	// Nothing is owed for it: several poll cycles pass and the connection stays quiet.
	expectNoEvent(t, ch, 500*time.Millisecond)

	// Now it reports for the first time. That IS delivered, as a position event with an id.
	h.seedFixAt(t, late, time.Now(), time.Now(), 9.19, 45.4642)
	first := waitFor(t, ch, 3*time.Second, "the late device's first position", func(e sseEvent) bool {
		return e.event == "position" && decodePayload(t, e).DeviceID == late
	})
	if first.id == "" {
		t.Error("a first-ever position arrived with no id; the cursor must advance over it")
	}
	if p := decodePayload(t, first); p.Presentation != "live" || p.Lon == nil {
		t.Errorf("first position payload = %+v, want a located entry presented as live", p)
	}
}

// resumeSweep is AC19's three named tests, each naming the cursor it resumes with.
//
// The referent is the CURSOR-INSTANT VALUE: what the same pure function yields with the evaluation
// instant set to the instant the cursor encodes, over only the fixes that had arrived by then. It is
// recomputed from stored fixes every time, which is why two resumes on one cursor emit one set.
func resumeSweep(t *testing.T, h *harness) {
	t.Run("a device that aged past the windows while away is told, idempotently", func(t *testing.T) {
		familyID, viewer := newFamilyWithViewer(t, h, "sweep-aged")
		device := h.addDevice(t, familyID, "aged-phone", "sweep-aged-device")
		arrived := time.Now()
		h.seedFixAt(t, device, arrived, arrived, 12.4964, 41.9028)

		srv := streamServer(t, h, tightWindows)
		// The cursor its last fix set: the arrival, in microseconds, exactly as an id.
		cursor := strconv.FormatInt(arrived.Truncate(time.Microsecond).UnixMicro(), 10)

		// Let it age past the staleness window with no fix arriving.
		time.Sleep(3100 * time.Millisecond)

		for attempt := 1; attempt <= 2; attempt++ {
			ch, cancel, resp := openStream(t, srv.URL+"/v1/stream", cursor, viewer)
			if resp.StatusCode != http.StatusOK {
				cancel()
				t.Fatalf("resume %d = %d, want 200", attempt, resp.StatusCode)
			}
			e := waitFor(t, ch, 3*time.Second, "the sweep's presentation event", func(e sseEvent) bool {
				return e.event == "presentation" && decodePayload(t, e).DeviceID == device
			})
			if p := decodePayload(t, e); p.Presentation != "stale" {
				t.Errorf("resume %d reported %q, want stale", attempt, p.Presentation)
			}
			if e.id != "" {
				t.Errorf("resume %d's sweep event carried id %q", attempt, e.id)
			}
			// The SAME single event: nothing else for this device, and no position replay either.
			expectNoEvent(t, ch, 300*time.Millisecond)
			cancel()
		}
	})

	t.Run("a device whose value has not changed gets nothing, and the stream stays open", func(t *testing.T) {
		familyID, viewer := newFamilyWithViewer(t, h, "sweep-silent")
		device := h.addDevice(t, familyID, "silent-phone", "sweep-silent-device")
		arrived := time.Now()
		h.seedFixAt(t, device, arrived, arrived, 12.4964, 41.9028)

		// Roomy windows: the device is `live` at the cursor instant and still `live` now, so its two
		// values agree and it is owed nothing. Silence here means "evaluated, unchanged".
		srv := streamServer(t, h, roomyWindows)
		cursor := strconv.FormatInt(arrived.Truncate(time.Microsecond).UnixMicro(), 10)

		ch, cancel, resp := openStream(t, srv.URL+"/v1/stream", cursor, viewer)
		defer cancel()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("resume = %d, want 200", resp.StatusCode)
		}
		expectNoEvent(t, ch, 500*time.Millisecond)
		if resp.StatusCode != http.StatusOK {
			t.Fatal("the connection did not stay open")
		}
	})

	t.Run("a device enrolled during the outage is never silently omitted", func(t *testing.T) {
		familyID, viewer := newFamilyWithViewer(t, h, "sweep-enrolled")
		reporter := h.addDevice(t, familyID, "reporter-phone", "sweep-reporter")
		arrived := time.Now()
		h.seedFixAt(t, reporter, arrived, arrived, 12.4964, 41.9028)
		cursor := strconv.FormatInt(arrived.Truncate(time.Microsecond).UnixMicro(), 10)

		// Enrolled after the cursor and never reporting: it holds no fix at or before the cursor
		// instant, so it HAS NO cursor-instant value — an absence that is not `no-position` and never
		// compares equal to anything.
		newcomer := h.addDevice(t, familyID, "newcomer-phone", "sweep-newcomer")

		srv := streamServer(t, h, roomyWindows)
		ch, cancel, _ := openStream(t, srv.URL+"/v1/stream", cursor, viewer)
		defer cancel()

		e := waitFor(t, ch, 3*time.Second, "the newcomer's presentation event", func(e sseEvent) bool {
			return e.event == "presentation" && decodePayload(t, e).DeviceID == newcomer
		})
		keys := jsonKeys(t, e.data)
		if len(keys) != 3 {
			t.Errorf("the newcomer's payload carries %d keys (%v), want the three-key unlocated entry", len(keys), keys)
		}
		if p := decodePayload(t, e); p.Presentation != "no-position" {
			t.Errorf("the newcomer is %q, want no-position", p.Presentation)
		}
	})
}

// unanchorableCursor is the unhappy path F29 names: a Last-Event-ID that is not a number cannot be
// anchored to an instant, so it is NOT a resume. It falls back to the full fresh snapshot — failing
// safe toward showing more, never toward a silent skip — which is the behaviour this endpoint has
// always had for a garbled cursor.
func unanchorableCursor(t *testing.T, h *harness) {
	familyID, viewer := newFamilyWithViewer(t, h, "banana")
	device := h.addDevice(t, familyID, "banana-phone", "banana-device")
	unset := h.addDevice(t, familyID, "banana-unset", "banana-unset-device")
	now := time.Now()
	h.seedFixAt(t, device, now.Add(-time.Second), now.Add(-time.Second), 12.4964, 41.9028)

	srv := streamServer(t, h, roomyWindows)
	ch, cancel, resp := openStream(t, srv.URL+"/v1/stream", "banana", viewer)
	defer cancel()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream with an unparseable cursor = %d, want 200", resp.StatusCode)
	}

	// The full snapshot: the located device's position (with an id) and the unlocated device's
	// three-key entry. A resume would have replayed nothing at all.
	pos := waitFor(t, ch, 3*time.Second, "the snapshot position", func(e sseEvent) bool {
		return e.event == "position" && decodePayload(t, e).DeviceID == device
	})
	if pos.id == "" {
		t.Error("the snapshot position carried no id")
	}
	waitFor(t, ch, 3*time.Second, "the unlocated snapshot entry", func(e sseEvent) bool {
		return e.event == "presentation" && decodePayload(t, e).DeviceID == unset
	})
}

// positionOnlyConsumer is AC34 and AC18's test: a consumer that handles ONLY `position` events sees
// exactly the event set it would have seen before this change, with unchanged ids and unchanged
// resume behaviour — even though the device crossed an age boundary while it was connected.
func positionOnlyConsumer(t *testing.T, h *harness) {
	familyID, viewer := newFamilyWithViewer(t, h, "legacy")
	device := h.addDevice(t, familyID, "legacy-phone", "legacy-device")
	arrived := time.Now()
	h.seedFixAt(t, device, arrived, arrived, 12.4964, 41.9028)

	srv := streamServer(t, h, tightWindows)
	ch, cancel, _ := openStream(t, srv.URL+"/v1/stream", "", viewer)

	first := waitFor(t, ch, 3*time.Second, "the snapshot position", func(e sseEvent) bool {
		return e.event == "position"
	})
	lastID := first.id
	if lastID == "" {
		t.Fatal("the snapshot position carried no id")
	}

	// Age it across a boundary with no fix arriving. A position-only consumer discards whatever the
	// transition produced; what matters is that the cursor it echoes back is still `lastID`.
	waitFor(t, ch, 4*time.Second, "the age transition", func(e sseEvent) bool {
		return e.event == "presentation"
	})
	cancel() // the connection drops.

	// A new position arrives while it is away.
	moved := time.Now()
	h.seedFixAt(t, device, moved, moved, 9.19, 45.4642)

	ch2, cancel2, resp := openStream(t, srv.URL+"/v1/stream", lastID, viewer)
	defer cancel2()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resume = %d, want 200", resp.StatusCode)
	}

	replayed := waitFor(t, ch2, 3*time.Second, "the position that arrived while away", func(e sseEvent) bool {
		return e.event == "position"
	})
	p := decodePayload(t, replayed)
	if p.Lon == nil || *p.Lon < 9.18 || *p.Lon > 9.20 {
		t.Errorf("resumed position lon = %v, want ~9.19 — the move made while away was skipped (a gap)", p.Lon)
	}
	prev, _ := strconv.ParseInt(lastID, 10, 64)
	cur, _ := strconv.ParseInt(replayed.id, 10, 64)
	if cur <= prev {
		t.Errorf("resumed id %d is not greater than the cursor %d — the cursor is not monotonic", cur, prev)
	}
	// And no SECOND position event: the boundary row was not re-delivered.
	for {
		select {
		case e, ok := <-ch2:
			if !ok {
				return
			}
			if e.event == "position" {
				t.Fatalf("a second position event arrived on resume: %+v — the boundary row was re-delivered", e)
			}
		case <-time.After(400 * time.Millisecond):
			return
		}
	}
}

// datastoreLossOnAnOpenStream is AC23: the datastore goes away while a stream is open, and the
// watcher is told with an explicit `error` event rather than being left with a frozen family
// presented as current. Nothing derived from data the server can no longer read follows it.
func datastoreLossOnAnOpenStream(t *testing.T, h *harness) {
	familyID, viewer := newFamilyWithViewer(t, h, "loss")
	device := h.addDevice(t, familyID, "loss-phone", "loss-device")
	arrived := time.Now()
	h.seedFixAt(t, device, arrived, arrived, 12.4964, 41.9028)

	failing := &atomic.Bool{}
	// Tight windows so that, if the server WERE still evaluating from data it cannot read, the
	// device would cross a boundary during this test and the events would show up.
	srv := httptest.NewServer(server.New(flakyDB{DB: h.pool, failing: failing}, nil, tightWindows, discardLogger()))
	t.Cleanup(srv.Close)

	ch, cancel, resp := openStream(t, srv.URL+"/v1/stream", "", viewer)
	defer cancel()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream open = %d, want 200", resp.StatusCode)
	}
	waitFor(t, ch, 3*time.Second, "the snapshot position", func(e sseEvent) bool {
		return e.event == "position" && decodePayload(t, e).DeviceID == device
	})

	failing.Store(true)

	errEvent := waitFor(t, ch, 3*time.Second, "the error event", func(e sseEvent) bool {
		return e.event == "error"
	})
	if errEvent.id != "" {
		t.Errorf("the error event carried id %q; it must not move the cursor", errEvent.id)
	}
	var body struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(errEvent.data), &body); err != nil {
		t.Fatalf("error event data is not JSON: %v (%q)", err, errEvent.data)
	}
	if body.Error == "" || body.Message == "" {
		t.Errorf("the error event does not name the condition: %q", errEvent.data)
	}

	// While the datastore is unreadable: no presentation events, and no second error event per tick.
	deadline := time.After(1500 * time.Millisecond)
	for done := false; !done; {
		select {
		case e, ok := <-ch:
			if !ok {
				done = true // the connection closed, which AC23 also permits.
				break
			}
			if e.event == "presentation" || e.event == "position" {
				t.Fatalf("the server kept emitting %s events from data it cannot read: %+v", e.event, e)
			}
			if e.event == "error" {
				t.Fatalf("a second error event arrived; the watcher is told once per outage, not once per poll")
			}
		case <-deadline:
			done = true
		}
	}
}

// TestStreamPurgeToNoPosition is AC17's purge branch and half of AC28's joint test: retention purging
// drops the partition holding a device's last fix, so the device becomes `no-position` from that
// instant onward, an unlocated `presentation` event goes out to every open stream, and
// GET /v1/positions returns the identical three-key object in the same window.
//
// It gets its OWN container because it drops partitions, which is a database-wide act: run against a
// shared one it could take another case's history with it.
func TestStreamPurgeToNoPosition(t *testing.T) {
	t.Cleanup(server.SetStreamPollInterval(20 * time.Millisecond))
	h := newHarness(t)

	familyID, viewer := newFamilyWithViewer(t, h, "purge")
	device := h.addDevice(t, familyID, "purged-phone", "purge-device")

	// A fix 45 days old: comfortably inside the ingest window and certainly in a PREVIOUS monthly
	// partition, which is the unit retention drops.
	old := time.Now().AddDate(0, 0, -45)
	h.seedFixAt(t, device, old, old, 12.4964, 41.9028)

	srv := streamServer(t, h, roomyWindows)
	ch, cancel, _ := openStream(t, srv.URL+"/v1/stream", "", viewer)
	defer cancel()

	first := waitFor(t, ch, 3*time.Second, "the snapshot position", func(e sseEvent) bool {
		return e.event == "position" && decodePayload(t, e).DeviceID == device
	})
	if p := decodePayload(t, first); p.Lon == nil {
		t.Fatalf("the device was not plotted before the purge: %+v", p)
	}

	// The purge, through the real mechanism: drop every partition whose entire range predates this
	// month. This is what internal/retention's job does on its timer.
	now := time.Now().UTC()
	cutoff := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	dropped, err := db.DropPartitionsBefore(context.Background(), h.pool, cutoff)
	if err != nil {
		t.Fatalf("DropPartitionsBefore: %v", err)
	}
	if len(dropped) == 0 {
		t.Fatalf("the purge dropped nothing; the fix at %s was not in an older partition", old.Format(time.RFC3339))
	}

	gone := waitFor(t, ch, 3*time.Second, "the purge-driven transition to no-position", func(e sseEvent) bool {
		return e.event == "presentation" && decodePayload(t, e).DeviceID == device
	})
	if gone.id != "" {
		t.Errorf("the purge event carried id %q", gone.id)
	}
	streamKeys := jsonKeys(t, gone.data)
	if len(streamKeys) != 3 {
		t.Errorf("the purge event carries %d keys (%v), want the three-key unlocated entry", len(streamKeys), streamKeys)
	}
	if p := decodePayload(t, gone); p.Presentation != "no-position" {
		t.Errorf("the purged device is %q, want no-position — it is not `stale`, it has no fix at all", p.Presentation)
	}

	// The read surface, in the same window, says exactly the same thing — the identical object.
	body := h.get(t, "/v1/positions", viewer).Body.Bytes()
	var read []json.RawMessage
	if err := json.Unmarshal(body, &read); err != nil {
		t.Fatalf("positions body: %v (%q)", err, string(body))
	}
	if len(read) != 1 {
		t.Fatalf("positions returned %d entries, want 1 (the device still EXISTS; only its fixes are gone)", len(read))
	}
	var fromRead, fromStream map[string]any
	if err := json.Unmarshal(read[0], &fromRead); err != nil {
		t.Fatalf("read entry: %v", err)
	}
	if err := json.Unmarshal([]byte(gone.data), &fromStream); err != nil {
		t.Fatalf("stream entry: %v", err)
	}
	if len(fromRead) != len(fromStream) {
		t.Fatalf("the surfaces disagree after a purge:\nread:   %v\nstream: %v", fromRead, fromStream)
	}
	for k, v := range fromRead {
		if fromStream[k] != v {
			t.Errorf("key %q is %v on the read surface and %v on the stream", k, v, fromStream[k])
		}
	}
	if strings.Contains(string(read[0]), "lat") || strings.Contains(string(read[0]), "lon") {
		t.Errorf("the purged device still carries a coordinate: %s", read[0])
	}
}
