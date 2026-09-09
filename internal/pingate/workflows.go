package pingate

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
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

// scanWorkflows enforces P3 over every action any workflow uses, and P1 over every container image
// a workflow names: a job may run IN a container (`container:`) and may attach service containers
// (`services:`), and both are published images pulled at run time exactly as a compose `image:` is.
//
// `runs-on: ubuntu-latest` is deliberately NOT reached: it is a RUNNER LABEL that selects a
// GitHub-hosted machine pool, not an image reference this repo could pin, and refusing it would be
// refusing a thing with no compliant form.
func scanWorkflows(root string, r *Report) error {
	files, err := walkFiles(root, isWorkflow)
	if err != nil {
		return err
	}

	examined, local, images := 0, 0, 0
	for _, rel := range files {
		lines, ok, err := readLines(root, rel)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}

		n, violations := scanWorkflowImages(root, rel)
		examined += n
		images += n
		r.Violations = append(r.Violations, violations...)
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
		Name:     "workflow action and image references",
		Examined: examined,
		Detail: fmt.Sprintf("%d uses: line(s) and %d job container/service image(s) across %d workflow(s); %d are local actions in this repo",
			examined-images, images, len(files), local),
	})
	return nil
}

// scanWorkflowImages reads the container images a workflow runs jobs in and attaches as services.
// It parses the YAML rather than reading lines, because `container:` takes either a bare image
// string or a mapping with an `image:` key and a line scan cannot tell one job's `image:` from
// another key that happens to be spelled the same.
func scanWorkflowImages(root, rel string) (int, []Violation) {
	data, ok, err := readFile(root, rel)
	if err != nil || !ok {
		return 0, nil
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		// The same stance the compose scanner takes: a workflow the gate could not parse is a
		// workflow the gate has not checked, and that must never round to "it is fine".
		return 0, []Violation{{
			File: rel, Line: 1, Reference: rel, Clause: P6,
			Why: "this workflow does not parse as YAML, so the container images it names could not be checked: " + err.Error(),
		}}
	}
	if len(doc.Content) == 0 {
		return 0, nil
	}

	jobs := mappingValue(doc.Content[0], "jobs")
	if jobs == nil || jobs.Kind != yaml.MappingNode {
		return 0, nil
	}

	examined := 0
	var out []Violation
	for i := 0; i+1 < len(jobs.Content); i += 2 {
		job, node := jobs.Content[i].Value, jobs.Content[i+1]
		if node.Kind != yaml.MappingNode {
			continue
		}

		if c := mappingValue(node, "container"); c != nil {
			image := c
			if c.Kind == yaml.MappingNode {
				image = mappingValue(c, "image")
			}
			if image != nil && image.Kind == yaml.ScalarNode {
				examined++
				if v, red := workflowImageViolation(rel, image, fmt.Sprintf("job %q runs in this container", job)); red {
					out = append(out, v)
				}
			}
		}

		services := mappingValue(node, "services")
		if services == nil || services.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(services.Content); j += 2 {
			name, svc := services.Content[j].Value, services.Content[j+1]
			image := mappingValue(svc, "image")
			if image == nil || image.Kind != yaml.ScalarNode {
				continue
			}
			examined++
			if v, red := workflowImageViolation(rel, image, fmt.Sprintf("job %q attaches service %q from this image", job, name)); red {
				out = append(out, v)
			}
		}
	}
	return examined, out
}

// workflowImageViolation grades one workflow image reference. There is no build: a workflow names
// an image someone else publishes, so P1 reaches every one of them with no exemption of the kind
// compose gives a service built from this repository.
func workflowImageViolation(rel string, image *yaml.Node, where string) (Violation, bool) {
	ref := image.Value
	v := Violation{File: rel, Line: image.Line, Reference: ref}
	switch {
	case strings.Contains(ref, "${{") || strings.Contains(ref, "${"):
		v.Clause, v.Why = P6, where+", and it is assembled from an expression, so this file does not state what it runs; write the reference out in full"
		return v, true
	case !reImageRef.MatchString(ref):
		v.Clause, v.Why = P1, where+", and a workflow pulls a published image it does not build, so it takes BOTH a tag and a digest; write "+withDigestHint(ref)
		return v, true
	}
	return Violation{}, false
}
