package pingate

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// the Go module graph (P4)
// ---------------------------------------------------------------------------

var (
	// A go.mod require, in either the single-line or the block form. The version is whatever
	// follows the module path; the `// indirect` marker is not part of it.
	reRequireLine = regexp.MustCompile(`^\s*(?:require\s+)?([a-zA-Z0-9][^\s()]*\.[^\s()]*/[^\s()]*|[a-zA-Z0-9][^\s()]*\.[^\s()]+)\s+(\S+)\s*(?://.*)?$`)

	// An exact module version. Go has no ranges, so the only floats available are the literal word
	// `latest` and a malformed version; a pseudo-version (v0.0.0-20250102033503-faa5f7b0171c) is
	// exact - it names one commit - and reads as a prerelease of v0.0.0.
	reGoVersion = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.\-]+)?(\+incompatible)?$`)
)

// lockfiles is the set of names that count as a committed lockfile for a node manifest beside them.
var lockfiles = []string{"package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock"}

func scanGoModule(root string, r *Report) error {
	lines, ok, err := readLines(root, "go.mod")
	if err != nil {
		return err
	}
	if !ok {
		r.Categories = append(r.Categories, Category{
			Name: "go module requires", Examined: 0, Detail: "no go.mod at the root",
		})
		return nil
	}

	moduleLine := 1
	examined, inBlock := 0, false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "module ") {
			moduleLine = i + 1
		}
		if strings.HasPrefix(trimmed, "//") || trimmed == "" {
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "require ("):
			inBlock = true
			continue
		case inBlock && trimmed == ")":
			inBlock = false
			continue
		case !inBlock && !strings.HasPrefix(trimmed, "require "):
			continue
		}

		m := reRequireLine.FindStringSubmatch(trimmed)
		if m == nil {
			continue
		}
		examined++
		module, version := m[1], m[2]
		if reGoVersion.MatchString(version) {
			continue
		}
		r.Violations = append(r.Violations, Violation{
			File: "go.mod", Line: i + 1, Reference: module + " " + version, Clause: P4,
			Why: "a require must name an exact version; `" + version + "` is not one, and a module graph that resolves to whatever is newest resolves to something different tomorrow",
		})
	}

	// The lockfile itself. `go build` will happily reconstruct a module graph without go.sum; what
	// go.sum adds is the cryptographic record that the graph resolved to the same bytes it did when
	// a human looked at it. Untracked, or excluded by .gitignore, is the same as absent for everyone
	// who clones this.
	switch {
	case !exists(root, "go.sum"):
		r.Violations = append(r.Violations, Violation{
			File: "go.mod", Line: moduleLine, Reference: "go.sum", Clause: P4,
			Why: "the module lockfile is missing, so nothing records which bytes this module graph resolved to; run `go mod tidy` and commit go.sum",
		})
	case isGitWorkTree(root) && !gitTracked(root, "go.sum"):
		r.Violations = append(r.Violations, Violation{
			File: "go.mod", Line: moduleLine, Reference: "go.sum", Clause: P4,
			Why: "go.sum exists but is not tracked by git, so it does not exist for anyone who clones this; `git add go.sum`",
		})
	case isGitWorkTree(root) && gitIgnored(root, "go.sum"):
		r.Violations = append(r.Violations, Violation{
			File: "go.mod", Line: moduleLine, Reference: "go.sum", Clause: P4,
			Why: "go.sum is excluded by a .gitignore rule; the lockfile is the pin, and an ignored lockfile is an unpinned module graph",
		})
	}

	r.Categories = append(r.Categories, Category{
		Name:     "go module requires",
		Examined: examined,
		Detail:   fmt.Sprintf("%d require(s) in go.mod, with go.sum committed and un-ignored", examined),
	})
	return nil
}

// ---------------------------------------------------------------------------
// Makefile tool pins (P4, P6)
// ---------------------------------------------------------------------------

var (
	// A tool version variable: NAME_VERSION, assigned by any of make's flavours. The bare `VERSION`
	// variable does NOT match - it is this artefact's own version, computed by `git describe`, not
	// a pin on something fetched, and refusing it would be refusing a thing with no pinned form.
	reMakeToolVersion = regexp.MustCompile(`^([A-Z][A-Z0-9]*(?:_[A-Z0-9]+)*_VERSION)\s*[:?+]?=\s*(.*)$`)

	// An exact tool version, in either flavour this repo uses: `v1.1.4` (govulncheck) and
	// `2025.1.1` (staticcheck's calendar scheme).
	reExactToolVersion = regexp.MustCompile(`^v?\d+(\.\d+)*$`)
)

func scanMakefile(root string, r *Report) error {
	lines, ok, err := readLines(root, "Makefile")
	if err != nil {
		return err
	}
	if !ok {
		r.Categories = append(r.Categories, Category{
			Name: "Makefile tool versions", Examined: 0, Detail: "no Makefile at the root",
		})
		return nil
	}

	examined := 0
	for i, line := range lines {
		m := reMakeToolVersion.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		examined++
		name, value := m[1], strings.TrimSpace(m[2])
		if idx := strings.Index(value, "#"); idx >= 0 {
			value = strings.TrimSpace(value[:idx])
		}
		if reExactToolVersion.MatchString(value) {
			continue
		}
		r.Violations = append(r.Violations, Violation{
			File: "Makefile", Line: i + 1, Reference: name + " = " + value, Clause: P4,
			Why: "a tool version must be an exact version, never `latest` and never an expression evaluated at build time; the gate has to run the tool it names, or `make check` proves something about a version nobody chose",
		})
	}

	r.Categories = append(r.Categories, Category{
		Name:     "Makefile tool versions",
		Examined: examined,
		Detail:   fmt.Sprintf("%d *_VERSION pin(s); the bare VERSION variable is this artefact's own version, not a pin", examined),
	})
	return nil
}

// ---------------------------------------------------------------------------
// node manifests (P4)
// ---------------------------------------------------------------------------

// reIgnoreScripts reads npm's lifecycle-script switch out of an .npmrc, tolerating the spacing
// npm itself tolerates.
var reIgnoreScripts = regexp.MustCompile(`^\s*ignore-scripts\s*=\s*(\S+)\s*$`)

// scanNodeManifests enforces the last sentence of P4: node installs run with lifecycle scripts
// disabled unless the repo opts back in with a COMMITTED REASON.
//
// tracker has no node in it, and that is exactly why this scanner is written as an ASSERTION OF
// ABSENCE rather than as a loop that happens to run zero times: "we looked and there are none" and
// "we stopped looking" are the same green tick otherwise, and only one of them is a fact. Add a
// package.json tomorrow and the category stops asserting absence and starts asserting the rule.
func scanNodeManifests(root string, r *Report) error {
	manifests, err := walkFiles(root, func(rel string) bool { return path.Base(rel) == "package.json" })
	if err != nil {
		return err
	}

	if len(manifests) == 0 {
		r.Categories = append(r.Categories, Category{
			Name:     "node manifests",
			Examined: 1,
			Detail:   "asserted ABSENT: no package.json anywhere in the tree, so there is no install for a lifecycle script to run during",
		})
		return nil
	}

	for _, rel := range manifests {
		dir := path.Dir(rel)
		r.Violations = append(r.Violations, checkNodeManifest(root, rel, dir)...)
	}

	r.Categories = append(r.Categories, Category{
		Name:     "node manifests",
		Examined: len(manifests),
		Detail:   fmt.Sprintf("%d package.json, each checked for a committed lockfile, exact version specs and lifecycle scripts disabled", len(manifests)),
	})
	return nil
}

func checkNodeManifest(root, rel, dir string) []Violation {
	var out []Violation
	data, ok, err := readFile(root, rel)
	if err != nil || !ok {
		return []Violation{{
			File: rel, Line: 1, Reference: rel, Clause: P4,
			Why: "this manifest could not be read, so nothing about its pins has been checked",
		}}
	}

	// Lifecycle scripts. npm runs a dependency's install/postinstall scripts BY DEFAULT, which is
	// arbitrary code from the dependency tree executing at install time; P4 turns that off and only
	// lets it back on with a reason written down beside the switch.
	npmrc, npmrcRel := findNpmrc(root, dir)
	switch {
	case npmrc == nil:
		out = append(out, Violation{
			File: rel, Line: lineOfKey(data, `"name"`), Reference: rel, Clause: P4,
			Why: "npm runs dependency lifecycle scripts by default, and no committed .npmrc beside this manifest (or at the repository root) turns them off; add `ignore-scripts=true`",
		})
	default:
		value, line := "", 0
		for i, l := range npmrc {
			if m := reIgnoreScripts.FindStringSubmatch(l); m != nil {
				value, line = strings.ToLower(m[1]), i+1
			}
		}
		switch {
		case value == "":
			out = append(out, Violation{
				File: npmrcRel, Line: 1, Reference: "ignore-scripts", Clause: P4,
				Why: "this .npmrc does not set ignore-scripts, so npm's default applies and a dependency's install script runs as this repository's build; add `ignore-scripts=true`",
			})
		case value == "false":
			if !hasReasonComment(npmrc, line) {
				out = append(out, Violation{
					File: npmrcRel, Line: line, Reference: "ignore-scripts=false", Clause: P4,
					Why: "lifecycle scripts are switched back ON with no reason committed beside the switch; P4 allows the opt-back-in only with one, so put the reason on the comment line immediately above",
				})
			}
		case value != "true":
			out = append(out, Violation{
				File: npmrcRel, Line: line, Reference: "ignore-scripts=" + value, Clause: P4,
				Why: "ignore-scripts takes true or false; anything else is read by npm as false, which runs the scripts",
			})
		}
	}

	// The lockfile.
	if !hasLockfile(root, dir) {
		out = append(out, Violation{
			File: rel, Line: lineOfKey(data, `"name"`), Reference: rel, Clause: P4,
			Why: "no lockfile beside this manifest (" + strings.Join(lockfiles, ", ") + "), so the dependency tree resolves to whatever the registry serves at install time",
		})
	}

	// The version specs themselves.
	var manifest struct {
		Dependencies         map[string]string `json:"dependencies"`
		DevDependencies      map[string]string `json:"devDependencies"`
		OptionalDependencies map[string]string `json:"optionalDependencies"`
		PeerDependencies     map[string]string `json:"peerDependencies"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		out = append(out, Violation{
			File: rel, Line: 1, Reference: rel, Clause: P4,
			Why: "this manifest is not valid JSON, so its version specs could not be checked: " + err.Error(),
		})
		return out
	}
	for _, set := range []map[string]string{manifest.Dependencies, manifest.DevDependencies, manifest.OptionalDependencies, manifest.PeerDependencies} {
		for _, name := range sortedKeys(set) {
			spec := set[name]
			if reason := floatingNodeSpec(spec); reason != "" {
				out = append(out, Violation{
					File: rel, Line: lineOfKey(data, `"`+name+`"`), Reference: name + ": " + spec, Clause: P4,
					Why: reason,
				})
			}
		}
	}
	return out
}

// floatingNodeSpec names why a version spec is unbounded, or returns "" when it is acceptable.
// A caret or tilde range is NOT refused: it is bounded above, and the lockfile - which P4 requires
// separately - is what fixes the resolved version.
func floatingNodeSpec(spec string) string {
	s := strings.TrimSpace(spec)
	switch {
	case s == "" || s == "*" || s == "x" || s == "X":
		return "an unbounded version spec resolves to whatever the registry serves at install time"
	case strings.EqualFold(s, "latest") || strings.EqualFold(s, "next"):
		return "`" + s + "` is a dist-tag the publisher moves, which is the same supply-chain path a floating image tag is"
	case strings.HasPrefix(s, ">") && !strings.Contains(s, "<"):
		return "a `" + s + "` range has no upper bound, so a future major version installs itself"
	}
	return ""
}

func findNpmrc(root, dir string) ([]string, string) {
	for _, candidate := range []string{path.Join(dir, ".npmrc"), ".npmrc"} {
		candidate = strings.TrimPrefix(candidate, "./")
		lines, ok, err := readLines(root, candidate)
		if err == nil && ok {
			return lines, candidate
		}
	}
	return nil, ""
}

// hasReasonComment reports whether the line above the switch is a comment carrying actual prose.
// A bare `#` is not a reason.
func hasReasonComment(lines []string, line int) bool {
	for i := line - 2; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			continue
		}
		if !strings.HasPrefix(l, "#") && !strings.HasPrefix(l, ";") {
			return false
		}
		return len(strings.Fields(strings.TrimLeft(l, "#; "))) >= 3
	}
	return false
}

func hasLockfile(root, dir string) bool {
	for _, name := range lockfiles {
		if exists(root, strings.TrimPrefix(path.Join(dir, name), "./")) {
			return true
		}
	}
	return false
}

// lineOfKey finds the 1-based line a JSON key appears on, so a refusal about a manifest entry can
// point at it. encoding/json does not carry positions, and a wrong line number is worse than a
// coarse one, so an unfound key falls back to line 1.
func lineOfKey(data []byte, key string) int {
	for i, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, key) {
			return i + 1
		}
	}
	return 1
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
