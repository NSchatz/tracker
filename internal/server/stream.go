package server

// The live-map surface. GET /v1/stream is a Server-Sent Events feed of a family's positions AND of
// their presentation state; GET /map is a vendored page that consumes it. A watcher opens one
// long-lived connection and the server pushes what changed.
//
// Four properties are load-bearing and each is pinned by a test (stream_test.go, presentation_stream_test.go):
//
//   - A watcher only ever receives its OWN family's stream. Every query behind it is family-scoped in
//     its SQL, so there is no code path that could emit another family's device to this connection.
//   - Every `position` event carries an `id` (the fix's received_at, in microseconds) and a dropped
//     connection resumes from `Last-Event-ID` with NO GAPS. That guarantee is UNCHANGED by the
//     presentation work, which is why `presentation` events deliberately carry no `id` at all: they
//     cannot advance the cursor a client echoes back, so a consumer that handles only `position`
//     events sees exactly the event set it saw before this feature existed.
//   - The stream is a POSITION stream, not a breadcrumb replay: it pushes a device's current position
//     when that position changes, coalescing a burst of fixes to the newest.
//   - A device's presentation state changes for reasons that are NOT position changes — time passing
//     across an age boundary, a backlogged fix refreshing last contact, retention purging deleting
//     fixes — and every one of those is announced within the sweep bound B. See streamConn.diff.

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/NSchatz/tracker/internal/auth"
	"github.com/NSchatz/tracker/internal/presentation"
	"github.com/NSchatz/tracker/internal/store"
)

// staticFS carries the vendored Leaflet library and the map page into the binary, so the server is
// self-sufficient: a family member's browser loads the map's JavaScript from the tracker they trust,
// never from a third-party CDN. That is the point for a self-hosted privacy product — the alternative
// leaks who is watching to whoever hosts the script. (Map TILES are still fetched from OpenStreetMap;
// see map.html for that honest caveat. The markers move with or without tiles.)
//
//go:embed static
var staticFS embed.FS

// streamPollInterval is how often an open stream re-queries the database for new positions and
// re-evaluates every device's presentation value. It is a var, not a const, only so the tests can
// shrink it — production polls at this cadence.
//
// A push architecture (LISTEN/NOTIFY or an in-process broker) would cut the latency further, but it
// would also break the stateless-replica story the roadmap keeps (§1) — a fix ingested on one replica
// must still reach a watcher on another, and a database poll is what makes that true for free.
var streamPollInterval = 1 * time.Second

// streamPollTick is the cadence ONE open stream actually polls at: the interval above, but never
// slower than HALF the sweep bound B for the windows this server is running.
//
// Why half and not B itself. The contract says a time-driven transition is announced "no later than B
// seconds after the change", and a change lands at an arbitrary point INSIDE a poll period: a crossing
// one instant after a poll waits a whole period, plus the query, before it is seen. So a period equal
// to B misses the bound by whatever the query cost is. B is floored at 1 second and the ordinary
// interval IS one second, so under every wide pair — the 120/900 defaults give B=60 — this returns the
// unchanged one-second cadence. It only bites the tightest legal pairs, (1,2) and (2,3), where B is 1:
// there it polls twice a second, so the worst case is half a bound plus a query rather than a whole
// one. Sub-second windows are a deliberate operator choice and pay for themselves in query load.
func streamPollTick(w presentation.Windows) time.Duration {
	tick := streamPollInterval
	if half := time.Duration(presentation.SweepBound(w)) * time.Second / 2; half > 0 && half < tick {
		tick = half
	}
	return tick
}

// streamHeartbeatInterval bounds how long a quiet stream goes without writing anything. A periodic
// SSE comment keeps intermediaries from timing out an idle connection and is how the server learns a
// watcher has silently gone away (the write fails). It is unrelated to event latency — a real change
// is sent as soon as the next poll finds it.
const streamHeartbeatInterval = 15 * time.Second

// The three event types, and there are exactly three (SPEC.md, "The live stream").
//
// `position` is unchanged from S4 and is the ONLY one that carries an id. `presentation` says a
// device's state changed with no change to its position, and carries no id BY DESIGN. `error` says
// the server can no longer read the data behind the stream, so a map never renders a frozen family as
// though it were current.
const (
	eventPosition     = "position"
	eventPresentation = "presentation"
	eventError        = "error"
)

// streamErrorPayload is the body of an `error` event: a machine-readable condition and a sentence.
// It names the condition rather than the underlying database error, which is ours and not the
// watcher's.
type streamErrorPayload struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// getStream serves GET /v1/stream: an SSE feed of the caller's family's positions and presentation
// state.
//
// It authenticates the viewer ITSELF rather than sitting behind the requireViewer middleware, for one
// hard reason: the browser EventSource API cannot set an Authorization header, so a viewer token has
// to arrive in the query string for the live map to work at all. That is a real, documented trade
// (SPEC.md) — a token in a URL is visible in logs and referrers — mitigated by TLS (S7) and retired
// by the in-app map (C5), which can set headers. A programmatic caller may still use the header.
func getStream(database DB, windows presentation.Windows, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			// Without flushing, "streaming" would buffer until the handler returned — i.e. never.
			// Every real ResponseWriter (and httptest's recorder) is a Flusher; this only trips on a
			// wrapper that swallowed it, and refusing loudly is better than a silently dead stream.
			writeError(w, logger, http.StatusInternalServerError, "internal", "streaming unsupported")
			return
		}

		v, ok := authViewerForStream(w, r, database, logger)
		if !ok {
			return // authViewerForStream has already written the 401/500.
		}

		cursor, resuming := parseResumeCursor(r)

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		// Defeat proxy buffering (nginx and friends), which would otherwise hold events until the
		// buffer filled and make a live map lag by an arbitrary amount.
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		flusher.Flush() // let the client's EventSource fire `open` before the first poll.

		conn := &streamConn{
			database:      database,
			logger:        logger,
			familyID:      v.FamilyID,
			windows:       windows,
			since:         cursor,
			cursorInstant: cursor,
			resuming:      resuming,
			w:             w,
			flusher:       flusher,
		}

		ctx := r.Context()
		ticker := time.NewTicker(streamPollTick(windows))
		defer ticker.Stop()

		// Deliver the first pass immediately rather than waiting a full interval, so the map paints
		// at once instead of after the first tick.
		if err := conn.poll(ctx); err != nil {
			return
		}

		for {
			select {
			case <-ctx.Done():
				// The watcher went away (closed the tab, dropped the network). Nothing to clean up
				// beyond the deferred ticker stop — the connection is already gone.
				return
			case <-ticker.C:
				if err := conn.poll(ctx); err != nil {
					return
				}
				if time.Since(conn.lastWrite) >= streamHeartbeatInterval {
					if _, werr := w.Write([]byte(": keep-alive\n\n")); werr != nil {
						return // the watcher is gone; stop.
					}
					flusher.Flush()
					conn.lastWrite = time.Now()
				}
			}
		}
	}
}

// observation is what ONE POLL SAW in the datastore about one device: whether it held a fix and, if
// so, when it last reached us.
//
// This is the only thing an open connection carries between polls, and what it is NOT is the point.
// It holds no presentation value and no record of what was delivered to this client — every value is
// derived at delivery time from data tracker stores, and nothing about a client is remembered. What
// it does hold is a cached READ of two stored facts, and holding it is what makes a RETENTION PURGE
// detectable at all: a purge deletes the very rows a "recompute the past from storage" comparison
// would have to read, so a server with no memory of the previous read cannot see that a device it was
// plotting has lost its last fix. Comparing consecutive observations sees it in one poll.
type observation struct {
	lastContact *time.Time
}

// streamConn is one open watcher: its cursor, its view of the previous poll, and where to write.
// Everything in it is per-connection, in-process and dies with the connection.
type streamConn struct {
	database DB
	logger   *slog.Logger
	familyID string
	windows  presentation.Windows

	// since is the position cursor: the arrival time of the last `position` event written. Positions
	// strictly after it are unsent. This is the S4 cursor, unchanged, and it ADVANCES as events go
	// out.
	since time.Time

	// cursorInstant is the instant the client's resume cursor encoded, captured before `since` starts
	// advancing. It is the referent the resume sweep compares against, and it must be the instant the
	// CLIENT named — not wherever this connection's cursor has since reached.
	cursorInstant time.Time

	// resuming records that the client arrived with a usable Last-Event-ID, so the first poll owes it
	// the resume sweep rather than a fresh snapshot.
	resuming bool
	// swept marks the resume sweep (or the fresh snapshot) as done, so it runs once per connection.
	swept bool

	// prev is the previous poll's observation of the family, and prevAt is when it was taken.
	// Together they are the "before" side of every presentation comparison.
	prev   map[string]observation
	prevAt time.Time

	// degraded is connection HEALTH, not device state: it records that the last poll could not read
	// the datastore and the watcher has already been told once. It stops an unreadable database
	// producing one `error` event per second.
	degraded bool

	w         http.ResponseWriter
	flusher   http.Flusher
	lastWrite time.Time
}

// poll runs one pass: read the family, write what the watcher is owed, remember what was read.
//
// It returns an error only when the WATCHER is gone (a failed write), which ends the stream. A failed
// QUERY is not that: it is reported to the watcher as an `error` event and retried on the next tick,
// because a transient database blip should not tear down every open map.
func (c *streamConn) poll(ctx context.Context) error {
	states, err := store.FamilyDeviceStates(ctx, c.database, c.familyID)
	if err != nil {
		return c.reportUnavailable(ctx, err)
	}
	// The datastore is readable again (or was never not). Anything sent from here on IS derived from
	// data the server can read, which is precisely what the degraded state was protecting.
	recovered := c.degraded
	c.degraded = false

	at := time.Now()

	// 1. Positions. Every device whose CURRENT position arrived strictly after the cursor, in arrival
	//    order so the ids stay monotonic. This is the S4 contract untouched — same predicate, same
	//    id, same ordering — and it is what a `position`-only consumer sees.
	sortByArrival(states)
	positioned := make(map[string]bool, len(states))
	for i := range states {
		s := states[i]
		if s.Current == nil || !s.Current.ReceivedAt.After(c.since) {
			continue
		}
		if err := c.writeEvent(eventPosition, s.Current.ReceivedAt.UnixMicro(), entryFor(s, at, c.windows)); err != nil {
			return err
		}
		// Advance the cursor to THIS event's arrival, in step with the id just sent. Rows are in
		// arrival order, so this is monotonic and a mid-batch disconnect resumes exactly here.
		c.since = s.Current.ReceivedAt
		positioned[s.DeviceID] = true
	}

	// 2. Presentation. Which devices are owed one depends on whether this is the connection's first
	//    pass (a fresh snapshot, or a resume sweep against the cursor instant) or a steady-state
	//    poll (a comparison against the previous poll).
	sortDeviceStates(states)
	var owed []store.DeviceState
	switch {
	case !c.swept && c.resuming:
		owed, err = c.resumeSweep(ctx, states, at, positioned)
		if err != nil {
			return c.reportUnavailable(ctx, err)
		}
	case !c.swept:
		owed = c.snapshotUnlocated(states, at, positioned)
	case recovered:
		owed = c.reannounce(states, positioned)
	default:
		owed = c.diff(states, at, positioned)
	}
	c.swept = true

	for _, s := range owed {
		// No id: a presentation event must never advance Last-Event-ID. That is what keeps the
		// resume cursor a pure function of positions and makes this whole mechanism invisible to a
		// consumer that handles only `position` events.
		if err := c.writeEvent(eventPresentation, 0, presentationPayloadFor(s, at, c.windows)); err != nil {
			return err
		}
	}

	// 3. Remember what this poll SAW, for the next poll to compare against.
	c.prev = make(map[string]observation, len(states))
	for _, s := range states {
		c.prev[s.DeviceID] = observation{lastContact: s.LastContact}
	}
	c.prevAt = at
	return nil
}

// snapshotUnlocated is the fresh-connection half of the initial snapshot: every device holding NO fix
// gets one `presentation` event carrying the three-key unlocated entry.
//
// The devices holding a fix were already delivered as `position` events by the caller, so between the
// two every device in the family is described exactly once — which is what makes the map non-blank
// and, more importantly, what makes a never-set-up phone visible instead of absent.
func (c *streamConn) snapshotUnlocated(states []store.DeviceState, at time.Time, positioned map[string]bool) []store.DeviceState {
	var owed []store.DeviceState
	for _, s := range states {
		if positioned[s.DeviceID] {
			continue
		}
		if presentation.Evaluate(at, s.LastContact, c.windows) == presentation.NoPosition {
			owed = append(owed, s)
		}
	}
	return owed
}

// resumeSweep is what a client disconnected for an hour is owed beyond its replay: the truth about
// EVERY device in the family, not only the ones whose positions moved.
//
// It enumerates the family's device set — not the replay buffer — and compares each device's
// DELIVERY-TIME value against its CURSOR-INSTANT VALUE: the value the same pure function yields with
// the evaluation instant set to the instant the cursor encodes, over only that device's fixes that
// had arrived by then. A device whose two values agree gets nothing; silence on a resumed stream
// therefore means "every device was evaluated and nothing changed", not "nothing was checked".
//
// A device with NO cursor-instant value — enrolled during the outage, or first reporting during it —
// is always emitted rather than silently omitted, EXCEPT when its position was just replayed: that
// `position` event already carries its delivery-time value, so a second event would say the same
// thing twice. (That is the choice this implementation makes where the contract admitted two
// readings; see MAP-VERIFICATION.md and the spec's exit record.)
//
// Both sides are recomputed from stored fixes on every resume, so nothing about any client is
// remembered and two resumes on the same cursor emit the same set.
func (c *streamConn) resumeSweep(ctx context.Context, states []store.DeviceState, at time.Time, positioned map[string]bool) ([]store.DeviceState, error) {
	asOf, err := store.FamilyLastContactAsOf(ctx, c.database, c.familyID, c.cursorInstant)
	if err != nil {
		return nil, err
	}

	var owed []store.DeviceState
	for _, s := range states {
		if positioned[s.DeviceID] {
			continue
		}
		lastContactThen, hadAny := asOf[s.DeviceID]
		if !hadAny {
			// No fix at or before the cursor: this device HAS NO cursor-instant value. That absence
			// is not `no-position` and never compares equal to anything, so the device is told about.
			owed = append(owed, s)
			continue
		}
		then := presentation.Evaluate(c.cursorInstant, &lastContactThen, c.windows)
		now := presentation.Evaluate(at, s.LastContact, c.windows)
		if then != now {
			owed = append(owed, s)
		}
	}
	return owed, nil
}

// reannounce is the first successful poll AFTER an `error` event: every device the watcher holds is
// re-stated at delivery time, whether or not its value moved.
//
// This exists because connection health has to be able to get BETTER, not only worse. The `error`
// branch deliberately holds the connection OPEN (AC23), so nothing is ever dropped and nothing is ever
// re-established on that path — and a map annotates every device "no longer confirmed" until data
// arrives. If recovery were left to the ordinary diff, a family that did not happen to change during
// the outage would produce no events at all, and a healthy server would leave every row marked
// unconfirmed indefinitely. A heartbeat cannot rescue it either: an SSE comment reaches no handler,
// and the wire contract fixes exactly three event types, so there is no "all clear" to invent.
//
// So the recovery signal is the data itself, in the vocabulary that already exists. Nothing new goes
// on the wire: these are ordinary `presentation` events carrying delivery-time values and no id, so a
// position-only consumer sees nothing and no cursor moves. A device whose position was just replayed
// is skipped — that `position` event already carries its delivery-time value.
//
// The residual, stated honestly rather than papered over: a family with NO devices at all has nothing
// to re-announce, so its page-level notice stays up until the viewer reconnects. There is no event
// that could carry it.
func (c *streamConn) reannounce(states []store.DeviceState, positioned map[string]bool) []store.DeviceState {
	var owed []store.DeviceState
	for _, s := range states {
		if positioned[s.DeviceID] {
			continue
		}
		owed = append(owed, s)
	}
	return owed
}

// diff is the steady-state sweep: every device whose presentation value differs from what the same
// pure function yields for the PREVIOUS POLL's observation, evaluated at that poll's instant.
//
// One comparison covers every cause the contract lists, which is why there is no per-cause branch:
//
//   - time passed and the device crossed an age boundary (`live` to `recent`, `recent` to `stale`) —
//     the last contact is unchanged and only the instants differ;
//   - a fix arrived that refreshed last contact WITHOUT becoming the current position (an offline
//     backlog flush), including the freshening direction `stale` to `live`;
//   - retention purging deleted fixes, up to and including the device's last one, which turns it
//     `no-position` from that instant onward and takes its marker off the map.
//
// A device with no previous observation — enrolled since the last poll — is skipped: an already-open
// connection is not required to announce it, and it will be described on the next fresh connection,
// the next resume sweep, or immediately by GET /v1/positions. A fix from it is still delivered as a
// `position` event by the caller, whenever it reports.
func (c *streamConn) diff(states []store.DeviceState, at time.Time, positioned map[string]bool) []store.DeviceState {
	var owed []store.DeviceState
	for _, s := range states {
		if positioned[s.DeviceID] {
			// Its position was just sent, carrying the delivery-time value; nothing to add.
			continue
		}
		prev, known := c.prev[s.DeviceID]
		if !known {
			continue
		}
		was := presentation.Evaluate(c.prevAt, prev.lastContact, c.windows)
		is := presentation.Evaluate(at, s.LastContact, c.windows)
		if was != is {
			owed = append(owed, s)
		}
	}
	return owed
}

// reportUnavailable handles a poll that could not read the datastore.
//
// The watcher is told ONCE, explicitly, with an `error` event — and then nothing else is sent until a
// poll succeeds again. Silently holding the last known states open while presenting them as current
// is the failure this prevents: a map that keeps showing everyone `live` because the server stopped
// being able to check is worse than a map that says it has lost contact.
//
// The connection is deliberately held OPEN and the cursor is left where it is, so a transient blip
// resumes exactly where it stopped rather than tearing down every map in the household.
func (c *streamConn) reportUnavailable(ctx context.Context, cause error) error {
	c.logger.ErrorContext(ctx, "stream: read family state", "error", cause, "family_id", c.familyID)
	if c.degraded {
		return nil // already told; do not emit one error per second.
	}
	c.degraded = true
	return c.writeEvent(eventError, 0, streamErrorPayload{
		Error:   "unavailable",
		Message: "tracker cannot currently read this family's data; the states shown are no longer confirmed",
	})
}

// writeEvent writes one SSE event and flushes it. An id of 0 means NO id line at all — which is how
// `presentation` and `error` events stay invisible to the resume cursor.
func (c *streamConn) writeEvent(name string, id int64, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		// A payload that will not marshal is our bug, not the watcher's: skip this event rather than
		// killing the stream, and leave the cursor where it is.
		c.logger.Error("stream: marshal event", "error", err, "event", name)
		return nil
	}

	var head string
	if id != 0 {
		head = "id: " + strconv.FormatInt(id, 10) + "\n"
	}
	head += "event: " + name + "\ndata: "

	if _, err := c.w.Write([]byte(head)); err != nil {
		return err
	}
	if _, err := c.w.Write(body); err != nil {
		return err
	}
	if _, err := c.w.Write([]byte("\n\n")); err != nil {
		return err
	}
	c.flusher.Flush()
	c.lastWrite = time.Now()
	return nil
}

// authViewerForStream resolves the viewer for a stream request, accepting the token from either the
// Authorization header (a programmatic caller) or the `token` query parameter (the browser
// EventSource, which cannot set headers). On failure it writes the response and returns ok=false.
//
// This is the read/write separation §7 requires, same as requireViewer: the token is looked up in the
// VIEWERS table, so a device (write) token authenticates nothing here and is a 401 — a write
// credential cannot open a read stream, in either the header or the query form.
func authViewerForStream(w http.ResponseWriter, r *http.Request, database DB, logger *slog.Logger) (store.Viewer, bool) {
	tok, ok := auth.FromRequest(r)
	if !ok {
		if q := r.URL.Query().Get("token"); q != "" {
			tok, ok = auth.Token(q), true
		}
	}
	if !ok {
		unauthorized(w, logger, "missing or malformed bearer token")
		return store.Viewer{}, false
	}
	hash := tok.Hash()
	v, err := store.AuthenticateViewer(r.Context(), database, hash[:])
	if err != nil {
		if errors.Is(err, store.ErrUnknownToken) {
			unauthorized(w, logger, "unknown or revoked token")
			return store.Viewer{}, false
		}
		logger.ErrorContext(r.Context(), "stream: authenticate viewer", "error", err)
		writeError(w, logger, http.StatusInternalServerError, "internal", "could not verify the token")
		return store.Viewer{}, false
	}
	return v, true
}

// parseResumeCursor reads the resume cursor from the standard SSE `Last-Event-ID` header the browser
// re-sends on reconnect, and reports whether this request is a RESUME at all.
//
// The id is a fix's received_at in microseconds — an instant, not an opaque handle — so the cursor
// anchors the resume sweep by its VALUE. That matters for the case retention purging creates: an
// hour-old cursor names exactly the kind of aged row a purge deletes, and the sweep must still work
// when the row the id came from is gone. It does, because nothing here looks the row up.
//
// A cursor that cannot be anchored — absent, or not a number (`Last-Event-ID: banana`) — is NOT a
// resume. It falls back to the inherited S4 behaviour: the zero time, which yields the full fresh
// snapshot. That fails SAFE, toward showing more rather than less, and it is the same decision this
// endpoint has always made about a garbled cursor.
func parseResumeCursor(r *http.Request) (cursor time.Time, resuming bool) {
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		return time.Time{}, false
	}
	micros, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.UnixMicro(micros), true
}

// mapAssets serves the static file surface for the live map: the vendored Leaflet library at
// /static/leaflet.js etc. The embedded paths already carry the `static/` prefix, so the URL
// /static/leaflet.js resolves to the embedded static/leaflet.js with no rewriting.
func mapAssets() http.Handler {
	return http.FileServer(http.FS(staticFS))
}

// serveMapPage serves the Leaflet map at GET /map. It is a static shell: it carries no token and no
// family data itself — the watcher pastes their viewer token into the page, which then opens the
// authenticated SSE stream. So the page needs no auth, and the data behind it still does.
func serveMapPage(logger *slog.Logger) http.HandlerFunc {
	page, err := staticFS.ReadFile("static/map.html")
	return func(w http.ResponseWriter, r *http.Request) {
		if err != nil {
			writeError(w, logger, http.StatusInternalServerError, "internal", "map page unavailable")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if _, werr := w.Write(page); werr != nil {
			logger.Error("write map page", "error", werr)
		}
	}
}
