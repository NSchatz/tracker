// Package secretscan is the "no secrets in the repo" half of `config-lint` (roadmap §7: secrets live
// in the environment or a secret store, never checked in).
//
// It is deliberately a HIGH-SIGNAL, low-false-positive scanner, not a general entropy hunter. The
// umbrella already runs a detective secret-scan hook; this is the repo's own preventive gate, and a
// gate that cries wolf gets disabled. So it matches only patterns that are almost never anything but
// a real secret — a PEM private key, an AWS access-key id, or a database DSN carrying a real embedded
// password — and it carries an allowlist of the synthetic, local-only placeholders this repo commits
// on purpose (the compose password reference, the testcontainer's throwaway "tracker" credential).
//
// The failure it prevents is a service-account key, a private TLS key, or a real DSN password landing
// in git history, where rotating it is the only remedy and it is already too late.
package secretscan

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Finding is one suspected secret: where it is and what matched.
type Finding struct {
	File string // path relative to the scanned root
	Line int    // 1-based
	Rule string // which detector fired
	// Excerpt is a short, REDACTED snippet — enough to locate the line, never the secret itself, so
	// the lint output does not itself become a place the secret is written down.
	Excerpt string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d: %s (%s)", f.File, f.Line, f.Rule, f.Excerpt)
}

// maxFileBytes skips anything larger than 5 MiB. Secrets are small; a large file is a build
// artifact, a vendored blob, or the compiled binary, and reading it wastes the scan.
const maxFileBytes = 5 << 20

var (
	// A PEM private key header of any flavour (RSA/EC/OPENSSH/PKCS#8). The single highest-signal
	// secret shape there is: nothing legitimate commits one.
	rePEMPrivateKey = regexp.MustCompile(`-----BEGIN (?:[A-Z0-9]+ )?PRIVATE KEY-----`)

	// An AWS access-key id: literally "AKIA" or "ASIA" then 16 upper-alphanumerics.
	reAWSKey = regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`)

	// A postgres DSN with an embedded user:password. The password is captured so it can be checked
	// against the placeholder allowlist — a DSN with a real password is the secret; one with the
	// synthetic "tracker" placeholder or a ${VAR} reference is not.
	rePostgresDSN = regexp.MustCompile(`postgres(?:ql)?://[^:@/\s]+:([^@/\s]+)@`)
)

// placeholderPasswords are the synthetic, local-only DSN passwords this repo commits deliberately —
// the testcontainer credential and the common dev defaults. A DSN password in this set is not a
// finding. Anything OUTSIDE it (a real-looking password) is.
var placeholderPasswords = map[string]bool{
	"tracker":  true,
	"postgres": true,
	"password": true,
	"changeme": true,
	"example":  true,
	"secret":   true, // the literal word, e.g. in prose/examples — not a real credential
}

// skipDirs are never descended into: VCS metadata and dependency/build trees that hold third-party
// code whose contents are not this repo's responsibility to lint.
var skipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	".worktrees":   true,
}

// Scan walks root and returns every suspected checked-in secret. A returned nil error with an empty
// slice means the tree is clean. Directory descent errors (a permission problem) are surfaced, not
// swallowed: a scan that could not read part of the tree has not proven that part clean.
func Scan(root string) ([]Finding, error) {
	var findings []Finding

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil // symlinks, devices, sockets — nothing to scan
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > maxFileBytes {
			return nil
		}

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		fs, scanErr := scanFile(path, rel)
		if scanErr != nil {
			return scanErr
		}
		findings = append(findings, fs...)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", root, err)
	}
	return findings, nil
}

func scanFile(path, rel string) ([]Finding, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", rel, err)
	}
	defer func() { _ = f.Close() }()

	var findings []Finding
	sc := bufio.NewScanner(f)
	// Allow long lines (minified assets, embedded data) without erroring the whole scan.
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)

	lineNo := 0
	binary := false
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		if lineNo == 1 && strings.IndexByte(line, 0) >= 0 {
			// A NUL in the first line means a binary file — a compiled binary, an image. Do not
			// scan it: it produces garbage matches and is never where a secret is authored.
			binary = true
			break
		}
		findings = append(findings, scanLine(rel, lineNo, line)...)
	}
	if binary {
		return nil, nil
	}
	if err := sc.Err(); err != nil {
		// A scanner error (e.g. a line past the buffer cap) means we did not fully read the file;
		// treat it as unscannable rather than silently clean.
		return nil, fmt.Errorf("read %s: %w", rel, err)
	}
	return findings, nil
}

// allowMarker is an inline, auditable escape hatch: a line carrying it is not scanned. It exists for
// the rare deliberate case — a synthetic password in a redaction test, a documentation example — so
// the scanner can stay strict (no broad allowlist of every test's fake password) while a human can
// still say, on the exact line and in review, "this one is not a real secret".
const allowMarker = "secretscan:allow"

func scanLine(rel string, lineNo int, line string) []Finding {
	if strings.Contains(line, allowMarker) {
		return nil
	}

	var findings []Finding

	if rePEMPrivateKey.MatchString(line) {
		findings = append(findings, Finding{File: rel, Line: lineNo, Rule: "pem-private-key", Excerpt: "-----BEGIN … PRIVATE KEY-----"})
	}
	if m := reAWSKey.FindString(line); m != "" {
		findings = append(findings, Finding{File: rel, Line: lineNo, Rule: "aws-access-key-id", Excerpt: m[:4] + "…"})
	}
	for _, m := range rePostgresDSN.FindAllStringSubmatch(line, -1) {
		pw := m[1]
		if strings.HasPrefix(pw, "$") || strings.HasPrefix(pw, "%") {
			continue // a ${VAR} / ${VAR:?…} reference or a percent-encoded token — not a literal secret
		}
		if placeholderPasswords[strings.ToLower(pw)] {
			continue
		}
		findings = append(findings, Finding{File: rel, Line: lineNo, Rule: "dsn-embedded-password", Excerpt: "postgres://…:<redacted>@…"}) // secretscan:allow — the excerpt string, not a real DSN
	}
	return findings
}
