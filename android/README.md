# tracker — Android client

The native **Kotlin / Jetpack Compose** client for tracker.

As of **C2** it collects and it **does not lose what it collects**: a **foreground service** takes
continuous fixes from the **fused location provider** behind the **two-step background-location
permission flow**, writes each one to a **durable on-disk queue**, and a **WorkManager** job
constrained to `NetworkType.CONNECTED` drains that queue into the server's already-shipped
`POST /v1/fixes`. Going offline now delays reporting instead of losing it. It does **not** yet store
its token securely (C3), adapt its cadence to save battery (C4), or show a map (C5).

As of **ALERT-2** it also **watches**: a crossing pushed by the server arrives as a notification
naming the device, the Place and the direction, and opening the app shows the family's recent
crossings read back from `GET /v1/geofence-events` - so a crossing the push backend dropped is still
findable. Receiving is **FCM only** in this phase; a UnifiedPush deployment is told so rather than
left silent.

## What exists

| | |
|---|---|
| **Collection** | `collect/LocationCollectionService` — a foreground service, `type=location`, with the mandatory ongoing notification. Continuous updates from `FusedLocationProviderClient`. |
| **Permission flow** | `permission/LocationPermissionFlow` — the two-step grant, as a pure state machine. Foreground first; background second, and on Android 11+ that second step is the **settings page**, not a dialog. |
| **Wire contract** | `protocol/` — the `POST /v1/fixes` payload, its validation, the response classifier, and the HTTP reporter. All pure JDK/Kotlin, no framework classes. |
| **Durable queue** | `queue/FixQueue` — one atomically-written file per fix under `filesDir`, named by its `ts`, holding the exact wire body. Survives the process, a reboot, and a long outage. |
| **Flush** | `queue/QueueFlusher` (pure: what to send, keep, discard) driven by `queue/FixUploadWorker` (WorkManager, `NetworkType.CONNECTED`, exponential backoff jittered by `queue/FlushBackoff`). |
| **Alert receive path** | `alert/TrackerMessagingService` - the FCM service. It decides nothing: `alert/AlertIntake` parses and either renders or discards-and-counts, and `alert/AlertNotifications` posts. |
| **Alert surface** | `alert/AlertStatusPolicy` (the ordered six-state delivery status), `alert/CrossingListView` (the in-app list and its five outcomes), `alert/AlertClient` (the two viewer routes), `alert/MiniJson` (a strict reader, no dependency). All pure. |
| **Configuration** | `collect/ClientPreferences` - server URL, device token and **viewer token**, entered in-app. **Plaintext for now** (see *Known limitations*). |
| **UI** | `ui/MainActivity` - one screen: the current permission step, the server settings, start/stop, honest counters (`delivered` / `queued` / `dropped`), and the alert delivery status with the crossing list. |

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
- **What a received push is allowed to become** (`PushPayloadTest`): that a complete push renders a
  notification naming the device, the Place and the direction; and that **twelve** distinct ways of
  being incomplete - no title, a blank title, the wrong `type`, no Place name, no direction, an
  unknown direction, no ids, no `ts`, a `ts` in the wrong format - each render **nothing** rather than
  a notification with an invented part. Also that the push's UTC calendar `ts` and the crossings
  route's epoch seconds resolve to the **same instant**, across leap days and the year-2000 leap rule
  - which is what makes one crossing one row.
- **The discard count** (`AlertPrivacyAndDiscardTest`): that a discarded push increments an
  observable counter and adds nothing to the list. Observable **off-device**, which was the point:
  a client that silently drops malformed messages is indistinguishable from one receiving none.
- **The alert delivery status, exhaustively** (`AlertStatusPolicyTest`, `AlertClientTest`): that the
  ordered procedure produces exactly one of nine distinguishable answers; that each of the four
  not-receivable reasons (no routing address on this phone, no backend configured, a backend this app
  cannot receive from, a server that does not report) is its own state; that a refused registration
  and an unreachable server are different; and - the one that would be easy to get wrong and
  impossible to notice - that a registration still **in flight** does not read as `armed`.
- **The crossings read, over real HTTP** (`AlertClientTest`): that a fake server returning two rows
  puts both in the list, that the viewer credential travels in the `Authorization` **header** and
  never in the URL, and that a rejected credential, an unreachable server, an unreadable answer and a
  `500` are each their own state and **none of them is an empty family**.
- **The merge across the two sources** (`CrossingListViewTest`): that one crossing arriving both as a
  push and from the route is shown **once**, and that two crossings of one device at one Place at
  different instants stay **two rows** - the merge key that cannot separate them is what would hide
  an arrival out of the list that exists to catch what got lost.
- **A crossings row with no device name** (`CrossingsResponseTest`): that a server older than the
  additive `device_name` field still yields the Place, the direction and the time, with a visibly
  not-a-name placeholder and **no fabricated name**; and that a body this client cannot read raises
  rather than returning an empty list that would be shown as "your family has no crossings".
- **The privacy boundary** (`AlertPrivacyAndDiscardTest`): that nothing the alert surface renders or
  holds carries a coordinate, an accuracy or a raw fix datum - driven with a push and a crossings row
  that both carry coordinate-shaped extras - and that this client reads only the two viewer routes
  that return none.
- **The viewer credential's validation** (`ViewerConfigValidationTest`): the same URL rules as the
  device token, a plaintext refusal that names which credential is at risk, and the two credentials
  failing independently. Plus (`AlertClientTest`) that the credential appears in **no** string the
  surface renders, sends as content, or would put in a log line.
- **That the app builds with no Firebase project configuration.** The gate itself is the test: this
  module depends on the FCM client library and deliberately does not apply the Google Services Gradle
  plugin, so `make check` is green on a checkout that has never seen a `google-services.json`.

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
- **That a real FCM message reaches a real handset, and wakes it under Doze.** FCM's high-priority
  Doze exemption is a documented platform behaviour; whether it happens on a given phone, network and
  OEM skin is not something a headless build can establish. The gate proves what this app does with a
  message it is given; it cannot make one arrive.
- **That the notification permission dialog can actually be granted**, and that a granted permission
  produces a visible notification on the lock screen. Same shape as C1's permission limitation: the
  gate proves which state the app *decides* it is in, and cannot tap a system dialog.
- **The full alert path end to end** - walk into a real Place and see the notification. Every piece of
  it is tested (the crossing is recorded by the server's own suite, the fan-out and the outcome
  accounting by `internal/push`, the parse and the render here), and the seams between them on a real
  device are not.
- **That a real rotated FCM registration token supersedes its predecessor.** The SERVER half is
  proved against a real PostGIS (`internal/server/alert_test.go`: a registration naming a replaced
  address removes exactly that endpoint, and one crossing then produces one delivery and not two).
  The rotation EVENT - `onNewToken` firing with a new address - is a platform behaviour on a device.
- **That a Firebase project, once configured, yields a registration token.** The no-project case is
  gate-proved (the build is green without one and the app reports it has no usable push
  configuration); the with-project case needs a Firebase account and a real handset.

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

**Nothing in that suite is `@Ignore`d.** The accessibility sweep and the operable-without-a-pointer
traversal were parked while `S0074-tracker-android-a11y-operability` carried them; both are graded
here again, and `FRONTEND-CONVENTIONS-RECORD.md` names the assertions rather than deferring the
clauses. The fence stays and is now shut on every pair: an `@Ignore` with no deferral behind it, or a
deferral of any kind at all, turns `make verify-ui-record` red.

**The accessibility sweep grades ONE claim at a time, in both themes, and measures rather than only
delegating.** Contrast, touch target size, a non-empty spoken name and no-state-by-colour-alone are
four claims with four separate mutations, because a mutation that breaks two at once cannot say which
check went blind — which is what happened the first time this was tried. Google's Accessibility Test
Framework still runs over the tree, and its ERRORs are failures; but the ratio, the target size and
the spoken name are also computed here from the same two inputs the platform checks read (the
`AccessibilityNodeInfo` tree the emulator published and the screenshot it painted), because a
hierarchy built from node infos carries no text or background colour, so those checks can only report
a screenshot heuristic — at WARNING, never at ERROR. Filtering their results to ERROR is how a sweep
came back clean over text at 1.7:1. Every number the sweep measured is written to
`build/uiverify/android-grading.log` and printed by `make verify-ui-android`, so a PASSING run is
inspectable rather than merely quiet.

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

#### The device check to run before believing ALERT-2 works

The alert half. Steps 1 to 3 need no Firebase project at all and are worth running first, because
they are the states most deployments will actually be in.

The card draws a LABEL for each state, a few words, and the paragraph explaining it is behind
**About alerts** on the same screen (F8 of the umbrella's frontend conventions). Both are quoted
below: read the label on the card, and the sentence one tap away.

1. `tracker add-viewer` on the server; copy the printed **viewer** token and paste it into the new
   field. With no Firebase project configured in this build, expect the card to read **Not
   receivable: no push address** - "this phone has no usable push configuration, so there is no
   address to register" behind the affordance - and to still list the family's crossings underneath.
   An app that says **Alerts armed** here, or that fails to start, is the defect.
2. `tracker add-place`, then walk a phone in and out of it. Within a debounce or two the crossing
   should appear in the in-app list, named with the family's own name for the device. This path does
   not involve push at all, and it is the fail-safe: it must work whether or not alerts do.
3. Point the app at a server with `TRACKER_PUSH_PROVIDER` unset, and then at one set to
   `unifiedpush`. Expect two *different* labels - **Not receivable: server sends none** and **Not
   receivable: backend unsupported** - and the crossing list in both. Then paste a wrong viewer token
   and expect **Not registered: token refused**, not an empty list. Four states, four labels: a
   screen that says the same words for two of them is the defect this enumeration exists to catch.
4. To exercise the push itself you need a Firebase project: add its `google-services.json`, apply the
   `com.google.gms.google-services` plugin **in your own build**, and configure the server with
   `TRACKER_PUSH_PROVIDER=fcm`, `TRACKER_FCM_PROJECT_ID` and `TRACKER_FCM_CREDENTIALS_FILE`. Neither
   the file nor the plugin is committed here, deliberately - see *Known limitations*.
5. With that in place, cross a Place and expect a notification naming the device, the Place and the
   direction, and the same crossing appearing **once** in the in-app list (not twice, once from each
   source).
6. Deny the notification permission (Android 13+) and cross again. Expect the card to read **Alerts
   cannot be shown**, no notification, and the crossing still in the list.
7. Turn the phone's network off, cross **five or more** distinct device-and-Place pairs, then bring
   it back. Expect some alerts to be missing, and expect
   `docker compose logs tracker | grep push.delivery.outcome` to show `beyond-collapse-bound` for the
   surplus. Nothing anywhere should say "delivered". Every one of those crossings must still be in
   the in-app list - that is the whole point of the list.

   With **two** phones registered, which is the ordinary case, each record names the endpoint it is
   about in its `endpoint` field - a digest, not the routing address. Map it back to a phone with the
   query in [`SPEC.md`](../SPEC.md); the offline phone's records are the ones carrying
   `beyond-collapse-bound`, and the other phone's are not.
8. Clear the app's data and re-enter both tokens. The old routing address is now unnameable, so the
   server keeps a stale row for it. This is the recorded residual below, not a bug to file.

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
- **BOTH credentials are stored in plaintext** `SharedPreferences`: the device (write) token, and -
  since ALERT-2 - the **viewer (read) token** the alert surface needs. Neither is readable by other
  apps on a non-rooted device; both are readable with root, an unlocked bootloader, or a full-device
  backup. `allowBackup="false"` is set, which keeps them out of cloud backups.

  The viewer token is a real widening of what this phone carries: it reads the family's whole
  crossing history. What bounds it is what the app does with it - exactly two routes,
  `GET /v1/geofence-events` and `POST /v1/push-subscriptions`, neither of which returns a coordinate,
  and it is never written to a log, a notification or a rendered string. What does **not** bound it is
  where it is kept. **SECRET-3** is the phase that fixes that, for both credentials at once, which is
  why they live in one place.
- **No enrollment flow.** Both tokens are pasted in by hand from `tracker enroll` and
  `tracker add-viewer` output. **SECRET-3**.
- **Alerts are received over FCM only.** A deployment configured for UnifiedPush will send, and this
  client has no distributor path, so nothing would arrive. That is *said* rather than left silent: the
  app reports "this server sends through a push backend this app cannot receive from", and the in-app
  crossing list still shows every crossing. The UnifiedPush receive path is deferred, partly because
  whether a distributor carries the same collapse and pending semantics as FCM is an open research
  question and this phase asserts nothing about it.
- **A Firebase project is not part of this repo, and cannot be.** `google-services.json` is
  deployment-specific and is not committed; the `com.google.gms.google-services` Gradle plugin is not
  applied. So the module builds, lints and unit-tests with no Firebase project anywhere, which is how
  the gate runs it - and an app built that way has no registration token at runtime and says so
  (reason R1) rather than failing to start. A deployment that wants push adds both in its own build.
- **A phone that forgets its own previous routing address leaves a stale endpoint behind.** A rotated
  FCM token is a new address, and registration is idempotent on `(provider, address)`, so the app
  names the address it is replacing and the server removes exactly that one. A reinstall or a restore
  onto another handset loses that record, so the old row survives until the backend reports the
  address unregistered - which nothing acts on yet. It wastes a send to a dead address; it does not
  double-notify a live phone.
- **The push is never the record.** FCM stores four collapsible messages per device, one per collapse
  key, and tracker's key is per (device, Place) - so a phone off the network past four distinct pairs
  has lost the rest, permanently. The in-app crossing list, read back from your own server, is the
  record. The server counts the surplus as `beyond-collapse-bound` and never as delivered.
- **Static cadence.** 60 s target, 25 m displacement filter, 2 min batching — the same whether the
  phone is parked or on a motorway. **C4** makes it adaptive.
- **No map.** **C5**.
- **Google Play services required.** `FusedLocationProviderClient` is Play services, not AOSP; there
  is no `LocationManager` fallback, so a fully degoogled phone cannot run this client today. FCM adds
  no *new* platform requirement for the same reason - the client was already tied to Play services -
  which is why the receive path built here is the FCM one.
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
