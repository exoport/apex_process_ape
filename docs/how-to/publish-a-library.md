# How to publish a library from an APEX project

A library's release is what its users download. APEX adds folders to the repository —
`.claude/`, `_apex/`, the development folder, and in a lifted project the old root
`evidence/` — and unless something keeps them out, every user gets them: the development notes
and evidence included. This guide marks the project as a library, keeps those folders out of
its package, and checks the package before each release.

Go is the case that needs work. A Go module's release is a zip of the module root at the tag,
and it holds every file outside a nested module. On pdfa that was **1,207 files**; without the
APEX folders it is **154**. npm, Python and Cargo already have a manifest that says what ships,
so for them the check is enough.

## Mark the project as a library

Add one line to `_apex/config.yaml`:

```yaml
library: true
```

Absent means "not a library". ape never writes the key: `ape project init` and
`ape project lift` print a suggestion when the project looks like a Go library (a `go.mod` at the
root and no `main` package outside `examples/`, `testdata/` and the development folder's gates):

```text
suggest: add "library: true" to _apex/config.yaml if this is a library others download — …
```

The key is not guarded: adding or removing it needs no `ape project accept-config`.

## Fence a Go library

```bash
ape project fence
```

With `library: true` and a `go.mod` at the root, it writes one `go.mod` in each APEX folder that
exists and commits them as `apex: module fences`:

| Project | Fences |
| --- | --- |
| a new v1 library | 3: `.claude/go.mod`, `_apex/go.mod`, `development/go.mod` |
| a lifted library with v0's root `evidence/` | 4: the same, and `evidence/go.mod` |

v1 has no root evidence folder: its evidence is committed under
`development/archive/milestones/<M>/evidence/`, inside the development fence.

Each fence is this file, with the root `go.mod`'s `go` line:

```text
// This folder is APEX's, not the library's: this go.mod keeps it out of the module zip.
// See https://github.com/exoport/apex_process_ape/blob/main/docs/how-to/publish-a-library.md. Written by ape project fence.
module apex.invalid/fence/.claude

go 1.27
```

A folder holding a `go.mod` is another module, so the module zip, `go build ./...`, `go vet
./...` and `go test ./...` leave it out. `go mod tidy`, `go mod verify` and `go work sync` stay
green and change no file. `.claude/go.mod` is committed with `git add -f`, because the `.claude/`
rules `ape project init` puts in `.gitignore` ignore it.

You rarely run it by hand. `ape project init` writes the missing fences of a library into its own
commit, `ape project lift` writes them into the lift commit when the v0 config says
`library: true`, and a framework migration runs it (command `ape project fence`, check
`ape project fence --check`).

### Keep them in place

`ape project check` reports a fence that is not in place, only for `library: true` with a root
`go.mod`:

| Code | What it means |
| --- | --- |
| `fence.missing` | An APEX folder has no `go.mod`, or git does not track it (the zip is built from git). |
| `fence.altered` | Its `module` line is not `apex.invalid/fence/<folder>`, or its `go` line is not the root's (after a Go bump, say), or it does not parse. |

`ape project fence` fixes both. For an altered fence it restores the `module` and `go` lines and
keeps every other line, so a gate's `require` and `replace` lines survive. A fence that does not
parse is yours to fix or delete: ape does not overwrite it.

`ape project fence --check` writes nothing. It exits 0 when every present folder's fence is in
place, 1 when one is not, and 2 when it cannot judge (`fence.unreadable`: the root `go.mod` does
not parse).

## Check the package before a release

```bash
ape project package-check
```

apex-release runs it before it writes the release commit, and refuses to release on a
`library.package-leak`. It packs the library as its users get it and fails on any file under
`.claude/`, `_apex/`, the development folder or `evidence/`:

| Ecosystem | What it lists |
| --- | --- |
| Go | the module zip at HEAD, from the files git holds, with `golang.org/x/mod/zip` — so commit first |
| npm | `npm pack --dry-run --json --ignore-scripts` |
| Python | `python3 -m build --no-isolation` on a copy of HEAD: the sdist and the wheel |
| Cargo | `cargo package --list --allow-dirty` |

A leak names the first 20 paths and counts the rest:

```text
  go.mod: the Go module zip (.) at HEAD holds 4 file(s) under APEX folders: .claude/settings.json, … — run `ape project fence`, …
```

The fix for Go is `ape project fence`. For the others, leave the folders out in the manifest:
`files` in `package.json` (or `.npmignore`), `MANIFEST.in` `prune` lines or your build backend's
excludes, `exclude` in `Cargo.toml`.

It never passes silently. When a package cannot be packed — the tool is not on `PATH`, it fails,
HEAD has no commit, or the project has no manifest ape knows — it prints the notice
`library.package-unchecked` and exits 0. apex-release states the notice in its reply; install the
tool, or check that package by hand.

## Run a gate that needs more than the library

A gate program under `development/gates/` belongs to the development fence's module once the
library is fenced. A gate uses only what its module provides, and the root `go.mod`, `go.sum`
and `go.work` never change for a gate. When a gate needs the library's own packages or another
module, give the fence its own requirements and run the gate from the fence:

```text
// development/go.mod
module apex.invalid/fence/development

go 1.27

require (
	example.com/lib v0.0.0
	github.com/acme/check v1.2.0
)

replace example.com/lib => ../
```

```bash
GOWORK=off go -C development mod tidy      # writes development/go.sum, not the root's
GOWORK=off go -C development run ./gates/M1-pdf-roundtrip
```

`ape project check` gives the notice `gate.root-module-change` for a gate that imports a package
its module does not provide, with the lines to add.

If the project tracks a `go.work.sum`, a gate run in file mode may add a line to it. Commit that
line; it is not a failure.

## Or: put the module in a subfolder

A library with a vanity import path can avoid the fences: move its `go.mod` into a subfolder such
as `go/`, and the APEX folders at the repository root are outside the module.

- The vanity page's `go-import` meta tag names the subfolder as a fourth field (Go 1.25 and
  later): `<meta name="go-import" content="example.com/lib git https://github.com/acme/lib go">`.
- Release tags carry the subfolder as a prefix: `go/v1.4.0`.
- A plain `github.com/…` library cannot do this without changing its import path, which breaks its
  users.

`ape project fence` writes nothing without a root `go.mod`, and `ape project package-check` checks
each subfolder module's zip (a module with no `LICENSE` gets the root's, as the go command adds
it).

## What does not work

Both were measured with `go mod download` straight from git, as the module proxy fetches:

- **`.gitattributes` `export-ignore`.** The go command turns it off before it archives the
  repository, so the zip keeps every file (and `.gitattributes` itself).
- **Dot and underscore folder names.** `go build` skips `.claude/` and `_apex/`, but the module
  zip does not.

## See also

- [`library` in `_apex/config.yaml`](../reference/config-yaml.md#library)
- [`ape project fence`](../reference/cli.md#ape-project-fence) and
  [`ape project package-check`](../reference/cli.md#ape-project-package-check)
