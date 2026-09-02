// Package vulngate is tracker's vulnerability gate: it runs the pinned govulncheck, reads its report,
// and decides the exit status.
//
// The rule it enforces is that an advisory is either REMEDIATED AT SOURCE — bump the module, raise the
// Go toolchain — or RECORDED, never ignored. `.govulncheck-suppressions.yaml` is the only surface that
// can keep the gate green in the face of a reported advisory, every entry has to say why no fix exists
// and why the reported call path cannot be exercised, and an entry is honoured only for as long as
// govulncheck still reports the advisory it was written for. A suppression that outlives its advisory
// is a lie the next reader has no way to detect, so it fails the gate too.
//
// Everything here is deliberately separable from the subprocess: the report parser, the record
// validator and the verdict are pure functions over strings, so the gate's own tests can feed it a
// synthetic report and prove it still turns red. A gate nobody can demonstrate biting is a gate nobody
// should trust.
package vulngate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// SuppressionsFile is the ONLY suppression surface in this repo. Nothing else may discard, downgrade
// or partially read govulncheck's verdict.
const SuppressionsFile = ".govulncheck-suppressions.yaml"

// recordedLayout is the ISO date every entry is stamped with.
const recordedLayout = "2006-01-02"

// reAdvisoryID matches an OSV identifier exactly as govulncheck prints it.
var reAdvisoryID = regexp.MustCompile(`^GO-[0-9]{4}-[0-9]+$`)

// requiredKeys is EXACTLY the set every entry carries. Missing one is malformed; carrying an extra one
// is malformed too, because an unrecognised key is either a typo that silently drops the field it meant
// to set or an attempt to widen the suppression surface.
var requiredKeys = []string{"id", "reason", "reachability", "recorded"}

// Entry is one recorded suppression: an advisory with no fix available anywhere, carried deliberately
// with its argument attached.
type Entry struct {
	// ID is the OSV identifier, matching GO-[0-9]{4}-[0-9]+.
	ID string
	// Reason says why no remediation exists today, naming the module or stdlib package.
	Reason string
	// Reachability quotes the symbol and call path govulncheck reports, and argues why this repo
	// cannot exercise it.
	Reachability string
	// Recorded is the ISO date the entry was written, so a stale record is visible as old.
	Recorded string
}

// LoadSuppressions reads the recorded suppressions from path.
//
// An ABSENT file means "no suppressions" and is not an error: a repo with nothing to suppress should
// not have to carry an empty file to satisfy the gate. It is also not a way to pass the gate — with no
// entries, every reported advisory is unrecorded and fails.
func LoadSuppressions(path string) ([]Entry, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return ParseSuppressions(path, data)
}

// ParseSuppressions decodes and fully validates the suppression file. Every malformed entry is named;
// none is silently skipped and none is silently honoured.
func ParseSuppressions(name string, data []byte) ([]Entry, error) {
	var nodes []yaml.Node
	if err := yaml.Unmarshal(data, &nodes); err != nil {
		return nil, fmt.Errorf("%s: not a YAML sequence of suppression records: %w", name, err)
	}
	// A file that is empty, holds only comments, or holds an explicit empty sequence decodes to no
	// nodes. That is "no suppressions", which is legal.
	if len(nodes) == 0 {
		return nil, nil
	}

	var problems []error
	entries := make([]Entry, 0, len(nodes))
	seen := map[string]int{}

	for i, node := range nodes {
		where := fmt.Sprintf("%s: entry #%d (line %d)", name, i+1, node.Line)

		if node.Kind != yaml.MappingNode {
			problems = append(problems, fmt.Errorf("%s: is not a mapping; every entry is a mapping with exactly the keys %s",
				where, strings.Join(requiredKeys, ", ")))
			continue
		}

		var fields map[string]string
		if err := node.Decode(&fields); err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", where, err))
			continue
		}

		if id := strings.TrimSpace(fields["id"]); id != "" {
			where = fmt.Sprintf("%s: entry #%d (line %d, id %s)", name, i+1, node.Line, id)
		}

		var missing, empty []string
		for _, key := range requiredKeys {
			value, ok := fields[key]
			switch {
			case !ok:
				missing = append(missing, key)
			case strings.TrimSpace(value) == "":
				empty = append(empty, key)
			}
		}
		var unknown []string
		for key := range fields {
			if !isRequiredKey(key) {
				unknown = append(unknown, key)
			}
		}

		entryOK := true
		if len(missing) > 0 {
			problems = append(problems, fmt.Errorf("%s: missing required key(s) %s", where, strings.Join(sorted(missing), ", ")))
			entryOK = false
		}
		if len(empty) > 0 {
			problems = append(problems, fmt.Errorf("%s: empty value for key(s) %s; a record with nothing written in it records nothing", where, strings.Join(sorted(empty), ", ")))
			entryOK = false
		}
		if len(unknown) > 0 {
			problems = append(problems, fmt.Errorf("%s: unknown key(s) %s; the entry keys are exactly %s", where, strings.Join(sorted(unknown), ", "), strings.Join(requiredKeys, ", ")))
			entryOK = false
		}

		id := strings.TrimSpace(fields["id"])
		if id != "" && !reAdvisoryID.MatchString(id) {
			problems = append(problems, fmt.Errorf("%s: id %q does not match GO-[0-9]{4}-[0-9]+", where, id))
			entryOK = false
		}
		if recorded := strings.TrimSpace(fields["recorded"]); recorded != "" {
			if _, err := time.Parse(recordedLayout, recorded); err != nil {
				problems = append(problems, fmt.Errorf("%s: recorded %q is not an ISO date (YYYY-MM-DD)", where, recorded))
				entryOK = false
			}
		}
		if id != "" {
			if first, dup := seen[id]; dup {
				problems = append(problems, fmt.Errorf("%s: duplicates entry #%d; one advisory gets one entry", where, first))
				entryOK = false
			} else {
				seen[id] = i + 1
			}
		}

		if !entryOK {
			continue
		}
		entries = append(entries, Entry{
			ID:           id,
			Reason:       strings.TrimSpace(fields["reason"]),
			Reachability: strings.TrimSpace(fields["reachability"]),
			Recorded:     strings.TrimSpace(fields["recorded"]),
		})
	}

	if err := errors.Join(problems...); err != nil {
		return nil, err
	}
	return entries, nil
}

func isRequiredKey(key string) bool {
	for _, k := range requiredKeys {
		if k == key {
			return true
		}
	}
	return false
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
