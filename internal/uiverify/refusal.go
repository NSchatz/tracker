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

	// --- the browser route, with no engine to be found -----------------------------------------
	saved, hadEnv := os.LookupEnv("TRACKER_BROWSER")
	savedPath := os.Getenv("PATH")
	if err := os.Setenv("TRACKER_BROWSER", filepath.Join(os.TempDir(), "uiverify-no-such-browser")); err != nil {
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

	if err == nil {
		problems = append(problems, "with no browser engine present the browser route returned NO error; it would have reported a clause green without rendering it")
	} else {
		var refusal *Refusal
		if !errors.As(err, &refusal) {
			problems = append(problems, "with no browser engine present the browser route failed with a bare error rather than a refusal naming the prerequisite: "+err.Error())
		} else {
			problems = append(problems, checkRefusalText("browser route", refusal.Error())...)
		}
		fmt.Fprintf(w, "browser route with no engine:\n%s\n", indent(err.Error()))
	}
	if strings.Contains(strings.ToLower(out.String()), "pass") {
		problems = append(problems, "the browser route printed a PASS line before refusing: "+strings.TrimSpace(out.String()))
	}

	// --- the Android route, with no SDK to be found --------------------------------------------
	androidOut, androidErr := runAndroidRequireWithoutSDK(ctx)
	fmt.Fprintf(w, "android route with no SDK:\n%s\n", indent(androidOut))
	if androidErr == nil {
		problems = append(problems, "with no Android SDK present scripts/android-emulator.sh exited ZERO; a route that goes quiet when its emulator is missing reports green while proving nothing")
	} else {
		problems = append(problems, checkRefusalText("android route", androidOut)...)
	}

	if len(problems) > 0 {
		return fmt.Errorf("the refusal paths do not bite:\n  - %s", strings.Join(problems, "\n  - "))
	}
	fmt.Fprintln(w, "refusal: both grading routes exit non-zero and name their missing prerequisite")
	return nil
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

func runAndroidRequireWithoutSDK(ctx context.Context) (string, error) {
	script := filepath.Join(repoRootFromEnv(), "scripts", "android-emulator.sh")
	cmd := exec.CommandContext(ctx, "bash", script, "require")
	cmd.Env = append(os.Environ(), "ANDROID_SDK_ROOT=", "ANDROID_HOME=")
	out, err := cmd.CombinedOutput()
	return string(out), err
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
