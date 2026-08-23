// What the SERVER controls about the browser map, asserted in Go.
//
// The map is one vendored HTML page with no build step and this repository carries no JavaScript test
// tooling; a headless-browser harness would be a new external dependency in a submodule, which is an
// operator decision and not one this change may take. So the map criteria are verified two ways,
// neither of which adds one.
//
// THIS FILE IS THE FIRST WAY: everything about the map that is decidable from the server side.
//
//   - the served page's bytes carry the whole presentation vocabulary and the two strings a viewer
//     must be able to tell apart from "still connecting" (the inputs behind AC24, AC29 and AC30);
//   - the non-conforming events AC28 must reject are CONSTRUCTED HERE, test-side, and shown to be
//     well-formed Server-Sent Events - so the rejection branch has a real input;
//   - and the production server GAINS NO PATH to emit one. Every event it writes is checked against
//     the three names and the four tokens, over a family holding one device of each kind, so a test
//     that obtained AC28's input by widening the server would fail this file rather than pass it.
//
// The second way is the rendered outcome, which only an eye can confirm: MAP-VERIFICATION.md at the
// repository root carries the procedure and its observations.
package server_test

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NSchatz/tracker/internal/presentation"
	"github.com/NSchatz/tracker/internal/server"
)

// mapPageBytes fetches GET /map exactly as a browser would.
func mapPageBytes(t *testing.T) string {
	t.Helper()
	handler := server.New(stubDB{}, nil, defaultWindows, discardLogger())
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/map", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /map = %d, want 200", rec.Code)
	}
	return rec.Body.String()
}

// TestMapPageCarriesTheWholeVocabulary is the input behind AC24: the page can only render a text
// token naming a state if it knows all four spellings. A page that had learned three of them would
// render the fourth device with no word at all, which is precisely the failure AC24 exists to
// prevent - and no eye scanning a map would notice which state was missing.
func TestMapPageCarriesTheWholeVocabulary(t *testing.T) {
	t.Parallel()
	page := mapPageBytes(t)

	for _, v := range presentation.Values {
		if !strings.Contains(page, string(v)) {
			t.Errorf("the map page never mentions the token %q; it cannot render a state it does not know", v)
		}
	}
	// The three event types, each of which AC28 requires its own branch for.
	for _, ev := range []string{"position", "presentation", "error"} {
		if !strings.Contains(page, ev) {
			t.Errorf("the map page never mentions the %q event type", ev)
		}
	}
}

// TestMapPageDistinguishesEmptyFromConnecting is AC29's and AC30's input: two states a viewer must be
// able to tell apart from a map that is still connecting, and neither is sayable unless the page
// carries the words.
func TestMapPageDistinguishesEmptyFromConnecting(t *testing.T) {
	t.Parallel()
	page := mapPageBytes(t)

	if !strings.Contains(page, "no devices in this family") {
		t.Error("the map page cannot say a family is empty, so an empty map is indistinguishable from one still connecting (AC29)")
	}
	if !strings.Contains(page, "refused") {
		t.Error("the map page cannot say a credential was refused, so a rejected token would read as \"connecting\" forever (AC30)")
	}
	// The credential check is a real request whose STATUS the page can read. EventSource cannot report
	// one - it reports every failure identically and then retries - so a page that only opened a
	// stream could not distinguish a refused token from a dead network, whatever it printed.
	if !strings.Contains(page, "401") {
		t.Error("the map page never inspects a 401, so it cannot know a credential was refused rather than the network being down")
	}
}

// TestNonConformingEventsAreWellFormedInput constructs, TEST-SIDE, the three inputs AC28 must reject:
// an unparseable body, an event type outside the three, and a presentation value outside the four.
//
// The point is that they are real Server-Sent Events. AC28 is about a map that keeps working when it
// receives one; if the fixture were not a well-formed event the browser would never deliver it to a
// handler and the criterion would be untested rather than satisfied. These exact bytes are what
// MAP-VERIFICATION.md's rejection steps feed the page.
//
// Nothing here goes near the server. The next test is the other half: that the server cannot produce
// any of them.
func TestNonConformingEventsAreWellFormedInput(t *testing.T) {
	t.Parallel()

	body := strings.Join([]string{
		"event: presentation",
		`data: {"device_id":"11111111-1111-1111-1111-111111111111","device_name":"fixture","presentation":"offline","last_contact_at":1752566402}`,
		"",
		"event: teleport",
		`data: {"device_id":"11111111-1111-1111-1111-111111111111","device_name":"fixture","presentation":"live"}`,
		"",
		"event: presentation",
		"data: {not json at all",
		"",
		"",
	}, "\n")

	events := parseSSEBody(t, body)
	if len(events) != 3 {
		t.Fatalf("the fixture parsed as %d events, want 3: %+v", len(events), events)
	}

	if events[0].event != "presentation" {
		t.Errorf("event 0 is %q, want a well-formed presentation event", events[0].event)
	}
	var out struct {
		Presentation string `json:"presentation"`
	}
	if err := json.Unmarshal([]byte(events[0].data), &out); err != nil {
		t.Fatalf("event 0 is not JSON, so it would exercise the unparseable branch instead: %v", err)
	}
	if presentation.Valid(presentation.Value(out.Presentation)) {
		t.Errorf("the out-of-vocabulary fixture carries %q, which IS one of the four tokens", out.Presentation)
	}

	if events[1].event == "position" || events[1].event == "presentation" || events[1].event == "error" {
		t.Errorf("the unknown-type fixture carries %q, which is one of the three the map recognises", events[1].event)
	}

	if err := json.Unmarshal([]byte(events[2].data), &struct{}{}); err == nil {
		t.Error("the unparseable fixture is valid JSON, so it would not exercise the unparseable branch")
	}
}

// parseSSEBody parses a handwritten SSE body into events, using the same framing rules openStream
// applies to a real response. It exists so the fixture above is checked against the wire format
// rather than against an assumption about it.
func parseSSEBody(t *testing.T, body string) []sseEvent {
	t.Helper()
	var out []sseEvent
	var cur sseEvent
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if cur != (sseEvent{}) {
				out = append(out, cur)
			}
			cur = sseEvent{}
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "id:"):
			cur.id = strings.TrimSpace(line[len("id:"):])
		case strings.HasPrefix(line, "event:"):
			cur.event = strings.TrimSpace(line[len("event:"):])
		case strings.HasPrefix(line, "data:"):
			cur.data = strings.TrimSpace(line[len("data:"):])
		}
	}
	if cur != (sseEvent{}) {
		out = append(out, cur)
	}
	return out
}

// serverVocabularyIsClosed is the half that keeps the fixture above honest: over a family holding one
// device of EACH kind, driven across an age boundary while the stream is open, every event the real
// server writes carries one of the three names and, where it names a state, one of the four tokens.
//
// This is what makes "the production server gains no path to emit a non-conforming event" a tested
// property rather than a claim. A change that widened the vocabulary to make AC28 easier to drive
// would fail here. It runs as a case of TestStreamPresentation, on that suite's harness.
func serverVocabularyIsClosed(t *testing.T, h *harness) {
	familyID, viewer := newFamilyWithViewer(t, h, "vocabulary")
	h.addDevice(t, familyID, "a-never-reported", "vocab-never")
	fresh := h.addDevice(t, familyID, "b-fresh", "vocab-fresh")
	aged := h.addDevice(t, familyID, "c-aged", "vocab-aged")
	older := h.addDevice(t, familyID, "d-older", "vocab-older")

	now := time.Now()
	h.seedFixAt(t, fresh, now, now, 12.4964, 41.9028)
	h.seedFixAt(t, aged, now.Add(-2*time.Second), now.Add(-2*time.Second), 9.19, 45.4642)
	h.seedFixAt(t, older, now.Add(-time.Hour), now.Add(-time.Hour), 2.3522, 48.8566)

	// Tight windows so all four states occur and the devices keep transitioning for the whole watch.
	srv := streamServer(t, h, tightWindows)
	ch, cancel, resp := openStream(t, srv.URL+"/v1/stream", "", viewer)
	defer cancel()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream open = %d, want 200", resp.StatusCode)
	}

	// The read surface is the same vocabulary check on the other surface.
	for _, e := range decodeEntries(t, h.get(t, "/v1/positions", viewer).Body.Bytes()) {
		if !presentation.Valid(presentation.Value(e.Presentation)) {
			t.Errorf("GET /v1/positions emitted the token %q for %s, which is not one of %v",
				e.Presentation, e.DeviceName, presentation.Values)
		}
	}

	deadline := time.After(4 * time.Second)
	seen := map[string]int{}
	for done := false; !done; {
		select {
		case e, ok := <-ch:
			if !ok {
				done = true
				break
			}
			switch e.event {
			case "position", "presentation", "error":
				seen[e.event]++
			default:
				t.Fatalf("the server emitted the event type %q; the contract fixes exactly three", e.event)
			}
			if e.event == "error" {
				continue // an error event names a condition, not a device state.
			}
			p := decodePayload(t, e)
			if !presentation.Valid(presentation.Value(p.Presentation)) {
				t.Fatalf("the server emitted the token %q on a %s event; the vocabulary is exactly %v",
					p.Presentation, e.event, presentation.Values)
			}
		case <-deadline:
			done = true
		}
	}

	// The watch has to have seen BOTH kinds, or the assertion above passed vacuously.
	if seen["position"] == 0 || seen["presentation"] == 0 {
		t.Fatalf("the watch saw %v; it must observe both position and presentation events for this to prove anything", seen)
	}
}
