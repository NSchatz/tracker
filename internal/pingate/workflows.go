package pingate

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

var (
	// A `uses:` step, with or without the list dash, and whatever trails it.
	reUses = regexp.MustCompile(`^\s*(?:-\s+)?uses:\s*(\S+)\s*(.*)$`)

	// A compliant action reference: owner/repo (optionally /path) at a 40-character lowercase
	// commit SHA. Nothing else is immutable - a tag and a branch are both moved by their publisher.
	reActionSHA = regexp.MustCompile(`^[^@\s]+@[0-9a-f]{40}$`)

	// The trailing comment that keeps the SHA readable: `# v4`, `# v1.2.3`. Without it the pin is
	// correct and unreviewable, and the next person bumps it by guessing.
	reVersionComment = regexp.MustCompile(`^#\s*(v\d[\w.\-]*)`)
)

func isWorkflow(rel string) bool {
	dir := path.Dir(rel)
	if dir != ".github/workflows" {
		return false
	}
	name := strings.ToLower(path.Base(rel))
	return strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml")
}

// scanWorkflows enforces P3 over every action any workflow uses.
//
// `runs-on: ubuntu-latest` is deliberately NOT reached: it is a RUNNER LABEL that selects a
// GitHub-hosted machine pool, not an image reference this repo could pin, and refusing it would be
// refusing a thing with no compliant form.
func scanWorkflows(root string, r *Report) error {
	files, err := walkFiles(root, isWorkflow)
	if err != nil {
		return err
	}

	examined, local := 0, 0
	for _, rel := range files {
		lines, ok, err := readLines(root, rel)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		for i, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			m := reUses.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			examined++
			ref := strings.Trim(m[1], `"'`)
			trailing := strings.TrimSpace(m[2])

			v := Violation{File: rel, Line: i + 1, Reference: ref}
			switch {
			case strings.HasPrefix(ref, "./") || strings.HasPrefix(ref, ".\\"):
				// An action stored in this repository. It is pinned by the commit the workflow
				// itself runs at; there is no third party to move it.
				local++
				continue

			case strings.HasPrefix(ref, "docker://"):
				image := strings.TrimPrefix(ref, "docker://")
				if reImageRef.MatchString(image) {
					continue
				}
				v.Clause, v.Why = P1, "a docker:// step runs a published container image, which takes both a tag and a digest; write docker://"+withDigestHint(image)

			case !reActionSHA.MatchString(ref):
				v.Clause, v.Why = P3, "this names a tag or a branch, and a publisher moves those under every consumer that wrote one; pin owner/action@<40-char lowercase commit sha> and put the version in a trailing `# v...` comment"

			case reVersionComment.FindStringSubmatch(trailing) == nil:
				v.Clause, v.Why = P3, "the commit SHA is pinned but nothing says which released version it is, so the next bump is a guess; add a trailing comment naming it, e.g. `# v4`"

			default:
				continue
			}
			r.Violations = append(r.Violations, v)
		}
	}

	r.Categories = append(r.Categories, Category{
		Name:     "workflow action references",
		Examined: examined,
		Detail:   fmt.Sprintf("%d uses: line(s) across %d workflow(s); %d are local actions in this repo", examined, len(files), local),
	})
	return nil
}
