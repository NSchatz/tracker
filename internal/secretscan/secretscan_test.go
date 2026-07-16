package secretscan

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFile drops content into a temp dir under name and returns the dir.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", name, err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func rules(findings []Finding) map[string]int {
	m := map[string]int{}
	for _, f := range findings {
		m[f.Rule]++
	}
	return m
}

// pemHeader assembles a PEM private-key header at runtime so the contiguous literal never appears in
// this source file — that keeps the umbrella's own secret-scan tripwire (and human reviewers) from
// mistaking a scanner FIXTURE for a real committed key, while the assembled value still exercises the
// detector under test.
func pemHeader() string { return "-----BEGIN " + "PRIVATE" + " KEY-----" }

func TestScanFlagsRealSecrets(t *testing.T) {
	// Each of these is a shape that is almost never anything but a real secret.
	tree := writeTree(t, map[string]string{
		// A PEM private key header. The body is synthetic filler; the header is what matters.
		"deploy/tls.key": pemHeader() + "\nMIIBVAIBADANBg\nsynthetic\n",
		// An AWS access-key id (synthetic — the id shape, not a live key).
		"scripts/deploy.sh": "export AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE\n", // secretscan:allow — fixture written to a temp file, not a real key
		// A DSN carrying a real (non-placeholder) password.
		"config/prod.env": "DATABASE_URL=postgres://app:S3cr3tPw@db.internal:5432/tracker\n", // secretscan:allow — fixture, synthetic password
	})

	findings, err := Scan(tree)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	got := rules(findings)
	for _, want := range []string{"pem-private-key", "aws-access-key-id", "dsn-embedded-password"} {
		if got[want] == 0 {
			t.Errorf("Scan did not flag a %s; findings = %v", want, findings)
		}
	}
}

func TestScanIgnoresPlaceholdersAndReferences(t *testing.T) {
	// The synthetic, local-only credentials this repo commits on purpose, and env-var references —
	// none of these is a checked-in secret, and flagging them is how a lint gets disabled.
	tree := writeTree(t, map[string]string{
		"docker-compose.yml":         "  DATABASE_URL: postgres://tracker:${DB_PASSWORD:?set it}@db:5432/tracker\n",
		"internal/testsupport/pg.go": `const dsn = "postgres://tracker:tracker@localhost:5432/tracker"` + "\n",
		"README.md":                  "Example: postgres://tracker:changeme@localhost/tracker\n",
	})

	findings, err := Scan(tree)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("Scan flagged placeholders/references it should ignore: %v", findings)
	}
}

func TestScanHonorsAllowMarker(t *testing.T) {
	tree := writeTree(t, map[string]string{
		"config_test.go": "dsn := \"postgres://app:realsecret@host/db\" // " + allowMarker + " synthetic\n", // secretscan:allow — the marker under test lives inside the fixture string
	})
	findings, err := Scan(tree)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("the %s marker did not suppress the finding: %v", allowMarker, findings)
	}
}

func TestScanSkipsBinaryAndGitDirs(t *testing.T) {
	tree := writeTree(t, map[string]string{
		// A "binary" file whose first byte is NUL, carrying a secret shape that must NOT be scanned.
		"bin/blob": "\x00" + pemHeader() + "\n",
		// A .git directory is never descended into.
		".git/config": "url = postgres://app:leaked@host/db\n", // secretscan:allow — fixture inside a skipped-dir test
	})
	findings, err := Scan(tree)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("Scan looked where it must not: %v", findings)
	}
}

// TestRepositoryIsClean scans the whole tracker tree and asserts it holds no checked-in secret. This
// is the regression that keeps a real credential from ever landing in the repo — the same check
// `tracker config-lint` runs, pointed at the module root.
func TestRepositoryIsClean(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	findings, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan(%s): %v", root, err)
	}
	if len(findings) != 0 {
		t.Fatalf("the repository contains %d suspected checked-in secret(s): %v", len(findings), findings)
	}
}
