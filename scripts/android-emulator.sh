#!/usr/bin/env bash
# Boot (or reuse) a headless Android emulator for the instrumented UI grading route.
#
# This script NEVER skips. Every prerequisite it cannot find is an exit-non-zero naming
# the criterion being verified, the missing piece and how to obtain it - the same stance
# internal/testsupport takes toward a missing Docker daemon, and the stance F2 requires:
# an Android rendered claim is graded by an emulator or it is not graded at all.
#
# Subcommands:
#   require   assert the SDK, the emulator binary, a system image and an AVD are present
#   boot      start the emulator headless and block until `sys.boot_completed` is 1
#   stop      kill the emulator this script started
set -uo pipefail

CRITERION="${CRITERION:-AC12-AC17 (Android rendered claims, F1/F2)}"
AVD_NAME="${TRACKER_AVD:-tracker-ui}"
SYS_IMAGE="${TRACKER_SYS_IMAGE:-system-images;android-34;google_apis;x86_64}"
BOOT_TIMEOUT="${TRACKER_EMULATOR_BOOT_TIMEOUT:-900}"

refuse() {
	echo "" >&2
	echo "REFUSED: cannot grade $CRITERION" >&2
	echo "  missing prerequisite: $1" >&2
	echo "  how to obtain it:     $2" >&2
	echo "" >&2
	echo "  No Android clause is reported as passed, skipped or green by this run." >&2
	echo "  F2 admits exactly one Android grader: an emulator, not the JVM. A JVM," >&2
	echo "  Robolectric, screenshot or source-text substitute is not offered here." >&2
	exit 1
}

sdk_root() {
	local sdk="${ANDROID_SDK_ROOT:-${ANDROID_HOME:-}}"
	if [ -z "$sdk" ] || [ ! -d "$sdk" ]; then
		refuse "an Android SDK (ANDROID_SDK_ROOT / ANDROID_HOME is unset or not a directory)" \
			"see android/README.md for the one-time rootless install; in CI, android-actions/setup-android@v3"
	fi
	echo "$sdk"
}

require() {
	local sdk emu adb avdhome
	sdk="$(sdk_root)" || exit 1
	emu="$sdk/emulator/emulator"
	adb="$sdk/platform-tools/adb"

	[ -x "$emu" ] || refuse "the Android emulator binary at $emu" \
		"sdkmanager --install 'emulator'"
	[ -x "$adb" ] || refuse "adb at $adb" \
		"sdkmanager --install 'platform-tools'"

	local img_path="$sdk/system-images/${SYS_IMAGE#system-images;}"
	img_path="${img_path//;//}"
	[ -f "$img_path/system.img" ] || refuse "the system image $SYS_IMAGE (looked for $img_path/system.img)" \
		"sdkmanager --install '$SYS_IMAGE'"

	avdhome="${ANDROID_AVD_HOME:-$HOME/.android/avd}"
	[ -f "$avdhome/$AVD_NAME.ini" ] || refuse "an AVD named '$AVD_NAME' under $avdhome" \
		"avdmanager create avd -n $AVD_NAME -k '$SYS_IMAGE' -d pixel_5"

	echo "ok: SDK=$sdk emulator=$emu image=$SYS_IMAGE avd=$AVD_NAME"
}

boot() {
	local sdk emu adb serial
	require >/dev/null || exit 1
	sdk="$(sdk_root)"
	emu="$sdk/emulator/emulator"
	adb="$sdk/platform-tools/adb"

	"$adb" start-server >/dev/null 2>&1
	if "$adb" devices | grep -q "^emulator-.*device$"; then
		serial="$("$adb" devices | awk '/^emulator-.*device$/{print $1; exit}')"
		echo "reusing booted emulator $serial"
		echo "$serial"
		return 0
	fi

	# No /dev/kvm means QEMU's TCG interpreter, which boots but slowly. `-accel off` makes
	# that explicit rather than letting the emulator abort on a missing accelerator.
	local accel=()
	[ -w /dev/kvm ] || accel=(-accel off)

	mkdir -p "${TMPDIR:-/tmp}/tracker-emulator"
	local log="${TMPDIR:-/tmp}/tracker-emulator/emulator.log"
	nohup "$emu" -avd "$AVD_NAME" -no-window -no-audio -no-boot-anim -no-snapshot \
		-gpu swiftshader_indirect -camera-back none -camera-front none \
		"${accel[@]}" >"$log" 2>&1 &
	echo "emulator starting (log: $log)" >&2

	local waited=0
	while [ "$waited" -lt "$BOOT_TIMEOUT" ]; do
		if "$adb" devices | grep -q "^emulator-.*device$"; then
			serial="$("$adb" devices | awk '/^emulator-.*device$/{print $1; exit}')"
			if [ "$("$adb" -s "$serial" shell getprop sys.boot_completed 2>/dev/null | tr -d '\r')" = "1" ]; then
				"$adb" -s "$serial" shell settings put global window_animation_scale 0 >/dev/null 2>&1
				"$adb" -s "$serial" shell settings put global transition_animation_scale 0 >/dev/null 2>&1
				"$adb" -s "$serial" shell settings put global animator_duration_scale 0 >/dev/null 2>&1
				echo "booted: $serial" >&2
				echo "$serial"
				return 0
			fi
		fi
		sleep 10
		waited=$((waited + 10))
	done

	echo "--- last 40 lines of $log ---" >&2
	tail -40 "$log" >&2 || true
	refuse "a BOOTED emulator device (waited ${BOOT_TIMEOUT}s for sys.boot_completed on AVD '$AVD_NAME')" \
		"give the runner /dev/kvm (hardware acceleration), or raise TRACKER_EMULATOR_BOOT_TIMEOUT for a TCG boot"
}

stop() {
	local sdk adb
	sdk="${ANDROID_SDK_ROOT:-${ANDROID_HOME:-}}"
	[ -n "$sdk" ] || return 0
	adb="$sdk/platform-tools/adb"
	[ -x "$adb" ] || return 0
	"$adb" devices | awk '/^emulator-/{print $1}' | while read -r s; do
		"$adb" -s "$s" emu kill >/dev/null 2>&1 || true
	done
}

case "${1:-require}" in
require) require ;;
boot) boot ;;
stop) stop ;;
*)
	echo "usage: $0 {require|boot|stop}" >&2
	exit 2
	;;
esac
