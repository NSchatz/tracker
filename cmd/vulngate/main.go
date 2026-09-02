// Command vulngate is the `govulncheck` step of `make check`.
//
// It runs the pinned govulncheck unchanged and then applies this repo's recorded-suppression policy:
// an advisory is either remediated at source or written down in .govulncheck-suppressions.yaml with a
// reason, a reachability argument and a date. Anything else fails. So does a record whose advisory is
// no longer reported, a record that is malformed, and any govulncheck exit status that is not a
// verdict — a tool that could not run has not told us the code is clean.
//
// The version of govulncheck comes in on the command line because the Makefile owns tool pins and
// nothing else in the repo restates them.
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
	suppressions := flag.String("suppressions", vulngate.SuppressionsFile,
		"path to the recorded-suppression file; absent means no suppressions")
	flag.Parse()

	patterns := flag.Args()
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	gate := vulngate.Gate{
		Runner:           vulngate.GovulncheckRunner{Version: *version, Patterns: patterns},
		SuppressionsPath: *suppressions,
		Report:           os.Stdout,
	}

	if err := gate.Check(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "\nvulnerability gate FAILED\n%v\n", err)
		os.Exit(1)
	}
	fmt.Printf("vulnerability gate OK: every advisory govulncheck reports is remediated at source or recorded in %s\n", *suppressions)
}
