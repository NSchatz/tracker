# tracker

A **self-hosted, Life360-style family location tracker**. An Android phone reports its location to a
server *you* run; family members see each other on a live map and get alerts when someone arrives at or
leaves a place you have defined.

> **Status: early. The server spine and its data model.**
> This repo has config, `/healthz`, a database pool, and — as of S1 — the **spatial schema**: families,
> devices, viewers, monthly-partitioned `fixes`, and geofences, with the query helpers for proximity and
> containment.
> There is still **no ingestion endpoint, no read API, no map and no Android client**; those are the
> phases that follow. **Nothing writes to these tables over the network yet.** It is not usable as a
> tracker today, and this README will say so until it is. The plan lives in the umbrella at
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

**The server refuses to start** without a valid `TRACKER_DATABASE_URL` — no default, no empty string, no
guess. A tracker pointed at the wrong database is worse than one that would not boot, because the first
is discovered in production.

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

- **No network surface for the data model.** The schema and its queries exist; nothing ingests or serves
  them. That is the next phase, not an oversight.
- **Partitions are provisioned at start-up, with a two-month lookahead.** A server process that runs
  continuously for longer than that would eventually reach an unprovisioned month and start **rejecting
  fixes** (loudly — they are never silently misfiled). A restart fixes it; a maintenance tick, alongside
  the retention job, is the real answer and belongs with it.
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
