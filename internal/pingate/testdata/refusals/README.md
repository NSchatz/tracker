# Committed refusals - the five shapes this gate exists to catch

Every tree beside this file is DELIBERATELY BROKEN and must stay that way. `internal/pingate`
scans each one and requires the refusal it is named for; `make pin-check` fails if fewer than five
of them go red. A pin gate that cannot be shown failing is not evidence of anything, so these are
the evidence.

They live under `testdata/` because the go tool ignores that directory entirely, and this directory
is the ONE path the repository scan skips - see `DemonstrationsDir` in `demonstrations.go`. That
skip is not a blind spot: `verifyDemonstrationTree` requires this directory to hold exactly the five
named cases and nothing else, so an unpinned reference cannot be parked here to hide it from the
gate. That is also why this README sits at the top level rather than inside a case: the check
tolerates a file here, and refuses an unexpected DIRECTORY.

| case | shape | clause |
|---|---|---|
| `dockerfile-tag-only` | a `FROM` carrying a tag and no digest | P2 |
| `compose-image-no-digest` | a compose `image:` for a service the file does not build, no digest | P1 |
| `workflow-mutable-tag` | a `uses:` naming a mutable tag instead of a commit SHA | P3 |
| `manifest-dynamic-version` | a version catalog carrying a dynamic version | P4 |
| `node-lifecycle-scripts` | a node manifest with lifecycle scripts on and no committed reason | P4 |

`node-lifecycle-scripts` is the shape it is because npm's DEFAULT is scripts-on: a `package.json`
with no `.npmrc` beside it already runs every dependency's install script, and nothing in that tree
says why. The other half of P4's node rule - an `.npmrc` that sets `ignore-scripts=false`
explicitly, with and without a reason on the line above - is exercised by
`TestNodeLifecycleScriptsOptBackIn` against a tree the test writes, because an `.npmrc` committed
here would be a credential-shaped file in the repository for no gain.

It is also the one case whose fixture is committed under a name nothing reads
(`package.json.committed`) and copied into a scratch tree at check time, via
`Demonstration.CommittedAs`. A file committed as `package.json` would make tracker a node
repository - `git ls-files '*package.json'` would find it - and "tracker has no node in it" is the
fact the node category asserts. Hiding a real manifest from the scanner would not have made that
claim true, only unchecked. `TestNodeCategoryAssertsAbsenceRatherThanCountingNothing` walks the
whole tree, this directory included, to keep it honest.

Do not "fix" these files. Fixing one turns the gate green while removing the proof that it bites.
