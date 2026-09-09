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
make check     # BOTH stacks: check-go + android
make check-go  # gofmt · vet · build · test -race · staticcheck · govulncheck · pin-check
make android   # ./gradlew assembleDebug lintDebug testDebugUnitTest
make pin-check # the supply-chain pin gate alone - no daemon, no SDK, no network
make smoke     # the real compose stack; asserts /healthz answers 200
```

**The user-interface gate is a separate set of targets, and it is not optional.**

```bash
make verify-ui          # the browser map, in a real browser engine
make verify-ui-android  # the Android screen, on a BOOTED emulator
make verify-ui-refusal  # both of those, with their prerequisite removed, must refuse
make verify-ui-record   # FRONTEND-CONVENTIONS-RECORD.md, against what actually ran
```

tracker ships two user interfaces and the umbrella's `documentation/frontend-conventions.md` binds
both. Its clause F2 is the one to internalise: **a claim about what a person SEES is graded by the
runtime that draws it.** Source text may never grade a rendered property — a grep cannot decide what
a CSS rule applies to, what won the cascade, or what was shown rather than merely built. So do not
"verify" a UI change by reading `map.html` or `strings.xml`; run the route. If you are tempted to add
a `strings.Contains` assertion over the served page for something a person sees, that is the move
this whole route exists to close.

Two rules go with it, and both are enforced:

- **Every assertion must be shown able to FAIL.** Each check is re-run against the same surface
  mutated to break exactly the claim it measures (a substitution in the served bytes; a debug-only
  `UiMutation` for the app), and the route fails if fewer demonstrations ran than there are claims.
  If you add a rendered claim, add its mutation in the same change. On the Android side that pairing
  is the NAME: an instrumented claim `X` is evidence only while a case `X_demonstration` passes
  beside it, and both `make verify-ui-android` and `make verify-ui-record` refuse by name when one
  goes missing. Do not rename half a pair.
- **These routes refuse; they never skip.** No engine, no SDK, no `/dev/kvm`, no booted device: exit
  non-zero naming the prerequisite. Same stance as the PostGIS tests. `make verify-ui-refusal` is
  what keeps that from rotting.

`make check` **is** the gate. CI runs exactly this target and so does the umbrella's
`scripts/verify.sh tracker` — one gate, defined once, in the `Makefile`. Tool versions are pinned there
and **nowhere else**: restating them in `ci.yml` is how CI silently drifts away from the gate a human
runs.

The one exception is the **Go toolchain**, which CI provisions (`GO_VERSION` in `ci.yml`), the
`Dockerfile` bakes into the shipped binary, and `go.mod`'s `toolchain` directive gives a local
`go build` — three different builds, so one copy will not do. `internal/toolchain` asserts all three name
the same version and that `go.mod`'s `go` directive (a *language floor*) never climbs above it. It runs
inside `make test`, so a half-landed bump fails `make check` naming the files that disagree.

**`govulncheck` records, it never ignores.** An advisory it reports is either remediated at source — bump
the module, raise the toolchain to the `Fixed in` version — or written down in
`.govulncheck-suppressions.yaml` with `id`, `reason`, `reachability` and `recorded`. That file is the only
suppression surface there is. Unrecorded fails; a record for an advisory that **has** a fix fails, because
that case is remediated, not recorded; a record the current run no longer reports is **stale** and fails; a
malformed record fails naming the entry; and any `govulncheck` exit status other than the one a run that
produced a report exits with fails with the tool's own error. Do not reach for `|| true`, a `-` prefix, a
report-only mode or a floating `GOVULNCHECK_VERSION` — `internal/vulngate` and its self-tests exist to make
each of those visible.

**"An advisory it reports" is all three levels.** `govulncheck` reports a vulnerable symbol it traced a
call path to, a vulnerable package that is imported, and a vulnerable module that is merely required; its
text report puts those under three different section headings and prints two of them only under
`-show verbose`. So the gate reads the `-format json` stream instead, where every advisory is a `finding`
object at whatever depth it was traced to, and it refuses a stream whose `config` says the scan asked for
less than `scan_level: symbol` / `scan_mode: source`. Reading the text report is reading **part** of the
verdict, and `.govulncheck-suppressions.yaml` is the only thing allowed to make the verdict smaller. Do
not narrow the invocation to get a green gate — that is the same move as `|| true`, spelled differently.

**Every pinnable reference is pinned, and `internal/pingate` is what keeps it that way.** Images carry a
tag AND a digest, actions carry a commit SHA with the version in a trailing comment, the Gradle wrapper
carries a `distributionSha256Sum`, and no manifest carries a dynamic version - the org's pinning
conventions, decided by the operator on 2026-09-07. An image is read **wherever this repo names one**,
Go source included: `testsupport.PostGISImage` is the database every spatial assertion is measured
against, it is pinned to the same `tag@digest` as the `postgis` service in `docker-compose.yml`, and the
two move together or the stack and the tests stop being the same database. The gate reads files and **asks no registry
anything**: it reaches the same verdict airgapped as it does in CI, because P8 is explicit that rot is
discovered when a build fails and *not* by a scheduled liveness workflow that reds unrelated pull
requests whenever a third party is down. Every refusal names the file, the line, the offending reference
and the broken clause. Five deliberately broken trees under `internal/pingate/testdata/refusals` are the
proof it still bites, and the check fails if fewer than five go red or if any category it examines has
quietly stopped finding anything. Do not "fix" those trees, do not add a `pin-check` step to `ci.yml`
(`make check` already reaches it), and when you move a pin, move the provenance row in `README.md` with
it. New pinned reference to resolve? Get the value from the publisher once and write it down; a pin
resolved twice can silently differ.

**The Claude Code tool layer refuses to write `android/gradle/wrapper/gradle-wrapper.properties`**, and
`.npmrc` with it: they are on its built-in sensitive-file list, so `Edit` and `Write` are both denied
there no matter what an approved spec says. Do not route around that with `sed`, `python3` or
`git apply`. The standing rule in the umbrella is that such a file is applied by hand, at the root, on
the item's branch, and the session's job is to finish everything else and say precisely which file, line
and value are outstanding. The wrapper's `distributionSha256Sum` is the pin that reached tracker this
way; the next one will too.

As of **C0** the gate carries **both stacks**: the Go server (above) **and** the Android client
(`android/` — assemble + Android Lint + JVM unit tests). So the gate env now needs **both** a reachable
Docker daemon (for the PostGIS tests) **and** a JDK 17 + an Android SDK. The Android half resolves the
SDK from `ANDROID_SDK_ROOT` (or `ANDROID_HOME`) and — like the PostGIS tests — **fails loudly when it is
missing, never skips**. The one-time rootless SDK install is documented in
[`android/README.md`](android/README.md). Android version pins (AGP, Kotlin, SDK levels) live in
`android/gradle/libs.versions.toml` and `android/app/build.gradle.kts`, and Gradle in the committed
wrapper — the same "pinned in one place, never restated in `ci.yml`" rule as the Go tools.

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
