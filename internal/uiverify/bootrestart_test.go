// The reboot-restart route refuses every missing prerequisite by name, and never reports a pass or a
// skip.
//
// That is the criterion this file grades, and it needs no SDK, no emulator and no device: each
// absence is staged as a temporary SDK tree holding everything the previous one had and one thing
// more, which is the same instrument `internal/uiverify/refusal.go` already uses for the UI routes.
// Staging is shared with it rather than copied, so a change to how the emulator script looks for a
// prerequisite cannot leave this file testing a shape that no longer exists.
//
// Why it matters that this is graded at all: a route whose prerequisite went missing and which
// quietly became a weaker, greener route is the failure the whole verify family exists to refuse. A
// reboot route is the worst place for it, because the thing it grades - collection coming back on a
// phone nobody touched - has no cheaper substitute to silently fall back to, so a skip there would
// read as "reboot behaviour is fine" forever.
package uiverify

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBootRestartRefusesEveryMissingPrerequisite(t *testing.T) {
	for _, absence := range androidAbsences() {
		t.Run(absence.what, func(t *testing.T) {
			out, err := runBootRestartWith(t, absence)
			if err == nil {
				t.Fatalf("with %s the route returned NO error; a route that goes quiet when a prerequisite "+
					"is missing reports green while proving nothing about a reboot", absence.what)
			}
			text := out + "\n" + err.Error()
			if problems := checkRefusalText("boot restart ("+absence.what+")", text); len(problems) > 0 {
				t.Fatalf("the refusal for %s is not shaped as one:\n  - %s\nfull text:\n%s",
					absence.what, strings.Join(problems, "\n  - "), text)
			}
			if !strings.Contains(text, absence.names) {
				t.Fatalf("the refusal for %s does not name it (looked for %q):\n%s", absence.what, absence.names, text)
			}
			// The refusal names THIS route's criterion rather than the UI suite's. The emulator script
			// reads CRITERION out of the environment, so a route that forgot to set it would refuse
			// while naming a set of clauses it is not grading.
			if !strings.Contains(text, "the reboot restart") {
				t.Fatalf("the refusal for %s does not name the criterion this route grades:\n%s", absence.what, text)
			}
		})
	}
}

// TestTheBootRestartRefusalCheckStillBites is the mutation for the check above.
//
// Every assertion in this file runs through checkRefusalText, so a checkRefusalText that had stopped
// finding anything would leave each case above passing over an empty set. These are refusal texts
// with exactly one thing wrong, and each must be caught - a grader that cannot go red against a
// broken refusal is not evidence that the real one is sound.
func TestTheBootRestartRefusalCheckStillBites(t *testing.T) {
	sound := "\nREFUSED: cannot grade the reboot restart\n" +
		"  missing prerequisite: an Android SDK\n" +
		"  how to obtain it:     see android/README.md\n"
	if problems := checkRefusalText("sound", sound); len(problems) > 0 {
		t.Fatalf("a well-formed refusal was rejected, so this mutation table measures the wrong thing: %v", problems)
	}
	for _, broken := range []struct{ name, text string }{
		{"no criterion", "REFUSED\n  missing prerequisite: an Android SDK\n  how to obtain it: see the README\n"},
		{"no prerequisite named", "REFUSED: cannot grade the reboot restart\n  how to obtain it: see the README\n"},
		{"no remedy", "REFUSED: cannot grade the reboot restart\n  missing prerequisite: an Android SDK\n"},
		{"claims a skip", sound + "  the reboot clauses were skipped for want of a device\n"},
		{"claims green", sound + "  every reboot clause is green\n"},
	} {
		t.Run(broken.name, func(t *testing.T) {
			if problems := checkRefusalText(broken.name, broken.text); len(problems) == 0 {
				t.Fatalf("a refusal with %s was accepted, so the check the cases above lean on cannot go red", broken.name)
			}
		})
	}
}

// TestBootRestartNeedsItsOwnPrerequisitesBeyondTheDevice covers the two prerequisites the emulator
// script knows nothing about: the debug build, and the app's own labels this route looks for in what
// the device published. Both refuse by name rather than failing later as something else.
func TestBootRestartNeedsItsOwnPrerequisitesBeyondTheDevice(t *testing.T) {
	t.Run("the app's labels", func(t *testing.T) {
		root := t.TempDir()
		_, err := renderedWords(root)
		if err == nil {
			t.Fatal("a tree with no string resources at all produced labels, so this route would look for nothing")
		}
		var refusal *Refusal
		if !errors.As(err, &refusal) {
			t.Fatalf("the missing resources failed with a bare error rather than a refusal: %v", err)
		}
		if problems := checkRefusalText("labels", refusal.Error()); len(problems) > 0 {
			t.Fatalf("the refusal is not shaped as one: %v", problems)
		}
	})

	t.Run("a resource this route looks for is gone", func(t *testing.T) {
		// Everything the route needs except one label. A route that quietly matched nothing for a
		// missing resource would report the reboot behaviour green having looked for an empty string.
		root := t.TempDir()
		dir := filepath.Join(root, "android", "app", "src", "main", "res", "values")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := `<resources>
  <string name="collection_running">Running</string>
  <string name="collection_stopped">Stopped</string>
</resources>`
		if err := os.WriteFile(filepath.Join(dir, "strings.xml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := renderedWords(root)
		if err == nil {
			t.Fatal("a resource file missing the enabled-not-running label was accepted")
		}
		if !strings.Contains(err.Error(), "collection_enabled_not_running") {
			t.Fatalf("the refusal does not name the resource that is gone: %v", err)
		}
	})

	t.Run("the labels the committed resources declare", func(t *testing.T) {
		// ... and against the real tree it finds all three, so the cases above are not the only shape
		// this function is ever measured in.
		words, err := renderedWords(theRepoRoot)
		if err != nil {
			t.Fatalf("the committed resources do not carry the labels this route looks for: %v", err)
		}
		for name, value := range map[string]string{
			"collection_running":             words.running,
			"collection_stopped":             words.stopped,
			"collection_enabled_not_running": words.enabledNotRunning,
		} {
			if strings.TrimSpace(value) == "" {
				t.Fatalf("%s reads as empty, and a search for an empty string matches every tree", name)
			}
		}
		if words.running == words.stopped {
			t.Fatal("the running and stopped labels are the same words, so no reading of the published tree could tell them apart")
		}
	})
}

// TestTheBootRestartRunsCoverWhatTheCriteriaName reads the run table.
//
// Four device runs, and one of them inverted: the assertion the enabled case makes has to be shown
// going RED against a build whose boot path cannot run, or every green run of it is worth nothing.
// That is a property of the table rather than of a device, so it is decided here rather than needing
// an emulator to discover that a route has quietly lost a case.
func TestTheBootRestartRunsCoverWhatTheCriteriaName(t *testing.T) {
	words := screenWords{running: "Running", stopped: "Stopped", enabledNotRunning: "Enabled, not running"}
	runs := bootRestartRuns(words)
	if len(runs) != 4 {
		t.Fatalf("the route makes %d device runs; the criteria name four: the enabled case, the "+
			"stopped-by-a-person case, the force-stopped case and the disabled-boot-path case", len(runs))
	}
	inverted := 0
	for _, r := range runs {
		if r.mustFail {
			inverted++
		}
		if r.name == "" || r.establishes == "" {
			t.Fatalf("a run with no name or no statement of what it establishes: %+v", r.name)
		}
		if r.prepare == nil || r.assert == nil || r.restore == nil {
			t.Fatalf("run %q does not reach a precondition, assert, and put the device back", r.name)
		}
	}
	if inverted != 1 {
		t.Fatalf("%d runs are inverted; exactly one is - the disabled-boot-path run, which shows the "+
			"restart assertion able to go red", inverted)
	}
}

// runBootRestartWith drives the route with exactly one prerequisite missing.
//
// The environment is set for the process rather than for a child, because the route runs in-process:
// it is RunBootRestart that has to refuse, and shelling out to the built binary would grade the
// Makefile instead. The cleanups restore every variable, so the cases do not leak into one another.
func runBootRestartWith(t *testing.T, absence androidAbsence) (string, error) {
	t.Helper()
	ctx := context.Background()

	avd := makeVar(ctx, theRepoRoot, "print-avd")
	image := makeVar(ctx, theRepoRoot, "print-sys-image")
	if avd == "" || image == "" {
		t.Fatalf("could not read the AVD name and system image from the Makefile, so a staged tree "+
			"could not be built to match what the script looks for (avd=%q image=%q)", avd, image)
	}

	sdk := ""
	if absence.stage != nil {
		sdk = t.TempDir()
		if err := absence.stage(sdk, sdkPins{avd: avd, image: image}); err != nil {
			t.Fatalf("staging %s: %v", absence.what, err)
		}
	}
	for k, v := range map[string]string{
		"TRACKER_REPO_ROOT":  theRepoRoot,
		"ANDROID_SDK_ROOT":   sdk,
		"ANDROID_HOME":       "",
		"ANDROID_AVD_HOME":   filepath.Join(sdk, "avd"),
		"ANDROID_PREFS_ROOT": "",
		"ANDROID_SDK_HOME":   "",
		// HOME is staged too, and that is not housekeeping. The emulator script looks for an AVD in
		// $HOME/.android/avd as its LAST resort, on purpose - avdmanager's own answer about where it
		// writes one has moved across cmdline-tools releases - so on a machine that really does have
		// the AVD in the developer's home directory, the no-AVD absence is not reachable without
		// this and the case would pass by finding the real thing. That is the whole shape this file
		// exists to refuse: an absence staged but never actually absent.
		"HOME":                          filepath.Join(sdk, "staged-home"),
		"TRACKER_AVD":                   avd,
		"TRACKER_SYS_IMAGE":             image,
		"TRACKER_EMULATOR_BOOT_TIMEOUT": "0",
		"TRACKER_BOOT_RESTART_TIMEOUT":  "1s",
	} {
		t.Setenv(k, v)
	}

	var out bytes.Buffer
	err := RunBootRestart(ctx, &out)
	// The caller passes both halves through checkRefusalText, which is what decides whether anything
	// here reads as a pass. A cruder substring test would fire on the script's own disclaimer ("no
	// Android clause is reported as passed, skipped or green by this run"), which is the opposite of
	// a claim - handling that negation is exactly what checkRefusalText does line by line.
	return out.String(), err
}
