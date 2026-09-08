package uiverify

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"sort"
	"strings"
	"time"
)

// maxLabelWords is the "a few words" floor F8 asks for, applied to every label, heading, state word,
// control name and status message the page AUTHORS. Device names are excluded: they are family data
// the server supplied, not a label this page wrote.
const maxLabelWords = 8

// viewerToken is the credential the driver watches with. The fixture stream accepts anything; what
// matters is that a token travels in the URL exactly as a real bookmarked link carries one, so the
// egress assertions are made against a page that is genuinely holding a credential.
const viewerToken = "s0056-viewer-token"

// theFamily is the fixture family: one device of each presentation state, one whose coordinate the
// server could not give, so every rendered branch has a row to be measured on.
func theFamily() string {
	return PositionsJSON([]map[string]any{
		Entry("11111111-1111-1111-1111-111111111111", "Alice phone", "live", 41.9028, 12.4964),
		Entry("22222222-2222-2222-2222-222222222222", "Bob phone", "recent", 45.4642, 9.19),
		Entry("33333333-3333-3333-3333-333333333333", "Carol phone", "stale", 48.8566, 2.3522),
		Entry("44444444-4444-4444-4444-444444444444", "Dan phone", "no-position", nil, nil),
		Entry("55555555-5555-5555-5555-555555555555", "Erin phone", "live", "not-a-number", nil),
	})
}

// Check is one rendered claim, its measurement, and the mutation that proves the measurement can go
// red. Both halves are required: a check with no mutation cannot be counted as a demonstration, and
// AC18 fails the run when the demonstration count falls short of the claim count.
type Check struct {
	ID        string
	Criterion string
	Mutation  *Mutation
	Run       func(r *Runner) error
}

// Runner holds the session and stack a check drives.
type Runner struct {
	S  *Session
	St *Stack
}

// mapURL is the page under test, opened the way a bookmarked link opens it: with a live viewer token
// in the query string. That is the exact shape AC22 is about.
func (r *Runner) mapURL() string { return r.St.URL() + "/map?token=" + viewerToken }

func (r *Runner) open(theme string, w, h int64) error {
	if err := r.S.SetTheme(theme); err != nil {
		return err
	}
	if err := r.S.SetViewport(w, h, w <= 480); err != nil {
		return err
	}
	if err := r.S.SetVisionDeficiency("none"); err != nil {
		return err
	}
	return r.S.Navigate(r.mapURL())
}

// openWatched opens the page and waits until it has finished its first paint of the family.
func (r *Runner) openWatched(theme string, w, h int64) error {
	r.St.SetPositions(http.StatusOK, theFamily())
	r.St.Restore()
	if err := r.open(theme, w, h); err != nil {
		return err
	}
	return r.waitSnapshot(func(s snapshot) bool { return s.DeviceSetKnown && s.DeviceCount == 5 }, 15*time.Second,
		"the page never rendered the fixture family")
}

type snapshot struct {
	DeviceSetKnown bool     `json:"deviceSetKnown"`
	DeviceCount    int      `json:"deviceCount"`
	LocatedCount   int      `json:"locatedCount"`
	Interrupted    bool     `json:"interrupted"`
	InterruptedWhy string   `json:"interruptedWhy"`
	ReceivedCount  int      `json:"receivedCount"`
	RejectedCount  int      `json:"rejectedCount"`
	LastRejection  string   `json:"lastRejection"`
	PanelState     string   `json:"panelState"`
	PanelStateText string   `json:"panelStateText"`
	Status         string   `json:"status"`
	StatusHealth   string   `json:"statusHealth"`
	MarkerIDsJSON  []string `json:"markerIds"`
}

func (r *Runner) snapshot() (snapshot, error) {
	var s snapshot
	err := r.S.Eval(`window.trackerMap ? window.trackerMap.snapshot() : null`, &s)
	return s, err
}

func (r *Runner) waitSnapshot(ok func(snapshot) bool, within time.Duration, why string) error {
	deadline := time.Now().Add(within)
	var last snapshot
	for time.Now().Before(deadline) {
		s, err := r.snapshot()
		if err == nil {
			last = s
			if ok(s) {
				return nil
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	return fmt.Errorf("%s (waited %s; last snapshot: %+v)", why, within, last)
}

// inject hands an event to the page through exactly the function a wire event goes through.
func (r *Runner) inject(eventType, data string) error {
	var ok bool
	expr := fmt.Sprintf(`(function(){window.trackerMap.onStreamEvent(%q,%q);return true})()`, eventType, data)
	return r.S.Eval(expr, &ok)
}

// ---------------------------------------------------------------------------------------------
// The checks. One per rendered claim, in criterion order.
// ---------------------------------------------------------------------------------------------

// Checks is the whole web surface's claim set. Its length is what AC18 counts demonstrations
// against.
func Checks() []Check {
	return []Check{
		checkKeyboard(),
		checkAccessibleNames(),
		checkColourFree(),
		checkContrast("light"),
		checkContrast("dark"),
		checkFocusIndicator(),
		checkThemeFollowsPreference(),
		checkTargetSize(),
		checkAbsence(),
		checkAggregates(),
		checkOneBadEvent(),
		checkStaleFeed(),
		checkThreeStates(),
		checkExplanation(),
		checkReflow(),
		checkPolicy(),
		checkEgress(),
	}
}

// --- AC1 -------------------------------------------------------------------------------------

func checkKeyboard() Check {
	return Check{
		ID:        "AC1-keyboard",
		Criterion: "AC1 F1-web: reachable and operable by keyboard alone, in reading order",
		Mutation: &Mutation{
			ID:      "AC1-keyboard",
			Find:    `<button id="watch">Watch</button>`,
			Replace: `<button id="watch" tabindex="-1">Watch</button>`,
		},
		Run: func(r *Runner) error {
			r.St.SetPositions(http.StatusOK, theFamily())
			r.St.Restore()
			// Open with NO token in the URL, so the whole flow has to be driven from the keyboard.
			if err := r.S.SetTheme("light"); err != nil {
				return err
			}
			if err := r.S.SetViewport(1280, 800, false); err != nil {
				return err
			}
			if err := r.S.Navigate(r.St.URL() + "/map"); err != nil {
				return err
			}

			// Tab from the top of the document and record the order the engine gives.
			var order []string
			for i := 0; i < 8; i++ {
				if err := r.S.PressTab(false); err != nil {
					return err
				}
				var p string
				if err := r.S.Eval(`window.__uiaudit.activePath()`, &p); err != nil {
					return err
				}
				order = append(order, p)
				if p == "#watch" {
					break
				}
			}
			ti := indexOf(order, "#token")
			wi := indexOf(order, "#watch")
			if ti < 0 {
				return fmt.Errorf("the token field is not reachable by Tab; tab order was %v", order)
			}
			if wi < 0 {
				return fmt.Errorf("the watch control is not reachable by Tab; tab order was %v", order)
			}
			if ti > wi {
				return fmt.Errorf("Tab reaches the watch control (%d) before the token field (%d), which is not the order they read: %v", wi, ti, order)
			}

			// Operable to the same effect a pointer has: type the token and activate the control
			// with the keyboard only, then assert the page did the thing a click would have done.
			if err := r.S.Eval(`(function(){document.getElementById('token').focus();return true})()`, new(bool)); err != nil {
				return err
			}
			if err := r.S.TypeInto("#token", viewerToken); err != nil {
				return err
			}
			// Tab to the button and press Enter on it, rather than pressing Enter in the field: the
			// claim is that the CONTROL is operable, not that the field has a shortcut.
			if err := r.S.PressTab(false); err != nil {
				return err
			}
			var focused string
			if err := r.S.Eval(`window.__uiaudit.activePath()`, &focused); err != nil {
				return err
			}
			if focused != "#watch" {
				return fmt.Errorf("Tab from the token field focused %q, not the watch control", focused)
			}
			if err := r.S.PressKey("\r"); err != nil {
				return err
			}
			return r.waitSnapshot(func(s snapshot) bool { return s.DeviceCount == 5 },
				15*time.Second, "activating the watch control from the keyboard did not start watching")
		},
	}
}

func checkAccessibleNames() Check {
	return Check{
		ID:        "AC1-names",
		Criterion: "AC1 F1-web: every interactive control and the status line has a non-empty accessible name",
		Mutation: &Mutation{
			ID:      "AC1-names",
			Find:    `<label for="token" id="token-label">Viewer token</label>`,
			Replace: `<span id="token-label">Viewer token</span>`,
		},
		Run: func(r *Runner) error {
			if err := r.openWatched("light", 1280, 800); err != nil {
				return err
			}
			nodes, err := r.S.axTree()
			if err != nil {
				return err
			}
			missing := nodes.unnamedInteractive()
			if len(missing) > 0 {
				return fmt.Errorf("these controls have no accessible name in the engine's accessibility tree: %s",
					strings.Join(missing, ", "))
			}
			if name := nodes.nameOfStatus(); name == "" {
				return errors.New("the status line has no accessible name in the accessibility tree, so a screen reader is told a region changed without being told which one")
			}
			return nil
		},
	}
}

func checkColourFree() Check {
	return Check{
		ID:        "AC1-colour-free",
		Criterion: "AC1 F1-web: every device state and the unconfirmed annotation survives colour being removed",
		Mutation: &Mutation{
			ID:      "AC1-colour-free",
			Find:    `span.textContent = stateText(d.state);`,
			Replace: `span.textContent = '';`,
		},
		Run: func(r *Runner) error {
			if err := r.openWatched("light", 1280, 800); err != nil {
				return err
			}
			// Sever the feed so the unconfirmed annotation is on the page too, then take colour out
			// of the rendering in the engine and read what is left.
			r.St.Sever()
			if err := r.waitSnapshot(func(s snapshot) bool { return s.Interrupted }, 12*time.Second,
				"the page never noticed the severed feed"); err != nil {
				return err
			}
			if err := r.S.SetVisionDeficiency("achromatopsia"); err != nil {
				return err
			}
			rows, err := r.deviceRows()
			if err != nil {
				return err
			}
			if len(rows) != 5 {
				return fmt.Errorf("expected 5 device rows, read %d", len(rows))
			}
			seen := map[string]bool{}
			for _, row := range rows {
				if row.Text == "" {
					return fmt.Errorf("device %s renders no text at all with colour removed", row.ID)
				}
				want := map[string]string{
					"no-position": "no position recorded",
					"live":        "live now",
					"recent":      "seen recently",
					"stale":       "not seen lately",
				}[row.State]
				if want == "" {
					return fmt.Errorf("device %s carries the unknown state %q", row.ID, row.State)
				}
				if !strings.Contains(row.Text, want) {
					return fmt.Errorf("device %s (state %s) renders %q, which does not contain the state in words (%q)",
						row.ID, row.State, row.Text, want)
				}
				if !strings.Contains(row.Text, "unconfirmed") {
					return fmt.Errorf("device %s renders %q with the feed severed; the unconfirmed annotation is not in the text, so it exists only as colour",
						row.ID, row.Text)
				}
				seen[row.State] = true
			}
			for _, state := range []string{"no-position", "live", "recent", "stale"} {
				if !seen[state] {
					return fmt.Errorf("no device rendered the state %q, so this check passed over it vacuously", state)
				}
			}
			return nil
		},
	}
}

// --- AC2 / AC3 -------------------------------------------------------------------------------

func checkContrast(theme string) Check {
	mut := &Mutation{
		ID:      "AC2-contrast-" + theme,
		Find:    `--muted: #4d5359;`,
		Replace: `--muted: #b9bec3;`,
	}
	if theme == "dark" {
		mut = &Mutation{
			ID:      "AC2-contrast-dark",
			Find:    `--muted: #b6bfc9;`,
			Replace: `--muted: #3a4149;`,
		}
	}
	return Check{
		ID:        "AC2-contrast-" + theme,
		Criterion: "AC2 F1/F10-web: text 4.5:1 (3:1 large) and control boundaries 3:1, " + theme + " theme, from live engine colours",
		Mutation:  mut,
		Run: func(r *Runner) error {
			if err := r.openWatched(theme, 1280, 800); err != nil {
				return err
			}
			// Drive the page through the states that paint their own colours, so the status line's
			// error and live paints are measured too rather than only its resting one.
			if err := r.inject("error", `{"message":"the server cannot read this family"}`); err != nil {
				return err
			}
			if err := r.waitSnapshot(func(s snapshot) bool { return s.Interrupted }, 5*time.Second,
				"the page never entered its interrupted paint"); err != nil {
				return err
			}
			var runs []struct {
				Path       string  `json:"path"`
				Text       string  `json:"text"`
				Color      string  `json:"color"`
				Background string  `json:"background"`
				FontSize   float64 `json:"fontSize"`
				FontWeight int     `json:"fontWeight"`
				Ratio      float64 `json:"ratio"`
				Required   float64 `json:"required"`
			}
			if err := r.S.Eval(`window.__uiaudit.textRuns()`, &runs); err != nil {
				return err
			}
			if len(runs) < 8 {
				return fmt.Errorf("only %d text runs were measured; the page cannot be that empty, so this check would pass vacuously", len(runs))
			}
			var bad []string
			for _, run := range runs {
				if run.Ratio+0.005 < run.Required {
					bad = append(bad, fmt.Sprintf("%s %q: %s on %s = %.2f:1, needs %.1f:1 (%.1fpx/%d)",
						run.Path, run.Text, run.Color, run.Background, run.Ratio, run.Required, run.FontSize, run.FontWeight))
				}
			}
			var bounds []struct {
				Path        string  `json:"path"`
				BorderColor string  `json:"borderColor"`
				Surface     string  `json:"surface"`
				Ratio       float64 `json:"ratio"`
			}
			if err := r.S.Eval(`window.__uiaudit.boundaries()`, &bounds); err != nil {
				return err
			}
			for _, b := range bounds {
				if b.Ratio+0.005 < 3 {
					bad = append(bad, fmt.Sprintf("%s boundary: %s on %s = %.2f:1, needs 3:1",
						b.Path, b.BorderColor, b.Surface, b.Ratio))
				}
			}
			if len(bad) > 0 {
				sort.Strings(bad)
				return fmt.Errorf("%d contrast failures in the %s theme:\n    %s", len(bad), theme, strings.Join(bad, "\n    "))
			}
			return nil
		},
	}
}

func checkFocusIndicator() Check {
	return Check{
		ID:        "AC3-focus",
		Criterion: "AC3 F1/F10-web: a focus indicator that differs by rendered pixels and clears 3:1, in both themes",
		Mutation: &Mutation{
			ID: "AC3-focus",
			Find: `:focus-visible {
      outline: 3px solid var(--focus);
      outline-offset: 2px;
    }`,
			Replace: `:focus-visible {
      outline: none;
    }`,
		},
		Run: func(r *Runner) error {
			for _, theme := range []string{"light", "dark"} {
				if err := r.openWatched(theme, 1280, 800); err != nil {
					return err
				}
				for _, sel := range []string{"#token", "#watch", "#controls-doc"} {
					before, err := r.S.Screenshot(sel)
					if err != nil {
						return fmt.Errorf("screenshotting %s unfocused (%s): %w", sel, theme, err)
					}
					if err := r.focusByTab(sel); err != nil {
						return fmt.Errorf("%s theme: %w", theme, err)
					}
					after, err := r.S.Screenshot(sel)
					if err != nil {
						return fmt.Errorf("screenshotting %s focused (%s): %w", sel, theme, err)
					}
					diff, err := pixelDifference(before, after)
					if err != nil {
						return err
					}
					if diff == 0 {
						return fmt.Errorf("%s theme: focusing %s changes not one rendered pixel, so the focus indicator is suppressed", theme, sel)
					}
					var fi struct {
						Path         string  `json:"path"`
						OutlineStyle string  `json:"outlineStyle"`
						OutlineWidth float64 `json:"outlineWidth"`
						OutlineColor string  `json:"outlineColor"`
						Surface      string  `json:"surface"`
						Ratio        float64 `json:"ratio"`
					}
					if err := r.S.Eval(`window.__uiaudit.focusIndicator()`, &fi); err != nil {
						return err
					}
					if fi.OutlineWidth <= 0 || fi.OutlineStyle == "none" {
						return fmt.Errorf("%s theme: %s is focused with no outline painted (style %q, width %v)",
							theme, sel, fi.OutlineStyle, fi.OutlineWidth)
					}
					if fi.Ratio+0.005 < 3 {
						return fmt.Errorf("%s theme: %s focus indicator %s on %s = %.2f:1, below the 3:1 floor",
							theme, sel, fi.OutlineColor, fi.Surface, fi.Ratio)
					}
				}
			}
			return nil
		},
	}
}

// focusByTab moves keyboard focus onto a selector using real Tab keystrokes, because Chromium only
// treats focus as :focus-visible when it came from the keyboard. Scripting .focus() would measure a
// different and more forgiving thing.
func (r *Runner) focusByTab(selector string) error {
	if err := r.S.Eval(`(function(){document.activeElement && document.activeElement.blur(); return true})()`, new(bool)); err != nil {
		return err
	}
	for i := 0; i < 12; i++ {
		if err := r.S.PressTab(false); err != nil {
			return err
		}
		var p string
		if err := r.S.Eval(`window.__uiaudit.activePath()`, &p); err != nil {
			return err
		}
		if p == selector {
			return nil
		}
	}
	return fmt.Errorf("%s was not reachable by Tab within 12 presses", selector)
}

func checkThemeFollowsPreference() Check {
	return Check{
		ID:        "AC3-theme",
		Criterion: "AC3 F10-web: the page and its panel render light at a light preference and dark at a dark one",
		Mutation: &Mutation{
			ID:      "AC3-theme",
			Find:    `--bg: #12161b;`,
			Replace: `--bg: #ffffff;`,
		},
		Run: func(r *Runner) error {
			type bgReport struct {
				Selector   string  `json:"selector"`
				Background string  `json:"background"`
				Luminance  float64 `json:"luminance"`
			}
			readings := map[string]map[string]bgReport{}
			for _, theme := range []string{"light", "dark"} {
				if err := r.openWatched(theme, 1280, 800); err != nil {
					return err
				}
				var got []bgReport
				if err := r.S.Eval(`window.__uiaudit.themeBackgrounds()`, &got); err != nil {
					return err
				}
				if len(got) < 2 {
					return fmt.Errorf("the %s rendering reported %d surfaces; body and the panel are both required", theme, len(got))
				}
				readings[theme] = map[string]bgReport{}
				for _, g := range got {
					readings[theme][g.Selector] = g
				}
			}
			for _, sel := range []string{"body", "#panel", "#bar"} {
				light, ok1 := readings["light"][sel]
				dark, ok2 := readings["dark"][sel]
				if !ok1 || !ok2 {
					return fmt.Errorf("%s was not measured in both themes", sel)
				}
				if light.Luminance < 0.5 {
					return fmt.Errorf("at a LIGHT operating-system preference %s renders %s (relative luminance %.3f), which is a dark slab",
						sel, light.Background, light.Luminance)
				}
				if dark.Luminance > 0.2 {
					return fmt.Errorf("at a DARK operating-system preference %s renders %s (relative luminance %.3f), which is not dark",
						sel, dark.Background, dark.Luminance)
				}
			}
			return nil
		},
	}
}

// --- AC4 / AC11 ------------------------------------------------------------------------------

func checkTargetSize() Check {
	return Check{
		ID:        "AC4-target-size",
		Criterion: "AC4 F1-web: every interactive control is at least 24x24 CSS pixels, desktop and 360x640",
		Mutation: &Mutation{
			ID: "AC4-target-size",
			Find: `#watch {
      min-height: 32px; min-width: 44px; padding: .35rem .8rem; font: inherit; cursor: pointer;`,
			Replace: `#watch {
      height: 14px; min-height: 0; min-width: 0; padding: 0; font-size: 8px; cursor: pointer;`,
		},
		Run: func(r *Runner) error {
			for _, vp := range []struct {
				w, h int64
			}{{1280, 800}, {360, 640}} {
				if err := r.openWatched("light", vp.w, vp.h); err != nil {
					return err
				}
				controls, err := r.controls()
				if err != nil {
					return err
				}
				if len(controls) < 4 {
					return fmt.Errorf("only %d interactive controls were found at %dx%d; the page has more than that, so the measurement is not reaching them",
						len(controls), vp.w, vp.h)
				}
				var bad []string
				for _, c := range controls {
					if c.Width+0.5 < 24 || c.Height+0.5 < 24 {
						bad = append(bad, fmt.Sprintf("%s (%s) is %.1fx%.1f", c.Path, c.Tag, c.Width, c.Height))
					}
				}
				if len(bad) > 0 {
					return fmt.Errorf("at %dx%d these controls are painted below 24x24 CSS pixels: %s", vp.w, vp.h, strings.Join(bad, "; "))
				}
			}
			return nil
		},
	}
}

func checkReflow() Check {
	return Check{
		ID:        "AC11-reflow",
		Criterion: "AC11 F9-web: at 360x640 the body does not scroll sideways and the controls stay inside it",
		Mutation: &Mutation{
			ID: "AC11-reflow",
			Find: `html, body {
      margin: 0; height: 100%;`,
			Replace: `html, body {
      margin: 0; height: 100%; min-width: 700px;`,
		},
		Run: func(r *Runner) error {
			if err := r.openWatched("light", 360, 640); err != nil {
				return err
			}
			var rf struct {
				ScrollWidth     float64 `json:"scrollWidth"`
				ClientWidth     float64 `json:"clientWidth"`
				BodyScrollWidth float64 `json:"bodyScrollWidth"`
				InnerWidth      float64 `json:"innerWidth"`
			}
			if err := r.S.Eval(`window.__uiaudit.reflow()`, &rf); err != nil {
				return err
			}
			if rf.ScrollWidth > rf.ClientWidth+0.5 {
				return fmt.Errorf("at 360 CSS pixels the document scrolls sideways: scrollWidth %.0f > clientWidth %.0f", rf.ScrollWidth, rf.ClientWidth)
			}
			controls, err := r.controls()
			if err != nil {
				return err
			}
			byPath := map[string]control{}
			for _, c := range controls {
				byPath[c.Path] = c
			}
			for _, sel := range []string{"#token", "#watch"} {
				c, ok := byPath[sel]
				if !ok {
					return fmt.Errorf("%s is not rendered at a 360px viewport", sel)
				}
				if c.Left < -0.5 || c.Right > rf.ClientWidth+0.5 {
					return fmt.Errorf("%s is laid out from %.0f to %.0f, outside the %.0fpx viewport", sel, c.Left, c.Right, rf.ClientWidth)
				}
			}
			// The status line and the device panel must be inside the viewport and legible.
			var boxes []struct {
				Path     string  `json:"path"`
				Left     float64 `json:"left"`
				Right    float64 `json:"right"`
				Top      float64 `json:"top"`
				Width    float64 `json:"width"`
				Height   float64 `json:"height"`
				FontSize float64 `json:"fontSize"`
			}
			if err := r.S.Eval(`(function(){
              return ['#status','#panel'].map(function(sel){
                var el=document.querySelector(sel); if(!el){return {path:sel,width:0,height:0};}
                var b=el.getBoundingClientRect(); var cs=getComputedStyle(el);
                return {path:sel,left:b.left,right:b.right,top:b.top,width:b.width,height:b.height,fontSize:parseFloat(cs.fontSize)};
              });
            })()`, &boxes); err != nil {
				return err
			}
			for _, b := range boxes {
				if b.Width <= 0 || b.Height <= 0 {
					return fmt.Errorf("%s is not rendered at a 360px viewport", b.Path)
				}
				if b.Left < -0.5 || b.Right > rf.ClientWidth+0.5 {
					return fmt.Errorf("%s spans %.0f..%.0f, outside the %.0fpx viewport", b.Path, b.Left, b.Right, rf.ClientWidth)
				}
				if b.Top > 640 {
					return fmt.Errorf("%s is laid out at y=%.0f, below a 640px-tall viewport", b.Path, b.Top)
				}
				if b.FontSize < 12 {
					return fmt.Errorf("%s renders at %.1fpx, below a legible floor of 12px", b.Path, b.FontSize)
				}
			}
			return nil
		},
	}
}

// --- AC5 / AC6 / AC7 ---------------------------------------------------------------------------

func checkAbsence() Check {
	return Check{
		ID:        "AC5-absence",
		Criterion: "AC5 F3-web: a no-position device reads as an absence in words and gets no marker; an unusable coordinate renders present but unlocated",
		Mutation: &Mutation{
			ID:      "AC5-absence",
			Find:    `'no-position': 'no position recorded',`,
			Replace: `'no-position': '0',`,
		},
		Run: func(r *Runner) error {
			if err := r.openWatched("light", 1280, 800); err != nil {
				return err
			}
			rows, err := r.deviceRows()
			if err != nil {
				return err
			}
			byID := map[string]deviceRow{}
			for _, row := range rows {
				byID[row.ID] = row
			}

			dan := byID["44444444-4444-4444-4444-444444444444"]
			if dan.ID == "" {
				return errors.New("the no-position device is not rendered at all")
			}
			if !strings.Contains(dan.Text, "no position recorded") {
				return fmt.Errorf("the no-position device renders %q, which is not words a reader understands as an absence", dan.Text)
			}
			for _, forbidden := range []string{"0,0", " 0 ", "-", "no-position"} {
				if strings.Contains(dan.Text, forbidden) && forbidden != "-" {
					return fmt.Errorf("the no-position device renders %q, which contains the bare token or a zero (%q)", dan.Text, forbidden)
				}
			}
			if dan.List != "unlocated" {
				return fmt.Errorf("the no-position device is listed under %q, not among the devices without a position", dan.List)
			}

			erin := byID["55555555-5555-5555-5555-555555555555"]
			if erin.ID == "" {
				return errors.New("the device whose coordinate could not be read is not rendered at all")
			}
			if erin.List != "unlocated" {
				return fmt.Errorf("the device with an unusable coordinate is listed under %q; it must render present but unlocated", erin.List)
			}
			if !strings.Contains(erin.Text, "not readable") {
				return fmt.Errorf("the device with an unusable coordinate renders %q, which does not say the position could not be read", erin.Text)
			}

			s, err := r.snapshot()
			if err != nil {
				return err
			}
			for _, id := range s.MarkerIDsJSON {
				if id == "44444444-4444-4444-4444-444444444444" || id == "55555555-5555-5555-5555-555555555555" {
					return fmt.Errorf("device %s has a map marker despite having no usable position", id)
				}
			}
			if len(s.MarkerIDsJSON) != 3 {
				return fmt.Errorf("the map holds %d markers for a family of 5 with 2 unlocated; want 3", len(s.MarkerIDsJSON))
			}

			// The stream branch of the same claim: a position event carrying a non-numeric
			// coordinate must leave the device present and unlocated, never located.
			if err := r.inject("position", `{"device_id":"11111111-1111-1111-1111-111111111111","device_name":"Alice phone","presentation":"live","lat":"nope","lon":null}`); err != nil {
				return err
			}
			if err := r.waitSnapshot(func(s snapshot) bool { return len(s.MarkerIDsJSON) == 2 }, 5*time.Second,
				"a position event with an unreadable coordinate left the device's marker in place"); err != nil {
				return err
			}
			rows, err = r.deviceRows()
			if err != nil {
				return err
			}
			for _, row := range rows {
				if row.ID == "11111111-1111-1111-1111-111111111111" {
					if row.List != "unlocated" || !strings.Contains(row.Text, "not readable") {
						return fmt.Errorf("after an unreadable coordinate the device renders %q in %q; it must read as present but unlocated", row.Text, row.List)
					}
				}
			}
			return nil
		},
	}
}

func checkAggregates() Check {
	return Check{
		ID:        "AC6-aggregates",
		Criterion: "AC6 F4-web: every figure states which rows it counted and how many it left out",
		Mutation: &Mutation{
			ID:      "AC6-aggregates",
			Find:    `locatedCount + ' of ' + ids.length + ' on map, ' + unlocatedCount + ' without position'`,
			Replace: `ids.length + ' devices'`,
		},
		Run: func(r *Runner) error {
			if err := r.openWatched("light", 1280, 800); err != nil {
				return err
			}
			// Reject one event so the rejected figure is on the page too.
			if err := r.inject("teleport", `{"device_id":"11111111-1111-1111-1111-111111111111","presentation":"live"}`); err != nil {
				return err
			}
			if err := r.waitSnapshot(func(s snapshot) bool { return s.RejectedCount > 0 }, 5*time.Second,
				"the page never rejected the unknown event type"); err != nil {
				return err
			}
			fig, err := r.figures()
			if err != nil {
				return err
			}
			if fig.Located == "" {
				return errors.New("the device-set figure is not rendered at all")
			}
			for _, want := range []string{"of", "5", "3"} {
				if !strings.Contains(fig.Located, want) {
					return fmt.Errorf("the device-set figure reads %q; it must name the whole set it counted over (5) and how many it left off the map (2)", fig.Located)
				}
			}
			if !strings.Contains(fig.Located, "without position") {
				return fmt.Errorf("the device-set figure reads %q; the rows excluded for want of a position are not reported", fig.Located)
			}
			if bareCount(fig.Located) {
				return fmt.Errorf("the device-set figure reads %q, which is a bare count a reader would take for the whole family", fig.Located)
			}

			if fig.Rejected == "" {
				return errors.New("one event was rejected but no figure says so")
			}
			if !strings.Contains(fig.Rejected, " of ") {
				return fmt.Errorf("the rejected figure reads %q; it must state the set it was counted over, so one bad update in forty is distinguishable from forty in forty", fig.Rejected)
			}
			s, err := r.snapshot()
			if err != nil {
				return err
			}
			if !strings.Contains(fig.Rejected, fmt.Sprint(s.ReceivedCount)) {
				return fmt.Errorf("the rejected figure reads %q but the page has handled %d updates; the denominator is not the set it counted over",
					fig.Rejected, s.ReceivedCount)
			}
			if fig.RejectedDetail == "" {
				return errors.New("the rejected figure names no reason, so a reader cannot tell what was left out")
			}
			return nil
		},
	}
}

// bareCount reports whether a figure is just a number and a noun - the shape F4 forbids.
func bareCount(s string) bool {
	fields := strings.Fields(s)
	return len(fields) <= 2
}

func checkOneBadEvent() Check {
	return Check{
		ID:        "AC7-one-bad-event",
		Criterion: "AC7 F5-web: one unreadable update costs only itself",
		Mutation: &Mutation{
			ID: "AC7-one-bad-event",
			Find: `function reject(why) {
      rejectedCount++;`,
			Replace: `function reject(why) {
      devices = {}; markers = {};
      rejectedCount++;`,
		},
		Run: func(r *Runner) error {
			if err := r.openWatched("light", 1280, 800); err != nil {
				return err
			}
			before, err := r.snapshot()
			if err != nil {
				return err
			}
			bad := []struct{ typ, data string }{
				{"presentation", `{not json at all`},
				{"teleport", `{"device_id":"11111111-1111-1111-1111-111111111111","presentation":"live"}`},
				{"presentation", `{"device_id":"11111111-1111-1111-1111-111111111111","presentation":"offline"}`},
				{"presentation", `{"presentation":"live"}`},
			}
			for i, b := range bad {
				if err := r.inject(b.typ, b.data); err != nil {
					return err
				}
				want := before.RejectedCount + i + 1
				if err := r.waitSnapshot(func(s snapshot) bool { return s.RejectedCount == want }, 5*time.Second,
					fmt.Sprintf("update %d (%s) was not rejected", i, b.typ)); err != nil {
					return err
				}
				s, err := r.snapshot()
				if err != nil {
					return err
				}
				if s.DeviceCount != before.DeviceCount {
					return fmt.Errorf("after the unreadable %s update the page shows %d devices, was %d; one bad update removed a device",
						b.typ, s.DeviceCount, before.DeviceCount)
				}
				if len(s.MarkerIDsJSON) != len(before.MarkerIDsJSON) {
					return fmt.Errorf("after the unreadable %s update the map holds %d markers, was %d",
						b.typ, len(s.MarkerIDsJSON), len(before.MarkerIDsJSON))
				}
				if s.Status == "" {
					return fmt.Errorf("after the unreadable %s update the status line is blank", b.typ)
				}
				ps, err := r.panelState()
				if err != nil {
					return err
				}
				if !ps.Visible || ps.Text == "" {
					return fmt.Errorf("after the unreadable %s update the panel has gone blank", b.typ)
				}
				rows, err := r.deviceRows()
				if err != nil {
					return err
				}
				if len(rows) != 5 {
					return fmt.Errorf("after the unreadable %s update %d device rows are drawn, was 5", b.typ, len(rows))
				}
			}
			// The stream is still the page's stream: a further, well-formed update still lands.
			if err := r.inject("presentation", `{"device_id":"22222222-2222-2222-2222-222222222222","device_name":"Bob phone","presentation":"stale"}`); err != nil {
				return err
			}
			return r.waitSnapshot(func(s snapshot) bool {
				rows, _ := r.deviceRows()
				for _, row := range rows {
					if row.ID == "22222222-2222-2222-2222-222222222222" && row.State == "stale" {
						return true
					}
				}
				return false
			}, 5*time.Second, "after four unreadable updates the page no longer accepts a good one, so the session was lost rather than the update")
		},
	}
}

// --- AC8 / AC9 ---------------------------------------------------------------------------------

func checkStaleFeed() Check {
	return Check{
		ID:        "AC8-stale",
		Criterion: "AC8 F6-web: a severed feed reads as interrupted and every device as last known, and a restored one clears it",
		Mutation: &Mutation{
			ID:      "AC8-stale",
			Find:    `mark.textContent = MSG_UNCONFIRMED;`,
			Replace: `mark.textContent = '';`,
		},
		Run: func(r *Runner) error {
			if err := r.openWatched("light", 1280, 800); err != nil {
				return err
			}
			beforeRows, err := r.deviceRows()
			if err != nil {
				return err
			}
			if len(beforeRows) != 5 {
				return fmt.Errorf("expected 5 device rows before severing, read %d", len(beforeRows))
			}
			for _, row := range beforeRows {
				if strings.Contains(row.Text, "unconfirmed") {
					return fmt.Errorf("device %s already reads as unconfirmed before the feed was severed: %q", row.ID, row.Text)
				}
			}

			// Sever it, and touch nothing. The page must change what it says on its own.
			severed := time.Now()
			r.St.Sever()
			if err := r.waitSnapshot(func(s snapshot) bool { return s.Interrupted }, 10*time.Second,
				"ten seconds after the feed was severed the page still does not say the connection is interrupted"); err != nil {
				return err
			}
			if elapsed := time.Since(severed); elapsed > 10*time.Second {
				return fmt.Errorf("the page took %s to say the connection was interrupted; the floor is ten seconds", elapsed)
			}
			fig, err := r.figures()
			if err != nil {
				return err
			}
			if !strings.Contains(strings.ToLower(fig.Status), "interrupt") {
				return fmt.Errorf("the status line reads %q with the feed severed; it does not read as interrupted", fig.Status)
			}
			rows, err := r.deviceRows()
			if err != nil {
				return err
			}
			if len(rows) != 5 {
				return fmt.Errorf("severing the feed left %d device rows, was 5; a device was silently removed", len(rows))
			}
			for _, row := range rows {
				if !strings.Contains(row.Text, "last known") || !strings.Contains(row.Text, "unconfirmed") {
					return fmt.Errorf("with the feed severed device %s reads %q, which does not read as last known rather than current", row.ID, row.Text)
				}
			}

			// Restore it. The annotation must clear with no reload, and no device may have gone.
			r.St.Restore()
			if err := r.waitSnapshot(func(s snapshot) bool { return !s.Interrupted }, 20*time.Second,
				"the restored feed never cleared the interruption"); err != nil {
				return err
			}
			afterRows, err := r.deviceRows()
			if err != nil {
				return err
			}
			if len(afterRows) != 5 {
				return fmt.Errorf("after the feed was restored %d device rows are drawn, was 5; a device was silently removed", len(afterRows))
			}
			for _, row := range afterRows {
				if strings.Contains(row.Text, "unconfirmed") {
					return fmt.Errorf("after the feed was restored device %s still reads %q", row.ID, row.Text)
				}
			}
			return nil
		},
	}
}

func checkThreeStates() Check {
	return Check{
		ID:        "AC9-three-states",
		Criterion: "AC9 F7-web: loading, empty and error are distinct, actionable and never two at once",
		Mutation: &Mutation{
			ID:      "AC9-three-states",
			Find:    `if (!deviceSetKnown) { return 'loading'; }`,
			Replace: `if (true) { return 'loading'; }`,
		},
		Run: func(r *Runner) error {
			// LOADING: the device set has not arrived.
			r.St.Restore()
			r.St.HoldPositions()
			if err := r.open("light", 1280, 800); err != nil {
				return err
			}
			ps, err := r.awaitPanelState("loading", 15*time.Second)
			if err != nil {
				return err
			}
			if ps.Text == "" || ps.Action == "" {
				return fmt.Errorf("the loading state renders %q / %q; it must carry text a person can act on", ps.Text, ps.Action)
			}
			loadingText := ps.Text

			// ... and it is never LEFT unresolved: a device set that arrives late is passed THROUGH
			// the loading state into the state the data implies, with no interaction.
			r.St.DelayPositions(1200*time.Millisecond, http.StatusOK, "[]")
			if err := r.open("light", 1280, 800); err != nil {
				return err
			}
			if _, err := r.awaitPanelState("loading", 10*time.Second); err != nil {
				return fmt.Errorf("a slow device set did not put the page in its loading state: %w", err)
			}
			empty, err := r.awaitPanelState("empty", 15*time.Second)
			if err != nil {
				snap, _ := r.snapshot()
				return fmt.Errorf("the loading state was never resolved by the device set arriving: %w (snapshot: %+v)", err, snap)
			}
			if empty.Text == "" || empty.Action == "" {
				return fmt.Errorf("the empty state renders %q / %q; it must carry text a person can act on", empty.Text, empty.Action)
			}
			if empty.Text == loadingText {
				return fmt.Errorf("the empty state and the loading state both read %q, so they are not distinct", empty.Text)
			}

			// ERROR: the credential is refused.
			r.St.RefuseCredential()
			if err := r.open("light", 1280, 800); err != nil {
				return err
			}
			errState, err := r.awaitPanelState("error", 15*time.Second)
			if err != nil {
				return err
			}
			if errState.Text == "" || errState.Action == "" {
				return fmt.Errorf("the error state renders %q / %q; it must carry text a person can act on", errState.Text, errState.Action)
			}
			if errState.Text == loadingText || errState.Text == empty.Text {
				return fmt.Errorf("the error state reads %q, which is one of the other two states", errState.Text)
			}

			// ERROR: the server cannot be reached at all.
			r.St.Restore()
			r.St.FailPositions()
			if err := r.open("light", 1280, 800); err != nil {
				return err
			}
			if _, err := r.awaitPanelState("error", 15*time.Second); err != nil {
				return fmt.Errorf("an unreachable server did not produce the error state: %w", err)
			}

			// And exactly one state element is ever drawn.
			var count int
			if err := r.S.Eval(`document.querySelectorAll('#panel-state').length`, &count); err != nil {
				return err
			}
			if count != 1 {
				return fmt.Errorf("the panel draws %d state elements; two of the three could be shown at once", count)
			}
			return nil
		},
	}
}

func (r *Runner) awaitPanelState(want string, within time.Duration) (panelStateReport, error) {
	deadline := time.Now().Add(within)
	var last panelStateReport
	for time.Now().Before(deadline) {
		ps, err := r.panelState()
		if err == nil {
			last = ps
			if ps.State == want && ps.Visible {
				return ps, nil
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	return last, fmt.Errorf("the panel never reached the %q state within %s (last: %+v)", want, within, last)
}

// --- AC10 --------------------------------------------------------------------------------------

func checkExplanation() Check {
	return Check{
		ID:        "AC10-explanation",
		Criterion: "AC10 F8-web: labels stay to a few words and each region carries exactly one resolving documentation link",
		Mutation: &Mutation{
			ID:      "AC10-explanation",
			Find:    `>How watching works</a>`,
			Replace: `>A full explanation of how watching your family live map actually works from end to end</a>`,
		},
		Run: func(r *Runner) error {
			if err := r.openWatched("light", 1280, 800); err != nil {
				return err
			}
			// Drive through the paints that carry their own words, so a long label in a state the
			// resting page never shows is still measured.
			if err := r.inject("teleport", `{"device_id":"x","presentation":"live"}`); err != nil {
				return err
			}
			r.St.Sever()
			if err := r.waitSnapshot(func(s snapshot) bool { return s.Interrupted }, 12*time.Second,
				"the page never entered its interrupted paint"); err != nil {
				return err
			}

			var texts []struct {
				Path  string `json:"path"`
				Text  string `json:"text"`
				Words int    `json:"words"`
			}
			if err := r.S.Eval(`window.__uiaudit.chromeTexts()`, &texts); err != nil {
				return err
			}
			if len(texts) < 8 {
				return fmt.Errorf("only %d chrome texts were measured; the page carries more than that", len(texts))
			}
			var wordy []string
			for _, t := range texts {
				if t.Words > maxLabelWords {
					wordy = append(wordy, fmt.Sprintf("%s: %d words %q", t.Path, t.Words, t.Text))
				}
			}
			if len(wordy) > 0 {
				return fmt.Errorf("these are paragraphs, not labels (the floor is %d words; the explanation belongs in the linked document):\n    %s",
					maxLabelWords, strings.Join(wordy, "\n    "))
			}

			var regions []struct {
				Region string   `json:"region"`
				Path   string   `json:"path"`
				Hrefs  []string `json:"hrefs"`
			}
			if err := r.S.Eval(`window.__uiaudit.regions()`, &regions); err != nil {
				return err
			}
			if len(regions) < 2 {
				return fmt.Errorf("the page declares %d regions needing explanation; the control bar and the device panel both do", len(regions))
			}
			var docLinks []string
			for _, reg := range regions {
				var docs []string
				for _, h := range reg.Hrefs {
					if strings.HasPrefix(h, "/static/") || strings.HasPrefix(h, "/docs/") {
						docs = append(docs, h)
					}
				}
				if len(docs) != 1 {
					return fmt.Errorf("region %q carries %d documentation links (%v); the clause is exactly one per region",
						reg.Region, len(docs), docs)
				}
				docLinks = append(docLinks, docs[0])
			}

			// Follow each one in the engine. A link is only an explanation if it resolves, and a
			// fragment is only a place in that explanation if the document has something with that
			// id - "a dead file reference" is exactly what a #section that lands nowhere is.
			for _, href := range docLinks {
				docPath, fragment, _ := strings.Cut(href, "#")
				// Navigate to the bare document each time: a same-document fragment jump makes no
				// request at all, so following two fragments of one file in a row would leave the
				// second one unverified.
				if err := r.S.Navigate(r.St.URL() + docPath); err != nil {
					return fmt.Errorf("following %s: %w", href, err)
				}
				ctype, ok := r.S.ResponseHeader(docPath, "content-type")
				if !ok {
					return fmt.Errorf("the engine recorded no response for %s, so the link is dead", docPath)
				}
				if !strings.HasPrefix(ctype, "text/html") {
					return fmt.Errorf("%s answered with %q rather than a document", docPath, ctype)
				}
				var title string
				if err := r.S.Eval(`document.title`, &title); err != nil {
					return err
				}
				if title == "" {
					return fmt.Errorf("%s answered with a document that has no title; a 404 page reads like that", docPath)
				}
				var bodyWords int
				if err := r.S.Eval(`(document.body.innerText||'').split(/\s+/).filter(function(w){return w.length}).length`, &bodyWords); err != nil {
					return err
				}
				if bodyWords < 100 {
					return fmt.Errorf("%s answered with %d words; that is not the explanation the label's claims moved into", docPath, bodyWords)
				}
				if fragment != "" {
					var landed bool
					if err := r.S.Eval(`!!document.getElementById(`+jsString(fragment)+`)`, &landed); err != nil {
						return err
					}
					if !landed {
						return fmt.Errorf("%s links to #%s, which is not in the document; that is a dead file reference", href, fragment)
					}
				}
			}
			return nil
		},
	}
}

// --- AC21 / AC22 -------------------------------------------------------------------------------

func checkPolicy() Check {
	return Check{
		ID:        "AC21-policy",
		Criterion: "AC21 F11-web: a Content-Security-Policy is sent for the page and for /static, and nothing it needs is refused",
		Mutation: &Mutation{
			ID:         "AC21-policy",
			CSPFind:    "script-src 'self' 'nonce-",
			CSPReplace: "script-src 'self' 'nonce-BROKEN",
		},
		Run: func(r *Runner) error {
			r.St.SetPositions(http.StatusOK, theFamily())
			r.St.Restore()
			if err := r.open("light", 1280, 800); err != nil {
				return err
			}

			csp, ok := r.S.ResponseHeader("/map", "content-security-policy")
			if !ok || csp == "" {
				return errors.New("GET /map was served with no Content-Security-Policy")
			}
			assetCSPSeen, ok := r.S.ResponseHeader("/static/leaflet.js", "content-security-policy")
			if !ok || assetCSPSeen == "" {
				return errors.New("GET /static/leaflet.js was served with no Content-Security-Policy")
			}

			// Nothing the page needs may be refused, and the browser's own record is the witness.
			violations, err := r.S.Violations()
			if err != nil {
				return err
			}
			if len(violations) > 0 {
				return fmt.Errorf("the browser reported %d policy violations against the shipped page:\n    %s",
					len(violations), strings.Join(violations, "\n    "))
			}

			// A policy that silences the page is a failure of this criterion, not a pass: every part
			// of the page has to still WORK under it.
			var works struct {
				Leaflet  bool    `json:"leaflet"`
				Hook     bool    `json:"hook"`
				Styled   bool    `json:"styled"`
				PanelBG  string  `json:"panelBackground"`
				TileReqs int     `json:"tiles"`
				Markers  int     `json:"markers"`
				BodyLum  float64 `json:"bodyLuminance"`
			}
			if err := r.waitSnapshot(func(s snapshot) bool { return s.DeviceSetKnown && s.StatusHealth == "live" },
				20*time.Second, "under the policy the page never completed its positions request and its event stream"); err != nil {
				return err
			}
			if err := r.S.Eval(`(function(){
              var panel=document.getElementById('panel');
              var cs=getComputedStyle(panel);
              return {
                leaflet: typeof window.L === 'object' && !!window.L.map,
                hook: !!window.trackerMap,
                styled: cs.backgroundColor !== 'rgba(0, 0, 0, 0)' && cs.backgroundColor !== 'transparent',
                panelBackground: cs.backgroundColor,
                tiles: document.querySelectorAll('img.leaflet-tile').length,
                markers: document.querySelectorAll('#map path.leaflet-interactive').length,
                bodyLuminance: 0
              };
            })()`, &works); err != nil {
				return err
			}
			if !works.Leaflet {
				return errors.New("the vendored Leaflet library did not run under the policy")
			}
			if !works.Hook {
				return errors.New("the page's own inline script did not run under the policy")
			}
			if !works.Styled {
				return fmt.Errorf("the page's inline stylesheet did not apply under the policy (the panel's background is %q)", works.PanelBG)
			}
			if works.Markers == 0 {
				return errors.New("no device marker was drawn under the policy, so the map is silenced even though nothing was formally refused")
			}
			tileRequested := false
			for _, u := range r.S.Requests() {
				if strings.Contains(u, "tile.openstreetmap.org") {
					tileRequested = true
				}
			}
			if !tileRequested {
				return errors.New("the page made no map tile request under the policy")
			}
			return nil
		},
	}
}

func checkEgress() Check {
	return Check{
		ID:        "AC22-egress",
		Criterion: "AC22 F11-web: no origin beyond the server and the tile host, no token to the tile host, no reporting endpoint",
		Mutation: &Mutation{
			ID:         "AC22-egress",
			Find:       `https://tile.openstreetmap.org/{z}/{x}/{y}.png`,
			Replace:    `https://tiles.example.net/{z}/{x}/{y}.png`,
			CSPFind:    "img-src 'self' data: https://tile.openstreetmap.org",
			CSPReplace: "img-src 'self' data: https://tile.openstreetmap.org https://tiles.example.net",
		},
		Run: func(r *Runner) error {
			r.St.SetPositions(http.StatusOK, theFamily())
			r.St.Restore()
			if err := r.open("light", 1280, 800); err != nil {
				return err
			}
			if err := r.waitSnapshot(func(s snapshot) bool { return s.DeviceSetKnown && s.StatusHealth == "live" },
				20*time.Second, "the page never got as far as watching, so its network record proves nothing"); err != nil {
				return err
			}
			// Give the tile layer time to ask for imagery.
			time.Sleep(1500 * time.Millisecond)

			allowed := map[string]bool{
				origin(r.St.URL()):                  true,
				"https://tile.openstreetmap.org":    true,
				"chrome-extension://":               true,
				"data:":                             true,
				"about:":                            true,
				"devtools://devtools":               true,
				"https://tile.openstreetmap.org/":   true,
				origin(r.St.URL()) + "/":            true,
				"chrome://":                         true,
				"blob:":                             true,
				"http://127.0.0.1":                  true,
				strings.TrimSuffix(r.St.URL(), "/"): true,
			}
			seen := map[string]int{}
			var foreign []string
			var tileURLs []string
			for _, u := range r.S.Requests() {
				if strings.HasPrefix(u, "data:") || strings.HasPrefix(u, "blob:") || strings.HasPrefix(u, "about:") {
					continue
				}
				o := origin(u)
				seen[o]++
				if strings.Contains(o, "tile.openstreetmap.org") {
					tileURLs = append(tileURLs, u)
					continue
				}
				if !allowed[o] {
					foreign = append(foreign, u)
				}
			}
			if len(seen) == 0 {
				return errors.New("the engine recorded no requests at all, so this check would pass vacuously")
			}
			if len(foreign) > 0 {
				return fmt.Errorf("the page made requests to an origin that is neither its own server nor the tile host:\n    %s",
					strings.Join(unique(foreign), "\n    "))
			}
			if len(tileURLs) == 0 {
				return errors.New("the page requested no map tiles, so the no-token-to-the-tile-host claim was not exercised")
			}
			for _, u := range tileURLs {
				if strings.Contains(u, viewerToken) {
					return fmt.Errorf("a tile request carries the viewer token: %s", u)
				}
			}
			// A referrer would carry the token just as surely as a query string would, because the
			// page's own URL is /map?token=...
			ref, ok := r.S.ResponseHeader("/map", "referrer-policy")
			if !ok || ref != "no-referrer" {
				return fmt.Errorf("GET /map sent Referrer-Policy %q; the page URL carries a live viewer token, so anything but no-referrer hands it to the tile host", ref)
			}

			csp, _ := r.S.ResponseHeader("/map", "content-security-policy")
			for _, reporter := range []string{"report-uri", "report-to"} {
				if strings.Contains(csp, reporter) {
					return fmt.Errorf("the policy names a %s; a reporting endpoint is one more place a URL carrying a viewer token can be sent:\n    %s", reporter, csp)
				}
			}
			for _, field := range strings.Fields(strings.ReplaceAll(csp, ";", " ")) {
				if strings.HasPrefix(field, "http://") || strings.HasPrefix(field, "https://") {
					if field != "https://tile.openstreetmap.org" {
						return fmt.Errorf("the policy names the host %q beyond the server and the tile imagery:\n    %s", field, csp)
					}
				}
			}
			return nil
		},
	}
}

func unique(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func indexOf(hay []string, needle string) int {
	for i, h := range hay {
		if h == needle {
			return i
		}
	}
	return -1
}

// pixelDifference counts pixels that differ between two PNG renderings of the same element.
func pixelDifference(a, b []byte) (int, error) {
	ia, err := png.Decode(bytes.NewReader(a))
	if err != nil {
		return 0, fmt.Errorf("decoding the unfocused rendering: %w", err)
	}
	ib, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return 0, fmt.Errorf("decoding the focused rendering: %w", err)
	}
	ra, rb := ia.Bounds(), ib.Bounds()
	if ra != rb {
		// A focus indicator that changes the element's box is still a rendered difference.
		return ra.Dx()*ra.Dy() + rb.Dx()*rb.Dy(), nil
	}
	diff := 0
	for y := ra.Min.Y; y < ra.Max.Y; y++ {
		for x := ra.Min.X; x < ra.Max.X; x++ {
			if !sameColor(ia, ib, x, y) {
				diff++
			}
		}
	}
	return diff, nil
}

func sameColor(a, b image.Image, x, y int) bool {
	ar, ag, ab, aa := a.At(x, y).RGBA()
	br, bg, bb, ba := b.At(x, y).RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}
