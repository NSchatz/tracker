// Command vulngate is the `govulncheck` step of `make check`.
//
// It runs the pinned govulncheck over the whole module and reads its WHOLE verdict — every advisory
// govulncheck reports, whether it traced a call path to a vulnerable symbol, found a vulnerable
// package imported, or only found a vulnerable module required. It then applies this repo's
// recorded-suppression policy: an advisory is either remediated at source or written down in
// .govulncheck-suppressions.yaml with a reason, a reachability argument and a date. Anything else
// fails. So does a record for an advisory that has a fix, a record whose advisory is no longer
// reported, a record that is malformed, and any govulncheck exit status that is not the one a run
// which produced a report exits with — a tool that could not run has not told us the code is clean.
//
// The version of govulncheck comes in on the command line because the Makefile owns tool pins and
// nothing else in the repo restates them. The suppression file does NOT: it is named once, in
// internal/vulngate, because C3/R5 makes it the only suppression surface there is, and a flag that
// can point the gate at a different file is a second way to answer that question.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/NSchatz/tracker/internal/vulngate"
)

func main() {
	version := flag.String("govulncheck-version", "",
		"exact pinned govulncheck version, e.g. v1.1.4 (owned by the Makefile)")
	flag.Parse()

	patterns := flag.Args()
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	gate := vulngate.Gate{
		Runner:           vulngate.GovulncheckRunner{Version: *version, Patterns: patterns},
		SuppressionsPath: vulngate.SuppressionsFile,
		Out:              os.Stdout,
	}

	if err := gate.Check(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "\nvulnerability gate FAILED\n%v\n", err)
		os.Exit(1)
	}
	fmt.Printf("vulnerability gate OK: every advisory govulncheck reports, at symbol, package and module level, is remediated at source or recorded in %s\n", vulngate.SuppressionsFile)
}
