# How to use the board (`ape aboard`)

`ape aboard` serves a browser UI for a project and keeps its state in a file
both a human and one or more agent sessions read and write. Tabs are *data*,
not code: an agent opens one for whatever it needs to show — a graph, a chart,
a question form, an annotated screenshot, a bespoke widget — and reads back
what the human changed.

The board is [aboard](https://github.com/exoport/aboard), a separate public
module. ape mounts its command tree rather than reimplementing it, so every
`aboard` subcommand is available as `ape aboard <cmd>`. See
[`ape aboard` in the CLI reference](../reference/cli.md) for the full surface.

## Start a board

```bash
ape aboard init --example     # create .aboard/ with a demo board
ape aboard serve              # run the server; prints the URL
ape aboard status             # what is running here, and on which port
```

State lives under `.aboard/` at the project root, found by walking up from
`--cwd`. Each project gets its own port derived from that root, so the URL is
stable and two checkouts never collide.

Add `.aboard/` to `.gitignore` — `ape aboard init --gitignore` does it for you.

## One board per project, whichever binary you use

`ape aboard` and the standalone `aboard` binary resolve the same `.aboard/`,
derive the same port, and write the same state file. A board started by
`ape aboard serve` is the board a bare `aboard status` reports, and either can
drive it. You do not need both installed; if you have both, they cooperate.

Exactly one thing differs. `/health` and `.aboard/run/instance.json` report
`"app": "ape-aboard"` instead of `"aboard"`, so a message can name the command
the reader actually has:

```console
$ ape aboard status
aboard running at http://localhost:44186
  ...
  served  ape-aboard
  caps    207b5d93
```

What must **not** differ is the capability manifest. `ape aboard capabilities`
and `aboard capabilities` are byte-identical, `capsHash` included — it
describes the board, not the process serving it, so an agent reading it cannot
tell which host it reached.

Messages name the command **you** typed. Under `ape aboard`, an error says
``run `ape aboard init` `` and help examples read `ape aboard history ab133`,
rather than naming a command you may not have. That needs aboard **v0.1.1 or
later**; ape pins it, so nothing to do.

## In VS Code

The [aboard VS Code extension](https://github.com/exoport/aboard_vscode) puts
the board's tabs in the sidebar and the board itself in a panel beside your
code, and handles the writes only a human is allowed to make. It talks to a
running board over HTTP and does not care which host started it.

Its **Start a board** button picks the command from your project: a folder with
an `_apex/` directory gets `ape aboard serve`, anything else gets `aboard
serve` when the dedicated binary is installed. It probes `ape aboard --version`
first, so an ape older than v0.0.55 — which has no `aboard` subcommand — is
never offered. Needs extension **v0.1.2 or later** for both rules.

It is not on any marketplace: download the `.vsix` from the extension's GitHub
Release and `code --install-extension aboard-vscode-<version>.vsix --force`.

## Exit statuses

The board has its own table, and ape preserves it:

| code | meaning |
| --- | --- |
| 0 | fine |
| 1 | it ran and failed |
| 2 | a flag or argument it could not act on, decided before anything was contacted |
| 3 | `ape aboard wait` returned because nobody came |

Exit 3 is the one worth scripting against:

```bash
ape aboard wait --for poke --timeout 5m || [ $? -eq 3 ] && echo "nobody came"
```

## Two rules that matter

1. **Do not edit `.aboard/aboard.json` by hand while a board is running.** Use
   `ape aboard apply`, which is a compare-and-set on the document's `rev`. A
   `409` means someone got there first — re-read and retry.
2. **Do not take a healthy server away from another session.** `ape aboard
   serve` refuses to start a second board for the same project and prints the
   URL of the one already running. The refusal is anchored to the board, not
   the port, so `--port` is not a way around it.

## Version

`ape aboard version` reports **aboard's** module version, not ape's — the
board is a dependency, and a bug report carrying the host's tag would name the
wrong project.

```console
$ ape aboard --version
aboard version 0.1.0
```

## Platform note

`ape aboard boards` scans `/proc` and is Linux-only by design. On other
platforms it still exists and exits 2 with a line naming the platform,
pointing at `ape aboard status` — a command that is present and honest beats
one that is missing on two of three platforms.
