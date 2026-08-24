# MAP-VERIFICATION.md - the browser map, verified by eye

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

Commit verified against: `NOT YET OBSERVED`
Actor: `NOT YET OBSERVED`
Date: `NOT YET OBSERVED`
Stack: `docker compose -p tracker-mapverify`, `TRACKER_LIVE_WINDOW_SECONDS=3600`,
`TRACKER_STALE_WINDOW_SECONDS=21600`

| criterion | steps run | how the outcome was observed | outcome |
|---|---|---|---|
| AC24 | NOT YET OBSERVED | NOT YET OBSERVED | NOT YET OBSERVED |
| AC25 | NOT YET OBSERVED | NOT YET OBSERVED | NOT YET OBSERVED |
| AC26 | NOT YET OBSERVED | NOT YET OBSERVED | NOT YET OBSERVED |
| AC27 (a) error event | NOT YET OBSERVED | NOT YET OBSERVED | NOT YET OBSERVED |
| AC27 (b) drop | NOT YET OBSERVED | NOT YET OBSERVED | NOT YET OBSERVED |
| AC27 (c) fails to open | NOT YET OBSERVED | NOT YET OBSERVED | NOT YET OBSERVED |
| AC28 (a) position | NOT YET OBSERVED | NOT YET OBSERVED | NOT YET OBSERVED |
| AC28 (b) located presentation | NOT YET OBSERVED | NOT YET OBSERVED | NOT YET OBSERVED |
| AC28 (c) unlocated presentation | NOT YET OBSERVED | NOT YET OBSERVED | NOT YET OBSERVED |
| AC28 (d) error | NOT YET OBSERVED | NOT YET OBSERVED | NOT YET OBSERVED |
| AC28 (e) three rejections | NOT YET OBSERVED | NOT YET OBSERVED | NOT YET OBSERVED |
| AC29 (a) empty family | NOT YET OBSERVED | NOT YET OBSERVED | NOT YET OBSERVED |
| AC29 (b) stream wins | NOT YET OBSERVED | NOT YET OBSERVED | NOT YET OBSERVED |
| AC30 | NOT YET OBSERVED | NOT YET OBSERVED | NOT YET OBSERVED |

**Every row above is deliberately unfilled, and that is a true statement about the world rather than
an unfinished draft.** Two roles tried and neither could observe anything. The implementer session has
no browser at all. The stage session that dispatched it HAS one - `/usr/bin/chromium` is installed and
the `chrome-devtools` MCP server is connected - but every call into it was refused at the permission
layer, as was `curl` against `localhost:8080`, and a non-interactive session has nobody to grant the
grant. So nothing on this page has been seen by anybody, and writing a predicted outcome into this
table is exactly what this document forbids.

A row is filled only by the actor who ran the step, naming the element or label they read the outcome
off. The block, the decision it needs and the two ways out are in
`work/specs/S0010-tracker-server-1/blocked-report.md` in the umbrella.
