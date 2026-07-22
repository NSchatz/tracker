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

.PHONY: build test check check-go android fmt vet staticcheck govulncheck tidy clean image compose-check smoke run-db

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o tracker ./cmd/tracker

# -race because the server is a concurrent program (pool + HTTP handlers + signal
# handling) and a data race there would be a wrong location, not a crash.
#
# These tests need a REACHABLE DOCKER DAEMON: the spatial correctness this project rests
# on cannot be tested against a mock, so internal/testsupport starts a real PostGIS with
# testcontainers. It FAILS — never skips — if it cannot. A green `make check` on a machine
# without Docker would be a gate proving nothing.
test:
	go test -race -covermode=atomic ./...

fmt:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needs:"; echo "$$out"; exit 1; fi

vet:
	go vet ./...

staticcheck:
	go run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) ./...

govulncheck:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

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
check-go: fmt vet build test staticcheck govulncheck

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
