package vulngate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// rePinnedVersion is an EXACT govulncheck version: v1.1.4 and the like. `latest`, a branch, a bare
// commit or an empty string are all refused, because a gate whose tool version floats reports a
// different answer on different days and cannot be reproduced from the tree.
var rePinnedVersion = regexp.MustCompile(`^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.\-]+)?(?:\+[0-9A-Za-z.\-]+)?$`)

// toolName is the binary `go install` produces for the govulncheck command.
const toolName = "govulncheck"

// modulePath is the pinned tool, unchanged from what this gate has always run.
const modulePath = "golang.org/x/vuln/cmd/govulncheck"

// GovulncheckRunner builds the pinned govulncheck and then EXECUTES IT DIRECTLY, rather than through
// `go run`.
//
// That distinction is the whole reason this type exists. `go run pkg@version prog-args` collapses the
// program's exit status: when the program exits non-zero, `go run` prints "exit status N" on stderr and
// itself exits 1. govulncheck's status is what tells the gate whether the tool RAN — a `-format json`
// run that produced a report exits 0, and every other status means it did not produce one — and a gate
// that cannot tell those apart reads a tool that never ran as a tool that found nothing. So the tool is
// installed to a throwaway GOBIN at its pinned version and invoked as itself, and the status the gate
// reads is govulncheck's own.
type GovulncheckRunner struct {
	Version  string   // exact module version, e.g. "v1.1.4"
	Patterns []string // package patterns to scan, e.g. ["./..."]
	Dir      string   // working directory for the scan; empty means the process's own
}

// InstallArgs builds the `go install` argument list, refusing an unpinned version.
func (r GovulncheckRunner) InstallArgs() ([]string, error) {
	if !rePinnedVersion.MatchString(r.Version) {
		return nil, fmt.Errorf("govulncheck version %q is not an exact pin (want e.g. v1.1.4); GOVULNCHECK_VERSION is pinned in the Makefile and may move forward, never float", r.Version)
	}
	return []string{"install", modulePath + "@" + r.Version}, nil
}

// jsonFormat asks govulncheck for its machine-readable stream.
//
// This is the ONE flag the gate passes, and it WIDENS what the gate reads rather than narrowing it.
// govulncheck's text report splits the verdict into `=== Symbol Results ===`, `=== Package Results
// ===` and `=== Module Results ===`, and prints the last two only under `-show verbose`; anything
// reading that text reads part of the verdict, and C3/R5 reserves making the verdict smaller to
// `.govulncheck-suppressions.yaml`. The JSON stream has no sections — every advisory arrives as a
// `finding` object whatever depth it was traced to — and it carries the `fixed_version` R4 needs.
// No -scan, no -mode, no -show: nothing here may ask govulncheck for less than it knows.
var jsonFormat = []string{"-format", "json"}

// ScanArgs is the argument list govulncheck itself is given: the JSON format and the package
// patterns, nothing else.
func (r GovulncheckRunner) ScanArgs() ([]string, error) {
	if len(r.Patterns) == 0 {
		return nil, errors.New("no package patterns to scan; the gate scans ./... and must never be narrowed to nothing")
	}
	return append(append([]string(nil), jsonFormat...), r.Patterns...), nil
}

// Run executes govulncheck and returns its output and its own exit status. A tool that could not be
// built or could not be started comes back as an error, never as a clean result.
func (r GovulncheckRunner) Run(ctx context.Context) (Result, error) {
	installArgs, err := r.InstallArgs()
	if err != nil {
		return Result{}, err
	}
	scanArgs, err := r.ScanArgs()
	if err != nil {
		return Result{}, err
	}

	binDir, err := os.MkdirTemp("", "vulngate-")
	if err != nil {
		return Result{}, fmt.Errorf("making a directory for the pinned govulncheck: %w", err)
	}
	defer os.RemoveAll(binDir)

	var installOut bytes.Buffer
	install := exec.CommandContext(ctx, "go", installArgs...)
	install.Env = append(os.Environ(), "GOBIN="+binDir)
	install.Stdout = &installOut
	install.Stderr = &installOut
	if err := install.Run(); err != nil {
		return Result{Stderr: installOut.String()},
			fmt.Errorf("building the pinned govulncheck (`go %s`): %w: %s",
				strings.Join(installArgs, " "), err, strings.TrimSpace(installOut.String()))
	}

	var stdout, stderr bytes.Buffer
	scan := exec.CommandContext(ctx, filepath.Join(binDir, toolName), scanArgs...)
	scan.Dir = r.Dir
	scan.Stdout = &stdout
	scan.Stderr = &stderr
	scan.Env = os.Environ()

	runErr := scan.Run()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}

	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
		res.ExitCode = 0
	case errors.As(runErr, &exitErr):
		// The tool ran and chose a status. Whether that status is a verdict is the gate's call.
		res.ExitCode = exitErr.ExitCode()
	default:
		// The tool never ran: the binary vanished, the context was cancelled, exec failed.
		return res, fmt.Errorf("running the pinned govulncheck: %w", runErr)
	}
	return res, nil
}
