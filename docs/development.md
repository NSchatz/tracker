# Development

The gate, configuration, the user-interface gate and the pinned references.
Moved out of `README.md`, unchanged. `CLAUDE.md` carries the rules a change is
held to; this is the detail behind them.

## Development

```bash
make check     # THE gate: gofmt · vet · build · test -race · staticcheck · govulncheck · pin-check · Android
make pin-check # the supply-chain pin gate alone - no daemon, no SDK, no network
make smoke     # brings the real stack up and asserts /healthz answers 200

make verify-ui          # the browser map's rendered claims, in a real browser engine
make verify-ui-android  # the Android screen's rendered claims, on a booted emulator
make verify-ui-refusal  # both of the above, with their prerequisite removed, must refuse
make verify-ui-record   # the F1-F11 record, against what those routes actually ran
```

`make check` is what CI runs and what the umbrella's `scripts/verify.sh tracker` runs - one gate, defined
once, in the `Makefile`.

### The user-interface gate

tracker ships **two** user interfaces - the browser map and the Android screen - and a claim about
what a person *sees* is graded by the runtime that draws it, never by searching HTML, CSS, Kotlin or
a compiled resource table. A text search cannot decide what a CSS rule applies to, what won the
cascade, or what was shown rather than merely built.

So `make verify-ui` drives **Chromium** over the production `/map` and `/static` handlers and reads
every number back out of the live engine: contrast from the resolved colours in both themes, focus
indicators from a pixel diff of the rendering, target sizes from laid-out boxes, accessible names
from the engine's own accessibility tree, network origins and policy violations from the browser's
own records. `make verify-ui-android` runs an **instrumented** suite on a booted Android emulator and
reads back what Compose actually laid out and drew.

**No assertion in either route may pass vacuously.** Each one is also re-run against the same surface
mutated to break exactly the claim it measures - one substitution in the bytes the server served, or
a debug-only `UiMutation` that is inert in a release build - and the route fails if fewer
demonstrations ran than there are claims. A check that cannot go red is not evidence.

The two routes count that differently because the demonstrations reach them differently. The browser
route watches each check go red inside its own process, so `uiverify.Summarise` compares the counts
directly. On the emulator a claim and its demonstration are two separate instrumented cases, and
`gradlew connectedDebugAndroidTest` is green whenever the cases that *ran* passed - so the naming
convention carries the pairing (`X` and `X_demonstration`, which passes only when `X`'s assertion
failed against the mutated screen) and `make verify-ui-android` finishes by reading the emulator's own
JUnit results back and refusing unless every claim the record names ran **and** carries a passing
demonstration beside it. Deleting, renaming or `@Ignore`-ing a demonstration fails the route by name.

**They refuse; they never skip.** No browser engine, no Android SDK, no `/dev/kvm`, no booted device:
each is an exit-non-zero naming the criterion, the missing prerequisite and how to obtain it, exactly
as `make android` does for a missing SDK and `make test` for a missing Docker daemon. `make
verify-ui-refusal` is the check that keeps that true, and it DRIVES each of the seven absences
separately - the browser engine and its driver, then the Android SDK, the emulator package, the
system image, the AVD and a device that never finishes booting - rather than trusting one refusal to
stand for all of them, and it prints its own count of them. The clause-by-clause record, for both
surfaces, is [`FRONTEND-CONVENTIONS-RECORD.md`](FRONTEND-CONVENTIONS-RECORD.md), and `make
verify-ui-record` refuses a record naming an assertion that did not actually run.

**Every clause is answered on both surfaces, and no clause is answered by an item name.** F1 and F10
on the Android screen - the platform accessibility sweep in both themes, and operability without a
pointer - are carried by named assertions in the record, each with its own mutation and each shown
going red. The route that would let a record cell answer a clause with an item instead of evidence is
fenced shut rather than merely unused: the list of clause/surface pairs on which that is legal is
**empty** in `internal/uiverify/record.go`, so there is no cell anyone can write which makes an
`@Ignore` in the instrumented suite legal, and adding one turns `make verify-ui-record` red. Opening
that route for a future split is a source change with a name on it.

**One accessibility claim is owned elsewhere and is graded by nothing here.** "Every control has a
non-empty spoken name" belongs to `S0076-tracker-android-spoken-name`: the check that answered it
read a name out of whatever node sat inside a control's *bounds*, so a control with no name of its
own borrowed a neighbour's label. F1 and F10 stay answered by the assertions that remain; what this
repository does not assert is that a screen reader announces every control on the Android screen.

**The paragraphs that used to stand on each surface live in a document instead**, one per surface:
[`/static/map-explained.html`](internal/server/static/map-explained.html) for the map, which the page
links to once per region, and [`/static/app-explained.html`](internal/server/static/app-explained.html)
for the Android screen, which is the repository copy of what the app's own explanation destination
renders from its string resources; the client has no web view and does not load it. Both are served
files rather than Markdown, because the map's links have to RESOLVE in a browser and the
credential-free surface is fixed at the health check, the map shell and its static assets.
`go run ./cmd/uiverify docs` refuses either document that has lost a claim which left a surface.

These are **not** folded into `make check`: that target is the gate a human runs on a laptop, and it
does not need a browser or an emulator. CI runs both.

**`make check` needs a reachable Docker daemon.** The tests start a real PostGIS with
`testcontainers-go`, and **they fail rather than skip if they cannot**. That is deliberate: this
project's correctness *is* spatial SQL, which cannot be tested against a mock - a mock would only ever
assert what we already believed. A gate that skips its bedrock when the database is missing reports green
while proving nothing, so a missing daemon is an error here, not a pass.

**The vulnerability gate records; it never ignores.** `govulncheck` runs on every `make check`, and an
advisory it reports is either **remediated at source** - bump the implicated module, raise the Go
toolchain to the version it names in `Fixed in` - or **written down** in
[`.govulncheck-suppressions.yaml`](.govulncheck-suppressions.yaml) with a reason, the reported call path
and the date it was recorded. There is no third option and no other suppression surface. An unrecorded
advisory fails; a record for an advisory `govulncheck` says *has* a fix fails, because that case is
remediated and not recorded; a record whose advisory the current run no longer reports fails, because a
suppression must not outlive the advisory it was written for; a malformed record fails naming the entry,
never skipped and never honoured; and any `govulncheck` exit status other than the one a run that
produced a report exits with fails with the tool's own error, because a tool that could not run has not
told you the code is clean. `internal/vulngate` enforces that, and its own tests - which run inside
`make check` - prove it still turns red on demand.

**"An advisory it reports" means all three levels.** `govulncheck` reports at three depths - it traced a
call path to a vulnerable *symbol*, it found a vulnerable *package* imported, or it found a vulnerable
*module* required - and its text report splits those across `=== Symbol Results ===`, `=== Package
Results ===` and `=== Module Results ===`, printing the last two only under `-show verbose`. The gate
therefore reads the tool's `-format json` stream, where every advisory arrives as a `finding` object
whatever depth it was traced to. That is the one flag it passes, and it *widens* what the gate sees:
reading the text report means reading part of the verdict, and the suppression file is the only thing
allowed to make the verdict smaller.

**The Go toolchain is stated in three files, and they are checked against each other.** CI provisions it
(`GO_VERSION` in `.github/workflows/ci.yml`), the `Dockerfile`'s builder image bakes it into the shipped
binary, and `go.mod`'s `toolchain` directive is what a local `go build` downloads and runs; those are
three different builds, so one copy will not do. `internal/toolchain` asserts all three name the same
version and that `go.mod`'s `go` directive - a *language floor*, not a toolchain pin - never climbs above
it. It runs inside `make check`, so a half-landed bump fails and names the files that disagree, instead of
leaving CI to prove things with a compiler production never runs. That matters more than it sounds:
`govulncheck` scans the standard library of whichever toolchain executes it, so a split pin is a
vulnerability gate that answers differently depending on where it ran.

### Pinned references

Every image this repo pulls, every action its workflow runs and the Gradle distribution its wrapper
downloads is pinned by **tag *and* digest**, per the org's pinning conventions (operator decision,
2026-09-07). The tag is what a human reads; the digest is what actually resolves. A tag alone is a
**floating** reference - its publisher moves it, and two of the four actions below had already moved
under this workflow before anyone wrote a SHA down.

`internal/pingate` enforces it. It runs as `make pin-check` and inside `make check`, it reads files
and **asks no registry anything**, and every refusal names the file, the line, the offending
reference and the clause it breaks. It reads image references wherever this repository names one:
`Dockerfile` bases, compose services, the container and service images a workflow job could run, and
**image references written in Go source** - the PostGIS the spatial tests start is a Go constant, and
a gate that pinned what the stack runs while leaving what the tests measure on a floating tag would
let those two become different databases. Five deliberately broken trees under
`internal/pingate/testdata/refusals` keep it honest: `make pin-check` fails if fewer than all five go
red, each matched on the clause, the file AND the reason so a case cannot stay green on a refusal
from some other rule, and it fails if any category it examines has quietly stopped finding anything.

Resolved **2026-09-08**. Every value below came from the command beside it; nothing was retyped.

| reference | pinned to | re-resolve with |
|---|---|---|
| `golang:1.26.8-bookworm`<br>*(Dockerfile builder)* | `sha256:9fdc884aacc3bec89b20ffc69f4bb369c78210e3e4f600387b5128b12c199f81` | `curl -s https://hub.docker.com/v2/namespaces/library/repositories/golang/tags/1.26.8-bookworm \| jq -r .digest` |
| `gcr.io/distroless/static-debian12:nonroot`<br>*(Dockerfile runtime)* | `sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab` | `curl -s https://gcr.io/v2/distroless/static-debian12/tags/list \| jq -r '.manifest \| to_entries[] \| select(.value.tag \| index("nonroot")) \| .key'` |
| `postgis/postgis:16-3.4`<br>*(compose database, and the same reference in `internal/testsupport` that every spatial test starts)* | `sha256:44126d872ac91993766c341e369c539e8196614321765d36a6f1bab0419a5fa5` | `curl -s https://hub.docker.com/v2/namespaces/postgis/repositories/postgis/tags/16-3.4 \| jq -r .digest` |
| `actions/checkout` | `11d5960a326750d5838078e36cf38b85af677262` *(v4)* | `gh api repos/actions/checkout/commits/v4 --jq .sha` |
| `actions/setup-go` | `40f1582b2485089dde7abd97c1529aa768e1baff` *(v5)* | `gh api repos/actions/setup-go/commits/v5 --jq .sha` |
| `actions/setup-java` | `cf277c60eb25467037889841efdb72551f06f6c3` *(v4)* | `gh api repos/actions/setup-java/commits/v4 --jq .sha` |
| `android-actions/setup-android` | `9fc6c4e9069bf8d3d10b2204b1fb8f6ef7065407` *(v3)* | `gh api repos/android-actions/setup-android/commits/v3 --jq .sha` |
| `actions/upload-artifact`<br>*(the `ui` job's evidence upload)* | `ea165f8d65b6e75b540449e92b4886f43607fa02` *(v4)* | `gh api repos/actions/upload-artifact/git/ref/tags/v4 --jq .object.sha` |
| `gradle-8.9-bin.zip`<br>*(wrapper distribution, SHA-256 of the archive, not an image digest)* | `d725d707bfabd4dfdc958c624003b3c80accc03f7037b5122c4b1d0ef15cecab` | `curl -s https://services.gradle.org/distributions/gradle-8.9-bin.zip.sha256` |

**Moving a pin is a two-minute job and is meant to be.** Run the command, paste the value into the
file that holds it - `Dockerfile`, `docker-compose.yml`, `.github/workflows/ci.yml`,
`android/gradle/wrapper/gradle-wrapper.properties`, `internal/testsupport/postgis.go` - and update
the row above. The PostGIS digest lives in **two** files, `docker-compose.yml` and
`internal/testsupport/postgis.go`, and they must move together: the stack and the tests are meant to
be the same database. `make pin-check` tells you if you missed one.

**Why these are pins you can leave alone.** The conventions require pinning to something the
publisher *keeps*, not just to something that resolves today. `golang:1.26.8-bookworm` is an active
official-library tag whose digests Docker Hub retains. `postgis/postgis:16-3.4` has not moved since
2024-10-14, which is what a stable database tag looks like. The distroless `nonroot` index is one of
fifteen aliases gcr.io keeps for that image. Action values are git commit SHAs, which do not expire.
The Gradle checksum is published beside a released distribution and never changes for that version.

**Two costs, stated rather than buried.** Pinning `postgis/postgis:16-3.4` by digest freezes that
database image at its 2024-10-14 build, **including its security refreshes**, until someone moves the
pin - that is the trade for a stack whose spatial behaviour cannot change underneath the tests that
assert it. And that tag publishes a **linux/amd64 manifest only**, so pinning its digest removes no
platform the tag offered, but it does turn a would-be arm64 pull into a digest-level failure rather
than a tag-level one.

**No scheduled liveness check, deliberately.** This repo does not ask a third party every night
whether it is still up; a gate that does reds every unrelated pull request whenever someone else has
a bad afternoon. Rot is discovered when a build fails, and the defences that make that survivable
are the retention argument above and refusals that say precisely which pin and which file.
