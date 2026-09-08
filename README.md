# tracker

A **self-hosted, Life360-style family location tracker**. An Android phone reports its location to a
server *you* run; family members see each other on a live map and get alerts when someone arrives at or
leaves a place you have defined.

> **Status: early. The server spine, its data model, ingestion, a read API, a live map, server-side
> geofencing, push alerts, and — as of S7 — security & privacy hardening.**
> This repo has config, `/healthz`, a database pool, the **spatial schema** (families, devices,
> viewers, monthly-partitioned `fixes`, geofences) with proximity/containment query helpers, a
> **token-authenticated ingestion surface** (per-device enrollment, `POST /v1/fixes`, and an interim
> `POST /owntracks` adapter), a **family-scoped read API** (`GET /v1/positions`,
> `GET /v1/devices/{id}/history`, `GET /v1/near`) behind a separate **viewer** credential, a
> **live-map SSE stream** (`GET /v1/stream` + a Leaflet page at `GET /map`), **server-side
> geofencing** (operator-managed "Places", a stream evaluator that records **enter/exit** events off
> the fix stream, and viewer reads for both — `GET /v1/places`, `GET /v1/geofence-events`), and now
> **push alerts**: a viewer registers its phone (`POST /v1/push-subscriptions`) and a crossing is
> delivered as a **high-priority FCM HTTP v1** message — or via **UnifiedPush/ntfy** for a degoogled
> deployment. **Location data goes in, reads back, streams to a live map, fires enter/exit events, and
> now pushes them to registered phones end-to-end.** Delivery is **best-effort** (FCM's own contract),
> and push is **off unless a backend is configured**. As of **S7** the server is **hardened**: TLS is enforced (it refuses to
> start in plaintext unless you say so explicitly), history **auto-purges** on a retention timer,
> database **roles are least-privilege**, tokens can be **rotated and expired**, `tracker config-lint`
> fails on a plaintext endpoint or a checked-in secret, and the honest server-holds-plaintext boundary
> is written down in [`THREAT-MODEL.md`](THREAT-MODEL.md). As of **C1** the **Android client collects
> location for real**: a Kotlin/Compose app (`android/`) with a **foreground service** (`type=location`)
> that streams fixes from the **fused location provider**, behind the **two-step background-location
> permission flow** Android requires (foreground first; then "Allow all the time", which on Android 11+
> can only be granted from the settings page). As of **C2** it **no longer loses what it collects**:
> every fix is written to a **durable on-disk queue** before anything tries to send it, and a
> **WorkManager** job constrained to `NetworkType.CONNECTED` drains it into `POST /v1/fixes`
> oldest-first, retrying indefinitely with jittered exponential backoff. Going offline now *delays*
> reporting instead of losing it, and a replay after a lost response is absorbed by the server's
> `(device_id, ts)` dedup, so nothing is stored twice. The device token is stored **in plaintext** until
> C3, and there is no in-app map until C5. **Much of the client is device behaviour a headless CI
> cannot prove** — runtime grants, a live GPS stream, screen-off survival, and whether WorkManager
> actually fires when the radio returns — so the gate covers the provable half (payload, validation,
> permission state machine, the queue against a real filesystem, and the flush loop against a real
> local server that enforces the same idempotency contract) and the rest is an **operator check on a
> real device**, written down in [`android/README.md`](android/README.md) rather than faked with a
> passing test.
> As of **S0010** every device a viewer reads carries a **server-computed presentation state** —
> `no-position` | `live` | `recent` | `stale` — on `GET /v1/positions`, on the SSE stream and on the
> map, and `/v1/positions` now lists **every device in the family** rather than only the ones that
> have reported. So *"this phone has never been set up"* is finally distinguishable from *"this phone
> went quiet an hour ago"*, and no browser derives liveness from a clock the server does not control.
> It is not a finished tracker, and this README will say so until it is. The wire contract is in
> [`SPEC.md`](SPEC.md); the plan lives in the umbrella at `operations/roadmaps/tracker.md`.

## Stack

| | |
|---|---|
| **Server** | Go — `chi` router, `pgx` pool, `goose` migrations; a single static binary |
| **Database** | **PostgreSQL + PostGIS**, `geography(Point,4326)` |
| **Client** *(collecting + offline-durable; C2)* | native Kotlin — Jetpack Compose, foreground service `type=location`, `FusedLocationProviderClient`, a file-backed durable fix queue flushed by **WorkManager**; AGP 8.5 / Gradle 8.9. See [`android/`](android/) |

**PostGIS is not incidental.** Location math is the product, and it is where a tracker gets things
quietly, confidently wrong. `geography` returns real **metres on the spheroid**; the `geometry` type on
lat/lon returns *degrees*, so the same LA→Paris pair that is `9,124,665 m` comes back as `121.90` — a
number that still looks like an answer. Everything spatial goes through `geography`, and the tests assert
it rather than trusting it.

## The data model

| table | what it holds |
|---|---|
| `families` | the **authorization boundary** — every other row hangs off one |
| `devices` | a phone that reports fixes; holds a **SHA-256 of** its bearer token, never the token |
| `viewers` | a human who watches the map; same credential shape, deliberately a separate table |
| `fixes` | the location history: `geography(Point,4326)`, **partitioned by month** |
| `geofences` | server-side "Places": `geography(Polygon,4326)` |
| `geofence_events` | the **append-only** enter/exit log, one row per crossing (S5) |
| `push_subscriptions` | a viewer's registered push endpoint — where a crossing is delivered (S6) |

Three decisions in there are load-bearing, and each is pinned by a test that fails if it is undone.

**History is expired by dropping a partition, not by deleting rows.** `fixes` is `PARTITION BY RANGE (ts)`,
monthly. Purging a month is then an instant, all-or-nothing `DROP` — a bulk `DELETE` can half-finish and
leave a family's trail corrupted, and this cannot. The cost is that purging is coarse: up to a month of
history outlives its retention date. That is the trade, taken deliberately.

**There is no `DEFAULT` partition.** A fix whose month was never provisioned is **rejected loudly** rather
than swept into a catch-all — because rows in a catch-all can never be expired by dropping a partition, so
retention would quietly leak. The server therefore provisions the current month and the next one **at
start-up**.

**Coordinates are validated in Go, before they reach PostGIS.** This is not belt-and-braces. PostGIS does
not *reject* an out-of-range coordinate — it **silently coerces it into range and stores the result**:

```
ST_Point(47.6062, -122.3321, 4326)     -- Seattle's lat/lon, swapped
NOTICE: Coordinate values were coerced into range [-180 -90, 180 90] for GEOGRAPHY
=> stored at 47.6062E, 57.6679S        -- the South Atlantic
```

The row lands, every constraint passes, and the family is now somewhere they have never been. No `CHECK`
can catch this after the fact — by the time a constraint sees the value it is already a legal point — so
the only place it *can* be caught is before the cast. `store.ValidateLonLat` is that place.

### A geofence edge is a geodesic, not a line on a map

Worth knowing before you draw a Place. On `geography`, a polygon's edges are **great-circle arcs**. A
"square" drawn on the lat/lon grid does not have the edges you drew: its north and south edges follow
*parallels*, and a parallel is not a great circle, so the real edge **bows away from the line you drew** —
about **120 m** at the midpoint of a 1°-wide box.

A fix sitting exactly on the drawn southern edge is therefore genuinely **outside** the Place, and PostGIS
is right to say so. This is not a bug and it is not fixable; it is what a geography polygon *means*. It is
pinned by `TestGeofenceBoundaryContainment` so that the geofencing phase builds on it knowingly rather
than discovering it when somebody's arrival alert never fires.

Containment uses **`ST_Covers`**, which is inclusive of the boundary — a fix exactly on the edge of
"school" counts as at school. (`ST_Contains` is `false` on the boundary, and does not exist for
`geography` at all.)

**A ring that crosses itself is refused**, by a typed error *and* by a `CHECK` constraint on the table.
PostGIS would otherwise store it: a bowtie parses, warns into a NOTICE nobody reads, and yields a polygon
of **zero area** that contains nothing, forever — a Place that looks real in every listing and whose alert
simply never fires.

## Reporting fixes (S2)

A phone reports its location by POSTing a fix with a **per-device bearer token**. The full wire
contract — schema, auth, idempotency, errors — is [`SPEC.md`](SPEC.md); the essentials:

- **`POST /v1/fixes`** — the first-party JSON schema (`lat`, `lon`, `ts` required; `accuracy`,
  `battery`, `speed`, `trigger`, `msg_id` optional). The Android client speaks this, and since **C2**
  it buffers these exact bytes on disk until the server confirms them.
- **`POST /owntracks`** — an **interim, deprecatable** adapter for the stock OwnTracks Android app,
  so a real phone can drive the server before the first-party client exists. It is not a product
  dependency and is a candidate for retirement in S7.

Three properties are load-bearing and each is pinned by a test:

- **Idempotent on `(device_id, ts)`.** A replayed report is a silent no-op (`200`, `deduped:true`),
  never a duplicate row — so a client can retry a report whose response it never saw. The server
  stamps its own `received_at` separately from the device's `ts`.
- **Malformed input is a typed `400`, stored nowhere.** Coordinates are validated **in Go, before
  the SQL** — PostGIS *coerces* a bad coordinate rather than rejecting it, so the check cannot live
  in the database. `/v1/fixes` also rejects unknown fields, so a client typo is loud, not silent.
- **A device writes only its own fixes.** The fix is stored under the *authenticated* device; there
  is no `device_id` in either payload for a caller to forge (§7 authz, by construction).

### Enrolling a device (and a viewer)

Tokens are issued **by the operator, out of band** — there is no admin login yet, so enrollment is a
command against the database rather than an HTTP endpoint (whoever can enroll can write or read a
family's history; the credential for that is shell access to the deployment, not a network call).

```bash
docker compose exec tracker tracker create-family -name "The Schatz family"
#   → prints the family id
docker compose exec tracker tracker enroll -family <family-id> -name "Alice's phone"
#   → prints the DEVICE (write) token ONCE — store it now, it is not recoverable
docker compose exec tracker tracker add-viewer -family <family-id> -email alice@example.com -name "Alice"
#   → prints the VIEWER (read) token ONCE — store it now, it is not recoverable
```

The server stores only the **SHA-256** of each token. The token is 43 characters (base64url of 32
random bytes), never 32 bytes — which is what makes `token_hash`'s length check a real guard against
a raw token being stored where its digest belongs, rather than a coincidence.

> **TLS is S7.** Bearer tokens must travel over TLS in a real deployment; today the server
> terminates plaintext HTTP. Do not expose this to an untrusted network yet.

## Reading fixes (S3)

A **viewer** reads a family's location back over three poll-able routes, each requiring a viewer
bearer token and each scoped to that viewer's own family. The full contract is [`SPEC.md`](SPEC.md);
the essentials:

- **`GET /v1/positions`** — **every device** in the family, in one total order, each carrying exactly
  one **presentation state** (`no-position` | `live` | `recent` | `stale`). A device holding a fix
  carries its latest one plus `presentation` and `last_contact_at`; a device that has never reported
  (or whose fixes have all been purged) is a three-key entry with **no coordinate at all**.
- **`GET /v1/devices/{id}/history`** — one device's fixes, newest first, within an optional
  `from`/`to` window, paginated with `limit`/`offset`.
- **`GET /v1/near?lat=&lon=&m=`** — the family's fixes within `m` **metres** of a point, nearest
  first (the §5.1 `ST_DWithin`-then-`ST_Distance` proximity template, over the GiST index).

Two properties are load-bearing and each is pinned by the authz-matrix tests:

- **Reads and writes use separate credentials.** A **device** token writes its own fixes and only its
  own; a **viewer** token reads its family and only its family. Neither works on the other's routes
  (§7) — a device token on a read route is a `401`, a viewer token on a write route is a `401`.
- **A viewer sees only its own family.** Every route is scoped in its SQL, not by the caller. A read
  that names a device in another family is a `403`, and an empty result is `[]` — **never** a leak of
  another family's data.

> These routes answer a one-shot request. For a **live** map that pushes updates without polling,
> see the SSE stream below (S4).

### Is this phone off, or has it never been set up? (presentation state)

A viewer could not previously tell *"this phone has never checked in"* — a setup problem — from
*"this phone stopped checking in an hour ago"* — a liveness problem, and the only one worth worrying
about. Both rendered as an absence: the read API omitted a device holding no fix, and the map drew no
marker for it. So every device now carries one **server-computed** presentation value on every
surface a viewer reads.

Four properties are load-bearing, and each is pinned by a test:

- **The server computes it; no client re-derives it.** A browser whose clock is hours off must still
  render what the server sent. That is why the value rides the wire instead of a timestamp a page
  compares against `Date.now()`.
- **Age is `now - max(received_at)` across ALL the device's fixes, never the current position's own
  arrival.** They differ exactly when a fix arrived that did not become the current position — an
  offline backlog flush, or a phone whose clock runs fast — and in both cases the device is plainly
  alive while the row on display is old. The aggregate rides the wire as `last_contact_at`, beside
  and distinct from the unchanged `received_at`.
- **A device that has never reported is never `stale`.** The ordered test terminates at "holds no
  fix", so no age comparison is ever made for it, however long ago it was enrolled. The same step is
  what makes a device whose last fix retention purging deleted become `no-position` from that
  instant — on both surfaces at once, so an open map takes the marker down rather than leaving it
  where the server no longer has anything.
- **`0,0` is unrepresentable for an unlocated device, not merely unused.** Its entry is exactly
  `{device_id, device_name, presentation}` — no `lat`, `lon`, `ts`, `received_at` or
  `last_contact_at`, and no zeros. Null Island is a real place off the coast of Ghana and a family
  map must not be able to plot a phone there because a struct field defaulted.

The windows are `TRACKER_LIVE_WINDOW_SECONDS` / `TRACKER_STALE_WINDOW_SECONDS` (below). The full
contract — token spellings, the ordered test, boundary inclusivity, the wire shapes and the stream's
three event types — is [`SPEC.md`](SPEC.md).

## Watching the live map (S4)

A **viewer** watches the family move in real time over one long-lived connection, instead of polling.

- **`GET /v1/stream`** — a **Server-Sent Events** feed of the family's **position updates and
  presentation state**. On connect it describes every device in the family (a `position` event for
  each one holding a fix, a `presentation` event carrying the three-key unlocated entry for each one
  holding none), then pushes changes as they happen. There are exactly **three** event types —
  `position`, `presentation`, `error` — and **only `position` carries an `id`**, so a consumer that
  handles only `position` events sees exactly the event set it saw before presentation state existed,
  with unchanged ids and unchanged resume.
- **`GET /map`** — a minimal, server-served **Leaflet** page that consumes the stream: paste a viewer
  token and watch markers move. It loads Leaflet **from tracker itself** (vendored under `/static`),
  never a third-party CDN — a self-hosted privacy product should not tell someone else's server who is
  watching. (Map *tiles* still come from OpenStreetMap; markers render and move without them.) It
  labels every device with the state word the server sent, lists the ones that are present but not
  located, and reports connection health as a **separate axis** from device state: a dropped stream
  or an `error` event marks every state *unconfirmed* rather than freezing a green map, and the
  server **re-states the whole family** on the first successful read after an outage, so health can
  get better again and not only worse. What the page says about a device is written for a person
  rather than for the wire — a device the server holds no position for reads **"no position
  recorded"**, not a token or a zero — and the words are explained at
  [`/static/map-explained.html`](internal/server/static/map-explained.html), which the page links to
  once per region and the server serves.

  **What a browser actually renders is graded by a browser** (`make verify-ui`, below). The map's
  page is served with a **Content-Security-Policy** admitting its inline style and script by a
  per-response nonce, naming exactly one third-party host (the OpenStreetMap tile imagery) and no
  reporting endpoint, alongside `Referrer-Policy: no-referrer` — the map URL carries a live viewer
  token, so anything that can carry a URL off-origin would leak a read credential.
  [`MAP-VERIFICATION.md`](MAP-VERIFICATION.md) is the earlier manual procedure and the record of what
  was once seen by eye; it is **superseded as evidence** for every clause the browser route now
  grades, and it says so at the top.

Two properties are load-bearing, each pinned by a test:

- **A watcher only ever receives its own family's stream.** The cursor query is family-scoped in its
  SQL, so there is no path that could push another family's position onto a connection — the worst
  failure this product has (§4), now guarded across every open watcher.
- **A dropped connection resumes with no gaps.** The event `id` is the fix's `received_at`, a
  monotonic cursor; on reconnect the browser re-sends it as `Last-Event-ID` and the server replays
  every position that arrived while the watcher was away — and not the one it already had.

The stream authenticates a **viewer** token (a device/write token is a `401`), presented in the
`Authorization` header **or**, because the browser `EventSource` API cannot set headers, as
`?token=<token>` in the URL. See [`SPEC.md`](SPEC.md) for the full contract and the honest trade-offs
(a token in a URL; HTTP/2 vs plaintext HTTP/1.1).

> **The map is a POSITION stream, not a breadcrumb replay.** It shows where everyone is *now*,
> coalescing a burst of fixes to the newest — a live map wants the current position, not a re-run of
> every fix. The full history is still `GET /v1/devices/{id}/history`.

## Geofencing — "Places" and enter/exit events (S5)

A **Place** is a `geofences` polygon a family cares about — home, school, work. tracker evaluates every
incoming fix against a device's family's Places and records an **enter** or **exit** when the device
crosses one, into the append-only `geofence_events` log. The events are **logged, not delivered** yet
(push is S6); a viewer can already read them back.

- **`GET /v1/places`** — the family's Places, each with its ring as GeoJSON (viewer token).
- **`GET /v1/geofence-events`** — the family's crossings, newest first (viewer token). Each is
  `{device_id, place_id, place_name, transition, ts}`, `ts` being the crossing fix's device event-time.

Managing Places is an **operator** act (like device enrollment — there is no admin login yet), a
command against the database rather than an HTTP write:

```bash
tracker add-place -family <family-id> -name "Home" \
  -point 12.0,41.0 -point 13.0,41.0 -point 13.0,42.0 -point 12.0,42.0
#   → prints the Place id (longitude FIRST in every -point; the ring is closed for you)
tracker list-places   -family <family-id>
tracker remove-place  -family <family-id> -id <place-id>
```

Four properties are load-bearing, and each is pinned by a test:

- **Containment is `ST_Covers` — inclusive of the boundary.** A fix exactly on the edge of a Place
  counts as inside (§5.3). Remember a geography polygon's edges are *geodesics*, so a "square" drawn on
  the grid is not quite the region its corners imply (see above).
- **Transitions are debounced against GPS jitter.** A crossing is recorded only once the new state has
  **dwelled** for 90 s, so a fix or two flapping across the boundary — a stationary phone at the edge of
  "home" — never fires a spurious enter/exit.
- **Events derive from `ts` order, not arrival.** The log is a deterministic projection of the fix
  history: a replayed fix produces **no duplicate** event (each crossing is keyed by the fix that caused
  it), and out-of-order fixes are placed by their `ts`, not by when they landed.
- **A missed fix delays but never fabricates.** An enter is not recorded until later fixes prove the
  device stayed inside; if the boundary-crossing fix never arrives, the event is attributed to a later
  fix — it is never invented, and a device tracker only ever *saw* inside a Place gets no phantom enter.

## Push alerts (S6)

A crossing that S5 records is delivered to the family's **watchers** as a push. A watcher (a **viewer**)
registers the push endpoint of its phone, and every enter/exit for that family is sent to it.

- **`POST /v1/push-subscriptions`** — register (or refresh) an endpoint (viewer token). Body:
  `{"provider": "fcm" | "unifiedpush", "token": "…"}`. For **fcm**, `token` is the app's FCM
  registration token; for **unifiedpush**, it is the distributor-issued endpoint URL. Re-registering the
  same endpoint is idempotent — it never duplicates. See [`SPEC.md`](SPEC.md) for the exact shapes.

Two backends, one contract (roadmap §1 — FCM by default, UnifiedPush/ntfy for a degoogled deployment).
A family gets the **same alert** either way: title, body, high priority, a collapse key, and a small
structured `data` map. Which one a deployment uses is a config choice (below); push is **off** until one
is set.

The properties that matter, each pinned by a test:

- **High priority, always.** The FCM message sets `android.priority: "high"` — only a high-priority push
  wakes a closed app on an idle (Doze) device, which is the entire point of an arrival alert.
- **Collapsible "latest state."** The collapse key is per **(device, Place)**, so a rapid enter→exit
  collapses to the newest state rather than buzzing a phone twice for a crossing it can no longer act on.
- **A push NEVER blocks or fails ingestion.** Delivery runs on a bounded, retrying background worker: a
  slow or dead push backend cannot slow a fix's ingest, a send that fails is retried within limits and
  then **logged, not crashed**, and a backlog past the **pending cap** is dropped rather than growing
  without limit. Delivery is best-effort — FCM's own contract — and tracker does not pretend to more.
- **No location in the body.** A notification carries only the family's **own labels** — the device's
  name, the Place's name, the direction — and never a coordinate, accuracy, or raw fix datum (§5.3). The
  family named the device and the Place; a latitude is not something they opted to broadcast.

> **The owner-side real-device check is deferred to the owner.** CI proves the pipeline against a mock
> FCM (request shape, high priority, collapse key, failure handling) and a UnifiedPush parity test; that
> a real push lands on a real handset is a manual check the deployment owner runs with their own Firebase
> project — it confirms, it does not gate.

## Running it

```bash
export TRACKER_DB_PASSWORD='choose-something'   # required — see below
docker compose up -d
curl localhost:8080/healthz                     # {"status":"ok","database":"up"}
```

**There is no default database password, and compose refuses to start without one.** Not a placeholder
you are trusted to change later — a refusal, up front. A default would mean a family's location history,
including minors', sitting behind a Postgres superuser password that nobody ever chose. The same rule as
the server's: never boot with a silent default for a secret.

> Use an **alphanumeric** password, or percent-encode it. It is interpolated into the DSN, so `@`, `/`,
> `:`, `#`, `%` and `?` have meaning there — `p@ss` would be parsed as a *hostname*. You will not get a
> wrong answer if you ignore this (the server fails to connect and says so, loudly), just a confusing one.

> **Changing `TRACKER_DB_PASSWORD` later does not rotate the password.** This is a sharp edge in
> Postgres, not in tracker: the variable is read **only by `initdb`**, on the very first start against an
> empty data volume. Afterwards it is ignored — so editing it leaves tracker's DSN disagreeing with the
> database, and the server crash-loops on *"password authentication failed"*. To actually rotate it,
> change it **inside the database** (`ALTER ROLE tracker WITH PASSWORD '…'`) and set the variable to
> match. `docker compose down -v` resets it too, but that **destroys the location history** with it.

The server **applies its own migrations on start-up, before it listens**, so there is no separate
migration step and it can never serve requests against a half-migrated schema.

`/healthz` pings the database: it answers **503**, not 200, when PostGIS is unreachable. A health check
that reports OK while the database is down just tells your orchestrator to keep sending traffic to a
process that fails every real request.

### Configuration

Environment variables only. There is no config file: the one value the server cannot run without carries
a password, and keeping it on the environment means there is no config artefact to accidentally commit.

| variable | required | default | meaning |
|---|---|---|---|
| `TRACKER_DATABASE_URL` | **yes** | *none, ever* | PostgreSQL DSN (the server reads this) |
| `TRACKER_DB_PASSWORD` | **yes** | *none, ever* | the database password (`docker-compose.yml` reads this) |
| `TRACKER_ADDR` | no | `:8080` | listen address |
| `TRACKER_LOG_LEVEL` | no | `info` | `debug` \| `info` \| `warn` \| `error` |
| `TRACKER_TLS_CERT_FILE` | for TLS | *none* | PEM certificate; **both** cert and key set → the server terminates HTTPS |
| `TRACKER_TLS_KEY_FILE` | for TLS | *none* | PEM private key (a secret — mounted, never committed) |
| `TRACKER_ALLOW_PLAINTEXT` | no | *unset* | `1` to run **without** TLS (dev, or behind a TLS-terminating proxy). Required if no cert/key is set, or the server refuses to start |
| `TRACKER_RETENTION_DAYS` | no | `0` | drop `fixes` older than this many days on a daily timer; `0` = keep forever (no purge) |
| `TRACKER_LIVE_WINDOW_SECONDS` | no | `120` | at or under this age a device is `live` (whole **seconds**, no unit suffix) |
| `TRACKER_STALE_WINDOW_SECONDS` | no | `900` | past this age a device is `stale` (whole **seconds**); must be strictly greater than the live window |
| `TRACKER_PUSH_PROVIDER` | no | *disabled* | `fcm` \| `unifiedpush` — the S6 push backend; unset = no push |
| `TRACKER_FCM_PROJECT_ID` | when `fcm` | *none* | the Firebase project id the FCM v1 endpoint is scoped to |
| `TRACKER_FCM_CREDENTIALS_FILE` | when `fcm` | *none* | path to the Google service-account JSON key (mounted, never committed) |

**The server refuses to start** without a valid `TRACKER_DATABASE_URL` — no default, no empty string, no
guess. A tracker pointed at the wrong database is worse than one that would not boot, because the first
is discovered in production.

**It also refuses to start in plaintext by accident (S7).** With neither a `TRACKER_TLS_CERT_FILE` /
`TRACKER_TLS_KEY_FILE` pair nor `TRACKER_ALLOW_PLAINTEXT=1`, it will not boot: location is maximally
sensitive, so serving it unencrypted must be a *decision*, never an unset variable. A half-configured
pair (cert without key, or vice versa) is refused too — that is a deploy that believes it is encrypted
and is not. `tracker config-lint` is stricter than start-up: it fails on **any** plaintext endpoint
(`ALLOW_PLAINTEXT` does not satisfy it) and additionally scans the repo for checked-in secrets, so a
production config cannot ship plaintext by inheriting the dev default.

**The presentation windows are seconds, and empty means the default.** Unset, empty and
whitespace-only are one case - not configured - and not configured is `120` / `900`, because a
rendered compose file with an unset shell default hands the process an empty string and refusing to
boot over a value nobody typed would be the wrong answer to it. A value somebody *did* type and got
wrong is a refusal: `2m`, `120s`, `1.5`, `abc`, `0`, `-5` and anything outside 1..2^63-1 seconds all
refuse to start, naming the variable. Each defaults independently and the ordering is checked on the
**effective** pair, so setting only `TRACKER_STALE_WINDOW_SECONDS=60` gives (120, 60) and refuses -
no device could ever be `recent`. A successful start logs both effective values under their variable
names.

**Push is disabled unless a backend is named**, and a named backend that is missing what it needs to send
also refuses to start — `TRACKER_PUSH_PROVIDER=fcm` without a project id and a credentials file is a
start-up error, not a server that boots and silently drops every alert. `unifiedpush` needs no global
config (the endpoint is per-subscription). The FCM credentials file is **mounted at deploy time and never
committed** — it is a secret.

## Security & retention (S7)

**TLS.** Point the server at a certificate and key, or run behind a proxy that terminates TLS:

```bash
export TRACKER_TLS_CERT_FILE=/etc/tracker/tls.crt
export TRACKER_TLS_KEY_FILE=/etc/tracker/tls.key     # a secret — mount it, never commit it
# …or, behind a TLS-terminating reverse proxy / for local dev:
export TRACKER_ALLOW_PLAINTEXT=1
```

**Lint before you deploy.** `config-lint` fails on a plaintext endpoint *or* a checked-in secret, and it
does **not** accept `ALLOW_PLAINTEXT` — a production config must terminate TLS:

```bash
TRACKER_TLS_CERT_FILE=/etc/tracker/tls.crt TRACKER_TLS_KEY_FILE=/etc/tracker/tls.key \
  tracker config-lint --repo .
```

**Rotate or expire a token.** A new token is minted and the old one dies in the same write; `-ttl` gives
it a lifetime (omit for "never expires"). An expired token authenticates nothing.

```bash
tracker rotate-device-token -id <device-id> -ttl 720h   # a phone's write token, valid 30 days
tracker rotate-viewer-token -id <viewer-id>             # a viewer's read token, no expiry
```

**Retention.** Set `TRACKER_RETENTION_DAYS` to drop `fixes` older than that window on a daily timer (a
`DROP` per whole month, transactional and all-or-nothing). Unset/`0` keeps history forever.

**Least-privilege database roles.** Migration `00005` ships two group roles the operator builds login
roles from — `tracker_readonly` (SELECT only) and `tracker_writer` (DML, no DDL) — so a reporting or
analytics connection can never alter or delete a family's trail. The server's own connection is the
schema **owner** (it provisions and drops partitions). See [`THREAT-MODEL.md`](THREAT-MODEL.md) §6.

## Development

```bash
make check     # THE gate: gofmt · vet · build · test -race · staticcheck · govulncheck · Android
make smoke     # brings the real stack up and asserts /healthz answers 200

make verify-ui          # the browser map's rendered claims, in a real browser engine
make verify-ui-android  # the Android screen's rendered claims, on a booted emulator
make verify-ui-refusal  # both of the above, with their prerequisite removed, must refuse
make verify-ui-record   # the F1-F11 record, against what those routes actually ran
```

`make check` is what CI runs and what the umbrella's `scripts/verify.sh tracker` runs — one gate, defined
once, in the `Makefile`.

### The user-interface gate

tracker ships **two** user interfaces — the browser map and the Android screen — and a claim about
what a person *sees* is graded by the runtime that draws it, never by searching HTML, CSS, Kotlin or
a compiled resource table. A text search cannot decide what a CSS rule applies to, what won the
cascade, or what was shown rather than merely built.

So `make verify-ui` drives **Chromium** over the production `/map` and `/static` handlers and reads
every number back out of the live engine: contrast from the resolved colours in both themes, focus
indicators from a pixel diff of the rendering, target sizes from laid-out boxes, accessible names
from the engine's own accessibility tree, network origins and policy violations from the browser's
own records. `make verify-ui-android` runs an **instrumented** suite on a booted Android emulator
with Google's Accessibility Test Framework applied to the tree the platform actually built.

**No assertion in either route may pass vacuously.** Each one is also re-run against the same surface
mutated to break exactly the claim it measures — one substitution in the bytes the server served, or
a debug-only `UiMutation` that is inert in a release build — and the route fails if fewer
demonstrations ran than there are claims. A check that cannot go red is not evidence.

**They refuse; they never skip.** No browser engine, no Android SDK, no `/dev/kvm`, no booted device:
each is an exit-non-zero naming the criterion, the missing prerequisite and how to obtain it, exactly
as `make android` does for a missing SDK and `make test` for a missing Docker daemon. `make
verify-ui-refusal` is the check that keeps that true. The clause-by-clause record, for both surfaces,
is [`FRONTEND-CONVENTIONS-RECORD.md`](FRONTEND-CONVENTIONS-RECORD.md), and `make verify-ui-record`
refuses a record naming an assertion that did not actually run.

These are **not** folded into `make check`: that target is the gate a human runs on a laptop, and it
does not need a browser or an emulator. CI runs both.

**`make check` needs a reachable Docker daemon.** The tests start a real PostGIS with
`testcontainers-go`, and **they fail rather than skip if they cannot**. That is deliberate: this
project's correctness *is* spatial SQL, which cannot be tested against a mock — a mock would only ever
assert what we already believed. A gate that skips its bedrock when the database is missing reports green
while proving nothing, so a missing daemon is an error here, not a pass.

**The vulnerability gate records; it never ignores.** `govulncheck` runs on every `make check`, and an
advisory it reports is either **remediated at source** — bump the implicated module, raise the Go
toolchain to the version it names in `Fixed in` — or **written down** in
[`.govulncheck-suppressions.yaml`](.govulncheck-suppressions.yaml) with a reason, the reported call path
and the date it was recorded. There is no third option and no other suppression surface. An unrecorded
advisory fails; a record for an advisory `govulncheck` says *has* a fix fails, because that case is
remediated and not recorded; a record whose advisory the current run no longer reports fails, because a
suppression must not outlive the advisory it was written for; a malformed record fails naming the entry,
never skipped and never honoured; and any `govulncheck` exit status other than the one a run that
produced a report exits with fails with the tool's own error, because a tool that could not run has not
told you the code is clean. `internal/vulngate` enforces that, and its own tests — which run inside
`make check` — prove it still turns red on demand.

**"An advisory it reports" means all three levels.** `govulncheck` reports at three depths — it traced a
call path to a vulnerable *symbol*, it found a vulnerable *package* imported, or it found a vulnerable
*module* required — and its text report splits those across `=== Symbol Results ===`, `=== Package
Results ===` and `=== Module Results ===`, printing the last two only under `-show verbose`. The gate
therefore reads the tool's `-format json` stream, where every advisory arrives as a `finding` object
whatever depth it was traced to. That is the one flag it passes, and it *widens* what the gate sees:
reading the text report means reading part of the verdict, and the suppression file is the only thing
allowed to make the verdict smaller.

**The Go toolchain is stated in three files, and they are checked against each other.** CI provisions it
(`GO_VERSION` in `.github/workflows/ci.yml`), the `Dockerfile`'s builder image bakes it into the shipped
binary, and `go.mod`'s `toolchain` directive is what a local `go build` downloads and runs; those are
three different builds, so one copy will not do. `internal/toolchain` asserts all three name the same
version and that `go.mod`'s `go` directive — a *language floor*, not a toolchain pin — never climbs above
it. It runs inside `make check`, so a half-landed bump fails and names the files that disagree, instead of
leaving CI to prove things with a compiler production never runs. That matters more than it sounds:
`govulncheck` scans the standard library of whichever toolchain executes it, so a split pin is a
vulnerability gate that answers differently depending on where it ran.

## Known limitations

Things that are true today and are not hidden:

- **The Android client's offline queue is bounded, and its scheduling is not gate-proved.** As of C2 a
  fix is written to disk before delivery is attempted and stays there until the server has it, so a
  transient failure can no longer lose one. Three honest edges remain: the queue holds **5,000 fixes**
  (~3.5 days at the current cadence) and evicts the **oldest** past that, counted as `dropped`;
  WorkManager's retry backoff is indefinite but tops out around **5 hours**, so a flush after a very
  long outage can lag the reconnection (latency, not loss); and delivery is at-least-once on the wire
  — the server's `(device_id, ts)` dedup is what makes it exactly-once in the database — so the app's
  `delivered` counter can over-count replays. The crash-atomicity of the *writer* (temp → `fsync` →
  rename) is review-only; the reader half of it is tested.
- **Most of the client's behaviour is not provable in CI, and is not claimed to be.** Runtime permission
  grants, a live GPS stream, foreground-service survival with the screen off, whether WorkManager runs
  the flush when connectivity returns, and OEM battery-killer behaviour all need a real device. The gate
  covers the pure half — payload construction, coordinate and timestamp validation, the permission state
  machine, the durable queue against a real filesystem, and the flush loop against a real local server
  — and the rest is an explicit **operator device check** documented in
  [`android/README.md`](android/README.md). No test in this repo mocks the platform and then reports the
  mock's answer as evidence.
- **The client's device token is stored in plaintext** `SharedPreferences` until C3 moves it to
  `EncryptedSharedPreferences` behind an Android Keystore key. `allowBackup="false"` limits the blast
  radius in the meantime.
- **The client needs Google Play services.** `FusedLocationProviderClient` has no AOSP equivalent and
  there is no `LocationManager` fallback, so a fully degoogled phone cannot run it today.
- **Push delivery is best-effort, and off by default.** A crossing is delivered to registered phones
  (S6) via FCM or UnifiedPush, but delivery is **not guaranteed** — FCM's own contract — and a missed
  alert is possible; the freshest state arrives on the device's next crossing. Push is disabled unless a
  backend is configured, and freshness is still bounded by the last received fix: an offline phone's
  crossings — and their pushes — fire when its buffered fixes arrive. Whether a real push reaches a real
  handset is the owner's manual real-device check (CI proves the pipeline against a mock).
- **Enter/exit is debounced, so it is deliberately not instant.** A crossing must dwell 90 s before it
  is recorded — the price of not alerting on GPS jitter. And the evaluator advances a (device, Place)'s
  state *forward* in `ts`: a fix arriving out of order and older than that pair's latest recorded
  transition is kept as history but does not splice a past event into the log. In practice a crossing
  is a moving, frequently-reporting phone, and reordered fixes ahead of the last transition are placed
  exactly by `ts`.
- **The live map is a position stream, coalesced.** `GET /v1/stream` pushes each device's *current*
  position, collapsing a burst of fixes between polls to the newest. That is deliberate — a live map
  wants where everyone is now — but it means the stream is not a lossless replay of every fix; the
  full trail is `GET /v1/devices/{id}/history`. The stream polls the database on a short interval
  rather than being pushed from ingestion, which keeps it stateless across replicas at the cost of up
  to that interval of latency. The interval is one second, and half a second under the two tightest
  legal window pairs, so a time-driven transition always lands inside its announcement bound.
- **An empty family is not told when an outage ends.** After an `error` event the server re-states
  the family on its first successful read, which is what clears a map's *unconfirmed* marks. A family
  with **no devices at all** has nothing to re-state and the wire contract has no fourth event type
  to carry an "all clear", so that one page keeps its interruption notice until the viewer
  reconnects. Named rather than hidden; a fourth event type is the fix, and it is not this change.
- **A stream position update can be delayed by one fix under a rare write race.** The stream's cursor
  is `received_at`; if two fixes for a family commit out of `received_at` order within one poll
  interval, the later-committing one can be skipped until that device's *next* fix re-establishes it.
  At family scale (a few devices, seconds apart) this is vanishingly rare and self-heals on the next
  fix; the durable fix (a strictly monotonic stream sequence, or logical decoding) is a scale-phase
  concern, not a family-deployment one. The reconnect/resume path itself has no such gap.
- **The stream token travels in the URL for browsers.** `EventSource` cannot set an `Authorization`
  header, so the Leaflet page passes the viewer token as `?token=`. URLs leak into logs and referrers;
  this is an interim trade mitigated by TLS (S7) and retired by the in-app map (C5). Programmatic
  callers should use the header. With TLS now enforceable (S7), that `?token=` no longer crosses the
  wire in the clear; the log/referrer exposure remains until the in-app map (C5).
- **HTTP/2 comes with TLS.** SSE multiplexes cleanly over HTTP/2, which also lifts the browser's
  ~6-connections-per-origin cap. Go negotiates HTTP/2 automatically when the server terminates TLS
  (S7), so configuring a cert/key pair gets it; over plaintext HTTP/1.1 (`ALLOW_PLAINTEXT`, or a proxy
  that speaks HTTP/1.1 upstream), more than ~6 simultaneous tabs to the same origin can queue.
- **TLS is enforced, but you supply the certificate.** The server terminates HTTPS when
  `TRACKER_TLS_CERT_FILE` / `TRACKER_TLS_KEY_FILE` are set (TLS 1.2 floor), and it **refuses to start**
  in plaintext unless `TRACKER_ALLOW_PLAINTEXT=1` says so on purpose (dev, or a TLS-terminating proxy).
  It does not obtain or renew certificates — that is the operator's (or the proxy's) job. `tracker
  config-lint` fails on any plaintext endpoint or a checked-in secret.
- **Partitions are provisioned at start-up, with a two-month lookahead — plus an on-ingest safety
  net.** A process running longer than the lookahead would otherwise reach an unprovisioned month and
  reject every fix at the rollover: harmless before S2 (nothing ingested), silent data loss after it.
  So ingestion now provisions a fix's month **on demand** if it is missing, bounded by the timestamp
  window (`[now−90d, now+24h]`) so untrusted input cannot create partitions without limit. **S7 added
  the retention purge on a daily timer** (dropping *old* months); forward provisioning still rides the
  start-up lookahead plus this on-ingest safety net, so a dedicated forward-provisioning ticker remains
  a later refinement rather than a correctness gap.
- **Retention purging is coarse, and off by default.** With `TRACKER_RETENTION_DAYS` set, a daily job
  **drops** whole monthly `fixes` partitions older than the window — a `DROP`, not a `DELETE`, so it is
  transactional and all-or-nothing per month. Because partitions are monthly, a fix can outlive its
  retention date by up to a month. Unset (`0`) runs no purge and keeps history forever — silently
  deleting on a window nobody chose would be worse, so it is opt-in.
- **Migrations are not safe against concurrent migrators.** Every instance migrates on boot, so two
  starting at once would race. The compose stack runs one replica; whoever scales it out owns fixing this.

## Privacy — the honest boundary

**Location is not end-to-end encrypted, and this project will not claim it is.** The server renders the
map and runs the spatial queries, so it necessarily holds plaintext positions. What self-hosting buys you
is *whose* server that is: the plaintext sits on yours rather than a company's. Anyone with
administrative access to it can read the family's location history — this is **inherent, not a fixable
gap**. The full, honest threat model — what tracker defends against (network eavesdropping, database
theft of hashed tokens, third-party-cloud exposure) and what it cannot (a compromised server or
malicious admin) — is in [`THREAT-MODEL.md`](THREAT-MODEL.md).

**Push notifications carry no location.** A crossing alert says only *"Alice's phone arrived at School"* —
the device name and Place name the family themselves chose, and the direction. It never carries a
coordinate, an accuracy, or any raw fix datum, so a family's precise whereabouts never transit a push
provider's servers (§5.3). A push endpoint is a routing address the phone can rotate, not a credential and
not location data, so it is stored in the clear — losing it leaks "this endpoint can be pushed to", not a
family's movements.
