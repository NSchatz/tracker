// Command prosecheck is the `prose-check` step of `make check`.
//
// It measures every tracked Go file's comment density from the token stream and
// refuses one whose prose is over the ceiling internal/prosegate enforces and
// COMMENT-DENSITY-RECORD.md records, naming the file, its measured ratio and the
// ceiling. It also refuses a record that no longer agrees with the gate, a file
// it cannot read or parse, and a sweep that measured nothing.
//
// Like cmd/pincheck it needs NOTHING: no Docker daemon, no Android SDK, no
// credentials and no network. It reads files. That is why it can be reached on
// its own, and why it reaches the same verdict on an airgapped laptop as in CI.
// internal/prosegate's own tests run inside `make test` as well, so this gate
// arrives in CI without a second step in ci.yml to drift away from the gate a
// human runs.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/NSchatz/tracker/internal/prosegate"
)

func main() {
	root := flag.String("root", ".", "repository root to measure")
	table := flag.Bool("table", false, "print the per-file table COMMENT-DENSITY-RECORD.md carries")
	flag.Parse()

	// -table reports the MEASUREMENT, so it is deliberately not a verdict: the
	// record's own pre-trim table is written from a tree that is over the
	// ceiling, and a run that refused before printing it could not be the thing
	// the thresholds are derived from.
	if *table {
		report, err := prosegate.Measure(*root, prosegate.Enforced)
		if err != nil {
			fail(err)
		}
		report.WriteTable(os.Stdout)
		return
	}

	report, err := prosegate.CheckRepo(*root, prosegate.Enforced)
	if err != nil {
		fail(err)
	}
	if err := prosegate.CheckRecord(*root, prosegate.Enforced); err != nil {
		fail(err)
	}
	fmt.Printf("prose gate OK: no file is over the %d%% ceiling %s records\n\n", prosegate.Enforced.CeilingPercent, prosegate.RecordFile)
	report.WriteSummary(os.Stdout)
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "\nprose gate FAILED\n%v\n\nA comment earns its place by saying WHY. A file where prose outweighs code is not documented, it is narrated; the thresholds and how they were derived are in %s.\n",
		err, prosegate.RecordFile)
	os.Exit(1)
}
