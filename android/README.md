# tracker — Android client

The native **Kotlin / Jetpack Compose** client for tracker. Today it is a **scaffold**: it builds,
installs, and shows a single screen saying so. **It collects no location.** All location logic — the
`FusedLocationProvider` foreground service, the two-step background-permission flow, the offline report
queue — is deferred to the C-track phases that follow (C1, C2, …) per the roadmap at
`operations/roadmaps/tracker.md`.

## What exists

- A Gradle project (`:app`) on **AGP 8.5.2 / Kotlin 1.9.24**, driven by the committed **Gradle 8.9
  wrapper** (`./gradlew`).
- **Compose** UI (Material3) and **WorkManager** wired as dependencies — the libraries the client is
  designed around — with no work enqueued yet.
- The three gate legs live and green: it assembles, Android Lint is clean, and a JVM unit test
  (`app/src/test/`) exercises the scaffold's own build metadata so the `testDebugUnitTest` leg is
  wired for the phases that follow. No product logic yet — location, enrollment, and the server
  contract all belong to later phases (C1+).

## SDK levels — and why

| | | why |
|---|---|---|
| `compileSdk` | 34 | build against the current platform (Android 14). |
| `targetSdk` | 34 | opt into current behavior, incl. foreground-service **`type=location`** (required at 34) that C1 needs. |
| `minSdk` | **29** | Android 10 is where `ACCESS_BACKGROUND_LOCATION` became its own runtime permission. The product is built around the background-location model that starts here (two-step "Allow all the time" on 30+); the floor is set where that model exists rather than carrying a separate legacy path below it. |

## The gate

The Android checks are compounded into tracker's one gate, `make check` (run from the repo root, not
here). The Android half is:

```bash
make android    # ./gradlew assembleDebug lintDebug testDebugUnitTest
```

`assembleDebug` proves it builds an APK, `lintDebug` proves it is clean, `testDebugUnitTest` runs the
JVM unit tests. All three must pass; lint errors fail the build (`abortOnError = true`).

## Toolchain — one-time, rootless

AGP 8.5 needs **JDK 17**; the build needs an **Android SDK**. Both install without root:

```bash
mise use -g java@temurin-17          # JDK 17
# Android SDK (cmdline-tools + platform-34 + build-tools 34.0.0 + platform-tools):
#   download commandlinetools-linux from dl.google.com, extract, accept licenses, install.
#   `unzip` is NOT baked in this container — extract with `python3 -m zipfile -e clt.zip <dir>`
#   and then `chmod +x cmdline-tools/latest/bin/*` (the zipfile module drops the exec bit).
export ANDROID_SDK_ROOT=/path/to/android-sdk   # or ANDROID_HOME
```

The gate resolves the SDK from `ANDROID_SDK_ROOT` (or `ANDROID_HOME`) and **fails loudly** when neither
points at an SDK — it never silently skips. Point it at an SDK cached under `/cache` so parallel and
subsequent runs hit it warm (it is hundreds of MB).

**Under egress lockdown** (`CLAUDE_EGRESS_LOCKDOWN=1`) the build needs these hosts allow-listed via
`CLAUDE_EGRESS_EXTRA_HOSTS`, **or** a fully vendored/cached SDK + offline Gradle:
`dl.google.com`, Google's Maven (`dl.google.com/dl/android/maven2`), `repo.maven.apache.org`,
`services.gradle.org` (the wrapper distribution), and Adoptium (the mise JDK).

## CI

`.github/workflows/ci.yml` provisions JDK 17 (`setup-java`) and the SDK (`setup-android`) for the
`check` job, then runs `make check` — the same target a human runs. Version pins live in the build files
(`gradle/libs.versions.toml`, `app/build.gradle.kts`) and the Gradle wrapper, **never** restated in CI.
Note the gate now carries **both** stacks: the Go server (a real PostGIS via testcontainers, Docker
required) and this Android client (JDK 17 + Android SDK). Size the runner for both.
