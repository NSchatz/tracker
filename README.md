# tracker

A **self-hosted, Life360-style family location tracker**. An Android phone reports its location to a
server *you* run; family members see each other on a live map and get alerts when someone arrives at or
leaves a place you have defined.

> **Status: early. The server spine, its data model, ingestion, a read API, a live map, server-side
> geofencing, and — as of S6 — push alerts.**
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
> and push is **off unless a backend is configured**. There is still **no Android client**; the native
> Kotlin app (C-track) is the phase that follows, so today a real phone drives the server through the
> interim OwnTracks adapter. It is not a finished tracker, and this README will say so until it is. The
> wire contract is in [`SPEC.md`](SPEC.md); the plan lives in the umbrella at
> `operations/roadmaps/tracker.md`.

## Stack

| | |
|---|---|
| **Server** | Go — `chi` router, `pgx` pool, `goose` migrations; a single static binary |
| **Database** | **PostgreSQL + PostGIS**, `geography(Point,4326)` |
| **Client** *(not started)* | native Kotlin (Android) |

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
  `battery`, `speed`, `trigger`, `msg_id` optional). The Android client will speak this.
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

- **`GET /v1/positions`** — the latest fix per device in the family, ordered by device name.
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

## Watching the live map (S4)

A **viewer** watches the family move in real time over one long-lived connection, instead of polling.

- **`GET /v1/stream`** — a **Server-Sent Events** feed of the family's **position updates**. On
  connect it sends the current position of every device (the snapshot that paints the map), then
  pushes each device's new position as it arrives. Every event carries an `id` (the fix's receive
  time, in microseconds).
- **`GET /map`** — a minimal, server-served **Leaflet** page that consumes the stream: paste a viewer
  token and watch markers move. It loads Leaflet **from tracker itself** (vendored under `/static`),
  never a third-party CDN — a self-hosted privacy product should not tell someone else's server who is
  watching. (Map *tiles* still come from OpenStreetMap; markers render and move without them.)

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
| `TRACKER_PUSH_PROVIDER` | no | *disabled* | `fcm` \| `unifiedpush` — the S6 push backend; unset = no push |
| `TRACKER_FCM_PROJECT_ID` | when `fcm` | *none* | the Firebase project id the FCM v1 endpoint is scoped to |
| `TRACKER_FCM_CREDENTIALS_FILE` | when `fcm` | *none* | path to the Google service-account JSON key (mounted, never committed) |

**The server refuses to start** without a valid `TRACKER_DATABASE_URL` — no default, no empty string, no
guess. A tracker pointed at the wrong database is worse than one that would not boot, because the first
is discovered in production.

**Push is disabled unless a backend is named**, and a named backend that is missing what it needs to send
also refuses to start — `TRACKER_PUSH_PROVIDER=fcm` without a project id and a credentials file is a
start-up error, not a server that boots and silently drops every alert. `unifiedpush` needs no global
config (the endpoint is per-subscription). The FCM credentials file is **mounted at deploy time and never
committed** — it is a secret.

## Development

```bash
make check     # THE gate: gofmt · vet · build · test -race · staticcheck · govulncheck
make smoke     # brings the real stack up and asserts /healthz answers 200
```

`make check` is what CI runs and what the umbrella's `scripts/verify.sh tracker` runs — one gate, defined
once, in the `Makefile`.

**`make check` needs a reachable Docker daemon.** The tests start a real PostGIS with
`testcontainers-go`, and **they fail rather than skip if they cannot**. That is deliberate: this
project's correctness *is* spatial SQL, which cannot be tested against a mock — a mock would only ever
assert what we already believed. A gate that skips its bedrock when the database is missing reports green
while proving nothing, so a missing daemon is an error here, not a pass.

## Known limitations

Things that are true today and are not hidden:

- **Data goes in, reads back, streams to a live map, fires geofence events, and pushes them — but there
  is no Android client yet.** Ingestion (S2), a family-scoped read API (S3), the live-map SSE stream +
  Leaflet page (S4), server-side geofencing (S5), and push alerts (S6) exist; the native Kotlin app
  (C-track) is the phase that follows, so today a real phone drives the server through the interim
  OwnTracks adapter — which is exactly that, interim.
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
  to that interval of latency.
- **A stream position update can be delayed by one fix under a rare write race.** The stream's cursor
  is `received_at`; if two fixes for a family commit out of `received_at` order within one poll
  interval, the later-committing one can be skipped until that device's *next* fix re-establishes it.
  At family scale (a few devices, seconds apart) this is vanishingly rare and self-heals on the next
  fix; the durable fix (a strictly monotonic stream sequence, or logical decoding) is a scale-phase
  concern, not a family-deployment one. The reconnect/resume path itself has no such gap.
- **The stream token travels in the URL for browsers.** `EventSource` cannot set an `Authorization`
  header, so the Leaflet page passes the viewer token as `?token=`. URLs leak into logs and referrers;
  this is an interim trade mitigated by TLS (S7) and retired by the in-app map (C5). Programmatic
  callers should use the header.
- **HTTP/2 is not terminated yet.** SSE multiplexes cleanly over HTTP/2, which also lifts the
  browser's ~6-connections-per-origin cap; tracker upgrades to it automatically once TLS lands (S7).
  Until then, over plaintext HTTP/1.1, more than ~6 simultaneous tabs to the same origin can queue.
- **No TLS yet.** The server terminates plaintext HTTP. Bearer tokens and location data must travel
  over TLS in any real deployment; enforcing that (and the threat model) is S7.
- **Partitions are provisioned at start-up, with a two-month lookahead — plus an on-ingest safety
  net.** A process running longer than the lookahead would otherwise reach an unprovisioned month and
  reject every fix at the rollover: harmless before S2 (nothing ingested), silent data loss after it.
  So ingestion now provisions a fix's month **on demand** if it is missing, bounded by the timestamp
  window (`[now−90d, now+24h]`) so untrusted input cannot create partitions without limit. That is a
  reactive mitigation; the real fix — a maintenance tick alongside the retention job — is S7's.
- **Retention purging is coarse.** History is dropped a whole month at a time, so a fix can outlive its
  retention date by up to a month. Deliberate — see above.
- **Migrations are not safe against concurrent migrators.** Every instance migrates on boot, so two
  starting at once would race. The compose stack runs one replica; whoever scales it out owns fixing this.

## Privacy — the honest boundary

**Location is not end-to-end encrypted, and this project will not claim it is.** The server renders the
map and runs the spatial queries, so it necessarily holds plaintext positions. What self-hosting buys you
is *whose* server that is: the plaintext sits on yours rather than a company's. Anyone with
administrative access to it can read the family's location history. A full threat model ships before the
tracker is usable.

**Push notifications carry no location.** A crossing alert says only *"Alice's phone arrived at School"* —
the device name and Place name the family themselves chose, and the direction. It never carries a
coordinate, an accuracy, or any raw fix datum, so a family's precise whereabouts never transit a push
provider's servers (§5.3). A push endpoint is a routing address the phone can rotate, not a credential and
not location data, so it is stored in the clear — losing it leaks "this endpoint can be pushed to", not a
family's movements.
