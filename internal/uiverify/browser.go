package uiverify

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// Refusal is what this package returns instead of a skip when a prerequisite is missing.
//
// The house rule in this repository is that a gate FAILS rather than skips: internal/testsupport
// calls t.Fatalf when it cannot start PostGIS, and `make android` exits non-zero when it cannot find
// the SDK, because a gate that goes quiet when its environment is missing reports green while
// proving nothing. A browser engine is the same kind of prerequisite, and F2 makes the stakes
// explicit: the alternative to an engine is not "a slightly weaker check", it is a text check that
// cannot decide what was rendered at all.
type Refusal struct {
	Criterion    string
	Prerequisite string
	HowToObtain  string
	Underlying   error
}

func (r *Refusal) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\nREFUSED: cannot grade %s\n", r.Criterion)
	fmt.Fprintf(&b, "  missing prerequisite: %s\n", r.Prerequisite)
	fmt.Fprintf(&b, "  how to obtain it:     %s\n", r.HowToObtain)
	if r.Underlying != nil {
		fmt.Fprintf(&b, "  underlying error:     %v\n", r.Underlying)
	}
	b.WriteString("\n  No clause is reported as passed, skipped or green by this run.\n")
	b.WriteString("  F2 admits one grader for a rendered claim: the runtime that draws it. Reading the\n")
	b.WriteString("  page's source, or a recorded manual observation, is not offered here as a substitute.\n")
	return b.String()
}

func (r *Refusal) Unwrap() error { return r.Underlying }

// browserCandidates are the engine binaries this route knows how to drive, in preference order.
var browserCandidates = []string{
	"chromium",
	"chromium-browser",
	"google-chrome",
	"google-chrome-stable",
	"chrome",
}

// FindEngine locates a Chromium-family browser, or refuses by name.
func FindEngine() (string, error) {
	if p := os.Getenv("TRACKER_BROWSER"); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", &Refusal{
				Criterion:    webCriteria,
				Prerequisite: "the browser engine named by TRACKER_BROWSER (" + p + ")",
				HowToObtain:  "unset TRACKER_BROWSER to search PATH, or point it at an installed Chromium",
				Underlying:   err,
			}
		}
		return p, nil
	}
	for _, c := range browserCandidates {
		if p, err := exec.LookPath(c); err == nil {
			return p, nil
		}
	}
	return "", &Refusal{
		Criterion:    webCriteria,
		Prerequisite: "a Chromium-family browser engine on PATH (tried: " + strings.Join(browserCandidates, ", ") + ")",
		HowToObtain:  "install chromium (Debian: the chromium package; CI: browser-actions/setup-chrome@v1), or set TRACKER_BROWSER to its path",
	}
}

const webCriteria = "AC1-AC11, AC21, AC22 (the map's rendered claims, F1/F2/F9/F10/F11)"

// Session is one browser page under measurement, with the engine's own records attached: every
// request it was about to make, every console entry, and every Content-Security-Policy violation the
// page itself reported.
type Session struct {
	ctx    context.Context
	cancel []context.CancelFunc

	mu         sync.Mutex
	requests   []string
	violations []string
	console    []string
	headers    map[string]map[string]string // url -> response headers
}

// NewSession launches the engine and opens a page context.
func NewSession(ctx context.Context, enginePath string) (*Session, error) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(enginePath),
		chromedp.Flag("headless", "new"),
		chromedp.Flag("hide-scrollbars", false),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("force-device-scale-factor", "1"),
		chromedp.Flag("disable-lcd-text", true),
	)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	taskCtx, cancelTask := chromedp.NewContext(allocCtx)

	s := &Session{
		ctx:     taskCtx,
		cancel:  []context.CancelFunc{cancelTask, cancelAlloc},
		headers: map[string]map[string]string{},
	}

	if err := chromedp.Run(taskCtx); err != nil {
		s.Close()
		return nil, &Refusal{
			Criterion:    webCriteria,
			Prerequisite: "a browser engine that will actually start (" + enginePath + ")",
			HowToObtain:  "check the engine runs headless in this environment: " + enginePath + " --headless=new --no-sandbox --dump-dom about:blank",
			Underlying:   err,
		}
	}

	chromedp.ListenTarget(taskCtx, func(ev any) {
		switch e := ev.(type) {
		case *network.EventRequestWillBeSent:
			s.mu.Lock()
			s.requests = append(s.requests, e.Request.URL)
			s.mu.Unlock()
		case *network.EventResponseReceived:
			h := map[string]string{}
			for k, v := range e.Response.Headers {
				if sv, ok := v.(string); ok {
					h[strings.ToLower(k)] = sv
				}
			}
			s.mu.Lock()
			s.headers[e.Response.URL] = h
			s.mu.Unlock()
		case *log.EventEntryAdded:
			s.mu.Lock()
			s.console = append(s.console, string(e.Entry.Level)+": "+e.Entry.Text)
			s.mu.Unlock()
		}
	})

	if err := chromedp.Run(taskCtx,
		network.Enable(),
		log.Enable(),
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(violationRecorderJS).Do(ctx)
			return err
		}),
	); err != nil {
		s.Close()
		return nil, fmt.Errorf("enabling the engine's own records: %w", err)
	}
	return s, nil
}

func (s *Session) Close() {
	for i := len(s.cancel) - 1; i >= 0; i-- {
		s.cancel[i]()
	}
}

func (s *Session) Context() context.Context { return s.ctx }

// ResetRecords clears the network, console and violation logs before a new navigation.
func (s *Session) ResetRecords() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = nil
	s.violations = nil
	s.console = nil
	s.headers = map[string]map[string]string{}
}

func (s *Session) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

func (s *Session) Console() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.console...)
}

// ResponseHeader returns a header the engine saw on the response for a URL with the given suffix.
func (s *Session) ResponseHeader(urlSuffix, header string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for u, h := range s.headers {
		if strings.HasSuffix(stripQuery(u), urlSuffix) {
			v, ok := h[strings.ToLower(header)]
			return v, ok
		}
	}
	return "", false
}

func stripQuery(u string) string {
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		return u[:i]
	}
	return u
}

// Violations returns the policy violations the PAGE reported. This is the browser's own record, read
// out of the document that was subject to the policy, which is what F11 asks for.
func (s *Session) Violations() ([]string, error) {
	var out []string
	if err := chromedp.Run(s.ctx, chromedp.Evaluate(`window.__cspViolations || []`, &out)); err != nil {
		return nil, err
	}
	// Chromium also logs a "Refused to ..." console entry for every refusal, which catches a
	// violation that happened before the recorder script ran.
	for _, c := range s.Console() {
		if strings.Contains(c, "Refused to") || strings.Contains(c, "Content Security Policy") {
			out = append(out, c)
		}
	}
	return out, nil
}

// violationRecorderJS runs before any page script, so a refusal of the page's own inline script is
// still recorded.
const violationRecorderJS = `
window.__cspViolations = [];
document.addEventListener('securitypolicyviolation', function (e) {
  window.__cspViolations.push(
    e.violatedDirective + ' blocked ' + (e.blockedURI || '(inline)') + ' on ' + e.documentURI);
});
`

// SetTheme tells the engine which operating-system colour preference to emulate. This is the OS
// preference as the page sees it, not a class the page was asked to add.
func (s *Session) SetTheme(theme string) error {
	return chromedp.Run(s.ctx, emulation.SetEmulatedMedia().WithFeatures([]*emulation.MediaFeature{
		{Name: "prefers-color-scheme", Value: theme},
	}))
}

// SetVisionDeficiency removes colour from the rendering, in the engine, so "distinguishable without
// colour" is measured on a rendering that genuinely has none.
func (s *Session) SetVisionDeficiency(kind string) error {
	return chromedp.Run(s.ctx, emulation.SetEmulatedVisionDeficiency(emulation.SetEmulatedVisionDeficiencyType(kind)))
}

// SetViewport sets the CSS-pixel viewport.
func (s *Session) SetViewport(w, h int64, mobile bool) error {
	return chromedp.Run(s.ctx, emulation.SetDeviceMetricsOverride(w, h, 1, mobile))
}

// Navigate loads a URL and waits for the page's own script to have installed its hook, which is the
// only signal that the surface is actually up rather than merely fetched.
func (s *Session) Navigate(url string) error {
	s.ResetRecords()
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()
	if err := chromedp.Run(ctx,
		chromedp.Navigate(url),
		chromedp.WaitReady("body"),
	); err != nil {
		return err
	}
	return s.injectAudit()
}

// injectAudit installs the measuring library into the page. It reads the engine's resolved values;
// it declares nothing and styles nothing.
func (s *Session) injectAudit() error {
	var ok bool
	return chromedp.Run(s.ctx, chromedp.Evaluate(auditJS+"; true", &ok))
}

// Eval runs an expression in the page and decodes the result.
func (s *Session) Eval(expr string, out any) error {
	return chromedp.Run(s.ctx, chromedp.Evaluate(expr, out))
}

// EvalAwait runs an expression that yields a promise.
func (s *Session) EvalAwait(expr string, out any) error {
	return chromedp.Run(s.ctx, chromedp.Evaluate(expr, out, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
		return p.WithAwaitPromise(true)
	}))
}

// PressTab sends a real Tab keystroke, which is what makes the focus that follows a KEYBOARD focus:
// Chromium only applies :focus-visible when the focus came from the keyboard, so scripting
// element.focus() would measure a different, more forgiving thing.
func (s *Session) PressTab(shift bool) error {
	key := "\t"
	if shift {
		return chromedp.Run(s.ctx, chromedp.KeyEvent(key, chromedp.KeyModifiers(2)))
	}
	return chromedp.Run(s.ctx, chromedp.KeyEvent(key))
}

func (s *Session) PressKey(key string) error {
	return chromedp.Run(s.ctx, chromedp.KeyEvent(key))
}

func (s *Session) TypeInto(selector, text string) error {
	return chromedp.Run(s.ctx, chromedp.SendKeys(selector, text, chromedp.ByQuery))
}

// Screenshot captures the rendered pixels around one element, with a margin.
//
// The margin is load-bearing. A focus indicator is normally an OUTLINE, which is painted outside the
// element's border box, so a clip of the box alone would miss the very pixels the indicator is made
// of and report "no visible change" for a page that draws a perfectly good ring.
func (s *Session) Screenshot(selector string) ([]byte, error) {
	var box struct {
		X, Y, W, H float64
	}
	expr := `(function(){var el=document.querySelector(` + jsString(selector) + `);
      if(!el){return null;} var r=el.getBoundingClientRect();
      var pad=10;
      return {X: Math.max(0, r.left-pad), Y: Math.max(0, r.top-pad), W: r.width+2*pad, H: r.height+2*pad};})()`
	if err := chromedp.Run(s.ctx, chromedp.Evaluate(expr, &box)); err != nil {
		return nil, err
	}
	if box.W <= 0 || box.H <= 0 {
		return nil, fmt.Errorf("%s is not rendered, so it cannot be screenshotted", selector)
	}
	var buf []byte
	err := chromedp.Run(s.ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		var e error
		buf, e = page.CaptureScreenshot().
			WithFormat(page.CaptureScreenshotFormatPng).
			WithCaptureBeyondViewport(true).
			WithClip(&page.Viewport{X: box.X, Y: box.Y, Width: box.W, Height: box.H, Scale: 1}).
			Do(ctx)
		return e
	}))
	return buf, err
}

// jsString quotes a Go string for embedding in a JavaScript expression.
func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// FullScreenshot captures the whole viewport.
func (s *Session) FullScreenshot() ([]byte, error) {
	var buf []byte
	err := chromedp.Run(s.ctx, chromedp.CaptureScreenshot(&buf))
	return buf, err
}
