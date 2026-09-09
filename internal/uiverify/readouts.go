package uiverify

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
)

type deviceRow struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Text  string `json:"text"`
	List  string `json:"list"`
}

func (r *Runner) deviceRows() ([]deviceRow, error) {
	var out []deviceRow
	err := r.S.Eval(`window.__uiaudit.deviceRows()`, &out)
	return out, err
}

type control struct {
	Path     string  `json:"path"`
	Tag      string  `json:"tag"`
	Width    float64 `json:"width"`
	Height   float64 `json:"height"`
	Left     float64 `json:"left"`
	Right    float64 `json:"right"`
	Top      float64 `json:"top"`
	TabIndex int     `json:"tabIndex"`
}

func (r *Runner) controls() ([]control, error) {
	var out []control
	err := r.S.Eval(`window.__uiaudit.controls()`, &out)
	return out, err
}

type figureReport struct {
	Located        string `json:"located"`
	Rejected       string `json:"rejected"`
	RejectedDetail string `json:"rejectedDetail"`
	Status         string `json:"status"`
}

func (r *Runner) figures() (figureReport, error) {
	var out figureReport
	err := r.S.Eval(`window.__uiaudit.figures()`, &out)
	return out, err
}

type panelStateReport struct {
	State   string `json:"state"`
	Text    string `json:"text"`
	Action  string `json:"action"`
	Visible bool   `json:"visible"`
}

func (r *Runner) panelState() (panelStateReport, error) {
	var out panelStateReport
	err := r.S.Eval(`window.__uiaudit.panelState()`, &out)
	return out, err
}

// --- the accessibility tree --------------------------------------------------------------------

// axTree reads the engine's own accessibility tree: the thing a screen reader is handed, rather than
// the markup a reviewer would have to imagine one from.
//
// The protocol response is decoded into types declared HERE rather than into cdproto's generated
// ones. Chromium ships accessibility property names faster than the generated bindings learn them
// (an `ignoredReasons` entry Chromium 151 emits is enough to fail a typed decode outright), and a
// grading route that dies on an unknown enum value would be a route that silently stops grading
// accessibility on the next browser release. What this tree needs — a role, a name and whether the
// node is ignored — has been stable for years.
type axNode struct {
	NodeID           string   `json:"nodeId"`
	Ignored          bool     `json:"ignored"`
	Role             *axValue `json:"role"`
	Name             *axValue `json:"name"`
	BackendDOMNodeID int64    `json:"backendDOMNodeId"`
}

type axValue struct {
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
}

type axTree struct {
	nodes []axNode
}

func (s *Session) axTree() (*axTree, error) {
	var resp struct {
		Nodes []axNode `json:"nodes"`
	}
	if err := chromedp.Run(s.ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		return cdp.Execute(ctx, "Accessibility.getFullAXTree", map[string]any{}, &resp)
	})); err != nil {
		return nil, err
	}
	if len(resp.Nodes) == 0 {
		return nil, errors.New("the engine returned an empty accessibility tree, so any assertion over it would pass vacuously")
	}
	return &axTree{nodes: resp.Nodes}, nil
}

// interactiveRoles are the roles a person can operate. A node in one of them with no accessible name
// is a control a screen-reader user is told exists without being told what it is.
var interactiveRoles = map[string]bool{
	"button":      true,
	"link":        true,
	"textbox":     true,
	"checkbox":    true,
	"combobox":    true,
	"radio":       true,
	"slider":      true,
	"switch":      true,
	"searchbox":   true,
	"menuitem":    true,
	"spinbutton":  true,
	"application": true,
}

// decodeAX decodes one accessibility property. The protocol carries it as a raw JSON value, so a
// role arrives as a JSON string and has to be unquoted rather than asserted.
func decodeAX(v *axValue) string {
	if v == nil || len(v.Value) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(v.Value, &s); err != nil {
		return strings.Trim(string(v.Value), `"`)
	}
	return s
}

func nodeRole(n axNode) string { return decodeAX(n.Role) }

func nodeName(n axNode) string { return strings.TrimSpace(decodeAX(n.Name)) }

func (t *axTree) unnamedInteractive() []string {
	var out []string
	for _, n := range t.nodes {
		if n.Ignored {
			continue
		}
		role := nodeRole(n)
		if !interactiveRoles[role] {
			continue
		}
		if nodeName(n) == "" {
			out = append(out, role+" (axNodeId "+n.NodeID+")")
		}
	}
	return out
}

// nameOfStatus returns the accessible name of the page's status region, or "" when it has none.
func (t *axTree) nameOfStatus() string {
	for _, n := range t.nodes {
		if n.Ignored {
			continue
		}
		if nodeRole(n) == "status" {
			return nodeName(n)
		}
	}
	return ""
}
