package pingate

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	// A compliant image reference: NAME:TAG@sha256:<64 lowercase hex>. Both halves are required by
	// P1 and they do different jobs - the tag is what a human reads, the digest is what resolves.
	// A digest with no tag is refused for the same reason a tag with no digest is: half the clause.
	reImageRef = regexp.MustCompile(`^[^\s@]+:[^\s@:/]+@sha256:[0-9a-f]{64}$`)

	// `FROM ...`, case-insensitively, which is how the Dockerfile parser reads it.
	reFrom = regexp.MustCompile(`^\s*(?i:FROM)\s+(.+)$`)
)

// ---------------------------------------------------------------------------
// Dockerfile FROM lines (P1, P2, P6)
// ---------------------------------------------------------------------------

func isDockerfile(rel string) bool {
	name := path.Base(rel)
	if name == "Dockerfile" || strings.HasPrefix(name, "Dockerfile.") {
		return true
	}
	return strings.HasSuffix(strings.ToLower(name), ".dockerfile")
}

func scanDockerfiles(root string, r *Report) error {
	files, err := walkFiles(root, isDockerfile)
	if err != nil {
		return err
	}

	examined := 0
	for _, rel := range files {
		lines, ok, err := readLines(root, rel)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}

		// Stage names declared by an earlier `AS <name>`. A later `FROM <name>` refers to a stage
		// built in THIS file, not to anything a registry serves, so there is no digest to demand -
		// and because the name must already have been declared above, this cannot be used to smuggle
		// an unpinned registry reference past the check.
		stages := map[string]bool{}

		for i, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			m := reFrom.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			examined++

			fields := strings.Fields(m[1])
			ref := ""
			for _, f := range fields {
				if strings.HasPrefix(f, "--") {
					continue // --platform=, --chmod= and friends are not the reference
				}
				ref = f
				break
			}
			for j := 0; j+1 < len(fields); j++ {
				if strings.EqualFold(fields[j], "AS") {
					stages[strings.ToLower(fields[j+1])] = true
				}
			}

			v := Violation{File: rel, Line: i + 1, Reference: strings.TrimSpace(m[1])}
			switch {
			case ref == "":
				v.Clause, v.Why = P2, "this FROM names no image at all"
			case strings.ContainsAny(ref, "$"):
				v.Reference = ref
				v.Clause, v.Why = P6, "the base image is assembled from a build argument, so this file does not state what it will build on and a caller chooses it at build time; write the reference out in full as NAME:TAG@sha256:<64 hex>"
			case stages[strings.ToLower(ref)]:
				continue // an earlier stage of this same build; nothing to pin
			case !reImageRef.MatchString(ref):
				v.Reference = ref
				v.Clause, v.Why = P2, "a base image is an image reference like any other and takes BOTH halves; write "+withDigestHint(ref)
			default:
				continue
			}
			r.Violations = append(r.Violations, v)
		}
	}

	r.Categories = append(r.Categories, Category{
		Name:     "container base images (Dockerfile FROM)",
		Examined: examined,
		Detail:   fmt.Sprintf("%d FROM instruction(s) across %d Dockerfile(s)", examined, len(files)),
	})
	return nil
}

// withDigestHint turns the offending reference into the shape the fix has to take, so the failure
// message is a patch rather than a complaint.
func withDigestHint(ref string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	}
	if !strings.Contains(path.Base(ref), ":") {
		return ref + ":<tag>@sha256:<64 hex>"
	}
	return ref + "@sha256:<64 hex>"
}

// ---------------------------------------------------------------------------
// compose service images (P1, P6)
// ---------------------------------------------------------------------------

func isComposeFile(rel string) bool {
	name := strings.ToLower(path.Base(rel))
	base := ""
	switch {
	case strings.HasSuffix(name, ".yml"):
		base = strings.TrimSuffix(name, ".yml")
	case strings.HasSuffix(name, ".yaml"):
		base = strings.TrimSuffix(name, ".yaml")
	default:
		return false
	}
	return base == "compose" || base == "docker-compose" ||
		strings.HasPrefix(base, "compose.") || strings.HasPrefix(base, "docker-compose.")
}

func scanComposeFiles(root string, r *Report) error {
	files, err := walkFiles(root, isComposeFile)
	if err != nil {
		return err
	}

	examined, built := 0, 0
	for _, rel := range files {
		data, ok, err := readFile(root, rel)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}

		var doc yaml.Node
		if err := yaml.Unmarshal(data, &doc); err != nil {
			// A compose file the gate cannot parse is a compose file the gate has not checked, and
			// "could not read it" must never round to "it is fine".
			r.Violations = append(r.Violations, Violation{
				File: rel, Line: 1, Reference: rel, Clause: P6,
				Why: "this compose file does not parse as YAML, so its image references could not be checked: " + err.Error(),
			})
			continue
		}

		for _, svc := range composeServices(&doc) {
			image := mappingValue(svc.node, "image")
			if image == nil {
				continue
			}
			examined++
			ref := image.Value

			// A service compose BUILDS has no publisher and no digest to resolve: `image:` is just
			// the local name the build output is tagged with, and what pins it is the Dockerfile's
			// own FROM lines. A service that names an image it does NOT build is pulling someone
			// else's bytes, and P1 reaches that one.
			if mappingValue(svc.node, "build") != nil {
				built++
				continue
			}

			v := Violation{File: rel, Line: image.Line, Reference: ref}
			switch {
			case strings.Contains(ref, "${") || strings.Contains(ref, "$("):
				v.Clause, v.Why = P6, fmt.Sprintf("service %q takes its image from an environment variable, so this file does not state what it runs; write the reference out in full", svc.name)
			case !reImageRef.MatchString(ref):
				v.Clause, v.Why = P1, fmt.Sprintf("service %q is not built from this repository, so it pulls a published image and takes BOTH a tag and a digest; write %s", svc.name, withDigestHint(ref))
			default:
				continue
			}
			r.Violations = append(r.Violations, v)
		}
	}

	r.Categories = append(r.Categories, Category{
		Name:     "compose service images",
		Examined: examined,
		Detail:   fmt.Sprintf("%d image: key(s) across %d compose file(s); %d belong to services built from this repo and need no digest", examined, len(files), built),
	})
	return nil
}

type composeService struct {
	name string
	node *yaml.Node
}

func composeServices(doc *yaml.Node) []composeService {
	if len(doc.Content) == 0 {
		return nil
	}
	services := mappingValue(doc.Content[0], "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return nil
	}
	var out []composeService
	for i := 0; i+1 < len(services.Content); i += 2 {
		out = append(out, composeService{name: services.Content[i].Value, node: services.Content[i+1]})
	}
	return out
}

// mappingValue returns the value node for key in a YAML mapping, or nil.
func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}
