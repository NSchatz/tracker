# tracker — Android client

The native **Kotlin / Jetpack Compose** client for tracker.

As of **C2** it collects and it **does not lose what it collects**: a **foreground service** takes
continuous fixes from the **fused location provider** behind the **two-step background-location
permission flow**, writes each one to a **durable on-disk queue**, and a **WorkManager** job
constrained to `NetworkType.CONNECTED` drains that queue into the server's already-shipped
`POST /v1/fixes`. Going offline now delays reporting instead of losing it. It does **not** yet store
its token securely (C3), adapt its cadence to save battery (C4), or show a map (C5).

## What exists

| | |
|---|---|
| **Collection** | `collect/LocationCollectionService` — a foreground service, `type=location`, with the mandatory ongoing notification. Continuous updates from `FusedLocationProviderClient`. |
| **Permission flow** | `permission/LocationPermissionFlow` — the two-step grant, as a pure state machine. Foreground first; background second, and on Android 11+ that second step is the **settings page**, not a dialog. |
| **Wire contract** | `protocol/` — the `POST /v1/fixes` payload, its validation, the response classifier, and the HTTP reporter. All pure JDK/Kotlin, no framework classes. |
| **Durable queue** | `queue/FixQueue` — one atomically-written file per fix under `filesDir`, named by its `ts`, holding the exact wire body. Survives the process, a reboot, and a long outage. |
| **Flush** | `queue/QueueFlusher` (pure: what to send, keep, discard) driven by `queue/FixUploadWorker` (WorkManager, `NetworkType.CONNECTED`, exponential backoff jittered by `queue/FlushBackoff`). |
| **Configuration** | `collect/ClientPreferences` — server URL + device token, entered in-app. **Plaintext for now** (see *Known limitations*). |
| **UI** | `ui/MainActivity` — one screen: the current permission step, the server settings, start/stop, and honest counters (`delivered` / `queued` / `dropped`). |

### The shape of the code, and why

Everything that can be decided without a device was pushed **out** of the Android classes and into
pure Kotlin: the fix payload, the coordinate and timestamp validation, the millis→seconds
conversion, the permission state machine, the HTTP status classifier, the backoff schedule. What is
left in `LocationCollectionService` and `MainActivity` is the thin framework edge — reading a
`Location`'s fields, calling `checkSelfPermission`, posting a notification.

That split is not stylistic. It is the only way this phase can be honestly gated: the pure half is
genuinely provable in CI, and the framework half genuinely is not. Blurring them would have meant
either testing nothing, or writing tests that mock the platform and prove only that the mock was
configured.

---

## What the gate proves — and what it cannot

**This is the honest part of C1 and it should be read before trusting a green build.**

`make check` on this module runs `assembleDebug lintDebug testDebugUnitTest`. That is a real gate and
it catches real defects — the `readNBytes` call that would have been a `NoSuchMethodError` on every
Android 10 phone was caught by `lintDebug` on the first run of this phase, not by review.

### Proven by the gate

- **The exact bytes of a fix report.** `FixReporterTest` stands up a real HTTP server on a loopback
  `ServerSocket` (`TestHttpServer`, which parses the request itself — `com.sun.net.httpserver` is
  **not** on the Android unit-test classpath) and asserts the method, the `/v1/fixes` target, the
  `Authorization: Bearer …` header, the `Content-Type`, the fixed `Content-Length`, and the **exact
  JSON body**. Real sockets, real bytes — not a mocked client returning what the test told it to.
- **Payload construction.** Required fields present; absent optionals **omitted, never zeroed**;
  present zeros preserved; strings escaped; coordinate precision not truncated; exactly the eight
  fields `SPEC.md` documents and no ninth.
- **Coordinate validation**, including the lon/lat swap that PostGIS would otherwise silently coerce
  into a real-looking point in the South Atlantic — and, explicitly, a test recording that a swap
  which *stays in range* (Rome ↔ Indian Ocean) is **not** detectable here.
- **The ingest window**, matching the server's ±24 h / −90 d bounds exactly.
- **The permission state machine**, exhaustively across API 29/30/31/32/33/34: that background
  location is never bundled into the foreground request; that API 29 gets a dialog and API 30+ gets
  the settings page; that a permanently-denied grant routes to settings instead of re-requesting into
  silence; that an approximate-only grant is not re-prompted forever.
- **The response classifier**: that `200` (idempotent replay) counts as delivered, that `400`/`401`
  are permanent, that `5xx`/`429` are retryable, and that no status is ever both.
- **Configuration validation**, including the refusal to send a bearer credential over plaintext
  `http://` in a release build.
- **The durable queue, against a real filesystem** (`FixQueueTest`): that a queued fix is visible to
  a *different* `FixQueue` instance on the same directory (the stand-in for a process death); that
  entries come back oldest-first whatever order they arrived in, including the zero-padding that
  stops `"10"` sorting before `"9"`; that enqueueing the same `ts` twice is an idempotent no-op and
  the **first** report for an instant wins, matching the server's rule; that the stored bytes are
  byte-for-byte the wire body and the `msg_id` is fixed at enqueue rather than regenerated; that a
  crash mid-write (a leftover `.tmp`) is invisible and swept; that an unreadable entry is discarded
  **and counted** rather than blocking the FIFO forever; that a full queue trims to the oldest and
  reports how many.
- **The flush loop, over real HTTP** (`QueueFlusherTest`), against a fake `/v1/fixes` that keeps the
  server's actual idempotency contract — `201` the first time it sees a `ts`, `200 duplicate` on a
  replay — so a client that sent anything twice would be caught rather than flattered. It pins the
  acceptance criterion directly: **25 fixes buffered while the server is unreachable, then delivered
  exactly once when it returns** — every `ts` stored, in ascending order, zero duplicates, queue
  empty. Also: that a transient failure or an unreachable server leaves **every** fix queued (there
  is no attempt counter to run out); that a `401` stops the flush *whole* after one request and
  discards nothing; that a `400` drops just that fix so the queue keeps moving; that a replay after a
  lost response is absorbed as a `200` rather than wedging the head of the queue; and that one run is
  bounded by its batch limit and says so.
- **The backoff jitter** (`FlushBackoffTest`): that the initial WorkManager delay is spread across a
  window rather than identical on every device, and never falls under WorkManager's own 10 s floor.

### NOT proven by the gate — operator checks on a real device

None of the following can be established by a headless build, and **no test in this repo pretends
otherwise**. A test asserting that a mocked `checkSelfPermission` returned `PERMISSION_GRANTED` would
be evidence about the mock, not about Android.

- **That a runtime permission can actually be granted.** A real grant needs a human tapping a system
  dialog. The gate proves which step the app *decides* to take; it cannot take it.
- **That the "Allow all the time" settings round-trip works** on a given Android version and OEM
  skin. The settings page layout is vendor-specific.
- **That the foreground service starts, posts its notification, and survives screen-off.**
- **That the fused provider actually delivers fixes** at the configured cadence, or at all.
- **That fixes land in the server's `fixes` table** end to end.
- **That the queue's writer is crash-atomic, or that it trims in the right order.** `FixQueue`
  writes to a `.tmp`, `fsync`s, renames into place, and only *then* trims the queue back to its cap —
  so a write that fails destroys nothing. The *reader* half of that is tested (a `.tmp` is never
  returned, it is swept, a zero-length entry is discarded and counted). The *writer* half is
  **review-only**: a JVM test cannot interrupt a write mid-syscall or cut the power, and no test
  fills the queue to its cap *and* fails the write, so reversing the trim/write order would keep the
  suite green. Said out loud here because an untested invariant nobody wrote down is the one that
  vanishes in a refactor — and one written down in the wrong list is worse.
- **That WorkManager actually schedules the flush when connectivity returns.** The queue and the
  flush loop are proved above; that `NetworkType.CONNECTED` fires on a real radio, survives Doze, and
  is restored after a reboot is platform behaviour on a device. `work-testing` would need a
  `Context` — i.e. an instrumented run or Robolectric — and would then be asserting WorkManager's
  own scheduler back to itself, which is why `FixUploadWorker` was kept free of decisions instead.
- **Battery cost**, and whether an OEM battery manager (Samsung, Xiaomi, …) kills the service anyway.

#### Why the instrumented suite grades the SCREEN and nothing else

The roadmap sketched instrumented tests with `LocationManager` mock providers for C1, and there were
none, for a reason that still holds: **they would not prove the thing that matters.** An instrumented
test grants permissions with `GrantPermissionRule`, which hands them over programmatically. That
bypasses the entire two-step flow — the dialog, the settings round-trip, the Android 11 behaviour
change — which *is* the risky part of C1. A green instrumented test would say "permissions we granted
ourselves are granted". Mock providers have the same shape of problem: they prove the app can read a
location the test injected, not that the fused provider delivers one on a real phone under Doze.

There **is** now an instrumented suite (`app/src/androidTest`, `make verify-ui-android`), and it is
carefully scoped to the half of that argument which does not apply. It grades **what the screen
draws** — brevity and the explanation destination, the counters' honesty, the three states,
staleness, and a 360dp layout that clips nothing — because that is a claim only a running Android
runtime can answer, and because the umbrella's frontend conventions say so in as many words: "an
emulator, not the JVM, for Android". It grades **nothing about collection**: not the permission
decisions, not the fused provider, not the flush schedule. Those are still the pure unit tests plus
the operator check below, and the reasoning above is why.

**Eight cases in that suite are `@Ignore`d and belong to another item.** The accessibility sweep
(contrast, touch targets, spoken names, state carried by colour) and the operable-without-a-pointer
traversal are carried by `S0074-tracker-android-a11y-operability`: on the emulator the traversal
never reaches the save control, and the platform sweep stayed green against a screen deliberately
broken to break it, so its passes were not evidence. They are kept verbatim as the artefacts that
item inherits. `FRONTEND-CONVENTIONS-RECORD.md` records the deferral clause by clause and
`make verify-ui-record` fences it — an `@Ignore` with no deferral behind it, or a deferral with no
`@Ignore` behind it, turns that check red.

The suite needs a booted emulator and **refuses loudly when it cannot have one**, naming the missing
piece and how to get it (`scripts/android-emulator.sh`). It never skips. Without `/dev/kvm` an
x86_64 image does not merely run slowly — it segfaults under QEMU's interpreter — so a runner without
hardware virtualisation turns the job red rather than grading nothing. CI enables KVM explicitly for
that job.

#### The device check to run before believing C1 works

1. `tracker create-family` / `tracker enroll` on the server; copy the printed device token.
2. Install the debug APK; enter the server URL and token; **Save**. Confirm the app reports the
   configuration as valid.
3. Walk the permission flow. Confirm it asks for **foreground location first**, and only then offers
   the background step — and that on Android 11+ the background step opens **settings**, not a
   dialog.
4. Start collection. Confirm the **ongoing notification appears** and names what it is doing.
5. Turn the screen off, wait past two collection intervals, walk more than 25 m.
6. Check the server: `GET /v1/positions` (viewer token) should show this device moving, and
   `GET /v1/devices/{id}/history` should show the fixes. The app's **delivered** counter should be
   climbing and **dropped** should be 0.
7. Close the app entirely (swipe from recents). Confirm collection continues — this is the step that
   actually exercises the background-location grant.
8. Enable airplane mode for a few minutes, then disable it. **Expect no gaps.** This is C2's
   acceptance check. While offline the app's **queued** counter should climb and **dropped** should
   stay at 0; when the radio comes back the queue should drain, **delivered** should climb by the
   same amount, and `GET /v1/devices/{id}/history` should show the buffered fixes in order with no
   duplicates. A gap here means the flush is not being scheduled — the one part of C2 the gate
   cannot prove.
9. Force-stop the app with fixes still queued, then reopen it. The **queued** counter should come
   back non-zero (it is read from disk, not from memory) and the queue should drain.

This mirrors how `holdfast` documented its CI-unprovable power-loss limitation rather than faking a
test for it. Writing the limitation down is the deliverable; a green test that proved nothing would
be worse than no test.

---

## Known limitations after C2

- **The queue is bounded at 5,000 fixes** (~3.5 days at the current one-a-minute cadence). Past that
  the **oldest** waiting fixes are evicted to make room, and the count surfaces in the UI's
  `dropped`. A cap has to exist — an unbounded queue on a phone offline for a month is a disk-full
  bug — and the oldest end is the right one to sacrifice: the recent trail is what answers "where are
  they now", and the oldest entries are nearest the server's 90-day ingest floor anyway.
- **A fix older than 90 days is dropped by the *server*, not locally.** It is sent, refused with a
  `400`, and then removed and counted like any other permanent refusal — one wasted request rather
  than a client-side copy of a server constant judged against the phone's own clock, which would
  silently delete deliverable fixes on a device whose clock ran fast. Only reachable after an outage
  measured in months.
- **A recovered server can wait out a long backoff.** WorkManager's exponential retry runs
  indefinitely (which is the point) but its ceiling is ~5 hours, and a new fix deliberately does not
  reset it — resetting on every fix would hammer a dead server once a minute. So after a very long
  outage the first successful flush can lag the reconnection by hours. Nothing is lost; it is
  latency, and the fixes carry their original `ts`.
- **Delivery is at-least-once on the wire.** A crash between the server's `201` and the local delete
  replays that fix; the server absorbs it as a `200 duplicate` on `(device_id, ts)`, so the database
  is exactly-once. The app's `delivered` counter can therefore over-count by the number of replays,
  which is the honest trade for never under-delivering.
- **The scheduling half is not gate-provable.** The queue and the flush loop are unit-tested against
  a real filesystem and a real socket; that WorkManager actually runs the job when the radio returns
  is an operator check (above).
- **The device token is stored in plaintext** `SharedPreferences`. Not readable by other apps on a
  non-rooted device, but readable with root, an unlocked bootloader, or a full-device backup.
  `allowBackup="false"` is set. **C3** moves it to `EncryptedSharedPreferences` with an Android
  Keystore master key.
- **No enrollment flow.** The token is pasted in by hand from `tracker enroll` output. **C3**.
- **Static cadence.** 60 s target, 25 m displacement filter, 2 min batching — the same whether the
  phone is parked or on a motorway. **C4** makes it adaptive.
- **No map.** **C5**.
- **Google Play services required.** `FusedLocationProviderClient` is Play services, not AOSP; there
  is no `LocationManager` fallback, so a fully degoogled phone cannot run this client today.
- **Background reliability is not absolute** — Doze, App Standby and OEM battery-killers can throttle
  or stop collection regardless of correct implementation. Roadmap §9; not solvable in-app.

## SDK levels — and why

| | | why |
|---|---|---|
| `compileSdk` | 34 | build against the current platform (Android 14). |
| `targetSdk` | 34 | opt into current behavior, incl. foreground-service **`type=location`**, which Android 14 makes **required**. |
| `minSdk` | **29** | Android 10 is where `ACCESS_BACKGROUND_LOCATION` became its own runtime permission. The product is built around the background-location model that starts here (two-step "Allow all the time" on 30+); the floor is set where that model exists rather than carrying a separate legacy path below it. |

## The gate

The Android checks are compounded into tracker's one gate, `make check` (run from the repo root, not
here). The Android half is:

```bash
make android    # ./gradlew assembleDebug lintDebug testDebugUnitTest
```

`assembleDebug` proves it builds an APK, `lintDebug` proves it is clean (lint **errors** fail the
build — `abortOnError = true`), `testDebugUnitTest` runs the JVM unit tests described above.

### The UI gate, which is a separate target on purpose

```bash
make verify-ui-android    # the instrumented suite, on a BOOTED emulator
make verify-ui-refusal    # ... and the proof it refuses when there is no emulator
```

`make check` is unchanged and still means what it always meant. `make verify-ui-android` sits beside
it because it needs something `make check` does not: a running Android device. It

1. checks the repository explanation documents carry every claim that left the screen,
2. asserts an SDK, an emulator binary, a system image and an AVD are present — **refusing by name**
   when any is missing,
3. boots the AVD headless and blocks until `sys.boot_completed`,
4. runs `connectedDebugAndroidTest`.

The AVD name and the system image are `TRACKER_AVD` and `TRACKER_SYS_IMAGE` in the root `Makefile`,
which is the only place they are stated; CI reads them back with `make print-avd` /
`make print-sys-image` rather than keeping a second copy.

Provision an emulator alongside the SDK below:

```bash
sdkmanager --install "platform-tools" "emulator" "system-images;android-34;google_apis;x86_64"
echo no | avdmanager create avd -n tracker-ui -k "system-images;android-34;google_apis;x86_64" -d pixel_5
```

**`/dev/kvm` is a prerequisite, not an optimisation.** Without it the x86_64 image does not boot at
all: QEMU's TCG interpreter segfaults partway through Android 14's start-up. `scripts/android-emulator.sh`
waits for `sys.boot_completed` and then refuses by name, so an environment without hardware
virtualisation reports a missing prerequisite rather than a passing suite.

## Toolchain — one-time, rootless

AGP 8.5 needs **JDK 17**; the build needs an **Android SDK**. Both install without root:

```bash
mise use -g java@temurin-17          # JDK 17 — and note the -g: without a GLOBAL pin the
                                     # `java` shim errors with "No version is set for shim: java"
                                     # and `make android` fails before Gradle even starts.
# Android SDK (cmdline-tools + platform-34 + build-tools 34.0.0 + platform-tools):
#   download commandlinetools-linux from dl.google.com, extract, accept licenses, install.
#   `unzip` is NOT baked in this container — extract with `python3 -m zipfile -e clt.zip <dir>`
#   and then `chmod +x cmdline-tools/latest/bin/*` (the zipfile module drops the exec bit).
export ANDROID_SDK_ROOT=/path/to/android-sdk   # or ANDROID_HOME
```

The gate resolves the SDK from `ANDROID_SDK_ROOT` (or `ANDROID_HOME`) and **fails loudly** when
neither points at an SDK — it never silently skips. Point it at an SDK cached under `/cache` so
parallel and subsequent runs hit it warm (it is hundreds of MB).

**Under egress lockdown** (`CLAUDE_EGRESS_LOCKDOWN=1`) the build needs these hosts allow-listed via
`CLAUDE_EGRESS_EXTRA_HOSTS`, **or** a fully vendored/cached SDK + offline Gradle:
`dl.google.com`, Google's Maven (`dl.google.com/dl/android/maven2`), `repo.maven.apache.org`,
`services.gradle.org` (the wrapper distribution), and Adoptium (the mise JDK). C1 adds
`com.google.android.gms:play-services-location`, which resolves from Google's Maven.

## CI

`.github/workflows/ci.yml` provisions JDK 17 (`setup-java`) and the SDK (`setup-android`) for the
`check` job, then runs `make check` — the same target a human runs. Version pins live in the build
files (`gradle/libs.versions.toml`, `app/build.gradle.kts`) and the Gradle wrapper, **never** restated
in CI. Note the gate carries **both** stacks: the Go server (a real PostGIS via testcontainers,
Docker required) and this Android client (JDK 17 + Android SDK). Size the runner for both.

A second job, `ui`, provisions the same toolchain plus an emulator, **enables `/dev/kvm` explicitly**,
and runs the four UI targets. It is separate from `check` because its prerequisites are different, and
because a browser or an emulator that has gone missing must turn a job red rather than quietly
grading nothing.
