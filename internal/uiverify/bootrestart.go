package uiverify

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// The reboot-restart route: does collection come back after the phone restarts, with nobody
// touching it?
//
// Everything here is read from the PLATFORM's own answers about a device that really rebooted:
// `dumpsys activity services` for whether the location foreground service is running, `dumpsys
// package` for whether the app is in the stopped state and whether the boot receiver is enabled,
// and `uiautomator dump` - the accessibility tree the app itself published - for what the screen
// says when it is reopened. None of it is a search over source text, which is what F2 asks for and
// what a reboot claim in particular has no source-level equivalent of.
//
// It is driven from the HOST rather than as an instrumented case, and that is a requirement rather
// than a preference: `internal/uiverify` counts EVERY passing instrumented case as a rendered claim
// and refuses one with no `<name>_demonstration` beside it, so a setup case that merely rebooted the
// device and passed would land in the results as an ungraded claim about the screen. An
// instrumented test also cannot survive the reboot it is testing.
//
// It NEVER skips. Every prerequisite it cannot find exits non-zero naming that prerequisite, and it
// reaches its device through scripts/android-emulator.sh so the four absences that script already
// refuses by name are refused here by the same code rather than by a second copy of the check.

// bootRestartCriterion is what the refusals say they could not grade. The emulator script reads it
// out of the environment, so its refusals name this route's criterion rather than the UI suite's.
const bootRestartCriterion = "the reboot restart (collection resumes after a boot with nobody touching the phone)"

const (
	trackerPackage  = "com.nschatz.tracker"
	bootReceiver    = trackerPackage + "/.collect.BootCompletedReceiver"
	mainActivity    = trackerPackage + "/.ui.MainActivity"
	debugAPKPath    = "android/app/build/outputs/apk/debug/app-debug.apk"
	prefsFileName   = "tracker_client.xml"
	bootRestartFile = "build/uiverify/boot-restart.log"
)

// bootRestartTimeout is how long one reboot has to reach a state this route can read.
//
// A reboot is two waits and they are different: the device coming back at all, and the boot
// broadcast being delivered and acted on afterwards. Both are covered by this one budget because a
// device that is slow at the first is slow at the second, and splitting it would only produce two
// numbers to tune. TRACKER_BOOT_RESTART_TIMEOUT overrides it.
func bootRestartTimeout() time.Duration {
	if v := os.Getenv("TRACKER_BOOT_RESTART_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return 6 * time.Minute
}

// RunBootRestart is the whole route: get a device, install the debug build, and reboot it once per
// case the criteria name.
func RunBootRestart(ctx context.Context, w io.Writer) error {
	root := repoRootFromEnv()
	log, closeLog, err := bootRestartWriter(root, w)
	if err != nil {
		return err
	}
	defer closeLog()

	apk := filepath.Join(root, filepath.FromSlash(debugAPKPath))
	dev, err := bootedDeviceFor(ctx, root, log)
	if err != nil {
		return err
	}
	if _, serr := os.Stat(apk); serr != nil {
		return &Refusal{
			Criterion:    bootRestartCriterion,
			Prerequisite: "the debug build at " + debugAPKPath,
			HowToObtain:  "`make verify-boot-restart` assembles it first; on its own, (cd android && ./gradlew assembleDebug)",
			Underlying:   serr,
		}
	}
	words, err := renderedWords(root)
	if err != nil {
		return err
	}

	if out, ierr := dev.adb("install", "-r", "-d", apk); ierr != nil {
		return fmt.Errorf("installing %s on %s: %v\n%s", debugAPKPath, dev.serial, ierr, out)
	}
	fmt.Fprintf(log, "installed %s on %s\n", debugAPKPath, dev.serial)
	// The log buffer is raised BEFORE any reboot and through a persisted property, because the boot
	// path runs while the device is booting and there is no moment afterwards at which a bigger
	// buffer would have kept its lines. The lines are corroboration rather than a gate - see
	// recordBootLog.
	_, _ = dev.shell("setprop persist.logd.size 16M")
	if gerr := dev.grantLocation(log); gerr != nil {
		return gerr
	}

	var problems []string
	for _, run := range bootRestartRuns(words) {
		fmt.Fprintf(log, "\n--- %s: %s\n", run.name, run.establishes)
		if err := run.prepare(dev, log); err != nil {
			return err
		}
		if err := dev.rebootAndWait(log); err != nil {
			return err
		}
		dev.recordBootLog(log)
		if serr := run.survived(dev, log); serr != nil {
			return serr
		}
		verdict := run.assert(dev, log)
		switch {
		case run.mustFail && verdict == nil:
			problems = append(problems, fmt.Sprintf(
				"%s: the restart assertion PASSED against a build whose boot path is disabled, so a green "+
					"run of it is not evidence - the assertion cannot go red", run.name))
		case run.mustFail:
			fmt.Fprintf(log, "%s: the restart assertion went red as it must:\n%s\n", run.name, indent(verdict.Error()))
		case verdict != nil:
			problems = append(problems, run.name+": "+verdict.Error())
			fmt.Fprintf(log, "%s: FAILED\n%s\n", run.name, indent(verdict.Error()))
		default:
			fmt.Fprintf(log, "%s: passed\n", run.name)
		}
		if run.capture != nil {
			if cerr := run.capture(dev, root, log); cerr != nil {
				problems = append(problems, fmt.Sprintf(
					"%s: the rendered evidence for this state was not taken (F12 of the umbrella's "+
						"frontend conventions asks for it in both themes at both widths): %v", run.name, cerr))
			}
		}
		if cerr := run.restore(dev, log); cerr != nil {
			return cerr
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("the reboot restart does not hold:\n  - %s", strings.Join(problems, "\n  - "))
	}
	fmt.Fprintf(log, "\nboot restart: %d device runs, each across a real reboot of %s\n",
		len(bootRestartRuns(words)), dev.serial)
	return nil
}

// bootRestartWriter tees this route's output into a file the CI job uploads, so a run that went red
// on the emulator leaves behind what it read rather than only what it concluded.
func bootRestartWriter(root string, w io.Writer) (io.Writer, func(), error) {
	path := filepath.Join(root, filepath.FromSlash(bootRestartFile))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, func() {}, err
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, func() {}, err
	}
	return io.MultiWriter(w, f), func() { _ = f.Close() }, nil
}

// --- the device ---------------------------------------------------------------------------------

// bootDevice is one booted emulator, reached through the repository's own emulator script.
type bootDevice struct {
	ctx    context.Context
	adbBin string
	serial string
}

// bootedDeviceFor asserts the prerequisites and boots the AVD, THROUGH scripts/android-emulator.sh.
//
// The script is the prerequisite gate this repository already has, and `make verify-ui-refusal`
// already drives each of its absences. Re-implementing the check here would mean two checks to keep
// honest and a second refusal nobody exercises, so the script's own refusal is passed through
// unchanged: it names this route's criterion (it reads CRITERION from the environment), the missing
// prerequisite, how to obtain it, and that no clause is reported green.
func bootedDeviceFor(ctx context.Context, root string, w io.Writer) (*bootDevice, error) {
	for _, sub := range []string{"require", "boot"} {
		out, err := runEmulatorScript(ctx, root, sub)
		fmt.Fprintf(w, "emulator script %q:\n%s", sub, indent(out))
		if err != nil {
			return nil, &passthroughRefusal{text: out}
		}
		if sub != "boot" {
			continue
		}
		serial := lastLine(out)
		if serial == "" || !strings.HasPrefix(serial, "emulator-") {
			return nil, &Refusal{
				Criterion:    bootRestartCriterion,
				Prerequisite: "a BOOTED emulator device (the emulator script exited 0 and named none)",
				HowToObtain:  "give the runner /dev/kvm, or raise TRACKER_EMULATOR_BOOT_TIMEOUT for a TCG boot",
			}
		}
		sdk := os.Getenv("ANDROID_SDK_ROOT")
		if sdk == "" {
			sdk = os.Getenv("ANDROID_HOME")
		}
		return &bootDevice{ctx: ctx, adbBin: filepath.Join(sdk, "platform-tools", "adb"), serial: serial}, nil
	}
	return nil, fmt.Errorf("unreachable: the emulator script neither booted nor refused")
}

// runEmulatorScript invokes one subcommand of the emulator script with this route's criterion.
func runEmulatorScript(ctx context.Context, root, sub string) (string, error) {
	cmd := exec.CommandContext(ctx, "bash", filepath.Join(root, "scripts", "android-emulator.sh"), sub)
	cmd.Env = append(os.Environ(), "CRITERION="+bootRestartCriterion)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// passthroughRefusal is a refusal another program already wrote, carried without a second voice.
type passthroughRefusal struct{ text string }

func (p *passthroughRefusal) Error() string { return p.text }

func (d *bootDevice) adb(args ...string) (string, error) {
	cmd := exec.CommandContext(d.ctx, d.adbBin, append([]string{"-s", d.serial}, args...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// adbBytes is adb with stdout kept BINARY and separate from stderr.
//
// `exec-out screencap -p` writes a PNG to stdout, and both halves of that matter: `adb shell` would
// translate the stream and corrupt it, and CombinedOutput would splice any adb warning into the middle
// of the image. A picture that decodes to nothing is worse than no picture, because it looks like
// evidence in a directory listing.
func (d *bootDevice) adbBytes(args ...string) ([]byte, error) {
	cmd := exec.CommandContext(d.ctx, d.adbBin, append([]string{"-s", d.serial}, args...)...)
	return cmd.Output()
}

func (d *bootDevice) shell(command string) (string, error) {
	return d.adb(append([]string{"shell"}, strings.Fields(command)...)...)
}

// grantLocation grants what the platform requires before a location foreground service may start
// from the background.
//
// Through adb rather than through a debug-only switch in the app, which is why the shipped app gains
// nothing from this route existing. Foreground location first and background second, because that is
// the order the platform's own model has and a background grant made before a foreground one is
// refused.
func (d *bootDevice) grantLocation(w io.Writer) error {
	for _, p := range []string{
		"android.permission.ACCESS_COARSE_LOCATION",
		"android.permission.ACCESS_FINE_LOCATION",
		"android.permission.ACCESS_BACKGROUND_LOCATION",
		"android.permission.POST_NOTIFICATIONS",
	} {
		if out, err := d.shell("pm grant " + trackerPackage + " " + p); err != nil {
			return fmt.Errorf("granting %s: %v\n%s", p, err, out)
		}
	}
	// Read back rather than assumed. A grant that did not take surfaces two reboots later as "the
	// service is not running", which is a true statement about the device and a useless one about
	// why, and it is the difference between a product defect and an unreached precondition.
	out, _ := d.shell("dumpsys package " + trackerPackage)
	granted := regexp.MustCompile(`ACCESS_BACKGROUND_LOCATION: granted=true`).MatchString(out)
	fmt.Fprintf(w, "grants: ACCESS_BACKGROUND_LOCATION granted=%v\n", granted)
	if !granted {
		return &Refusal{
			Criterion:    bootRestartCriterion,
			Prerequisite: "a granted ACCESS_BACKGROUND_LOCATION on " + d.serial,
			HowToObtain:  "adb -s " + d.serial + " shell pm grant " + trackerPackage + " android.permission.ACCESS_BACKGROUND_LOCATION",
		}
	}
	return nil
}

// writeStoredState puts the persisted ask and the server settings on the device, in the file the app
// reads them from.
//
// `run-as` is the whole mechanism, and it is available because the debug build is debuggable: the
// app's own data directory is reachable as the app's own uid, so this writes exactly the file the app
// would have written and nothing in the shipped app has to offer a way in. That is what keeps this
// route from needing a debug-only input the release build would then have to be shown ignoring.
//
// The app is force-stopped first because SharedPreferences caches its map per process, so a running
// app would write its stale copy back over this one. Every caller then LAUNCHES the app, which is
// not politeness: a force-stopped app - and a freshly installed one that has never been launched -
// is in the STOPPED state, and Android delivers no BOOT_COMPLETED to an app in that state at all.
func (d *bootDevice) writeStoredState(w io.Writer, collectionEnabled bool, configured bool) error {
	if out, err := d.shell("am force-stop " + trackerPackage); err != nil {
		return fmt.Errorf("force-stopping %s: %v\n%s", trackerPackage, err, out)
	}
	url, token := "", ""
	if configured {
		// A host that resolves nowhere, deliberately: this route grades whether collection is
		// RUNNING, never whether a fix was delivered, and pointing it at something reachable would
		// make the assertion depend on a server nobody started.
		url, token = "https://tracker.invalid", "a-device-token"
	}
	body := fmt.Sprintf(`<?xml version='1.0' encoding='utf-8' standalone='yes' ?>
<map>
    <string name="base_url">%s</string>
    <string name="device_token">%s</string>
    <boolean name="collection_enabled" value="%t" />
</map>
`, url, token, collectionEnabled)

	local, err := os.CreateTemp("", "tracker-prefs-*.xml")
	if err != nil {
		return err
	}
	defer os.Remove(local.Name())
	if _, err := local.WriteString(body); err != nil {
		return err
	}
	if err := local.Close(); err != nil {
		return err
	}

	staged := "/data/local/tmp/" + prefsFileName
	if out, err := d.adb("push", local.Name(), staged); err != nil {
		return fmt.Errorf("pushing the stored state: %v\n%s", err, out)
	}
	dir := "/data/data/" + trackerPackage + "/shared_prefs"
	if out, err := d.shell("run-as " + trackerPackage + " mkdir -p " + dir); err != nil {
		return fmt.Errorf("making %s: %v\n%s", dir, err, out)
	}
	if out, err := d.shell("run-as " + trackerPackage + " cp " + staged + " " + dir + "/" + prefsFileName); err != nil {
		return fmt.Errorf("writing the stored state: %v\n%s", err, out)
	}

	// Read it back off the device. A precondition that was not reached must refuse, not be assumed:
	// every assertion after this reboot is about a phone in a state this line is the only proof of.
	back, _ := d.shell("run-as " + trackerPackage + " cat " + dir + "/" + prefsFileName)
	want := fmt.Sprintf(`value="%t"`, collectionEnabled)
	if !strings.Contains(back, want) {
		return &Refusal{
			Criterion:    bootRestartCriterion,
			Prerequisite: fmt.Sprintf("a stored collection ask of %t on %s (read back: %q)", collectionEnabled, d.serial, strings.TrimSpace(back)),
			HowToObtain:  "the debug build must be debuggable so `run-as " + trackerPackage + "` can write its own data directory",
		}
	}
	fmt.Fprintf(w, "stored state: collection_enabled=%t configured=%t\n", collectionEnabled, configured)
	return nil
}

// openTheApp launches the screen and waits for it to settle.
//
// This is also what takes the app OUT of the stopped state, which is the one thing standing between
// a correct boot receiver and a boot that delivers nothing to it.
func (d *bootDevice) openTheApp(w io.Writer) error {
	if out, err := d.shell("am start -W -n " + mainActivity); err != nil {
		return fmt.Errorf("launching %s: %v\n%s", mainActivity, err, out)
	}
	time.Sleep(5 * time.Second)
	fmt.Fprintf(w, "opened %s\n", mainActivity)
	return nil
}

// leaveTheStoppedState opens the app and asserts the platform agrees it is no longer stopped.
//
// Three of the four runs depend on this and cannot say so for themselves: Android delivers no
// ACTION_BOOT_COMPLETED to an app in the STOPPED state, and a freshly installed app that has never
// been launched is in it, as is one that has been force-stopped. So a run that skipped this would
// measure a device the broadcast never reached and report it as a boot path that did not work.
//
// It is asserted rather than assumed because "a launch clears the stopped flag" is a platform
// behaviour, not a guarantee this code owns. If it ever stops being true, this refuses by name here
// instead of surfacing two minutes later as "the service is not running".
func (d *bootDevice) leaveTheStoppedState(w io.Writer) error {
	if err := d.openTheApp(w); err != nil {
		return err
	}
	state := d.packageState()
	fmt.Fprintf(w, "platform package state: %s\n", state.userLine)
	if state.stopped {
		return &Refusal{
			Criterion:    bootRestartCriterion,
			Prerequisite: "an app the boot broadcast can reach on " + d.serial + " (the platform still records it as stopped after a launch: " + state.userLine + ")",
			HowToObtain:  "adb -s " + d.serial + " shell am start -W -n " + mainActivity + ", or launch it from the launcher",
		}
	}
	return nil
}

// settleBeforeReboot waits for the platform to write its own package state to disk.
//
// This is not defensive padding; it is the difference between this route working and silently measuring
// the wrong device. `PackageManagerService` persists its settings - which is where the STOPPED flag
// lives - on a DELAYED write, ten seconds after the change, and `adb reboot` does not wait for it. So a
// launch five seconds before the reboot was lost on the way down and the guest came back reporting
// `stopped=true notLaunched=true`, as though the app had never been opened: no boot broadcast was
// delivered, the service did not start, and the route reported a boot path that does not work when what
// had happened was that its precondition never survived the reboot.
//
// The wait clears that delay and the sync flushes the page cache behind it. The post-boot precondition
// on each run is the assertion that this worked, so a platform that changes the delay refuses rather
// than quietly grading a fresh install.
func (d *bootDevice) settleBeforeReboot(w io.Writer) {
	time.Sleep(25 * time.Second)
	_, _ = d.shell("sync")
	fmt.Fprintln(w, "waited for the platform to persist its package state, and synced")
}

func (d *bootDevice) rebootAndWait(w io.Writer) error {
	d.settleBeforeReboot(w)
	fmt.Fprintf(w, "rebooting %s\n", d.serial)
	if out, err := d.adb("reboot"); err != nil {
		return fmt.Errorf("rebooting %s: %v\n%s", d.serial, err, out)
	}
	deadline := time.Now().Add(bootRestartTimeout())
	// wait-for-device returns as soon as adbd is up, which is well before the boot has finished and
	// long before a boot broadcast has been delivered. The property is the platform's own answer to
	// "has the boot completed", so it is the one that is waited on.
	if out, err := d.adb("wait-for-device"); err != nil {
		return fmt.Errorf("waiting for %s to come back: %v\n%s", d.serial, err, out)
	}
	for time.Now().Before(deadline) {
		if out, _ := d.shell("getprop sys.boot_completed"); strings.TrimSpace(out) == "1" {
			// The broadcast is dispatched after the property is set, and the service then takes a
			// moment to enter the foreground. Nothing observable says "every boot receiver has run",
			// so the settle is a wait rather than a poll - and every assertion afterwards polls for
			// what it is looking for, so this is a floor rather than the whole budget.
			time.Sleep(20 * time.Second)
			fmt.Fprintf(w, "booted: %s\n", d.serial)
			return nil
		}
		time.Sleep(5 * time.Second)
	}
	return &Refusal{
		Criterion:    bootRestartCriterion,
		Prerequisite: fmt.Sprintf("a device that finished rebooting (waited %s for sys.boot_completed on %s)", bootRestartTimeout(), d.serial),
		HowToObtain:  "give the runner /dev/kvm, or raise TRACKER_BOOT_RESTART_TIMEOUT",
	}
}

// recordBootLog copies the app's own boot-path log lines into this route's evidence.
//
// Corroboration, and deliberately NOT one of the assertions. The boot path logs from inside a device
// that is booting, into a ring buffer the rest of the boot is also writing to, so a line can be
// evicted on a slow run - and the platform's own state answers the same questions without that risk:
// whether the service is running, whether the app was in the stopped state, whether the receiver was
// enabled. Asserting on a line that can vanish would make a green run depend on log volume.
func (d *bootDevice) recordBootLog(w io.Writer) {
	out, _ := d.adb("logcat", "-b", "all", "-d", "-v", "raw", "-s", "TrackerBoot:V")
	text := strings.TrimSpace(out)
	if text == "" {
		text = "(no boot-path log line survived this boot's ring buffer)"
	}
	fmt.Fprintf(w, "the app's own boot log (corroboration, not an assertion):\n%s\n", indent(text))
}

// --- what the platform says --------------------------------------------------------------------

// locationServiceIsRunning polls the platform's own service list until the location foreground
// service is running, or the budget is out.
func (d *bootDevice) locationServiceIsRunning() (bool, string) {
	// Generous on purpose. Nothing observable says "every boot receiver has run", so this budget has to
	// cover a broadcast queue still working through a cold boot as well as the service's own start.
	deadline := time.Now().Add(150 * time.Second)
	last := ""
	for {
		out, _ := d.shell("dumpsys activity services " + trackerPackage)
		last = out
		if at := strings.Index(out, "LocationCollectionService"); at >= 0 {
			// isForeground is read from the record for THIS service rather than from anywhere in the
			// dump, because the app declares a second service and a foreground flag belonging to it
			// would answer a question nobody asked.
			window := out[at:]
			if len(window) > 2000 {
				window = window[:2000]
			}
			if strings.Contains(window, "isForeground=true") {
				return true, window
			}
		}
		if !time.Now().Before(deadline) {
			return false, last
		}
		time.Sleep(5 * time.Second)
	}
}

// packageState is what the platform records about the app itself, rather than about its services.
type packageState struct {
	stopped          bool
	notLaunched      bool
	receiverDisabled bool
	userLine         string
	dump             string
}

// stoppedField matches the platform's own record of the STOPPED state.
//
// It is matched mid-line and not at the start of one, which is the whole reason it is a named pattern.
// `dumpsys package` prints the per-user state as one long line - "installed=true hidden=false
// suspended=false distractionFlags=0 stopped=false notLaunched=false enabled=0 ..." - so an anchored
// pattern matches nothing whatever the state is, and a check built on one reads "not stopped" for every
// device. That is not a hypothetical: it is what the first CI run of this route did, which reported the
// app reachable when nothing had been checked.
var (
	stoppedField     = regexp.MustCompile(`(?:^|\s)stopped=true(?:\s|$)`)
	notLaunchedField = regexp.MustCompile(`(?:^|\s)notLaunched=true(?:\s|$)`)
	userStateLine    = regexp.MustCompile(`(?m)^\s*User \d+:.*$`)
)

func (d *bootDevice) packageState() packageState {
	out, _ := d.shell("dumpsys package " + trackerPackage)
	line := userStateLine.FindString(out)
	return packageState{
		stopped:     stoppedField.MatchString(line),
		notLaunched: notLaunchedField.MatchString(line),
		// The component has to be INSIDE the disabledComponents list, not merely mentioned somewhere in
		// a dump that names every component the package declares. `pm disable-user` prints it as an
		// indented line under a "disabledComponents:" heading, so the heading is found and the lines
		// after it are what is searched.
		receiverDisabled: componentIsDisabled(out, "BootCompletedReceiver"),
		userLine:         strings.TrimSpace(line),
		dump:             out,
	}
}

// componentIsDisabled reads the package's disabledComponents list rather than the whole dump.
func componentIsDisabled(dump, component string) bool {
	at := strings.Index(dump, "disabledComponents:")
	if at < 0 {
		return false
	}
	window := dump[at:]
	if len(window) > 4000 {
		window = window[:4000]
	}
	return strings.Contains(window, component)
}

// publishedScreenText is the accessibility tree the app itself published, read off the device.
//
// This is the app's own answer about what it is showing, which is what F2 admits for a claim about
// what a person sees. It is not a screenshot and it is not a source read: it is the tree an assistive
// technology would be handed.
func (d *bootDevice) publishedScreenText() (string, error) {
	remote := "/sdcard/tracker-window-dump.xml"
	if out, err := d.shell("uiautomator dump " + remote); err != nil {
		return "", fmt.Errorf("dumping the published tree: %v\n%s", err, out)
	}
	out, err := d.shell("cat " + remote)
	if err != nil {
		return "", fmt.Errorf("reading the published tree: %v\n%s", err, out)
	}
	return out, nil
}

// --- the rendered evidence F12 asks for ---------------------------------------------------------

// renderedEvidenceDir is where this route leaves the screenshots. CI uploads build/uiverify/ whole.
const renderedEvidenceDir = "build/uiverify/shots"

// renderProfiles are the two widths F12 names, expressed as something a phone actually has.
//
// F12 asks for both themes "at 360 and at desktop width". A native phone surface has no desktop, so
// the wide cell is the widest configuration this AVD can be put into rather than a claim about one:
// 600dp, which is where Android's own breakpoints put a small tablet.
//
// Both are TALL on purpose, and that is the part worth explaining. The home screen is a single
// scrolling column and the card this work changes is the third one on it, so a capture at a phone's
// real height would be a picture of the permission card. Setting the display's height rather than
// swiping down to the card keeps the capture deterministic - no coordinates, no gestures, nothing that
// lands in the wrong place on a slow frame - and puts the whole column in one frame.
//
// dp = px * 160 / dpi, which is the same arithmetic UiHarness.set360dpProfile uses: 720 x 160 / 320 is
// exactly 360dp of width, and 2400 x 160 / 320 is 1200dp of height.
var renderProfiles = []struct{ cell, size, density string }{
	{"360", "720x2400", "320"},
	{"desktop", "1200x2400", "320"},
}

var renderThemes = []struct{ cell, night string }{
	{"light", "no"},
	{"dark", "yes"},
}

// captureRenderedEvidence photographs the state this work adds, in both themes at both widths.
//
// It runs at the one moment the device is IN that state: after the force-stopped reboot, with the app
// reopened, showing a collection somebody asked for that is not running and the reason it is not. A
// screenshot of any other moment would be a picture of a screen this change did not alter.
//
// The device is put back to its own profile and to light mode afterwards, so the run that follows this
// one inherits a device rather than a configuration.
func (d *bootDevice) captureRenderedEvidence(root string, w io.Writer) error {
	dir := filepath.Join(root, filepath.FromSlash(renderedEvidenceDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	defer func() {
		_, _ = d.shell("wm size reset")
		_, _ = d.shell("wm density reset")
		_, _ = d.shell("cmd uimode night no")
		time.Sleep(2 * time.Second)
	}()

	var written []string
	for _, profile := range renderProfiles {
		if out, err := d.shell("wm size " + profile.size); err != nil {
			return fmt.Errorf("setting the display to %s: %v\n%s", profile.size, err, out)
		}
		if out, err := d.shell("wm density " + profile.density); err != nil {
			return fmt.Errorf("setting the density to %s: %v\n%s", profile.density, err, out)
		}
		for _, theme := range renderThemes {
			if out, err := d.shell("cmd uimode night " + theme.night); err != nil {
				return fmt.Errorf("setting night mode to %s: %v\n%s", theme.night, err, out)
			}
			time.Sleep(3 * time.Second)
			// Relaunched per cell rather than left to recompose, because a display resize and a theme
			// change are both configuration changes and what is wanted is the screen as it is drawn
			// from scratch in that configuration.
			if err := d.openTheApp(w); err != nil {
				return err
			}
			png, err := d.adbBytes("exec-out", "screencap", "-p")
			if err != nil {
				return fmt.Errorf("screenshotting %s/%s: %v", theme.cell, profile.cell, err)
			}
			// Checked rather than trusted, which is the lesson of finding F3: a mechanism that wrote
			// nothing while two documents said it wrote everything went unnoticed for the whole life of
			// the mechanism. A file that is not a PNG, or one too small to be a screen, is not evidence.
			if len(png) < 4 || png[0] != 0x89 || png[1] != 'P' || png[2] != 'N' || png[3] != 'G' {
				return fmt.Errorf("the screenshot for %s/%s is %d bytes and does not begin with a PNG "+
					"signature, so it is not a picture of anything", theme.cell, profile.cell, len(png))
			}
			if len(png) < 20_000 {
				return fmt.Errorf("the screenshot for %s/%s is %d bytes, which is too small to be a "+
					"rendered screen at %s", theme.cell, profile.cell, len(png), profile.size)
			}
			name := fmt.Sprintf("collection.%s.%s.png", theme.cell, profile.cell)
			if err := os.WriteFile(filepath.Join(dir, name), png, 0o644); err != nil {
				return err
			}
			written = append(written, fmt.Sprintf("%s (%d bytes)", name, len(png)))
		}
	}
	fmt.Fprintf(w, "rendered evidence in %s:\n%s\n", renderedEvidenceDir, indent(strings.Join(written, "\n")))
	if len(written) != len(renderProfiles)*len(renderThemes) {
		return fmt.Errorf("%d of the %d theme-and-width cells were captured", len(written), len(renderProfiles)*len(renderThemes))
	}
	return nil
}

// --- the words the app declares -----------------------------------------------------------------

// screenWords are the texts this route looks for in what the app published.
type screenWords struct{ running, stopped, enabledNotRunning string }

// renderedWords reads the three labels out of the app's own resource file.
//
// The GRADE is on the rendering - what the app published about itself after a real reboot - and this
// supplies only the expected VALUE, so F2's rule is untouched: a source read cannot decide what was
// shown, and nothing here asks it to. Reading them rather than spelling them out is what stops this
// route and the app's resources drifting into two different vocabularies, and a missing resource
// refuses rather than quietly matching nothing.
func renderedWords(root string) (screenWords, error) {
	path := filepath.Join(root, filepath.FromSlash("android/app/src/main/res/values/strings.xml"))
	body, err := os.ReadFile(path)
	if err != nil {
		return screenWords{}, &Refusal{
			Criterion:    bootRestartCriterion,
			Prerequisite: "the app's string resources at " + path,
			HowToObtain:  "run this route from the repository root, or set TRACKER_REPO_ROOT",
			Underlying:   err,
		}
	}
	words := screenWords{}
	for name, into := range map[string]*string{
		"collection_running":             &words.running,
		"collection_stopped":             &words.stopped,
		"collection_enabled_not_running": &words.enabledNotRunning,
	} {
		m := regexp.MustCompile(`<string name="` + name + `">([^<]*)</string>`).FindSubmatch(body)
		if m == nil {
			return screenWords{}, &Refusal{
				Criterion:    bootRestartCriterion,
				Prerequisite: fmt.Sprintf("the string resource %q, which this route looks for in what the app publishes", name),
				HowToObtain:  "restore it in android/app/src/main/res/values/strings.xml, or re-derive this route against its new name",
			}
		}
		*into = string(m[1])
	}
	return words, nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// --- the device runs ----------------------------------------------------------------------------

// bootRestartRun is one reboot: a state to reach, and what must be true on the other side.
type bootRestartRun struct {
	name        string
	establishes string
	prepare     func(*bootDevice, io.Writer) error
	// survived asserts, after the boot, that the state prepare reached is still the state the device is
	// in. It REFUSES rather than fails, and the distinction is the whole reason it exists: a precondition
	// the reboot lost is a route that cannot grade this criterion, not a phone that misbehaved, and
	// reporting the second when the first happened is how a green run stops meaning anything.
	survived func(*bootDevice, io.Writer) error
	assert   func(*bootDevice, io.Writer) error
	restore  func(*bootDevice, io.Writer) error
	// capture photographs the state this run reached, for the runs where there is rendered evidence to
	// take. Its verdict is reported separately from assert's, so a missing screenshot and a phone that
	// failed to restart are never read as the same failure.
	capture func(*bootDevice, string, io.Writer) error
	// mustFail inverts the verdict. It is how the route is shown able to go RED: one run disables the
	// boot path and re-runs the SAME assertion the enabled case uses, and a pass there would mean the
	// assertion cannot fail and every green run of it is worth nothing.
	mustFail bool
}

// theAppIsStillReachable refuses unless the platform still records the app as launchable after the boot.
//
// Three of the four runs need it, because Android delivers no ACTION_BOOT_COMPLETED to an app in the
// STOPPED state: without this, a reboot that lost the launch would produce a device that was never sent
// the broadcast and a route that called that a boot path which does not work.
func theAppIsStillReachable(d *bootDevice, w io.Writer) error {
	state := d.packageState()
	fmt.Fprintf(w, "platform package state after the boot: %s\n", state.userLine)
	if state.stopped || state.notLaunched {
		return &Refusal{
			Criterion:    bootRestartCriterion,
			Prerequisite: "an app the boot broadcast could reach, still recorded as launched after the reboot on " + d.serial + " (it reports: " + state.userLine + ")",
			HowToObtain:  "the platform writes its package state on a delayed write; raise the settle in settleBeforeReboot, or reboot through a path that flushes it",
		}
	}
	return nil
}

// theAppIsStillStopped is the force-stopped run's mirror of it: that run's whole point is an app in the
// STOPPED state, so a reboot that CLEARED the flag would leave it measuring an ordinary phone.
func theAppIsStillStopped(d *bootDevice, w io.Writer) error {
	state := d.packageState()
	fmt.Fprintf(w, "platform package state after the boot: %s\n", state.userLine)
	if !state.stopped {
		return &Refusal{
			Criterion:    bootRestartCriterion,
			Prerequisite: "the app still in the platform's STOPPED state after the reboot on " + d.serial + " (it reports: " + state.userLine + ")",
			HowToObtain:  "the platform writes its package state on a delayed write; raise the settle in settleBeforeReboot, or reboot through a path that flushes it",
		}
	}
	return nil
}

func bootRestartRuns(words screenWords) []bootRestartRun {
	noRestore := func(*bootDevice, io.Writer) error { return nil }
	return []bootRestartRun{
		{
			name:        "collection was enabled",
			establishes: "a phone that was collecting is collecting again after the reboot, untouched",
			prepare: func(d *bootDevice, w io.Writer) error {
				if err := d.writeStoredState(w, true, true); err != nil {
					return err
				}
				return d.leaveTheStoppedState(w)
			},
			survived: theAppIsStillReachable,
			assert:   collectionIsRunningAfterTheBoot,
			restore:  noRestore,
		},
		{
			name:        "collection was stopped by a person",
			establishes: "a phone whose owner stopped collecting stays stopped after the reboot",
			prepare: func(d *bootDevice, w io.Writer) error {
				if err := d.writeStoredState(w, false, true); err != nil {
					return err
				}
				return d.leaveTheStoppedState(w)
			},
			// The survival check is what makes this run mean something rather than being a tautology: an
			// app Android delivered no boot broadcast to would also not be collecting, and the two would
			// be indistinguishable. This one was reachable and is not collecting, so the boot path
			// DECIDED not to start it.
			survived: theAppIsStillReachable,
			assert:   collectionIsNotRunningAfterTheBoot,
			restore:  noRestore,
		},
		{
			name:        "the app was force-stopped",
			establishes: "a force-stopped app is not collecting after the boot, and says so when reopened",
			prepare: func(d *bootDevice, w io.Writer) error {
				if err := d.writeStoredState(w, true, true); err != nil {
					return err
				}
				// Opened and THEN force-stopped, in that order. Opening clears the stopped state, so
				// without it the force-stop would be putting the app into a state it was already in
				// and the run would prove nothing about a force-stop.
				if err := d.leaveTheStoppedState(w); err != nil {
					return err
				}
				if out, err := d.shell("am force-stop " + trackerPackage); err != nil {
					return fmt.Errorf("force-stopping %s: %v\n%s", trackerPackage, err, out)
				}
				forced := d.packageState()
				if !forced.stopped {
					return &Refusal{
						Criterion:    bootRestartCriterion,
						Prerequisite: "the app in the platform's STOPPED state on " + d.serial + " (it reports: " + forced.userLine + ")",
						HowToObtain:  "adb -s " + d.serial + " shell am force-stop " + trackerPackage,
					}
				}
				fmt.Fprintf(w, "the platform records the app as stopped: %s\n", forced.userLine)
				return nil
			},
			survived: theAppIsStillStopped,
			assert: func(d *bootDevice, w io.Writer) error {
				if err := collectionIsNotRunningAfterTheBoot(d, w); err != nil {
					return err
				}
				return theReopenedAppSaysItIsNotCollecting(d, w, words)
			},
			// The one run whose state is worth photographing: collection asked for, not running, and
			// the reason on the card. That is the screen this work adds, and F12 asks for it in both
			// themes at both widths.
			capture: func(d *bootDevice, root string, w io.Writer) error {
				return d.captureRenderedEvidence(root, w)
			},
			restore: noRestore,
		},
		{
			name:        "the boot path is disabled",
			establishes: "the restart assertion goes RED when the boot path cannot run, so a green run is evidence",
			prepare: func(d *bootDevice, w io.Writer) error {
				if err := d.writeStoredState(w, true, true); err != nil {
					return err
				}
				if err := d.leaveTheStoppedState(w); err != nil {
					return err
				}
				// The boot path is switched off at the platform, not in a second build. `pm
				// disable-user` is the platform's own record of a disabled component, so what this run
				// grades is a device whose boot path genuinely cannot run rather than one this route
				// merely says so about.
				if out, err := d.shell("pm disable-user --user 0 " + bootReceiver); err != nil {
					return fmt.Errorf("disabling the boot receiver: %v\n%s", err, out)
				}
				if !d.packageState().receiverDisabled {
					return &Refusal{
						Criterion:    bootRestartCriterion,
						Prerequisite: "a disabled boot receiver on " + d.serial + " (the platform still reports it enabled)",
						HowToObtain:  "adb -s " + d.serial + " shell pm disable-user --user 0 " + bootReceiver,
					}
				}
				fmt.Fprintln(w, "the platform records the boot receiver as disabled")
				return nil
			},
			survived: theAppIsStillReachable,
			assert:   collectionIsRunningAfterTheBoot,
			mustFail: true,
			restore: func(d *bootDevice, w io.Writer) error {
				if out, err := d.shell("pm enable " + bootReceiver); err != nil {
					return fmt.Errorf("re-enabling the boot receiver: %v\n%s", err, out)
				}
				fmt.Fprintln(w, "the boot receiver is enabled again")
				return nil
			},
		},
	}
}

// collectionIsRunningAfterTheBoot is the assertion the enabled case makes and the disabled-boot-path
// case is required to break. One function, used twice, so the two cannot drift apart.
func collectionIsRunningAfterTheBoot(d *bootDevice, w io.Writer) error {
	running, dump := d.locationServiceIsRunning()
	fmt.Fprintf(w, "platform service list says running=%v\n%s\n", running, indent(strings.TrimSpace(dump)))
	if !running {
		// The diagnosis travels with the failure. "The service is not running" is a true statement with
		// four different causes - the app was stopped so no broadcast arrived, the receiver is disabled,
		// the stored state is not what was written, or the receiver ran and declined - and a run that
		// reported only the outcome would need another reboot to tell them apart.
		return fmt.Errorf("the location foreground service is not running after the boot; the platform's "+
			"own service list for %s reports:\n%s\nand this is the state it was in:\n%s",
			trackerPackage, indent(strings.TrimSpace(dump)), indent(d.diagnose()))
	}
	return nil
}

// diagnose gathers what the device can say about why a boot path did not run.
func (d *bootDevice) diagnose() string {
	state := d.packageState()
	prefs, _ := d.shell("run-as " + trackerPackage + " cat /data/data/" + trackerPackage + "/shared_prefs/" + prefsFileName)
	// Every buffer, not just main: the broadcast queue's own account of what it delivered is in the
	// system buffer, and it is the half that says whether the receiver was ever reached.
	boot, _ := d.adb("logcat", "-b", "all", "-d", "-v", "brief", "-s", "TrackerBoot:V", "TrackerCollection:V")
	broadcasts, _ := d.adb("logcat", "-b", "all", "-d", "-v", "brief", "-s", "ActivityManager:I", "BroadcastQueue:I")
	// The crash buffer, because "the service was asked for and died" and "the service was never asked
	// for" produce the same empty service list. A start that threw - in onCreate, in startForeground, or
	// anywhere in onStartCommand - leaves its stack here and nowhere else this route reads.
	crash, _ := d.adb("logcat", "-b", "crash", "-d", "-v", "brief")
	return strings.Join([]string{
		"package state:     " + state.userLine,
		fmt.Sprintf("receiver disabled: %v", state.receiverDisabled),
		"stored state:\n" + indent(strings.TrimSpace(prefs)),
		"the app's own log (every buffer):\n" + indent(strings.TrimSpace(boot)),
		"what the platform says about this package's broadcasts:\n" + indent(linesMentioning(broadcasts, trackerPackage, "BOOT_COMPLETED")),
		"the crash buffer:\n" + indent(strings.TrimSpace(crash)),
	}, "\n")
}

// linesMentioning keeps the lines of a log that name any of the given fragments, bounded so a failure
// message stays readable.
func linesMentioning(log string, fragments ...string) string {
	var out []string
	for _, line := range strings.Split(log, "\n") {
		for _, f := range fragments {
			if strings.Contains(line, f) {
				out = append(out, strings.TrimSpace(line))
				break
			}
		}
		if len(out) >= 60 {
			break
		}
	}
	return strings.Join(out, "\n")
}

func collectionIsNotRunningAfterTheBoot(d *bootDevice, w io.Writer) error {
	running, dump := d.locationServiceIsRunning()
	fmt.Fprintf(w, "platform service list says running=%v\n", running)
	if running {
		return fmt.Errorf("the location foreground service IS running after the boot, and nothing asked "+
			"it to be; the platform's own service list reports:\n%s", indent(strings.TrimSpace(dump)))
	}
	return nil
}

// theReopenedAppSaysItIsNotCollecting reads the app's published accessibility tree after the reboot.
//
// The second half of the force-stopped case: Android delivered no boot signal, so nothing restarted,
// and the screen has to say that rather than presenting the stored ask as a running collection. The
// words are the app's own, read from its resources; the answer about what was SHOWN comes from the
// tree the app published.
func theReopenedAppSaysItIsNotCollecting(d *bootDevice, w io.Writer, words screenWords) error {
	if err := d.openTheApp(w); err != nil {
		return err
	}
	tree, err := d.publishedScreenText()
	if err != nil {
		return err
	}
	shows := func(text string) bool { return strings.Contains(tree, `text="`+text+`"`) }
	fmt.Fprintf(w, "the reopened app published: %q=%v %q=%v %q=%v\n",
		words.stopped, shows(words.stopped),
		words.running, shows(words.running),
		words.enabledNotRunning, shows(words.enabledNotRunning))

	if shows(words.running) {
		return fmt.Errorf("the reopened app publishes %q; a stored intent to collect must never be "+
			"presented as a running collection", words.running)
	}
	if !shows(words.stopped) {
		return fmt.Errorf("the reopened app publishes neither %q nor anything this route recognises as "+
			"the collection state:\n%s", words.stopped, indent(tree))
	}
	if !shows(words.enabledNotRunning) {
		return fmt.Errorf("the reopened app publishes %q but not %q, so a restart that did not happen "+
			"reads exactly like a deliberate stop", words.stopped, words.enabledNotRunning)
	}
	return nil
}
