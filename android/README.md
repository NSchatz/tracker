# tracker — Android client

The native **Kotlin / Jetpack Compose** client for tracker.

As of **C2** it collects and it **does not lose what it collects**: a **foreground service** takes
continuous fixes from the **fused location provider** behind the **two-step background-location
permission flow**, writes each one to a **durable on-disk queue**, and a **WorkManager** job
constrained to `NetworkType.CONNECTED` drains that queue into the server's already-shipped
`POST /v1/fixes`. Going offline now delays reporting instead of losing it. It does **not** yet store
its token securely (C3), adapt its cadence to save battery (C4), or show a map (C5).

As of **REBOOT-1 (device half)** it also **comes back after a reboot**: a phone whose operator asked
it to report starts reporting again with nobody touching it, every case where it cannot is visible in
the app instead of silent, and each transition into collecting sends **one position exempt from the
25 m displacement filter** so that "it restarted" is distinguishable from "it did not". Steady-state
collection is unchanged - the same 60 s target, the same 25 m filter, the same durable queue with the
same bound. A **force-stopped** app is still not covered and cannot be: see *Known limitations*.

## What exists

| | |
|---|---|
| **Collection** | `collect/LocationCollectionService` - a foreground service, `type=location`, with the mandatory ongoing notification. Continuous updates from `FusedLocationProviderClient`. One live instance IS one transition into collecting, which is what makes a duplicate boot signal collapse to one session without latching per boot. |
| **Restart after boot** | `collect/BootCompletedReceiver` - `ACTION_BOOT_COMPLETED` only, and it starts nothing on its own judgement: `collect/BootStartDecision` (pure) decides from the persisted intent and the granted permission, and every refusal writes a recorded reason. |
| **Operator intent** | `collect/CollectionIntent` + `collect/CollectionState` - the operator's last explicit choice, persisted, never guessed, and written only by an explicit action (the button, or the notification's Stop). Distinct from whether collection is running right now. |
| **Restart position** | `collect/FilterExemption` + `collect/CollectionRequests` - one position per transition, exempt from the 25 m filter, taken at or after the transition, delivered however long it takes. A second request that is removed the instant it yields one, so the steady-state stream and its filter are never touched. |
| **Recorded reason** | `collect/RecordedReason` - one durable, human-readable reason at a time: a refused or failed start, an unconfigured server, an unreadable intent, or a restart position that could not be taken while collection runs. Shown beside the running state whichever state that is. |
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
- **What a boot does**, exhaustively (`BootStartDecisionTest`): three intent readings times three
  permission capabilities, all nine. That intent ON with background location granted starts
  collection; that a foreground-only or absent grant refuses and says which permission is missing,
  because Android will not create a `location` foreground service from the background without
  `ACCESS_BACKGROUND_LOCATION` and a boot receiver is the background; that intent OFF starts nothing
  and records **no** reason in any permission state; and that a missing or unrecognised stored intent
  is never guessed into a value.
- **The persisted intent's read** (`CollectionIntentCodecTest`): that `"ON"`, `" on"`, `"true"`,
  `"1"` and `""` are all *unreadable* rather than being folded into a value the operator never chose.
- **The filter exemption** (`FilterExemptionTest`): that it opens at a transition and accepts one
  position taken at or after it; that a position the provider took **before** the transition is
  ignored and does **not** spend the exemption; that it covers exactly one position however many
  arrive in a burst; that the five-minute mark reports and changes nothing else, so a position
  obtained twenty minutes later is still exempt; that collection stopping closes it and delivers
  nothing retrospectively; and - the case a per-boot latch fails - that turning collection off and on
  again gets a fresh exemption and its own restart position.
- **That steady state is left alone** (`CollectionRequestsTest`): that the collection stream still
  carries the pin's 60 s interval and 25 m displacement filter, that the exemption is one request
  with a zero filter rather than a relaxation of the shared policy, and that once the restart
  position is taken nothing further is exempt - so a stationary device falls silent again
  immediately, as it does at the pin.
- **The restart position in the queue** (`RestartPositionQueueTest`): that at the bound it is
  evicted under the existing oldest-first policy exactly like any other entry and the eviction is
  counted, that below the bound it is kept in order ahead of everything measured after it, and that a
  restart position sharing an instant with a sample collapses to one entry on the same
  `(device_id, ts)` identity the server dedups on.
- **What the screen shows** (`CollectionPresentationTest`, `RecordedReasonTest`): that the running
  state follows the **live** signal and never the stored intent, so a force-stopped app opens showing
  not running; that a reason is presented alongside that state whether it reads running or not; that
  a device whose intent is simply off presents nothing; and that a start which succeeds clears a
  refusal but not the case that exists precisely while collection runs.

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
- **That `ACTION_BOOT_COMPLETED` is delivered at all**, when it arrives relative to the lock screen,
  and whether a given OEM skin delivers it after its own "optimisation". The gate proves what the app
  *decides* when the broadcast arrives; that it arrives is the platform's business and is an open
  question no claim here rests on. Every reboot procedure below therefore unlocks the device once, and
  no verdict depends on what happened before that unlock.
- **That the fused provider produces a position at all after a restart**, or how long it takes. The
  exemption is proved above; a phone with no view of the sky is a real state the app reports rather
  than a failure it can prevent.

#### Why there are no instrumented tests in this phase

The roadmap sketched instrumented tests with `LocationManager` mock providers for C1. There are none,
for two reasons, in order of importance:

1. **They would not prove the thing that matters.** An instrumented test grants permissions with
   `GrantPermissionRule`, which hands them over programmatically. That bypasses the entire two-step
   flow — the dialog, the settings round-trip, the Android 11 behaviour change — which *is* the risky
   part of C1. A green instrumented test would say "permissions we granted ourselves are granted".
   Mock providers have the same shape of problem: they prove the app can read a location the test
   injected, not that the fused provider delivers one on a real phone under Doze.
2. **The CI environment cannot run them.** There is no `/dev/kvm` in this container, so a
   hardware-accelerated emulator is unavailable, and no emulator or system image is installed.

So the pure logic is unit-tested for real, and the device behaviour is an operator check. Adding an
instrumented suite that only restates its own fixtures would grow the gate while proving nothing —
the exact trade this repo refuses elsewhere when it forbids `t.Skip` in the Go tests.

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

#### The device checks for the reboot behaviour (REBOOT-1)

**Nine checks, V1 to V9, and every one of them needs a real handset with a secure lock screen.** A
green `make check` is not evidence for any of them and must never be offered as one - the gate is
unit tests, and no runner here holds a phone. The results go in [the results table](#reboot-results)
below, in this repo, as part of the change that claims them.

Read these three rules first. They apply to every row and they are what keeps a correct build from
being failed by the weather.

**1. Unlock once, then leave the app alone.** Every reboot procedure requires the operator to unlock
the device once after the reboot and then return to the launcher, touching nothing in the tracker
app. This is deliberate: nothing here asserts when the platform delivers the boot broadcast relative
to the lock screen, and no verdict may depend on it. Whether anything arrived *before* the unlock is
worth recording as an observation and is never a pass condition. An unlock is not "human action"
starting collection - it starts and resumes nothing.

<a id="stationary-baseline"></a>
**2. The stationary baseline (rows V1, V8 and V9).** Those rows attribute a stored position to the
restart position on the strength of a device that did not move, and the 25 m filter operates on
displacement **as reported by the provider**, which indoors can jitter past 25 m with the phone
sitting still. So each of those rows opens with a measurement, not an assumption:

> With collection already RUNNING and the device parked where it will stay for the whole run,
> observe 10 minutes and record how many positions the server stores for it. The run is VALID only
> if that count is **0**. A non-zero baseline means this spot reports jitter past the filter, and the
> run is VOID.

**3. VOID is not FAIL, and never becomes one.** A run is VOID if its baseline is non-zero, if the
device is moved beyond 25 m before the verdict, or where a row says so. A VOID run is **repeated** -
another time of day, or a location with a clearer view of the sky. If a row cannot produce a valid
run in three attempts, **do not record a fail**: record the observed baseline and raise it for human
triage. A recorded fail blocks the change; provider noise must not be able to do that.

Before starting: the server is reachable, the device is enrolled with a token that works, background
location is granted ("Allow all the time"), and `GET /v1/devices/{id}/history` for this device is
readable so stored positions can be counted.

| row | what it checks | procedure |
|---|---|---|
| **V1** | collection restarts after a boot with no human action; intent OFF stays off; the restart position | On a phone with a secure lock screen. (1) Take the [stationary baseline](#stationary-baseline); a non-zero baseline VOIDs the run. (2) With collection ON, record the server's newest stored `ts` for this device, and reboot. (3) Unlock once and return to the launcher; do not open or otherwise touch the tracker app. (4) The device stays within 25 m of where it sat from the reboot to the verdict - park it on a desk; a run in which it moved further is VOID. (5) **PASS** iff the server has stored at least one position for this device with `ts` after the reboot, within 10 minutes of the unlock. FAIL otherwise. The zero baseline is what makes that position the restart position rather than jitter through the ordinary filter. (6) Record whether anything arrived before the unlock, as an observation only. (7) Repeat with collection turned OFF before the reboot: **PASS** iff nothing arrives within 10 minutes of the unlock, and the app, when then opened, shows collection as **not running with no reason presented**. |
| **V2** | the stopped state, and that opening the app starts nothing | **Precondition, and it is the whole point of the row: collection intent must be ON and collection must be RUNNING immediately before the force-stop.** Confirm both on the screen first (it reads "Running.") - a force-stop of an app whose collection was already off passes this row without exercising anything. Then: force-stop the app from Settings, reboot, unlock once, and confirm nothing arrives within 10 minutes. Open the app. **PASS** iff it shows collection as **not running** - not "Running." on the strength of the stored setting - and iff opening it started nothing: nothing arrives in the following 2 minutes while the app sits open and the device stays still. |
| **V3** | the refused start: missing permission, and a platform refusal | Revoke background location, reboot, unlock once. **PASS** iff no collection session is running, the app has not crashed, nothing was delivered, and when the app is opened it shows **not running** AND presents a recorded reason **naming the missing permission**. Then, separately, provoke a platform refusal of the start if the test device permits it. **PASS** iff the app has not crashed and the presented reason names **that** case instead. |
| **V4** | no lost position, no duplicate, and the same cadence as a hand-started run | Exact counting, so "no lost position" is a number. (1) With collection running and the device moving, make the server unreachable from the phone and wait until the app's `queued` counter reads `Q`, with `5 <= Q <= 100`. At that instant, call it `T0`: record `Q`, and record `S`, the server's stored position count for this device **with `ts <= T0`**. (2) Reboot, unlock once, do not touch the app. (3) Restore connectivity and wait for `queued` to return to 0. (4) **PASS** iff the server's stored count for this device **with `ts <= T0`** is now exactly `S + Q`, AND no `(device_id, ts)` pair is stored twice for it. **Counting only `ts <= T0` is deliberate and is not a detail to drop**: positions the client captured between the counter read and the power-off, and the restart position, all carry a later `ts`, so a conforming build cannot be made to read as a duplicate by them. A count below `S + Q` IS the lost-position failure; a repeated `(device_id, ts)` IS the duplicate failure. (5) Cadence: walk continuously for 10 minutes on the restarted run, and for 10 minutes on a run started by hand on the same device. **PASS** iff neither stored count is 0 and the two differ by no more than 2 positions or 20% of the larger count, whichever allowance is greater. This is looking for a cadence that CHANGED - a doubled or halved interval shows up as roughly half or twice the count - and must not fail a build over a first fix that took 45 s or one dropped sample. |
| **V5** | no position available: the app says so, keeps collecting, and still delivers later | Put the device where the provider cannot obtain a position (airplane mode with location services on, or a shielded spot), then cause a transition into collecting. **PASS** iff ALL of: after 5 minutes the app is still collecting and has not crashed; opening the app shows collection as **RUNNING** and presents alongside it a reason naming that no restart position could be taken - **a build that shows running and presents nothing FAILS this row**; and then, after the provider is restored **without any further transition**, a position taken at or after the original transition is stored on the server even though the device did not move, and the reason is no longer presented. That last step is the check that the exemption stayed open past the five-minute mark. |
| **V6** | an undeliverable restart position blocks nothing | With the server unreachable from the phone, cause a transition into collecting. **PASS** iff the transition completes (the app shows collection running) in the same time it takes with the server reachable, the app does not crash, the `queued` counter rises by at least 1, and when connectivity is restored a position taken at or after that transition is stored on the server. This row deliberately does NOT drive the queue to its 5,000 bound: the eviction behaviour there is unchanged and is asserted by `RestartPositionQueueTest`, not by a multi-day device run. |
| **V7** | an intent that cannot be read is never guessed | With collection ON, corrupt or remove the persisted intent, then reboot and unlock once. **The documented means**, on a debuggable build: `adb shell run-as com.nschatz.tracker`, then edit `shared_prefs/tracker_client.xml` and either delete the `collection_intent` entry (the *missing* case) or set its value to something that is not `on` or `off`, such as `maybe` (the *unreadable* case). **PASS** iff nothing is delivered within 10 minutes and the app, when opened, shows collection as **not running** and presents a reason naming the unreadable or absent setting. |
| **V8** | a duplicate boot signal collapses to one, and a later transition still gets its own position | Two parts, **both** required for a PASS. Same [stationary baseline](#stationary-baseline) and 25 m rule as V1, or the run is VOID. (1) *Duplicate signal*: cause the boot signal to be delivered a second time after the boot-completed one, by whatever means the test device offers (on a debuggable build, `adb shell am broadcast -a android.intent.action.BOOT_COMPLETED -p com.nschatz.tracker`). **PASS** iff exactly one collection session is running afterwards and exactly **one** position taken after that boot is stored. (2) *Second transition in the SAME boot*: without rebooting, turn collection OFF in the app, wait 1 minute, turn it ON again, and leave the device parked. **PASS** iff a further position, taken at or after that second transition, is stored within 10 minutes even though the device did not move. A build that records a boot id and suppresses further restart positions until the next boot passes part 1 and FAILS part 2 - which is exactly what this part exists to produce. |
| **V9** | steady state after the restart position is unchanged | Machine half: `make check` stays green, and the client suite carries the three cases named under *Proven by the gate* (the position after the restart position is under the displacement filter; the reporting interval is the pin's; at the bound the restart position is evicted oldest-first like any other entry). Operator half: continuing the V1 run, whose baseline was 0, leave the device parked for **30 minutes** after the restart position is stored; moving it beyond 25 m VOIDs the run. **PASS** iff the server has stored **exactly one** position for this device with `ts` after the reboot at the end of those 30 minutes. If two or more are stored, read them before recording a verdict: extras **within** 25 m of the first *as reported* are positions the unchanged filter would have suppressed, so that IS the exemption leaking and the row records **FAIL**; extras **beyond** 25 m as reported, on a device that did not move, are provider jitter the unchanged filter legitimately passes, so the run is **VOID** and is repeated. |

<a id="reboot-results"></a>
##### Recorded results

Nine rows, nine results. Each states **pass or fail**, the **date**, the **device model** and the
**Android version** it was run on. A VOID run is not a result: it is repeated. **A missing result is
a landing blocker** - a green `make check` alongside empty rows is exactly the state this table
exists to make visible, and it is not a state anything may be landed in.

| row | result | date | device model | Android version |
|---|---|---|---|---|
| V1 | *not run* | | | |
| V2 | *not run* | | | |
| V3 | *not run* | | | |
| V4 | *not run* | | | |
| V5 | *not run* | | | |
| V6 | *not run* | | | |
| V7 | *not run* | | | |
| V8 | *not run* | | | |
| V9 (operator half) | *not run* | | | |

**Why they are empty.** The change was built in a container with no handset, so none of the nine
could be performed and none is guessed at. That is the intended outcome rather than an oversight:
the alternative is landing a change whose every criterion is unverified. Whoever runs them fills in
the row and nothing else - the procedures above are the whole instruction, and a row that cannot
produce a valid run in three attempts is raised for triage instead of being written down as a fail.

---

## Known limitations after C2 and the device half of REBOOT-1

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
- **A force-stopped app does not come back at a reboot, and nothing here can make it.** An app in the
  **stopped state** is not delivered `ACTION_BOOT_COMPLETED` at all until a user action removes it
  from that state, so a phone whose app was force-stopped from Settings simply does not run the boot
  receiver. This is **surfaced, not solved**: the app's running state is read from a live signal
  rather than from the stored setting, so opening it shows collection as **not running**, and when
  the intent is on with nothing recorded to explain the silence it says exactly that. The remedy is
  a human opening the app and starting collection.
- **The same is true of an OEM battery-killer that stops the app** (Samsung, Xiaomi and friends).
  A stopped app is a stopped app whoever stopped it, and it looks identical from inside. There is no
  in-app fix; there is only saying so.
- **The restart position is one position, not a heartbeat.** A phone that cannot get a fix - a car
  park, a shielded room - delivers nothing until it can, and to the server it is indistinguishable
  from one that never restarted. The app says so on the device after five minutes; nothing tells the
  server. Closing that gap is the server half of REBOOT-1, not this one.
- **A restart position that is undeliverable is queued like anything else, and evicted like anything
  else.** At the 5,000 bound the oldest entries go first, and on a device that has been offline long
  enough to fill the queue that will be the restart position. It is not pinned or reserved: keeping
  an old proof-of-restart by sacrificing a newer position would trade the answer to "where are they
  now" for evidence about a restart that is by then hours old.
- **The intent is a preference file, so it is as durable as one.** It is written with `commit()`
  rather than `apply()` so a power cycle moments after the operator taps Stop cannot lose it, but it
  lives in the same plaintext `SharedPreferences` as the token and can be edited with root or by
  `run-as` on a debuggable build. A value that is neither `on` nor `off` is treated as unreadable and
  is never guessed - the app says it could not read the setting and starts nothing.

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
