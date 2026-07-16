package server

// S4 live-map surface. GET /v1/stream is a Server-Sent Events feed of a family's position updates;
// GET /map is a minimal, server-served Leaflet page that consumes it. Together they retire the
// poll-only map (S3): a watcher opens one long-lived connection and the server pushes each device's
// new position as it arrives.
//
// Three properties are load-bearing and each is pinned by a test (stream_test.go):
//
//   - A watcher only ever receives its OWN family's stream. The cursor query (store.PositionsSince)
//     is family-scoped in its SQL, so there is no code path that could emit another family's fix to
//     this connection — the §4 risk-path-#1 breach, now multiplied across every open watcher.
//   - Every event carries an `id` (the fix's received_at, in microseconds) and a dropped connection
//     resumes from `Last-Event-ID` with NO GAPS: on reconnect the browser sends the last id it saw,
//     and the server replays every position that arrived strictly after it. received_at is the
//     server's own monotonic receive clock (§5.2), which is exactly what makes it a resumable cursor.
//   - The stream is a POSITION stream, not a breadcrumb replay: it pushes a device's current position
//     when that position changes, coalescing a burst of fixes to the newest. A live map wants where
//     everyone is now, not a re-run of every fix.

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

// streamPollInterval is how often an open stream re-queries the database for new positions. It is a
// var, not a const, only so the tests can shrink it — production polls at this cadence. One second is
// imperceptible on a live map and keeps the query load trivial at family scale (a handful of devices,
// a handful of watchers). A push architecture (LISTEN/NOTIFY or an in-process broker) would cut the
// latency further, but it would also add machinery and, for the broker, break the stateless-replica
// story the roadmap keeps (§1) — a fix ingested on one replica must still reach a watcher on another,
// and a database poll is what makes that true for free.
var streamPollInterval = 1 * time.Second

// streamHeartbeatInterval bounds how long a quiet stream goes without writing anything. A periodic
// SSE comment keeps intermediaries from timing out an idle connection and is how the server learns a
// watcher has silently gone away (the write fails). It is unrelated to position latency — a real
// position update is sent as soon as the next poll finds it.
const streamHeartbeatInterval = 15 * time.Second

// streamEvent is the SSE `event:` name carried by a position update. Naming it (rather than sending
// an unnamed message) lets the page bind one handler to it and lets a future event kind (a geofence
// alert, say) share the stream without ambiguity.
const streamEvent = "position"

// getStream serves GET /v1/stream: an SSE feed of the caller's family's position updates.
//
// It authenticates the viewer ITSELF rather than sitting behind the requireViewer middleware, for one
// hard reason: the browser EventSource API cannot set an Authorization header, so a viewer token has
// to arrive in the query string for the live map to work at all. That is a real, documented trade
// (SPEC.md) — a token in a URL is visible in logs and referrers — mitigated by TLS (S7) and retired
// by the in-app map (C5), which can set headers. A programmatic caller may still use the header.
func getStream(database DB, logger *slog.Logger) http.HandlerFunc {
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

		// The resume cursor. On a fresh connection there is no Last-Event-ID, so `since` is the zero
		// time and the first poll delivers the current position of everyone (the snapshot that makes
		// the map non-blank). On a reconnect the browser sends the last id it saw and we resume
		// strictly after it — the no-gaps guarantee.
		since := parseLastEventID(r)

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		// Defeat proxy buffering (nginx and friends), which would otherwise hold events until the
		// buffer filled and make a live map lag by an arbitrary amount.
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		flusher.Flush() // let the client's EventSource fire `open` before the first poll.

		ctx := r.Context()
		ticker := time.NewTicker(streamPollInterval)
		defer ticker.Stop()

		lastWrite := time.Now()
		// Deliver the initial snapshot immediately rather than waiting a full interval, so the map
		// paints at once instead of after the first tick.
		var err error
		if since, err = pollOnce(ctx, w, flusher, database, logger, v.FamilyID, since, &lastWrite); err != nil {
			return
		}

		for {
			select {
			case <-ctx.Done():
				// The watcher went away (closed the tab, dropped the network). Nothing to clean up
				// beyond the deferred ticker stop — the connection is already gone.
				return
			case <-ticker.C:
				if since, err = pollOnce(ctx, w, flusher, database, logger, v.FamilyID, since, &lastWrite); err != nil {
					return
				}
				if time.Since(lastWrite) >= streamHeartbeatInterval {
					if _, werr := w.Write([]byte(": keep-alive\n\n")); werr != nil {
						return // the watcher is gone; stop.
					}
					flusher.Flush()
					lastWrite = time.Now()
				}
			}
		}
	}
}

// pollOnce runs one cursor query and writes every new position as an SSE event, advancing and
// returning the cursor. A write failure returns an error so the caller stops the stream (the watcher
// has gone). A QUERY failure is logged but NOT fatal: a transient database blip should not tear down
// every open map — the next tick retries from the same cursor, so nothing is skipped.
func pollOnce(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, database DB, logger *slog.Logger, familyID string, since time.Time, lastWrite *time.Time) (time.Time, error) {
	positions, err := store.PositionsSince(ctx, database, familyID, since)
	if err != nil {
		logger.ErrorContext(ctx, "stream: query positions", "error", err, "family_id", familyID)
		return since, nil // keep the cursor; retry next tick. Never advance past unread data.
	}

	for _, p := range positions {
		id := p.ReceivedAt.UnixMicro()
		payload, err := json.Marshal(positionResponse{
			DeviceID:   p.DeviceID,
			DeviceName: p.DeviceName,
			Lat:        p.Lat,
			Lon:        p.Lon,
			TS:         p.TS.Unix(),
			ReceivedAt: p.ReceivedAt.Unix(),
		})
		if err != nil {
			// A position that will not marshal is our bug, not the watcher's; skip it rather than
			// killing the stream, and do not advance the cursor past it on a partial failure.
			logger.ErrorContext(ctx, "stream: marshal position", "error", err, "device_id", p.DeviceID)
			continue
		}
		// id, then a named event, then the JSON on one line. The blank line terminates the event.
		if _, err := w.Write([]byte("id: " + strconv.FormatInt(id, 10) + "\nevent: " + streamEvent + "\ndata: ")); err != nil {
			return since, err
		}
		if _, err := w.Write(payload); err != nil {
			return since, err
		}
		if _, err := w.Write([]byte("\n\n")); err != nil {
			return since, err
		}
		// Advance the cursor to THIS event's arrival time, in step with the id we just sent. Rows come
		// back received_at-ascending, so this is monotonic and a mid-batch disconnect resumes exactly
		// where it stopped. UnixMicro round-trips the id the client will echo in Last-Event-ID.
		since = time.UnixMicro(id)
		*lastWrite = time.Now()
	}
	if len(positions) > 0 {
		flusher.Flush()
	}
	return since, nil
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

// parseLastEventID reads the resume cursor from the standard SSE `Last-Event-ID` header the browser
// re-sends on reconnect. It is the fix's received_at in microseconds; an absent or unparseable value
// means "start from the beginning" (the zero time), which yields the current snapshot rather than a
// silent skip — a fresh watcher and a garbled cursor both fail SAFE toward showing more, never less.
func parseLastEventID(r *http.Request) time.Time {
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		return time.Time{}
	}
	micros, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.UnixMicro(micros)
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
