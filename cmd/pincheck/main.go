// Command pincheck is the `pin-check` step of `make check`.
//
// It reads every pinnable reference in the working tree - Dockerfile bases, compose service images,
// image references named in Go source, workflow actions and the container images a workflow job
// runs, the Go module graph, the Makefile's tool versions, the Gradle wrapper distribution, the
// Gradle version catalog and any node manifest - and refuses one that does not name exactly what it
// will get, naming the file, the line, the reference and the clause of
// documentation/pinning-conventions.md it breaks.
//
// It exists as a command as well as a test so the pin verdict is reachable on its own, on a machine
// with no Docker daemon, no Android SDK, no credentials and no network: this gate READS FILES and
// asks nothing of any registry. The same code runs inside `make test` (internal/pingate's own
// tests), which is how it reaches CI without a second step in ci.yml to drift away from the gate a
// human runs.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/NSchatz/tracker/internal/pingate"
)

func main() {
	root := flag.String("root", ".", "repository root to check")
	flag.Parse()

	if err := pingate.CheckRepo(*root); err != nil {
		fmt.Fprintf(os.Stderr, "\npin gate FAILED\n%v\n\nThe rules are in %s. Every line above names the file, the line, the reference and the clause it breaks.\n",
			err, pingate.ConventionsFile)
		os.Exit(1)
	}

	report, err := pingate.Scan(*root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\npin gate FAILED\n%v\n", err)
		os.Exit(1)
	}

	fmt.Printf("pin gate OK: every reference below names both what a human reads and what actually resolves (%s)\n\n", pingate.ConventionsFile)
	report.WriteSummary(os.Stdout)
	fmt.Printf("\n%d reference(s) examined; all %d committed refusal demonstrations under %s still go red.\n",
		report.Examined(), len(pingate.Demonstrations), pingate.DemonstrationsDir)
}
