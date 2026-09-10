# CLAUDE.md - tracker

Guidance for Claude Code working in this repo. This file governs here; the umbrella's `CLAUDE.md`
governs the umbrella.

## What this is

A self-hosted family location tracker: a Kotlin Android client reports fixes to a Go + PostGIS server
that stores history, serves a live map, and fires geofence alerts. **Read `README.md` for the honest
status** - today this is the server spine and nothing more.

The plan, its acceptance criteria and its phase order live in the umbrella at
`operations/roadmaps/tracker.md`. **That roadmap is the contract.** Build the phase you were given;
don't build the next one because it seems easy.

## The gate

```bash
make check            # BOTH stacks: check-go + android
make check-go         # gofmt · vet · build · test -race · staticcheck · govulncheck · pin-check · prose-check
make android          # ./gradlew assembleDebug lintDebug testDebugUnitTest
make pin-check        # the supply-chain pin gate alone - no daemon, no SDK, no network
make prose-check      # the comment-density gate alone, and prose-check-test for its suite
make smoke            # the real compose stack; asserts /healthz answers 200
```

**The user-interface gate is a separate set of targets, and it is not optional.**

```bash
make verify-ui          # the browser map, in a real browser engine
make verify-ui-android  # the Android screen, on a BOOTED emulator
make verify-ui-refusal  # both of those, with their prerequisite removed, must refuse
make verify-ui-record   # FRONTEND-CONVENTIONS-RECORD.md, against what actually ran
```

tracker ships two user interfaces and the umbrella's `documentation/frontend-conventions.md` binds
both. Clause F2 is the one to internalise: **a claim about what a person SEES is graded by the runtime
that draws it.** A grep cannot decide what a CSS rule applies to, what won the cascade, or what was
shown rather than merely built. Never "verify" a UI change by reading `map.html` or `strings.xml` -
run the route. A `strings.Contains` assertion over the served page is the move this route exists to
close.

Two rules go with it, and both are enforced:

- **Every assertion must be shown able to FAIL.** Each check re-runs against the same surface
  mutated to break exactly the claim it measures, and the route fails if fewer demonstrations ran
  than there are claims. Add a rendered claim, add its mutation in the same change. On Android the
  pairing is the NAME: a claim `X` is evidence only while `X_demonstration` passes beside it, and
  both `verify-ui-android` and `verify-ui-record` refuse by name when one goes missing.
- **These routes refuse; they never skip.** No engine, no SDK, no `/dev/kvm`, no booted device: exit
  non-zero naming the prerequisite. Same stance as the PostGIS tests. `make verify-ui-refusal` is
  what keeps that from rotting, and it drives each absence separately rather than letting one
  refusal stand in for the rest.

**`@Ignore` in the instrumented suite is fenced shut.** A case stops running only once
`FRONTEND-CONVENTIONS-RECORD.md` carries a DEFERRAL row naming the item that took the clause over, and
`splitPairs` in `internal/uiverify/record.go` - the list of pairs a deferral is legal on - is EMPTY, so
no row anyone can write makes an `@Ignore` legal. `verify-ui-record` and `verify-ui-android` compare
the deferred set against the `@Ignore`d set both ways, require every parked case to name its owner, and
refuse a claim parked without its demonstration or the reverse. Re-opening the route is a source change
with a name on it.

**A claim is graded on the UNMUTATED screen, and that is checked.** A JUnit file says a case passed;
it cannot say what the case launched. So `uiverify android` refuses any case that is not a
`_demonstration` and yet selects a `UiMutation` - otherwise a claim points at a broken screen, passes,
and is reported as evidence about the screen people use.

`make check` IS the gate: CI runs exactly this target, so tool versions are pinned in the `Makefile`
and nowhere else. Restating them in `ci.yml` is how CI drifts from the gate a human runs.

The one exception is the Go toolchain, which three different builds need: CI provisions it
(`GO_VERSION`), the `Dockerfile` bakes it into the shipped binary, and `go.mod`'s `toolchain` directive
gives a local `go build`. `internal/toolchain` asserts all three name the same version and that
`go.mod`'s `go` directive (a language floor) never climbs above it. It runs inside `make test`, so a
half-landed bump fails `make check` naming the files that disagree.

**`govulncheck` records, it never ignores.** An advisory it reports is either remediated at source - bump
the module, raise the toolchain to the `Fixed in` version - or written down in
`.govulncheck-suppressions.yaml` with `id`, `reason`, `reachability` and `recorded`. That file is the only
suppression surface there is. Unrecorded fails; a record for an advisory that **has** a fix fails, because
that case is remediated, not recorded; a record the current run no longer reports is **stale** and fails; a
malformed record fails naming the entry; and any `govulncheck` exit status other than the one a run that
produced a report exits with fails with the tool's own error. Do not reach for `|| true`, a `-` prefix, a
report-only mode or a floating `GOVULNCHECK_VERSION` - `internal/vulngate` and its self-tests exist to make
each of those visible.

**"An advisory it reports" is all three levels** - a traced symbol, an imported package, a required
module. The text report splits those across headings and hides two behind `-show verbose`, so the gate
reads the `-format json` stream and refuses one whose `config` asked for less than
`scan_level: symbol` / `scan_mode: source`. Reading the text report reads PART of the verdict.
`.govulncheck-suppressions.yaml` is the only thing allowed to make the verdict smaller: narrowing the
invocation for a green gate is `|| true` spelled differently.

**Every pinnable reference is pinned, and `internal/pingate` keeps it that way.** Images carry a tag
AND a digest, actions a commit SHA with the version in a trailing comment, the Gradle wrapper a
`distributionSha256Sum`, and no manifest carries a dynamic version. An image counts WHEREVER this repo
names one, Go source included: `testsupport.PostGISImage` is the database every spatial assertion is
measured against and is pinned to the same `tag@digest` as the `postgis` service, or the stack and the
tests stop being the same database.

The gate reads files and asks no registry anything, so it reaches the same verdict airgapped as in CI:
rot is discovered when a build fails, not by a scheduled liveness workflow that reds unrelated PRs
whenever a third party is down. Five deliberately broken trees under
`internal/pingate/testdata/refusals` prove it still bites, and the check fails if fewer than five go
red or if any category has quietly stopped finding anything. Do not "fix" those trees and do not add a
`pin-check` step to `ci.yml` - `make check` already reaches it. When you move a pin, move the
provenance row in `README.md` with it, and resolve a new pin from the publisher ONCE: a pin resolved
twice can silently differ.

**Comments earn their place by saying WHY, and `make prose-check` holds the line.** `cmd/prosecheck`
measures every tracked Go file's prose from the GO TOKEN STREAM (`go/parser`, `go/scanner`, `go/ast`)
and refuses a file over the ceiling `COMMENT-DENSITY-RECORD.md` records, naming the file, its ratio
and the ceiling - and refuses a record that no longer agrees with the gate, a file it cannot read or
parse, and a sweep that measured nothing. Like `pin-check` it needs nothing and rides `check-go`
rather than a second step in `ci.yml`. The thresholds are THIS repository's own measured baseline,
derived in the record: the ceiling is the smallest whole multiple of five percentage points no
eligible file exceeds (capped at fifty), the band ten points below. Raising one is a source change
with a name on it, and `internal/prosegate` fails if the record and the gate ever disagree.

Count by TOKENS, never by pattern: a comment marker inside a string literal, and the closing
delimiter of a multi-line raw string, read exactly like prose to anything matching on lines. A
directive is CODE - build constraints in both forms, any `//go:` directive, `//line`, `//nolint` - so
no run can demand a deletion that changes what compiles. A generated file leaves the measurement
entirely and a leading licence notice is in neither count. Four deliberately broken trees under
`internal/prosegate/testdata/refusals` prove the gate still bites; do not fix them.

**The Claude Code tool layer refuses to write `android/gradle/wrapper/gradle-wrapper.properties`**, and
`.npmrc` with it: they are on its built-in sensitive-file list, so `Edit` and `Write` are both denied
there no matter what an approved spec says. Do not route around that with `sed`, `python3` or
`git apply`. The standing rule in the umbrella is that such a file is applied by hand, at the root, on
the item's branch, and the session's job is to finish everything else and say precisely which file, line
and value are outstanding. The wrapper's `distributionSha256Sum` is the pin that reached tracker this
way; the next one will too.

As of **C0** the gate carries **both stacks**: the Go server (above) **and** the Android client
(`android/` - assemble + Android Lint + JVM unit tests). So the gate env now needs **both** a reachable
Docker daemon (for the PostGIS tests) **and** a JDK 17 + an Android SDK. The Android half resolves the
SDK from `ANDROID_SDK_ROOT` (or `ANDROID_HOME`) and - like the PostGIS tests - **fails loudly when it is
missing, never skips**. The one-time rootless SDK install is documented in
[`android/README.md`](android/README.md). Android version pins (AGP, Kotlin, SDK levels) live in
`android/gradle/libs.versions.toml` and `android/app/build.gradle.kts`, and Gradle in the committed
wrapper - the same "pinned in one place, never restated in `ci.yml`" rule as the Go tools.

### The rule that is not negotiable: the tests FAIL, they never SKIP

`internal/testsupport` starts a **real PostGIS** with testcontainers, and calls `t.Fatalf` - never
`t.Skip` - when it cannot.

The most important convention in the repo. tracker's correctness IS spatial SQL: `geography`-vs-
`geometry` units, lon/lat axis order, on-boundary containment, whether the planner uses the GiST
index. None of that can be tested against a mock, because the thing under test is PostGIS's behaviour
and a fake asserts only what we already believed. A gate that skips when the database is missing
reports green while proving nothing, which is worse than no gate.

Adding a `t.Skip` so the tests pass without Docker REMOVES THE GATE. Fix the environment.

## Spatial rules (the bedrock - roadmap §5.1)

Location bugs are silent, confident and wrong, which is the worst combination in a product whose entire
job is telling you where your family is. These are not style preferences:

- **Store fixes as `geography(Point,4326)`, never `geometry`.** On lat/lon, `geography` gives metres on
  the spheroid; `geometry` gives *degrees* - LA→Paris is `9,124,665 m` as geography and `121.90` as
  geometry, and the second one still looks like a number you could use.
- **Axis order is longitude FIRST**: `ST_Point(lon, lat, 4326)`. Swapping it is the classic silent bug,
  and it is guarded by a permanent regression test. Never remove that test.
- **`ST_MakePoint` assigns no SRID** (you get `0`). Use `ST_Point(..., 4326)` or wrap in `ST_SetSRID`.
- **Distance/radius must be computed on `geography`** - on `geometry`, `ST_DWithin(..., 100)` means 100
  *degrees*, which is nonsense, not 100 metres.
- **On the boundary, `ST_Contains` is FALSE.** Use `ST_Intersects` / `ST_Covers` when a fix exactly on a
  geofence edge must count as inside. On `geography`, `ST_Contains` does not exist *at all* - reaching for
  it forces a cast to `geometry`, which is a one-way door back into degree-space. The rule is `ST_Covers`.
- **GiST index every geography column.** `ST_Distance` is *not* index-accelerated: pre-filter with
  `ST_DWithin`, then rank by `ST_Distance`.
- **PostGIS does not reject a bad coordinate - it COERCES it.** An out-of-range latitude (the signature of
  a lon/lat swap) is silently folded back into range and *stored*: swapped Seattle lands in the South
  Atlantic, with every constraint passing. No `CHECK` can catch this, because the corruption happens
  inside the cast. **Validate lon/lat in Go before it reaches SQL** - `store.ValidateLonLat` - and never
  delete that call because "the database checks it". The database launders it.
- **A geography polygon's edges are GEODESICS, not lines on a lat/lon grid.** A "square" drawn on the grid
  has north/south edges that follow parallels, which are not great circles, so the true edge bows ~120 m
  away from the drawn one on a 1°-wide box. A fix on the drawn edge is genuinely *outside*. This is what a
  geography polygon means; it is not fixable, and geofencing must be built knowing it.

## Fail-safe stance

Ambiguous or malformed input gets a typed error, never a stored guess. A missing optional field is
absent, never fabricated. A spatial result that looks wrong FAILS THE GATE rather than shipping as a
confident wrong answer. The server refuses to start on missing or invalid required config rather than
booting with a silent default.

## Conventions

- **Conventional Commits.** Commit as `Noah Schatz <noah.lane.schatz@gmail.com>`. **Never** add an AI
  co-author or `Co-Authored-By` trailer.
- **No secrets, ever.** The DSN password in `docker-compose.yml` and the test credentials in
  `internal/testsupport` are synthetic, local-only placeholders. Keep them that way.
- **Documentation follows code.** A change to the public surface, the stack or the status is not done
  until `README.md` says what is true, status banner included.
- Plain hyphens only - no en or em dashes. No dates and no narrated history here; git holds that.
