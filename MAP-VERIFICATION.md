# MAP-VERIFICATION.md - the browser map, verified by eye

> ## SUPERSEDED AS EVIDENCE. Kept as a record.
>
> **This document is no longer evidence for any clause `make verify-ui` grades.** The harness this
> file said it could not have now exists: `cmd/uiverify` drives a real browser engine over the
> production `/map` and `/static` handlers, in CI and on a laptop, and the clause record is
> `FRONTEND-CONVENTIONS-RECORD.md`. An eye pass never satisfied F2 of the umbrella's frontend
> conventions and it does not satisfy it now; the difference is that there is finally something that
> does.
>
> Nothing here has been deleted. What was observed on 2026-08-24 was really observed, and deleting an
> observation is not the same as superseding it. The table below says, row by row, which machine
> assertion now grades the property the row was standing in for.
>
> | this document's row | now graded by | entry point |
> |---|---|---|
> | AC24 each of the four states names itself in words | `AC1-colour-free`, `AC5-absence` | `make verify-ui` |
> | AC25 a `no-position` device is present, unlocated, and has no marker | `AC5-absence` | `make verify-ui` |
> | AC26 the map renders the value the server sent, whatever the browser clock says | still this document ONLY | (see below) |
> | AC27 connection health is a separate axis from device state | `AC8-stale` | `make verify-ui` |
> | AC28 one bad event is one bad event | `AC7-one-bad-event` | `make verify-ui` |
> | AC29 an empty family says so | `AC9-three-states` | `make verify-ui` |
> | AC30 a refused credential says so | `AC9-three-states` | `make verify-ui` |
>
> **The one row still carried by an eye is AC26**, the browser-clock skew, and it is named here rather
> than quietly dropped. `make verify-ui` does not replace the whole `Date` object in the page, so the
> claim "a browser whose clock is hours off still renders exactly the token the server sent" remains
> an observation from 2026-08-24 against a page whose rule has not changed. It is a smaller claim than
> it looks - the page contains no clock arithmetic at all, which `make check-go` can see - but it is
> not machine-graded, and no row of `FRONTEND-CONVENTIONS-RECORD.md` claims it is.
>
> **Do not add a row to section 5 for a clause the record maps to a machine assertion.** A manual pass
> beside a machine one is confusing rather than reassuring: it invites a future reader to treat the
> weaker of the two as a second opinion.

The map at `GET /map` is one vendored HTML page with no build step, and this repository carries no
JavaScript test tooling. Adding a headless-browser harness would be a new external dependency in this
submodule, which is an operator decision and not one the presentation-state change may take. So the
map criteria are verified two ways, neither of which adds one:

1. **Whatever the server controls is asserted in Go** - `internal/server/map_test.go`. That the served
   page carries all four state tokens and the two strings a viewer must be able to tell apart from
   "still connecting"; that the non-conforming events the map must reject are well-formed SSE and are
   **constructed test-side**; and that the production server has **no path** to emit an event type
   outside the three or a presentation token outside the four. `internal/server/presentation_read_test.go`
   and `presentation_stream_test.go` assert that no unlocated payload on any surface carries a
   coordinate key, and that both surfaces emit the identical three-key object.
2. **The rendered outcome is confirmed by an eye** - this document. It is the procedure, and below it
   the record of what was actually seen.

**What "observed" means here, and it is the whole point.** A step is RUN when a person, or an agent
driving a real browser, loaded the page against a running stack and read the result off the screen.
**Predicting an outcome from the code is NOT an observation and must never be written as one.** A row
in the record below is either an outcome somebody read, naming what they read it off, or it says
`NOT YET OBSERVED`. If the executing role cannot run a browser, it files a blocked report proposing a
harness as an operator ask; it does not fill this table in.

> **An agent with no display can still run this.** A headless Chromium driven over the DevTools
> Protocol loads the page, runs its JavaScript and hands back the RENDERED DOM and a screenshot,
> which is reading the result off the screen by a slower road. One way that needs nothing from this
> repository: run `chromedp/headless-shell` as a container on the same compose network as the stack
> (so the page is reachable at `http://tracker:8080/map`), publish its DevTools port, and drive it
> from a throwaway CDP client OUTSIDE this checkout - `chromedp.Navigate`, then read `innerText` of
> `#status` and `#panel`, `outerHTML` of `#located` and `#unlocated`, the `.leaflet-tooltip` nodes,
> and `window.trackerMap.snapshot()`. Attach to a persistent page target rather than a fresh tab per
> command, so the page survives between steps: several steps below stop a container while the map
> stays open. **The harness lives outside this repository and this repository gains no dependency on
> it** - that is the constraint, not "no browser".

---

## 1. Bring the stack up

**Every command in sections 1 to 4 is complete and uses the one project name `tracker-mapverify`.**
Run them from the root of a `tracker` checkout at the commit under review; nothing here depends on a
stack somebody else left running.

The windows are set deliberately wide so that a device seeded into a state STAYS in it for the length
of an unhurried observation session - under the 120/900 defaults the `live` fixture would age out in
two minutes and the four-state screen would be unreproducible.

```bash
cd /path/to/tracker                          # the checkout at the commit under review

export TRACKER_DB_PASSWORD=choosesomethingalphanumeric   # no @ / : # % ? - it goes into a DSN
export TRACKER_LIVE_WINDOW_SECONDS=3600      # 1 hour  -> at or under this age, `live`
export TRACKER_STALE_WINDOW_SECONDS=21600    # 6 hours -> past this, `stale`

docker compose -p tracker-mapverify up -d --build
```

> **If your shell cannot export** (a sandboxed agent session, for instance), put those same three
> `NAME=value` lines in a file **outside the repository** - say `~/tracker-mapverify.env` - and add
> `--env-file ~/tracker-mapverify.env` to **every** `docker compose` line below, plus
> `-f docker-compose.yml --project-directory .` if you are not running from the checkout root. Delete
> that file when you tear the stack down: it carries a database password, synthetic or not.

Wait for health, and confirm the server picked the windows up (this is AC6's start-up log line, and
it is worth reading before anything else - a typo here would make every state below wrong):

```bash
until curl -sf localhost:8080/healthz; do sleep 2; done; echo
docker compose -p tracker-mapverify logs tracker | grep 'presentation windows'
#   {"time":"…","level":"INFO","msg":"presentation windows",
#    "TRACKER_LIVE_WINDOW_SECONDS":3600,"TRACKER_STALE_WINDOW_SECONDS":21600,"unit":"seconds"}
```

## 2. Seed the fixture families

Two families: one holding a device in each of the four presentation states, and one with no devices
at all (which is AC29's case, and is NOT the same thing as a family whose devices have never
reported). Every token and id below is printed **once**; capture each as you go.

```bash
C="docker compose -p tracker-mapverify exec -T tracker tracker"

$C create-family -name "Verification family"
#   prints "family created / id: <uuid>"  ->  export FAM=<uuid>

$C enroll -family "$FAM" -name a-never-reported   # enrolled, never reports: no-position
$C enroll -family "$FAM" -name b-live
$C enroll -family "$FAM" -name c-recent
$C enroll -family "$FAM" -name d-stale
#   each prints "id: <uuid>" and a DEVICE token. Keep the four ids - section 3 needs them as
#   ID('b-live') and friends. The device tokens are NOT used here; the fixes are seeded in SQL.

$C add-viewer -family "$FAM" -email map@example.test -name "Map viewer"
#   prints a VIEWER token  ->  export VIEWER=<token>

$C create-family -name "Empty family"
#   prints an id  ->  export EMPTY=<uuid>
$C add-viewer -family "$EMPTY" -email empty@example.test -name "Empty viewer"
#   prints a VIEWER token  ->  export EMPTY_VIEWER=<token>
```

Now the fixes. Both clocks are chosen here on purpose: `received_at` is what the presentation value
is computed from, and the ingestion route stamps it with the server's own clock, which is right in
production and useless for putting a device into a state on demand. The device names are looked up in
the statement, so no id has to be copied.

```bash
PSQL="docker compose -p tracker-mapverify exec -T postgis psql -U tracker -d tracker -v ON_ERROR_STOP=1"

$PSQL <<'SQL'
INSERT INTO fixes (device_id, ts, location, received_at)
SELECT d.id, now() - i.age, ST_Point(i.lon, i.lat, 4326)::geography, now() - i.age
FROM (VALUES
    ('b-live',   interval '5 minutes',  12.4964, 41.9028),   -- age 300s    <= 3600 -> live
    ('c-recent', interval '2 hours',     9.1900, 45.4642),   -- age 7200s   in band -> recent
    ('d-stale',  interval '12 hours',    2.3522, 48.8566)    -- age 43200s  > 21600 -> stale
  ) AS i(device_name, age, lon, lat)
JOIN devices d ON d.name = i.device_name
JOIN families f ON f.id = d.family_id AND f.name = 'Verification family';
SQL
#   expect: INSERT 0 3
```

> `fixes` is partitioned by month and the server provisions the current month and the next one at
> start-up, so every `ts` above must fall in the current month. If the local clock is within 12 hours
> of the start of a month, use `interval '2 hours'` for `d-stale` and set
> `TRACKER_STALE_WINDOW_SECONDS=3600` instead; the four states are unaffected.

`b-live` stays `live` for one hour from seeding. If a session runs long, refresh its last contact
(this is a new fix arriving, exactly as a phone reporting would be) and it becomes `live` again:

```bash
$PSQL <<'SQL'
INSERT INTO fixes (device_id, ts, location, received_at)
SELECT d.id, now(), ST_Point(12.4964, 41.9028, 4326)::geography, now()
FROM devices d
JOIN families f ON f.id = d.family_id AND f.name = 'Verification family'
WHERE d.name = 'b-live'
ON CONFLICT (device_id, ts) DO NOTHING;
SQL
```

Confirm the server agrees before opening a browser. This is the reference the rendered labels are
compared against - if these four values are not what you expect, the map is not what is wrong:

```bash
curl -s -H "Authorization: Bearer $VIEWER" localhost:8080/v1/positions
```

```json
[{"device_id":"…","device_name":"a-never-reported","presentation":"no-position"},
 {"device_id":"…","device_name":"b-live","lat":41.9028,"lon":12.4964,"ts":…,"received_at":…,"presentation":"live","last_contact_at":…},
 {"device_id":"…","device_name":"c-recent","lat":45.4642,"lon":9.19,"ts":…,"received_at":…,"presentation":"recent","last_contact_at":…},
 {"device_id":"…","device_name":"d-stale","lat":48.8566,"lon":2.3522,"ts":…,"received_at":…,"presentation":"stale","last_contact_at":…}]
```

Four entries, one per device, in the total order (folded name ascending), and the never-reported one
carrying **exactly three keys**. And the empty family:

```bash
curl -s -H "Authorization: Bearer $EMPTY_VIEWER" localhost:8080/v1/positions
#   []
```

Then load the map. **The URL is `http://localhost:8080/map`**; paste `$VIEWER` into the token field
and press Watch (or open `http://localhost:8080/map?token=$VIEWER`, which watches immediately).

## 3. The steps, and what a correct outcome looks like

Several steps hand the page an event that the **production server has no way to emit** - an
unparseable body, an event type outside the three, a presentation token outside the four - or a
`GET /v1/positions` response arriving after the stream has already repainted a device. Those inputs
are constructed **by the verifier**, in the browser console, and handed to `window.trackerMap`, which
is the page's verification hook. `onStreamEvent` is the same function every real wire event is routed
through and `applyPositions` is the same function the real positions response goes through, so what
is exercised is the page's real routing, not a second path built for the test. `snapshot()` returns
the page's render state for reading back.

In the steps below, `ID('b-live')` means that device's `device_id` as printed by `tracker enroll` or
by the `curl` above.

**Run the steps IN ORDER.** They are sequenced, not independent: AC24 to AC26 need the four-state
fixture intact, AC28(c) then purges `c-recent` and leaves it `no-position` for good, and AC27 stops
containers. Press **Watch** between groups to repaint from the server if a step left the page in a
state the next one does not want.

### AC24 - each of the four states names itself in words

1. With the map watching, read the **rendered label of every device**: the permanent tooltip beside
   each marker, and each row of the **Devices** panel on the left.
2. Correct outcome: four devices are listed, and each label contains the state word the server sent -
   `no-position`, `live`, `recent`, `stale` - one each, matching the `curl` output above word for
   word. The distinction survives with colour removed, because it is text.

### AC25 - a `no-position` device is present and unlocated, and has no marker anywhere

1. Read the **Present, not located** section of the panel.
2. Count the markers on the map. In the console: `window.trackerMap.snapshot().markerIds`.
3. Correct outcome: `a-never-reported` is listed under **Present, not located** with the state word
   `no-position`; `markerIds` holds exactly the three located devices and does **not** contain
   `ID('a-never-reported')`; and there is no marker at `0,0` or at the map centre. Panning to
   latitude 0, longitude 0 shows nothing.

### AC26 - the map renders exactly the value the server sent, whatever the browser clock says

1. In the console, skew the page's clock six hours FORWARD:
   `window.__realNow = Date.now; Date.now = function () { return window.__realNow.call(Date) + 6*3600*1000; };`
2. Press **Watch** again to repaint the whole map from the server.
3. Read the four labels. Correct outcome: identical to AC24's - `d-stale` still reads `stale`, and no
   device's word has changed.
4. Repeat with the clock six hours BACKWARD (`- 6*3600*1000`). Same outcome.
5. Restore: `Date.now = window.__realNow;` and press **Watch**.

### AC27 - connection health is a separate axis from device state

Three antecedents, and all three must be seen; each was a separate hole.

**(a) An `error` event on a connection that is still open.** With the map live:

1. `docker compose -p tracker-mapverify stop postgis`
2. Within a second or two the server can no longer read the family and sends `event: error`.
3. Correct outcome: the status line reads **connection interrupted: ...** and says the states below
   are no longer confirmed; **every** device row carries `(unconfirmed)`; no device is presented as
   currently `live` - `b-live` reads `live (unconfirmed)`, which is the annotation, not the token
   being replaced. Each device still shows the exact word the server last sent. The connection is
   still **open** - `window.trackerMap` aside, `source.readyState` is `1` - because this is the
   error branch, not a drop.
4. `docker compose -p tracker-mapverify start postgis`, and **change nothing about the family**.
   Correct outcome: within a poll or two of the database accepting connections again, every
   `(unconfirmed)` mark disappears and the status returns to `live` - **without** any device having
   moved or aged. That is the server re-stating the family on the first successful poll after an
   `error` (SPEC.md, "When the server cannot read"); an implementation that only sent what CHANGED
   would leave this page unconfirmed forever against a healthy server, which is the bug this step
   exists to catch.

**(b) The connection drops.** With the map live: `docker compose -p tracker-mapverify stop tracker`.
Correct outcome: the status reads `connection interrupted: the live connection dropped; reconnecting`
and every row is `(unconfirmed)`. `docker compose -p tracker-mapverify start tracker` clears it.

**(c) The connection fails to open.** Leave the server stopped from (b) and press **Watch** on the
page that is already loaded. (Do NOT reload first: `/map` is served BY tracker, so with the server
down there is no page to load - a fresh navigation gets a browser error page and observes nothing
about this criterion.) Correct outcome: the status says the connection could not be opened (or that
the server could not be reached); it never sits on `connecting...` implying progress, and it still
says so several seconds later. Then `docker compose -p tracker-mapverify start tracker`.

### AC28 - one bad event is one bad event, and each good one has its own branch

**(a) `position`** - already observed in AC24: a marker is placed and its state rendered.

**(b) `presentation` carrying a LOCATED payload updates the state in place.** In the console:

```js
var before = window.trackerMap.snapshot();
window.trackerMap.onStreamEvent('presentation', JSON.stringify({
  device_id: ID('b-live'), device_name: 'b-live', presentation: 'recent', last_contact_at: 1
}));
window.trackerMap.snapshot();
```

Correct outcome: `b-live`'s label now reads `recent`; its marker is in the **same place** and still
present - `markerIds` is unchanged from `before.markerIds`, and the marker did not move on screen.

**(c) `presentation` carrying an UNLOCATED entry removes the marker.** Through the real server, by
purging a device's last fix while the map is open:

```bash
$PSQL -c "DELETE FROM fixes WHERE device_id IN (
            SELECT d.id FROM devices d JOIN families f ON f.id = d.family_id
            WHERE f.name = 'Verification family' AND d.name = 'c-recent')"
```

Correct outcome: within a second or two `c-recent`'s marker **disappears** from the map, it moves
into the **Present, not located** list reading `no-position`, and
`curl -s -H "Authorization: Bearer $VIEWER" localhost:8080/v1/positions` in the same window returns
the **identical three-key object** for it - `{"device_id":…,"device_name":"c-recent","presentation":"no-position"}`
with no `lat`, `lon`, `ts`, `received_at` or `last_contact_at`. (That is AC28 and AC32 jointly. The
production purge drops a whole monthly partition rather than deleting rows; the partition-drop path
is covered by the Go test `TestStreamPurgeToNoPosition`, and both leave the device holding no fix,
which is the state the map reacts to.)

**(d) `error`** - already observed in AC27(a).

**(e) The three rejections.** Run each in the console, reading `snapshot()` after each one:

```js
window.trackerMap.onStreamEvent('presentation', '{not json at all');
window.trackerMap.onStreamEvent('teleport', JSON.stringify({device_id: ID('b-live'), device_name: 'b-live', presentation: 'live'}));
window.trackerMap.onStreamEvent('presentation', JSON.stringify({device_id: ID('b-live'), device_name: 'b-live', presentation: 'offline', last_contact_at: 1}));
```

Correct outcome for each: the map is **not blanked**; every device already rendered keeps its marker
and its state word; `rejectedCount` increases by one and the bar shows a rejection notice naming what
was refused; and the stream keeps consuming - a subsequent real event (wait for one, or press Watch)
still lands.

### AC29 - an empty family says so, and a late positions response never reverts the stream

**(a) The empty family.** Reload `/map`, paste `$EMPTY_VIEWER`, press Watch. Correct outcome: the
panel shows **no devices in this family**, explicitly - not an empty map indistinguishable from one
still connecting.

**(b) The stream wins.** Back on `$VIEWER`, with the map live and `b-live` rendered as `live` (press
Watch to reset it after AC28(b)), hand the page a positions response that lands late and disagrees:

```js
window.trackerMap.applyPositions([{
  device_id: ID('b-live'), device_name: 'b-live', lat: 41.9028, lon: 12.4964,
  ts: 1, received_at: 1, presentation: 'stale', last_contact_at: 1
}]);
window.trackerMap.snapshot().devices[ID('b-live')].state;
```

Correct outcome: the rendered label still reads `live` and the snapshot's state is `"live"`. The late
response did not revert a device the stream had already described.

### AC30 - a refused credential says so

1. Reload `/map` and press **Watch** with the token field **empty**.
2. Reload `/map`, type `not-a-real-token`, press **Watch**.
3. Correct outcome, both times: the status reads
   **viewer token refused: the server would not accept this credential** (the first case adding
   `no viewer token was given`, the second `HTTP 401`). It does **not** read `connecting...`, and no
   EventSource is left retrying in the background - `window.trackerMap.snapshot().status` shows the
   refusal and it does not change over the following seconds.

## 4. Tear down

```bash
docker compose -p tracker-mapverify down -v
rm -f ~/tracker-mapverify.env      # only if you used the env-file route in section 1
```

---

## 5. The record

**Commit verified against: `eb58f53c70f1f43aeea15f96d039894cd4ff8ebf`** ("fix(tracker):
S0010-tracker-server-1 - connection health can get better, and the sweep bound holds"), on branch
`sdd/S0010-tracker-server-1`. That is the commit the stack below was BUILT FROM and the commit that
was under the browser. This record is committed on top of it, because a record that names the commit
it was run against cannot also be inside that commit; the only thing between them is this table.

**Actor: the implementer agent for `S0010-tracker-server-1`, fix loop after implementation verdict 1**
(a Claude Code session, non-interactive). **Method: a real browser, driven headlessly.** Chromium
`151.0.7922.109` (`chromedp/headless-shell`, user agent read off `navigator.userAgent` on the page
itself) ran as a container on the stack's own compose network, so the page loaded from the running
server at `http://tracker:8080/map`. It was driven over the DevTools Protocol by a throwaway `chromedp`
client living OUTSIDE this repository, attached to one persistent page target so the map stayed open
across steps that stop and start containers. Every outcome below was read back from the page AFTER its
JavaScript ran - `innerText` / `outerHTML` of the named element, the `.leaflet-tooltip` nodes Leaflet
had drawn, `window.trackerMap.snapshot()`, and a full-page screenshot for each - never predicted from
source. **No dependency was added to this repository**; the driver is scratch tooling that was thrown
away with the container.

**Date: 2026-08-24**, 13:42 to 13:50 UTC.
**Stack:** `docker compose -p tracker-mapverify up -d --build` from this commit, with
`TRACKER_LIVE_WINDOW_SECONDS=3600` and `TRACKER_STALE_WINDOW_SECONDS=21600`. Start-up log read back
first, per section 1: `{"msg":"presentation windows","TRACKER_LIVE_WINDOW_SECONDS":3600,
"TRACKER_STALE_WINDOW_SECONDS":21600,"unit":"seconds"}`.
**Server reference** (`GET /v1/positions`, section 2) before the browser was opened, and it matched
the expected fixture exactly: `a-never-reported` `no-position` with exactly three keys, `b-live`
`live`, `c-recent` `recent`, `d-stale` `stale`, in that order; the empty family returned `[]`.

| criterion | steps run | how the outcome was observed | outcome |
|---|---|---|---|
| AC24 | §3 AC24 steps 1-2: loaded `/map?token=$VIEWER`, waited for the snapshot, read every rendered label | `innerText` of `#panel`; `outerHTML` of `#located` and `#unlocated`; the text of every `.leaflet-tooltip` Leaflet had drawn; screenshot | PASS. Panel read `b-live live` / `c-recent recent` / `d-stale stale` under DEVICES and `a-never-reported no-position` under PRESENT, NOT LOCATED. Marker tooltips read `b-live - live`, `c-recent - recent`, `d-stale - stale`. Each `<li>` also carried `data-state` equal to the word the server sent. All four words are text, so they survive colour being removed |
| AC25 | §3 AC25 steps 1-3: read the unlocated section, counted markers, then centred the map on lat 0 lon 0 at zoom 10 and looked | `innerText` of `#unlocated`; `snapshot().markerIds`; `window.markers[<a-never-reported>]`; a viewport-intersection count over `#map .leaflet-overlay-pane path`; screenshot of null island | PASS. `a-never-reported no-position` listed as present and unlocated. `markerIds` held exactly the three located devices; the never-reported id was absent and `window.markers[...]` for it was undefined. The three markers sat at 41.9028,12.4964 / 45.4642,9.19 / 48.8566,2.3522. With the map showing 0,0, **0** marker shapes fell inside the map viewport - the screenshot is empty ocean |
| AC26 | §3 AC26 steps 1-5, with the WHOLE JS clock replaced (a `Date` proxy, so both `new Date()` and `Date.now()` move), +6h then -6h, pressing **Watch** each time for a full repaint from the server | the page's own clock read back (`new Date().toISOString()`) beside real UTC; then `innerText` of `#panel` and the `.leaflet-tooltip` text; screenshots at both skews | PASS. Page clock read `2026-08-24T19:44:34Z` and then `2026-08-24T07:44:39Z` while real UTC was `13:44`. At both skews the labels were byte-identical to AC24's - `d-stale` still read `stale`, `b-live` still `live`. Restoring the clock and repainting again changed nothing |
| AC27 (a) error event | §3 AC27(a) steps 1-3 on a HEALTHY open connection: `docker compose -p tracker-mapverify stop postgis`, then read the page | `innerText` of `#status`; `outerHTML` of `#located` and `#unlocated`; tooltip text; `source.readyState`; screenshot | PASS. Status read `connection interrupted: tracker cannot currently read this family's data; the states shown are no longer confirmed. Device states below are no longer confirmed.` Every row carried `(unconfirmed)` in its own `<span class="unconfirmed">` BESIDE the state span - `b-live live (unconfirmed)`, tooltip `b-live - live (unconfirmed)` - so each device kept the exact token the server last sent (`data-state` still `live`/`stale`/`no-position`) and none was presented as CURRENTLY live. `readyState` was `1` (OPEN): the error branch, not a drop |
| AC27 (a) recovery | §3 AC27(a) step 4: `start postgis` and change NOTHING about the family, then wait 20s | `innerText` of `#status` and `#panel`; tooltip text; `snapshot().interrupted`; screenshot | PASS. Status returned to `live` (class `live`), `interrupted` was `false`, and NO row carried `(unconfirmed)` - with no device having moved, aged or reported. This is the fix for the implementation verdict's F3: before it, this exact sequence left every row unconfirmed indefinitely against a healthy server (observed that way on `fbb074b` in this same browser, which is why the change was made) |
| AC27 (b) drop | §3 AC27(b): with the map live, `docker compose -p tracker-mapverify stop tracker` | `innerText` of `#status` and `#panel`; tooltip text; screenshot | PASS. Status read `connection interrupted: the live connection dropped; reconnecting. Device states below are no longer confirmed.` Every row `(unconfirmed)`, tooltips `b-live - live (unconfirmed)` and `d-stale - stale (unconfirmed)`, nothing presented as currently live |
| AC27 (c) fails to open | §3 AC27(c): server still stopped, pressed **Watch** on the already-loaded page (a fresh navigation is impossible - `/map` is served by the stopped server) | `innerText` of `#status` immediately, again 8s later; `snapshot().status`; screenshot | PASS. Status read `connection interrupted: the live connection could not be opened. Device states below are no longer confirmed.` It never contained `connecting...` and was identical eight seconds later, so nothing sat there implying progress |
| AC28 (a) position | §3 AC28(a): the snapshot's `position` events at AC24, and later a REAL new fix inserted for `b-live` while the map was open | tooltip text and `window.markers[<b-live>].getLatLng()` before and after | PASS. Markers were placed with their state rendered at snapshot; the later real fix moved `b-live`'s marker from 41.9028,12.4964 to 41.91,12.5 and its label back to `live` |
| AC28 (b) located presentation | §3 AC28(b): handed `window.trackerMap.onStreamEvent` a LOCATED `presentation` payload saying `recent` for `b-live` | `innerText` of `#located`; tooltip text; `getLatLng()` and the tooltip's on-screen `left` before and after; `snapshot().markerIds` compared to the saved before-set | PASS. Label went `b-live live` to `b-live recent` and the tooltip to `b-live - recent`, while `getLatLng()` stayed 41.9028,12.4964, the tooltip's screen position stayed `538`, and `markerIds` was unchanged. Updated IN PLACE: not moved, not removed, not treated as a new fix |
| AC28 (c) unlocated presentation | §3 AC28(c): purged `c-recent`'s last fix through the real server (`DELETE FROM fixes ...`) with the map open, then widened the view over all three seeded coordinates | `innerText` of `#panel`; `outerHTML` of `#unlocated`; `snapshot().markerIds`; `window.markers[<c-recent>]`; a count of `#map .leaflet-overlay-pane path`; screenshot; and `GET /v1/positions` in the same window | PASS. Within one poll `c-recent`'s marker DISAPPEARED - `window.markers[...]` undefined, `markerIds` down to the two survivors, 2 marker shapes drawn over an area that had shown 3, Milan empty in the screenshot - and it moved into PRESENT, NOT LOCATED reading `no-position`. `GET /v1/positions` in the same window returned exactly `{"device_id":"09571b84-4bf6-46bb-929d-97770bd0fcb0","device_name":"c-recent","presentation":"no-position"}`: the identical three-key object, no `lat`, `lon`, `ts`, `received_at` or `last_contact_at`. (AC28 and AC32 jointly) |
| AC28 (d) error | §3 AC28(d): the same `error` event as AC27(a) | as AC27(a) | PASS. The `error` event took AC27's interrupted path rather than being rejected or ignored |
| AC28 (e) three rejections | §3 AC28(e): handed the page, in turn, `'{not json at all'`, an event typed `teleport`, and a `presentation` value of `offline` | `innerText` of `#bar` and `#panel` after each; `snapshot().rejectedCount` / `.lastRejection` / `.markerIds` after each; then a REAL fix inserted afterwards | PASS. Each was refused individually: `rejectedCount` went 1, 2, 3 with the bar reading `1 event(s) rejected: unparseable event data`, then `... unrecognised event type "teleport"`, then `... presentation value "offline" is outside the four tokens`. The map was never blanked - all three markers and all four state words were unchanged after every one. The stream kept consuming: a real `position` event that landed afterwards moved `b-live`'s marker and set it back to `live`, with the rejection notice still displayed |
| AC29 (a) empty family | §3 AC29(a): reloaded `/map`, entered `$EMPTY_VIEWER`, pressed **Watch** | `innerText` of `#panel`; `outerHTML` of `#empty`; `snapshot().deviceSetKnown` and device count; screenshot | PASS. Before a token was given the panel showed only the DEVICES heading and claimed nothing. After watching the empty family it rendered `<p id="empty">no devices in this family</p>` with `deviceSetKnown: true`, 0 devices and 0 markers - explicitly empty, not indistinguishable from still connecting |
| AC29 (b) stream wins | §3 AC29(b): with the stream having already described `b-live` as `live`, handed `window.trackerMap.applyPositions` a late positions response saying `stale` for it | `innerText` of `#located`; tooltip text; `snapshot().devices[<b-live>].state`; `window.describedByStream[<b-live>]` | PASS. `describedByStream` was `true` beforehand; after the late, disagreeing response the label still read `b-live live`, the tooltip still `b-live - live`, and the snapshot state was still `"live"`. The response did not revert a device the stream had already described |
| AC30 | §3 AC30 steps 1-3: pressed **Watch** with the token field EMPTY, then again with `not-a-real-token` | `innerText` of `#status` immediately and 8s later for each; `snapshot().status`; `window.source` (the EventSource); screenshots | PASS. Empty field: `viewer token refused: the server would not accept this credential (no viewer token was given)`. Bad token: `viewer token refused: the server would not accept this credential (HTTP 401)`. Neither contained `connecting`, both were identical eight seconds later, and `window.source` was `null` in both cases - the EventSource was closed, not left retrying |

**What was NOT observed, stated plainly.** The production retention purge drops a whole monthly
PARTITION; this run deleted the rows instead, which leaves the device in the same state the map reacts
to (holding no fix) but is not the same SQL. The partition-drop path is covered by the Go test
`TestStreamPurgeToNoPosition`, which asserts the same unlocated event and the same cross-surface
identity. Everything else in the table above is a rendered outcome that was read off the page.

**What has changed in the server since the observation, stated rather than left for a reader to
find.** One further commit touches server code after `eb58f53`: the fix for implementation verdict 2's
finding F5, which makes `streamConn.poll` decide its batch of `position` events against the cursor as
the poll BEGAN instead of one advanced mid-loop, so two devices whose current positions share a
`received_at` microsecond both reach the stream. It changes no wire shape, no event type, no
presentation value and not one byte of `internal/server/static/map.html`, and every input the fourteen
rows above were read against is produced the same way it was on the day. Its only visible effect on a
map is that a device which the tie used to swallow now arrives as an ordinary `position` event, taking
the AC28(a) branch that was observed here. The rows therefore stand as written; nothing in them was
re-run, and this paragraph is the disclosure, not a claim of re-observation.
