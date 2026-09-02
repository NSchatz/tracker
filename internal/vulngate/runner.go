package vulngate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
)

// rePinnedVersion is an EXACT govulncheck version: v1.1.4 and the like. `latest`, a branch, a bare
// commit or an empty string are all refused, because a gate whose tool version floats reports a
// different answer on different days and cannot be reproduced from the tree.
var rePinnedVersion = regexp.MustCompile(`^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.\-]+)?(?:\+[0-9A-Za-z.\-]+)?$`)

// GovulncheckRunner invokes the pinned govulncheck exactly as `make govulncheck` always has:
// `go run golang.org/x/vuln/cmd/govulncheck@<version> ./...`. The version comes from the Makefile,
// which is the only place tool versions are pinned in this repo.
type GovulncheckRunner struct {
	Version  string   // exact module version, e.g. "v1.1.4"
	Patterns []string // package patterns to scan, e.g. ["./..."]
	Dir      string   // working directory; empty means the process's own
}

// Args builds the `go run` argument list, refusing an unpinned version.
func (r GovulncheckRunner) Args() ([]string, error) {
	if !rePinnedVersion.MatchString(r.Version) {
		return nil, fmt.Errorf("govulncheck version %q is not an exact pin (want e.g. v1.1.4); GOVULNCHECK_VERSION is pinned in the Makefile and may move forward, never float", r.Version)
	}
	if len(r.Patterns) == 0 {
		return nil, errors.New("no package patterns to scan; the gate scans ./... and must never be narrowed to nothing")
	}
	args := []string{"run", "golang.org/x/vuln/cmd/govulncheck@" + r.Version}
	return append(args, r.Patterns...), nil
}

// Run executes govulncheck and returns its output and exit status. A process that could not be started
// at all comes back as an error, never as a clean result.
func (r GovulncheckRunner) Run(ctx context.Context) (Result, error) {
	args, err := r.Args()
	if err != nil {
		return Result{}, err
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = r.Dir
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = os.Environ()

	runErr := cmd.Run()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}

	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		res.ExitCode = 0
	case errors.As(runErr, &exitErr):
		// The tool ran and chose a status. Whether that status is a verdict is the gate's call.
		res.ExitCode = exitErr.ExitCode()
	default:
		// The tool never ran: `go` missing, the context cancelled, the binary unexecutable.
		return res, fmt.Errorf("running `go %s`: %w", args[0], runErr)
	}
	return res, nil
}
