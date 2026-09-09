# tracker — developer tasks.
#
# `make check` IS the gate. CI runs exactly this target, `scripts/verify.sh tracker` in the
# umbrella runs exactly this target, and so do you. The tool pins below are therefore the
# only ones — restating them in ci.yml is how the PR gate drifts away from the gate a human
# runs, so ci.yml deliberately does not.
#
# The toolchain mirrors holdfast, the org's other Go repo, rather than the `golangci-lint`
# the roadmap sketched in §6: staticcheck + govulncheck + vet is a strictly stronger set
# than golangci-lint's default linters, and one Go gate across both repos beats two.
STATICCHECK_VERSION ?= 2025.1.1
GOVULNCHECK_VERSION ?= v1.1.4

# The GO TOOLCHAIN is the one version this repo cannot state only here. CI provisions it
# (.github/workflows/ci.yml GO_VERSION), the Dockerfile's builder image bakes it into the
# shipped binary, and go.mod's `toolchain` directive is what a local `go build` downloads —
# three different builds, so one copy will not do. Copies held in step by hope is how the
# compiler CI proves things with drifts from the compiler production runs, and because
# govulncheck scans the standard library of whichever toolchain executes it, that drift
# surfaces as a vulnerability gate that is green here and red there. internal/toolchain
# asserts the copies agree and that go.mod's `go` directive — a LANGUAGE FLOOR, not a pin —
# never climbs above them; it runs inside `make test`, so `make check` fails on a
# half-landed bump and names the files that disagree.

# Android client gate (C0). The tasks below ARE the Android half of `make check`: an app
# that assembles, lints clean, and passes its JVM unit tests. Versions (AGP, Kotlin, SDK
# levels) are pinned in android/gradle/libs.versions.toml and android/app/build.gradle.kts —
# here, as with the Go tools, the Makefile only names the gate, never a second copy of the
# pins. The gate resolves the Android SDK from ANDROID_SDK_ROOT (or ANDROID_HOME); it FAILS
# loudly when neither is set rather than silently skipping — same stance as the PostGIS tests.
ANDROID_TASKS ?= assembleDebug lintDebug testDebugUnitTest

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w

IMAGE ?= tracker:dev

# The user-interface grading routes (S0056). The AVD name and the system image are the only two
# knobs; like every other tool pin in this repo they live HERE and are not restated in ci.yml.
TRACKER_AVD ?= tracker-ui
TRACKER_SYS_IMAGE ?= system-images;android-34;google_apis;x86_64
ANDROID_UI_TASKS ?= connectedDebugAndroidTest
# The log tag the instrumented suite writes what it measured under - the contrast ratio, the target
# size and the own text of every view it swept - and which `verify-ui-android` dumps off the device into
# build/uiverify/android-grading.log. AC13 requires a FAILING run to name the view, the check and the
# measured value; this is how a PASSING one is inspectable too, rather than merely quiet, and
# `uiverify android` REFUSES a run whose evidence is missing or silent, so it cannot go quiet again.
#
# It is the device LOG rather than a device FILE because of impl-gate finding F3: every real emulator
# run pulled back a zero-byte file while this comment and android/README.md both said it carried
# every number. `UiAutomation.executeShellCommand` runs its argument through
# `Runtime.getRuntime().exec`, which splits on whitespace and honours no quoting, expands nothing and
# starts no shell - so the suite's `sh -c '... | base64 -d >> FILE'` never redirected anything. The
# log needs no quoting, no redirect and no filesystem permission. The tag is stated in one more
# place, `UiHarness.EVIDENCE_TAG`, because the device end cannot read a Makefile.
ANDROID_EVIDENCE_TAG ?= TrackerUiGrade
# How much device log to keep while the suite runs. The sweep writes a line per view per claim per
# theme, which is a few hundred kilobytes; the default ring buffer would evict the first cases.
ANDROID_LOG_BUFFER ?= 16M

.PHONY: build test check check-go android fmt vet staticcheck govulncheck pin-check tidy clean image compose-check smoke run-db \
	verify-ui verify-ui-android verify-ui-refusal verify-ui-record verify-ui-all print-avd print-sys-image

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o tracker ./cmd/tracker

# -race because the server is a concurrent program (pool + HTTP handlers + signal
# handling) and a data race there would be a wrong location, not a crash.
#
# These tests need a REACHABLE DOCKER DAEMON: the spatial correctness this project rests
# on cannot be tested against a mock, so internal/testsupport starts a real PostGIS with
# testcontainers. It FAILS — never skips — if it cannot. A green `make check` on a machine
# without Docker would be a gate proving nothing.
#
# This target also carries the two assertions that guard the gate itself: internal/toolchain
# (every file pinning the Go toolchain names the same version) and internal/vulngate (the
# vulnerability gate still turns red on an unrecorded, malformed or stale suppression).
test:
	go test -race -covermode=atomic ./...

fmt:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needs:"; echo "$$out"; exit 1; fi

vet:
	go vet ./...

staticcheck:
	go run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) ./...

# The vulnerability gate. cmd/vulngate runs the pinned govulncheck UNCHANGED — same tool, same
# version, same ./... scope — and then decides the exit status: an advisory is either remediated
# at source or recorded in .govulncheck-suppressions.yaml with a reason, a reachability argument
# and a date. An unrecorded advisory fails, a record whose advisory is no longer reported fails,
# a malformed record fails, and any govulncheck exit status that is not a verdict fails with the
# tool's own error — a tool that could not run has not told us the code is clean.
#
# internal/vulngate's tests run inside `make test` above and prove each of those still bites.
govulncheck:
	go run ./cmd/vulngate -govulncheck-version=$(GOVULNCHECK_VERSION) ./...

# The SUPPLY-CHAIN PIN gate (umbrella documentation/pinning-conventions.md, operator 2026-09-07).
# cmd/pincheck reads every pinnable reference in the working tree - Dockerfile bases, compose
# service images, workflow actions, the Go module graph, the tool versions above, the Gradle wrapper
# distribution, the Gradle version catalog and any node manifest - and refuses one that names
# something a publisher can move under it, quoting the file, the line, the reference and the broken
# clause.
#
# It has its own target because it is the one half of the gate that needs NOTHING: no Docker daemon,
# no Android SDK, no credentials and no network. It asks no registry whether a digest is still
# live - P8 is explicit that rot is discovered when a build fails, not by a gate that reds every
# unrelated pull request when a third party is down. So `make pin-check` reaches the same verdict on
# an airgapped laptop as it does in CI.
#
# internal/pingate's own tests run inside `make test` as well, which is how this reaches CI with no
# second step in ci.yml - the same shape as internal/toolchain and internal/vulngate.
pin-check:
	go run ./cmd/pincheck

# The Android client gate — assemble the debug APK, run Android Lint, run the JVM unit
# tests. Uses the committed Gradle wrapper (pinned to 8.9), so the only host requirements
# are a JDK 17 and an Android SDK. Fails with a clear message when the SDK is not located,
# rather than skipping — a gate that skips its own half proves nothing.
android:
	@sdk="$${ANDROID_SDK_ROOT:-$$ANDROID_HOME}"; \
	if [ -z "$$sdk" ] || [ ! -d "$$sdk" ] || { [ ! -d "$$sdk/platform-tools" ] && [ ! -d "$$sdk/cmdline-tools" ] && [ ! -d "$$sdk/licenses" ]; }; then \
		echo "✗ Android SDK not found. Set ANDROID_SDK_ROOT (or ANDROID_HOME) to an installed" >&2; \
		echo "  SDK (cmdline-tools + accepted licenses). Gradle/AGP fetch platforms;android-34" >&2; \
		echo "  and build-tools;34.0.0 on demand; the level pins live in app/build.gradle.kts." >&2; \
		echo "  See android/README.md for the one-time rootless install." >&2; \
		exit 1; \
	fi; \
	cd android && ./gradlew --no-daemon $(ANDROID_TASKS)

# THE gate. CI runs exactly this. It is now BOTH stacks: the Go server (with a real PostGIS
# via testcontainers) and the Android client (assemble + lint + unit). Sized accordingly —
# the gate env needs a Docker daemon AND a JDK 17 + Android SDK.
check: check-go android

# The Go half of the gate, kept as its own target so `make check-go` can run the server
# checks alone (e.g. on a machine without the Android SDK). `check` runs both.
#
# pin-check rides here rather than in ci.yml, because ci.yml runs `make check` and nothing else:
# that is the rule that keeps the PR gate identical to the gate a human runs. It guards BOTH stacks'
# pins (the Gradle wrapper and version catalog included), so it sits in the half that runs
# everywhere rather than in `android`, which needs an SDK.
check-go: fmt vet build test staticcheck govulncheck pin-check

# --- user-interface grading -----------------------------------------------------
#
# tracker ships TWO user interfaces and the umbrella's frontend conventions bind both: the browser
# map at GET /map, and the Android client's single Compose screen. F2 of those conventions admits
# exactly ONE grader for a claim about what a person sees — the runtime that draws it — so these
# four targets drive a real browser engine and a real Android emulator, and refuse loudly rather
# than skipping when either is missing. That refusal is the deliverable, not a nuisance: a route
# that goes quiet when its prerequisite is absent reports green while proving nothing, which is the
# same stance `make android` takes toward a missing SDK and `make test` toward a missing Docker.
#
# These are deliberately NOT folded into `make check`. `make check` is the gate a human runs on a
# laptop; these need a browser engine and a booted emulator, and CI runs all six targets.

# The map's rendered claims, in a real browser engine (AC1-AC11, AC21, AC22), each shown able to go
# red against a surface mutated to break exactly that claim (AC18).
verify-ui:
	go run ./cmd/uiverify web

# The Android screen's rendered claims (AC12-AC17) on a booted emulator, plus the repository
# explanation documents the screen's labels moved their paragraphs into.
#
# The accessibility claims are graded ONE AT A TIME - contrast, touch target size, and no state
# carried by colour alone - each in both themes and each with its own mutation. A single mutation
# breaking two claims at once cannot say which check is blind, which is what impl-gate finding F21
# was. A fourth claim, a non-empty spoken name on every control, is NOT graded on this route: it is
# S0076-tracker-android-spoken-name's, and the check that used to answer it here read a neighbouring
# node's label.
#
# The boot's exit status is CHECKED rather than piped away. `x=$(cmd | tail -1)` takes tail's status,
# so a boot that refused on a timeout - the one absence `require` cannot pre-check - would not stop
# the line, and the refusal message AC19 asks for would be lost behind Gradle's own "no device".
#
# The last line is AC18's count, and it is not optional. `gradlew connectedDebugAndroidTest` is green
# whenever the cases that RAN passed, so on its own it cannot tell a suite that graded every claim
# from a suite whose demonstrations were all deleted. `uiverify android` reads the emulator's own
# JUnit results back and fails unless every claim the record names ran AND carries a passing
# `<claim>_demonstration` beside it - the same force `Summarise` has on the browser route.
verify-ui-android:
	go run ./cmd/uiverify docs
	@TRACKER_AVD="$(TRACKER_AVD)" TRACKER_SYS_IMAGE="$(TRACKER_SYS_IMAGE)" ./scripts/android-emulator.sh require
	@set -e; \
	out="$$(mktemp)"; trap 'rm -f "$$out"' EXIT; \
	TRACKER_AVD='$(TRACKER_AVD)' TRACKER_SYS_IMAGE='$(TRACKER_SYS_IMAGE)' ./scripts/android-emulator.sh boot >"$$out"; \
	serial="$$(tail -1 "$$out")"; \
	if [ -z "$$serial" ]; then echo "the emulator script exited 0 but named no device" >&2; exit 1; fi; \
	echo "instrumented suite on $$serial"; \
	adb="$${ANDROID_SDK_ROOT:-$$ANDROID_HOME}/platform-tools/adb"; \
	: "Room for the whole sweep, and a clean slate, so the evidence dumped below is THIS run's." ; \
	"$$adb" -s "$$serial" logcat -G $(ANDROID_LOG_BUFFER) >/dev/null 2>&1 || true; \
	"$$adb" -s "$$serial" logcat -c >/dev/null 2>&1 || true; \
	status=0; \
	( cd android && ANDROID_SERIAL="$$serial" ./gradlew --no-daemon $(ANDROID_UI_TASKS) ) || status=$$?; \
	mkdir -p build/uiverify; \
	rm -f build/uiverify/android-grading.log; \
	: "-v raw prints the message and nothing else, so the file is the suite's own lines; -s TAG:I" ; \
	: "silences every other tag. The suite writes here through android.util.Log because the" ; \
	: "instrumentation's shell cannot redirect: executeShellCommand is Runtime.exec, which splits on" ; \
	: "whitespace and starts no shell, so the file this used to write was never created (finding F3)." ; \
	"$$adb" -s "$$serial" logcat -d -v raw -s $(ANDROID_EVIDENCE_TAG):I >build/uiverify/android-grading.log 2>/dev/null || true; \
	echo "--- what the emulator measured (build/uiverify/android-grading.log) ---"; \
	if [ -s build/uiverify/android-grading.log ]; then cat build/uiverify/android-grading.log; \
	else echo "(the device wrote no grading evidence this run)"; fi; \
	exit $$status
	go run ./cmd/uiverify android

# Both routes, with their prerequisite removed, must exit non-zero naming what is missing (AC19).
verify-ui-refusal:
	go run ./cmd/uiverify refusal

# The committed F1-F11 record, checked against what the two routes actually ran (AC20).
verify-ui-record:
	go run ./cmd/uiverify record

verify-ui-all: verify-ui verify-ui-android verify-ui-refusal verify-ui-record

# CI provisions the emulator from these, so that the AVD name and the system image stay pinned HERE
# and are never restated in ci.yml - the same rule the Go tools and the Android SDK levels follow.
print-avd:
	@echo "$(TRACKER_AVD)"

print-sys-image:
	@echo "$(TRACKER_SYS_IMAGE)"

# --- deployment ---------------------------------------------------------------

image:
	docker build -t $(IMAGE) .

compose-check:
	docker compose config -q && echo "docker-compose.yml is valid"

# Brings the real stack up and asserts /healthz answers 200 — the S0 acceptance. This is
# the packaging gate: "it compiled" does not prove the binary can find its database.
smoke:
	./scripts/smoke-compose.sh

tidy:
	go mod tidy

clean:
	rm -f tracker
