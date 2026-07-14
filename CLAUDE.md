# CLAUDE.md — tracker

Guidance for Claude Code working in this repo. This file governs here; the umbrella's `CLAUDE.md`
governs the umbrella.

## What this is

A self-hosted family location tracker: a Kotlin Android client reports fixes to a Go + PostGIS server
that stores history, serves a live map, and fires geofence alerts. **Read `README.md` for the honest
status** — today this is the server spine and nothing more.

The plan, its acceptance criteria and its phase order live in the umbrella at
`operations/roadmaps/tracker.md`. **That roadmap is the contract.** Build the phase you were given;
don't build the next one because it seems easy.

## The gate

```bash
make check     # gofmt · vet · build · test -race · staticcheck · govulncheck
make smoke     # the real compose stack; asserts /healthz answers 200
```

`make check` **is** the gate. CI runs exactly this target and so does the umbrella's
`scripts/verify.sh tracker` — one gate, defined once, in the `Makefile`. Tool versions are pinned there
and **nowhere else**: restating them in `ci.yml` is how CI silently drifts away from the gate a human
runs.

### The rule that is not negotiable: the tests FAIL, they never SKIP

`internal/testsupport` starts a **real PostGIS** with testcontainers, and calls `t.Fatalf` — never
`t.Skip` — when it cannot.

This is deliberate and it is the most important convention in the repo. tracker's correctness *is*
spatial SQL: `geography`-vs-`geometry` units, lon/lat axis order, on-boundary containment, whether the
planner actually uses the GiST index. **None of that can be tested against a mock**, because the thing
under test is PostGIS's behaviour — a fake would only ever assert what we already believed. So a gate
that skips when the database is missing reports green while proving nothing, which is worse than no gate:
it manufactures confidence.

If you are tempted to add a `t.Skip` so the tests pass without Docker, **you are removing the gate.**
Fix the environment instead.

## Spatial rules (the bedrock — roadmap §5.1)

Location bugs are silent, confident and wrong, which is the worst combination in a product whose entire
job is telling you where your family is. These are not style preferences:

- **Store fixes as `geography(Point,4326)`, never `geometry`.** On lat/lon, `geography` gives metres on
  the spheroid; `geometry` gives *degrees* — LA→Paris is `9,124,665 m` as geography and `121.90` as
  geometry, and the second one still looks like a number you could use.
- **Axis order is longitude FIRST**: `ST_Point(lon, lat, 4326)`. Swapping it is the classic silent bug,
  and it is guarded by a permanent regression test. Never remove that test.
- **`ST_MakePoint` assigns no SRID** (you get `0`). Use `ST_Point(..., 4326)` or wrap in `ST_SetSRID`.
- **Distance/radius must be computed on `geography`** — on `geometry`, `ST_DWithin(..., 100)` means 100
  *degrees*, which is nonsense, not 100 metres.
- **On the boundary, `ST_Contains` is FALSE.** Use `ST_Intersects` / `ST_Covers` when a fix exactly on a
  geofence edge must count as inside. On `geography`, `ST_Contains` does not exist *at all* — reaching for
  it forces a cast to `geometry`, which is a one-way door back into degree-space. The rule is `ST_Covers`.
- **GiST index every geography column.** `ST_Distance` is *not* index-accelerated: pre-filter with
  `ST_DWithin`, then rank by `ST_Distance`.
- **PostGIS does not reject a bad coordinate — it COERCES it.** An out-of-range latitude (the signature of
  a lon/lat swap) is silently folded back into range and *stored*: swapped Seattle lands in the South
  Atlantic, with every constraint passing. No `CHECK` can catch this, because the corruption happens
  inside the cast. **Validate lon/lat in Go before it reaches SQL** — `store.ValidateLonLat` — and never
  delete that call because "the database checks it". The database launders it.
- **A geography polygon's edges are GEODESICS, not lines on a lat/lon grid.** A "square" drawn on the grid
  has north/south edges that follow parallels, which are not great circles, so the true edge bows ~120 m
  away from the drawn one on a 1°-wide box. A fix on the drawn edge is genuinely *outside*. This is what a
  geography polygon means; it is not fixable, and geofencing must be built knowing it.

## Fail-safe stance

Ambiguous or malformed input gets a **typed error** — never a stored guess. A missing optional field is
*absent*, never fabricated. A spatial result that looks wrong **fails the gate**; it is never shipped as
a confident wrong answer. The server **refuses to start** on missing or invalid required config rather
than booting with a silent default.

## Conventions

- **Conventional Commits.** Commit as `Noah Schatz <noah.lane.schatz@gmail.com>`. **Never** add an AI
  co-author or `Co-Authored-By` trailer.
- **No secrets, ever.** The DSN password in `docker-compose.yml` and the test credentials in
  `internal/testsupport` are synthetic, local-only placeholders. Keep them that way.
- **Documentation follows code.** A change to the public surface, the stack or the status is not done
  until `README.md` says what is now true — including the status banner, which must keep being honest
  about what does not exist yet.
