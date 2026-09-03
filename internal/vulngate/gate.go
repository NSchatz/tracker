package vulngate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// exitReportProduced is the ONLY status govulncheck exits with when a `-format json` run produced a
// report. Unlike the text report — where 3 means "reachable vulnerabilities found" — the JSON stream
// carries the verdict in its findings, so the status carries only "did I run".
//
// Anything else is therefore a tool that DID NOT RUN: it could not be downloaded, the module graph
// was unreadable, it crashed, or its status conventions changed under us. None of those is a tool
// that found nothing, and the gate refuses all of them with the tool's own error.
const exitReportProduced = 0

// maxRawEcho caps how much unparseable output is echoed. The JSON stream is hundreds of kilobytes;
// when it cannot be read, the first few thousand bytes say why and the rest is noise.
const maxRawEcho = 4000

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

// Gate is the whole vulnerability gate: run the pinned tool, read its whole verdict, apply the
// recorded suppressions, decide.
type Gate struct {
	Runner Runner
	// SuppressionsPath is the recorded-suppression file. Absent is legal and means "none".
	SuppressionsPath string
	// Out receives the report, so a failing gate shows what govulncheck actually said.
	Out io.Writer
}

// Check runs the gate and returns nil only when every advisory govulncheck reported — at symbol,
// package OR module level — is covered by a well-formed, non-stale record for an advisory that has
// no fix.
func (g Gate) Check(ctx context.Context) error {
	// Validate the records FIRST. A malformed record must fail the gate even on a day when govulncheck
	// happens to report nothing, because otherwise it sits there unnoticed until the day it is asked
	// to carry an advisory and cannot.
	entries, err := LoadSuppressions(g.SuppressionsPath)
	if err != nil {
		return fmt.Errorf("%s is not a valid suppression record set:\n%w", g.SuppressionsPath, err)
	}

	res, err := g.Runner.Run(ctx)
	if err != nil {
		g.echoRaw(res)
		return fmt.Errorf("govulncheck could not run: %w", err)
	}

	if res.ExitCode != exitReportProduced {
		g.echoRaw(res)
		return fmt.Errorf("govulncheck exited with status %d; a `-format json` run that produced a report exits %d, so this run did not produce one: %s",
			res.ExitCode, exitReportProduced,
			firstNonEmpty(strings.TrimSpace(res.Stderr), strings.TrimSpace(res.Stdout), "no output"))
	}

	report, err := ParseReport(res.Stdout)
	if err != nil {
		g.echoRaw(res)
		return err
	}
	g.echoReport(report, res.Stderr)

	return Evaluate(report.Advisories, entries)
}

// echoReport prints the whole verdict: every advisory at every level, and anything the tool wrote to
// stderr.
func (g Gate) echoReport(report *Report, stderr string) {
	if g.Out == nil {
		return
	}
	fmt.Fprint(g.Out, report.String())
	g.echoStderr(stderr)
}

// echoRaw prints the tool's own output verbatim, for the paths where there is no report to render.
func (g Gate) echoRaw(res Result) {
	if g.Out == nil {
		return
	}
	if out := strings.TrimSpace(res.Stdout); out != "" {
		if len(out) > maxRawEcho {
			out = fmt.Sprintf("%s\n... (truncated; %d bytes of output in total)", out[:maxRawEcho], len(res.Stdout))
		}
		fmt.Fprintln(g.Out, out)
	}
	g.echoStderr(res.Stderr)
}

func (g Gate) echoStderr(stderr string) {
	if out := strings.TrimSpace(stderr); out != "" {
		fmt.Fprintln(g.Out, out)
	}
}

// Evaluate is the verdict:
//
//   - every reported advisory must be recorded (or it should have been remediated at source);
//   - no record may cover an advisory that HAS a fix — R4 sends that case to the module graph or the
//     toolchain, never to this file;
//   - every record must still have an advisory to cover — R1, a suppression may not outlive it.
func Evaluate(reported []Advisory, entries []Entry) error {
	recorded := make(map[string]Entry, len(entries))
	for _, e := range entries {
		recorded[e.ID] = e
	}
	isReported := make(map[string]bool, len(reported))
	for _, a := range reported {
		isReported[a.ID] = true
	}

	var problems []error

	var unrecorded []string
	for _, a := range reported {
		if _, ok := recorded[a.ID]; ok {
			continue
		}
		remedy := "no fix reported"
		if a.HasFix() {
			remedy = "fixed in " + a.FixedVersion
		}
		unrecorded = append(unrecorded, fmt.Sprintf("%s (%s level, %s@%s, %s)", a.ID, a.Level, a.Module, a.Version, remedy))
	}
	if len(unrecorded) > 0 {
		sort.Strings(unrecorded)
		problems = append(problems, fmt.Errorf("govulncheck reports %d advisory(ies) that are neither remediated at source nor recorded in %s: %s\n  Remediate: bump the implicated module, or raise the pinned Go toolchain to the version govulncheck names in `Fixed in`.\n  Only if NO fix exists anywhere, add a record with id, reason, reachability and recorded",
			len(unrecorded), SuppressionsFile, strings.Join(unrecorded, ", ")))
	}

	var fixable []string
	for _, a := range reported {
		if _, ok := recorded[a.ID]; !ok {
			continue
		}
		if !a.HasFix() {
			continue
		}
		fixable = append(fixable, fmt.Sprintf("%s (fixed in %s; found in %s@%s)", a.ID, a.FixedVersion, a.Module, a.Version))
	}
	if len(fixable) > 0 {
		sort.Strings(fixable)
		problems = append(problems, fmt.Errorf("%s records %d advisory(ies) govulncheck says HAS a fix: %s\n  A record covers an advisory with no fix anywhere, and nothing else. Take the fix at source - bump the module, or raise the pinned Go toolchain - and delete the record",
			SuppressionsFile, len(fixable), strings.Join(fixable, ", ")))
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
