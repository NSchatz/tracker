package uiverify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// CheckRefusal is AC19, and it is the criterion this whole route exists to keep honest.
//
// The failure mode F2 names is not "the check was wrong"; it is "the check quietly became a
// different, weaker check when its prerequisite went missing, and reported green". So the refusal
// path is graded like any other claim: remove the prerequisite, invoke the route, and assert it
// exits non-zero, names the criterion it was verifying, names what is missing and how to get it, and
// says nothing that could be read as a pass.
func CheckRefusal(ctx context.Context, w io.Writer) error {
	var problems []string

	// --- the browser route, once per absence AC19/AC29 names on that surface ---------------------
	//
	// The ENGINE and its DRIVER are two absences and they refuse differently: an engine that is not
	// there at all is caught before anything starts, while an engine that is there and will not
	// START under a driver - a snap-confined chromium is the real case - fails only once the driver
	// has waited for a DevTools endpoint that never arrives. Driving only the first left the second
	// refusal written but never executed, which is the silent downgrade this criterion forbids.
	for _, absence := range browserAbsences() {
		saved, hadEnv := os.LookupEnv("TRACKER_BROWSER")
		savedPath := os.Getenv("PATH")
		path, cleanup, perr := absence.stage()
		if perr != nil {
			return perr
		}
		if err := os.Setenv("TRACKER_BROWSER", path); err != nil {
			return err
		}
		if err := os.Setenv("PATH", filepath.Join(os.TempDir(), "uiverify-empty-path")); err != nil {
			return err
		}
		var out bytes.Buffer
		_, err := RunWeb(ctx, &out)
		if hadEnv {
			_ = os.Setenv("TRACKER_BROWSER", saved)
		} else {
			_ = os.Unsetenv("TRACKER_BROWSER")
		}
		_ = os.Setenv("PATH", savedPath)
		cleanup()

		if err == nil {
			problems = append(problems, fmt.Sprintf(
				"with %s the browser route returned NO error; it would have reported a clause green without rendering it", absence.what))
			continue
		}
		var refusal *Refusal
		if !errors.As(err, &refusal) {
			problems = append(problems, fmt.Sprintf(
				"with %s the browser route failed with a bare error rather than a refusal naming the prerequisite: %s", absence.what, err.Error()))
		} else {
			problems = append(problems, checkRefusalText("browser route ("+absence.what+")", refusal.Error())...)
			if !strings.Contains(refusal.Error(), absence.names) {
				problems = append(problems, fmt.Sprintf(
					"the refusal for %s does not name it (looked for %q): %s", absence.what, absence.names, strings.TrimSpace(refusal.Error())))
			}
		}
		fmt.Fprintf(w, "browser route with %s:\n%s\n", absence.what, indent(err.Error()))
		if strings.Contains(strings.ToLower(out.String()), "pass") {
			problems = append(problems, "the browser route printed a PASS line before refusing: "+strings.TrimSpace(out.String()))
		}
	}

	// --- the Android route, once per prerequisite AC19 names -----------------------------------
	//
	// AC19 lists five absences: the browser engine, its driver, the Android emulator, the Android
	// SDK and a booted device. Driving only the first and the fourth would leave three refusals that
	// have never been executed - and an untested refusal is exactly the silent downgrade this
	// criterion exists to forbid, since the four Android ones share one `refuse()` whose call sites
	// do not. Each absence below is staged in a temporary SDK tree that has everything the previous
	// one had and one thing more, so the refusal that fires is the one being tested.
	for _, absence := range androidAbsences() {
		out, err := runAndroidScript(ctx, absence)
		fmt.Fprintf(w, "android route with %s:\n%s\n", absence.what, indent(out))
		if err == nil {
			problems = append(problems, fmt.Sprintf(
				"with %s scripts/android-emulator.sh exited ZERO; a route that goes quiet when a prerequisite is missing reports green while proving nothing", absence.what))
			continue
		}
		problems = append(problems, checkRefusalText("android route ("+absence.what+")", out)...)
		if !strings.Contains(out, absence.names) {
			problems = append(problems, fmt.Sprintf(
				"the refusal for %s does not name it (looked for %q): %s", absence.what, absence.names, strings.TrimSpace(out)))
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("the refusal paths do not bite:\n  - %s", strings.Join(problems, "\n  - "))
	}
	fmt.Fprintf(w, "refusal: both grading routes exit non-zero and name their missing prerequisite, once per each of the %d absences AC29 lists\n",
		len(browserAbsences())+len(androidAbsences()))
	return nil
}

// browserAbsence is one browser-side prerequisite removed, and what the refusal for it must say.
type browserAbsence struct {
	what  string
	names string
	// stage produces the value TRACKER_BROWSER is pointed at, and a cleanup for whatever it made.
	stage func() (string, func(), error)
}

// browserAbsences drives BOTH browser-side prerequisites AC29 names, separately.
func browserAbsences() []browserAbsence {
	return []browserAbsence{
		{
			what:  "no browser engine",
			names: "the browser engine named by TRACKER_BROWSER",
			stage: func() (string, func(), error) {
				return filepath.Join(os.TempDir(), "uiverify-no-such-browser"), func() {}, nil
			},
		},
		{
			// An engine that EXISTS and does not come up under a driver. A snap-confined chromium is
			// the real instance of this: it starts, cannot read the driver's profile directory, and
			// never publishes a DevTools endpoint - which reads exactly like a broken harness unless
			// the route refuses by name. Standing in for it here is an executable that exits at once,
			// which reaches the same refusal by the same path.
			what:  "an engine that will not start under a driver",
			names: "a browser engine that will actually START under a driver",
			stage: func() (string, func(), error) {
				dir, err := os.MkdirTemp("", "uiverify-dead-browser-")
				if err != nil {
					return "", func() {}, err
				}
				path := filepath.Join(dir, "chromium")
				if werr := os.WriteFile(path, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); werr != nil {
					return "", func() { _ = os.RemoveAll(dir) }, werr
				}
				return path, func() { _ = os.RemoveAll(dir) }, nil
			},
		},
	}
}

// checkRefusalText holds a refusal to the four things AC19 requires it to say, and to the one thing
// it must never say.
func checkRefusalText(what, text string) []string {
	var problems []string
	lower := strings.ToLower(text)
	for _, want := range []struct{ label, needle string }{
		{"the criterion it was verifying", "cannot grade"},
		{"the missing prerequisite", "missing prerequisite"},
		{"how to obtain it", "how to obtain"},
	} {
		if !strings.Contains(lower, want.needle) {
			problems = append(problems, what+" refusal does not name "+want.label+": "+strings.TrimSpace(text))
		}
	}
	// A refusal must not claim any clause came out green. It IS allowed - required, really - to say
	// that it is NOT reporting one that way, so a line carrying a negation is not evidence of a
	// claim. Lines rather than character windows, because that is the unit the disclaimer is
	// written in and the unit a reader would judge.
	negations := []string{"no clause", "not offered", "never", "rather than", "no android clause", "is not"}
	for _, line := range strings.Split(lower, "\n") {
		negated := false
		for _, n := range negations {
			if strings.Contains(line, n) {
				negated = true
				break
			}
		}
		if negated {
			continue
		}
		for _, forbidden := range []string{"skipping", "skipped", " green", "satisfied", "clause passed"} {
			if strings.Contains(line, forbidden) {
				problems = append(problems, what+" refusal claims a clause was "+strings.TrimSpace(forbidden)+": "+strings.TrimSpace(line))
			}
		}
	}
	return problems
}

// androidAbsence is one prerequisite removed, and the subcommand that should refuse for it.
type androidAbsence struct {
	what       string // how the absence reads in the route's own output
	subcommand string
	names      string // a fragment the refusal must contain, so the right one is being read
	// stage builds an SDK tree missing exactly this prerequisite. A nil stage means no SDK at all.
	stage func(dir string, p sdkPins) error
}

// sdkPins are the AVD name and system image the Makefile pins, read from it rather than copied.
type sdkPins struct{ avd, image string }

func androidAbsences() []androidAbsence {
	return []androidAbsence{
		{
			what:       "no SDK",
			subcommand: "require",
			names:      "an Android SDK",
		},
		{
			what:       "no emulator binary",
			subcommand: "require",
			names:      "the Android emulator binary",
			stage: func(dir string, _ sdkPins) error {
				return os.MkdirAll(filepath.Join(dir, "licenses"), 0o755)
			},
		},
		{
			what:       "no system image",
			subcommand: "require",
			names:      "the system image",
			stage: func(dir string, p sdkPins) error {
				return stageSDK(dir, p, false, false)
			},
		},
		{
			what:       "no AVD",
			subcommand: "require",
			names:      "an AVD named",
			stage: func(dir string, p sdkPins) error {
				return stageSDK(dir, p, true, false)
			},
		},
		{
			// The one absence `require` cannot pre-check: everything is installed and no device ever
			// reaches sys.boot_completed. Driven with the boot timeout at zero against stub tools, so
			// it is the refusal that is under test rather than a real emulator's start-up.
			what:       "no booted device",
			subcommand: "boot",
			names:      "a BOOTED emulator device",
			stage: func(dir string, p sdkPins) error {
				return stageSDK(dir, p, true, true)
			},
		},
	}
}

// stageSDK builds a temporary Android SDK tree holding stub tools, optionally the system image the
// Makefile pins, and optionally the AVD. The stubs are what let the boot refusal be exercised
// without an emulator: `adb` reports no device, so the boot loop never completes.
func stageSDK(dir string, p sdkPins, withImage, withAVD bool) error {
	stub := "#!/usr/bin/env bash\nexit 0\n"
	for _, tool := range []string{"emulator/emulator", "platform-tools/adb"} {
		path := filepath.Join(dir, filepath.FromSlash(tool))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(stub), 0o755); err != nil {
			return err
		}
	}
	if withImage {
		// The script derives this path from TRACKER_SYS_IMAGE by turning its semicolons into
		// separators, so the staged tree is derived the same way rather than spelled out.
		img := filepath.Join(dir, filepath.FromSlash(strings.ReplaceAll(p.image, ";", "/")))
		if err := os.MkdirAll(img, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(img, "system.img"), []byte("stub"), 0o644); err != nil {
			return err
		}
	}
	if withAVD {
		avd := filepath.Join(dir, "avd")
		if err := os.MkdirAll(avd, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(avd, p.avd+".ini"), []byte("path="+avd+"\n"), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// runAndroidScript invokes the emulator script with exactly one prerequisite missing.
func runAndroidScript(ctx context.Context, absence androidAbsence) (string, error) {
	root := repoRootFromEnv()
	script := filepath.Join(root, "scripts", "android-emulator.sh")

	// The AVD name and the system image are Makefile pins; this reads them from there rather than
	// keeping a second copy, so a staged tree cannot disagree with the route about what it looks for.
	avd := makeVar(ctx, root, "print-avd")
	image := makeVar(ctx, root, "print-sys-image")

	sdk := ""
	if absence.stage != nil {
		dir, err := os.MkdirTemp("", "uiverify-sdk-")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(dir)
		if err := absence.stage(dir, sdkPins{avd: avd, image: image}); err != nil {
			return "", err
		}
		sdk = dir
	}

	cmd := exec.CommandContext(ctx, "bash", script, absence.subcommand)
	cmd.Env = append(os.Environ(),
		"ANDROID_SDK_ROOT="+sdk,
		"ANDROID_HOME=",
		"ANDROID_AVD_HOME="+filepath.Join(sdk, "avd"),
		"ANDROID_PREFS_ROOT=",
		"ANDROID_SDK_HOME=",
		"TRACKER_AVD="+avd,
		"TRACKER_SYS_IMAGE="+image,
		"TRACKER_EMULATOR_BOOT_TIMEOUT=0",
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// makeVar reads one of the Makefile's UI pins, so this check and the route cannot disagree about the
// AVD name or the system image.
func makeVar(ctx context.Context, root, target string) string {
	cmd := exec.CommandContext(ctx, "make", "-s", "-C", root, target)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func repoRootFromEnv() string {
	if r := os.Getenv("TRACKER_REPO_ROOT"); r != "" {
		return r
	}
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func indent(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		b.WriteString("    " + line + "\n")
	}
	return b.String()
}
