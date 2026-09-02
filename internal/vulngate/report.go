package vulngate

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// symbolResultsMarker heads the section govulncheck uses for the advisories it found a CALL PATH to —
// the ones that fail the run. Its absence means the output is not a govulncheck report at all, and an
// absent report is never read as a clean one.
const symbolResultsMarker = "=== Symbol Results ==="

// sectionMarker starts any other results section (package-level, module-level). Only the symbol
// section is the gate's business: those are the advisories govulncheck reports as affecting this code.
const sectionMarker = "\n=== "

var reReportedID = regexp.MustCompile(`(?m)^Vulnerability #\d+:\s+(GO-[0-9]{4}-[0-9]+)\b`)

// ParseReport extracts the advisory ids govulncheck reported as reachable from this code, in sorted
// order. It fails rather than returning an empty set when the output does not look like a report: a
// tool whose output cannot be parsed has not told us there are no vulnerabilities.
func ParseReport(stdout string) ([]string, error) {
	idx := strings.Index(stdout, symbolResultsMarker)
	if idx < 0 {
		return nil, fmt.Errorf("govulncheck output has no %q section, so it cannot be read as a report; treating an unparseable report as a clean one is how a gate goes quiet", symbolResultsMarker)
	}

	section := stdout[idx+len(symbolResultsMarker):]
	if next := strings.Index(section, sectionMarker); next >= 0 {
		section = section[:next]
	}

	seen := map[string]bool{}
	var ids []string
	for _, m := range reReportedID.FindAllStringSubmatch(section, -1) {
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		ids = append(ids, m[1])
	}
	sort.Strings(ids)
	return ids, nil
}
