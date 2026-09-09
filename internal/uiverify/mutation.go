package uiverify

import (
	"errors"
	"net/http"
	"strings"
)

// A Mutation is a surface broken in exactly one way.
//
// F2's no-vacuous-pass rule (AC18) needs more than "the assertion passed": it needs the SAME
// measuring code shown going red against a surface mutated to break exactly the claim it measures. A
// check that cannot go red is not evidence, and the way an accessibility harness usually stops being
// evidence is that a selector stops matching and the loop over zero elements passes.
//
// A mutation rewrites the bytes the server served — the page's own source, or the policy header it
// was sent — rather than poking the live DOM. Two reasons. First, a DOM poke is undone by the next
// render(), so a check that reads after a repaint would see the pristine page and the demonstration
// would silently prove nothing. Second, a source substitution that no longer matches FAILS LOUDLY
// (apply returns an error and the response becomes a 500), which keeps the mutation set in step with
// the page instead of rotting into a no-op.
type Mutation struct {
	// ID names the claim this mutation breaks, and is what the run reports.
	ID string

	// Path is the served path whose bytes are rewritten; empty means /map.
	Path string

	// Find and Replace are applied to the response body. Find must occur at least once.
	Find    string
	Replace string

	// CSPFind and CSPReplace are applied to the Content-Security-Policy header instead.
	CSPFind    string
	CSPReplace string

	// DropHeader is removed from the response entirely.
	DropHeader string
}

func (m *Mutation) apply(path string, body *string, headers http.Header) error {
	target := m.Path
	if target == "" {
		target = "/map"
	}
	if path != target {
		return nil
	}

	if m.Find != "" {
		if !strings.Contains(*body, m.Find) {
			return errors.New(m.ID + ": the page no longer contains " + short(m.Find))
		}
		*body = strings.Replace(*body, m.Find, m.Replace, 1)
	}
	if m.CSPFind != "" {
		csp := headers.Get("Content-Security-Policy")
		if !strings.Contains(csp, m.CSPFind) {
			return errors.New(m.ID + ": the policy no longer contains " + short(m.CSPFind))
		}
		headers.Set("Content-Security-Policy", strings.Replace(csp, m.CSPFind, m.CSPReplace, 1))
	}
	if m.DropHeader != "" {
		if headers.Get(m.DropHeader) == "" {
			return errors.New(m.ID + ": the response no longer carries " + m.DropHeader)
		}
		headers.Del(m.DropHeader)
	}
	if m.Find == "" && m.CSPFind == "" && m.DropHeader == "" {
		return errors.New(m.ID + ": mutation changes nothing")
	}
	return nil
}

func short(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 70 {
		return "\"" + s[:70] + "...\""
	}
	return "\"" + s + "\""
}
