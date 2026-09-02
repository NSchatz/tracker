package vulngate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// govulncheck's own exit statuses. Anything else — a tool that failed to download, a module graph it
// could not read, an output it could not produce — is a tool that DID NOT RUN, which is not the same
// thing as a tool that found nothing.
const (
	// exitNoVulnerabilities: the scan completed and found nothing reachable.
	exitNoVulnerabilities = 0
	// exitVulnerabilitiesFound: the scan completed and found reachable advisories.
	exitVulnerabilitiesFound = 3
)

// Result is one govulncheck invocation's outcome.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Runner executes govulncheck. Production uses GovulncheckRunner; the gate's own tests substitute a
// fake so they can prove the verdict logic still turns red without needing a vulnerable dependency.
type Runner interface {
	Run(ctx context.Context) (Result, error)
}

// Gate is the whole vulnerability gate: run the pinned tool, apply the recorded suppressions, decide.
type Gate struct {
	Runner Runner
	// SuppressionsPath is the recorded-suppression file. Absent is legal and means "none".
	SuppressionsPath string
	// Report receives govulncheck's own output verbatim, so a failing gate shows the tool's report and
	// not a summary of it.
	Report io.Writer
}

// Check runs the gate and returns nil only when every advisory govulncheck reported is covered by a
// well-formed, non-stale record.
func (g Gate) Check(ctx context.Context) error {
	// Validate the records FIRST. A malformed record must fail the gate even on a day when govulncheck
	// happens to report nothing, because otherwise it sits there unnoticed until the day it is asked
	// to carry an advisory and cannot.
	entries, err := LoadSuppressions(g.SuppressionsPath)
	if err != nil {
		return fmt.Errorf("%s is not a valid suppression record set:\n%w", g.SuppressionsPath, err)
	}

	res, err := g.Runner.Run(ctx)
	g.echo(res)
	if err != nil {
		return fmt.Errorf("govulncheck could not run: %w", err)
	}

	switch res.ExitCode {
	case exitNoVulnerabilities, exitVulnerabilitiesFound:
		// The tool ran and produced a verdict.
	default:
		return fmt.Errorf("govulncheck exited with status %d, which is not a verdict: %s",
			res.ExitCode, firstNonEmpty(strings.TrimSpace(res.Stderr), strings.TrimSpace(res.Stdout), "no output"))
	}

	reported, err := ParseReport(res.Stdout)
	if err != nil {
		return err
	}
	if res.ExitCode == exitVulnerabilitiesFound && len(reported) == 0 {
		return fmt.Errorf("govulncheck reported vulnerabilities (exit status %d) but no advisory id could be read out of its output; refusing to pass on a report we cannot parse", res.ExitCode)
	}
	if res.ExitCode == exitNoVulnerabilities && len(reported) > 0 {
		return fmt.Errorf("govulncheck exited 0 but its output names %s; refusing to pass on a self-contradictory report", strings.Join(reported, ", "))
	}

	return Evaluate(reported, entries)
}

func (g Gate) echo(res Result) {
	if g.Report == nil {
		return
	}
	if res.Stdout != "" {
		fmt.Fprint(g.Report, res.Stdout)
		if !strings.HasSuffix(res.Stdout, "\n") {
			fmt.Fprintln(g.Report)
		}
	}
	if res.Stderr != "" {
		fmt.Fprint(g.Report, res.Stderr)
		if !strings.HasSuffix(res.Stderr, "\n") {
			fmt.Fprintln(g.Report)
		}
	}
}

// Evaluate is the verdict: every reported advisory must be recorded, and every record must still have
// an advisory to cover.
func Evaluate(reported []string, entries []Entry) error {
	recorded := make(map[string]Entry, len(entries))
	for _, e := range entries {
		recorded[e.ID] = e
	}
	isReported := make(map[string]bool, len(reported))
	for _, id := range reported {
		isReported[id] = true
	}

	var problems []error

	var unrecorded []string
	for _, id := range reported {
		if _, ok := recorded[id]; !ok {
			unrecorded = append(unrecorded, id)
		}
	}
	if len(unrecorded) > 0 {
		sort.Strings(unrecorded)
		problems = append(problems, fmt.Errorf("govulncheck reports %d advisory(ies) that are neither remediated at source nor recorded in %s: %s\n  Remediate: bump the implicated module, or raise the pinned Go toolchain to the version govulncheck names in `Fixed in`.\n  Only if NO fix exists anywhere, add a record with id, reason, reachability and recorded",
			len(unrecorded), SuppressionsFile, strings.Join(unrecorded, ", ")))
	}

	var stale []string
	for _, e := range entries {
		if !isReported[e.ID] {
			stale = append(stale, fmt.Sprintf("%s (recorded %s)", e.ID, e.Recorded))
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		problems = append(problems, fmt.Errorf("%s records %d advisory(ies) the current govulncheck run does not report: %s\n  A suppression may not outlive the advisory it was written for. Delete the record",
			SuppressionsFile, len(stale), strings.Join(stale, ", ")))
	}

	return errors.Join(problems...)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
