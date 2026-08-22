// GET /v1/positions after the presentation change: every device in the family, each with exactly one
// state, in one total order — and the unhappy paths that decide whether the new enumeration is safe.
//
// The enumeration is the risky half. A device id and a human-chosen phone name for a device that has
// produced no data at all is a NEW disclosure surface, so the cross-family case is here, not implied.
package server_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NSchatz/tracker/internal/auth"
	"github.com/NSchatz/tracker/internal/db"
	"github.com/NSchatz/tracker/internal/presentation"
	"github.com/NSchatz/tracker/internal/server"
	"github.com/NSchatz/tracker/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// --- helpers -------------------------------------------------------------------------------------

// addDevice enrolls a device with an exact name into an existing family. The ordering fixture needs
// names it chose (`apple`, `APPLE`, two `Banana`s), which the label-derived enroll helper cannot give.
func (h *harness) addDevice(t *testing.T, familyID, name, tokenSeed string) string {
	t.Helper()
	hash := sha256.Sum256([]byte(t.Name() + "/" + tokenSeed))
	id, err := store.CreateDevice(context.Background(), h.pool, familyID, name, hash[:])
	if err != nil {
		t.Fatalf("CreateDevice %q: %v", name, err)
	}
	return id
}

// seedFixAt stores one fix with BOTH clocks chosen by the test: the device's event-time `ts` and the
// server's receive-time `received_at`.
//
// The ingest path stamps received_at from the database's own clock, which is right for production and
// useless here: the criteria this file proves are about instants that differ by exactly 61 minutes or
// exactly 900 seconds, and a test that waited for them would take an hour and still race. Writing the
// row directly is how "this fix arrived 61 minutes ago" becomes an input rather than a wait.
func (h *harness) seedFixAt(t *testing.T, deviceID string, ts, receivedAt time.Time, lon, lat float64) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.EnsureMonthlyPartition(ctx, h.pool, ts); err != nil {
		t.Fatalf("EnsureMonthlyPartition for %s: %v", ts.Format(time.RFC3339), err)
	}
	if _, err := h.pool.Exec(ctx,
		`INSERT INTO fixes (device_id, ts, location, received_at)
		 VALUES ($1, $2, ST_Point($3, $4, 4326)::geography, $5)`,
		deviceID, ts.UTC(), lon, lat, receivedAt.UTC()); err != nil {
		t.Fatalf("seed fix (ts %s, received_at %s): %v", ts.Format(time.RFC3339), receivedAt.Format(time.RFC3339), err)
	}
}

// entry is a maximally tolerant decode of one wire entry: every key optional, so a test can assert
// what is present AND what is absent. json.RawMessage of the whole object comes along so the
// three-key shape can be checked as an object rather than as a struct that silently drops keys.
type entry struct {
	DeviceID      string   `json:"device_id"`
	DeviceName    string   `json:"device_name"`
	Presentation  string   `json:"presentation"`
	Lat           *float64 `json:"lat"`
	Lon           *float64 `json:"lon"`
	TS            *int64   `json:"ts"`
	ReceivedAt    *int64   `json:"received_at"`
	LastContactAt *int64   `json:"last_contact_at"`
}

func decodeEntries(t *testing.T, body []byte) []entry {
	t.Helper()
	var out []entry
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("positions body is not a JSON array of entries: %v (%q)", err, string(body))
	}
	return out
}

// keysOf returns the JSON keys of one object in the array, so "exactly these three keys and nothing
// else" is assertable rather than approximated by checking a few fields are zero.
func keysOf(t *testing.T, body []byte, index int) []string {
	t.Helper()
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("body is not a JSON array of objects: %v (%q)", err, string(body))
	}
	if index >= len(raw) {
		t.Fatalf("asked for entry %d of %d", index, len(raw))
	}
	keys := make([]string, 0, len(raw[index]))
	for k := range raw[index] {
		keys = append(keys, k)
	}
	return keys
}

// flakyDB wraps a real database and can be told to start failing every multi-row QUERY, while leaving
// QueryRow (the token lookup) working — so a test reaches an authenticated handler and then finds the
// datastore unreadable, which is the actual failure being modelled. A stub that failed everything
// would only ever produce a 401.
type flakyDB struct {
	server.DB
	failing *atomic.Bool
}

var errDatastoreUnavailable = errors.New("datastore unavailable (injected)")

func (f flakyDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if f.failing.Load() {
		return nil, errDatastoreUnavailable
	}
	return f.DB.Query(ctx, sql, args...)
}

func (f flakyDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return f.DB.Exec(ctx, sql, args...)
}

// --- the suite -----------------------------------------------------------------------------------

// TestPositionsPresentation is the whole read-side presentation suite on one shared container, each
// case isolated in its own family — family being the authorization boundary.
func TestPositionsPresentation(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	t.Run("every device appears, with exactly one state", func(t *testing.T) { positionsEnumeratesEveryone(t, h) })
	t.Run("an unlocated entry is exactly three keys", func(t *testing.T) { unlocatedEntryShape(t, h) })
	t.Run("the total order folds case and breaks ties deterministically", func(t *testing.T) { positionsTotalOrder(t, h) })
	t.Run("age is last contact, not the current position's arrival", func(t *testing.T) { backlogGapFill(t, h) })
	t.Run("a future-ts device keeps reporting and stays live", func(t *testing.T) { futureTimestampDevice(t, h) })
	t.Run("clock skew is live, never a negative age", func(t *testing.T) { clockSkewIsLive(t, h) })
	t.Run("an empty family is [] and a family with no fixes is not", func(t *testing.T) { emptyFamilyIsEmptyArray(t, h) })
	t.Run("a refused credential discloses no device", func(t *testing.T) { refusedCredentialDisclosesNothing(t, h) })
	t.Run("never-reported devices stay inside their family", func(t *testing.T) { neverReportedIsFamilyScoped(t, h) })
	t.Run("a located entry still decodes against today's six keys", func(t *testing.T) { locatedEntryStaysCompatible(t, h) })
	t.Run("a datastore failure returns no partial list", func(t *testing.T) { datastoreFailureIsNotPartial(t, h) })
}

// positionsEnumeratesEveryone is AC7's totality: one entry per device in the family, including the
// ones tracker holds no fix for, each carrying exactly one of the four values — and all four occur
// over a set holding one of each kind.
func positionsEnumeratesEveryone(t *testing.T, h *harness) {
	ctx := context.Background()
	familyID, err := store.CreateFamily(ctx, h.pool, t.Name())
	if err != nil {
		t.Fatalf("CreateFamily: %v", err)
	}
	viewer := h.addViewer(t, familyID, "totality-viewer")

	now := time.Now()
	// One device per kind. The ages are exact inputs, not waits.
	never := h.addDevice(t, familyID, "a-never-reported", "never")
	live := h.addDevice(t, familyID, "b-live", "live")
	recent := h.addDevice(t, familyID, "c-recent", "recent")
	stale := h.addDevice(t, familyID, "d-stale", "stale")

	h.seedFixAt(t, live, now.Add(-30*time.Second), now.Add(-30*time.Second), 12.4964, 41.9028)
	h.seedFixAt(t, recent, now.Add(-500*time.Second), now.Add(-500*time.Second), 9.19, 45.4642)
	h.seedFixAt(t, stale, now.Add(-2*time.Hour), now.Add(-2*time.Hour), 2.3522, 48.8566)

	rec := h.get(t, "/v1/positions", viewer)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/positions = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	got := decodeEntries(t, rec.Body.Bytes())
	if len(got) != 4 {
		t.Fatalf("got %d entries, want one per device (4): %+v", len(got), got)
	}

	want := map[string]string{
		never:  "no-position",
		live:   "live",
		recent: "recent",
		stale:  "stale",
	}
	seen := map[string]int{}
	for _, e := range got {
		w, known := want[e.DeviceID]
		if !known {
			t.Fatalf("entry for an unexpected device %s", e.DeviceID)
		}
		if e.Presentation != w {
			t.Errorf("device %s presented as %q, want %q", e.DeviceName, e.Presentation, w)
		}
		seen[e.DeviceID]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("device %s appeared %d times; each device carries exactly one entry", id, n)
		}
	}
	if len(seen) != 4 {
		t.Errorf("only %d of 4 devices appeared — the enumeration is not total", len(seen))
	}
}

// unlocatedEntryShape is AC8: the entry for a device tracker holds no fix for is EXACTLY
// {device_id, device_name, presentation} — no zeros, no nulls, no fabricated location.
func unlocatedEntryShape(t *testing.T, h *harness) {
	ctx := context.Background()
	familyID, err := store.CreateFamily(ctx, h.pool, t.Name())
	if err != nil {
		t.Fatalf("CreateFamily: %v", err)
	}
	viewer := h.addViewer(t, familyID, "shape-viewer")
	h.addDevice(t, familyID, "unset-phone", "shape")

	rec := h.get(t, "/v1/positions", viewer)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/positions = %d, want 200", rec.Code)
	}

	keys := keysOf(t, rec.Body.Bytes(), 0)
	if len(keys) != 3 {
		t.Fatalf("unlocated entry carries %d keys (%v), want exactly 3", len(keys), keys)
	}
	for _, forbidden := range []string{"lat", "lon", "ts", "received_at", "last_contact_at"} {
		for _, k := range keys {
			if k == forbidden {
				t.Errorf("unlocated entry carries %q; `0,0` must be unrepresentable, not merely unused", forbidden)
			}
		}
	}
	got := decodeEntries(t, rec.Body.Bytes())
	if got[0].Presentation != "no-position" {
		t.Errorf("presentation = %q, want no-position", got[0].Presentation)
	}
}

// positionsTotalOrder is AC7's ordering fixture, verbatim: `apple`, `Banana`, `Banana` (two ids) and
// `APPLE` come back as APPLE, apple, then the two Bananas by ascending device id — and two
// consecutive requests are BYTE-IDENTICAL, which is what makes the order a contract rather than
// whatever the planner returned.
func positionsTotalOrder(t *testing.T, h *harness) {
	ctx := context.Background()
	familyID, err := store.CreateFamily(ctx, h.pool, t.Name())
	if err != nil {
		t.Fatalf("CreateFamily: %v", err)
	}
	viewer := h.addViewer(t, familyID, "order-viewer")

	// Deliberately enrolled out of order, and one of them located, so neither insertion order nor
	// "located devices first" could accidentally produce the right answer.
	h.addDevice(t, familyID, "apple", "apple-lower")
	banana1 := h.addDevice(t, familyID, "Banana", "banana-1")
	banana2 := h.addDevice(t, familyID, "Banana", "banana-2")
	h.addDevice(t, familyID, "APPLE", "apple-upper")
	now := time.Now()
	h.seedFixAt(t, banana2, now.Add(-time.Minute), now.Add(-time.Minute), 12.0, 41.0)

	firstBanana, secondBanana := banana1, banana2
	if banana2 < banana1 {
		firstBanana, secondBanana = banana2, banana1
	}

	rec := h.get(t, "/v1/positions", viewer)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/positions = %d, want 200", rec.Code)
	}
	got := decodeEntries(t, rec.Body.Bytes())
	if len(got) != 4 {
		t.Fatalf("got %d entries, want 4", len(got))
	}

	wantNames := []string{"APPLE", "apple", "Banana", "Banana"}
	for i, w := range wantNames {
		if got[i].DeviceName != w {
			t.Fatalf("entry %d is %q, want %q (order: folded name, then raw bytes, then device id) — full order %v",
				i, got[i].DeviceName, w, namesOf(got))
		}
	}
	if got[2].DeviceID != firstBanana || got[3].DeviceID != secondBanana {
		t.Errorf("the two Banana rows are ordered %s, %s; want ascending device id %s, %s",
			got[2].DeviceID, got[3].DeviceID, firstBanana, secondBanana)
	}

	// Byte-identical on a second request. Only `last_contact_at` and the entries' contents could
	// drift, and neither may: nothing about the family changed between the two calls.
	again := h.get(t, "/v1/positions", viewer)
	if again.Body.String() != rec.Body.String() {
		t.Errorf("two consecutive requests differ:\n%s\n%s", rec.Body.String(), again.Body.String())
	}
}

func namesOf(entries []entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.DeviceName)
	}
	return out
}

// backlogGapFill is AC10, the criterion that discriminates the two readings of age.
//
// Device holds two fixes: P, with an event-time 62 minutes in the past, which ARRIVED 61 minutes ago
// and is the current position; and Q, an older event-time (90 minutes back) that arrived 60 SECONDS
// ago as an offline backlog gap-fill. The device is plainly alive. Age measured off the current
// position's own arrival would call it `stale`; age measured off max(received_at) — last contact —
// calls it `live`, and P's coordinates are still what a viewer sees.
func backlogGapFill(t *testing.T, h *harness) {
	ctx := context.Background()
	familyID, err := store.CreateFamily(ctx, h.pool, t.Name())
	if err != nil {
		t.Fatalf("CreateFamily: %v", err)
	}
	viewer := h.addViewer(t, familyID, "backlog-viewer")
	device := h.addDevice(t, familyID, "backlog-phone", "backlog")

	now := time.Now().Truncate(time.Second)
	pTS := now.Add(-62 * time.Minute)
	pReceived := now.Add(-61 * time.Minute)
	qTS := now.Add(-90 * time.Minute)
	qReceived := now.Add(-60 * time.Second)

	h.seedFixAt(t, device, pTS, pReceived, 12.4964, 41.9028) // the current position: newest ts
	h.seedFixAt(t, device, qTS, qReceived, 9.19, 45.4642)    // older ts, freshest arrival

	rec := h.get(t, "/v1/positions", viewer)
	got := decodeEntries(t, rec.Body.Bytes())
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	e := got[0]

	if e.Presentation != "live" {
		t.Errorf("presentation = %q, want live: last contact is Q's arrival, 60 seconds ago. "+
			"(%q is what measuring age off the CURRENT POSITION's arrival would give.)", e.Presentation, "stale")
	}
	if e.Lon == nil || *e.Lon < 12.49 || *e.Lon > 12.50 {
		t.Errorf("lon = %v, want ~12.4964 — the current position is still P, by newest ts", e.Lon)
	}
	if e.TS == nil || *e.TS != pTS.Unix() {
		t.Errorf("ts = %v, want P's %d", e.TS, pTS.Unix())
	}
	if e.ReceivedAt == nil || *e.ReceivedAt != pReceived.Unix() {
		t.Errorf("received_at = %v, want P's own arrival %d (unchanged meaning)", e.ReceivedAt, pReceived.Unix())
	}
	// Equality of INSTANT: both facts are epoch seconds here, so this compares the same unit on both
	// sides rather than making a comparison true by redefining a field.
	if e.LastContactAt == nil || *e.LastContactAt != qReceived.Unix() {
		t.Errorf("last_contact_at = %v, want Q's arrival %d — the two keys are different facts",
			e.LastContactAt, qReceived.Unix())
	}
}

// futureTimestampDevice is AC36: a phone with a fast clock reports a fix whose ts is in the FUTURE,
// so every later fix has a smaller ts and the current position never changes. The device keeps
// reporting, so it is `live` — a reporting device is never `stale` — while ts and received_at stay
// pinned to the future-ts fix and last_contact_at advances with each arrival.
func futureTimestampDevice(t *testing.T, h *harness) {
	ctx := context.Background()
	familyID, err := store.CreateFamily(ctx, h.pool, t.Name())
	if err != nil {
		t.Fatalf("CreateFamily: %v", err)
	}
	viewer := h.addViewer(t, familyID, "future-viewer")
	device := h.addDevice(t, familyID, "fast-clock-phone", "future")

	now := time.Now().Truncate(time.Second)
	futureTS := now.Add(3 * time.Hour) // the phone's clock is three hours fast.
	futureArrived := now.Add(-40 * time.Minute)
	h.seedFixAt(t, device, futureTS, futureArrived, 12.4964, 41.9028)

	// Forty minutes of honest reporting since: every fix has a SMALLER ts, so none becomes the
	// current position, and each one only moves last contact.
	h.seedFixAt(t, device, now.Add(-20*time.Minute), now.Add(-20*time.Minute), 9.19, 45.4642)
	h.seedFixAt(t, device, now.Add(-10*time.Second), now.Add(-10*time.Second), 2.3522, 48.8566)

	got := decodeEntries(t, h.get(t, "/v1/positions", viewer).Body.Bytes())
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	e := got[0]
	if e.Presentation != "live" {
		t.Errorf("a device that has reported ten seconds ago is %q; a REPORTING device is never stale", e.Presentation)
	}
	if e.TS == nil || *e.TS != futureTS.Unix() {
		t.Errorf("ts = %v, want the future-ts fix's %d (the current position never changed)", e.TS, futureTS.Unix())
	}
	if e.ReceivedAt == nil || *e.ReceivedAt != futureArrived.Unix() {
		t.Errorf("received_at = %v, want the future-ts fix's own arrival %d", e.ReceivedAt, futureArrived.Unix())
	}
	if e.LastContactAt == nil || *e.LastContactAt != now.Add(-10*time.Second).Unix() {
		t.Errorf("last_contact_at = %v, want the newest ARRIVAL %d — it advances with each report",
			e.LastContactAt, now.Add(-10*time.Second).Unix())
	}
}

// clockSkewIsLive is AC11 over HTTP: last contact AHEAD of the server clock is `live`, with no error,
// no omission, and nothing negative on the wire.
func clockSkewIsLive(t *testing.T, h *harness) {
	ctx := context.Background()
	familyID, err := store.CreateFamily(ctx, h.pool, t.Name())
	if err != nil {
		t.Fatalf("CreateFamily: %v", err)
	}
	viewer := h.addViewer(t, familyID, "skew-viewer")
	device := h.addDevice(t, familyID, "skewed-phone", "skew")

	now := time.Now().Truncate(time.Second)
	ahead := now.Add(90 * time.Minute)
	h.seedFixAt(t, device, now.Add(-time.Minute), ahead, 12.4964, 41.9028)

	rec := h.get(t, "/v1/positions", viewer)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/positions with a skewed clock = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	got := decodeEntries(t, rec.Body.Bytes())
	if len(got) != 1 {
		t.Fatalf("a skewed device was omitted: %d entries", len(got))
	}
	if got[0].Presentation != "live" {
		t.Errorf("presentation = %q, want live", got[0].Presentation)
	}
	if strings.Contains(rec.Body.String(), "-") && got[0].LastContactAt != nil && *got[0].LastContactAt < 0 {
		t.Errorf("last_contact_at = %d, want a real instant, never a negative age", *got[0].LastContactAt)
	}
}

// emptyFamilyIsEmptyArray is AC12 and the distinction it insists on: no devices is `[]`; devices
// with no fixes is one unlocated entry each, which is a completely different fact.
func emptyFamilyIsEmptyArray(t *testing.T, h *harness) {
	ctx := context.Background()
	empty, err := store.CreateFamily(ctx, h.pool, t.Name()+"/empty")
	if err != nil {
		t.Fatalf("CreateFamily: %v", err)
	}
	emptyViewer := h.addViewer(t, empty, "empty-viewer")
	rec := h.get(t, "/v1/positions", emptyViewer)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/positions (no devices) = %d, want 200", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
		t.Fatalf("a family with no devices returned %q, want [] (never null)", body)
	}

	unset, err := store.CreateFamily(ctx, h.pool, t.Name()+"/unset")
	if err != nil {
		t.Fatalf("CreateFamily: %v", err)
	}
	unsetViewer := h.addViewer(t, unset, "unset-viewer")
	h.addDevice(t, unset, "never-reported", "never-reported-empty")
	got := decodeEntries(t, h.get(t, "/v1/positions", unsetViewer).Body.Bytes())
	if len(got) != 1 || got[0].Presentation != "no-position" {
		t.Fatalf("a family with a device but no fixes returned %+v, want one no-position entry", got)
	}
}

// refusedCredentialDisclosesNothing is AC13: no token, a malformed token, an unknown token and a
// DEVICE (write) token are all 401 — and the body names no device, no name and no state. The last
// part matters now that never-reported devices are enumerable at all.
func refusedCredentialDisclosesNothing(t *testing.T, h *harness) {
	deviceID, deviceToken := h.enroll(t, "disclose")
	familyID := h.familyOf(t, deviceID)
	named := h.addDevice(t, familyID, "secret-phone-name", "disclose-named")

	for _, tok := range []auth.Token{"", "not a real token", "unknown-token-value", deviceToken} {
		rec := h.get(t, "/v1/positions", tok)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("token %q = %d, want 401", tok, rec.Code)
		}
		body := rec.Body.String()
		for _, secret := range []string{deviceID, named, "secret-phone-name", "no-position", "live", "recent", "stale"} {
			if strings.Contains(body, secret) {
				t.Errorf("a 401 body disclosed %q: %s", secret, body)
			}
		}
	}
}

// neverReportedIsFamilyScoped is AC14 on the new surface. Two families, each holding exactly one
// never-reported device: neither viewer's response may contain the other family's device id or name.
// This is the criterion that scopes the new disclosure.
func neverReportedIsFamilyScoped(t *testing.T, h *harness) {
	ctx := context.Background()
	famA, err := store.CreateFamily(ctx, h.pool, t.Name()+"/A")
	if err != nil {
		t.Fatalf("CreateFamily A: %v", err)
	}
	famB, err := store.CreateFamily(ctx, h.pool, t.Name()+"/B")
	if err != nil {
		t.Fatalf("CreateFamily B: %v", err)
	}
	viewerA := h.addViewer(t, famA, "scope-viewer-a")
	viewerB := h.addViewer(t, famB, "scope-viewer-b")
	deviceA := h.addDevice(t, famA, "alices-unset-phone", "scope-a")
	deviceB := h.addDevice(t, famB, "bobs-unset-phone", "scope-b")

	bodyA := h.get(t, "/v1/positions", viewerA).Body.String()
	bodyB := h.get(t, "/v1/positions", viewerB).Body.String()

	if !strings.Contains(bodyA, deviceA) || !strings.Contains(bodyB, deviceB) {
		t.Fatalf("a family did not see its own never-reported device.\nA: %s\nB: %s", bodyA, bodyB)
	}
	for _, leak := range []string{deviceB, "bobs-unset-phone"} {
		if strings.Contains(bodyA, leak) {
			t.Errorf("viewer A's positions disclosed family B's %q: %s", leak, bodyA)
		}
	}
	for _, leak := range []string{deviceA, "alices-unset-phone"} {
		if strings.Contains(bodyB, leak) {
			t.Errorf("viewer B's positions disclosed family A's %q: %s", leak, bodyB)
		}
	}
}

// locatedEntryStaysCompatible is AC33: a decoder that REQUIRES today's six keys and TOLERATES unknown
// ones still decodes a located entry. That is the compatibility promise — keys were added, none was
// renamed, retyped, re-united or removed.
//
// The decoder is scoped to a LOCATED ENTRY, which is the object AC33 governs: on GET /v1/positions
// and on `position` events. The four-key `presentation` event payload is a partial update about a
// device whose position has NOT changed, and it is deliberately not a located entry.
func locatedEntryStaysCompatible(t *testing.T, h *harness) {
	ctx := context.Background()
	familyID, err := store.CreateFamily(ctx, h.pool, t.Name())
	if err != nil {
		t.Fatalf("CreateFamily: %v", err)
	}
	viewer := h.addViewer(t, familyID, "compat-viewer")
	device := h.addDevice(t, familyID, "compat-phone", "compat")
	now := time.Now().Truncate(time.Second)
	h.seedFixAt(t, device, now.Add(-time.Minute), now.Add(-time.Minute), 12.4964, 41.9028)

	body := h.get(t, "/v1/positions", viewer).Body.Bytes()
	var legacy []legacyPosition
	if err := json.Unmarshal(body, &legacy); err != nil {
		t.Fatalf("a pre-change decoder failed on a located entry: %v (%q)", err, string(body))
	}
	if len(legacy) != 1 {
		t.Fatalf("decoded %d entries, want 1", len(legacy))
	}
	if err := legacy[0].requireTodaysKeys(body); err != nil {
		t.Fatal(err)
	}
	if legacy[0].Lon < 12.49 || legacy[0].Lon > 12.50 || legacy[0].Lat < 41.90 || legacy[0].Lat > 41.91 {
		t.Errorf("(lon,lat) = (%v,%v), want ~(12.4964, 41.9028) — axis order and meaning survived",
			legacy[0].Lon, legacy[0].Lat)
	}
	if legacy[0].TS != now.Add(-time.Minute).Unix() || legacy[0].ReceivedAt != now.Add(-time.Minute).Unix() {
		t.Errorf("ts/received_at = %d/%d, want epoch seconds unchanged", legacy[0].TS, legacy[0].ReceivedAt)
	}
}

// legacyPosition is the entry shape as it stood BEFORE this change: exactly the six keys, decoded
// tolerantly (encoding/json ignores unknown fields by default, which is what a tolerant consumer
// does). A strict-unknown-field decoder fails here by construction — `presentation` and
// `last_contact_at` are now always present — and the repair for such a consumer is the decoder, never
// withholding a key.
type legacyPosition struct {
	DeviceID   string  `json:"device_id"`
	DeviceName string  `json:"device_name"`
	Lat        float64 `json:"lat"`
	Lon        float64 `json:"lon"`
	TS         int64   `json:"ts"`
	ReceivedAt int64   `json:"received_at"`
}

// requireTodaysKeys asserts every one of the six is actually PRESENT in the JSON, not merely absent
// and defaulted to a zero by the decoder.
func (legacyPosition) requireTodaysKeys(body []byte) error {
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return err
	}
	for _, key := range []string{"device_id", "device_name", "lat", "lon", "ts", "received_at"} {
		if _, ok := raw[0][key]; !ok {
			return errors.New("a located entry no longer carries the key " + key + ": " + string(body))
		}
	}
	return nil
}

// datastoreFailureIsNotPartial is AC22: the query behind GET /v1/positions fails, so the answer is a
// server error with NO device list at all. A device the server could not evaluate is not reported —
// and above all not reported as `live`.
func datastoreFailureIsNotPartial(t *testing.T, h *harness) {
	ctx := context.Background()
	familyID, err := store.CreateFamily(ctx, h.pool, t.Name())
	if err != nil {
		t.Fatalf("CreateFamily: %v", err)
	}
	viewer := h.addViewer(t, familyID, "failure-viewer")
	device := h.addDevice(t, familyID, "failure-phone", "failure")
	now := time.Now()
	h.seedFixAt(t, device, now.Add(-time.Minute), now.Add(-time.Minute), 12.4964, 41.9028)

	failing := &atomic.Bool{}
	handler := server.New(flakyDB{DB: h.pool, failing: failing}, nil,
		presentation.Windows{LiveSeconds: 120, StaleSeconds: 900}, discardLogger())

	failing.Store(true)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/positions", nil)
	req.Header.Set("Authorization", "Bearer "+string(viewer))
	handler.ServeHTTP(rec, req)

	if rec.Code < 500 {
		t.Fatalf("GET /v1/positions with an unreadable datastore = %d, want a server error", rec.Code)
	}
	body := rec.Body.String()
	for _, forbidden := range []string{device, "failure-phone", "live", "recent", "stale", "no-position"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("a failed read returned %q in its body — no partial list, no fabricated value: %s", forbidden, body)
		}
	}
}
