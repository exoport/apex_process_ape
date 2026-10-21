# How to pin ape in a project

A project that pins ape with [bingo](https://github.com/bwplotka/bingo) runs that version, whichever `ape` is first on PATH. From v0.9.0, a bare `ape` honours the project's pin the way `go` honours a module's `toolchain` line.

## Pin a version

In the project's repo root:

```bash
bingo get -l github.com/exoport/apex_process_ape/cmd/ape@v1.0.0
git add .bingo && git commit -m "chore: pin ape v1.0.0"
```

Later, `ape update --pin` or `ape framework update --update-ape` moves the pin to the newest release and commits it for you.

bingo builds `<GOBIN>/ape-v1.0.0` and records the version in `.bingo/ape.mod`. `-l` also points the bare `<GOBIN>/ape` at it. To move the pin later, run the same command with the new version and commit `.bingo/` again. Don't use `ape update` for this: it refuses on a pinned binary because it would overwrite the versioned file.

## What a bare `ape` does in a pinned project

Every invocation looks for `.bingo/ape.mod`, starting in the working directory and walking up to the repo root (the nearest directory with `.git`). **Only a repo is pinned:** outside any git repo nothing is honoured, even a `.bingo/ape.mod` in a directory above.

| Situation | What happens |
| --- | --- |
| No `.bingo/ape.mod` | The `ape` on PATH runs the command, as before. The cost is one file lookup. |
| The pin is this ape's version | It runs the command. |
| The pin is another version, and `ape-vX.Y.Z` is installed | It hands the command to that binary: same arguments, same stdin and stdout, and the pinned binary's exit status. On Unix it is an `exec`, so the caller sees one ordinary command. |
| The pin is not installed | It stops with exit 13, naming `bingo get ape` (install the pinned version) and `ape update --pin` (move the pin). It never quietly runs its own version instead. |
| The pin is below the installed framework's `min_ape_version` | It stops with exit 13 and the `bingo get -l …@v<floor>` that moves the pin forward. A pre-floor binary under a post-floor framework is what the floor exists to prevent. |
| An APEX v1 project (`_apex/state/` or `development/project.md`) has no `_apex/ape-commands.yaml`, and the pin is below 1.0.0 | The floor is 1.0.0: it stops with exit 13, as above. Right after a lift the payload's manifest is not there yet, and a v0 pin would otherwise run a v0 ape, which has no `hook` command. |

**`ape hook pre-tool` exits 2 on a pin refusal**, not 13, with the same message: Claude Code reads only exit 2 from a PreToolUse hook as a block, so the guard fails closed instead of letting every tool call through. `ape hook stop` and `ape hook session-start` keep 13, which blocks nothing. `ape hook pre-commit` keeps 13, and git refuses the commit.

The pinned binary is looked for where bingo puts it: `$GOBIN`, then the first `$GOPATH` entry's `bin`, then `~/go/bin`. `go env` is asked only when none of those has it.

**Recovery commands still run when the pin can't.** `ape framework update`, `ape doctor`, `ape version`, `ape update`, bare `ape` and any `--help` run on the current binary, with a note on stderr. That is how a project finds out what is wrong and moves its pin. When the pin is usable they hand over like every other command, except `ape update`.

**`ape update` never hands over.** It updates the machine's launcher, the `ape` on PATH, whatever `.bingo/ape.mod` says. It never touches `.bingo/` unless asked, because moving the pin is a tracked, team-wide change. When the project's pin is behind the newest release, it prints the bingo command that moves it. `ape update --pin` also moves the pin, committed on its own as `chore(ape): update ape to vA.B.C`. If the launcher is itself a bingo pin (`~/go/bin/ape → ape-vX.Y.Z`), it is not self-updated: plain `ape update` refuses and names `--pin`, and `--pin` moves the project's pin (with `-l` when the launcher is bingo's link, so the bare `ape` follows).

**`ape project lift` never hands over.** It lifts a v0 project, whose pin names a v0 ape with no `project` command, so it runs on the binary you invoked and its first lines say which one (version and path). The lift commit moves the pin to that version when it is a release and bingo is on PATH; otherwise the lift prints the `bingo get` command that does, and until the pin moves every other v1 command in the project needs `APE_TOOLCHAIN=local`: that includes every `git commit` (the pre-commit hook runs `ape hook pre-commit`) and every Claude session in the project (its hooks run ape), so start one as `APE_TOOLCHAIN=local claude`.

**`ape framework update --update-ape`** moves the pin to the newest release before installing, also in its own `chore(ape)` commit. At a terminal it asks first, defaulting to No. See [the framework update guide](framework-update.md#apes-minimum-version).

## Check which ape ran

```bash
ape version
```

After a hand-over it adds `pinned by:  .bingo/ape.mod (started as ape 0.7.0)`. `--output-format json` carries the same as `pinnedFrom`.

## Run a different ape on purpose

```bash
APE_TOOLCHAIN=local ape …
```

This skips the pin for that command, for example to try a newer ape in a pinned project before moving the pin.

## Environment

| Variable | Set by | Meaning |
| --- | --- | --- |
| `APE_TOOLCHAIN=local` | you | skip the pin for this command |
| `APE_PINNED` | ape, on a hand-over | the pin it honoured. A binary that finds its own project's pin here runs without checking again, so no build can hand over to itself forever. A session that moves to a project with a different pin is still checked. |
| `APE_PINNED_FROM` | ape, on a hand-over | the version that handed over, reported by `ape version` |

Sessions ape spawns already run the pinned binary first on PATH (see `internal/selfpath`), so every `ape` inside them either is the pin or carries the marker. The check is what covers a Claude session you open yourself.

> A `.bingo/ape.mod` that is not inside a repo pins nothing. For example, `~/_dev/.bingo/ape.mod` installs your launcher with `bingo get -l`, but an `ape` run in `~/_dev/exoar` (not a repo) is not switched to it.

## A pin below the framework's minimum

A project whose pin is older than the installed framework's `min_ape_version` exits 13 on every ordinary command until the pin moves. Move it with either of these; both still run in that state:

```bash
ape framework update --update-ape   # moves the pin, commits it as chore(ape), then updates the framework
ape update --pin                    # moves the pin and commits it as chore(ape)
```
