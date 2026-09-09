package pingate

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Impl-gate refuter evidence for S0059-tracker-pinning-1 finding F1. It FAILS today: it is an
// artifact, not a fix. See verdict-impl-1.md for the clauses and the argument.
func TestRegress0059F1TestSupportImageIsUnpinnedAndUnexamined(t *testing.T) {
	const rel = "internal/testsupport/postgis.go"

	data, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}

	reConst := regexp.MustCompile(`PostGISImage\s*=\s*"([^"]+)"`)
	var ref string
	var line int
	for i, l := range strings.Split(string(data), "\n") {
		if m := reConst.FindStringSubmatch(l); m != nil {
			ref, line = m[1], i+1
			break
		}
	}
	if ref == "" {
		t.Fatalf("%s no longer declares PostGISImage; this regression artifact needs re-aiming", rel)
	}

	if !reImageRef.MatchString(ref) {
		t.Errorf("%s:%d: %s: breaks P1 (%s) of %s: this image is pulled by testcontainers on every `make test`, and a tag alone is moved by its publisher; write %s",
			rel, line, ref, P1.Summary, ConventionsFile, withDigestHint(ref))
	}

	report, err := Scan(repoRoot)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	examined := false
	for _, v := range report.Violations {
		if v.File == rel {
			examined = true
		}
	}
	if !examined && !reImageRef.MatchString(ref) {
		t.Errorf("the pin gate passed a working tree that carries the unpinned image reference %s at %s:%d, and reported no violation naming that file: AC-12's \"every image reference ... in the working tree\" is not what the scanners cover (Dockerfile FROM and compose image: only)",
			ref, rel, line)
	}

	composed, err := os.ReadFile(filepath.Join(repoRoot, "docker-compose.yml"))
	if err != nil {
		t.Fatalf("reading docker-compose.yml: %v", err)
	}
	if strings.Contains(string(composed), ref+"@sha256:") && !strings.Contains(ref, "@sha256:") {
		t.Errorf("docker-compose.yml pins %s by digest while %s:%d still tracks the bare tag: the database the spatial tests run against and the database the stack runs are no longer the same reference",
			ref, rel, line)
	}
}
