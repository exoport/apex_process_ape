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

## Recipes

A **recipe** is a short markdown method for one board move, written for an
agent to follow: what to look at first, what to build, what to say to you, and
what to do with your answer. It is prose plus — usually — a tab skeleton, not
code and not a template engine.

Recipes exist because the board's surface is large and the judgement is the
hard part. `ape aboard capabilities` answers *what can this board do*; a recipe
answers *what should I do here, and what goes wrong if I get it wrong*.

### Where they come from

Four sources, and **the first match on a name wins**:

| order | directory | who it is for |
| --- | --- | --- |
| 1 | `_apex/aboard/recipes/` | every project in a workspace — **where `ape framework setup` and `ape framework update` install the framework's library** |
| 2 | `_aboard/recipes/` | this project's team, committed |
| 3 | `.aboard/recipes/` | this checkout only (gitignored, so it stays yours) |
| 4 | *(built into the binary)* | everywhere ape reaches, with no file to copy |

Built-ins travel inside the ape binary, so a fresh checkout has them with
nothing installed. The three directories are how a workspace, a team or one
person adds their own.

> **Do not copy a built-in into a directory above it.** That shadows the
> binary's own copy and freezes it at the moment you copied, so the recipe
> silently stops tracking the renderer it describes. Copy the ones that are
> *meant* to be copied — a curated library recipe, or one you wrote.

`ape framework setup|update` refreshes the framework's own recipe files and
leaves every other file in that directory alone, because the same directory is
where a workspace keeps its own.

### The format

One flat markdown file. The stem of the filename **is** the recipe's name and
must equal the `name` in its frontmatter.

```markdown
---
name: my-recipe          # required, equals the file stem
description: "..."       # required — one line, shown by `recipes list`
when_to_use: "..."       # required — the SITUATION, in words an agent
                         #   would recognise, not the mechanism
tags: [ui, decisions]    # optional
requires:
  min_schema: 1          # optional
---

The body an agent follows...
```

Optionally **one** fenced ` ```aboard-template ` block holding a tab skeleton —
no `id`, no `rev`, nothing the server manages. A recipe with no such block is
still a recipe; it just has no skeleton to print.

### Using them

```bash
ape aboard recipes list                 # every recipe here, and where each came from
ape aboard recipes show <name>          # the body, as an agent reads it
ape aboard recipes show <name> --template   # only the JSON tab skeleton
```

`recipes list` is the **complete** answer for a given project, which is why
nothing in these docs enumerates the recipes themselves: the set depends on
what the binary carries and what your directories add, so any list written here
would be wrong for somebody. `recipes show` exits non-zero on an unknown name
rather than guessing at a near-miss.

An agent using the `/aboard` skill can be pointed at one directly:

```
/aboard --show-a-structure the auth migration
```

The token after `--` is the recipe; the rest is the subject.

**The template is a TAB and `ape aboard apply` takes a DOCUMENT**, so there is
no `--template | apply` one-liner, and there should not be: composing a
document from a skeleton alone would drop every tab you are not touching. The
shape is read-modify-apply. Checking a skeleton on its own is a different
question and needs no board:

```bash
ape aboard recipes show <name> --template \
  | python3 -c 'import json,sys; json.dump({"tabs":[json.load(sys.stdin)]}, sys.stdout)' \
  | ape aboard apply --check
```

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
