# Deliberately broken trees

Every directory here is a shape `make prose-check` has to refuse. They are
committed so the gate can be **shown** going red rather than merely asserted to,
and `make prose-check-test` fails if any of them stops failing, if a refusal
category stops being covered, or if anything else appears in this directory.

**Do not fix these.** A tidy fixture proves nothing.

The sweep skips every `testdata` directory, so nothing here is measured as part
of the repository. That skip is closed from the other side: the suite requires
this directory to hold exactly the committed cases and this file.

Fixtures are committed under a suffix no Go tool reads and materialised under
the name the sweep reads at check time: `gofmt -l .` walks `testdata`, so a
deliberately unparseable file committed as `.go` here would be reported by
`make fmt`. A `.committed` file materialises as its content; a `.symlink` file
materialises as a symlink whose target is its content.

| case | shape | refusal |
|---|---|---|
| `over-ceiling` | an eligible file with more narration than code | `over-ceiling` naming the file, its ratio and the ceiling |
| `unparseable` | a Go file inside the swept set that does not parse | `unparseable` naming the file and the parser's own error |
| `unreadable` | a `.go` symlink pointing at nothing | `unreadable` naming the file and the error |
| `empty-sweep` | a tree holding no Go file at all | `empty-sweep` saying it measured nothing |

`unreadable` is a dangling symlink rather than a file with its permission bits
cleared: a mode of `0000` is no obstacle to a process running as root, and the
case would stop demonstrating anything in a root container without saying so.
