// Command uiverify grades tracker's user interfaces against the umbrella's frontend conventions.
//
// It is the entry point behind four make targets, and they are a contract rather than a mechanism:
//
//	make verify-ui          uiverify web      the map's rendered claims, in a real browser engine
//	make verify-ui-refusal  uiverify refusal  both routes refuse loudly with the engine removed
//	make verify-ui-record   uiverify record   the F1-F11 record against what actually ran
//	make verify-ui-android  uiverify docs     + the instrumented suite on an emulator, then
//	                        uiverify android  the emulator's claim/demonstration count (AC18)
//
// A fifth target reaches a device the same way and is not about a rendered claim:
//
//	make verify-boot-restart  uiverify bootrestart  collection after a real reboot of the emulator
//
// The one thing it will never do is report a clause green without having rendered it. A missing
// browser engine exits non-zero naming the engine and how to get it, exactly as `make android` does
// for a missing SDK and internal/testsupport does for a missing Docker daemon.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/NSchatz/tracker/internal/uiverify"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "web":
		err = runWeb(ctx)
	case "refusal":
		err = runRefusal(ctx)
	case "record":
		err = uiverify.CheckRecord(os.Stdout, repoRoot())
	case "docs":
		err = uiverify.CheckDocuments(os.Stdout, repoRoot())
	case "android":
		err = uiverify.CheckAndroidRun(os.Stdout, repoRoot())
	case "bootrestart":
		err = uiverify.RunBootRestart(ctx, os.Stdout)
	default:
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: uiverify {web|refusal|record|docs|android|bootrestart}")
}

// repoRoot is where the record and the documents live. The make targets run from the repository
// root, so the default is the working directory; TRACKER_REPO_ROOT overrides it.
func repoRoot() string {
	if r := os.Getenv("TRACKER_REPO_ROOT"); r != "" {
		return r
	}
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func runWeb(ctx context.Context) error {
	results, err := uiverify.RunWeb(ctx, os.Stdout)
	if err != nil {
		return err
	}
	if err := uiverify.RecordRun(repoRoot(), "web", results); err != nil {
		return err
	}
	return uiverify.Summarise(os.Stdout, "browser map", results)
}

// runRefusal proves the loud-refusal path bites. It runs the browser route with the engine removed
// from the search and asserts the run REFUSES rather than skipping, passing, or reporting green.
func runRefusal(ctx context.Context) error {
	return uiverify.CheckRefusal(ctx, os.Stdout)
}
