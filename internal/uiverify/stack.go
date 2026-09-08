// Package uiverify grades tracker's browser surface with a real browser engine.
//
// F2 of the umbrella's frontend conventions admits exactly one grader for a claim about what a user
// SEES: the runtime that draws it. Source text may never grade a rendered property, because a grep
// cannot decide what a rule applies to, what won the cascade, or what was shown rather than merely
// built. So everything in this package measures a page that Chromium has actually laid out and
// painted, through the DevTools protocol, and every number it compares against a threshold was read
// back out of that engine.
//
// The three things this package deliberately does NOT do:
//
//   - It never skips. A missing browser engine is an exit-non-zero naming the criterion, the missing
//     prerequisite and how to get it (F2 again: a silent downgrade to a text check is the failure
//     mode this whole route exists to close).
//   - It never asserts against the stylesheet. `getComputedStyle` is the engine's resolved value
//     after the cascade, custom-property substitution and the media query; the declared value is not
//     consulted anywhere.
//   - It never passes vacuously. Every check must also be shown going RED against a copy of the
//     surface mutated to break exactly that one claim (see mutation.go).
package uiverify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/NSchatz/tracker/internal/db"
	"github.com/NSchatz/tracker/internal/presentation"
	"github.com/NSchatz/tracker/internal/server"
)

// shellDB satisfies server.DB so the PRODUCTION handler can be built and asked for the real /map and
// /static responses — headers, policy, nonce and all. Its query surface is a nil db.Querier, which
// is the proof that nothing under test here touches the database: a request that reached the store
// would panic rather than quietly pass.
type shellDB struct {
	db.Querier
}

func (shellDB) Ping(context.Context) error { return nil }

// Stack is the thing the browser is pointed at.
//
// /map and /static come from tracker's real handler, because they ARE the surface under test. The
// two routes the page CONSUMES — GET /v1/positions and GET /v1/stream — are served by a fixture the
// verifier drives, for the reason the spec gives: several criteria describe states the running
// server has no path to produce (an event type outside the three, a presentation token outside the
// four, a severed feed), and the surface is the unit under test while the doctoring is the fixture.
//
// The fixture is bolted on OUTSIDE the production handler and cannot change what it serves. Nothing
// here widens the server's vocabulary; internal/server's own tests pin that shut separately.
type Stack struct {
	srv *httptest.Server

	mu sync.Mutex

	// positions is what GET /v1/positions answers with.
	positionsStatus int
	positionsBody   string
	positionsFail   bool // close the connection instead of answering
	positionsDelay  time.Duration

	// stream carries the SSE feed. Sever() drops it; Restore() lets the next connection open.
	streamOpen    bool
	streamClosers []chan struct{}
	streamRefuse  bool

	// mutation, when set, rewrites the served document and its policy. This is how a check is shown
	// going red against a surface broken in exactly one way.
	mutation *Mutation
}

// NewStack starts the verification stack and returns it. Close it when done.
func NewStack() *Stack {
	s := &Stack{
		positionsStatus: http.StatusOK,
		positionsBody:   "[]",
		streamOpen:      true,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	production := server.New(shellDB{}, nil, presentation.Windows{LiveSeconds: 120, StaleSeconds: 900}, logger)

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/positions", s.servePositions)
	mux.HandleFunc("/v1/stream", s.serveStream)
	mux.Handle("/", s.mutating(production))

	s.srv = httptest.NewServer(mux)
	return s
}

func (s *Stack) URL() string { return s.srv.URL }

func (s *Stack) Close() {
	s.Sever()
	s.srv.Close()
}

// SetMutation installs (or clears, with nil) the one-claim mutation applied to the served surface.
func (s *Stack) SetMutation(m *Mutation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mutation = m
}

// SetPositions scripts the next GET /v1/positions.
func (s *Stack) SetPositions(status int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.positionsStatus = status
	s.positionsBody = body
	s.positionsFail = false
	s.positionsDelay = 0
}

// FailPositions makes GET /v1/positions unreachable — the connection is closed with no response, so
// the page's fetch rejects, which is the "cannot reach the server" branch of AC9.
func (s *Stack) FailPositions() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.positionsFail = true
	s.positionsDelay = 0
}

// HoldPositions makes GET /v1/positions hang, so the page stays in its loading state.
func (s *Stack) HoldPositions() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.positionsStatus = 0 // sentinel: block until the client goes away
	s.positionsFail = false
	s.positionsDelay = 0
}

// DelayPositions answers GET /v1/positions after a pause, so the loading state is genuinely passed
// THROUGH on the way to a resolved one rather than merely reachable.
func (s *Stack) DelayPositions(d time.Duration, status int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.positionsStatus = status
	s.positionsBody = body
	s.positionsFail = false
	s.positionsDelay = d
}

// Sever drops every open event stream and refuses new ones, with no other signal to the page. This
// is the feed going away, which is the input F6 is graded on.
func (s *Stack) Sever() {
	s.mu.Lock()
	closers := s.streamClosers
	s.streamClosers = nil
	s.streamRefuse = true
	s.mu.Unlock()
	for _, c := range closers {
		close(c)
	}
}

// Restore lets the page's EventSource reconnect.
func (s *Stack) Restore() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streamRefuse = false
}

// RefuseCredential makes BOTH read routes answer the way a real server answers a token it does not
// accept. Refusing only the positions request would leave the stream open and the page saying "live"
// beside "token refused", which is not a state a real deployment can produce.
func (s *Stack) RefuseCredential() {
	s.mu.Lock()
	s.positionsStatus = http.StatusUnauthorized
	s.positionsBody = `{"error":"unauthorized","message":"unknown or revoked token"}`
	s.positionsFail = false
	s.positionsDelay = 0
	closers := s.streamClosers
	s.streamClosers = nil
	s.streamRefuse = true
	s.mu.Unlock()
	for _, c := range closers {
		close(c)
	}
}

func (s *Stack) servePositions(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	status, body, fail, delay := s.positionsStatus, s.positionsBody, s.positionsFail, s.positionsDelay
	s.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}
	if fail {
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, err := hj.Hijack()
			if err == nil {
				_ = conn.Close()
				return
			}
		}
		panic(http.ErrAbortHandler)
	}
	if status == 0 {
		<-r.Context().Done()
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// serveStream is a real Server-Sent Events endpoint. It holds the connection open and does nothing
// else: the events the map consumes are injected through window.trackerMap by the driver, exactly as
// MAP-VERIFICATION.md's hook was designed for, and what this endpoint contributes is a stream that
// can genuinely be OPENED and genuinely SEVERED.
func (s *Stack) serveStream(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	refuse := s.streamRefuse
	s.mu.Unlock()
	if refuse {
		// A connection that is refused outright, so the page's EventSource reports an error rather
		// than sitting open. EventSource retries on its own; refusing keeps it interrupted.
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no flusher", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, ": open\n\n")
	flusher.Flush()

	done := make(chan struct{})
	s.mu.Lock()
	s.streamClosers = append(s.streamClosers, done)
	s.mu.Unlock()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-r.Context().Done():
			return
		case <-ticker.C:
			_, _ = io.WriteString(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// mutating wraps the production handler so a mutation can rewrite the bytes and the headers it
// produced. The PRISTINE run goes through untouched — what is graded is the real response — and a
// mutation run is that same response with exactly one substitution applied.
func (s *Stack) mutating(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		m := s.mutation
		s.mu.Unlock()
		if m == nil {
			next.ServeHTTP(w, r)
			return
		}

		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, r)

		body := rec.Body.String()
		headers := rec.Header().Clone()
		if err := m.apply(r.URL.Path, &body, headers); err != nil {
			// A mutation that no longer applies is a broken demonstration, and a broken
			// demonstration is worse than none: it would let a check "prove" it can fail against a
			// surface that was never actually changed. Say so in the response so the driver sees it.
			http.Error(w, "uiverify: mutation did not apply: "+err.Error(), http.StatusInternalServerError)
			return
		}
		for k, vs := range headers {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.WriteHeader(rec.Code)
		_, _ = io.WriteString(w, body)
	})
}

// PositionsJSON builds a GET /v1/positions body from entries, in the shape SPEC.md fixes.
func PositionsJSON(entries []map[string]any) string {
	b, err := json.Marshal(entries)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// Entry is one row of a positions response.
func Entry(id, name, state string, lat, lon any) map[string]any {
	e := map[string]any{"device_id": id, "device_name": name, "presentation": state}
	if lat != nil {
		e["lat"] = lat
	}
	if lon != nil {
		e["lon"] = lon
	}
	return e
}

// origin strips a URL down to scheme://host for the network-origin assertions.
func origin(rawurl string) string {
	i := strings.Index(rawurl, "://")
	if i < 0 {
		return rawurl
	}
	rest := rawurl[i+3:]
	if j := strings.IndexAny(rest, "/?#"); j >= 0 {
		rest = rest[:j]
	}
	return rawurl[:i+3] + rest
}
