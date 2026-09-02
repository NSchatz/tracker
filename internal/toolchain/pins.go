// Package toolchain asserts that every file in this repo which PINS THE GO TOOLCHAIN names the same
// version, and that go.mod's language floor never climbs above it.
//
// tracker states the toolchain twice: the `FROM golang:<X>-bookworm` builder in the Dockerfile that
// compiles the SHIPPED binary, and `GO_VERSION` in the CI workflow that compiles the binary the gate
// tests. Two copies of a version held in step by hope is exactly how the compiler CI proves things
// with drifts away from the compiler production runs — and, because govulncheck scans the standard
// library of whichever toolchain executes it, that drift shows up as a vulnerability gate that is
// green on one machine and red on another for a reason no diff explains. So the agreement is asserted
// by a test that fails and names the disagreeing files, never by a comment asking the next person to
// remember.
//
// The `go` directive in go.mod is a LANGUAGE FLOOR, not a toolchain pin. It may sit below the pinned
// toolchain; it may not sit above it, because that would demand a compiler the pinned image does not
// contain.
package toolchain

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// MinimumVersion is the floor the toolchain may not drop below: the version both of the org's Go
// repos pinned when this check was written. Fixing a vulnerability gate must never be done by walking
// the compiler backwards, so a downgrade is a gate failure and not a judgement call.
const MinimumVersion = "1.25.12"

// Pin is one place a Go version is stated, with enough location to name it in a failure.
type Pin struct {
	File    string // path relative to the repo root
	Line    int    // 1-based
	Version string // e.g. "1.25.14"
	Kind    string // human-readable description of what the pin controls
}

func (p Pin) String() string {
	return fmt.Sprintf("%s:%d: %s = %s", p.File, p.Line, p.Kind, p.Version)
}

var (
	// The builder stage of the Dockerfile: `FROM golang:1.25.14-bookworm AS build`.
	reDockerfileGo = regexp.MustCompile(`^\s*FROM\s+golang:(\d+(?:\.\d+){1,2})[-@\s]`)

	// A workflow's Go version input: `GO_VERSION: "1.25.14"`. Matching the key rather than one named
	// workflow means a future release workflow is covered the day it is added, not the day someone
	// remembers to extend this list.
	reWorkflowGo = regexp.MustCompile(`^\s*GO_VERSION:\s*["']?(\d+(?:\.\d+){1,2})["']?\s*(?:#.*)?$`)

	// go.mod's language floor: `go 1.25.12`.
	reGoDirective = regexp.MustCompile(`^go\s+(\d+(?:\.\d+){1,2})\s*(?://.*)?$`)
)

// CollectPins finds every toolchain pin under root. It is an ERROR to find none: a check that silently
// passes because its patterns stopped matching is worse than no check, since it reports agreement it
// never looked for.
func CollectPins(root string) ([]Pin, error) {
	var pins []Pin

	dockerfile := filepath.Join(root, "Dockerfile")
	found, err := scanFile(dockerfile, "Dockerfile", "container base image that builds the shipped binary", reDockerfileGo)
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("no `FROM golang:<version>` builder image in Dockerfile: the toolchain pin check has nothing to compare")
	}
	pins = append(pins, found...)

	workflows, err := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.y*ml"))
	if err != nil {
		return nil, fmt.Errorf("listing workflows: %w", err)
	}
	sort.Strings(workflows)
	var workflowPins []Pin
	for _, wf := range workflows {
		rel := filepath.ToSlash(filepath.Join(".github", "workflows", filepath.Base(wf)))
		found, err := scanFile(wf, rel, "Go version input to the workflow", reWorkflowGo)
		if err != nil {
			return nil, err
		}
		workflowPins = append(workflowPins, found...)
	}
	if len(workflowPins) == 0 {
		return nil, fmt.Errorf("no workflow under .github/workflows states a GO_VERSION: the toolchain pin check has nothing to compare")
	}
	pins = append(pins, workflowPins...)

	return pins, nil
}

// LanguageFloor reads the `go` directive from go.mod.
func LanguageFloor(root string) (Pin, error) {
	gomod := filepath.Join(root, "go.mod")
	found, err := scanFile(gomod, "go.mod", "language floor (`go` directive)", reGoDirective)
	if err != nil {
		return Pin{}, err
	}
	if len(found) != 1 {
		return Pin{}, fmt.Errorf("go.mod states %d `go` directives, want exactly 1", len(found))
	}
	return found[0], nil
}

func scanFile(path, rel, kind string, re *regexp.Regexp) ([]Pin, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", rel, err)
	}
	return scanBytes(data, rel, kind, re), nil
}

func scanBytes(data []byte, rel, kind string, re *regexp.Regexp) []Pin {
	var pins []Pin
	for i, line := range strings.Split(string(data), "\n") {
		if m := re.FindStringSubmatch(line); m != nil {
			pins = append(pins, Pin{File: rel, Line: i + 1, Version: m[1], Kind: kind})
		}
	}
	return pins
}

// Verify reports every way the pins disagree with each other, with the minimum, or with the language
// floor. All findings are returned together — fixing one and rediscovering the next on the following
// run is how a half-landed bump stays half-landed.
func Verify(pins []Pin, languageFloor Pin) error {
	if len(pins) == 0 {
		return errors.New("found no toolchain pins to compare")
	}

	var problems []error

	// Every pin must name the same version X.
	byVersion := map[string][]Pin{}
	for _, p := range pins {
		byVersion[p.Version] = append(byVersion[p.Version], p)
	}
	if len(byVersion) > 1 {
		versions := make([]string, 0, len(byVersion))
		for v := range byVersion {
			versions = append(versions, v)
		}
		sort.Slice(versions, func(i, j int) bool { return compare(versions[i], versions[j]) < 0 })
		var b strings.Builder
		fmt.Fprintf(&b, "the Go toolchain is pinned to %d different versions (%s); every pin must name the same version:",
			len(versions), strings.Join(versions, ", "))
		for _, p := range pins {
			fmt.Fprintf(&b, "\n  %s", p)
		}
		problems = append(problems, errors.New(b.String()))
	}

	// The agreed (or lowest disagreeing) version is X for the remaining checks.
	x := pins[0].Version
	for _, p := range pins[1:] {
		if compare(p.Version, x) < 0 {
			x = p.Version
		}
	}

	if compare(x, MinimumVersion) < 0 {
		problems = append(problems, fmt.Errorf("pinned Go toolchain %s is below the %s floor: a vulnerability gate is never fixed by walking the compiler backwards", x, MinimumVersion))
	}

	if compare(languageFloor.Version, x) > 0 {
		problems = append(problems, fmt.Errorf("%s exceeds the pinned Go toolchain %s: the `go` directive is a language floor and must not demand a compiler the pinned image does not contain", languageFloor, x))
	}

	return errors.Join(problems...)
}

// compare orders two dotted Go versions numerically: 1.25.9 sorts BELOW 1.25.14, which string
// comparison gets backwards.
func compare(a, b string) int {
	as, bs := split(a), split(b)
	for i := 0; i < len(as) || i < len(bs); i++ {
		var av, bv int
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}
	return 0
}

func split(v string) []int {
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			// Unparseable components sort as -1 so a garbled version can never compare equal to a
			// real one and slip past the agreement check.
			out = append(out, -1)
			continue
		}
		out = append(out, n)
	}
	return out
}
