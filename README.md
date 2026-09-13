# tracker

A **self-hosted, Life360-style family location tracker**. An Android phone reports its location to a
server *you* run; family members see each other on a live map and get alerts when someone arrives at or
leaves a place you have defined.

> **Status: early. The server spine, its data model, ingestion, a read API, a live map, server-side
> geofencing, push alerts, and - as of S7 - security & privacy hardening.**
> This repo has config, `/healthz`, a database pool, the **spatial schema** (families, devices,
> viewers, monthly-partitioned `fixes`, geofences) with proximity/containment query helpers, a
> **token-authenticated ingestion surface** (per-device enrollment, `POST /v1/fixes`, and an interim
> `POST /owntracks` adapter), a **family-scoped read API** (`GET /v1/positions`,
> `GET /v1/devices/{id}/history`, `GET /v1/near`) behind a separate **viewer** credential, a
> **live-map SSE stream** (`GET /v1/stream` + a Leaflet page at `GET /map`), **server-side
> geofencing** (operator-managed "Places", a stream evaluator that records **enter/exit** events off
> the fix stream, and viewer reads for both - `GET /v1/places`, `GET /v1/geofence-events`), and now
> **push alerts**: a viewer registers its phone (`POST /v1/push-subscriptions`) and a crossing is
> delivered as a **high-priority FCM HTTP v1** message - or via **UnifiedPush/ntfy** for a degoogled
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
> `(device_id, ts)` dedup, so nothing is stored twice.
> As of **ALERT-2** the alert finally **reaches a person**: the client receives a pushed crossing over
> **FCM** and renders it as a notification naming the device, the Place and the direction, and opening
> the app shows the family's recent crossings read back from `GET /v1/geofence-events` - so a crossing
> the push backend dropped is still findable. The push is deliberately **never the record**: FCM stores
> four collapsible messages per phone and then discards, so the server now counts the surplus as
> `beyond-collapse-bound`, records **three delivery outcomes and no fourth that asserts delivery**, and
> the in-app list is the fail-safe. The app holds a **viewer** token alongside its device token to read
> that list and register its endpoint; both are still **plaintext** until SECRET-3, and there is no
> in-app map until C5. **Much of the client is device behaviour a headless CI
> cannot prove** - runtime grants, a live GPS stream, screen-off survival, whether WorkManager
> actually fires when the radio returns, and whether a real FCM message wakes a real handset - so the
> gate covers the provable half (payload, validation, permission state machine, the queue against a
> real filesystem, the flush loop against a real local server that enforces the same idempotency
> contract, and every branch of the alert surface's parse, merge and status state machine) and the rest
> is an **operator check on a real device**, written down in
> [`android/README.md`](android/README.md) rather than faked with a passing test.
> As of **S0010** every device a viewer reads carries a **server-computed presentation state** -
> `no-position` | `live` | `recent` | `stale` - on `GET /v1/positions`, on the SSE stream and on the
> map, and `/v1/positions` now lists **every device in the family** rather than only the ones that
> have reported. So *"this phone has never been set up"* is finally distinguishable from *"this phone
> went quiet an hour ago"*, and no browser derives liveness from a clock the server does not control.
> As of **REBOOT-1** a **reboot no longer ends collection**: the phone remembers that somebody asked
> for it, and a `BOOT_COMPLETED` receiver starts the location foreground service again with nobody
> touching the device. When it cannot - no "Allow all the time" grant, or server settings it cannot
> report to - the collection card reads **"Enabled, not running"** and names which, instead of reading
> like a deliberate stop. This is the first piece of client device behaviour the gate does NOT leave to
> an operator: `make verify-boot-restart` reboots a real Android runtime four times in CI and reads the
> platform's own service list, including one run that disables the boot path and requires the restart
> assertion to go **red**. Two things stay out of reach and are said rather than implied - whether a
> given **OEM skin** delivers the broadcast on a real handset, and a **force-stopped** app, which
> Android delivers no boot signal to at all until somebody opens it.
> It is not a finished tracker, and this README will say so until it is. The wire contract is in
> [`SPEC.md`](SPEC.md); the plan lives in the umbrella at `operations/roadmaps/tracker.md`.

## Stack

| | |
|---|---|
| **Server** | Go - `chi` router, `pgx` pool, `goose` migrations; a single static binary |
| **Database** | **PostgreSQL + PostGIS**, `geography(Point,4326)` |
| **Client** *(collecting + offline-durable; C2)* | native Kotlin - Jetpack Compose, foreground service `type=location`, `FusedLocationProviderClient`, a file-backed durable fix queue flushed by **WorkManager**; AGP 8.5 / Gradle 8.9. See [`android/`](android/) |

**PostGIS is not incidental.** Location math is the product, and it is where a tracker gets things
quietly, confidently wrong. `geography` returns real **metres on the spheroid**; the `geometry` type on
lat/lon returns *degrees*, so the same LA→Paris pair that is `9,124,665 m` comes back as `121.90` - a
number that still looks like an answer. Everything spatial goes through `geography`, and the tests assert
it rather than trusting it.

## The data model

| table | what it holds |
|---|---|
| `families` | the **authorization boundary** - every other row hangs off one |
| `devices` | a phone that reports fixes; holds a **SHA-256 of** its bearer token, never the token |
| `viewers` | a human who watches the map; same credential shape, deliberately a separate table |
| `fixes` | the location history: `geography(Point,4326)`, **partitioned by month** |
| `geofences` | server-side "Places": `geography(Polygon,4326)` |
| `geofence_events` | the **append-only** enter/exit log, one row per crossing (S5) |
| `push_subscriptions` | a viewer's registered push endpoint - where a crossing is delivered (S6) |

Three decisions in there are load-bearing, and each is pinned by a test that fails if it is undone.

**History is expired by dropping a partition, not by deleting rows.** `fixes` is `PARTITION BY RANGE (ts)`,
monthly. Purging a month is then an instant, all-or-nothing `DROP` - a bulk `DELETE` can half-finish and
leave a family's trail corrupted, and this cannot. The cost is that purging is coarse: up to a month of
history outlives its retention date. That is the trade, taken deliberately.

**There is no `DEFAULT` partition.** A fix whose month was never provisioned is **rejected loudly** rather
than swept into a catch-all - because rows in a catch-all can never be expired by dropping a partition, so
retention would quietly leak. The server therefore provisions the current month and the next one **at
start-up**.

**Coordinates are validated in Go, before they reach PostGIS.** This is not belt-and-braces. PostGIS does
not *reject* an out-of-range coordinate - it **silently coerces it into range and stores the result**:

```
ST_Point(47.6062, -122.3321, 4326)     -- Seattle's lat/lon, swapped
NOTICE: Coordinate values were coerced into range [-180 -90, 180 90] for GEOGRAPHY
=> stored at 47.6062E, 57.6679S        -- the South Atlantic
```

The row lands, every constraint passes, and the family is now somewhere they have never been. No `CHECK`
can catch this after the fact - by the time a constraint sees the value it is already a legal point - so
the only place it *can* be caught is before the cast. `store.ValidateLonLat` is that place.

### A geofence edge is a geodesic, not a line on a map

Worth knowing before you draw a Place. On `geography`, a polygon's edges are **great-circle arcs**. A
"square" drawn on the lat/lon grid does not have the edges you drew: its north and south edges follow
*parallels*, and a parallel is not a great circle, so the real edge **bows away from the line you drew** -
about **120 m** at the midpoint of a 1°-wide box.

A fix sitting exactly on the drawn southern edge is therefore genuinely **outside** the Place, and PostGIS
is right to say so. This is not a bug and it is not fixable; it is what a geography polygon *means*. It is
pinned by `TestGeofenceBoundaryContainment` so that the geofencing phase builds on it knowingly rather
than discovering it when somebody's arrival alert never fires.

Containment uses **`ST_Covers`**, which is inclusive of the boundary - a fix exactly on the edge of
"school" counts as at school. (`ST_Contains` is `false` on the boundary, and does not exist for
`geography` at all.)

**A ring that crosses itself is refused**, by a typed error *and* by a `CHECK` constraint on the table.
PostGIS would otherwise store it: a bowtie parses, warns into a NOTICE nobody reads, and yields a polygon
of **zero area** that contains nothing, forever - a Place that looks real in every listing and whose alert
simply never fires.

## The API

Reporting fixes, reading them back, the live map stream, geofences and enter/exit
events, and push alerts: **[`docs/api.md`](docs/api.md)**.

Two things worth knowing before you read it. Enrolment issues a device token and
a viewer token, and they are not the same thing. And a phone that is off is NOT
the same as one that was never set up - the server computes a presentation state
so a reader is never left guessing which it is looking at.


## Running it

```bash
export TRACKER_DB_PASSWORD='choose-something'   # required - see below
docker compose up -d
curl localhost:8080/healthz                     # {"status":"ok","database":"up"}
```

**There is no default database password, and compose refuses to start without one.** Not a placeholder
you are trusted to change later - a refusal, up front. A default would mean a family's location history,
including minors', sitting behind a Postgres superuser password that nobody ever chose. The same rule as
the server's: never boot with a silent default for a secret.

> Use an **alphanumeric** password, or percent-encode it. It is interpolated into the DSN, so `@`, `/`,
> `:`, `#`, `%` and `?` have meaning there - `p@ss` would be parsed as a *hostname*. You will not get a
> wrong answer if you ignore this (the server fails to connect and says so, loudly), just a confusing one.

> **Changing `TRACKER_DB_PASSWORD` later does not rotate the password.** This is a sharp edge in
> Postgres, not in tracker: the variable is read **only by `initdb`**, on the very first start against an
> empty data volume. Afterwards it is ignored - so editing it leaves tracker's DSN disagreeing with the
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
| `TRACKER_TLS_KEY_FILE` | for TLS | *none* | PEM private key (a secret - mounted, never committed) |
| `TRACKER_ALLOW_PLAINTEXT` | no | *unset* | `1` to run **without** TLS (dev, or behind a TLS-terminating proxy). Required if no cert/key is set, or the server refuses to start |
| `TRACKER_RETENTION_DAYS` | no | `0` | drop `fixes` older than this many days on a daily timer; `0` = keep forever (no purge) |
| `TRACKER_LIVE_WINDOW_SECONDS` | no | `120` | at or under this age a device is `live` (whole **seconds**, no unit suffix) |
| `TRACKER_STALE_WINDOW_SECONDS` | no | `900` | past this age a device is `stale` (whole **seconds**); must be strictly greater than the live window |
| `TRACKER_PUSH_PROVIDER` | no | *disabled* | `fcm` \| `unifiedpush` - the S6 push backend; unset = no push |
| `TRACKER_FCM_PROJECT_ID` | when `fcm` | *none* | the Firebase project id the FCM v1 endpoint is scoped to |
| `TRACKER_FCM_CREDENTIALS_FILE` | when `fcm` | *none* | path to the Google service-account JSON key (mounted, never committed) |

**The server refuses to start** without a valid `TRACKER_DATABASE_URL` - no default, no empty string, no
guess. A tracker pointed at the wrong database is worse than one that would not boot, because the first
is discovered in production.

**It also refuses to start in plaintext by accident (S7).** With neither a `TRACKER_TLS_CERT_FILE` /
`TRACKER_TLS_KEY_FILE` pair nor `TRACKER_ALLOW_PLAINTEXT=1`, it will not boot: location is maximally
sensitive, so serving it unencrypted must be a *decision*, never an unset variable. A half-configured
pair (cert without key, or vice versa) is refused too - that is a deploy that believes it is encrypted
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
also refuses to start - `TRACKER_PUSH_PROVIDER=fcm` without a project id and a credentials file is a
start-up error, not a server that boots and silently drops every alert. `unifiedpush` needs no global
config (the endpoint is per-subscription). The FCM credentials file is **mounted at deploy time and never
committed** - it is a secret.

## Security & retention (S7)

**TLS.** Point the server at a certificate and key, or run behind a proxy that terminates TLS:

```bash
export TRACKER_TLS_CERT_FILE=/etc/tracker/tls.crt
export TRACKER_TLS_KEY_FILE=/etc/tracker/tls.key     # a secret - mount it, never commit it
# …or, behind a TLS-terminating reverse proxy / for local dev:
export TRACKER_ALLOW_PLAINTEXT=1
```

**Lint before you deploy.** `config-lint` fails on a plaintext endpoint *or* a checked-in secret, and it
does **not** accept `ALLOW_PLAINTEXT` - a production config must terminate TLS:

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
roles from - `tracker_readonly` (SELECT only) and `tracker_writer` (DML, no DDL) - so a reporting or
analytics connection can never alter or delete a family's trail. The server's own connection is the
schema **owner** (it provisions and drops partitions). See [`THREAT-MODEL.md`](THREAT-MODEL.md) §6.

## Development

`make check` is the gate. The full detail - configuration, the user-interface
gate, and the pinned references - is in
**[`docs/development.md`](docs/development.md)**, and the rules a change is held
to are in [`CLAUDE.md`](CLAUDE.md).


## Known limitations

This is a spine, not a finished product. What does not exist yet, what is
approximate, and what will not scale is written down rather than implied:
**[`docs/limitations.md`](docs/limitations.md)**.


## Privacy - the honest boundary

**Location is not end-to-end encrypted, and this project will not claim it is.** The server renders the
map and runs the spatial queries, so it necessarily holds plaintext positions. What self-hosting buys you
is *whose* server that is: the plaintext sits on yours rather than a company's. Anyone with
administrative access to it can read the family's location history - this is **inherent, not a fixable
gap**. The full, honest threat model - what tracker defends against (network eavesdropping, database
theft of hashed tokens, third-party-cloud exposure) and what it cannot (a compromised server or
malicious admin) - is in [`THREAT-MODEL.md`](THREAT-MODEL.md).

**Push notifications carry no location.** A crossing alert says only *"Alice's phone arrived at School"* -
the device name and Place name the family themselves chose, and the direction. It never carries a
coordinate, an accuracy, or any raw fix datum, so a family's precise whereabouts never transit a push
provider's servers (§5.3). A push endpoint is a routing address the phone can rotate, not a credential and
not location data, so it is stored in the clear - losing it leaks "this endpoint can be pushed to", not a
family's movements.
