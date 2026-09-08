// The read-authorization boundary, pinned as a shape rather than as a habit.
//
// tracker's blast radius is continuous family location history: an authorization defect here exposes
// where people physically are, and no re-run undoes a disclosure. The frontend work this file was
// added alongside touches response headers, a rendered page and an instrumented suite — none of
// which has any business moving a route into or out of a credential group. So the surface is
// enumerated here, from the router itself, and any move fails the gate naming the route that moved.
//
// This is deliberately NOT a list of routes that happen to 401 today. It walks the registered
// routing table, so a NEW route added without a credential fails this test even though no existing
// assertion mentions it.
package server_test

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/NSchatz/tracker/internal/server"
	"github.com/go-chi/chi/v5"
)

// credentialFreeRoutes is the ENTIRE set of routes tracker answers without a credential. Three
// entries, and each one carries no family data:
//
//   - /healthz          a liveness answer plus whether the database pings.
//   - /map              the map SHELL: markup, styling and script. The watcher pastes their own
//     viewer token into it; the page holds none.
//   - /static/*         the vendored Leaflet library and the documents the surfaces link to.
//
// Every byte of family data is behind requireViewer, requireDevice, or the token-checking
// GET /v1/stream handler.
// The key is the chi route pattern; the value is the set of methods that may answer it without a
// credential, or "*" for every method. /static/* is registered with chi's Handle, which binds every
// method to the same file server — so the pattern, not one verb of it, is what this pins.
var credentialFreeRoutes = map[string]string{
	"/healthz":  http.MethodGet,
	"/map":      http.MethodGet,
	"/static/*": "*",
}

func credentialFree(method, pattern string) bool {
	allowed, ok := credentialFreeRoutes[pattern]
	return ok && (allowed == "*" || allowed == method)
}

// routesRequiringACredential are the routes that must refuse a request carrying no credential. The
// SSE stream authenticates inside its handler rather than at the middleware (an EventSource cannot
// set an Authorization header), which is why it is listed here rather than derived from the group.
var routesRequiringACredential = []struct {
	method string
	path   string
}{
	{http.MethodPost, "/v1/fixes"},
	{http.MethodPost, "/owntracks"},
	{http.MethodGet, "/v1/positions"},
	{http.MethodGet, "/v1/devices/11111111-1111-1111-1111-111111111111/history"},
	{http.MethodGet, "/v1/near"},
	{http.MethodGet, "/v1/places"},
	{http.MethodGet, "/v1/geofence-events"},
	{http.MethodPost, "/v1/push-subscriptions"},
	{http.MethodGet, "/v1/stream"},
}

// TestCredentialFreeSurfaceIsExactlyThreeRoutes walks the router and asserts that the set of routes
// reachable without a credential has not grown. A new unauthenticated route — a CSP report sink, a
// metrics endpoint, a documentation route — fails here by name.
func TestCredentialFreeSurfaceIsExactlyThreeRoutes(t *testing.T) {
	t.Parallel()

	h := server.New(stubDB{}, nil, defaultWindows, discardLogger())
	router, ok := h.(chi.Routes)
	if !ok {
		t.Fatalf("server.New no longer returns a chi router (%T); this test can no longer enumerate the surface", h)
	}

	type walkedRoute struct{ method, pattern string }
	var registered []walkedRoute
	var patterns []string
	err := chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		p := strings.TrimSuffix(route, "/")
		registered = append(registered, walkedRoute{method, p})
		patterns = append(patterns, method+" "+p)
		return nil
	})
	if err != nil {
		t.Fatalf("walking the routing table: %v", err)
	}
	sort.Strings(patterns)

	// Nothing may LEAVE the credential-free set either: a /map that started demanding a token would
	// break every bookmark, and a /healthz that did would break the orchestrator.
	seen := map[string]bool{}
	for _, r := range registered {
		if credentialFree(r.method, r.pattern) {
			seen[r.pattern] = true
		}
	}
	for want := range credentialFreeRoutes {
		if !seen[want] {
			t.Errorf("the route %q is no longer registered credential-free; the surface has MOVED, not merely changed", want)
		}
	}

	for _, r := range registered {
		if credentialFree(r.method, r.pattern) {
			continue
		}
		if !refusesWithoutCredential(t, h, r.method, r.pattern) {
			t.Errorf("%s %s answers without a credential and is not one of the three routes that may: %v",
				r.method, r.pattern, sortedKeys(credentialFreeRoutes))
		}
	}
	if len(registered) == 0 {
		t.Fatal("the routing table walked to zero routes, so this test proved nothing")
	}
	t.Logf("routing table: %v", patterns)
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// refusesWithoutCredential drives one walked route with a request carrying no credential. A chi
// route pattern can carry parameters ({id}); a placeholder is substituted so the request reaches the
// same middleware chain a real one would.
func refusesWithoutCredential(t *testing.T, h http.Handler, method, pattern string) bool {
	t.Helper()
	path := strings.ReplaceAll(pattern, "{id}", "11111111-1111-1111-1111-111111111111")
	path = strings.ReplaceAll(path, "/*", "")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, http.NoBody))
	return rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden
}

// TestEveryDataRouteRefusesAnAnonymousRequest is the behavioural half: each named data route answers
// 401 to a request with no credential, before any handler or database touch. The stub's nil Querier
// is the proof it never reached the store — a gate that let the request through would panic rather
// than pass.
func TestEveryDataRouteRefusesAnAnonymousRequest(t *testing.T) {
	t.Parallel()

	h := server.New(stubDB{}, nil, defaultWindows, discardLogger())
	for _, rt := range routesRequiringACredential {
		t.Run(rt.method+rt.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(rt.method, rt.path, http.NoBody))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s with no credential = %d, want 401", rt.method, rt.path, rec.Code)
			}
		})
	}
}

// TestEveryBrowserSurfaceSendsAPolicy is the server-side regression guard behind F11. The
// authoritative grading is done by a real engine (make verify-ui), which is the only thing that can
// say whether the policy silences the page; this only pins that the header is still SENT, that its
// nonce is fresh per response, and that it names no reporting endpoint and no host beyond the tile
// imagery — a reporting endpoint being one more place a URL carrying a viewer token could be sent.
func TestEveryBrowserSurfaceSendsAPolicy(t *testing.T) {
	t.Parallel()

	h := server.New(stubDB{}, nil, defaultWindows, discardLogger())
	surfaces := []string{"/map", "/static/map-explained.html", "/static/app-explained.html", "/static/leaflet.js"}
	for _, path := range surfaces {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
			continue
		}
		csp := rec.Header().Get("Content-Security-Policy")
		if csp == "" {
			t.Errorf("GET %s sends no Content-Security-Policy", path)
			continue
		}
		for _, forbidden := range []string{"report-uri", "report-to", "unsafe-inline", "unsafe-eval"} {
			if strings.Contains(csp, forbidden) {
				t.Errorf("GET %s policy contains %q: %s", path, forbidden, csp)
			}
		}
		for _, host := range hostsIn(csp) {
			if host != "https://tile.openstreetmap.org" {
				t.Errorf("GET %s policy names the host %q; the only third party the map needs is the tile imagery",
					path, host)
			}
		}
		if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
			t.Errorf("GET %s Referrer-Policy = %q, want no-referrer — the map URL carries a viewer token", path, got)
		}
	}
}

// hostsIn pulls every scheme-bearing source out of a policy. Keyword sources ('self', 'none',
// nonces) are not hosts and are not returned.
func hostsIn(csp string) []string {
	var out []string
	for _, f := range strings.Fields(strings.ReplaceAll(csp, ";", " ")) {
		if strings.HasPrefix(f, "http://") || strings.HasPrefix(f, "https://") || strings.HasPrefix(f, "//") {
			out = append(out, f)
		}
	}
	return out
}

// TestTheNonceIsFreshOnEveryResponse. A nonce reused across responses is a nonce an injected script
// can guess by reading an earlier page, which makes the policy decorative.
func TestTheNonceIsFreshOnEveryResponse(t *testing.T) {
	t.Parallel()

	h := server.New(stubDB{}, nil, defaultWindows, discardLogger())
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/map", nil))
		csp := rec.Header().Get("Content-Security-Policy")
		nonce := nonceIn(csp)
		if nonce == "" {
			t.Fatalf("no nonce in the policy: %s", csp)
		}
		if seen[nonce] {
			t.Fatalf("the nonce %q was reused across responses", nonce)
		}
		seen[nonce] = true
		if !strings.Contains(rec.Body.String(), `nonce="`+nonce+`"`) {
			t.Fatalf("the page does not carry the nonce the policy names (%q); its inline style and script would be refused", nonce)
		}
		if strings.Contains(rec.Body.String(), "__CSP_NONCE__") {
			t.Fatal("the page still carries the nonce PLACEHOLDER, so the browser would refuse its inline style and script")
		}
	}
}

func nonceIn(csp string) string {
	const prefix = "'nonce-"
	i := strings.Index(csp, prefix)
	if i < 0 {
		return ""
	}
	rest := csp[i+len(prefix):]
	j := strings.Index(rest, "'")
	if j < 0 {
		return ""
	}
	return rest[:j]
}
