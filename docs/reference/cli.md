# ape CLI reference

> Generated from the command tree by `make docs-cli` (which runs the hidden
> `ape gen-docs`). Do not edit by hand — change the command definitions in
> `internal/apecmd/` and regenerate. PLAN-9 F4.

## ape

APE — APEX Process Engine CLI

```
ape
```

ape runs APEX v1 work against your project through an interactive
Claude Code REPL.

Common commands:
  ape project init      Make this repository an APEX v1 project.
  ape status            Where the project stands: milestones, changes, runs.
  ape change <verb>     The unit of change: new, approve, start, verify, archive.
  ape task <skill>      Run a single framework skill in a Claude session.
  ape chat              Open an interactive Claude session in the project.
  ape costs             Show this project's Claude cost rollup.

Also: milestone, decision, hook, framework setup/update, doctor, sessions.
Every claude invocation runs in an in-process PTY — there is no "claude -p"
programmatic path.

The APEX v0 commands (pipeline, story, sprint, adr, registry, …) are retired:
a v0 project keeps the v0 ape its .bingo pins, until `ape framework update`
to v1 lifts it (`ape project lift`).

Subcommands:

- `aboard` — A shared visual board for a human and one or more agent sessions
- `change` — The unit of change: new, check, approve (tweaks), start, verify, review, accept, block, answer, archive, tool
- `chat` — Bridged claude REPL with hooks captured to a runlog
- `config` — Resolve the project's APEX configuration
- `costs` — Show this project's Claude cost rollup
- `decision` — Record architecture decisions (ADR-NNNN) in development/decisions/
- `doctor` — Probe the local environment for prerequisites
- `event` — Publish a session progress event over NATS
- `framework` — Install and inspect APEX framework assets in a project
- `hook` — Claude Code and git hooks for APEX v1 projects (installed by `ape project init`)
- `log` — Publish a structured log record over NATS
- `metrics` — Scan and publish this session's usage metrics over NATS
- `milestone` — Add, approve, gate and close roadmap milestones
- `next` — Record a finding for later in development/next.md (Next)
- `plugins` — Install the plugins _apex/config.yaml lists, and report what they and the tools slot bring
- `project` — Create and check an APEX v1 project layout
- `prompt` — Drive an unattended Claude session from a prompt or a handoff file
- `rollback` — Rollback ape to the previous version
- `run` — Wait for, or inspect, a run started with --detach
- `sandbox` — Provision and operate hardware-isolated Kata VM workspaces (via aped)
- `script` — Run a Go orchestration script through the yaegi interpreter
- `service` — Run a NATS-micro job daemon that accepts task/prompt/script jobs over request/reply
- `sessions` — List, prune, or open the URL of live ape sessions
- `status` — Show the current milestone, active changes, blocks, pending human review and spend
- `task` — Run a single framework skill through the interactive PTY runner
- `transcript` — Work with Claude session transcripts
- `update` — Update ape to the latest version
- `version` — Print version information

## ape aboard

A shared visual board for a human and one or more agent sessions

```
ape aboard
```

aboard serves a browser UI for a project and keeps its state in a file both
sides read and write. Tabs are DATA, not code: an agent opens one for whatever it
needs to show — a graph, a chart, a question form, an annotated screenshot, a
channel to another session, a bespoke widget — and reads back what the human
changed.

State lives under .aboard/ at the project root, which is found by walking up from
--cwd. Each project gets its own port, derived from that root, so the URL is the
same every time and two checkouts never collide.

Start with:

  ape aboard serve            run the server for this project
  ape aboard status           what is running here, and on which port
  ape aboard capabilities     what this board can do (no server needed)

Subcommands:

- `apply` — Write a board document from stdin, through the running board
- `boards` — List every board running on this machine, from the process table (Linux only)
- `capabilities` — Print what this board can do: types, state fields, controls, endpoints, commands
- `export` — Print one tab as text, for pasting into the project's own documents
- `history` — List what a tab said before, from the journal
- `init` — Create .aboard/ in this directory and write an empty board
- `journal` — Print recent accepted writes: when, who, which tabs
- `log` — Read stdin and append it to a tab's sidecar log, line by line
- `poke` — Release every session waiting on this board
- `recipes` — List the recipes available here, or print one
- `rendered` — Print what the browser reported it drew
- `requests` — List the human's notes to an agent
- `serve` — Run the board server for this project
- `shot` — Screenshot tabs of the running board with a headless browser
- `status` — Report this project's running board, if any, and the caps beacon
- `uploads` — List the files under .aboard/uploads/ and the tabs that mention them
- `version` — Print the build identity of this binary
- `wait` — Block until the board is poked, or until a predicate matches
- `watch` — Follow every change as JSON lines until interrupted

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | directory to resolve the project root from (default: the working directory) |
| `--name` | string | `—` | board name, for a second isolated board in the same project (env ABOARD_NAME) |

## ape aboard apply

Write a board document from stdin, through the running board

```
ape aboard apply [flags]
```

Read a whole board document on stdin and POST it to the running board, which
applies it under compare-and-set.

Never edit the state file directly while a board is running: a direct write has
no compare-and-set, so a concurrent change from the browser or another session is
destroyed with no error. A 409 here means somebody got there first — re-read,
redo the edit, apply again.

Warnings print on stderr before the write: a schema version the board does not
write, state no renderer reads, an unknown ui component or prop, a {bind} that
resolves nowhere, a colour name this board no longer has. They warn rather than
refuse, because a spec can lag its renderer — but read them, because "applied"
is not evidence that anything rendered. The same warnings are recorded on the
journal entry and shown to the human on the tab, so a write that warns is no
longer something only the terminal knows about.

--check runs those checks and stops: nothing is posted, no board need be
running, and the document needs no `rev`. --strict turns any warning into a
refusal (exit 1, nothing written) — the guard for a loop that must stop rather
than ship a wrong tab. Together they are "tell me and exit non-zero".

--label records WHY this write is happening on the journal entry, where the
journal, watch and trace commands show it. It is navigation inside a local,
rotating file — never a record to cite anywhere permanent.

The compare-and-set base is the `rev` inside the document you submit — the one
you read. A document with no `rev` is refused (exit 2) rather than written
unconditionally; --force writes it anyway and says so on stderr.

Examples:

```
  ape aboard apply --by agent-1 < next.json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--by` | string | `agent-1` | actor recorded in lastEditedBy and on every tab this write touched |
| `--check` | bool | `false` | run the write warnings and stop: nothing is posted, and no board need be running |
| `--force` | bool | `false` | write without compare-and-set, overwriting anything since you read the document |
| `--label` | string | `—` | why this write is happening; recorded on the journal entry, not in the board |
| `--strict` | bool | `false` | refuse the write if anything warns (exit 1, nothing written) |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | directory to resolve the project root from (default: the working directory) |
| `--name` | string | `—` | board name, for a second isolated board in the same project (env ABOARD_NAME) |

## ape aboard boards

List every board running on this machine, from the process table (Linux only)

```
ape aboard boards [flags]
```

Every running board on this machine, whichever project it belongs to.

This is the cross-project half of `ape aboard status`. It needs no project of its
own — it works from a directory that has never held a board — because it asks
the PROCESS TABLE rather than a registry: it walks /proc for an `aboard serve`
or an `ape aboard serve`, resolves each one's project root, and then does exactly
what status does for one project — read the instance record, verify it over
/health, and report what the board says about itself.

One row per (project, name), because two boards in one project are two boards.
The project path is printed in full: the point of a machine-wide listing is that
you are not standing in the project it names.

Nothing is trusted that a live board did not just confirm. A record whose process
has gone is listed as "recorded but not answering" rather than dropped — a stale
record is information — and the count of processes inspected is printed with the
result, because "no board found" after 3 processes and after 400 mean different
things.

/proc is Linux only. Everywhere else this command exits 2 and says so, and the
per-project answer is `ape aboard status` inside each project.

Examples:

```
  ape aboard boards
  ape aboard boards --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | human, json or yaml |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | directory to resolve the project root from (default: the working directory) |
| `--name` | string | `—` | board name, for a second isolated board in the same project (env ABOARD_NAME) |

## ape aboard capabilities

Print what this board can do: types, state fields, controls, endpoints, commands

```
ape aboard capabilities [type] [flags]
```

Ask the BINARY what it can do, rather than reconstructing it from a document.

Every renderer declares its own surface in views/<type>.spec.json beside the code
it describes, and this aggregates those with the declared command table and the
route list. It needs no running server and no project: a fresh checkout, a copied
binary, or another session holding the port all still answer.

  ape aboard capabilities            the whole manifest, as JSON
  ape aboard capabilities kanban     one type — cheap, for a mid-task lookup
  ape aboard capabilities --format md    the markdown reference the skill commits
  ape aboard capabilities --check    exit 1 if that committed reference is stale

--check treats a MISSING reference as "nothing to check": a project that never
copied the skill has nothing to be out of date.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--check` | bool | `false` | exit 1 if the committed skill reference is stale |
| `--format` | string | `json` | json, md, or js (the generated control module) |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | directory to resolve the project root from (default: the working directory) |
| `--name` | string | `—` | board name, for a second isolated board in the same project (env ABOARD_NAME) |

## ape aboard export

Print one tab as text, for pasting into the project's own documents

```
ape aboard export <tab|key> [flags]
```

Turn a tab into markdown (or CSV, where it has rows) so its conclusions can be
promoted into a spec, an ADR, or whatever this project's own documents are.

Reads the board document from disk, so it works with no server running — for the
same reason `capabilities` does: an agent should never have to start a server to
read out a conclusion.

The strategy is not to promote early. It is to make LATE promotion cheap, and
retyping was the cost that made it expensive.

Examples:

```
  ape aboard export ab128
  ape aboard export table-example --format csv
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--format` | string | `md` | md or csv |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | directory to resolve the project root from (default: the working directory) |
| `--name` | string | `—` | board name, for a second isolated board in the same project (env ABOARD_NAME) |

## ape aboard history

List what a tab said before, from the journal

```
ape aboard history <tab> [flags]
```

Per-tab history, read out of the journal the board already keeps.

Every accepted write records each changed tab AS IT WAS, which is the unit
somebody undoing a bad write actually wants. This lists those versions newest
first, naming who replaced each one — and says plainly where the record ends,
because rotation keeps one older generation and a listing that just stopped would
read as "this tab has only ever been written twice".

  ape aboard history ab133                          what it said, and when
  ape aboard history ab133 --at 1 | ape aboard apply --by agent-1     put version 1 back

--at prints a WHOLE document with that one tab put back, not the tab on its own:
a single-tab document is a document that deletes every other tab, and the server
would answer it with a removal request on each one. It carries the board's
current `rev`, so a restore built while somebody else was writing is refused
rather than clobbering.

What a restore carries depends on which generation of the record it came from,
and the listing marks each version with it. An entry stamped `schema: 2` holds
the whole tab, so the name, type, note, stateFrom and key come back with the state; an
older entry holds a bare state, and then only the state moves. Neither ever puts
back `touched`, `pendingRemoval` or `seen` — re-raising a dot the human
dismissed, or re-opening a removal request they answered, is not an undo.

Reads from the running board when there is one and from
.aboard/run/journal.jsonl when there is not — journal.<name>.jsonl on a named
board, whose history is its own. The restore line the listing prints carries
--name on both halves for exactly that reason.

Examples:

```
  ape aboard history ab133
  ape aboard history ab133 --at 1 | ape aboard apply --by agent-1
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--at` | int | `0` | print the document that restores version N instead of listing (1 is the most recent) |
| `--limit` | int | `20` | how many versions to list |
| `--output-format` | string | `human` | human, json or yaml |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | directory to resolve the project root from (default: the working directory) |
| `--name` | string | `—` | board name, for a second isolated board in the same project (env ABOARD_NAME) |

## ape aboard init

Create .aboard/ in this directory and write an empty board

```
ape aboard init [flags]
```

Create a board for the project you are standing in: `.aboard/` with an empty
document, an uploads directory, a recipes directory and the run directory.

This is the ONE command that does not walk up. Every other command finds the
project root by climbing from --cwd, because a board belongs to a project rather
than to whichever subdirectory you happened to be in — but there is nothing to
find yet, and climbing would mean `ape aboard init` in a subdirectory quietly doing
nothing while reporting success. So it creates a root where you stand, and
refuses when that would make a second one, naming the root it found.

It never overwrites an existing board document. --name opens a SECOND board in
the same project, with its own state file and its own port.

--example seeds the board compiled into this binary: fifteen tabs, one per
renderer, each noted with what it demonstrates. It is a worked example, not your
work — delete what you do not want.

--gitignore adds `.aboard/` to the project's .gitignore. A board is a
LOCAL, persistent, non-authoritative channel: several developers on one repo each
get their own, and a committed one is a whole-file JSON conflict on every merge
over a conversation that was never theirs.

Examples:

```
  ape aboard init
  ape aboard init --example --gitignore
  ape aboard init --name review
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--example` | bool | `false` | seed the board with the example tabs compiled into this binary |
| `--gitignore` | bool | `false` | append .aboard/ to the project's .gitignore if it is not already ignored |
| `--output-format` | string | `human` | human, json or yaml |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | directory to resolve the project root from (default: the working directory) |
| `--name` | string | `—` | board name, for a second isolated board in the same project (env ABOARD_NAME) |

## ape aboard journal

Print recent accepted writes: when, who, which tabs

```
ape aboard journal [flags]
```

Per-write history of this board: the time, the actor, and the tabs that changed.

Reads from the running board when there is one and from .aboard/run/journal.jsonl
when there is not, so this works in a project whose board is stopped — it is the
third command of the resume protocol, and a session that has just cleared its
context has no reason to start a server before asking what happened.

Each board owns its journal: a named board's is journal.<name>.jsonl, so the
entries here are this board's writes and nobody else's.

With two sessions and a human writing one document, "who changed the plan while I
was thinking?" otherwise has no answer except git archaeology over a file that
moves constantly. Every accepted write funnels through one function, so this
cannot be bypassed by an agent that forgot to record something.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--limit` | int | `40` | how many entries to print |
| `--output-format` | string | `human` | human, json or yaml |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | directory to resolve the project root from (default: the working directory) |
| `--name` | string | `—` | board name, for a second isolated board in the same project (env ABOARD_NAME) |

## ape aboard log

Read stdin and append it to a tab's sidecar log, line by line

```
ape aboard log <tab>
```

Pipe a long-running command's output onto the board, so the human can watch it
happen rather than waiting for it to finish.

The stream lives in a sidecar file under .aboard/run/logs/, NOT inside the board
document: that document is rewritten whole on every write, so an appending log
inside a tab's state would mean rewriting the entire board once per line. The
tab's state holds only a pointer.

A named board writes into .aboard/run/logs/<name>/ instead — tab ids are
allocated per board, so both boards have a ab1 and one directory would have
interleaved two commands' output in one file.

Lines are echoed to stdout as well — piping output to the board should not mean
losing it from the terminal you are watching.

Examples:

```
  go test ./... 2>&1 | ape aboard log ab126
```

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | directory to resolve the project root from (default: the working directory) |
| `--name` | string | `—` | board name, for a second isolated board in the same project (env ABOARD_NAME) |

## ape aboard poke

Release every session waiting on this board

```
ape aboard poke [flags]
```

Do what the human's notify button does: release every session currently blocked
on `ape aboard wait`, and tell them who released them and why.

Nothing here starts an agent. A session is released only if it had already
decided to listen; a board with nobody waiting is simply not listening, and this
command says so rather than pretending otherwise.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--by` | string | `agent-1` | who is releasing them |
| `--note` | string | `—` | a message for the waiting sessions |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | directory to resolve the project root from (default: the working directory) |
| `--name` | string | `—` | board name, for a second isolated board in the same project (env ABOARD_NAME) |

## ape aboard recipes

List the recipes available here, or print one

```
ape aboard recipes
```

A recipe is a worked method for one shape of board work — put a structure in
front of the human, ask for a decision, annotate a screenshot, coordinate with
another session. The bodies live in files rather than in the skill, so the skill
stays small and can never disagree with the recipe it describes.

Four tiers, first wins by name:

  _apex/aboard/recipes/   the wider workspace's house style
  _aboard/recipes/        committed, shared with the team
  .aboard/recipes/        this checkout only, gitignored with the rest
  built-in                compiled into this binary

Shadowing is allowed and always reported: a project that overrides a built-in
recipe is doing something deliberate, and the row says what it replaced. A file
that does not parse is not skipped either — it is listed as invalid with the
reason, because a recipe the author is looking at and the tool pretends does not
exist is the worst of the three outcomes.

Subcommands:

- `list` — List every recipe available in this project, and where each came from
- `show` — Print one recipe's body, or just its tab skeleton

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | directory to resolve the project root from (default: the working directory) |
| `--name` | string | `—` | board name, for a second isolated board in the same project (env ABOARD_NAME) |

## ape aboard recipes list

List every recipe available in this project, and where each came from

```
ape aboard recipes list [flags]
```

Every recipe this project can use, one row each: the name, the tier it came
from, and what it is for.

This is the only complete answer. The skill's generated index lists what is
compiled into the binary and cannot know what a project added, so an agent that
reads only that index is incomplete.

Rows carry what is wrong with them rather than being dropped: a shadowed file is
named under the recipe that won, a file that does not parse is marked INVALID
with the reason, and a recipe needing a newer board schema says so.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | human, json or yaml |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | directory to resolve the project root from (default: the working directory) |
| `--name` | string | `—` | board name, for a second isolated board in the same project (env ABOARD_NAME) |

## ape aboard recipes show

Print one recipe's body, or just its tab skeleton

```
ape aboard recipes show <name> [flags]
```

Print the recipe an agent should follow: a title line naming it and saying what
it is for, then the body. The frontmatter is stripped — it is metadata for the
list, and YAML at the top of something meant to be read as prose is noise.

--template prints ONLY the JSON tab skeleton the recipe carries, so it pipes
straight into an edit and then into `ape aboard apply`. A recipe with no skeleton
exits 1 saying so, rather than printing an empty document that would be applied
as an empty tab.

Examples:

```
  ape aboard recipes show apply-a-write
  ape aboard recipes show my-recipe --template | jq .
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--template` | bool | `false` | print only the recipe's JSON tab skeleton |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | directory to resolve the project root from (default: the working directory) |
| `--name` | string | `—` | board name, for a second isolated board in the same project (env ABOARD_NAME) |

## ape aboard rendered

Print what the browser reported it drew

```
ape aboard rendered [tab] [flags]
```

What a real browser actually put on screen for a tab, as it reported it.

`ape aboard apply` printing "applied" is evidence a write was accepted, not that
anything renders — an unknown `ui` component draws a marker and an unknown PROP
draws nothing at all. After every mount the shell posts the control ids it drew,
the ones somebody pressed, and any unknown-component markers, and this prints
them.

It also says what did NOT FIT, at the width that browser had: a `ui`
component or an `html` widget whose content is larger than its box — cut off
(overflow hidden), spilling past its edge (an unbroken URL), or scrolling inside
the tab (a wide code block or table). Only those two renderers measure; the
others draw the board's own layouts, which truncate on purpose.

This is NOT a DOM sweep. Every id here is already declared in
views/<type>.spec.json; nothing is scraped and nothing is matched against prose.

Two things it is deliberately not evidence of, printed with the output so they
travel with it: no receipt means nobody had the tab OPEN, and a control listed
here was REACHED — never that it behaved correctly.

Reads .aboard/run/rendered.json — or rendered.<name>.json on a named board — so
it needs no server. With no argument it prints every tab that has a receipt.

Examples:

```
  ape aboard rendered ab133
  ape aboard rendered
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | human, json or yaml |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | directory to resolve the project root from (default: the working directory) |
| `--name` | string | `—` | board name, for a second isolated board in the same project (env ABOARD_NAME) |

## ape aboard requests

List the human's notes to an agent

```
ape aboard requests [flags]
```

What the human has asked for, tab by tab, oldest first.

Everything else on a board flows one way: an agent shows them something and
reads back what they changed. A request is the other direction — they point at a
tab and say "this is wrong, fix it" — so it needs a channel an agent can find
without being told to look, because by definition it arrives while nobody is
watching.

Run this at the start of a turn, next to `ape aboard status` (which prints the
count). Then say you did one:

  ape aboard requests done ab199 --by agent-1 --note "redrew the arrow"

Only the human writes these. An agent write that creates, edits, reorders or
deletes one has it restored by the server; adding a done stamp is the one change
an agent may make, and the stamp is never cleared — the human deleting the whole
note is how it goes away.

Needs no running board: it falls back to the state file. Stamping one does need
the board, for the same reason `ape aboard apply` does.

Subcommands:

- `done` — Say you acted on one of the human's requests

Examples:

```
  ape aboard requests
  ape aboard requests --tab ab14 --all
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--all` | bool | `false` | include the ones already stamped done |
| `--output-format` | string | `human` | human, json or yaml |
| `--tab` | string | `—` | only this tab's requests, by id or name |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | directory to resolve the project root from (default: the working directory) |
| `--name` | string | `—` | board name, for a second isolated board in the same project (env ABOARD_NAME) |

## ape aboard requests done

Say you acted on one of the human's requests

```
ape aboard requests done <request-id> [flags]
```

Stamp one request done: who did it, when, and optionally a line about what.

The board strikes the note through and prints the tick beside your name, which
is the only feedback the human gets that anything happened — so write the
`--note`. "done" tells them nothing they could not have guessed; "redrew the
arrow, it points at the worker now" is an answer.

A thin `apply`: it reads the board, changes one field, and posts it back with
compare-and-set. It does not merge a conflict — run it again instead, which is
cheaper than any merge could be. Re-running one that is already stamped says so
and writes nothing.

--by human is refused: the stamp says which SESSION acted, and the human answers
their own requests by deleting them.

Examples:

```
  ape aboard requests done ab199 --by agent-1 --note "redrew the arrow"
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--by` | string | `agent-1` | which session did it; shown beside the tick |
| `--note` | string | `—` | one line about what you did, shown with the tick |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | directory to resolve the project root from (default: the working directory) |
| `--name` | string | `—` | board name, for a second isolated board in the same project (env ABOARD_NAME) |

## ape aboard serve

Run the board server for this project

```
ape aboard serve [flags]
```

Serve this project's board over HTTP and watch its state file for changes.

The port is derived from the discovered project root, so the URL is the same
every run and two checkouts never collide; --port or PORT overrides it. The
running instance is recorded in .aboard/run/instance.json, which is how every
other command finds the board and how restart.sh stops the right process.

--base-path serves the whole board under a URL prefix, for putting it behind a
reverse proxy or inside another tool's routing. The prefix is injected into the
shell, so every fetch, the SSE stream and an html tab's iframe all build from it.
Because it is injected, it is also validated: one or more /segments of letters,
digits, dot, underscore, tilde or hyphen. Anything else is a usage error.

--detach starts the same server in a session of its own, with its output in
.aboard/run/serve.log, and returns once it answers — so a board started from an
agent session outlives that session restarting, where one started with
`nohup … &` dies with it. It still refuses a second board for this project, and
says so with the log of the process that refused. Stop it by the pid it prints.

Examples:

```
  ape aboard serve
  ape aboard serve --detach
  ape aboard serve --dev
  ape aboard serve --base-path /aboard
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--base-path` | string | `—` | serve under a URL prefix, e.g. /aboard (default: the server root) |
| `--detach` | bool | `false` | start the server in a session of its own, log to .aboard/run/serve.log, and return once it answers |
| `--dev` | bool | `false` | serve the web tree from disk instead of the embedded copy |
| `--dev-dir` | string | `—` | with --dev, the web tree to serve (default: pkg/aboard/web under the root) |
| `--port` | int | `0` | port to listen on (0 derives one from the project root; env PORT) |
| `--state` | string | `—` | state file to serve (default: .aboard/aboard.json under the root) |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | directory to resolve the project root from (default: the working directory) |
| `--name` | string | `—` | board name, for a second isolated board in the same project (env ABOARD_NAME) |

## ape aboard shot

Screenshot tabs of the running board with a headless browser

```
ape aboard shot <tab>... [flags]
```

Take a picture of a tab as the human sees it, and print where it went. Then
READ the picture: `ape aboard apply` exits 0 for a tree that draws an empty box, and
the write warnings cannot see a layout that is legal and still unreadable.

A tab is named by id, key or type. The board must be running — the picture is of
the page the binary serving it draws — and a chromium-family browser must be
installed: chromium, google-chrome or Edge, found on $PATH or in its usual place,
or named with --browser (or ABOARD_BROWSER).

Pictures go to .aboard/run/shots/<tab>.png, and the previous one is removed
first, so a file there is this run's or nothing.

Under each picture it lists what did NOT FIT in that window, as the page itself
measured it: a ui component or an html widget whose content is larger than its
box, cut off, spilling past its edge, or scrolling inside the tab. That catches
what a picture hides — the text below a clipped edge, the column a table
scrolls away — and it is measured at --width, so try a narrower one too.

What it knows so you do not have to:

  a ui tab's panels     --node <panel label> opens that panel; without it the
                        picture shows the FIRST one, and the output says so
  an html tab           is shot on its own route, because a headless browser
                        does not reliably paint the frame inside the board
  a snap browser        renders into its own directory and the file is moved,
                        because a snap cannot write to a project under a hidden
                        directory such as ~/.cache, or outside $HOME
  mount receipts        none are posted: `ape aboard rendered` and
                        `ape aboard wait --for "rendered <id>"` go on meaning a person
                        had the tab open

It never writes to the board.

Examples:

```
  ape aboard shot ab24
  ape aboard shot ab24 --node Summary
  ape aboard shot kanban dag --theme light --width 1000
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--browser` | string | `—` | the chromium-family browser to drive, a path or a name on $PATH (env ABOARD_BROWSER; default: the first one found) |
| `--height` | int | `900` | window height in pixels |
| `--help-panel` | bool | `false` | open the board's help panel over the tab |
| `--node` | string | `—` | a node's id or a ui panel's label to open first; refused if the tab has none |
| `--output-format` | string | `human` | human, json or yaml |
| `--theme` | string | `—` | dark or light (default: the board's own default) |
| `--timeout` | duration | `1m30s` | how long each picture may take |
| `--width` | int | `1400` | window width in pixels |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | directory to resolve the project root from (default: the working directory) |
| `--name` | string | `—` | board name, for a second isolated board in the same project (env ABOARD_NAME) |

## ape aboard status

Report this project's running board, if any, and the caps beacon

```
ape aboard status [flags]
```

Say whether a board is running for this project, where, and since when — and
whether the committed skill reference still matches this binary.

The caps line is the beacon: an agent runs status as its first act, so a skill
that was generated for a different capsHash is reported in a command it was
going to run anyway. A MISSING reference is not staleness; a project that never
copied the skill has nothing to be out of date.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | human, json or yaml |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | directory to resolve the project root from (default: the working directory) |
| `--name` | string | `—` | board name, for a second isolated board in the same project (env ABOARD_NAME) |

## ape aboard uploads

List the files under .aboard/uploads/ and the tabs that mention them

```
ape aboard uploads [flags]
```

Every image the human pasted or dropped, with its size and the tabs that name it.

The reference scan reads each tab's RAW state text, plus its name and note — not
its declared fields. An html widget's markup can name a file no spec knows
about, and a scan over declared fields would call that file an orphan and offer
to delete an image somebody is looking at.

  ape aboard uploads                    list them, unreferenced ones marked *
  ape aboard uploads --prune            show exactly what deleting them would remove
  ape aboard uploads --prune --yes      delete them

--prune on its own prints and REFUSES: deletion is irreversible and .aboard/ is
gitignored, so there is no copy anywhere to go back to.

The accounting is per PROJECT, not per board, so --name does not narrow it:
.aboard/uploads/ is shared by every board in the project, and a scan of one
board's tabs would call another board's image an orphan. Tab ids from a named
board are printed as <board>:<tab>.

Reads the state files directly, so it needs no server.

Examples:

```
  ape aboard uploads
  ape aboard uploads --prune --yes
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | human, json or yaml |
| `--prune` | bool | `false` | show which unreferenced files would be deleted |
| `--yes` | bool | `false` | with --prune, actually delete them |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | directory to resolve the project root from (default: the working directory) |
| `--name` | string | `—` | board name, for a second isolated board in the same project (env ABOARD_NAME) |

## ape aboard version

Print the build identity of this binary

```
ape aboard version [flags]
```

Which binary is actually serving — never a constant somebody has to remember to
bump, because those lie eventually. Go stamps the VCS revision into a plain
build, so a local binary reports the commit it came from, with "+dirty" when the
tree had uncommitted changes.

A release build carries all three stamps (version, build date, commit) through
ldflags; --output-format json prints them whether or not they were stamped, so a
build that reports "dev" says so plainly instead of looking finished.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | human, json or yaml |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | directory to resolve the project root from (default: the working directory) |
| `--name` | string | `—` | board name, for a second isolated board in the same project (env ABOARD_NAME) |

## ape aboard wait

Block until the board is poked, or until a predicate matches

```
ape aboard wait [flags]
```

Block on one long-poll request until the human presses the notify button, until
another session pokes, or until the write you named arrives.

The predicate vocabulary is deliberately tiny, and an unknown one is refused up
front rather than accepted and never fired:

  poke                 the human pressed Notify (or another session poked)
  change               any accepted write at all
  tab ab71             that tab changed
  answer ab15          that tab changed AND a human made the change
  node ab58=done       that node reached that status
  rendered ab133       a browser MOUNTED that tab and posted a receipt
  request              the human has a note waiting for an agent
  request ab14         one on that tab

Two of them are not about a write. The rendered form is released by the browser
reporting a mount, so waiting on it is waiting for a HUMAN to have the tab open
and nothing here can cause that. The request form is checked the moment you ask
as well as on every write, because a note left an hour ago is already waiting and
blocking on it would mean asking them to write it twice.

While you are waiting the human's button says who is waiting, why, and for how
long — so fill in --note. A waiter is an open connection, so the count cannot go
stale: if this process dies, the button stops claiming anyone is listening.

Exit 0 means released. Exit 3 means the timeout ran out and nobody came.

Examples:

```
  ape aboard wait --for "answer ab128" --note "waiting on the gate"
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--by` | string | `agent-1` | who is waiting; shown on the human's notify button |
| `--for` | string | `poke` | what to wait for: poke \| change \| "tab <id>" \| "answer <id>" \| "node <id>=<status>" \| "rendered <id>" \| "request [<tab>]" |
| `--note` | string | `—` | why you are waiting; shown on the button beside your name |
| `--timeout` | duration | `10m0s` | how long to block before giving up |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | directory to resolve the project root from (default: the working directory) |
| `--name` | string | `—` | board name, for a second isolated board in the same project (env ABOARD_NAME) |

## ape aboard watch

Follow every change as JSON lines until interrupted

```
ape aboard watch
```

Stream each accepted write as one JSON object per line, as it happens.

Not SSE: the consumer here is a shell pipeline, and `data: ` prefixes would just
be something for jq to strip. Each line says THAT something changed and which
tabs; re-read the board for the contents.

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | directory to resolve the project root from (default: the working directory) |
| `--name` | string | `—` | board name, for a second isolated board in the same project (env ABOARD_NAME) |

## ape change

The unit of change: new, check, approve (tweaks), start, verify, review, accept, block, answer, archive, tool

```
ape change
```

A change is development/changes/<id>/change.md (written by the model) plus state.json
(written only by ape). Its lifecycle:

  new → draft ──start──▶ in-progress ──verify green──▶ verified ──review pass──▶ reviewed ──archive──▶ archived
                              ▲   │                        │                         │
                              └───┴── verify red ◀─────────┴──── review block ◀──────┘
  any status but archived ──block──▶ blocked ──start──▶ in-progress
  blocked ──answer (a person)──▶ answered ──re-plan, start──▶ in-progress
  any status but archived ──abandon──▶ abandoned (moved to the archive, no delta merged)

Done is a command ape ran at a sha: models never set a status. Every status change is
committed by ape: state.json alone, subject "apex: <id> <status>", trailer "Change: <id>".
Skills never stage state.json. A red verify of an in-progress change commits nothing (D5):
its attempt is counted in the git-ignored _apex/state/attempts.json. A blocked change
claims no requirements: it takes no part in start's conflict check. A change whose red
verifies reach limits.attempts is blocked by verify itself (see `ape change verify --help`).

Branch per change (trial decision D38):

  base branch ──change new (draft, planned here)──start──▶ change/<id> ──work, verify,
  review (ape's status commits land here)──archive──▶ ONE squash commit on the base
  branch ("<change title>", trailer "Change: <id>"); change/<id> deleted

  - start creates change/<id> from the branch HEAD is on (recorded as base_branch, with
    base_sha) and checks it out; start on a change whose branch exists checks it out
  - start, verify, review and archive run the change's checks, and need HEAD on
    change/<id> (start and archive switch to it; verify and review refuse elsewhere with
    exit 1, code change.wrong-branch: `git switch change/<id>`)
  - records from any branch (D51 Q17): check, diff, approve, accept, block, answer and
    abandon find a change wherever its records live — a started change's on change/<id>,
    a change with no branch of its own (a draft, an approved tweak not started) on the base
    branch, read from the base's tip when HEAD is on another change's branch (that
    branch's copy is the one it was cut with) — and commit there, without a checkout
    when HEAD is elsewhere: HEAD, the index and the working tree stay as they were
    (refused when that branch is checked out in another worktree: git.base-checked-out).
    `ape status` lists the changes the same way, so what it lists these find
  - new, and the milestone and decision-accept records, need HEAD off every change
    branch (code branch.change-branch: `git switch <base>`)
  - archive rebases change/<id> onto the base branch and squash-merges it; abandon
    deletes it. The base branch only ever receives verified, reviewed changes.
  - a change's status is read from its own branch when HEAD is elsewhere, so
    `ape status` on the base branch shows every change's real status

Tweak lane (trial decision D40, D240): a change with "milestone: none" is a tweak — asked
for by a person outside the roadmap (apex-change with a request). The lane says where a
change comes from, its type: what it is (feat, fix, …; `ape change new --help`). A
tweak is approved on its own, `ape change approve <id> --by <who>` (on the base
branch), instead of through a milestone, and otherwise lives like any change: its branch,
baseline, checks, a review in a separate session, the delta merged at archive. It counts toward no milestone gate, no milestone's
regression baseline, and no milestone's spend or budget. Tweaks form a queue: planned and
approved between runs (or `ape run stop`, plan, approve, restart), never overlapping a
run (D45). An approved tweak runs ahead of milestone work, at the loop's next change
boundary; with no milestone run active, `ape task apex-loop --args <id>` runs it
alone. `ape status` lists approved tweaks first.

One checkout, one operator (D45): archive and abandon switch to the base branch (abandon
from another change's branch commits on it without a checkout), so they refuse when another
worktree has it checked out (exit 1, git.base-checked-out, naming that
worktree; nothing written) — an environment problem: the loop stops, it does not
re-plan.

Resume after the base moved (D40): whenever a started change's base branch has moved
ahead of its base_sha, ape — at `ape change start` on a change that has its branch, at
verify and at archive — rebases change/<id> onto the base, re-runs the baseline at the new
base tip, and marks the old verify stale. A conflict is left on change/<id> for
apex-apply (H8): markers in the tree, HEAD on the branch, the change in-progress (exit 3,
change.conflicted, the files listed); apex-apply resolves them, `git add`s and commits, verify
refuses until it has (change.unresolved-conflict), and the review runs again. When
apex-apply cannot resolve them it blocks the change, and the loop re-plans it
(D43). The model never rebases, merges or cherry-picks on a change branch
(`ape hook pre-tool` denies it).

Provenance (H14): start, verify and archive first walk the history since the v1 epoch
(the newest "apex: project init" or "apex: lift" commit) and refuse (exit 1, code
change.provenance, action person, findings spec.edited / state.forged) when a commit wrote
development/specs/** outside an archive squash (a "Change: <id>" trailer of an archived
change) or a state.json outside an "apex: …" commit or a squash. A person reverts the
commit (git revert), which clears it.

Notifier (H20): a subcommand that leaves the change blocked (block, verify at its attempts
limit, a rebase conflict) runs _apex/config.yaml's optional notify: command with
APE_EVENT=block, APE_CHANGE=<id> and APE_REASON=<the blocked reason>; best effort, it never
changes the outcome (see ape task --help).

Exit codes for every subcommand: 0 ok · 1 check failed (one-line remediation) · 2 usage ·
3 conflict (another change claims the same requirement or the delta no longer applies:
nothing written; or the change does not rebase onto its moved base: the conflict is left
on its branch).

Subcommands:

- `abandon` — Move a change to the archive as abandoned, merging no delta; commit
- `accept` — Accept a human-review task, in flight or archived — a person's act, or a delegate's (D75)
- `answer` — A person answers a blocked change: status answered, the answer recorded
- `approve` — Approve a tweak (a change with milestone: none), so it can start
- `archive` — Catch change/<id> up with its base, squash-merge it with the delta and the archive move as one commit
- `block` — Set blocked, with the reason shown by ape status
- `check` — Validate change.md: shape, caps, delta against specs, a check on every task
- `diff` — Show the specs as archive would leave them (unified diff per capability)
- `new` — Validate the id and write the change.md skeleton and state.json (draft)
- `review` — Record a review verdict, or run the review in its own fresh Claude Code session
- `start` — Check green + approved → create and check out change/<id>, record the baseline, set in-progress
- `tool` — Run a tools.design or tools.review entry for a change, in its own detached session (D74, D77)
- `verify` — Run the task checks and the effective spec at HEAD; verified iff all exit 0

## ape change abandon

Move a change to the archive as abandoned, merging no delta; commit

```
ape change abandon <id> --reason "<text>" [flags]
```

Drop a change: its folder moves to development/archive/changes/<YYYY-MM-DD>-<id>/ with
status abandoned and the reason recorded in state.json; its delta is NOT merged into
specs/. Committed as "apex: <id> abandoned" with trailer "Change: <id>". The id stays
taken. Abandon a blocked change only after planning its replacement, or with the reason
none is needed.

A started change (D38): abandon runs from its branch change/<id> — then it switches to the
base_branch — or from any other non-change branch; the tree must be clean (uncommitted
files inside the change's own folder are committed on its branch first). From another
change's branch (D51 Q17) nothing is switched: the move is committed on the base branch
without a checkout, the folder taken as the branch holding its records has it (its own
branch, or the base for a draft), and the working tree is not touched. The archived
folder holds the change's files as its branch last had them, the move is committed on the
base branch, change/<id> is deleted and its last commit recorded as branch_tip in the
archived state.json, kept reachable as refs/apex/abandoned/<id> (tip_ref; H16, local). None of the change's work reaches the base branch. A draft never
started (no branch) is abandoned on the branch HEAD is on, as before.

Abandon never refuses for size (D89): no caps.change_md, file ceiling or caps: check — it
moves the folder as used and never trims or edits it.

Exit codes: 0 abandoned · 1 no such change, archived, dirty tree, change/<id> checked out in
another worktree, the base branch checked out in another worktree (git.base-checked-out), or git
failed · 2 usage · 3 the archive folder already exists.

Examples:

```
  ape change abandon add-tenant-delete --reason "replaced by split-tenant-delete"
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |
| `--reason` | string | `—` | why it is dropped, and what replaces it (required) |

## ape change accept

Accept a human-review task, in flight or archived — a person's act, or a delegate's (D75)

```
ape change accept <id> [T<n>] (--task <T> --by <who> [--evidence <path|url>]... | --delegate [--wait 9m] | --verdict pass|fail --reason "<text>" [--evidence <path|url>]... [--defect <path>]...) [flags]
```

Record that a person accepted a human-review task (one whose check is "human-review"),
in state.json's accepted: [{task, by, at, evidence}]. Archive requires every human-review
task accepted, unless a person archives with --allow-pending-human. The task is the
second argument (T<n>) or --task.

  - a change in flight: committed on its branch, change/<id> (D38) — from any branch
    (D51 Q17): without a checkout when HEAD is elsewhere (refused when that branch is
    checked out in another worktree: git.base-checked-out). A draft (no branch) is
    refused (change.wrong-branch: start it first)
  - a reviewed change with HEAD off change/<id> — on the base branch, say, where a
    person commits the evidence (D52 Q18): recorded on its base branch, in
    development/changes/<id>/accepted.json ({"accepted": [{task, by, at, evidence}]}),
    committed there as "apex: <id> accepted <T>" (trailer "Change: <id>"; without a
    checkout when HEAD is elsewhere). --evidence paths are resolved at the base's tip:
    the file must be committed there (else exit 2, usage.evidence), and that content's
    SHA-256 is recorded. The record never makes the review stale, and a base moved only
    by it (and ape's other records) is not a move archive rebases for. Every reader
    counts it — archive's pending-human check, `ape status` awaiting_human and
    pending_human, milestone close — and archive folds it into the archived state.json
    and drops the file with the folder. With HEAD on change/<id> it is recorded there,
    as before
  - an archived change (H4): its archived state.json, under
    development/archive/changes/<date>-<id>/ (or development/archive/<date>-<id>/, the
    layout before ape project archive-layout), is updated and committed on the base branch it was
    archived into — "apex: <id> accepted <T> (archived)", trailer "Change: <id>" — from any
    branch, without a checkout when HEAD is elsewhere (refused when that branch is checked
    out in another worktree: git.base-checked-out). `ape status` lists archived changes
    with unaccepted human-review tasks until then (JSON awaiting_human)
  - --evidence <path|url>, repeatable: what the acceptance rests on. A URL
    (scheme://…) is recorded as given; a path — relative to the project root — must be a
    regular file in the repository, recorded with its SHA-256 (else exit 2,
    usage.evidence)

Accepting a task again replaces its earlier acceptance.

A person's acceptance (--by) is a person's act (OI-4): refused in a model session
(APE_SESSION or CLAUDECODE set, a `!` line inside Claude Code included) with exit 2,
session.person-only — unless, in the person's own Claude Code session, the person typed
"accept <T> of <id>" in that turn (E2: `ape hook pre-tool --help`): --by is then optional
and must be git config user.name (else session.by-mismatch, exit 1), recorded with the
prompt. --delegate and --verdict are not person acts.

--delegate: autonomous acceptance (D75, D76). Off by default (D78: project init writes no
human_review block; without one, delegate.off, action person). Only when
_apex/config.yaml delegates it:

    human_review:
      delegate: apex-accept                  # the default when the block is present
      args: "--task {task} --change {id}"    # the default; placeholders {id}, {task},
                                             # {assets}, {change} (D77)
      model: sonnet                          # optional, else models.<delegate>
      effort: high                           # optional, else models.<delegate>'s
      docs: person                           # optional: the docs default off (below)

A block that sets only docs: delegates no other task (delegate.off).

The one default-on exception (D152): the docs check of the milestone's docs change
(D130). With no configured delegate, --delegate takes a human-review task of a change
whose change.md frontmatter says docs: true with apex-accept, the default args and
models.apex-accept — no human_review block needed — when:
  - the change's diff from its base_sha to its tip (change/<id>'s; the recorded branch
    tip once archived; a rename counts as both paths) touches only the docs — release.docs
    paths (else README.md and docs/), and the configured docs.changelog
    and docs.user_docs (D158) — and the development folder (and ape's _apex/state/);
    else delegate.docs-scope (action person), naming the paths outside: a code change
    marked docs: true still stops for a person
  - human_review.docs is not person (human_review: {docs: person} turns it off:
    delegate.off); human_review and release are under the config.changed guard, and
    --delegate refuses while either, or the docs block, is a person's unaccepted change
    (delegate.unaccepted-config)
The records are the delegate's below, by "delegate:apex-accept". A configured delegate
(a block with any key but docs:) takes every human-review task, a docs change's
included, whatever human_review.docs says (D78). `ape status --json` lists what a
delegate may take now (changes[].delegable).

  - the task must be awaiting a person: the change reviewed (or archived) and the task
    not accepted (else delegate.not-awaiting); human_review is under the config.changed
    guard, and
    --delegate refuses while a commit no person accepted changed it, or while the
    working tree's block differs from HEAD's (delegate.unaccepted-config, action person):
    a model cannot add the block and accept its own work. Not person-only: that is the
    point
  - ape starts `ape task <delegate> --args "<args filled>"` detached, as review --run
    does, with APE_DELEGATE=<id>/<T> (and APE_DELEGATE_PARENT=<the asking session>). Model
    and effort: human_review's, else models.<delegate>'s, else the asking session's, else
    _apex/config.yaml effort:, else claude's
  - the delegate inspects the task's artefact read-only and records its verdict with
    --verdict (below); `ape hook pre-tool` denies it every edit inside the project but
    an evidence note, changes/<id>/assets/accept-* (the archive folder's, for an
    archived change)
  - when the session ends ape settles it. A session that committed, switched branch or
    left any file other than its accept-* notes has its verdict voided (delegate.voided,
    action person; recorded in the verdict file, shown by ape status). On a pass ape
    records the acceptance exactly as a person's (the same record and branch rules as
    above), by "delegate:<skill>" — never a person — with "run": <run-id>; its evidence
    is the verdict's --evidence, else the verdict file; its accept-* notes are committed
    with the record, which carries the trailer
    "Accepted-By: delegate:<skill> <T> run <run-id>" (and so does the archive squash).
    On a fail nothing is accepted. A session that records no verdict is a fail, reason
    "no verdict" (D78; its last message is never read for one), or, when its last Stop
    still had background work running, reason "ended with live background work:
    <tasks>" (D88; JSON outcome.background lists them)
  - a fail goes back to the change (D188), as a review block does, when it names at
    least one defect in the change's own diff (--defect <path>, a path its diff from
    base_sha to its tip touches; a path outside the diff is recorded but does not count:
    outcome.defects_outside), the change is reviewed and not archived, and the task has
    no return since a person last answered the change or accepted the task: the change
    goes to in-progress with its review cleared (verify and review run again), state.json
    records delegate_returns.<T>: [{run, reason, defects, at}], committed as
    "apex: <id> returned by delegate" where its state lives, exit 1 delegate.returned
    (action apply); ape status shows the reason, and next names apex-change.
    JSON outcome.returned is the return
  - any other fail stays a person's (D78, as D188 amends it): one that names no in-diff
    defect ("no verdict" included), a second fail of the task after a return, or a
    fail on an archived change (ape cannot reopen it: a person accepts the task, or has
    a tweak planned). The task stays awaiting_human, ape status shows the verdict
    (JSON changes[].delegate.<T>: {verdict, reason, run, at, defects, returned}), exit 1
    delegate.fail (action person); JSON outcome.to_person says why: no-defect-in-diff,
    second-fail, archived or not-reviewed (outcome.returns lists the earlier returns)
  - no automatic retry on the same artefact (D78): after a fail, --delegate for that task
    refuses (delegate.already-failed, action person) until the change's tip moves
    (change/<id>'s tip; the archive folder's content once archived)
  - --wait (default 9m) bounds one call; exit 75 means still running: call
    `ape change accept <id> <T> --delegate` again — it attaches to the running session
  - JSON: {id, task, skill, args, running, detached_id, log, task_exit, launch,
    outcome: {verdict, reason, evidence, notes, violations, voided, accepted,
    verdict_file, background, defects, defects_outside, returned, to_person, returns}}

--verdict pass|fail --reason "<text>" [--evidence <path|url>]... [--defect <path>]...: the delegate session
records its verdict — only the session --delegate started for that change and task
(APE_DELEGATE=<id>/<T>, its run in progress; elsewhere exit 2, delegate.not-in-session).
It writes _apex/state/runs/delegate/<run-id>.json {change, task, skill, run, verdict,
reason, evidence, at, change_tip, defects} — ignored state, never a commit. ape, not the session,
records the acceptance. --evidence (D78): a URL as given; the session's own
assets/accept-* note as it is on disk (ape commits it with the acceptance); any other
path must be committed at change/<id>'s tip while the change is unarchived — where its
design artefacts live, whatever branch HEAD is on — and a file in the working tree once
it is archived; each recorded with its SHA-256 (else exit 2, usage.evidence). With no
--evidence the acceptance rests on the verdict file, recorded by its path and SHA-256.
--defect <path>, repeatable, only with --verdict fail (else usage.delegate): a file of the
change's own diff the fail found defective (the docs the change wrote, not a spec the
delegate disagrees with), relative to the project root; ape, not the session, checks it
is in the diff when the session ends (above).

Exit codes: 0 recorded · 1 the task has a command check, a draft, or the branch it is
recorded on is checked out elsewhere; --delegate: off, config not accepted, not
awaiting, already failed at this tip, returned to the change, fail (no verdict
included), voided · 2 usage (no such task, bad evidence, --verdict
outside its session) · 75 --delegate: still running, call again.

Examples:

```
  ape change accept add-tenant-delete --task T2 --by diego
  ape change accept add-tenant-delete T2 --by diego --evidence docs/review/tenant-delete.png --evidence https://ci.example.com/run/812
  ape change accept add-tenant-delete T2 --delegate --wait 9m --json
  ape change accept add-tenant-delete T2 --verdict pass --reason "matches the task" --evidence development/changes/add-tenant-delete/assets/accept-T2.md
  ape change accept add-docs T2 --verdict fail --reason "the guide shows <TAB> where T2 asks for a literal tab" --defect docs/guide.md
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--by` | string | `—` | who accepts (required for a person's acceptance) |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--defect` | stringArray | `[]` | --verdict fail: a file of the change's own diff the fail found defective (repeatable; D188: a fail naming one goes back to the change once) |
| `--delegate` | bool | `false` | accept through _apex/config.yaml human_review.delegate (or, for a docs change, the docs default, D152), in its own detached session (D75) |
| `--evidence` | stringArray | `[]` | a file in the repository or a URL the acceptance (or --verdict) rests on (repeatable) |
| `--json` | bool | `false` | machine output (JSON on stdout) |
| `--reason` | string | `—` | --verdict: why |
| `--task` | string | `—` | task id, e.g. T2 (or the second argument) |
| `--verdict` | string | `—` | pass or fail: the delegate session's verdict (only inside that session) |
| `--wait` | duration | `9m0s` | --delegate: how long this call waits for the session (exit 75 when it is still running) |

## ape change answer

A person answers a blocked change: status answered, the answer recorded

```
ape change answer <id> --text "<answer>" --by <who> [flags]
```

Record a person's answer to what blocks a change (H9, D47): status answered, and
answer: {by, at, text} in its state.json (also in `ape status --json`, changes[].answer).
Only for a blocked change (else exit 1, change.wrong-status). Committed as
"apex: <id> answered" (trailer "Change: <id>") where the change's state lives — its branch
change/<id> when it has one, else the branch the draft was planned on — from any branch:
without a checkout when HEAD is elsewhere (refused when that branch is checked out in
another worktree: git.base-checked-out).

The loop then has apex-change re-plan the change with the answer as the hint and
starts it again: `ape change start` takes an answered change as it takes a blocked one (a
re-plan is recorded as a re-start). The answer also restarts the counts limits.replans and
limits.attempts_total judge (`ape change verify --help`).

Only a person answers (D47 Q6): refused in a model session — APE_SESSION or CLAUDECODE set,
a `!` line inside Claude Code included — with exit 2, session.person-only — unless, in
the person's own Claude Code session, the person typed "answer <id>: <the --text>" in that
turn (E2: `ape hook pre-tool --help`; --by is then git config user.name, recorded with the prompt).

Exit codes: 0 answered · 1 not blocked, no such change, or the branch is checked out in
another worktree · 2 usage (no --text or --by), or a model session.

Examples:

```
  ape change answer add-tenant-delete --text "soft delete; keep rows 30 days" --by diego
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--by` | string | `—` | who answers (required) |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |
| `--text` | string | `—` | the answer (required) |

## ape change approve

Approve a tweak (a change with milestone: none), so it can start

```
ape change approve <id> --by <who> [flags]
```

Record a person approving a tweak (trial decision D40, D240): a change with "milestone: none",
written by apex-change for a person's request outside the roadmap. A milestone change is
approved through its milestone (`ape milestone approve <M> --by <who>`); approve refuses one
(exit 1, change.approve-milestone). The approval is what `ape change start` of a tweak needs
in place of an approved milestone (else exit 1, change.not-approved).

  - the tweak must pass `ape change check <id>` and not be started yet (status draft, no
    change/<id> branch; else exit 1, change.wrong-status)
  - recorded in state.json as approved: {by, at, plan_digest} and committed alone on the
    base branch as "apex: <id> approved" (trailer "Change: <id>"), from any branch (D51
    Q17): with HEAD on a change's branch, the tweak is read and checked as the base holds
    it and the approval committed there without a checkout (refused when the base is
    checked out in another worktree: git.base-checked-out)
  - the approval is of the plan change.md has now: a tweak whose plan (change.md outside
    ## Why and ## Notes) changed since is refused at start until approved again
  - re-approving the same plan is a no-op (exit 0, JSON "already": true, nothing written)
  - only a person approves (D47 Q6): refused in a model session — APE_SESSION or
    CLAUDECODE set, a `!` line inside Claude Code included — with exit 2, code
    session.person-only; run it from a plain terminal, or, in the person's own Claude
    Code session, the person types "approve <tweak-id>" in that turn (E2: `ape hook pre-tool --help`;
    --by is then git config user.name, recorded with the prompt)

Tweaks form a queue: planned and approved between runs (or `ape run stop`, plan, approve,
restart), never overlapping a run (D45). An approved tweak runs ahead of milestone work, at
the loop's next change boundary; with no milestone run active,
`ape task apex-loop --args <id>` runs it alone. `ape status` lists approved
tweaks first (JSON "tweaks", changes[].lane "tweak", changes[].approved) and points next at
the first. A tweak counts toward no milestone gate and no milestone's spend or budget.

Exit codes: 0 approved (or already) · 1 not a tweak, check red, started, or the base branch
checked out in another worktree · 2 usage (no --by), or a model session.

Examples:

```
  ape change approve fix-login-redirect --by diego
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--by` | string | `—` | who approves (required) |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |

## ape change archive

Catch change/<id> up with its base, squash-merge it with the delta and the archive move as one commit

```
ape change archive <id> [flags]
```

Archive a reviewed change onto its base branch (trial decision D38):

  1. needs status reviewed (review pass), the plan frozen at start unchanged, and change.md
     without ## Notes within caps.change_md (default 16 KB, D69, D89 — change.too-big: the
     plan is frozen at start, so only ## Why can have grown; move what it gained since
     start to ## Notes, commit, archive again), the whole change.md within the 48 KB file
     ceiling (change.over-ceiling: move bulk to the change's assets/), and no
     caps.over-ceiling (caps: holding a value above its ceiling); human-
     review tasks must be accepted, unless --allow-pending-human (recorded) — a person's
     flag only (D45): refused in a model session (APE_SESSION or CLAUDECODE set) with exit
     2, session.person-only — unless, in the person's own Claude Code session, the person
     typed "allow pending human on <id>" in that turn (E2: `ape hook pre-tool --help`; recorded
     as allow_pending_human_by, and a plain archive running on the change is run.busy); the
     loop leaves such a change reviewed and moves on.
     An archived change's unaccepted tasks stay in `ape status` and stay acceptable
     (`ape change accept --help`)
  2. runs with HEAD on change/<id>, or on another non-change branch (it switches to
     change/<id>); the tree must be clean — uncommitted files inside the change's own
     folder (review.md a review left) are first committed on change/<id> as
     "apex: <id> notes"; anything else refuses (code tree.dirty)
  3. refused if the work changed on change/<id> since the review (review.stale); refused up
     front while the base branch is behind its upstream (base.behind-upstream: pull;
     the local remote-tracking ref, never fetched — D45)
  4. when the base branch moved ahead of base_sha, catches the change up — the one path
     start and verify share (D40, `ape change start --help`): rebases change/<id> onto the
     base (on a conflict nothing reaches the base: the conflict is left on change/<id>
     for apex-apply, the change back to in-progress, and archive exits 3,
     change.conflicted), re-runs the baseline at the new
     base tip and marks the verify stale; here the change stays reviewed
  5. re-runs verify when the work moved since it or it is stale — the base had moved (red
     → the change goes back to in-progress on its branch, exit 1, counted as an attempt;
     a green one keeps it reviewed: the review judged the change's own work)
  6. applies the delta to development/specs/: ADDED appends, MODIFIED replaces, REMOVED
     deletes; a new capability gets specs/<cap>/spec.md (Purpose from the change's Why).
     Any conflict — including a MODIFIED that would drop a scenario the current spec has,
     or change its check, or a MODIFIED/REMOVED requirement whose spec block changed since
     start (another change archived first: re-plan and re-start) — → exit 3 and nothing
     reaches the base branch
  7. switches to the base branch, `git merge --squash change/<id>`, writes the specs,
     moves the folder, exactly as used — archive never trims it or edits change.md —
     to development/archive/changes/<YYYY-MM-DD>-<id>/ (status archived,
     branch_tip = change/<id>'s last commit) — with "evidence: archive" in
     _apex/config.yaml (H23; default off) also its evidence/: one file per check of the
     final verify (command, exit, duration, output sha256, and the output from the log,
     capped at caps.evidence_log, default 1 MiB: head and tail kept, the full log named by
     path and sha256) — and
     commits everything as ONE commit (H15):
     subject from _apex/config.yaml commit.subject — placeholders {type} (change.md's
     type:), {title} (its # title, as written) and {id}; default "{type}: {title}" (D193;
     "{title}" before rc.7: set commit.subject: "{title}" to keep it) — and a body holding the
     branch commits' own subjects (not ape's "apex:" ones) and every trailer of theirs but
     "Change:", deduplicated, then "Change: <id>". Under the default only (commit.subject
     unset), a change with no type: — one in flight when the default changed — archives
     with "{title}" alone, and says so ("subject: … the title alone"; JSON
     "subject_fallback": true, Q111); a configured template that uses {type} refuses a
     change with no type: (change.check-failed, change.no-type). ape's own "apex: …"
     commits are unchanged (Q112). The project's git hooks run on this
     commit (commit.hooks: run, the default; skip opts out) — ape's status commits keep
     skipping them. A hook that refuses rolls archive back: exit 1, archive.hook-failed,
     the hook output's tail in the message. Then keeps change/<id>'s tip as
     refs/apex/archive/<id> (H16: recorded as tip_ref in the archived state.json, so
     branch_tip and the verify and review shas survive git gc; local, never pushed by
     ape) and deletes change/<id>. If anything fails
     on the base branch, it is reset to where it was and change/<id> checked out again.

The base branch gets exactly one commit per archived change; ape's status commits stay on
the change branch, squashed away.

A crash mid-archive (H28): archive keeps a journal, _apex/state/archive-journal.json
(ignored; base, its sha before, the branch and its tip, the step), from before the
catch-up until it returns. A journal that outlives its process is rolled back by the next
v1 command that writes (start, verify, review, accept, block, archive, abandon, change /
milestone / decision new, approve, gate, close, decision accept): the base branch back at
its pre-archive sha (a squash commit undone, a half squash dropped — cleaning only the
paths archive writes), change/<id> at its recorded tip and checked out. That command then
exits 1, archive.interrupted (retry), saying what was rolled back; archive again.
`ape status` reports the journal (JSON "archive_journal") and changes nothing.

Exit codes: 0 archived (prints the commit) · 1 not reviewed, re-verify red, stale review, a
gate edit (change.touches-gate, as by verify), a git hook refused the squash,
pending human-review, change.md over its cap or the file ceiling, dirty tree, no change
branch, the base behind its upstream
(base.behind-upstream), the base branch checked out in another
worktree (git.base-checked-out: stop, do not re-plan), or git failed · 2 usage, or
--allow-pending-human from a model session · 3 the
rebase conflicts or the delta no longer applies.

Detached runs (D58 2.3): the command takes the run lock of its change (or milestone),
_apex/state/runs/<id|M>.json, and runs in a detached worker (`ape __run`, its own
session, output in _apex/state/runs/<id|M>.log), so the call that started it can be cut,
backgrounded or killed without stopping it. The call waits up to --wait (default 9m, at
most 9m30s; 0 returns at once); a run that ends in time is printed as always, with the same
exit code and --json shape. Otherwise the call exits 75, still running: "… still running
since <time> — run the same command again to attach" (--json: {"running": true, "verb",
"scope", "since", "log", "pid", "log_tail"}, the shape `ape change review --run` has).
Running the same command again attaches: it waits again and prints the run's result once it
ends — never a second run — or exits 75 again with the log lines the run wrote since the
last call (stderr; --json: "log_tail"). A result no call
printed yet is printed by the next call of the same verb while HEAD has not moved. While a
run holds the lock, another of these verbs on the same change or milestone is refused:
run.busy (exit 3, action retry; the message names the running verb and since when). A lock
whose process is gone (killed, the machine restarted) is removed and reported as
run.interrupted (exit 1, action retry) — never an attempt. A run stopped by SIGTERM/SIGINT
kills its checks and records nothing — no attempt, no state commit, no gate run — and
reports run.interrupted. No check outlives the worker. Never run these with
run_in_background, and never end a turn on an exit 75: run the same command again.
Beside the exit codes above: 75 still running · 3 run.busy · 1 run.interrupted · 2 a
--wait outside 0–9m30s (usage.wait).

Examples:

```
  ape change archive add-tenant-delete
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--allow-pending-human` | bool | `false` | archive with human-review tasks unaccepted (recorded in state.json; a person only) |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |
| `--timeout` | duration | `0s` | ceiling for each command run, overriding _apex/config.yaml timeouts.<tier>.max (default: check 2h, regression 2h, gate 4h; capped at ape's hard ceilings 4h/8h/12h); the tier's idle window still applies; exit 124 when exceeded |
| `--wait` | duration | `9m0s` | how long this call waits for the detached run (at most 9m30s; 0 returns at once); exit 75 while it still runs — run the same command again to attach |

## ape change block

Set blocked, with the reason shown by ape status

```
ape change block <id> --reason "<text>" [flags]
```

Set a change blocked. The reason is what `ape status` shows: what blocks it, or the
question only a person can answer. A blocked change lets the stop hook allow a stop.
`ape change start <id>` resumes it. Recorded where the change's records live, from any
branch (D51 Q17; `ape change --help`): a started change on its branch, change/<id>
(D38), where it stays — re-plan it there and start it again, or abandon it; a change with
no branch (a draft, an approved tweak) on the base branch. Without a checkout when HEAD is
elsewhere. A milestone change's block is counted in milestones.json on the base branch
(limits.consecutive_blocks).

Exit codes: 0 blocked · 1 no such change, archived, or the branch it is recorded on is
checked out in another worktree · 2 usage.

Examples:

```
  ape change block add-tenant-delete --reason "needs a decision: soft or hard delete?"
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |
| `--reason` | string | `—` | what blocks it (required) |

## ape change check

Validate change.md: shape, caps, delta against specs, a check on every task

```
ape change check <id> [flags]
```

Validate one change and list every failure:

  - frontmatter id = folder, milestone in roadmap.md (or "none": a tweak, D40, approved
    with `ape change approve`), touches non-empty kebab slugs, decisions exist in
    decisions/, paths are valid globs; type: (the archive commit's {type}, D47) one of
    the project's commit types — _apex/config.yaml commit.types (D120), default feat,
    fix, perf, refactor, docs, test, build, ci, chore — required when the subject
    template uses {type}, as the default "{type}: {title}" does (D193; change.no-type;
    change.bad-type; ape change new writes one);
    review_tools: [<name>, …] names tools.review entries of _apex/config.yaml whose
    when: the change matches (the loop runs each before the review; else
    tools.unknown, D74); docs: true marks the milestone's docs change (D130, D152: its
    human-review tasks are the docs check, a delegate's by default — `ape change accept --help`),
    a YAML boolean only (else change.frontmatter); any other key must start with x-
  - # title; ## Why and ## Out of scope non-empty; ## Delta; ## Tasks
  - change.md without its ## Notes section ≤ caps.change_md (D89; change.too-big) and
    design.md ≤ caps.design_md (16 KB each by default; _apex/config.yaml caps:, see
    `ape project check --help`) ("split this change"); ## Notes is bounded only by
    the 48 KB file ceiling on the whole change.md, whatever the cap (change.over-ceiling:
    ## Notes is for working notes — move bulk to the change's assets/)
  - every spec the delta touches stays ≤ caps.spec (24 KB) once merged — the in-memory merge
    `ape change diff` shows and archive makes (else change.spec-too-big: split the
    capability first; start runs this check too, so it fails before any work, H18)
  - ## Delta: "### ADDED|MODIFIED|REMOVED [<capability>] Requirement: <name>" blocks, or
    the single line "none — <reason>". The capability must be in touches. ADDED names must
    not exist in specs/<capability>/spec.md yet; MODIFIED/REMOVED must name an existing
    heading exactly. ADDED/MODIFIED carry ≥ 1 "#### Scenario:" with exactly one
    "- check: <command> | human-review"; REMOVED carries "Reason:". A MODIFIED block
    keeps every scenario of the requirement it replaces, each with the same check
    (it may add scenarios); changed behaviour is REMOVED + ADDED under a new name.
    A name that nearly matches an existing heading (case, spacing, a typo) gets a
    "did you mean"; an ADDED that nearly matches one is refused. An ADDED block of a
    new capability may carry its "Purpose:": that line and every line after it up to a
    blank line, heading or fenced code, kept whole as the new spec's Purpose (never in
    the requirement)
  - nothing archive would drop: no prose before the first delta block, nothing but
    "Reason:" in a REMOVED block
  - a delta that REMOVES every requirement of a capability needs "retire: [<cap>]" in
    the frontmatter (archive then deletes its spec); retire: lists only such capabilities
  - ## Tasks: "- T<n> <what> — check: <command> | human-review", or "- T<n> <what> —
    regression: <command>" for a check that may already pass before the change (it runs
    like any check task; `ape change start` lets it be green at baseline); no checkboxes,
    unique ids. A design-tool task names its tool before the check:
    "- T<n> <what> — tool: <name> — check: <command> | human-review", <name> a
    tools.design entry of _apex/config.yaml (else tools.unknown, D74; see
    `ape plugins --help`); ape status --json lists it under changes[].tools
  - a human-review check is exactly "human-review": the artefact and the criteria go
    in <what>, before " — check:". A check that starts with human-review or "human
    review" in any case but is anything else ("human-review: read the README",
    "Human-Review", "human-review.") would run as a shell command, and is
    change.malformed-human-review (action replan; start refuses it)

The change is checked as the branch holding its records has it, against that branch's
specs and roadmap (D51 Q17; `ape change --help`): HEAD needs to be on no particular branch.

Exit codes: 0 green · 1 findings · 2 usage.

Examples:

```
  ape change check add-tenant-delete
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |

## ape change diff

Show the specs as archive would leave them (unified diff per capability)

```
ape change diff <id> [flags]
```

Merge the change's delta into the current specs in memory — the same merge archive
makes, nothing written — and print a unified diff per capability it touches (a new
capability diffs from /dev/null). The change and the specs are read as the branch holding
its records has them, from any branch (D51 Q17). For review and for handoff: what the living spec will
say once this change is archived. JSON: {id, specs: [{capability, path, created, added,
modified, removed, diff}], delta_none?}.

Exit codes: 0 · 1 no such change · 2 usage · 3 the delta no longer applies to the specs
(conflicts listed).

Examples:

```
  ape change diff add-tenant-delete
  ape change diff add-tenant-delete --json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |

## ape change new

Validate the id and write the change.md skeleton and state.json (draft)

```
ape change new <id> --milestone <M>|none --touches a,b [--paths globs] [--type <t>] [flags]
```

Create development/changes/<id>/ with a change.md skeleton (frontmatter id, milestone,
type, touches, paths; # title; ## Why, ## Out of scope (pre-filled with the milestone's Out of
scope bullets), ## Delta, ## Tasks, ## Notes) and state.json with status draft, and
commit both as "apex: <id> draft" (trailer "Change: <id>"). Filling change.md in is the
author's own commit. A change is planned on the base branch: refused while HEAD is on a
change branch, change/<other> (exit 1, code branch.change-branch; `git switch <base>`).

<id> is a verb-led kebab slug, ^[a-z][a-z0-9-]{2,39}$, unique across active AND archived
changes (development/archive/changes/, development/archive/ itself as an older ape archived
them, and an older ape's development/changes/archive/).
--milestone none makes the change a tweak (trial decision D40, D240): what apex-change writes for
a person's request outside the roadmap. It needs no roadmap entry, inherits no Out of
scope, is approved on its own (`ape change approve <id> --by <who>`) instead of through a
milestone, and counts toward no milestone; it may touch capabilities and carry a delta
like any change.
--type is change.md's type: (Q110), the archive commit's {type} — the default subject is
"{type}: {title}" (D193): one of the project's commit types (_apex/config.yaml
commit.types, default feat, fix, perf, refactor, docs, test, build, ci, chore; else exit 2,
usage.type). It is required with --milestone none (exit 2, usage.type, naming the
project's types; Q135): a tweak says what it is — fix for a defect, feat for a small
feature — so an off-roadmap feature never lands under Fixed. Without it a milestone's
change gets feat (none when feat is not among the project's commit types); the planner
may change it before start. JSON "type" is the type written.
--touches lists the capabilities the change adds to or modifies (kebab slugs; new ones
are fine — a capability is born from its first change). --paths are globs used for
overlap detection. Overlap (shared touches or paths) with another unarchived change
is a warning, not a failure.

Then fill change.md and run `ape change check <id>`.

Consecutive blocks (H19, D49): every block of a milestone's change (`ape change block`, a
limit reached at verify or start, a rebase conflict) counts toward its milestone in
_apex/state/milestones.json (consecutive_blocks), committed on the base branch from
whichever branch as "apex: <M> consecutive blocks <n> (<id>)"; an archived change of the
milestone sets it back to 0, inside the archive's commit. At limits.consecutive_blocks
(default 2), change new and change start of the milestone's changes and
`ape task apex-loop` for it refuse with exit 1, code milestone.blocks-exceeded,
action stop, and `ape status --json` reports it under milestone.diagnostics. A person deals
with the blocked changes (answer or abandon them) and resets the count by approving the
milestone again: `ape milestone approve <M> --by <who>`. Tweaks count toward no milestone.

Exit codes: 0 ok · 1 id taken (incl. archive), milestone not in roadmap, the milestone past
limits.consecutive_blocks, or HEAD on a change branch · 2 usage (incl. usage.type: --type
unknown, or missing with --milestone none).

Examples:

```
  ape change new add-tenant-delete --milestone M2 --touches tenants --paths 'internal/api/**'
  ape change new fix-login-redirect --milestone none --type fix --touches auth
  ape change new document-the-api --milestone M2 --touches api --type docs
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |
| `--milestone` | string | `—` | milestone id from roadmap.md, or none for a tweak (required) |
| `--paths` | string | `—` | comma-separated globs, for overlap detection |
| `--touches` | string | `—` | comma-separated capability slugs (required) |
| `--type` | string | `—` | change.md's type: (a commit type); required with --milestone none, default feat otherwise |

## ape change review

Record a review verdict, or run the review in its own fresh Claude Code session

```
ape change review <id> (--verdict pass|block --findings <file> | --run [--wait 9m] [--model M]) [flags]
```

Record the review of a verified change. --findings names the review file (normally
development/changes/<id>/review.md); it must exist and is recorded by path.

  pass   needs a green verify whose work has not moved since → status reviewed
  block  → status in-progress (fix, verify, review again)

HEAD must be on the change's branch, change/<id> (D38; else exit 1, code
change.wrong-branch): the verdict is committed there.

--run reviews the change in its own Claude Code session — a separate process with an
empty context and its own hooks, never a sub-agent of the session that did the work:
ape starts `ape task apex-review --args <id>` detached (survives the caller's shell),
and that session writes review.md and records the verdict with --verdict/--findings.
It counts as starting apex-review under the nesting rule (D62; see ape task --help):
from a terminal (depth 0) always; from inside an ape session only from ape's own
apex-loop run, as its one child (exit 3, session.child-running, while
another child runs, checked before the change is read); from any other session — apex-apply a person ran with ape task,
say — refused (exit 2, session.depth); never from inside a review session. The
review session is depth 2 under a loop run, its manifest records depth and
parent_run, and it is stopped when that loop run ends.
  - which changes: a verified one; or a reviewed one whose review is stale — the work
    changed since the review's sha, what archive refuses as review.stale (D51 Q16):
    --run first verifies it again in-process, as `ape change verify <id>` would
    (JSON "verify"; a red verify is returned as its error, verify.red, and no session
    starts), then reviews it as a verified change. A reviewed change whose review is
    fresh is refused (change.wrong-status): archive it.
  - on the change's branch: --run needs HEAD on change/<id>, the session runs in this
    checkout and records its verdict there; a session that leaves HEAD elsewhere
    has its verdict voided and the branch checked out again
  - read-only: the session runs with APE_REVIEW=<id>, and `ape hook pre-tool` denies
    every edit inside the project except changes/<id>/review.md (outside the project,
    e.g. a throwaway copy to break, is allowed)
  - post-run assertion: a session that committed or left anything but review.md, the
    review tools' reports changes/<id>/assets/review-<name>.md (written before --run,
    committed with review.md; D74) and the state.json ape commits has its verdict
    voided (recorded under review_voided). A commit touching only development/ (assets/
    included) after the verify or the review leaves both fresh
  - --wait (default 9m, at most 9m30s, under the Bash tool's 10-minute cap; 0 returns
    at once) bounds one call; exit 75 means still running: call
    `ape change review <id> --run` again to keep waiting — it attaches to the running
    session, it never starts a second one. The --json payload of a 75 has the fields every
    long verb's has (`ape change verify --help`): "running": true, "verb", "scope",
    "since", "log"
  - the session's cost is its own ape task manifest (ape status sums it)
  - model and effort: --model/--effort; else _apex/config.yaml models.apex-review
    ("apex-review: sonnet" or "apex-review: {model: sonnet, effort: high}"; a config
    model without an effort does not take the asking session's effort); else the
    model and effort of the ape session that asked (so a review runs like the arm
    that requested it); else, for the effort, _apex/config.yaml effort:; else claude's
    defaults. The effort is passed as claude --effort. The review session's manifest
    records each and its source under review_launch (and, the same, under launch).

Exit codes: 0 recorded / review passed · 1 not verified, the work changed since the verify,
--run: a fresh review, or a red re-verify of a stale one, review block (--run: findings
in JSON "outcome"), verdict voided, or no verdict (review.no-verdict; when the session's
last Stop still had background work running, the message says "ended with live
background work: <tasks>" and JSON outcome.background lists them) · 2 usage
(bad verdict, findings file missing, --run inside a review session, or the nesting
rule: session.depth) · 3 --run: the loop's child session is running
(session.child-running) · 75 --run: still running, call again.

Examples:

```
  ape change review add-tenant-delete --verdict pass --findings development/changes/add-tenant-delete/review.md
  ape change review add-tenant-delete --run --wait 9m --json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--effort` | string | `—` | --run: effort for the review session (default: models.apex-review's effort, else the asking session's) |
| `--findings` | string | `—` | the review file (required without --run) |
| `--json` | bool | `false` | machine output (JSON on stdout) |
| `--model` | string | `—` | --run: model for the review session (default: models.apex-review in _apex/config.yaml, else the asking session's) |
| `--run` | bool | `false` | review in a fresh, detached, read-only Claude Code session (apex-review) |
| `--verdict` | string | `—` | pass or block (required without --run) |
| `--wait` | duration | `9m0s` | --run: how long this call waits for the session (at most 9m30s; 0 returns at once); exit 75 when it is still running |

## ape change start

Check green + approved → create and check out change/<id>, record the baseline, set in-progress

```
ape change start <id> [flags]
```

Start a draft (or resume a blocked or answered) change:

  1. `ape change check <id>` must be green, and its milestone approved — through ape: the
     approval record in milestones.json, not the Status line alone (H2; else exit 1,
     milestone.status-unrecorded: a person runs `ape milestone approve <M> --by <who>`) — or, for a tweak
     (milestone: none, D40), the tweak approved for the plan it has
     (`ape change approve <id> --by <who>`; else exit 1, change.not-approved)
  2. refused with exit 3 if another active change (in-progress, verified, reviewed —
     not blocked; read from its own branch) also ADDs, MODIFIES or REMOVES one of the
     same requirements
  3. creates the branch change/<id> at HEAD and checks it out (trial decision D38): the
     tree must be clean (ignored files aside), HEAD on a branch (not detached, not another
     change's change/<other>); records that branch as base_branch, and base_sha (HEAD)
     and started_at (both kept when resuming a blocked change). A refused start leaves no
     branch and HEAD where it was. Refused while that branch is behind its upstream
     (exit 1, base.behind-upstream: pull; compared with the local remote-tracking ref,
     never fetched; no upstream, no check — D45: one operator per checkout, local trunk)
  4. runs every check verify will run once, as the baseline (which were already green):
     the command tasks and the effective spec's scenario checks of the capabilities the
     change touches (plus any scenario sharing a task's command — see ape change verify
     --help); from the project root with sh -c; output under _apex/state/logs/<id>/. With
     "regression: <cmd>" in _apex/config.yaml (the project's own check command; with tiers,
     "regression: {change: <cmd>, milestone: <cmd>}", its change tier) it runs that too,
     the same way, and records it in state.json (regression_baseline: cmd, exit, output
     sha256, sha); a red regression baseline is recorded, it does not block start.
     Vacuous checks (backlog 43): when the change has an ADDED or MODIFIED delta, a task
     check that already passes here, before any of the change's work, proves nothing
     about it — start refuses (exit 1, change.vacuous-check, nothing written, no branch
     left): rewrite the check to fail until the change is done, or write a check meant to
     stay green as "- T<n> <what> — regression: <command>". Not judged for Delta: none,
     a REMOVED-only delta, or a task a re-plan adds after the change has work
  5. sets in-progress, freezes the plan (SHA-256 of change.md outside ## Why and ## Notes), fingerprints
     the spec block of every requirement it MODIFIES or REMOVES, commits state.json
     ("apex: <id> in-progress") on change/<id>, and resets the attempts count since start
     (_apex/state/attempts.json)

All of the change's work — work commits, ape's status commits — then happens on
change/<id>. Start on a change whose branch exists, from another branch (the base, say),
checks change/<id> out first (clean tree); when the change is started and nothing needs
re-baselining it only resumes there (JSON "resumed": true). Re-start keeps base_branch.
A blocked change is re-planned on its branch (change.md there), then started again.

Resume after the base moved (D40): start on a change that has its branch first checks
whether the base branch moved ahead of base_sha (a tweak or another change landed there).
If so ape catches the change up — the one path start, verify and archive share:
  - start only: refused while the base branch is behind its upstream (exit 1,
    base.behind-upstream: pull)
  - the tree must be clean (uncommitted files in the change's own folder are committed on
    its branch; anything else: exit 1, tree.dirty)
  - `git rebase <base_branch>` on change/<id>. A conflict (H8, D47 Q3) aborts the rebase;
    ape takes the baseline below at the base tip, keeps the old tip as
    refs/apex/pre-rebase/<id>/<n>, recreates change/<id> at the base tip with the change's
    folder as the old tip had it and the state recorded (status in-progress, the verify
    stale, rebases[] with old_tip_ref and conflicts), committed as "apex: <id> conflicted
    rebasing onto <base>"; then it cherry-picks the branch's net diff (merge-base..old
    tip) --no-commit, which leaves conflict markers and unmerged paths in the tree with
    HEAD on change/<id>: exit 3, change.conflicted, the files listed. apex-apply resolves
    them (edit, `git add`, commit with trailer "Change: <id>"); verify refuses until no
    unmerged path or marker remains (exit 1, change.unresolved-conflict); verify and the
    review then run again. When apex-apply cannot resolve them it blocks the change, and
    the loop re-plans it (D43). A net diff that applies cleanly after all is
    committed as "apex: <id> re-applied onto <base> as one commit", and the catch-up ends
    as a clean one
  - the baseline (task checks, effective-spec scenarios, regression command) is re-run
    at the NEW BASE TIP, in place — the checkout detached there, then switched back, so
    ignored files (dependencies, build caches) are present as usual — never at the
    rebased HEAD, which holds the change's work: a new check of the change is red
    there; it replaces the old baseline, and green-at-baseline is judged against it
  - recorded in state.json (rebases: [{at, from, to, head, rebased}]; base_sha = the base
    tip; the last verify kept with stale: true and stale_reason), a verified or reviewed
    change goes back to in-progress, and the state is committed on change/<id> as
    "apex: <id> rebased onto <base>"
JSON "catch_up": {base_branch, from, to, rebased, baseline, regression, verify_stale,
status}. The model never rebases: ape does, here, at verify and at archive.

Re-start: once the plan is frozen, verify and archive refuse while change.md differs from
it anywhere but ## Why and ## Notes (frontmatter, title, Out of scope, Delta, Tasks, …). Running start again on an in-progress, verified or reviewed change
re-baselines: recorded under "replans" in state.json, verify and review cleared, new or
changed checks baselined now (unchanged ones keep their original baseline, and so does the
regression command unless it changed), fingerprints re-taken. It applies when the plan
changed, when a spec block the change modifies or removes was changed by another change
since start, or when the regression command has no baseline for it (configured or changed
since start); otherwise it is refused.

Loop limits (H19): a re-plan past limits.replans (default 3) since a person last answered
the change blocks it instead (reason "replans exceeded: …", exit 1, code
change.replans-exceeded, action person; the refused re-plan is not recorded). A change of a
milestone whose changes were blocked limits.consecutive_blocks (default 2) times in a row is
not started (exit 1, code milestone.blocks-exceeded, action stop; see `ape change new --help`).

Exit codes: 0 started (or resumed) · 1 check red, milestone (or tweak) not approved, a vacuous
task check, wrong status, dirty tree, detached HEAD, HEAD on another change's branch, the base
behind its upstream, or git failed · 2 usage ·
3 requirement conflict with another active change, or the change does not rebase onto
its moved base.

Detached runs (D58 2.3): the command takes the run lock of its change (or milestone),
_apex/state/runs/<id|M>.json, and runs in a detached worker (`ape __run`, its own
session, output in _apex/state/runs/<id|M>.log), so the call that started it can be cut,
backgrounded or killed without stopping it. The call waits up to --wait (default 9m, at
most 9m30s; 0 returns at once); a run that ends in time is printed as always, with the same
exit code and --json shape. Otherwise the call exits 75, still running: "… still running
since <time> — run the same command again to attach" (--json: {"running": true, "verb",
"scope", "since", "log", "pid", "log_tail"}, the shape `ape change review --run` has).
Running the same command again attaches: it waits again and prints the run's result once it
ends — never a second run — or exits 75 again with the log lines the run wrote since the
last call (stderr; --json: "log_tail"). A result no call
printed yet is printed by the next call of the same verb while HEAD has not moved. While a
run holds the lock, another of these verbs on the same change or milestone is refused:
run.busy (exit 3, action retry; the message names the running verb and since when). A lock
whose process is gone (killed, the machine restarted) is removed and reported as
run.interrupted (exit 1, action retry) — never an attempt. A run stopped by SIGTERM/SIGINT
kills its checks and records nothing — no attempt, no state commit, no gate run — and
reports run.interrupted. No check outlives the worker. Never run these with
run_in_background, and never end a turn on an exit 75: run the same command again.
Beside the exit codes above: 75 still running · 3 run.busy · 1 run.interrupted · 2 a
--wait outside 0–9m30s (usage.wait).

Examples:

```
  ape change start add-tenant-delete
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |
| `--timeout` | duration | `0s` | ceiling for each command run, overriding _apex/config.yaml timeouts.<tier>.max (default: check 2h, regression 2h, gate 4h; capped at ape's hard ceilings 4h/8h/12h); the tier's idle window still applies; exit 124 when exceeded |
| `--wait` | duration | `9m0s` | how long this call waits for the detached run (at most 9m30s; 0 returns at once); exit 75 while it still runs — run the same command again to attach |

## ape change tool

Run a tools.design or tools.review entry for a change, in its own detached session (D74, D77)

```
ape change tool <id> <name> [--task T<n>] [--wait 9m] [flags]
```

Run the skill of a tools: entry in _apex/config.yaml for change <id>, in its own Claude
Code session: ape starts `ape task <skill> --args "<args filled>"` detached, as
`ape change review --run` does, and waits for it.

    tools:
      design:
        - {name: ui-mockup, skill: frontend-design:mockup, when: "…",
           args: "{change} --out {assets}", model: sonnet, effort: high}
      review:
        - {name: security, skill: security-lens, when: "…"}

  - <name> is a tools.design or tools.review entry (else tool.unknown, action replan).
    A design tool runs for the task whose line names it (`- T<n> <what> — tool: <name> —
check: …`), given with --task; a review tool, for a change whose review_tools:
    name it, before the review
  - args (D77): the entry's args: template, else "--change {id}" (plus " --task {task}"
    with --task). Placeholders: {id} the change id, {task} the task, {assets} the
    change's assets folder (development/changes/<id>/assets), {change} its change.md,
    both project-relative; a template using {task} needs --task (usage.task)
  - model and effort: the entry's model:/effort:, else models.<skill>, else the asking
    session's, else _apex/config.yaml effort:, else claude's
  - the session writes what its skill writes (a mock-up, a report under assets/): no
    read-only rule and no verdict; its exit is the result (non-zero or lost:
    tool.failed, action retry)
  - --wait (default 9m) bounds one call; exit 75 means still running: call it again —
    it attaches to the running session for that change and tool
  - JSON: {id, tool, slot, skill, task, args, running, detached_id, log, stdout,
    task_exit, launch}; stdout holds the session's `ape task --output-format json`
    envelope

Exit codes: 0 the session ended 0 · 1 tool.unknown, tool.args, tool.failed, a change not
found · 2 usage (no such task, {task} without --task) · 75 still running, call again.

Examples:

```
  ape change tool add-tenant-delete ui-mockup --task T3 --wait 9m --json
  ape change tool add-tenant-delete security --json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |
| `--task` | string | `—` | the task the tool runs for, e.g. T3 (a design tool's) |
| `--wait` | duration | `9m0s` | how long this call waits for the session (exit 75 when it is still running) |

## ape change verify

Run the task checks and the effective spec at HEAD; verified iff all exit 0

```
ape change verify <id> [flags]
```

Run, at HEAD, every task's command check and the scenario checks of the effective
spec — the spec as it will be once this change is archived: each current requirement the
change does not MODIFY or REMOVE (regression), plus the scenarios of its own ADDED and
MODIFIED blocks — of the capabilities the change touches (frontmatter touches:), plus any
other scenario whose check command is identical to a task's (H10). The rest of the spec
is the milestone gate's to run (`ape milestone gate` runs all of it). human-review checks
are not run. A command shared by several checks runs once. A check, regression or gate
command that is one ( … ) block of several lines, each a simple command (a first line of
set -e, as the lift writes, is dropped), runs as its lines joined by && — each line's exit counts, as under set -e (a line that is itself a ;, & or
|| list goes in { …; }) — and its log opens with "split into N commands". A line's
constructs count as simple when they open and close on that line (for … done, while …
done, if … fi, case … esac, { … }, [[ … ]], (( … ))). A block with a line that changes the
shell's state (cd, export, set, NAME=value, … — inside a same-line loop too; a ( … )
subshell's own cd is fine), continues (a trailing \), opens a construct it closes on
another line, defines a function, starts a here-doc, or is already joined (&&, ||, ; at
its end) runs as written, its log saying which line. JSON cmd stays the command as
configured. Each check runs under
_apex/config.yaml timeouts.check (default idle 15m, max 2h), the regression command under
timeouts.regression (idle 30m, max 2h): a command that makes no progress — no output, no
CPU time used by its process group — for idle, or still runs at max, is killed with its
whole process group and is red, exit 124, reason idle or ceiling (its last output lines
kept), never retried; --timeout sets max for both (see `ape project init --help`).
Ran nothing is not green (H11): a check that exits 0 but whose output says it ran
nothing (_apex/config.yaml checks.vacuous_output; default "no tests to run", "running 0
tests", "Ran 0 tests", "0 passing", "collected 0 items") is red with reason "vacuous"
(exit -2); a check whose requirement (requires: {NAME: env|cmd}) is missing — it mentions
$NAME and NAME is unset, or runs NAME and NAME is not on PATH — is not run and is red with
reason "unrun" (exit -3). JSON results carry "reason" and "detail" for both.
Flaky checks (H12): with "retries: <n>" in _apex/config.yaml a red check runs again up
to n times; a pass on a retry is green, recorded "flaky": true with "tries" (the failed
runs' output beside its log as <name>.try<k>.log), and listed by `ape status`. Results (sha, exit, seconds, output sha256; kind, capability, requirement, scenario
for scenario checks) are recorded in state.json; output under _apex/state/logs/<id>/.
What a check leaves in the tree outside development/ — files it created, tracked files it
changed — is undone after it and reported (JSON "stray_files"); files that were there
before it ran are never touched. A copy of each undone file, as it was when the check
ended, is kept under _apex/state/strays/ (JSON "strays_kept"): git cannot tell a check's
litter from another writer's edit made while it ran. While a check runs, ape hook pre-tool
denies an Edit/Write that its cleanup would undo (Q144). A red scenario of the lasting spec whose check names
what `ape decision standard` removed says so on its line and in results[].stale_names
(D232; see `ape decision standard --help`).
With "regression: <cmd>" in _apex/config.yaml (with tiers, its change tier), the
regression command runs too (recorded in the results with kind "regression") and is judged against the change's regression
baseline (JSON "regression": {cmd, exit, baseline_exit, pre_existing_red, failed,
new_failures}): it fails verify only when it is newly red — green at baseline, red now —
or when the change has no baseline for this command (re-start records one). Red at
baseline and still red is reported (pre_existing_red), not failed: the planner roadmaps
it — unless a test fails now that did not then. When both runs name their failing tests
(go test "--- FAIL:", gotestsum "=== FAIL:", pytest, cargo, jest, flutter and dart test
"[E]"; the baseline's are in
state.json regression_baseline.failing, recorded at start since rc.5), every test failing
now must have failed at baseline, else verify fails with the new ones in new_failures
(the first 50; new_failures_total counts them); when either run names none (a baseline
recorded before rc.5, or one naming more than 500, which stores none, included), red at
both is pre-existing, as before. new_failures also lists a red run's tests against a
green baseline.
Docs-only (Q87): a change whose diff from base_sha to HEAD touches nothing but the
user-facing docs — release.docs (default README.md and docs/), docs.changelog and
docs.user_docs — and development/ skips the regression command, and only that: its task
and scenario checks run, and the milestone gate still runs everything. JSON "regression":
{cmd, skipped: "docs-only", paths (the docs paths it touched, development/ not listed;
the first 50, paths_total counts them), baseline_exit, exit 0, failed false}, also
recorded in state.json as verify.regression; results has no regression row. It runs
anyway, JSON regression.not_skipped and a human line saying why, when a touched path is
under docs.tested (docs the project executes), when _apex/config.yaml's release or docs
block was changed by a commit no person accepted or has an uncommitted edit, or when the
regression command names a touched docs path or a folder holding one as a word
(`test -f README.md`, `markdownlint docs/`; the change's own checks run anyway). No skip when the
change has no base_sha or its diff cannot be read.
The working tree must hold no uncommitted changes outside development/ — commit first
(trailer "Change: <id>"): a verify belongs to a sha. HEAD must be on the change's branch,
change/<id> (D38; else exit 1, code change.wrong-branch).
When the base branch moved ahead of base_sha, verify first catches the change up exactly
as `ape change start --help` describes (D40): rebase onto the base (a conflict is left on
the branch: exit 3, change.conflicted), the baseline re-run at the new base tip, the old verify
marked stale, a verified/reviewed change back to in-progress, "apex: <id> rebased onto
<base>" committed; then it verifies the rebased work (JSON "catch_up"). A reviewed change
caught up here needs a new review. Refused while a conflicted catch-up is unresolved —
unmerged paths, or conflict markers in a file the rebase conflicted in (exit 1,
change.unresolved-conflict: resolve, `git add`, commit).

All checks exit 0 → status verified (a reviewed change whose work has not moved
since its review stays reviewed), state.json committed. Any red → the failing checks' last
lines are printed. Green at baseline: the change's own checks (tasks, delta scenarios) that
already passed when they were baselined, before the work, are listed (JSON
"green_at_baseline", also recorded in state.json) for review to justify or flag.
On red: every red verify is an attempt, counted in the git-ignored
_apex/state/attempts.json (JSON "attempts": since the last start or re-start, which reset
it; "attempts_total": never reset; a green verify changes neither). A red verify of an
in-progress change writes nothing to state.json and commits nothing (trial decision D5,
restored by D38): it keeps its last verify record. A verified/reviewed one goes back to
in-progress with this verify recorded, a status change committed as "apex: <id>
in-progress" on change/<id>. The red verify that brings attempts to the cap
(_apex/config.yaml limits.attempts, default 5) blocks the change instead: status blocked,
blocked_reason "attempts exceeded: <n> red verifies — <failing checks>", committed as
"apex: <id> blocked", exit 1 with code change.attempts-exceeded. Re-plan, then
`ape change start <id>` resumes it with a fresh count.
Re-planning does not reset limits.attempts_total (default 15, H19): the red verify that
brings the change's red verifies across all its starts — since a person last answered it
(`ape change answer`) — to it blocks the change (reason "attempts_total exceeded: …"),
exit 1 with code change.attempts-total-exceeded, action person: re-planning did not converge; a person answers, abandons it, or raises the
limit.
Every block of a milestone's change — here, by `ape change block`, by start's replans cap,
or by a rebase conflict — also counts toward the milestone's consecutive blocks, committed
on the base branch as "apex: <M> consecutive blocks <n> (<id>)" (see `ape change new --help`).
Refused while change.md differs from the plan frozen at start outside ## Why and ## Notes
(re-start first). human-review tasks are never run; those not yet accepted are listed
as pending.
Refused, before any check runs, when the change's commits since its base_sha edit a gate a
person approved (RK-6 R2): a recorded gate file of its milestone — a script the Gate:
command runs, or one of the gate's own files (`ape milestone approve --help`: gate_own_files);
for a tweak, of any approved milestone. Exit 1, code change.touches-gate, action replan:
only a person or apex-plan changes a gate; restore the files on the change branch, or reply
"gate defect: <evidence>". archive refuses it the same way before it rebases.

Exit codes: 0 all command checks green · 1 a check is red (the attempts cap reached
included), uncommitted work, an unresolved conflict, a gate edit, or wrong status · 2 usage · 3 the change
does not rebase onto its moved base (the conflict is left on its branch).

Detached runs (D58 2.3): the command takes the run lock of its change (or milestone),
_apex/state/runs/<id|M>.json, and runs in a detached worker (`ape __run`, its own
session, output in _apex/state/runs/<id|M>.log), so the call that started it can be cut,
backgrounded or killed without stopping it. The call waits up to --wait (default 9m, at
most 9m30s; 0 returns at once); a run that ends in time is printed as always, with the same
exit code and --json shape. Otherwise the call exits 75, still running: "… still running
since <time> — run the same command again to attach" (--json: {"running": true, "verb",
"scope", "since", "log", "pid", "log_tail"}, the shape `ape change review --run` has).
Running the same command again attaches: it waits again and prints the run's result once it
ends — never a second run — or exits 75 again with the log lines the run wrote since the
last call (stderr; --json: "log_tail"). A result no call
printed yet is printed by the next call of the same verb while HEAD has not moved. While a
run holds the lock, another of these verbs on the same change or milestone is refused:
run.busy (exit 3, action retry; the message names the running verb and since when). A lock
whose process is gone (killed, the machine restarted) is removed and reported as
run.interrupted (exit 1, action retry) — never an attempt. A run stopped by SIGTERM/SIGINT
kills its checks and records nothing — no attempt, no state commit, no gate run — and
reports run.interrupted. No check outlives the worker. Never run these with
run_in_background, and never end a turn on an exit 75: run the same command again.
Beside the exit codes above: 75 still running · 3 run.busy · 1 run.interrupted · 2 a
--wait outside 0–9m30s (usage.wait).

Examples:

```
  ape change verify add-tenant-delete
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |
| `--timeout` | duration | `0s` | ceiling for each command run, overriding _apex/config.yaml timeouts.<tier>.max (default: check 2h, regression 2h, gate 4h; capped at ape's hard ceilings 4h/8h/12h); the tier's idle window still applies; exit 124 when exceeded |
| `--wait` | duration | `9m0s` | how long this call waits for the detached run (at most 9m30s; 0 returns at once); exit 75 while it still runs — run the same command again to attach |

## ape chat

Bridged claude REPL with hooks captured to a runlog

```
ape chat [flags]
```

Spawn claude as a child of ape with the ape bridge attached.
Bridge hooks (PreToolUse, PostToolUse, UserPromptSubmit, Stop, and
friends) are captured to <project>/_apex/state/runs/chats/<id>/ in an APEX
v1 project (<project>/_output/ape/chats/<id>/ elsewhere), beside ape's
other run records.

ape chat must be run from a project root (a directory containing
_apex/config.yaml).

While attached:
  /exit, /quit       exit claude (default slash commands)
  Ctrl+D in claude   exits the REPL

ape exits when claude exits. The chat session is bound to this
terminal for its lifetime — there is no detach/reattach. To run
claude in the background, use a real terminal multiplexer
separately (e.g. wrap ape chat in tmux or screen).

A chat is a model session under the nesting rule (D62; see ape task
--help): it starts only outside every ape session (depth 0), and is refused
inside one (exit 2, session.depth or session.not-allowed).

Exit codes: 0 success · 1 claude/bridge failure · 2 usage or preflight
error (no _apex/config.yaml, bad cwd, the nesting rule).

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working directory). |
| `--effort` | string | `—` | Reasoning effort for the session and its sub-agents (low\|medium\|high\|xhigh\|max). Defaults to claude's native effort when unset. |
| `--ignore-project-settings` | bool | `false` | Tell claude to skip project + local .claude/settings*.json. |
| `--model` | string | `—` | Initial claude model. A bare family (fable, haiku, mythos, opus, sonnet) resolves to its current generation; sonnet-5 / claude-sonnet-5 / opus[1m] pin explicitly. Empty falls back to claude's default. |
| `--output-style` | string | `—` | Output style pinned on the spawned session (default "Default"). Pass "inherit" to keep whatever style the machine is configured with. |

## ape config

Resolve the project's APEX configuration

```
ape config
```

Subcommands:

- `effort` — Show the per-model effort defaults a spawn in this project gets
- `pin` — Write a framework-resolved fallback into _apex/config.yaml
- `resolve` — Resolve _apex/config.yaml + the config.local.yaml overlay

## ape config effort

Show the per-model effort defaults a spawn in this project gets

```
ape config effort [flags]
```

Read _apex/effort-defaults.yaml and report what ape gives a spawned claude
session when no --effort value is given, outside an APEX v1 project (for
one, see "In an APEX v1 project" below).

With the file, every spawn carries the table in its --settings: each model,
sub-agents included, runs at its family's row, and any other model at the
fallback. An explicit effort still wins, process-wide. Without the file,
every spawn gets xhigh process-wide, which is what ape did before
the table existed.

settings_keys are the exact modelSettings keys written: the family word,
which follows the family's current alias, plus every id of that family
this ape knows. A model id newer than this binary that is not its family's
alias target gets the fallback.

--model resolves one model the way a spawn would.

In an APEX v1 project the table does not apply: ape task passes
claude --effort from --effort, models.<skill>.effort, the parent
session's effort or _apex/config.yaml effort:, else nothing (see ape task
--help). The report's v1 section shows the config's part of that.

Exit codes:
  0  resolved (with or without a table)
  2  the table exists and is invalid, or --model is empty after trimming
  4  no _apex/config.yaml in --cwd or any parent

Examples:

```
  ape config effort --output-format json
  ape config effort --model sonnet
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--model` | string | `—` | Resolve this model's effort as a spawn would (e.g. sonnet, claude-opus-5-5) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape config pin

Write a framework-resolved fallback into _apex/config.yaml

```
ape config pin <key> [flags]
```

Resolve a key the framework has been falling back for, and append it to
the project's own config so every reader sees the same value.

Only evidence_folder today. The value written is the path the project's
skills were already resolving — an existing {governance_folder}/evidence
where the project keeps its evidence there, else the literal evidence —
so no install silently moves. It is a no-op when the base config or the
config.local.yaml overlay already sets the key.

--check writes nothing and answers in its exit code: 0 when the key is
set, 1 when it is not. Those two, and only those two, are answers about
the KEY. A missing config still exits 4 and a malformed one still exits
2, because a migration runner reads anything but 0 or 1 as "the check
failed" — and a config ape cannot read is not a config it should be
repairing.

Examples:

```
  ape config pin evidence_folder
  ape config pin evidence_folder --check
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--check` | bool | `false` | Answer in the exit code and write nothing: 0 set, 1 unset (still 4 with no config, 2 if malformed) |
| `--cwd` | string | `—` | Project root (default: current working dir) |

## ape config resolve

Resolve _apex/config.yaml + the config.local.yaml overlay

```
ape config resolve [flags]
```

Walk up from --cwd for _apex/config.yaml, overlay _apex/config.local.yaml
key-wise, and emit the seventeen folder/name variables every framework
skill resolves on activation — plus the four derived ext_* flags, the
absolute paths those folders denote, and the local date/timestamp.

An absent config.local.yaml is a normal outcome (local_overlay_applied:
false). A config.local.yaml that exists but does not parse is a hard
failure, not a fall-back to base values: this resolution is the first act
of every skill, so one typo'd override would otherwise run a whole
session against folders nobody chose.

governance_repository_path names the CANONICAL governance repository that
apex-adr-reconciliation / apex-pattern-reconciliation import from. It is
not an alternative home for this project's own records — those always
live under governance_folder, which is what paths.adrs and paths.patterns
report.

Exit codes:
  0  resolved
  2  a config file exists but is malformed (the message names it)
  4  no _apex/config.yaml in --cwd or any parent

Examples:

```
  ape config resolve --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root to resolve from (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape costs

Show this project's Claude cost rollup

```
ape costs [flags]
```

Reads the project's cost-rollup.json (_apex/state/runs/ in an APEX v1
project, _output/ape/ elsewhere) and prints totals — today, this week,
all-time — broken down per task, prompt, chat and (from an older ape) pipeline. In an APEX v1 project the loop's task runs are one
row, "task:apex-loop (+apex-orchestrator)" when an ape before rc.7 recorded
some of them under the loop's older name (Q95); JSON keeps both buckets as
recorded.

  ape costs                          Project rollup (human / json).
  ape costs run <run-id>             Single task run (reads manifest.yaml):
                                     its own totals, its parent_run and depth
                                     (D62), and for a loop run the
                                     children it started and tree_cost_usd
                                     (its own cost plus theirs). Totals are
                                     always the run's own session, here and in
                                     the manifest, so a sum over every run
                                     counts each child once.
  ape costs chat <chat-id>           Single chat session (reads session.yaml).
  ape costs prompt <prompt-id>       Single prompt session (reads prompt.yaml).
  ape costs update --from <file>     Refresh the price table from a YAML file.
  ape costs coverage                 Check the price table against the models
                                     Claude Code is actually emitting locally.
  ape costs reprice                  Recompute stored costs from on-disk tokens
                                     using the current price table.
  ape costs roll                     Force a project rollup rebuild from all
                                     run / chat directories.

Subcommands:

- `chat` — Show cost for a single chat session (reads its session.yaml)
- `coverage` — Check the built-in price table against the models Claude Code is actually emitting
- `prompt` — Show cost for a single prompt session (reads its prompt.yaml)
- `reprice` — Recompute stored costs from on-disk token counts using the current price table
- `roll` — Rebuild the project's cost-rollup.json from on-disk run / chat artefacts
- `run` — Show cost for a single task run, or a pipeline run an older ape recorded (reads its manifest.yaml)
- `update` — Persist model price overrides from a YAML file to ~/.ape/prices.yaml

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | human \| json |

## ape costs chat

Show cost for a single chat session (reads its session.yaml)

```
ape costs chat <chat-id> [flags]
```

Examples:

```
  ape costs chat 0a675bc4
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | human \| json |

## ape costs coverage

Check the built-in price table against the models Claude Code is actually emitting

```
ape costs coverage [flags]
```

Sweep the local Claude Code transcripts (~/.claude/projects) and report
how this ape binary prices every model id it finds.

Each observed model resolves to one of:

  exact      a rate for this exact model id (built-in table, an override
             in ~/.ape/prices.yaml, or a dated promotional window)
  family     no exact row — priced from the model's family tier. Close,
             but an approximation, and flagged as one everywhere.
  unpriced   nothing matched. Those turns contribute $0.00 to every
             total, which is not the same as being free.

--strict exits 2 when any observed model is not exactly priced. A sweep
that finds no transcripts exits 0 and says so: absence of evidence is not
coverage, so CI (which has no transcripts) skips rather than passes.

Exit codes:
  0  every observed model exactly priced, or nothing observed
  2  --strict and at least one model is estimated or unpriced

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--days` | int | `30` | Only read transcripts modified in the last N days (0 = all) |
| `--output-format` | string | `human` | human \| json \| yaml |
| `--strict` | bool | `false` | Exit 2 when any observed model is not exactly priced |

## ape costs prompt

Show cost for a single prompt session (reads its prompt.yaml)

```
ape costs prompt <prompt-id> [flags]
```

Examples:

```
  ape costs prompt 20260713-120102-a1b2c3d
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | human \| json |

## ape costs reprice

Recompute stored costs from on-disk token counts using the current price table

```
ape costs reprice [flags]
```

Walk this project's run artefacts and recompute every cost_usd from the
per-model token counts stored alongside it.

Use this after correcting the price table (a new model id added to
internal/cost/prices.yaml, or an override persisted via
`ape costs update --from`) to fix runs that were recorded while the
table was stale.

Artefacts covered, under ape's run root (_apex/state/runs/ in an APEX v1
project, _output/ape/ elsewhere):
  tasks/<skill>/<run-id>/manifest.yaml (and pipelines/…, from an older ape)
  prompts/<prompt-id>/prompt.yaml

Chat session.yaml has no per-model breakdown, so there is nothing to
reprice from — those are skipped.

A model priced by prompt length (Claude Haiku 5.5) cannot be repriced
exactly: the stored totals do not say how long each turn's prompt was. A
stored cost those totals could produce is kept; any other is replaced by
the least they can cost, and the model is reported as a lower bound.

Dry run by default: it prints what would change and touches nothing. Pass
--write to apply, then run `ape costs roll` to refresh the rollup cache.
Only cost_usd scalars are rewritten; key order, comments, and every other
field survive the round-trip.

A model that is STILL unpriced cannot be fixed by repricing — its stored
cost is left alone and the model is listed in the report so you know the
total remains a lower bound.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | human \| json \| yaml |
| `--write` | bool | `false` | Apply the recomputed costs (default: dry run) |

## ape costs roll

Rebuild the project's cost-rollup.json from on-disk run / chat artefacts

```
ape costs roll
```

## ape costs run

Show cost for a single task run, or a pipeline run an older ape recorded (reads its manifest.yaml)

```
ape costs run <run-id> [flags]
```

Examples:

```
  ape costs run 20260703-120102-a1b2c3
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | human \| json |

## ape costs update

Persist model price overrides from a YAML file to ~/.ape/prices.yaml

```
ape costs update [flags]
```

Reads a YAML file in the shape:

  prices:
    claude-opus-4-7:
      base_input: 5.00
      output: 25.00
    claude-sonnet-4-6:
      base_input: 3.00
      output: 15.00

and persists it to ~/.ape/prices.yaml. Subsequent ape invocations
prefer these values over the built-in price table (cost.Prices).
PLAN-5 / C7.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--from` | string | `—` | Path to a YAML file with model price overrides |

## ape decision

Record architecture decisions (ADR-NNNN) in development/decisions/

```
ape decision
```

ADRs live in development/decisions/NNNN-<slug>.md. A decision is proposed, then
accepted (`ape decision accept`); an accepted decision's text is fixed — `ape project check`
refuses an edit to it — and a different decision is a new ADR that supersedes it
(`ape decision new <slug> --supersedes ADR-NNNN`).

ADR-0001 is APEX's standard ADR, "Record architecture decisions" (D205): ape project
init and the lift write it, `ape decision standard` keeps it at the installed standard,
and a project's own decisions number from 0002.

Subcommands:

- `accept` — Accept a proposed ADR and fix its text
- `new` — Allocate the next ADR number and write the skeleton
- `standard` — Bring ADR-0001 to APEX's installed standard ADR (framework migrations run it)

## ape decision accept

Accept a proposed ADR and fix its text

```
ape decision accept <ADR-NNNN> --by <who> [flags]
```

Set a proposed ADR's Status to accepted and record the text it was accepted with in
_apex/state/decisions.json; both are committed as "apex: ADR-NNNN accepted". From
then on `ape project check` fails if the ADR's text (everything but its Status line)
changes: supersede it instead.

An ADR whose Status says accepted with no record in decisions.json — accepted by hand, or
before ape recorded acceptances — is reported by `ape project check`
(decision.accepted-unrecorded) and held to the text git first committed it accepted with.
Accept records it as it is ("apex: ADR-NNNN accepted (recorded)"), unless its text changed
since that commit (decision.accepted-edited: restore it first). The lift migration's
records (by "lift") count as records.

Only a person accepts (OI-4): refused in a model session (APE_SESSION or CLAUDECODE set,
a `!` line inside Claude Code included) with exit 2, session.person-only — unless, in the
person's own Claude Code session, the person typed "accept <ADR-NNNN>" in that turn (E2:
`ape hook pre-tool --help`; --by is then git config user.name, recorded with the prompt;
another --by is session.by-mismatch, exit 1).

A person's acceptance is recorded on the base branch (D11, D38): refused on a change's
branch, change/<id> (code branch.change-branch; `git switch <base>` first).

Exit codes: 0 accepted · 1 no such ADR, not proposed, malformed, HEAD on a change
branch, or session.by-mismatch · 2 usage, or a model session with no grant.

Examples:

```
  ape decision accept ADR-0003 --by diego
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--by` | string | `—` | who accepts (required) |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |

## ape decision new

Allocate the next ADR number and write the skeleton

```
ape decision new <slug> [--supersedes ADR-NNNN] [flags]
```

Write development/decisions/NNNN-<slug>.md with the next free number:

  # ADR-NNNN: <title>
  Status: proposed
  Date: YYYY-MM-DD
  Supersedes: ADR-MMMM        (only with --supersedes)
  ## Context / ## Decision / ## Consequences
  Enforced by: review

--supersedes ADR-MMMM (proposed or accepted) also sets that ADR's Status to
"superseded by ADR-NNNN". Committed as "apex: ADR-NNNN proposed[, supersedes ADR-MMMM]".
Fill it in (your own commit); set Enforced by: to a command, hook or review. Reference it
from a change's frontmatter (decisions: [ADR-NNNN]) or a requirement (Decisions: ADR-NNNN).

Runs on any branch: an ADR written while a change is worked, on its branch change/<id>,
lands on the base branch with the change's squash commit at archive (D38) — and can be
accepted only once it is there.

Exit codes: 0 ok (prints id and path) · 1 slug already used, or the superseded ADR is
missing or already superseded · 2 usage.

Examples:

```
  ape decision new use-sqlite
  ape decision new use-postgres --supersedes ADR-0002 --title "Store tenants in Postgres"
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |
| `--supersedes` | string | `—` | ADR-NNNN this decision replaces |
| `--title` | string | `—` | ADR title (default: from the slug) |

## ape decision standard

Bring ADR-0001 to APEX's installed standard ADR (framework migrations run it)

```
ape decision standard [--check] [flags]
```

ADR-0001 of every APEX project is APEX's standard ADR, "Record architecture
decisions" (D205): the decision file, its sections, the proposed → accepted lifecycle,
and change only by superseding. It is framework-owned, like the payload (D206): its
text is one shipped version of the standard — the installed payload's
_apex/standards/adr-0001.md, else ape's own copy of it — recorded in
_apex/state/decisions.json as accepted by "APEX standard (<the ape command>)", with
standard: {framework_version, sha256}, the version it came from and the digest of its
bytes. ape project init and the lift write it. This command brings a project there
that was made before them, and follows a framework release that changes the standard:
the framework's migration v1.0.0_seq-08 runs it, as does the entry of each release
that changes the standard (command: ape decision standard; check: ape decision
standard --check).

In order:
  differs    ADR-0001 is the project's own decision (neither the standard nor
             framework-owned — the apex-v0 governance standard an earlier lift
             converted, its Lifted from: line naming a use-markdown-adrs record, or a
             standard ape wrote), or there are two ADR-0001 files: nothing is done; ape
             never replaces a project's decision (ape project check:
             decision.standard-differs)
  opted-out  an ACCEPTED ADR has Supersedes: ADR-0001: the project opted out of the
             standard; nothing is done, the command says so, and exits 0 (ape project
             check: decision.standard-superseded). A proposed supersede does not opt out
  created    there is no ADR-0001: the standard is written as
             development/decisions/0001-record-architecture-decisions.md
  kept       ADR-0001 is the installed standard already (compared without its
             Status: line): nothing is done
  replaced   ADR-0001 is framework-owned and not the installed standard: its file is
             replaced by the standard (an old file name, 0001-use-markdown-adrs.md, is
             removed) and the decisions.json record re-written. When it was the apex-v0
             governance standard, each PROPOSED ADR that supersedes it is removed too —
             an unaccepted supersede of what is replaced has nothing left to supersede
             (pdfa's ADR-0031) — named, with the lines that still cite it, listed and
             not rewritten. A proposed supersede of a standard ape wrote is a person's
             pending opt-out and is left. No other ADR is renumbered
A write is its own commit, "apex: ADR-0001 standard <framework version>" (or "… ape's
own copy" when no payload is installed). Its decisions.json record lists what the verb
removed, standard.removed: [{path, id}] ([] when nothing; kept across later writes;
D232 Q130).

Stale scenarios (D232 Q131): a scenario of a lasting spec
(development/specs/<capability>/spec.md) whose check: names what it removed — its path,
file name or ADR id, or "Supersedes: ADR-0001" once it removed a supersede — can never
pass again. The verb lists each, after a write and on any later run ("stale scenario
<spec>:<line>: …"; under --check, what the write would leave stale), and ape project
check shows the notice spec.check-names-removed (action replan) while one exists; a
check that asserts the name's absence (! test -e <path>, [ ! -f <path> ]) is not stale.
The spec is never edited here (Q132): a change retires the scenario with a REMOVED
delta citing the migration v1.0.0_seq-08 — never by recreating the records or by
superseding ADR-0001 again, which opts the project out. A project whose seq-08 ran
before the record (rc.7b) has none; for it, a check naming a decisions/ file that does
not exist, or an ADR-0001 file name that is not the standard's, is read as stale
(Q133). decision.accepted-edited accepts exactly that
change of an accepted ADR-0001 — its bytes equal the digest ape recorded for a shipped
standard — and still refuses a hand edit. When a release changes the standard and no
entry runs this, ape project check lists the notice decision.standard-stale.

Refused on a change branch (branch.change-branch), outside a git repository
(git.not-a-repo), and while a file it writes (ADR-0001's, decisions.json, an ADR it
removes) has uncommitted changes (tree.dirty): nothing is written. A command any
session may run (not person-only): it applies the framework's record, and decides
nothing a person decides.

--check writes nothing: exit 0 when nothing is due (kept, opted-out or differs), 1
(decision.standard-pending, action person, naming what is due) when a write is — the
migration's check: 0 applied, 1 not applied.

JSON: {"action": "created"|"replaced"|"kept"|"opted-out"|"differs", "path", "source":
"_apex/standards/adr-0001.md"|"ape", "framework_version", "replaces" (the file it
replaced), "opted_out_by": [ADR-NNNN, …], "removed_adrs": [{id, path, citations:
["<file>:<line>", …]}], "commit", "stale_scenarios": [{spec, requirement, scenario,
line, names}]}. Under --check it says what is due, under "result"
beside the failure's ok/exit/diagnostics (exit 1).

Exit codes: 0 written, or nothing due (--check: nothing due) · 1 refused, or (--check)
a write due.

Examples:

```
  ape decision standard --check
  ape decision standard
  ape decision standard --json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--check` | bool | `false` | write nothing: exit 0 when nothing is due, 1 when a write of the standard is |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |

## ape doctor

Probe the local environment for prerequisites

```
ape doctor [flags]
```

Probe the local environment for prerequisites and report a per-check
verdict.

Doctor runs a fixed set of checks against the host (claude / git /
node / npx binaries, Playwright host compatibility, ~/.claude
writability) and the project at --cwd. Which project rows run depends on
the project (H27):

  APEX v1 (_apex/state/ or development/project.md): the host rows, the
  framework rows that apply to any install (skills, output styles,
  command surface, the ape floor framework.ape_version, tag_moved,
  upgrade migrations), and the v1 rows:
    project.check     ape project check (its hooks and plugins have rows
                      of their own); WARN with the first findings
    hooks.project     ape's hooks missing from or altered in
                      .claude/settings.json (ape project init restores them)
    plugins.project   see below
    claude.version    the installed Claude Code's version, reported only
                      (a tested range comes later)
    state.gitignore   _apex/state/'s whitelist: milestones.json and
                      decisions.json tracked, ape's run records, logs,
                      hook verdicts and counters ignored and untracked
    settings.shared   .claude/settings.json and .claude/skills/ not ignored
                      by git: an ignored one shares ape's hooks, or the
                      framework's skills, with no other checkout (only
                      ape task sessions get the hooks inline)
    hooks.pre-commit  git's pre-commit hook does not run ape's secret scan
                      (project check's notice hooks.pre-commit-absent): WARN
                      when absent — a fresh clone; ape project init installs
                      it —, another tool's hook without the line, or not
                      executable
    hooks.prompt      the UserPromptSubmit hook ape hook user-prompt missing
                      from or altered in .claude/settings.json (project
                      check's notice hooks.prompt-absent): WARN — without it a
                      person act from a Claude Code session is refused (E2)
  None of v0's rows run there: --strict is green on a healthy v1 project.

  Anywhere else: the rows below, v0's included, plus apex.v0, which WARNs
  in an APEX v0 project: this ape retired the v0 commands its skills
  call, so use the project's pinned v0 ape (.bingo), or lift it
  (ape framework update to v1 runs ape project lift).

  --only runs exactly the checks it names, whatever the project.

The v0 rows: framework metadata, installed pipelines, the always-on
operating-rules fragment + CLAUDE.md managed block (hard-failing only when
a framework install that manages them has lost the fragment, import, or
apex-orchestrator skill), and seven that report on v0 PROJECT DATA:
whether the config resolves at all, registry drift, story frontmatter,
tracker divergence, whether ape's own run subtree is gitignored,
team-memory size, and any pending project-data migration. Project-scoped
checks degrade to INFO outside a project root.

The "is it gitignored" rows report and never write. .gitignore is the
operator's file; a tool that edits it uninvited is worse than one that
points. Both also distinguish COMMITTED from merely unignored, because an
ignore line does not untrack anything — on a project that already
committed the path, adding the line changes nothing at all.

memory.size is one of only two Required checks among the v0 rows, deliberately:
a non-required FAIL is downgraded to WARN, so nothing else could surface a
team-memory.md past its hard ceiling: far past what one Read shows, and
nearing the 256 KiB byte cap at which a Read fails outright.

Two checks report on the step-completion gates rather than on
prerequisites, because both protect against a failure that is otherwise
silent:

  hooks.contract_drift          whether Claude Code still sends the hook
                                fields the gates read. If it stops, the
                                gates stop firing without erroring.
  framework.terminal_contracts  whether _apex/terminal-contracts.csv is
                                installed, and which skills it enrols. No
                                table means no run is checked for a
                                terminal contract.
  framework.change_routes       whether _apex/change-routes.yaml is
                                installed and every route in it can print
                                its commands. Absent = ape change names
                                an escalation's route and prints nothing
                                for it, which is also what an older ape
                                does — one output, three causes.
  framework.output_styles       which skills _apex/output-styles.csv
                                enrols, and the style each resolves to.
                                A missing table, an unenrolled skill and
                                a style name Claude Code cannot resolve
                                all look identical from inside a run.
  runs.legacy_layout            whether run artifacts are still at the
                                pre-{output_folder}/ape paths. Until they
                                move, cost rollups and the hook check read
                                a project with no history.
  framework.ape_version         whether this ape meets the installed
                                framework's min_ape_version
                                (_apex/ape-commands.yaml, framework
                                v0.27.0+). FAIL below it; WARN for a dev
                                build or pseudo-version, which cannot be
                                compared; INFO when none is declared. An
                                rc of exactly the minimum meets it.
  framework.tag_moved           whether the installed release tag now
                                names a different commit in the
                                framework clone ($APEX_FRAMEWORK_REPO,
                                else ape's own) than the one installed.
                                Tags are meant never to move.
  framework.command_surface     whether this binary provides every ape
                                command the installed framework declares
                                it requires (_apex/ape-commands.yaml). The
                                skills have no fallback branch, so a
                                missing one fails a skill mid-stage rather
                                than degrading.

                                It compares command NAMES ONLY. A binary
                                can provide "ape change start" with an
                                older flag set and still pass, so this is
                                a version-skew guard and not a statement
                                of version compatibility. A framework that
                                ships no manifest predates the contract,
                                and a v1 manifest declares only
                                min_ape_version: both report a skip.
  aboard.skill_reference        whether a .claude/skills/aboard reference
                                copied into this project still describes
                                the board this binary serves, by capsHash.
                                The renderers are compiled in and the skill
                                is a copy, so the two drift independently;
                                an agent reading a stale one writes state no
                                renderer reads and the write still reports
                                success. Deliberately NOT a check that ape
                                provides "ape aboard" — the tree is compiled
                                in, so that could only assert a tautology.
                                A project that never copied the skill skips.

Both SKIP or report INFO when there is nothing to judge — absence of
evidence is not coverage.

  plugins.project               (APEX v1) whether the plugins
                                _apex/config.yaml lists are installed at
                                project scope at their refs, and
                                .claude/settings.json declares and enables
                                them. --fix puts them right: it installs or
                                updates each through the claude plugin CLI,
                                writes the settings and commits them as
                                "apex: plugins", before the checks run.
                                --fix fixes nothing else; other rows print
                                their fix command.

--only runs just the named checks, so one gate can be scripted on its own:

  ape doctor --only hooks.contract_drift --strict --cwd <project>

Note the --cwd: hook drift is observed from the runlogs ape itself wrote
(<project>/_apex/state/runs in a v1 project, else <project>/_output/ape), so
it can only be judged against a project ape has actually run sessions in,
not against the ape repo. This is the
observational read — point it at a real project. The release gate takes the
other route: "make check-hooks" seeds its own runlog with one unattended
session, so it always has a corpus to judge. An unknown name in --only is
an error rather than a silent no-op — a typo that ran zero checks would
exit 0 and read as a pass.

Exit codes:
  0  every required check passed (warnings allowed unless --strict)
  1  at least one required check failed, any warning under --strict, or
     --only named a check that does not exist

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root to probe (default: current working directory) |
| `--fix` | bool | `false` | APEX v1: first install/update the plugins _apex/config.yaml lists and write their settings (the only fixer) |
| `--only` | string | `—` | Run ONLY these checks (comma-separated). Errors on an unknown name; applied before --skip |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--skip` | string | `—` | Comma-separated list of check names to skip (e.g. node.binary,npx.binary) |
| `--strict` | bool | `false` | Treat WARN-level findings as failures (exit 1) |

## ape event

Publish a session progress event over NATS

```
ape event <event> [--payload <json>|@file|-] [flags]
```

Publish a caller-named progress event for the current Claude session on
ape.evt.<user>.<project>.session.<session-id>.<event>.

The <event> token is caller-chosen (validated [a-z0-9-]+). --payload is
arbitrary JSON, given inline, as @file, or "-" for stdin; it rides the
versioned envelope under "payload" alongside the decoded user identity and
the resolved session id.

The session is resolved as: --session-id → --transcript → APE_SESSION_ID →
the newest transcript for the current project.

Exit codes: 0 published · 1 publish failed (connected) · 2 usage error,
no NATS configured, or session unresolvable.

Examples:

```
  ape event status --payload '{"phase":"implement","pct":60}'
  ape event build-green
  echo '{"pr":42}' | ape event pr-opened --payload -
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root for session auto-resolution (default: current working dir). |
| `--events-subject-prefix` | string | `ape.evt` | Subject root for the published event. |
| `--nats-creds` | string | `—` | NATS .creds file; its decoded user identity is baked into every subject (env APE_NATS_CREDS). |
| `--nats-url` | string | `—` | NATS server URL (env APE_NATS_URL). Required — no URL is a usage error (exit 2). |
| `--output-format` | string | `human` | Output format: human\|json (result object on stdout, diagnostics on stderr). |
| `--payload` | string | `—` | Event payload as JSON: inline, @file, or "-" for stdin. |
| `--quiet` | bool | `false` | Suppress the human-mode confirmation line. |
| `--session-id` | string | `—` | Claude session id to report for (default: auto-resolve the current project's newest). |
| `--transcript` | string | `—` | Explicit transcript file; the session id is parsed from its name. |

## ape framework

Install and inspect APEX framework assets in a project

```
ape framework
```

Manage the apex_process_framework assets installed at the project root.

  ape framework setup      One-time install: skills (+ pipelines and a
                           bootstrapped _apex/config.yaml for APEX v0; an
                           APEX v1 project runs 'ape project init' next).
                           Refuses if already installed (pass --force to
                           re-bootstrap).
  ape framework update     Install the framework's newest release (or
                           --version) and commit it. Refuses if not yet
                           set up (run setup first).
  ape framework status     Inspect the installed framework version, with
                           optional drift report against the framework repo.

The framework repo is --repo, else $APEX_FRAMEWORK_REPO, else ape's own
clone under the user cache directory (cloned from $APEX_FRAMEWORK_URL, else
the framework's GitHub repo). Since ape v0.4.0 setup and update install a
RELEASE — the highest vX.Y.Z tag, or the one --version names (the only way
to a vX.Y.Z-rc.N candidate) — exported from that clone; since v0.7.0 they
also check that release out in the clone, and keep the project's governance
repo at its newest release the same way. --from-worktree installs the
working tree instead. See docs/explanation/framework-updates-from-releases.md
and docs/explanation/framework-and-governance-clones.md.
The project root is resolved from --cwd or the current working directory.

Subcommands:

- `setup` — Initial install of a framework release into the project, committed
- `status` — Inspect the installed framework version + drift report
- `update` — Install the framework's newest release, run pending migrations, and commit the result

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root directory (default: current working dir) |
| `--repo` | string | `—` | Path to a checked-out apex_process_framework repo (default: $APEX_FRAMEWORK_REPO) |

## ape framework setup

Initial install of a framework release into the project, committed

```
ape framework setup [flags]
```

Initial install of framework-managed assets into <project>, from the
framework's newest release tag (--version picks one; --from-worktree
installs the repo's working tree instead):

  - .claude/skills/apex-*  copied from <repo>/.claude/skills
  - _apex/pipelines/*.yaml copied from <repo>/_apex/pipelines, when the
                           release ships any (APEX v0)
  - _apex/config.yaml      seeded, APEX v0 only (interactive prompt by
                           default; supply --project-name and --extensions
                           to skip the TUI; --no-bootstrap to skip seeding
                           entirely)
  - _apex/framework.yaml   metadata recording what was installed.

A release is a tag whose tree holds .claude/skills/ and
_apex/ape-commands.yaml at its root (an APEX v0 release may hold
_apex/pipelines/ instead of the manifest). It is APEX v1 when that
manifest's min_ape_version is 1.0.0 or later. An APEX v1 release:
  - seeds no _apex/config.yaml: 'ape project init' writes it, so neither
    --project-name nor --extensions is needed, headless or not (passed,
    they are ignored, and setup says so)
  - removes the project's APEX v0 migration entries (_apex/migrations/v0.*)
    in the install commit; the ledger in _apex/framework.yaml keeps them
  - skips v0's surfaces: the operating-rules, commit-owners, output-styles
    and effort-table notices, and the sprint-status.yaml.lock .gitignore
    line. .aboard/ is still created
  - ends with 'next: ape project init' when the project has no v1 layout
    yet (and the JSON carries "next"). Either order works; setup first
    keeps the install commit before the project's v1 epoch
  - puts the .claude/ shape in the root .gitignore, in the install commit
    (D203; created when absent): .claude/*, !.claude/settings.json,
    !.claude/skills/ and CLAUDE.local.md, a whole-folder .claude ignore
    rewritten in place — so the skills it installs are committed even where
    .claude/ was ignored. A file of yours the rewrite un-ignores (a skill of
    your own) is listed and left out of the commit. See ape project init
    --help; the output's "Ignore:" line and JSON "gitignore" say what it did

The install is committed as 'chore(framework): install APEX framework
vX.Y.Z' with Framework-Version / Framework-Commit trailers and
'Generator: ape framework setup'. --no-commit leaves it in the working
tree instead.

Refuses to run when:
  - _apex/framework.yaml already exists (pass --force to re-bootstrap;
    this resets project_name and extensions)
  - the release is not a release layout: no .claude/skills/, or neither
    _apex/ape-commands.yaml nor _apex/pipelines/ (exit 3; the message
    names what is missing)
  - committing, and the project is not a git repo, is on a detached HEAD,
    or has modified or staged tracked files (exit 4)
  - this ape is older than the release's min_ape_version (exit 11; on a
    terminal it offers to update ape first and re-runs itself)
  - --from-worktree, and the framework repo is dirty, on a non-main
    branch, or its .claude/skills/apex-* subtree has uncommitted changes
    (pass --force to bypass)
  - your framework or governance clone is behind the release and has
    local changes (exit 3; pass --force-clone to overwrite them). A clone
    of yours only ever moves forward: one whose branch is ahead of the
    release, or diverged from it, is left where it is and reported. ape's
    own clones under the user cache are always overwritten

Headless contexts (APEX v0 releases): when stdout is not a TTY (or
--output-format is not human) and the project lacks _apex/config.yaml,
you must supply --project-name and --extensions, OR pass --no-bootstrap.
Otherwise 'setup' refuses to seed silently (exit 5).

For subsequent refreshes against a framework version bump, use
'ape framework update'.

Exit codes: 2 usage; 3 the framework source (no release, missing tag, not a
release layout); 4 the project tree (not a git repository, a detached HEAD,
modified or staged tracked files); 5 an APEX v0 release with no terminal and
no --project-name/--extensions or --no-bootstrap; 6 already installed
(_apex/framework.yaml exists); 11 ape below min_ape_version; 12 a commit
failed (the changes are left staged).

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--extensions` | string | `—` | Bootstrap value for extensions, comma-separated (e.g. ext-adrs,ext-features). Empty string = none. |
| `--force` | bool | `false` | Bypass safety checks (already installed, dirty framework, non-main branch, modified project skills) |
| `--force-clone` | bool | `false` | Overwrite local changes in YOUR framework or governance clone when moving it to the release (ape's own clones are always overwritten; a clone ahead of the release is never moved) |
| `--from-worktree` | bool | `false` | Install the framework repo's working tree instead of a release tag (the pre-v0.4.0 behaviour: main-only, clean, fast-forward) |
| `--no-bootstrap` | bool | `false` | Skip _apex/config.yaml seeding entirely |
| `--no-commit` | bool | `false` | Leave the result in the working tree instead of committing it |
| `--no-fetch` | bool | `false` | Skip fetching the framework and governance repos; use the releases they already hold |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--project-name` | string | `—` | Bootstrap value for project_name (skips the TUI prompt) |
| `--version` | string | `—` | Install exactly this release tag, vX.Y.Z or vX.Y.Z-rc.N (default: the highest final vX.Y.Z; a candidate is only ever installed by name) |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root directory (default: current working dir) |
| `--repo` | string | `—` | Path to a checked-out apex_process_framework repo (default: $APEX_FRAMEWORK_REPO) |

## ape framework status

Inspect the installed framework version + drift report

```
ape framework status [flags]
```

Read <project>/_apex/framework.yaml and report what was installed.

When there is a framework clone — --repo, $APEX_FRAMEWORK_REPO, or ape's
own once cloned — also compares the install with what that repo offers now (after a best-effort tag fetch, unless
--no-fetch): the NEWEST RELEASE tag, which is what 'update' would install,
and whether the installed tag still names the installed commit (a moved
tag is reported, never followed). --from-worktree compares against the
repo's working-tree HEAD instead, as before ape v0.4.0.

Then it says where the framework and governance clones are, and where
each path came from (flag, env, config, or ape's cache), so you know where
to read their docs.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--from-worktree` | bool | `false` | Compare against the repo's working-tree HEAD instead of its newest release |
| `--no-fetch` | bool | `false` | Skip the best-effort 'git fetch' against the framework repo |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root directory (default: current working dir) |
| `--repo` | string | `—` | Path to a checked-out apex_process_framework repo (default: $APEX_FRAMEWORK_REPO) |

## ape framework update

Install the framework's newest release, run pending migrations, and commit the result

```
ape framework update [flags]
```

Install the framework's newest release tag (or the one --version names;
--from-worktree installs the repo's working tree instead) into <project>:

  - .claude/skills/apex-*  re-copied from <repo>/.claude/skills
  - _apex/pipelines/*.yaml re-copied from <repo>/_apex/pipelines, when the
                           release ships any (APEX v0)
  - _apex/framework.yaml   metadata refreshed (preserves project_name +
                           extensions recorded by 'ape framework setup')

A release holds .claude/skills/ and _apex/ape-commands.yaml at its root
(an APEX v0 release may hold _apex/pipelines/ instead of the manifest);
see 'ape framework setup --help' for what makes it APEX v1. Installing an
APEX v1 release removes the project's APEX v0 migration entries
(_apex/migrations/v0.*) in the update commit, and the runner never plans
or runs an entry below 1.0.0, whatever the ledger says; the ledger keeps
its history. v0's notices and its .gitignore lock line are skipped. An
APEX v1 install puts the .claude/ shape in the root .gitignore before the
migrations run, in the update commit, as setup does (see 'ape framework
setup --help'): the skills are committed where .claude/ was ignored, and
the v1.0.0 lift finds the shape in place.

Then any pending PROJECT-DATA migration (PLAN-25 D10), and then the
framework's own per-version UPGRADE list from _apex/migrations/*.md.
Migrations run here rather than as a separate command a skill has to
police, so no skill ever meets an un-migrated project and no skill needs a
migration failure path. This is the right transaction boundary: explicitly
invoked, at the moment framework expectations change, outside the build
loop.

The upgrade list is ordered semantically — semver on 'version', integer on
'seq', honouring 'after:' — and never by filename, under which v0.9.0
sorts after v0.10.0. Applied ids are recorded in _apex/framework.yaml as
an ordered list, which is what makes a second run a no-op and a failed run
resumable. Only 'kind: derivable' entries are executed; 'kind: judged' is
listed with the skill to dispatch and is NEVER run, under any flag. An
entry's 'check:' reports and never gates: one that cannot run leaves the
entry unapplied-and-unverifiable, which is reported and blocks nothing.

The install and the migrations it ran are committed together as
'chore(framework): update APEX framework to vX.Y.Z', with Framework-Version,
Framework-Commit, Framework-Migrations and Generator trailers; an update
that changed nothing commits nothing. A migration that commits on its own
(the v1.0.0 lift) lands its commit first.

A 'blocking: true' derivable migration — the lift, above all — is one the
install is not valid without. When one fails or is not attempted (an
earlier entry failed, its check could not run, it cannot be run), the
update exits 1, names it and why, and commits nothing: the install is
reverted and the tree is as the update found it, so a re-run starts from
the release again. Its own error is printed where it ran. A commit that
migration (or an earlier one) made itself stays. With --no-commit the
result is left in the working tree instead. Any other failed migration
is reported, the ledger keeps what succeeded, and the update still
commits the install. When this ape is older than the
release's min_ape_version, a terminal is offered an ape update first (bingo
when the project pins ape, else 'ape update'), committed as
'chore(ape): update ape to vA.B.C' when bingo changed, and the command
re-runs on the new ape; without a terminal it exits 11.

When this ape already meets the minimum and a newer one exists, nothing
changes unless asked: --update-ape moves ape the same way (the pin in its
own chore(ape) commit), and a terminal is asked, defaulting to No. Without
either, only a line says so — an unattended run never picks up an ape
nobody measured.

Until ape v0.4.0 this command committed nothing. --no-commit keeps that:
the whole result sits in the working tree for one 'git diff'. --repair's
output is never committed.

Does NOT touch _apex/config.yaml — that's the one-time bootstrap from
'ape framework setup'. To re-bootstrap, pass --force to 'setup'.

In an APEX v1 project, the plugins _apex/config.yaml lists are installed or
updated at project scope and written into .claude/settings.json, which the
update commit carries (see ape project init --help); a plugin that cannot be
put right is a warning with the command to run, not a failed update.

  --plan        print the upgrade-migration plan and do NOTHING ELSE — no
                install, no migration (it fetches the clone's tags unless
                --no-fetch). The plan is the list the
                project will hold AFTER this update: the installed entries
                overlaid by the repo's, with a SOURCE column marking the
                ones the update brings, so a migration can be read before
                it runs. The repo is read at the release it selects. It
                distinguishes pending / applied / half-applied /
                cannot-tell rather than collapsing them, because a runner
                that reads cannot-tell as pending re-applies things
  --dry-run     show the framework drift AND the pending migrations,
                writing nothing. The drift is against the release the update
                would install: --version's tag when given (a candidate too),
                else the newest final release. An apex-v1 install has no
                apex-v0 project-data migration, so none is listed
  --no-migrate  install framework files only; migrations stay pending, and
                'ape doctor' reports them so the state is visible
  --repair      also run the judgment phase over free-form deferred
                records. OFF by default: it spawns a paid opus session, and
                a file-copying verb should not start doing that silently.

Refuses to run when:
  - _apex/framework.yaml is absent (run 'ape framework setup' first)
  - committing, and the project is not a git repo, is on a detached HEAD,
    or has modified or staged tracked files (exit 4)
  - this ape is older than the release's min_ape_version (exit 11)
  - --from-worktree, and the framework repo is dirty, on a non-main
    branch, or its .claude/skills/apex-* subtree has uncommitted changes
    (pass --force to bypass)
  - your framework or governance clone is behind the release and has
    local changes (exit 3; pass --force-clone to overwrite them). A clone
    of yours only ever moves forward: one whose branch is ahead of the
    release, or diverged from it, is left where it is and reported. ape's
    own clones under the user cache are always overwritten

Exit codes: 1 a blocking migration was not applied (nothing committed), or
--version names an APEX v0 release in an APEX v1 project (nothing installed);
2 usage; 3 the framework source (no release, missing tag, not a release
layout); 4 the project tree; 7 not installed; 11 ape below
min_ape_version; 12 a commit failed (the changes are left staged).

A migration is skipped (never forced) when ITS OWN paths have uncommitted
changes. The gate is path-scoped rather than whole-tree: those paths are
disjoint from what the install writes, so the two are order-independent,
and unrelated work-in-progress elsewhere does not block anything.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--dry-run` | bool | `false` | Show the framework diff and pending migrations, writing nothing |
| `--force` | bool | `false` | Bypass safety checks (dirty framework, non-main branch, modified project skills) |
| `--force-clone` | bool | `false` | Overwrite local changes in YOUR framework or governance clone when moving it to the release (ape's own clones are always overwritten; a clone ahead of the release is never moved) |
| `--from-worktree` | bool | `false` | Install the framework repo's working tree instead of a release tag (the pre-v0.4.0 behaviour: main-only, clean, fast-forward) |
| `--no-check` | bool | `false` | With --plan: do not run any migration's check: command; every row falls back to the ledger alone |
| `--no-commit` | bool | `false` | Leave the result in the working tree instead of committing it |
| `--no-fetch` | bool | `false` | Skip fetching the framework and governance repos; use the releases they already hold |
| `--no-migrate` | bool | `false` | Install framework files only; leave migrations pending |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--plan` | bool | `false` | Print the upgrade-migration plan, incoming entries included, and do nothing else |
| `--repair` | bool | `false` | Also run the opus judgment phase over free-form deferred records (spends money) |
| `--update-ape` | bool | `false` | Also move ape to its newest release first, when this one already meets the minimum: the project's bingo pin (committed on its own as chore(ape)) or `ape update`. A terminal is asked instead, defaulting to No |
| `--version` | string | `—` | Install exactly this release tag, vX.Y.Z or vX.Y.Z-rc.N (default: the highest final vX.Y.Z; a candidate is only ever installed by name) |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root directory (default: current working dir) |
| `--repo` | string | `—` | Path to a checked-out apex_process_framework repo (default: $APEX_FRAMEWORK_REPO) |

## ape hook

Claude Code and git hooks for APEX v1 projects (installed by `ape project init`)

```
ape hook
```

Claude Code hook handlers, and git's pre-commit hook. Each Claude Code hook reads
the hook payload (JSON) on stdin.

  session-start   SessionStart: adds `ape status --brief` as context
  stop            Stop and SubagentStop: in a session ape launched, blocks the main
                  session's stop while background work runs, and a stop while the
                  in-progress change has a red command check (at most 3 times in a row);
                  in a person's own session, only shows what was red or still running
  pre-tool        PreToolUse (Edit|Write|MultiEdit|NotebookEdit|Bash): denies edits to
                  development/specs/**, changes/*/state.json, archive/**, _apex/state/**,
                  an existing development/briefs/** file, and Bash commands that create,
                  delete or push a git tag, push at all, stop processes by name (pkill,
                  killall), or rebase or merge on a change branch; while ape runs a
                  check in the checkout, denies an edit outside development/ that its
                  cleanup would undo; grants a person act on the person's own words
                  (E2), and denies one without them
  user-prompt     UserPromptSubmit: records the prompt a person typed, which is what
                  grants a person act (E2); never blocks the prompt

  pre-commit      git's pre-commit hook (installed by `ape project init` and the lift):
                  the commit secret scan on the staged added lines; exit 1 refuses
                  the commit (commit.secret)

Every Claude Code hook exits 0; a block or deny is expressed in its JSON output.
When the project's ape pin cannot run (.bingo/ape.mod; exit 13 for other verbs),
pre-tool exits 2 with the same message, so Claude Code blocks the call: the guard
fails closed. stop and session-start keep 13, which blocks nothing.
pre-commit is git's, not Claude Code's: its exit code is the verdict.

In a session ape task launches in a v1 project, session-start, stop and pre-tool also
come inline in ape's --settings (with --inline), so a hand-edited settings.json cannot
drop them; the settings.json copy then does nothing, so each event is decided once.
user-prompt is never inline: in a session ape launched it records nothing.

Subcommands:

- `pre-commit` — git pre-commit hook: refuse a commit whose staged added lines hold a secret
- `pre-tool` — PreToolUse hook: deny edits to specs, state.json, the archive, _apex/state and briefs, git tag writes, git push, pkill/killall, and rebases on a change branch
- `session-start` — SessionStart hook: inject `ape status --brief` as additional context
- `stop` — Stop/SubagentStop hook: block a stop while background work runs or the in-progress change's checks are red
- `user-prompt` — UserPromptSubmit hook: record the prompt a person typed (E2)

## ape hook pre-commit

git pre-commit hook: refuse a commit whose staged added lines hold a secret

```
ape hook pre-commit [flags]
```

git's pre-commit hook (Q89). `ape project init` and `ape project lift` install it
as <hooks dir>/pre-commit — the folder `git rev-parse --git-path hooks` names: core.hooksPath
when set, else .git/hooks (a linked worktree's is the main checkout's) — when no
pre-commit hook is there: a POSIX sh script, marked as ape's by its
"# ape: pre-commit secret scan" line, that runs `ape hook pre-commit` (ape resolved on PATH
when git runs it; not on PATH, the commit is refused saying so). A hook that is not ape's
is never touched: `ape project check` then lists the notice hooks.pre-commit-absent, naming
the line to add to it (`ape hook pre-commit || exit 1`), and so does `ape doctor`. git never
versions .git/hooks, so a fresh clone has no hook until `ape project init` runs again
(idempotent: it installs only what is missing). A hook in a core.hooksPath folder inside
the project, not ignored, is committed with the init or lift commit.

The scan is the one every commit ape makes goes through: the added lines of what git is
about to commit (the index git hands its hooks, so `git commit -a` and `git commit <paths>`
are scanned as they commit), betterleaks' default rules plus ape's aws-access-key-id,
the secrets.allow list at the guard's baseline (committed AND accepted), and the same
commit.secret refusal on stderr: each secret as <file>:<line> rule <id>: <redacted>,
the placeholder fix first, the allow entry a person may paste. Its files stay staged.

Exit codes: 0 clean (prints nothing), not a v1 project, or APE_SCANNED=1 (set by ape on
its own commits that run hooks — the archive squash, scanned already; the script checks
it before starting ape) · 1 a secret (commit.secret), or the scan could not run
(commit.failed): git commits nothing. `git commit --no-verify` skips the hook — a person's
escape; the archive's scan of the squash stays the backstop.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | project directory (default: the working directory, where git runs its hooks) |

## ape hook pre-tool

PreToolUse hook: deny edits to specs, state.json, the archive, _apex/state and briefs, git tag writes, git push, pkill/killall, and rebases on a change branch

```
ape hook pre-tool [flags]
```

PreToolUse hook for Edit|Write|MultiEdit|NotebookEdit|Bash. Denies, with
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny",
"permissionDecisionReason":"… specs change only through ape change archive"}}, an edit
whose target is under development/specs/, is development/changes/*/state.json, is under
development/archive/, or is under _apex/state/, and an edit to .claude/settings.json or
.claude/settings.local.json (ape's hooks and the session's environment are a person's;
ape project init restores the hooks).

A running check (RK-4b, Q144): while ape runs a check in this checkout (a verify, a gate,
a baseline, a Stop hook's re-run), denies an edit outside development/ and _apex/state/,
naming the check, its run and pid: when the check ends ape undoes what changed in the
tree while it ran. The check's marker (_apex/state/runs/checking-*.json) counts only while
the process that wrote it lives — its pid and that process's start time — so a check that
crashed never blocks an edit.

Briefs: denies an edit to a file that already exists under development/briefs/ (any
depth) — a brief is a plan's input, dated and verbatim, and is history: a new or
changed request is a new dated brief (development/briefs/YYYY-MM-DD-<slug>.md), linked
from the roadmap's Delivery: line. Writing a new file there is allowed. Symlinked
folders and links to a brief are resolved. A Bash write is not seen here;
`ape project check` reports it (brief.edited).

Bash: denies a command that writes a git tag — only a person tags or releases:
  git tag <name> (-a, -s, -f, -m …), git tag -d|--delete
  git push --tags | --follow-tags | --mirror
  git push <remote> <ref> where <ref> is refs/tags/… (either side, :refs/tags/x),
    "tag <name>", or a name the repository has as a tag and not as a branch;
    git push --delete|-d <remote> <name> likewise
Listing stays allowed: git tag, -l/--list [<pattern>], -n, --contains, --merged,
--points-at, --sort, --format, -v. The command is parsed: quotes, && || ; | chains,
subshells, NAME=value prefixes, env/sudo/xargs wrappers, git -C/-c, and sh -c / eval
scripts are followed, so `echo "git tag v1"` is not flagged. Not seen: "$(…)"
inside double quotes, git aliases, script files. A command without "git" is allowed
at once.

Bash, too (D45): denies every git push, in any form — a remote, a refspec, --dry-run,
git -C, sh -c, a chain: only a person pushes (with push.followTags set, even a branch
push publishes every reachable unpushed tag). A tag push needs both overrides below.

Bash, too (D46): denies pkill, killall, killall5 and skill in any form (sudo, env,
timeout, xargs, sh -c, a chain, an absolute path): they stop processes the session did
not start. The deny says: stop the process you started by its PID (kill <pid>), which
stays allowed.

Overrides: APE_ALLOW_TAG=1 allows tag writes, APE_ALLOW_PUSH=1 git push,
APE_ALLOW_KILL=1 pkill/killall. Each counts ONLY in the environment the person launches
the session with (`APE_ALLOW_PUSH=1 claude`, or `APE_ALLOW_PUSH=1 ape task …`),
which the hook inherits. Set inside a command the model runs — APE_ALLOW_PUSH=1 git
push, env APE_ALLOW_PUSH=1 …, export APE_ALLOW_PUSH=1; … — it does not count: the
command is denied as if it were not there (D47 Q6).

Bash, too (trial decision D40, the model never rebases): denies git rebase and git merge
while the repository the command runs in has a change branch, change/<id>, checked out —
ape catches a change up with its moved base itself (`ape change start|verify|archive <id>`
rebase it and re-take the baseline at the base tip). Allowed: rebase/merge --abort and
--quit, git merge-base, merge-file and merge-tree, and rebase or merge on any other
branch. Parsed like the tag rule, with the same limits.

Bash, too (E2, T6): denies a command that names _apex/state/prompts or
_apex/state/grants, or runs `ape hook user-prompt`, `ape hook pre-tool` or `ape __run`:
they write or act on the records a person act rests on.

Person acts (E2; D178, D179): eight commands are a person's — from a terminal outside
Claude Code as always, or in the person's own Claude Code session on the person's own
words. Each line is the command, then what the person types (the phrase grammar):
  ape milestone approve <M>                               approve <M>
  ape change approve <tweak-id>                           approve <tweak-id>
  ape change answer <change-id> --text "<answer>"         answer <change-id>: <answer>
  ape change accept <change-id> T<n>                      accept T<n> of <change-id>
  ape project accept-config                               accept the config
  ape milestone close <M> --accept-red "<reason>"         accept red on <M>
  ape change archive <change-id> --allow-pending-human    allow pending human on <change-id>
  ape decision accept <ADR-NNNN>                          accept <ADR-NNNN>
A Bash command that runs one is allowed only when the prompt that opened this turn — the
tool call's prompt_id is the prompt's — is recorded by `ape hook user-prompt`, the
session's transcript holds the person's user message with that promptId and the same
words (its origin.kind "human": a turn a notification opened is judged on the person's
latest typed prompt, D238), and the phrase is there: its words in order, the target exact (M2, T3, the change
id, ADR-0012; case aside), no negation word (not, no, never, don't and every n't, wait,
hold, stop, cannot, nope) before them in their sentence nor among the three words after the
target, and the sentence not a question; for an answer, the --text itself after the target.
"yes", "ok", "do it" and "approve it" grant nothing. Then pre-tool writes a grant,
_apex/state/grants/<sha256 of the command line>.json, used once, for 120s; a prompt grants
each act and target once. The command line is normalised — the verb, its arguments and
every flag set but --cwd, --json, --wait and --no-detach, as --name=value, sorted — and
must be literal (no $ or backquote anywhere), one person act per command. The command
consumes the grant and records by: <git config user.name> (prompt <ts>: "<the sentence,
at most 160 characters, secrets redacted>"); a --by there must be git config user.name
(else session.by-mismatch). Without a grant it is refused, session.person-only, naming the
phrase; pre-tool denies it first, saying why. A `!` line fires no hook, so it can never be
granted. In a session ape launched (APE_SESSION) nothing is recorded or granted.
A grant counts only in the session it was issued for (its session_id is the command's
CLAUDE_CODE_SESSION_ID; with none set, nothing is), and only while its prompt record and the
transcript's message with that promptId still pass the same match (D219). The residual: a
script the model writes and runs as the same OS user can forge all three; E2 attests the
person's words, it is not a sandbox, and pre-tool sees direct calls only.
A long act (approve, --accept-red, --allow-pending-human; D179) that meets a run of the
same command line on its milestone or change — still running (exit 75: run it again), or
ended with its result not yet printed — attaches with no grant; another run there is
run.busy; once its result is printed, the same command is a new act and needs the
person's words again.

Anything else: no output.

When the project's ape pin cannot run here — it names an ape below the floor (the
installed framework's min_ape_version, or 1.0.0 in a v1 project without
_apex/ape-commands.yaml) or one that is not installed — this hook exits 2 with the
pin's message, which Claude Code reads as a deny: the guard fails closed rather than
letting every tool call through. APE_TOOLCHAIN=local in the session's environment
runs this ape regardless.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | project directory (default: the payload's cwd) |

## ape hook session-start

SessionStart hook: inject `ape status --brief` as additional context

```
ape hook session-start [flags]
```

SessionStart hook. Prints {"hookSpecificOutput":{"hookEventName":"SessionStart",
"additionalContext":"<ape status --brief>"}}. Prints nothing outside a v1 project.

When the session's shell is zsh ($CLAUDE_CODE_SHELL when set, else $SHELL; Claude Code
runs the model's Bash commands in it), a paragraph follows the brief after a blank line:
quote globs and variable expansions, never an unquoted word starting with =, file names
from find, never from ls (apex-v0's operating rules said it; the lift drops them). ape's own
checks run under sh -c and need none of it.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | project directory (default: the payload's cwd) |

## ape hook stop

Stop/SubagentStop hook: block a stop while background work runs or the in-progress change's checks are red

```
ape hook stop [flags]
```

Stop and SubagentStop hook.

Background work (D88), first: in a session ape launched, the main session's Stop (not a
SubagentStop) is blocked while the payload's background_tasks lists anything — every
type, a backgrounded shell and a monitor included — on any branch, so in review,
delegate and tool sessions too. The reason starts "background work is still running",
names each task, and tells the model to wait for it in this turn, never to end the turn
while it runs, and next time to run a long command in the foreground with a 600000 ms
timeout. It counts toward the same 3 consecutive blocks; once they are spent the stop is
allowed. The tasks are recorded under "background" in the verdict, whatever it is.

Checks: when HEAD is on a change's branch, change/<id>, that
change is in-progress (read from the working tree), and any of the checks it re-runs
exits non-zero now, prints {"decision":"block","reason":"<the failing checks' last lines
and what to do>"}. It re-runs only the checks that were red at the change's last verify
of its current plan (the regression command too, when it was newly red), recorded in
_apex/state/logs/<id>/last-verify.json; before any verify, the task checks (H10).
Re-run budget (D58 2.5): a check whose last recorded run took longer than 60s (or
timeouts.check.max, if lower) is never re-run, nor any once one hook run has spent 600s;
a check with no recorded run is re-run under that budget, and one that outruns it is not
red. Such a check is judged from the last verify at HEAD — red stays red, with its last
lines — or, when the work moved since that verify, the stop is blocked telling the model to
run `ape change verify <id> --wait 9m`. The hook's timeout stays 1800s: it never runs a
long check, the verify does.
Allows (prints nothing) when HEAD is on no change branch (the base branch: planning,
milestones), when the change is not in-progress (blocked, verified, reviewed), when the
last verify had nothing red, when every re-run check is green, or after 3 consecutive
blocks (the count resets when the payload's stop_hook_active is false).

Only in a session ape launched (H13): the hook blocks when its process carries
APE_SESSION, which every session ape spawns has and Claude Code hands to its hooks. In a
person's own Claude Code session (plain `claude`, the settings.json copy of the hook) it
never blocks and runs no check: on an in-progress change whose last verify had red
checks it allows the stop with {"systemMessage":"APEX: change <id> is in progress, and at
its last verify … check(s) were red: …"}, shown to the person, and with background work
running it says so the same way ("APEX: the turn ended with background work still
running: …"); otherwise it prints nothing.

The verdict is also written to _apex/state/hooks/<session>[-<agent>].json, and the
main session's to the log <session>.jsonl, which is how `ape task` learns that a
Stop it saw did not end the turn (it takes one verdict per Stop, in order), and how
`ape change review --run` and `accept --delegate` learn that a session ended with
live background work.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | project directory (default: the payload's cwd) |
| `--timeout` | duration | `0s` | ceiling for each command run, overriding _apex/config.yaml timeouts.<tier>.max (default: check 2h, regression 2h, gate 4h; capped at ape's hard ceilings 4h/8h/12h); the tier's idle window still applies; exit 124 when exceeded |

## ape hook user-prompt

UserPromptSubmit hook: record the prompt a person typed (E2)

```
ape hook user-prompt [flags]
```

UserPromptSubmit hook (E2, D178). Appends {ts, session_id, transcript_path, prompt,
prompt_id, kind} — the prompt at most 64 KiB — to _apex/state/prompts/<session_id>.jsonl
(ignored, never committed): what `ape hook pre-tool` grants a person act on (its --help
has the phrase grammar). A prompt Claude Code generated (a <task-notification>, a
<system-reminder>, tags only) is recorded as kind notification and never grants (D238).
Records nothing in a session ape launched (APE_SESSION: ape types those prompts itself)
or outside a v1 project. Never blocks the prompt: it prints nothing and exits 0, an error
noted on stderr. `ape project init` registers it; missing, `ape project check` lists the notice
hooks.prompt-absent and `ape doctor` the row hooks.prompt — and a person act from a Claude
Code session is refused (session.person-only).

The phrase grammar (A12; the same lines as `ape hook pre-tool --help`, which says how a
grant is decided): each line is the command, then what the person types for it.
  ape milestone approve <M>                               approve <M>
  ape change approve <tweak-id>                           approve <tweak-id>
  ape change answer <change-id> --text "<answer>"         answer <change-id>: <answer>
  ape change accept <change-id> T<n>                      accept T<n> of <change-id>
  ape project accept-config                               accept the config
  ape milestone close <M> --accept-red "<reason>"         accept red on <M>
  ape change archive <change-id> --allow-pending-human    allow pending human on <change-id>
  ape decision accept <ADR-NNNN>                          accept <ADR-NNNN>
Run the command as a literal line (no $ or backquote, one act per command) and without
--by: ape records by: <git config user.name> with the person's quoted words.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | project directory (default: the payload's cwd) |

## ape log

Publish a structured log record over NATS

```
ape log <level> <message> [flags]
```

Publish one structured log record for the current Claude session on
ape.log.<user>.<project>.<session-id>.<level>.

<level> is one of debug|info|warn|error. Extra structured context is passed
as repeated --field key=value pairs. Centralized-logging consumers subscribe
ape.log.> (or per-user/project subtrees — the subject is the routing key).

Exit codes: 0 published · 1 publish failed (connected) · 2 usage error,
no NATS configured, or session unresolvable.

Examples:

```
  ape log info "migration step 3 complete"
  ape log warn "retrying" --field attempt=2 --field endpoint=api
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root for session auto-resolution (default: current working dir). |
| `--field` | stringArray | `[]` | Structured field as key=value (repeatable). |
| `--nats-creds` | string | `—` | NATS .creds file; its decoded user identity is baked into every subject (env APE_NATS_CREDS). |
| `--nats-url` | string | `—` | NATS server URL (env APE_NATS_URL). Required — no URL is a usage error (exit 2). |
| `--output-format` | string | `human` | Output format: human\|json (result object on stdout, diagnostics on stderr). |
| `--quiet` | bool | `false` | Suppress the human-mode confirmation line. |
| `--session-id` | string | `—` | Claude session id to report for (default: auto-resolve the current project's newest). |
| `--transcript` | string | `—` | Explicit transcript file; the session id is parsed from its name. |

## ape metrics

Scan and publish this session's usage metrics over NATS

```
ape metrics [flags]
```

Scan the resolved Claude session set (main + sub-agents) and publish a
usage snapshot on ape.metrics.<user>.<project>.<session-id>.

The payload carries per-model token counts (with the ephemeral 5m/1h cache
split), turn count, first/last turn timestamps, and the Claude Code version
— everything needed to reprice against Claude Code API rates at any later
moment (per_model tokens × the price table = cost_usd).

--run-id <id> instead publishes a completed run's manifest totals (a reader
over the run's manifest.yaml), with run_id populated. Republishing is
idempotent; consumers key on (session_id, ts).

Exit codes: 0 published · 1 publish failed (connected) · 2 usage error,
no NATS configured, or the session/run was unresolvable.

Examples:

```
  ape metrics
  ape metrics --output-format json
  ape metrics --run-id 20260709-abc123
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root for session auto-resolution (default: current working dir). |
| `--nats-creds` | string | `—` | NATS .creds file; its decoded user identity is baked into every subject (env APE_NATS_CREDS). |
| `--nats-url` | string | `—` | NATS server URL (env APE_NATS_URL). Required — no URL is a usage error (exit 2). |
| `--output-format` | string | `human` | Output format: human\|json (result object on stdout, diagnostics on stderr). |
| `--quiet` | bool | `false` | Suppress the human-mode confirmation line. |
| `--run-id` | string | `—` | Publish a completed run's manifest totals instead of a live session scan. |
| `--session-id` | string | `—` | Claude session id to report for (default: auto-resolve the current project's newest). |
| `--transcript` | string | `—` | Explicit transcript file; the session id is parsed from its name. |

## ape milestone

Add, approve, gate and close roadmap milestones

```
ape milestone
```

Milestones live in development/roadmap.md as "## M<n> — <title>" sections.
Their Status: line is written only by ape: proposed → approved (by <who> <ts>) →
done (<ts> <sha>), or done (accepted red by <who> <ts> <sha>) when a person closed it
with its gate red (`ape milestone close --help`). Every one of those writes is committed by ape (roadmap.md, plus
_apex/state/milestones.json when it exists) as "apex: <M> proposed|approved|done"
with trailer "Milestone: <M>". milestones.json holds each gate's result (green|red)
and is committed, as "apex: <M> gate green|red", only when that result changes; the
latest run of any result is in the gitignored _apex/state/logs/gate-<M>.json.
No milestone command leaves the tree dirty.

Approvals are records (H2): `ape milestone approve` records who approved, when, and the gate
approved — its command and the SHA-256 of each script it runs — under the milestone's
"approval" in milestones.json. change start, gate and close trust that record, never the
Status line alone: an approved milestone with no record (approved by hand, or before ape
recorded approvals) is refused with milestone.status-unrecorded, and gate and close refuse
one whose gate changed since with milestone.gate-changed — in both cases a person runs
`ape milestone approve <M> --by <who>` (again). `ape project check` reports both.

Branches (trial decision D38): every milestone command records on the base branch — the
one changes are started from and archived into (D11). On a change's branch, change/<id>,
new, approve, gate and close are refused (exit 1, code branch.change-branch): run
`git switch <base>` first. A change's status is read from its own branch, so close sees
every change in flight wherever it is worked.

Subcommands:

- `approve` — Write Status: approved (by <who> <ts>)
- `close` — Run the gate; on exit 0 write Status: done (<ts> <sha>)
- `gate` — Run the milestone's gate and record the result
- `new` — Append a milestone (Status: proposed) to roadmap.md

## ape milestone approve

Write Status: approved (by <who> <ts>)

```
ape milestone approve <M> --by <who> [--accept-green "<reason>"] [flags]
```

Approve a proposed milestone: its Status: line becomes "approved (by <who> <ts>)", and
milestones.json records the approval (H2): {by, at, gate_cmd, gate_files, gate_files_sha256,
gate_own_files, base_sha} — base_sha the milestone's start (D130): HEAD at its first approval, kept by a
re-approval, passed to every gate run as APE_MILESTONE_BASE (see `ape milestone gate --help`)
— the Gate: command, exactly as written, and the SHA-256 of each gate file: a script the
command runs that is a regular file under the project root at approval. Of each simple
command (split at ; && || | & newlines and parentheses; leading NAME=value assignments,
env, exec, command, time, !, if/then/else/elif/while/until/do skipped) the gate file is
the executable word when it is a path (it has a slash: ./scripts/e2e.sh, scripts/x.sh),
or the script given to an interpreter — sh bash zsh dash python python3 node ruby perl —
after its options (bash scripts/gate.sh, python3 scripts/gate.py; python3 -m/-c, bash -c,
node -e and friends name no script). No other word is hashed: arguments and data files
(grep -q x CHANGELOG.md), commands inside $(…) or backquotes (test "$(python3 greet.py)"
= Hello hashes nothing), bare command names (looked up on PATH), absolute paths, paths
leaving the root and words with $ or a glob; what a script runs in turn is not covered.
gate_files_sha256 digests the gate files. gate_own_files (RK-6, rc.7o) records the gate's own
files beside its scripts, each with its SHA-256: every file under the milestone's gate
folders (development/gates/<M>-*/) and development/gates/lib/; every file a recorded script
sources (". <path>", "source <path>", literal or "$(dirname "$0")/<path>"); and every
literal relative path a recorded script names under its own folder, when that folder is
not the project root or the development folder (scripts/checks/m4.checks beside
scripts/m4.sh) — never the files a gate checks elsewhere (README.md, docs/), which are the
milestone's work. One level: a file found is not read in turn. The gate differs from the
approved one when its command changed, or a recorded gate file it still runs, or a recorded
own file, is gone or changed (compared file by file); a gate file or own file created since
the approval, and a file an older approval recorded that the gate does not run as a
script, are not drift (the next re-approval records it). A change's commits that edit a
recorded gate file are refused by verify and archive (change.touches-gate). Changes can start only under an
approved milestone; gate and close refuse a gate that differs from the approved one.
Committed together as "apex: <M> approved".

Before it records anything, approve runs the gate once against the current tree (D70 B),
under timeouts.gate (default 30m without progress, 4h at most), in a detached run (OI-12; below), with progress lines
in its log. A new
gate must run and be red — it is red until the milestone's work is done:
  exit 0                                  refused, milestone.gate-green (action replan):
                                          a green gate proves nothing the milestone adds
  a gate file missing (a script the gate  refused, milestone.gate-broken (action replan)
  runs, as above), a shell gate file that
  fails `bash -n`, exit 126 or 127, a
  requires: entry missing, or a timeout
  any other exit                          red: approved, and the run recorded in the
                                          approval as self_check {at, sha, exit, secs, log,
                                          output_sha256}; its full output is
                                          _apex/state/logs/gate-<M>-approve.log
A refusal writes nothing and its message ends with the gate's last output lines. So a gate
whose script the milestone's work would write cannot be approved: the script exists at
approval and fails until the work is done (test -x ./bin/app || exit 1, not a bare
./bin/app, which exits 127). A re-approval runs the same check.

--accept-green "<reason>" (RK-5, Q145): a re-approval of an approved milestone whose work is
done and whose gate a person or apex-plan fixed after it — the fixed gate is green, which
the check above refuses, and gate and close refuse the changed gate until it is approved
again. With the flag a person states that case: the green run is recorded as self_check
(exit 0) beside accept_green {reason, by, at}, committed as "apex: <M> re-approved (gate
green: <reason>)"; then `ape milestone close <M>` closes it. A gate fixed before the work is done
and already green proves nothing: re-plan it instead. On a proposed milestone (a first
approval) the flag is a usage error (exit 2); on a re-approval whose gate is red it is not
needed, and the approval is recorded as without it. A person's act, as approve is.

Approving an approved milestone records the approval again — "apex: <M> re-approved", the
Status line kept as it was (the milestone's spend counts from it) — when ape holds no
record of it, its gate changed since, it runs a gate file the record does not hold (created
since: approving freezes it), or changes of it were blocked in a row (which resets
that count, limits.consecutive_blocks: `ape change new --help`); otherwise it is a no-op. A done one is refused.
Refused on a change branch (see `ape milestone --help`).

Only a person approves (D47 Q6): refused in a model session — APE_SESSION or CLAUDECODE
set, which includes a `!` line inside Claude Code — with exit 2, code
session.person-only, unless, in the person's own Claude Code session, the person typed
"approve <M>" in this turn (E2: `ape hook pre-tool --help`): --by is then optional and must be
git config user.name (else session.by-mismatch, exit 1), and the record's by is
"<user.name> (prompt <ts>: \"<the person's words>\")". The run lock records the act
(person_act): running the same command again while it runs (exit 75), or before its result
is printed, attaches with no new grant; another run on the milestone is run.busy.

Exit codes: 0 ok · 1 no such milestone, done, malformed (Gate/Goal missing), the gate green
or broken, HEAD on a change branch, or session.by-mismatch · 2 usage, or a model session
with no grant.

Detached runs (D58 2.3): the command takes the run lock of its change (or milestone),
_apex/state/runs/<id|M>.json, and runs in a detached worker (`ape __run`, its own
session, output in _apex/state/runs/<id|M>.log), so the call that started it can be cut,
backgrounded or killed without stopping it. The call waits up to --wait (default 9m, at
most 9m30s; 0 returns at once); a run that ends in time is printed as always, with the same
exit code and --json shape. Otherwise the call exits 75, still running: "… still running
since <time> — run the same command again to attach" (--json: {"running": true, "verb",
"scope", "since", "log", "pid", "log_tail"}, the shape `ape change review --run` has).
Running the same command again attaches: it waits again and prints the run's result once it
ends — never a second run — or exits 75 again with the log lines the run wrote since the
last call (stderr; --json: "log_tail"). A result no call
printed yet is printed by the next call of the same verb while HEAD has not moved. While a
run holds the lock, another of these verbs on the same change or milestone is refused:
run.busy (exit 3, action retry; the message names the running verb and since when). A lock
whose process is gone (killed, the machine restarted) is removed and reported as
run.interrupted (exit 1, action retry) — never an attempt. A run stopped by SIGTERM/SIGINT
kills its checks and records nothing — no attempt, no state commit, no gate run — and
reports run.interrupted. No check outlives the worker. Never run these with
run_in_background, and never end a turn on an exit 75: run the same command again.
Beside the exit codes above: 75 still running · 3 run.busy · 1 run.interrupted · 2 a
--wait outside 0–9m30s (usage.wait).

Examples:

```
  ape milestone approve M1 --by eval
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--accept-green` | string | `—` | re-approval only: the gate is green because the milestone's work is done and the gate was fixed after it — why (Q145) |
| `--by` | string | `—` | who approves (required from a terminal; in a Claude Code session, git config user.name) |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |
| `--wait` | duration | `9m0s` | how long this call waits for the detached run (at most 9m30s; 0 returns at once); exit 75 while it still runs — run the same command again to attach |

## ape milestone close

Run the gate; on exit 0 write Status: done (<ts> <sha>)

```
ape milestone close <M> [--accept-red "<reason>" --by <who>] [flags]
```

Close an approved milestone. Refused while a change of this milestone is in-progress,
verified or reviewed (archive or block it first) — each change's status read from its own
branch, change/<id>; a tweak in flight (milestone: none, D40) counts toward no milestone and
does not hold the close — and on a change branch (see `ape milestone --help`). Otherwise runs the gate (recorded as by
`ape milestone gate`) and, only on exit 0, writes "Status: done (<ts> <sha>)" into
roadmap.md, committed with milestones.json as "apex: <M> done". Green means the gate command
AND every scenario check of the spec pass, AND the regression command (when configured) is
not newly red against the milestone's earliest baseline for it — with no baseline, red fails
(see `ape milestone gate`). A red gate is recorded as
by `ape milestone gate`. A milestone is done when this command exits 0 — never because
anyone says so. Refused, as by `ape milestone gate`, when ape holds no approval record or the
gate changed since the approval (H2). ape records the close in milestones.json (done_at).

Gate folders (D155 Q74): each folder development/gates/<M>-*/ (by name; development/gates/lib/
is no milestone's and stays) moves, in the close commit (a rename: every file's content and
sha256 unchanged), to development/archive/milestones/<M>/gate/ — one folder's contents into
gate/, several each into gate/<folder>/ — beside the close evidence; --accept-red too. A
gate outside development/gates/ (scripts/…) stays where it is. milestones.json records each
move as gate_archived: [{from, to}] (JSON "gate_archived"); the approval's gate_files keep
the paths as approved, so a verifier maps a path under "from" to "to" and compares the same
sha256. A gate/ already there refuses the close before the gate runs, with nothing written
(milestone.gate-archive-exists, action person: move it away).

With "evidence: archive" in _apex/config.yaml (H23; default off), a green close writes the
gate run's evidence to development/archive/milestones/<M>/evidence/ — one file per check
(the gate command, every spec scenario, the regression command): command, exit, duration,
output sha256 and the output, capped at caps.evidence_log, default 1 MiB (head and tail
kept, the full log named by path and sha256) — plus accept-red.json when milestones.json records an accept_red for
<M>; committed with the close record (JSON "evidence").

Changes in flight refuse with milestone.changes-in-flight, action apply — or person when
one of them is reviewed with human-review tasks not accepted (awaiting_human; D49).

--accept-red "<reason>" --by <who> (H22, D45): a person closes the milestone although its
gate is red — a release shipped with known defects, say. Everything else is as without it
(approval record, gate as approved, no change in flight); when the gate is red, the
milestone is done anyway: milestones.json records accept_red: {reason, by, at, gate: the red
run}, the Status line reads "done (accepted red by <who> <ts> <sha>)", committed as
"apex: <M> done (accepted red)", and `ape status` shows it (JSON milestones[].accept_red;
the brief names the last such milestone). A green gate closes as usual and accepts nothing.
Only a person passes it: refused in a model session (APE_SESSION or CLAUDECODE set, a `!`
line inside Claude Code included) with exit 2, session.person-only — unless, in the person's
own Claude Code session, the person typed "accept red on <M>" in this turn (E2: `ape hook pre-tool --help`;
--by is then git config user.name, recorded with the prompt). Such a close attaches only to
a run of the same command line: a plain close running on the milestone is run.busy (exit 3),
never an attach that would drop the flag. A person tags after close.

Exit codes: 0 done · 1 gate red (without --accept-red), changes in flight, not approved,
no approval record, a changed gate, an archived gate/ in the way, or HEAD on a change
branch · 2 usage, or --accept-red from a model session.

Detached runs (D58 2.3): the command takes the run lock of its change (or milestone),
_apex/state/runs/<id|M>.json, and runs in a detached worker (`ape __run`, its own
session, output in _apex/state/runs/<id|M>.log), so the call that started it can be cut,
backgrounded or killed without stopping it. The call waits up to --wait (default 9m, at
most 9m30s; 0 returns at once); a run that ends in time is printed as always, with the same
exit code and --json shape. Otherwise the call exits 75, still running: "… still running
since <time> — run the same command again to attach" (--json: {"running": true, "verb",
"scope", "since", "log", "pid", "log_tail"}, the shape `ape change review --run` has).
Running the same command again attaches: it waits again and prints the run's result once it
ends — never a second run — or exits 75 again with the log lines the run wrote since the
last call (stderr; --json: "log_tail"). A result no call
printed yet is printed by the next call of the same verb while HEAD has not moved. While a
run holds the lock, another of these verbs on the same change or milestone is refused:
run.busy (exit 3, action retry; the message names the running verb and since when). A lock
whose process is gone (killed, the machine restarted) is removed and reported as
run.interrupted (exit 1, action retry) — never an attempt. A run stopped by SIGTERM/SIGINT
kills its checks and records nothing — no attempt, no state commit, no gate run — and
reports run.interrupted. No check outlives the worker. Never run these with
run_in_background, and never end a turn on an exit 75: run the same command again.
Beside the exit codes above: 75 still running · 3 run.busy · 1 run.interrupted · 2 a
--wait outside 0–9m30s (usage.wait).

Examples:

```
  ape milestone close M1
  ape milestone close M3 --accept-red "ships with the known RF drift; fixed in M4" --by diego
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--accept-red` | string | `—` | close although the gate is red, for this reason (a person only; with --by) |
| `--by` | string | `—` | who accepts the red gate (with --accept-red) |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |
| `--timeout` | duration | `0s` | ceiling for each command run, overriding _apex/config.yaml timeouts.<tier>.max (default: check 2h, regression 2h, gate 4h; capped at ape's hard ceilings 4h/8h/12h); the tier's idle window still applies; exit 124 when exceeded |
| `--wait` | duration | `9m0s` | how long this call waits for the detached run (at most 9m30s; 0 returns at once); exit 75 while it still runs — run the same command again to attach |

## ape milestone gate

Run the milestone's gate and record the result

```
ape milestone gate <M> [flags]
```

Run the milestone's Gate: command from the project root — with APE_MILESTONE=<M> and
APE_MILESTONE_BASE=<the milestone's start: HEAD at its first approval, ape status --json
milestone.base_sha> in its environment (D130), so a gate can test what changed since, e.g.
`git diff --quiet "$APE_MILESTONE_BASE" HEAD -- docs/ && exit 1`; the same two at approve's
self-check and at close; empty base for an approval before rc.3 — and every scenario check of
the whole spec beside it as the regression set (human-review not run; output under
_apex/state/logs/gate-<M>/), and the project's regression command from _apex/config.yaml
when one is configured — its milestone tier, "regression: {change: <cmd>, milestone:
<cmd>}", else the one command (H10). The gate is green only when all pass — the regression command
only needs not to be newly red: it is judged against the earliest regression baseline,
for the same command, recorded by any change of the milestone (active or archived; a tweak,
milestone: none, is no change of any milestone and never supplies it — D40);
red there and red now is reported (JSON run.regression.pre_existing_red), not failed —
unless, with failing tests named by both runs (as for `ape change verify`), a test fails
now that did not at that baseline: red, the names in run.regression.new_failures.
When no change of the milestone has a baseline for the command, a red regression fails
the gate: nothing shows it was red before the milestone's work — always the case for a
milestone tier that differs from the change tier, which no change baselines. Timeouts:
the Gate: command runs under timeouts.gate (default idle 30m, max 4h), each scenario check
under timeouts.check (idle 15m, max 2h), the regression command under timeouts.regression
(idle 30m, max 2h) — killed at idle without progress (output or CPU time) or at max: exit
124, reason idle|ceiling; --timeout sets max for all three. A vacuous or unrun gate command, scenario or regression command is
red (see `ape change verify --help`; JSON reason/detail). Files the gate
or a check leaves outside development/ are undone and reported ("stray_files"), a copy of
each kept under _apex/state/strays/ ("strays_kept"; see `ape change verify --help`). A red scenario
whose check names what `ape decision standard` removed says so on its line and in
run.spec_failed[].stale_names (D232; see `ape decision standard --help`). The run (exit, sha, time,
output hash, last lines) goes to _apex/state/logs/gate-<M>.json (gitignored; shown by
`ape status`). When the result differs from the recorded one (green vs red), or is
the first, _apex/state/milestones.json is updated and committed as
"apex: <M> gate green|red". Does not change the milestone's status — `ape milestone close` does.
Refused on a change branch: the gate judges the base branch (see `ape milestone --help`).
On an approved milestone, refused when ape holds no approval record (milestone.status-unrecorded)
or the gate differs from the approved one (milestone.gate-changed): a person approves it again.

Exit codes: 0 gate green · 1 gate red (last output lines shown), no such milestone, no
approval record, a changed gate, or HEAD on a change branch · 2 usage.

Detached runs (D58 2.3): the command takes the run lock of its change (or milestone),
_apex/state/runs/<id|M>.json, and runs in a detached worker (`ape __run`, its own
session, output in _apex/state/runs/<id|M>.log), so the call that started it can be cut,
backgrounded or killed without stopping it. The call waits up to --wait (default 9m, at
most 9m30s; 0 returns at once); a run that ends in time is printed as always, with the same
exit code and --json shape. Otherwise the call exits 75, still running: "… still running
since <time> — run the same command again to attach" (--json: {"running": true, "verb",
"scope", "since", "log", "pid", "log_tail"}, the shape `ape change review --run` has).
Running the same command again attaches: it waits again and prints the run's result once it
ends — never a second run — or exits 75 again with the log lines the run wrote since the
last call (stderr; --json: "log_tail"). A result no call
printed yet is printed by the next call of the same verb while HEAD has not moved. While a
run holds the lock, another of these verbs on the same change or milestone is refused:
run.busy (exit 3, action retry; the message names the running verb and since when). A lock
whose process is gone (killed, the machine restarted) is removed and reported as
run.interrupted (exit 1, action retry) — never an attempt. A run stopped by SIGTERM/SIGINT
kills its checks and records nothing — no attempt, no state commit, no gate run — and
reports run.interrupted. No check outlives the worker. Never run these with
run_in_background, and never end a turn on an exit 75: run the same command again.
Beside the exit codes above: 75 still running · 3 run.busy · 1 run.interrupted · 2 a
--wait outside 0–9m30s (usage.wait).

Examples:

```
  ape milestone gate M1
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |
| `--timeout` | duration | `0s` | ceiling for each command run, overriding _apex/config.yaml timeouts.<tier>.max (default: check 2h, regression 2h, gate 4h; capped at ape's hard ceilings 4h/8h/12h); the tier's idle window still applies; exit 124 when exceeded |
| `--wait` | duration | `9m0s` | how long this call waits for the detached run (at most 9m30s; 0 returns at once); exit 75 while it still runs — run the same command again to attach |

## ape milestone new

Append a milestone (Status: proposed) to roadmap.md

```
ape milestone new "<title>" --gate "<cmd>" [--budget N] [flags]
```

Append "## M<n> — <title>" to development/roadmap.md with Status: proposed, the gate
command, the budget (USD list-price equivalent, when given) and a Goal: line. The id is
the next free M<n>. Write the Goal paragraph and the Out of scope bullets afterwards
(or pass --goal); the Status: line is ape's. Committed as "apex: <M> proposed".
The gate's scripts must exist, and the gate must run red, before a person approves it
(D70 B: `ape milestone approve --help`).

Refused on a change branch (see `ape milestone --help`).

Exit codes: 0 ok (prints the id) · 1 no roadmap.md, or HEAD on a change branch · 2 usage.

Examples:

```
  ape milestone new "Walking skeleton" --gate "./scripts/e2e.sh m1" --budget 30
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--budget` | float64 | `0` | budget in USD list-price equivalent |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--gate` | string | `—` | the one command that is red until the milestone is done (required) |
| `--goal` | string | `—` | the Goal: paragraph (else a placeholder to fill in) |
| `--json` | bool | `false` | machine output (JSON on stdout) |

## ape next

Record a finding for later in development/next.md (Next)

```
ape next
```

Next holds what was noticed but is out of scope now (H26): one dated line per
finding, which apex-plan triages at the next delivery — a milestone, kept, or deleted
with the reason in its commit. Next is not a milestone.

Since D217 it lives in development/next.md; the roadmap keeps one line in its
place, `Next — not milestones: [next.md](next.md)`. A project made before keeps
Next in development/roadmap.md (a "## Next" section, or items under apex-plan's
"Next — not milestones:" label) until `ape next move` moves it (the framework's
migration v1.0.0_seq-09).

Subcommands:

- `add` — Append a dated finding to Next (development/next.md), on the base branch
- `move` — Move the roadmap's Next into development/next.md (the framework's seq-09 runs it)

## ape next add

Append a dated finding to Next (development/next.md), on the base branch

```
ape next add "<text>" [--from <change-id>] [flags]
```

Append one line to Next:

  - YYYY-MM-DD <text> (from <change-id>)

(without "(from …)" when --from is not given) at the end of development/next.md (D217).
next.md is used when it exists, or when the roadmap holds no Next block: a project with
neither gets next.md from its skeleton (a "# Next" title and a line saying these are
findings to triage, not milestones) and the roadmap the line pointing at it,
`Next — not milestones: [next.md](next.md)`, above its first milestone. A project not yet
moved — its roadmap holds Next and there is no next.md — keeps getting the line in the
roadmap, so its items are never split across two files (D217 Q125): at the end of its
"## Next" section; else under apex-plan's "Next — not milestones:" label above the first
milestone, after its list. `ape next move` moves them (see its --help).

Committed alone on the base branch as "apex: next — <text>": from the base branch
itself, or from a change's branch, change/<id>, without a checkout — the change's branch
and working tree do not move, and a base branch moved only by such a record does not
make the change catch up. Refused when the base branch is checked out in another
worktree (git.base-checked-out).

For models and people alike: apex-apply and apex-change record what they notice outside
the change's scope here instead of building it (--from <id>, the change that noticed it).
The archive carries a review's "Noticed, out of scope" items the same way.

JSON: {"line", "path" (the file the line went to: development/next.md, or the roadmap
of a project not yet moved), "pointer" (the roadmap, when it was given the pointer
line), "branch", "commit"}.

Exit codes: 0 recorded · 1 no roadmap.md, or the base branch is checked out elsewhere ·
2 usage (empty or multi-line text, --from not a change id).

Examples:

```
  ape next add "the CSV export ignores --tz" --from add-tenant-export
  ape next add "ask finance whether refunds need an audit trail"
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--from` | string | `—` | the change that found it |
| `--json` | bool | `false` | machine output (JSON on stdout) |

## ape next move

Move the roadmap's Next into development/next.md (the framework's seq-09 runs it)

```
ape next move [--check] [flags]
```

Move Next out of development/roadmap.md into development/next.md (D217), in one commit,
"apex: Next moved to development/next.md (seq-09)". The framework's migration
v1.0.0_seq-09 runs it (command: ape next move; check: ape next move --check); ape
project init and the lift lay the new layout down directly.

  - the items of the roadmap's "## Next" sections and those under apex-plan's
    "Next — not milestones:" label above the first milestone are appended to next.md
    byte for byte (list line and continuation lines), in order — a section's other
    lines (prose, sub-headings, code) with them — and each item once: one next.md
    holds already, or the block held earlier, is left out (JSON "duplicates")
  - next.md is created from its skeleton when absent: a "# Next" title and one line
    saying these are findings to triage, not milestones
  - the label and its list are replaced, in place, by the line
    `Next — not milestones: [next.md](next.md)`; each "## Next" section is removed, and
    with no label to replace the line goes above the first milestone. A roadmap that
    has the line keeps it, and gets no second one. Nothing else of the roadmap changes

Idempotent: once the roadmap holds no Next block and has the line, and next.md exists,
it writes nothing (JSON "pending": false). A project with both — a roadmap block and
next.md — is merged. `ape project check` lists the notice roadmap.next-inline
while the roadmap holds Next; `ape next add` and the archive keep adding to the
roadmap's block until then, when there is no next.md.

Refused on a change branch (branch.change-branch), outside a git repository
(git.not-a-repo), and while roadmap.md or next.md has uncommitted changes (tree.dirty):
nothing is written. A command any session may run (not person-only): it moves the
project's own lines and decides nothing.

--check writes nothing: exit 0 when Next is in next.md already, 1 (next.move-pending,
action apply, naming what is left) when a move is due — the migration's check: 0
applied, 1 not applied.

JSON: {"moved": [the items, as ape status lists them], "duplicates": […], "path"
(next.md), "roadmap", "created", "pointer_added", "pending", "commit"}. Under --check it
says what is due, under "result" beside the failure's ok/exit/diagnostics (exit 1).

Exit codes: 0 moved, or nothing due (--check: nothing due) · 1 refused, no roadmap.md,
or (--check) a move due.

Examples:

```
  ape next move --check
  ape next move
  ape next move --json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--check` | bool | `false` | write nothing: exit 0 when Next is in next.md already, 1 when a move is due |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |

## ape plugins

Install the plugins _apex/config.yaml lists, and report what they and the tools slot bring

```
ape plugins
```

The Claude Code plugins an APEX v1 project uses, and the tools slot (D59 Q4–Q6).

_apex/config.yaml (marketplaces, plugins and tools are under the config.changed
guard: a commit that changes them and is not ape's is a person's to accept):

  marketplaces:
    team-tools: {source: github, repo: org/claude-plugins, ref: v2}   # or {source: git, url: …},
                                                                      # {source: directory, path: …}
  plugins:
    - {name: nats-expert, marketplace: team-tools, ref: v1.4.0}
    - {name: kb, marketplace: team-tools, allow: [mcp]}
  tools:
    design:
      - {name: ui-mockup, skill: frontend-design:mockup, when: "the change adds or changes a screen",
         args: "{change} --out {assets}", model: sonnet, effort: high}
    review:
      - {name: security, skill: security-lens, when: "the change touches auth, secrets or input parsing"}

allow: lists what a plugin may carry besides skills — hooks, mcp. A listed plugin
that carries hooks (hooks/hooks.json, or hooks in .claude-plugin/plugin.json) or MCP
servers (.mcp.json, or mcpServers in plugin.json) its entry does not allow is the
finding plugin.hooks-or-mcp (action person): a tool is a skill only. Another allow:
value is a config error (plugins.config).

tools: has two slots, design and review; each entry is {name, skill, when}, names
unique, plus optional args: (the template ape fills into ape task <skill> --args;
placeholders {id}, {task}, {assets}, {change}, no other), model: and effort: (low,
medium, high, xhigh, max; unset: the D65 defaults). A skill is <plugin>:<skill> (the plugin listed in plugins:), or a project
skill (.claude/skills/<skill>/SKILL.md) or user skill ($CLAUDE_CONFIG_DIR, else
~/.claude, /skills/<skill>/SKILL.md; a user skill wins over a project one). Each must
be installed (tools.not-installed) and slash-invocable — not user-invocable: false
(tools.not-invocable) — because ape task types it as a slash command; a malformed
block is tools.invalid. The loop may start these skills as children, beside
apex-plan, apex-change, apex-apply, apex-review, apex-release and apex-accept (D62,
D76); apex-handoff, apex-loop and the operator's apex-orchestrator never.

ape project check reports the same findings (exit 1).

Subcommands:

- `status` — Report each listed plugin and tools: skill, and their findings
- `sync` — Install or update every plugin _apex/config.yaml lists and write their settings

## ape plugins status

Report each listed plugin and tools: skill, and their findings

```
ape plugins status [flags]
```

Report, for each plugin _apex/config.yaml lists: whether it is installed at project
scope, its version against ref, whether settings.json enables it, how many skills it
adds and how many of them are model-invocable (no disable-model-invocation: true —
each one's description is listed in every session, the plugin's per-session cost),
whether it carries hooks or MCP servers, and whether its allow: covers them. And for
each tools: entry: where its skill was found (plugin, project, user) and its state —
ok, not-installed, not-invocable, or unchecked (a plugin skill while claude is not on
PATH, so the installs cannot be read).

Findings: plugins.config, plugins.drift (ape plugins sync puts it right),
plugin.hooks-or-mcp, tools.invalid, tools.not-installed, tools.not-invocable — see
ape plugins --help. They are reported, not failed on: ape project check is the gate.

JSON: {"installs_checked", "plugins": [{"id", "ref", "installed", "version", "at_ref",
"enabled", "install_path", "skills", "model_invocable", "hooks", "mcp", "allow",
"allowed"}], "tools": [{"slot", "name", "skill", "source", "path", "state"}],
"findings": [{"file", "message", "code", "fix"}]}.

Exit codes: 0 reported (findings or not) · 1 the config cannot be read · 2 usage.

Examples:

```
  ape plugins status
  ape plugins status --json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |

## ape plugins sync

Install or update every plugin _apex/config.yaml lists and write their settings

```
ape plugins sync [flags]
```

Make the project's plugins what _apex/config.yaml says: each marketplace added, each
plugin installed at project scope (or, off its ref, updated after its marketplace is
refreshed) through the claude plugin CLI, and extraKnownMarketplaces and enabledPlugins
written into .claude/settings.json (other keys kept). A settings.json that changed is
committed as "apex: plugins" (that file only). A second run installs, writes and
commits nothing.

ape project init, ape framework update and ape doctor --fix do the same; ape task
installs a missing plugin before it spawns. Afterwards the findings ape plugins status
reports (plugin.hooks-or-mcp, tools.*) are listed; they do not fail the sync.

Exit codes: 0 synced · 1 a plugin is still missing or off its ref (plugins.missing,
with the command to run), the config is malformed (plugins.config), or the commit
failed · 2 usage.

Examples:

```
  ape plugins sync
  ape plugins sync --json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |

## ape project

Create and check an APEX v1 project layout

```
ape project
```

Subcommands:

- `accept-config` — Accept the committed _apex/config.yaml's guarded blocks (limits, checks, caps, …)
- `archive-layout` — Move archived changes into development/archive/changes/ (the v1.0.0_seq-06 migration)
- `check` — Validate every v1 shape and size cap; list all failures
- `codes` — List the diagnostic codes v1 commands report in --json failures
- `init` — Create the development/ skeleton and merge ape's hooks (never overwrites)
- `lift` — Lift an APEX v0 project to v1 in one commit (the mechanical half of the migration)
- `operator-interface` — Bring a project made before rc.7 to the operator interface (the v1.0.0_seq-07 migration)

## ape project accept-config

Accept the committed _apex/config.yaml's guarded blocks (limits, checks, caps, …)

```
ape project accept-config --by <who> [flags]
```

Fifteen blocks of _apex/config.yaml can loosen what ape enforces, and are guarded:
  limits        attempts, re-plans, consecutive blocks, the budget
  checks        vacuous output
  retries       red checks run again
  requires      unrun checks
  timeouts      how long a check, the regression command or a gate may run
  caps          the artefact size caps (D69; see ape project check --help)
  regression    the project's own check command (unset: the net is off)
  commit        the archive's squash (hooks: skip skips the project's hooks)
  evidence      archive|off (off keeps no evidence)
  marketplaces, plugins, tools   what runs inside a session (hooks, MCP, skills)
  human_review  who accepts a human-review task in a person's place (D75; docs: D152)
  release       how apex-release proposes the next version, and what it brings up to date (D120)
  secrets       the fingerprints the commit secret scan lets through (D155; commit.secret):
                an entry counts only once a person accepts it (R23)
The guard compares whole blocks, either way: raising a cap or a timeout loosens as surely
as lowering one. Since the project's v1 epoch (the newest "apex: project init" or "apex:
lift" commit; a block committed before it, or in it, is the baseline), a commit that
changes one of them and is not ape's own ("apex: …") is the finding config.changed (action
person; D50), and change start, verify and archive refuse (change.provenance) — so a
session cannot loosen its own limits with an Edit. Unguarded: project_name,
development_folder, models, effort, docs, notify.

A person who made the change, or approves it, accepts it: this command commits an empty
"apex: config accepted by <who>" on the base branch (its body lists the guarded blocks),
and every guarded change before it is clean. A later change is judged again. A git revert of the change also clears it.

Only a person accepts (D47 Q6): refused in a model session — APE_SESSION or CLAUDECODE set,
which includes a `!` line inside Claude Code — with exit 2, code session.person-only,
unless, in the person's own Claude Code session, the person typed "accept the config" in
this turn (E2: `ape hook pre-tool --help`; --by is then git config user.name, recorded with
the prompt; another --by is session.by-mismatch, exit 1).
Refused on a change branch, and with staged changes in the index.

Exit codes: 0 accepted (prints the commit) · 1 HEAD on a change branch, staged changes, or
git failed · 2 usage, or a model session.

Examples:

```
  ape project accept-config --by diego
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--by` | string | `—` | who accepts (required) |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |

## ape project archive-layout

Move archived changes into development/archive/changes/ (the v1.0.0_seq-06 migration)

```
ape project archive-layout [--check] [flags]
```

Move every archived or abandoned change still in the older layout,
development/archive/<YYYY-MM-DD>-<id>/, into development/archive/changes/<YYYY-MM-DD>-<id>/,
where change archive and change abandon put them (D155). The framework's migration
v1.0.0_seq-06 runs it (command: ape project archive-layout; check: ape project
archive-layout --check). development/archive/milestones/ stays where it is.

One commit, "apex: archive layout (seq-06)": each folder moved with git mv, and in each
moved state.json the recorded paths that named the old folder (archived_to, and an
acceptance's evidence inside the folder) rewritten to the new one; nothing else in the
files changes. The refs refs/apex/archive/<id> and refs/apex/abandoned/<id>, and
state.json's tip_ref, name commits and are untouched. Idempotent: with nothing left to
move it writes nothing and says "nothing to move" (exit 0).

Every ape command reads archived changes from both layouts, always, so a project that has
not moved yet, or a branch cut before the move, works the same either way.

Refused on a change branch (branch.change-branch), with uncommitted changes (tree.dirty),
outside a git repository (git.not-a-repo), and when a folder's new place already exists
(archive.folder-exists, exit 3: nothing is written; a person merges the two by hand). A
command any session may run (not person-only).

--check writes nothing: exit 0 when no archived change is left in the older layout, 1
(archive.legacy-layout, naming them) when some is — the migration's check: 0 applied,
1 not applied.

JSON: {"moved": [{"id", "from", "to"}], "rewritten": [<state.json>], "commit"}; "moved"
is always there, [] when nothing moved. Under --check it lists what is left to move, at
the top level beside the failure's ok/exit/diagnostics when some is (exit 1), and is []
when nothing is (exit 0).

Exit codes: 0 moved, or nothing to move (--check: nothing left) · 1 refused, or (--check)
some left · 3 a folder's new place exists.

Examples:

```
  ape project archive-layout --check
  ape project archive-layout
  ape project archive-layout --json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--check` | bool | `false` | write nothing: exit 0 when nothing is left in the older layout, 1 when some is |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |

## ape project check

Validate every v1 shape and size cap; list all failures

```
ape project check [flags]
```

Validate the whole project and list every failure, not just the first.

  development/project.md   # <name>, ## Vision, ## Users, ## Scope, ## Out of scope,
                           ## Constraints (each non-empty); ≤ caps.project_md (16 KB;
                           no line cap)
  development/architecture.md
                           optional; when present ≤ caps.architecture_md (16 KB;
                           architecture.too-big: move detail to specs/, decisions/ or
                           docs/explanation/)
  AGENTS.md                present, ≤ caps.agents_md (16 KB; its lines are not measured);
                           a line outside code carries @development/project.md (the
                           configured folder; the reference counts, not the wording —
                           agents.no-project-context: `ape project init` adds the line);
                           CLAUDE.md (if any) has the line @AGENTS.md
  development/roadmap.md   # Roadmap, ≥ 1 "## M<n> — <title>" with Status (ape-written),
                           Gate, Goal; Budget a number when present; the line
                           `Next — not milestones: [next.md](next.md)` points at Next
                           (D217); a "## Next" section that still holds findings is
                           not a milestone (notice roadmap.next-inline). Its Delivery: line
                           links the current brief, e.g.
                           `Delivery: Phase 1 — "…" ([brief](briefs/2026-10-16-<slug>.md))`;
                           a Markdown link or a bare briefs/….md path, relative to
                           roadmap.md (or development/briefs/… from the project root),
                           must resolve (roadmap.delivery-link); briefs with no linking
                           Delivery: line are roadmap.delivery-unlinked. An approved
                           milestone has ape's approval record (milestone.status-
                           unrecorded; a done one, ape's close) and its gate as approved
                           (milestone.gate-changed): a person approves it again
  development/next.md      optional (D217); only its items are checked: a leading date
                           is a real YYYY-MM-DD with text after it, a "(from <id>)"
                           names a change id (next.bad-item); an undated item, as
                           apex-plan wrote them, is accepted
  development/briefs/      YYYY-MM-DD-<slug>.md with a real date (brief.bad-name);
                           committed with the plan (brief.uncommitted); never changed
                           from the text of the commit that added it, in the working
                           tree or a later commit (brief.edited: restore it, write a
                           new dated brief)
  specs/<cap>/spec.md      # title, Purpose: (the line and those after it up to a blank
                           line, heading or fenced code), "### Requirement: <name>" each
                           with ≥ 1 "#### Scenario:" and exactly one "- check: <cmd> |
                           human-review" per scenario; ≤ caps.spec (24 KB; "split into two
                           capabilities")
  decisions/NNNN-<slug>.md # ADR-NNNN: title, Status, Date, Context/Decision/Consequences,
                           Enforced by; an accepted one recorded in decisions.json
                           (decision.accepted-unrecorded: `ape decision accept` records it)
  changes/<id>/            every non-archived change, as `ape change check`
  layout                   no development/changes/archive/ (an older ape's archive: move
                           its folders to development/archive/), no development/.apex/
                           (an older ape's state: move milestones.json and decisions.json
                           to _apex/state/, delete it; `ape project init` then adds
                           _apex/state/.gitignore)
  lift                     development/lift-disposition.md in a project whose v1 epoch is a
                           lift commit ("apex: lift …"): the disposition list awaits review
                           (lift.disposition-pending) until the person deletes the file;
                           its full lists, _apex/state/lift-disposition.json, are ignored
                           state and never asked for
  config                   since the v1 epoch, a commit that changed a guarded block of
                           _apex/config.yaml — limits, checks, retries, requires, timeouts,
                           caps, regression, commit (types included), evidence,
                           marketplaces, plugins, tools, human_review, release (D120),
                           secrets (D155) — and is not ape's (config.changed, D50) — a
                           person reverts it or accepts it: `ape project accept-config`
  hot/                     the project's secrets (D155): when hot/ exists, git ignores a new
                           file in it (hot/.gitignore "*", which init and the lift write, or
                           any ignore rule), and git tracks nothing under it — else
                           hot.not-ignored (action person: give it the .gitignore and
                           untrack the rest, `git rm -r --cached hot/`); no hot/ is no finding
  caps                     _apex/config.yaml caps: (D69) — sizes like 16KB, 1MiB or plain
                           bytes, KB = KiB = 1024 bytes; default / hard ceiling:
                             project_md   16 KB / 48 KB    agents_md    16 KB / 48 KB
                             change_md    16 KB / 48 KB    design_md    16 KB / 48 KB
                             spec         24 KB / 64 KB    (per capability)
                             evidence_log  1 MiB / 16 MiB  (per check, evidence: archive)
                             architecture_md 16 KB / 48 KB (development/architecture.md, optional)
                           A value above its ceiling is caps.over-ceiling (action person):
                           reported here, and change check, start and archive refuse
                           until a person fixes it (abandon measures nothing, D89)
                           change_md counts change.md without ## Notes (D89); the whole
                           change.md, Notes included, has the 48 KB ceiling whatever the
                           cap (change.over-ceiling, action apply)
  models                   each _apex/config.yaml models: key names a skill — one of the
                           defaults `ape project init` writes, a project or user skill
                           (.claude/skills/<key>/SKILL.md), or a skill tools: or
                           human_review names — else models.unknown-skill: the entry pins
                           nothing (models.effort is not a skill: a skill's effort goes
                           inside its entry, {model, effort}; the default for every
                           session is the top-level effort:); an entry is a model name or
                           {model, effort}, any other field is models.unknown-field (both
                           action person)
  docs                     each path _apex/config.yaml docs: names (changelog, user_docs)
                           exists (docs.missing: create it, or fix the path)
  .claude/settings.json    ape's hooks registered as `ape project init` writes them:
                           hooks.missing / hooks.altered (matcher, type, async, a
                           stop timeout below 1800 s); `ape project init` restores them;
                           and shared through git: settings.ignored when git ignores it
                           (and does not track it), since its hooks then reach no other
                           checkout — only `ape task` sessions get them inline
  .claude/skills/          shared through git too (D203): settings.ignored when git
                           ignores a skill folder there (asked of one no release ships),
                           since the framework's skills then reach no other checkout.
                           `ape project init` puts the .claude/ shape in the root
                           .gitignore (see its --help); an ignore elsewhere is removed
                           by hand
  provenance               since the v1 epoch (the newest "apex: project init" or "apex: lift"
                           commit; nothing before it is judged), one git log walk: a commit
                           touching development/specs/** must be an archive squash — a
                           "Change: <id>" trailer whose change is archived, whatever its
                           subject — else spec.edited; a change's state.json may change only
                           in an "apex: …" commit or an archive squash, else state.forged.
                           A git revert of such a commit clears both (both action person)
  plugins                  _apex/config.yaml marketplaces/plugins: settings.json's
                           extraKnownMarketplaces/enabledPlugins match them, and (when
                           claude is on PATH) each plugin is installed at project scope
                           at its ref (plugins.drift; plugins.config for a bad config);
                           `ape plugins sync` (or `ape doctor --fix`) puts them right;
                           an installed one that carries hooks or MCP servers its entry
                           does not allow (allow: [hooks, mcp]) is plugin.hooks-or-mcp
  tools                    _apex/config.yaml tools: {design: [{name, skill, when, args?,
                           model?, effort?}], review: [...]}: well-formed — args:
                           placeholders only {id} {task} {assets} {change}, effort: a
                           level — (tools.invalid), each skill
                           installed — <plugin>:<skill> of a listed plugin, or a project
                           or user skill — (tools.not-installed) and not
                           user-invocable: false (tools.not-invocable); a plugin skill
                           is judged only when claude is on PATH. See ape plugins --help

Notices (D155 Q73) are not findings: listed as "notice: <file>:<line>: …" lines on stderr
and under "notices" with --json ({severity: "notice", code, message, fix, action, file,
line}; [] when none, beside the findings on a failure too), they never change the exit.
  gate.outside-gates       an open (proposed or approved) milestone's gate runs a script
                           file outside development/gates/ (scripts/e2e.sh, say; a gate
                           that runs no script file, like make e2e, has none): apex-plan
                           writes gates into development/gates/<M>-<slug>/, and moving an
                           approved gate's script is gate drift, so it moves with the
                           milestone's next re-approval (before its approval, when
                           proposed). See ape milestone close --help for the gate folders
  hooks.pre-commit-absent  git's pre-commit hook (core.hooksPath honoured) does not run
                           ape's secret scan, so commits ape does not make (a session's
                           own git commit, a person's) go unscanned until the archive:
                           absent — a fresh clone; `ape project init` installs it —,
                           another tool's hook — add `ape hook pre-commit || exit 1` to
                           it; ape never edits it —, or not executable. ape doctor reports
                           it too (hooks.pre-commit). See ape hook pre-commit --help
  hooks.prompt-absent      .claude/settings.json does not register `ape hook user-prompt` for
                           UserPromptSubmit (or it is altered), so the person's prompts are
                           not recorded and a person act from a Claude Code session is
                           refused (E2); `ape project init` merges it back. ape doctor
                           reports it too (hooks.prompt). See ape hook pre-tool --help
  models.operator-row      _apex/config.yaml has a models.apex-orchestrator row (Q96):
                           with no models.apex-loop row it is read as apex-loop's (a
                           project made before rc.7: `ape project operator-interface`
                           renames it); beside one it is a leftover apex-loop's row
                           overrides (drop it). The operator's apex-orchestrator runs
                           in your own session and has no models row
  decision.standard-stale  ADR-0001, APEX's standard ADR (D205, D206), is absent, or is
                           framework-owned and not the installed standard (an earlier
                           text, or the apex-v0 governance standard an earlier lift
                           converted): `ape decision standard` writes it (the
                           framework's migration v1.0.0_seq-08 runs it, and the entry
                           of a release that changes the standard)
  decision.standard-superseded
                           an accepted ADR supersedes ADR-0001: the project opted out
                           of the standard, and updates leave ADR-0001 alone
  decision.standard-differs
                           ADR-0001 is the project's own decision, not the standard:
                           ape never replaces it; a person decides
  roadmap.next-inline      the roadmap still holds Next — a "## Next" section, or items
                           under apex-plan's "Next — not milestones:" label — while it
                           lives in development/next.md (D217): `ape next move` moves
                           it (the framework's migration v1.0.0_seq-09 runs it)
  spec.check-names-removed (action replan) a lasting spec scenario's check names what
                           `ape decision standard` removed (D232; see its --help), so it
                           can never pass: a change retires it with a REMOVED delta
                           citing v1.0.0_seq-08 — never a new supersede of ADR-0001

Exit codes: 0 green · 1 findings (listed on stderr, or under "findings" with --json) · 2 usage.

Examples:

```
  ape project check
  ape project check --json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |

## ape project codes

List the diagnostic codes v1 commands report in --json failures

```
ape project codes [flags]
```

List every diagnostic code an APEX v1 command (project, status, milestone, change,
decision) can report, with the exit status it comes with and what it means.

Every --json failure is {"ok": false, "exit": N, "error": "<one line>",
"diagnostics": [{"severity", "code", "message", "fix", "action", "file"?, "line"?}], ...}:
the first diagnostic is the failure itself, the rest its findings or conflicts. Branch on
"action" (D47): retry (run the same command again), apply (apex-apply: the work, or a
re-start), review (review again), replan (apex-change re-plans), stop (the run stops), person
(a person must act: list it, go on with other work). A code's action is the one listed here;
where its meaning names a case that changes it (milestone.changes-in-flight), the diagnostic
carries the action of the case (D49). "code" says which rule; "message" and
"fix" are for people. Codes are stable: a new rule gets a new
code, and a code's meaning never changes. severity is always "error" (no warning tier);
project check's notices, severity "notice", are no failure's diagnostics (exit 0 here).
Exit: 1 check failed · 2 usage · 3 conflict (nothing written).

Examples:

```
  ape project codes
  ape project codes --json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--json` | bool | `false` | machine output (JSON on stdout) |

## ape project init

Create the development/ skeleton and merge ape's hooks (never overwrites)

```
ape project init [flags]
```

Create the APEX v1 skeleton where it is absent. Nothing that exists is overwritten
(the exceptions: the @AGENTS.md line appended to a CLAUDE.md that lacks it, the
project-context line added to an AGENTS.md that lacks it, and the models: defaults
appended to an _apex/config.yaml that has no models: block).

A new project installs the framework's skills first, then runs init: `ape framework setup`,
then `ape project init` (each names the other in its next:, D162). init's next: asks
for setup while no .claude/skills/apex-* exists; JSON "next" carries the same line.

  _apex/config.yaml                  project_name, development_folder, and the models:
                                     defaults (D65, D93) — apex-apply, apex-loop
                                     and apex-release {model: sonnet, effort: high};
                                     apex-review, apex-plan, apex-change and apex-accept
                                     {model: opus, effort: medium}. No row for the
                                     operator's apex-orchestrator: it runs in your own
                                     session, on the model you chose (D175, D176). A
                                     models.apex-orchestrator row (a project made before
                                     rc.7) is read as apex-loop's while there is no
                                     apex-loop row, with a warning, until ape project
                                     operator-interface moves it (see its --help). An existing config
                                     with no top-level models: gets that block appended,
                                     its own text kept byte for byte (restored, with a
                                     warning, if the result would not parse); the
                                     amendment is committed with "apex: project init"
                                     only when the config was tracked and clean before,
                                     else left for you to commit. An existing models: is
                                     left alone: init warns (stderr; JSON "warnings") for
                                     each of those skills it has no entry for, which then
                                     runs on claude's default model (also read, never
                                     written: regression, effort, caps (see ape project
                                     check --help), timeouts, and limits —
                                     budget: enforce|off (default off; see ape task --help),
                                     attempts: N red verifies per start (default 5; see
                                     ape change verify --help); commit: {subject,
                                     hooks} for the archive's squash (no commit: line
                                     is written: the subject defaults to
                                     "{type}: {title}", D193), and evidence:
                                     archive|off (default off) — see ape change archive
                                     --help; notify: <command>, run on a limit stop or a
                                     blocked change — see ape task --help); see "Check
                                     settings" below
  AGENTS.md                          the project map (skeleton). Right under its title:
                                     Project context: read @development/project.md before
                                     planning or changing anything.
                                     (the configured development folder; plain text, so
                                     Claude Code imports project.md into every session),
                                     then the line naming the operator's interface (A13):
                                     Talk to APEX: type `/apex-orchestrator` in Claude Code.
                                     (an existing AGENTS.md is not given it: ape project
                                     operator-interface adds it).
                                     An existing AGENTS.md whose lines carry no
                                     @development/project.md outside code gets that line
                                     after its first # heading (or at the top), and
                                     nothing else — re-run init on a project made before
                                     rc.4 to add it
  CLAUDE.md                          "@AGENTS.md"; an existing CLAUDE.md without that line
                                     gets it appended, and nothing else
  development/project.md             vision, users, scope, out of scope, constraints (skeleton);
                                     optional: stack or tools (either name, D118), notes
  development/roadmap.md             "# Roadmap", then the line pointing at Next:
                                     `Next — not milestones: [next.md](next.md)` (D217)
  development/next.md                Next (D217): "# Next" and a line saying these are
                                     findings to triage, not milestones — written only
                                     with a new roadmap; an existing roadmap keeps its
                                     Next until ape next move moves it
  development/specs/                 empty: no backfill — a spec is born from its first change
  development/changes/               the changes in flight
  development/archive/               archived and abandoned changes
  development/decisions/
  development/decisions/0001-record-architecture-decisions.md
                                     ADR-0001, APEX's standard ADR "Record architecture
                                     decisions" (D205), accepted: the installed payload's
                                     _apex/standards/adr-0001.md, else ape's own copy;
                                     recorded in decisions.json as accepted by "APEX
                                     standard (ape project init)" with standard:
                                     {framework_version, sha256} — the framework's own
                                     record, no person act. Kept when ADR-0001 is the
                                     standard; any other ADR-0001 is left as it is (JSON
                                     standard_adr.action differs; see ape decision
                                     standard)
  development/briefs/                each plan's input, dated and verbatim (YYYY-MM-DD-<slug>.md,
                                     written by apex-plan; never edited — see ape project check)
  hot/.gitignore                     "*" when hot/ has no .gitignore (an existing one is kept):
                                     hot/ holds the project's secrets, which gates and checks
                                     read at run time ("$(cat hot/<name>)"), never as
                                     literals. The "*" ignores the .gitignore too, so hot/
                                     stays this checkout's and is not in the init commit
                                     (see ape project check: hot.not-ignored)
  _apex/state/                       ape's own records (never edited by a model; never touched
                                     by ape framework update): milestones.json, decisions.json
                                     tracked; logs/, runs/, hooks/ ignored. Its .gitignore is a
                                     whitelist (*, !.gitignore, !milestones.json, !decisions.json)

Hooks merged into .claude/settings.json (existing keys and hooks are kept):
  SessionStart          ape hook session-start
  Stop, SubagentStop    ape hook stop          (timeout 1800 s: it runs the change's checks)
  PreToolUse            ape hook pre-tool      (matcher Edit|Write|MultiEdit|NotebookEdit|Bash:
                                                the edit guard, and the Bash guards:
                                                git tag writes, git push, pkill/killall,
                                                rebase/merge on a change branch)
  UserPromptSubmit      ape hook user-prompt   (records the person's own prompts, for
                                                person acts typed in Claude Code (E2))
A hook that was altered (another matcher, async, a shorter stop timeout) is replaced
by the one above; running init again changes nothing. ape task passes the same
hooks inline as well, except UserPromptSubmit, so its sessions have them even when
settings.json does not.

Git's pre-commit hook (Q89): in a git work tree, init installs <hooks dir>/pre-commit
(`git rev-parse --git-path hooks`: core.hooksPath when set, else .git/hooks) when no
pre-commit hook is there — a sh script marked "# ape: pre-commit secret scan" that runs
`ape hook pre-commit`, the commit secret scan, so a session's own git commit and a
person's are scanned as ape's commits are (see ape hook pre-commit --help). An existing
hook that is not ape's is kept, never edited: ape project check then lists the notice
hooks.pre-commit-absent with the line to add. The output's "git hook" line and JSON
"pre_commit" ({path, status: installed|present|kept, runs_ape}) say which happened. git
never versions .git/hooks, so a fresh clone has none: run init again, which installs it.

What .claude/ commits (D203): .claude/settings.json (ape's hooks) and .claude/skills/
(the framework's skills) are the team's and tracked; the rest of .claude/
(settings.local.json, your own agents) and CLAUDE.local.md are personal. init puts that
shape in the root .gitignore (created when absent):
  # APEX (ape): .claude/settings.json and .claude/skills/ are the team's; …
  .claude/*
  !.claude/settings.json
  !.claude/skills/
  CLAUDE.local.md
With no .claude rule the block is appended (only its missing lines when part of it is
there); an ignore of the whole folder (.claude, .claude/, /.claude, /.claude/, .claude/**,
/.claude/**) is rewritten in place to it, since git never reads a negation under an
ignored folder; your own .claude/<x> lines and every other line are kept as they are.
Idempotent. The output's ".gitignore:" line and JSON "gitignore" ({action:
added|rewritten|already, created, uncommitted, revealed}) say which; "revealed" lists
the files under .claude/ it un-ignored that init does not commit (a skill of your own:
commit it or ignore it by name). A settings.json it un-ignored is committed with the
hooks. An ignore elsewhere (a nested .gitignore, .git/info/exclude, core.excludesFile)
is not rewritten: ape project check reports it (settings.ignored).

Everything init wrote but hot/.gitignore and a hook under .git/ (and settings.json
when it merged hooks) is committed as "apex: project init", with the files it amended (CLAUDE.md, AGENTS.md, and the config
when it was tracked and clean) and the .gitignore when it was absent or tracked and
clean; other files that existed before are not touched and
not committed. JSON: "amended" lists the files init changed, "amended_uncommitted"
those it left out of the commit.

Plugins: when _apex/config.yaml lists marketplaces/plugins, init installs each
plugin at project scope through the claude plugin CLI (marketplace add, install,
or marketplace update + update when it is off its ref), writes
extraKnownMarketplaces and enabledPlugins into .claude/settings.json (other keys
kept) and commits that as "apex: plugins". ape framework update and ape doctor
--fix do the same; ape task installs a missing one before it spawns.

  marketplaces:
    team-tools: {source: github, repo: org/claude-plugins}   # or {source: git, url: …},
                                                             # {source: directory, path: …}
  plugins:
    - {name: nats-expert, marketplace: team-tools, ref: v1.4.0}

ref is the version the install must report (claude plugin list; a leading v is
ignored; a commit matches by prefix). Claude Code cannot pin a plugin version
from the consumer side: a marketplace serves one version of each plugin, so pin
by pointing the marketplace at a ref (github/git: ref: <branch|tag>). A plugin
the marketplace does not serve at ref fails with the command to run. An entry's
allow: [hooks, mcp] lets the plugin carry hooks or MCP servers; ape plugins sync
re-syncs at any time and ape plugins status reports them, and the tools: slot
(see ape plugins --help).

Check settings in _apex/config.yaml (read by start, verify, the gate and the hooks; a
value ape cannot use, or a key it does not know inside these blocks, makes the config
unparseable and every command refuses it):

  regression: make test              the project's own check command, at start/verify/gate, failing
                                     only when newly red; or in tiers:
  regression: {change: make test, milestone: make test-all}
                                     change: at start and verify (baseline-aware);
                                     milestone (else change): at the milestone gate
  timeouts: {check: 20m, regression: {idle: 10m, max: 1h}, gate: {idle: 3m}}
                                     per tier, a Go duration (its max) or {idle, max}. A
                                     command is killed (exit 124, reason idle|ceiling, its
                                     last output lines kept) when it makes no progress —
                                     no new output bytes and no CPU time used by its
                                     process group — for idle, or when it reaches max.
                                     Defaults: check idle 15m / max 2h, regression 30m /
                                     2h, gate 30m / 4h. Any positive duration (no floor);
                                     a scalar's idle is the default clamped to it; idle
                                     above max, or max above ape's hard ceiling (check 4h,
                                     regression 8h, gate 12h), is a config error. A
                                     command's --timeout flag is every tier's max (capped
                                     at the ceiling). A timed-out command is not retried
  retries: 1                         a red check runs again up to n times (0–5, default
                                     0); a pass on a retry is green and recorded flaky:
                                     true (JSON results[].flaky, tries; listed by ape
                                     status). Vacuous and unrun checks are not retried
  checks: {vacuous_output: ["no tests to run", …]}
                                     a check that exits 0 but whose output says it ran
                                     nothing is red, reason "vacuous" (exit -2). Default
                                     list: "no tests to run" (go test), "running 0 tests"
                                     (cargo), "Ran 0 tests" (unittest < 3.12), "0 passing"
                                     (mocha), "collected 0 items" (pytest with exit 5
                                     masked); [] turns it off. A phrase counts as a whole
                                     word, and not when a line without one starts like a
                                     line with one (go test ./... -run X that ran X in one
                                     package: "ok pkgA 0.4s" beside "ok pkgB … [no tests
                                     to run]" is not vacuous)
  requires: {RADIX_DSN: env, docker: cmd}
                                     a check that mentions $RADIX_DSN / ${RADIX_DSN (a
                                     comment counts: "go test ./store/... # needs
                                     $RADIX_DSN") while it is unset or empty, or runs
                                     docker as a command while it is not on PATH, is not
                                     run: reason "unrun" (exit -3), red, never green
  docs: {changelog: CHANGELOG.md, user_docs: docs/, tested: [docs/examples/]}
                                     all optional, project-relative: the changelog and
                                     user docs a user-visible change updates (apex-change
                                     adds the task, apex-review checks it); ape project
                                     check reports a missing path (docs.missing); ape
                                     status --json carries the block as "docs". tested
                                     (a list, each path once) names the docs the project
                                     executes (doc-tests, tested examples): a change
                                     touching one is never docs-only, so ape change
                                     verify runs its regression command (see its help)

Scope (D45): v1.0: one operator per checkout, on a local trunk; PR mode comes later. Init
says so (JSON "scope"); change start and archive refuse while the base branch is behind its
upstream (base.behind-upstream).

--brownfield adds nothing; it records in _apex/state/project.json (ignored) that the
project existed before APEX.

Exit codes: 0 ok · 1 failed (e.g. settings.json is not valid JSON) · 2 usage.

Examples:

```
  ape project init
  ape project init --brownfield --json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--brownfield` | bool | `false` | record that the project existed before APEX |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |

## ape project lift

Lift an APEX v0 project to v1 in one commit (the mechanical half of the migration)

```
ape project lift [flags]
```

Lift an APEX v0 project (framework v0.15.0–v0.31.x) to the v1 layout, in ONE commit
whose subject starts "apex: lift" — the v1 epoch (once the regression command has run,
a second commit records its verdict, below). The judgment half (which rule lives
where, what M1 delivers) is apex-plan's, from the disposition file this writes.

Before: a person tags the commit to lift and pushes the tag. The tag is the archive of
everything the lift deletes; v0 files stay readable as `git show apex-v0-final:<path>`.
The lift checks the tag on the current branch's upstream, so the branch needs one
(D162-5): `git push -u origin <branch>` once, if `git rev-parse @{u}` fails.

  git push -u origin "$(git branch --show-current)"   # only when the branch has no upstream
  git tag -a apex-v0-final -m "apex-v0 final" && git push origin apex-v0-final
  ape project lift --dry-run        # the whole plan, nothing written
  ape project lift

Which ape: the lift runs on the binary invoked, whatever the project's .bingo/ape.mod
pins — it is exempt from the pin swap, since a v0 pin names an ape with no v1 commands.
The output's first lines say which binary runs (version, path) and what becomes of the
pin: when this ape is a release (vX.Y.Z or vX.Y.Z-rc.N) and bingo is on PATH, the lift
commit moves the pin to it (bingo get); otherwise the lift prints the one command that
does, and until the pin moves every other v1 command there needs APE_TOOLCHAIN=local —
every git commit too (the pre-commit hook runs ape) and every Claude session in the
project (its hooks run ape: start it as APE_TOOLCHAIN=local claude).

Pre-flight (refused, exit 1): a v0 install (_apex/framework.yaml records a v0 version,
or did in git history) and no v1 layout or earlier lift (lift.not-v0); a version in
v0.15.0–v0.31.x and a resolvable _apex/config.yaml (lift.unsupported-layout); a clean
tree (lift.dirty); apex-v0-final exists (lift.no-tag), is HEAD (lift.tag-not-head),
and is on the current branch's upstream remote at the same commit, checked with
git ls-remote — the one network call (lift.tag-not-pushed; lift.remote-unreachable);
and git ignores nothing the v1 project commits (lift.claude-ignored) — above all
.claude/settings.json, where the v1 hooks go, and .claude/skills/, where the framework's
skills go — judged with the root .gitignore as the lift's commit leaves it (the .claude/
shape below, so a whole-folder .claude ignore there is no refusal); what is refused is an
ignore ape does not rewrite (a nested .gitignore, .git/info/exclude, core.excludesFile, a
later line of the root .gitignore), named with its rule: remove it, commit, and re-tag.
--dry-run lists every problem, not the first, and writes nothing.

The commit:
  deletes        the v0 families, at the paths _apex/config.yaml's folder variables
                 resolve to: the skills the v0 install manifest lists (only those —
                 never a project's own; ones git never tracked, under an ignored
                 .claude/, leave the working tree after the commit), the v0 _apex
                 tables and pipelines, stories, the sprint tracker, retrospectives, planning (PRD, architecture,
                 epics, proposals, release records), ADR and pattern records and
                 indexes, capability and feature registries, deferred records, team
                 memory, project-context. Kept: docs, CHANGELOG, evidence, research,
                 code, _apex/framework.yaml (the migration ledger)
  writes         ADR-0001, APEX's standard ADR "Record architecture decisions"
                 (D205): development/decisions/0001-record-architecture-decisions.md,
                 accepted, the installed payload's _apex/standards/adr-0001.md (else
                 ape's own copy), recorded in decisions.json by "APEX standard (ape
                 project lift)" with standard: {framework_version, sha256}. The
                 apex-v0 governance standard ("Use Markdown Architectural Decision
                 Records") is identified by its canon source — a record whose
                 output_document is governance/adrs/adr-NNNN_use-markdown-adrs.md, or
                 that canon record copied from the governance clone — not by its
                 number: it is not converted (its text stays at apex-v0-final), its
                 citations go to ADR-0001, and the disposition and the commit name it
                 as replaced. A project with no such record gets the standard all the
                 same. JSON: standard_adr {action: created|replaced, path, source,
                 framework_version, replaces}
  converts       every other ADR (accepted, superseded, proposed) into
                 development/decisions/NNNN-<slug>.md, renumbered from 0002 in the
                 original order, with Status, Date, Supersedes and a "Lifted from:"
                 line. Nothing is invented: no date → git's first date of the file; no
                 Enforced by → "review" (what ape decision new writes); a missing
                 Context/Decision/Consequences → the section, marked "not recorded";
                 each is listed as a gap. Accepted and superseded ones are recorded
                 in _apex/state/decisions.json (by: lift), so their text is held from
                 here on. Canon ADRs the project cites and has no record of are copied
                 from the governance clone when _apex/config.yaml resolves one
  rewrites       every ADR citation (ADR-NNNN, and paths to the old records) in
                 Markdown and in code comments to the new number. Only that
                 four-digit form is the project's own: a short ADR-08 cites
                 another decision log, and is listed as unresolved, never
                 rewritten — even where the project has an ADR-0008
  strips         epic, story and deferred ids from code comments — Story 3.2, Epic 12,
                 (3-2) (a known story key), DW-…, paths into the deferred store or the
                 implementation folder — keeping the rest of the comment; a comment
                 line that was only an id goes, and an edited list item keeps its
                 marker ("- ", "2. "). A story takes its fragments with it (Story 3.2,
                 Task 2 · Subtask 3.1 of Story 5.10 · AC 2) and nothing else: what
                 follows them is kept, ADR ids renumbered
                 ("(Story 3.2, ADR-0019 decision 3)" → "(ADR-0019 decision 3)",
                 "(Story 3.2, 2026-08-08)" → "(2026-08-08)"), and the separator a
                 removal leaves by a bracket or a mark goes too
                 ("(ADR-0019, Story 3.2)" → "(ADR-0019)"); a "," ";" or ":" ending a
                 line goes only when the id was right before it ("proves," and "bump:"
                 stay). Comments only: ids in identifiers, strings and directives are
                 listed, not edited — and so are ids in a data-shaped comment, which a
                 tool may parse: one opening with a lowercase key ("# owner: …") or a
                 record of key: value fields ("# pending: q | owner: …"), and so is a
                 comment using an id or a removed path as a possessive
                 ("Story 3.2's own test", "(Story 2.3)'s own",
                 "development/deferred/'s Gates"), which no removal keeps whole. An
                 aside split over comment lines loses both brackets with its id, and a
                 mark a removal leaves at the start of a line moves to the end of the
                 line above ("(ADR-0008," over "Story 1.3), extracted" → "(ADR-0008)"
                 over "extracted"; a separator goes): no edited line starts with ")"
                 or "." or loses the blank after its "//". Only a bracket pair the
                 removal emptied goes: run()'s, []byte stay.
                 Tokenized: Go (go/scanner), shell, Python, JS/TS (JSX/TSX: comments
                 that begin their line only), Dart, YAML, Makefile (recipe lines are
                 the shell's: left), C/C++. A file the tokenizer cannot read, a
                 generated file, or an --exclude match is left and listed. Go: the
                 token stream without comments is checked unchanged; other languages:
                 the text outside comments, re-tokenized, is checked unchanged (blank
                 lines and trailing whitespace aside) — a file failing its check is
                 left unedited and listed. An edited Go file that was gofmt-clean at
                 the tag is formatted with go/format (an id's removal can leave a
                 line gofmt reads as a heading or a code block); one that then does
                 not format is left unedited and listed, and one that was not clean
                 at the tag is edited, not reformatted, and listed
  carries        ## Code Validation (project-context.md) into AGENTS.md ## Commands,
                 and the runnable ones, joined with &&, into regression: in
                 _apex/config.yaml (rewritten to project_name, development_folder,
                 regression). A command with a placeholder — <…>, {…} (not a
                 shell ${…}) or a lone … — is listed, not run; Go's ./... is no
                 placeholder. A trailing " # …" shell comment is dropped first.
                 A code block whose lines hold a construct (for … done) is one
                 command, ( set -e ⏎ its lines ⏎ ), so it fails on its first
                 red line wherever it runs as written; its AGENTS.md bullet
                 reads "The Code Validation block, as one command:". A block
                 line that is exactly "source <file>" is written ". <file>"
                 (RN-1: ape runs checks with sh, which has no source where it
                 is dash), each listed in gates.rewrites, the .md and the output
  lays down      the v1 skeleton, as `ape project init --brownfield` (CLAUDE.md keeps its
                 content, loses the v0 apex:managed block and gains @AGENTS.md;
                 AGENTS.md gains "Talk to APEX: type `/apex-orchestrator` in Claude
                 Code." when it does not name /apex-orchestrator; models: names
                 apex-loop, never apex-orchestrator), hot/.gitignore (`*`, when hot/ has none) included: it ignores
                 itself, so it is not in the lift commit; and git's pre-commit hook,
                 the secret scan, when the repository has none (an existing one is
                 kept; the output's "git hook" line says which; see ape hook
                 pre-commit --help)
  shapes         the root .gitignore's .claude/ rules (D203), as ape project init does:
                 .claude/*, !.claude/settings.json, !.claude/skills/, CLAUDE.local.md —
                 a whole-folder .claude ignore (pdfa's .claude/) rewritten in place, else
                 the block appended; every other line kept. settings.json and skills/ are
                 the team's, the rest of .claude/ personal. The output's ".gitignore:" line
                 and JSON "gitignore" ({action: added|rewritten|already, revealed}) say
                 which; "revealed" lists the files under .claude/ it un-ignored and did not
                 commit (a skill of your own: commit it or ignore it by name)
  writes         development/lift-disposition.md (16 KB, unless its candidates need
                 more; they are never cut, whatever its size): the binding-rule
                 candidates, EVERY one, under a line giving their count and the
                 file's size ("N rows, numbered 1–N … read on with offset/limit
                 until row N"): from project-context.md, every section, an
                 overview too — its list items of three words or more (not one
                 that is only links) and its rule-shaped paragraphs (must,
                 never, always, do not, avoid, only, required, prefer, shall,
                 forbidden), whole, and the notes in ## Code Validation's code
                 blocks (a command's # comment, with its command, or a run of
                 comment lines), whose commands go to AGENTS.md ## Commands.
                 CLAUDE.md (outside the apex-v0 managed block) and team-memory
                 are read whole: every list item, paragraph and table body row
                 of three words or more, whatever its wording, and every
                 code-block note (a fenced markdown example's lines are not); a
                 team-memory entry that gives nothing else is listed by its
                 title. An HTML comment is text: indented under a list item it
                 continues the item, and a comment of several lines elsewhere
                 is a paragraph. Each CLAUDE.md import the lift drops — the
                 managed block's @_apex/apex-operating-rules.md, an @ import of
                 any other file it deletes — is one row naming the file, its
                 size, and git show apex-v0-final:<path>; the operating rules' row
                 also names the two carried already: tags and pushes by ape hook
                 pre-tool, zsh quoting by ape hook session-start. Grouped by source and section (JSON
                 rule_candidates[].section, the heading path); text found twice is
                 listed once with both sources (also_in). An empty Disposition
                 column for apex-plan, then a fresh read-only review, then the
                 person, who deletes it. Then "Code and
                 tests that read deleted v0 state": every kept code, test, script,
                 Makefile or CI file (not docs, not evidence) that names a path the
                 lift deletes outside its comments (SQL's -- and /* */ too) — by its
                 path (./ and ../ too), a path under a deleted folder at a segment
                 boundary, or the base name of a deleted file when it has an extension
                 and no other file has it and its line reads it (cat, test -f, a
                 redirection, an open/read/glob/Join call, a quoted path). Neither
                 counts in an echo's or a printf's words or a test's or logger's
                 message (t.Errorf, fmt.Errorf, log.*, console.*, print(, a testify
                 message), a message string a call opens on one line and carries on
                 the next included, unless a reading call or command on its line
                 takes it — one line per file with
                 what it reads (--json and the report: v0_state_readers, {file,
                 reads}); re-point each to v1 state or retire it. Then, as summaries:
                 other code and CI that still names removed paths (in a comment, or a
                 file that is not code), one line per file with its count and first
                 reference, and edited files a guard hashes — those a lock file or a
                 hasher names one line per file (at most 50), those only a folder ties
                 to a hasher one line per hasher ("57 files in pkg/x share a folder
                 with guard_test.go (folder match only)"); ADR gaps; ids left and
                 files not edited, counted per file; references in evidence and docs,
                 counted; readings at the tag. To fit 16 KB the summaries shrink to
                 counts, then the ADR table to the ADRs with gaps; the v0 state
                 readers grow the file past 16 KB, up to 48 KB, and past that go to
                 counts (saying so). The candidates are never cut: the file is as
                 big as they need (cortex: 146 KB, 441 rows), since the JSON below
                 is not in any other clone. Every non-rule list whole goes to
                 _apex/state/lift-disposition.json after the commit: ignored by the
                 state folder's whitelist, never committed, in this checkout only

A byte-identity perimeter: a file a test, CI script or lock file hashes goes red once
the lift edits a comment in it. The plan lists, as "hashed", the edited files that a lock
file names, that a test or CI file names within 3 lines of a hash word or call (sha256,
sha256sum, checksum, fingerprint, byte-identical, crypto/sha3, hashlib, …), or that sit
beside a hashing test that globs or names them — run --dry-run, then --exclude them. A
test or CI file hashes only when a hash word or call stands outside its comments: one
that says "byte-identical" in prose hashes nothing. Documents and evidence (.md, .json,
.log, .txt, anything under an evidence folder) never count as hashers. The order is
how sure the match is: what a lock file names, then what a hasher names (by path, or a
test beside the file by its name), then what only its folder ties to a hasher (a glob,
a ReadDir). --dry-run lists every one, text and --json; a real lift's output lists at
most 50, and the disposition file at most 50 of the first two kinds and the folder kind
as one line per hasher; edited_but_hashed_total counts them all, and the report has
every one. It is a heuristic: a copy compared byte for byte with no hash (an
embedded mirror of a hashed file) needs its own --exclude.

The regression command runs twice (--no-verify-run skips both): at the tag, before
anything is written, and at the lift commit — the baseline red gates are judged against.
regression_verdict says whose a red is: green · red-at-tag (red there too, failing as it
did: pre-existing, for apex-plan to roadmap) · red-at-tag-new-failures (red there too, but
tests fail that did not at the tag — the LIFT broke those, named in
regression_new_failures) · red-after-lift (green at the tag: the LIFT broke it — fix it
in a commit, or reset to the tag and lift with --exclude) · cannot-run (exit 126/127, or
an output saying a command was not found — "command not found", "No such file or
directory" — with make's "Error 127", and no failing test named: a tool it runs is
missing here, which is not a red; read the same at both points; when the output says a
bash/zsh builtin was not found — "sh: 2: source: not found" — the note names it instead:
/bin/sh here has no such builtin, so write it in POSIX sh, "." for source, RN-1) · red-no-baseline ·
flaky (RG-1: a red-after-lift or red-at-tag-new-failures command is run once more at the
lift commit, and passed that time: a flake, not the lift's; record it, plan no fix; a
red-at-tag-new-failures command whose re-run fails only as at the tag is red-at-tag, its
note naming the new failures that did not reproduce (RV-1); red again keeps its verdict,
its note saying so; regression_commands[] / regression.commands[] carry the re-run's
exit_rerun and log_rerun). A run
that exits 0 but whose output names failing tests (as below) is red for the verdict, by
those names, and its note says "exit 0, but the output names N failing tests": green at
the tag, it is red-after-lift with the names in regression_new_failures (a ( … ) block
without set -e passes on its last line's exit alone). A run red at the lift commit and
green at the tag lists the failing tests its output names in regression_new_failures
whatever its exit code (less any the tag's run named), at most 50. Red at both, the
failing tests are compared by name where the output names them (go test "--- FAIL:",
gotestsum "=== FAIL: <pkg> <test>" and "✖ <pkg>", pytest "FAILED", cargo "... FAILED",
jest "✕", flutter and dart test "<name> [E]", the counters before the name dropped);
otherwise by output hash — tool-install lines ("(re)installing …", as bingo
prints on a first run) and durations (0.12s, 4m19s, (12ms)) left out — and a changed
output stays red-at-tag with the note "the output changed after the lift", never called
pre-existing. A command that is a chain joined with && at its top level (outside quotes,
( ), { }, $( ) and backticks; ;, || and pipes stay inside a command) runs command by
command at both points, past a red one, so a red lint cannot hide the tests after it:
each command is judged on its own, regression_verdict and the regression_* fields are
the worst one's, and --json lists every command in regression_commands (cmd, verdict,
note, new_failures, regression, regression_at_tag; logs regression-<n>.log and
regression-<n>-at-tag.log). From a command that changes the shell's state (cd, export,
source, NAME=value, …) on, the chain stays one command. A ( … ) block of several lines
that splits (each line a simple command, a first set -e line dropped, as a change's verify splits it) is, for these
two runs, one command per line in that chain, not its lines joined by &&: each line runs
past a red one and is judged on its own in regression_commands, and its log opens with
"split into N commands: the lift runs each line of the ( … ) block on its own"; a block
that does not split (a line that changes the shell's state, a construct left open) stays
one command, run as written. The exit code stays 0: the
commit stands either way, and the verdict is in --json and the output.

The verdict is also recorded where apex-plan reads it (RG-6): `regression` in
_apex/state/lift-disposition.json — verdict, note, regression_new_failures, commit, and
commands (cmd, verdict, note, exit_at_tag, exit_after, regression_new_failures, log,
log_at_tag), one per command run — and a "## Regression" section of the disposition
file (the verdict and its note, then one line per command with its exits and logs),
committed on its own after the lift commit as "apex: lift regression verdict"
(regression_commit in --json). With no run (no command, --no-verify-run) the lift
commit's disposition file says why, and the JSON has the note with an empty verdict.

--from-update is how the framework's v1.0.0 migration entry runs it inside
`ape framework update`: uncommitted changes under .claude/skills/, _apex/ (not
config.yaml or state/), .aboard/ and .gitignore are the install's, left out of the lift
commit for the update's own; skills the v1 install placed are never deleted. An untracked
.claude/settings.json (the update's .claude/ shape un-ignored it) is no refusal: the lift
merges the v1 hooks into it and commits it. The regression verdict lines go to stderr,
which the update shows (its stdout is not).

Exit codes: 0 lifted (or planned) · 1 refused or failed (code in --json) · 2 usage.

Examples:

```
  ape project lift --dry-run
  ape project lift --json
  ape project lift --exclude 'schema/core/**' --exclude deploy/init/00-bootstrap.sh
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--dry-run` | bool | `false` | print the whole plan; write nothing (pre-flight problems are reported, not refused) |
| `--exclude` | stringArray | `[]` | a glob (** allowed) of files the lift must not edit, e.g. a byte-identity perimeter; repeatable |
| `--from-update` | bool | `false` | run inside ape framework update (the v1.0.0 migration entry): the install's uncommitted files are left to it |
| `--json` | bool | `false` | machine output (JSON on stdout) |
| `--no-verify-run` | bool | `false` | do not run the regression command (at the tag, and after the commit) |
| `--timeout` | duration | `0s` | ceiling for each command run, overriding _apex/config.yaml timeouts.<tier>.max (default: check 2h, regression 2h, gate 4h; capped at ape's hard ceilings 4h/8h/12h); the tier's idle window still applies; exit 124 when exceeded |

## ape project operator-interface

Bring a project made before rc.7 to the operator interface (the v1.0.0_seq-07 migration)

```
ape project operator-interface [--check] [flags]
```

Since rc.7 (D171) apex-orchestrator is the operator's interface — a skill a person
opens in their own Claude Code session by typing /apex-orchestrator — and the unattended
runner of one approved milestone or tweak is apex-loop. This brings a project that ape
project init or the lift made before rc.7 there; the framework's migration
v1.0.0_seq-07 runs it (command: ape project operator-interface; check: ape project
operator-interface --check). A project init or the lift made with rc.7b has nothing to do.

One commit, "apex: operator interface (seq-07)", of what is left of:
  models     _apex/config.yaml's models.apex-orchestrator row renamed to apex-loop, its
             value kept byte for byte (D176: sonnet/high unless the project chose
             otherwise); when a models.apex-loop row exists it wins and the old row's
             lines are removed. Every other byte of the file is kept. models: is not a
             guarded block, so this is no config.changed. A models: written as a flow
             mapping ({…}) whose old row must be removed is a person's to edit
             (project.operator-interface-pending, nothing written)
  hooks      ape's hooks merged into .claude/settings.json with ape project init's
             merge: a missing one added, an altered one restored, every other key and
             hook kept (hooks were only merged by init before)
  AGENTS.md  the line
               Talk to APEX: type `/apex-orchestrator` in Claude Code.
             after the project-context line (else after its first # heading, else at
             the top), when no line outside code names /apex-orchestrator (A11)
  .gitignore the .claude/ shape (D203, Q114): .claude/*, !.claude/settings.json,
             !.claude/skills/ and CLAUDE.local.md, as ape project init writes it — appended
             (the file created when absent), or a whole-folder .claude ignore rewritten in
             place; every other line kept. Files under .claude/ it un-ignores that are not
             settings.json are listed ("revealed"), not committed
Idempotent: with nothing left it writes nothing and says "nothing to do" (exit 0).

How ape knows seq-07 ran: from the config, not a marker (a fresh clone would lose one). A
models.apex-orchestrator row with no models.apex-loop row is the state before seq-07:
`ape task apex-loop` reads it as apex-loop's, with a warning on stderr, and ape project
check lists the notice models.operator-row. Beside an apex-loop row it is a leftover,
the notice says to drop it, and apex-loop's row is the one read.

Refused on a change branch (branch.change-branch), outside a git repository
(git.not-a-repo), and while _apex/config.yaml, .claude/settings.json or AGENTS.md (or
the .gitignore, when it has the shape to take) has uncommitted changes or is untracked
(tree.dirty): nothing is written. A command any session may run (not
person-only).

--check writes nothing: exit 0 when nothing is left, 1
(project.operator-interface-pending, action person, naming what is left) when something
is — the migration's check: 0 applied, 1 not applied.

JSON: {"models": "moved"|"removed" (absent when there is no old row), "hooks": [<Event>:
<command>, …], "agents_line": true|false, "gitignore": "added"|"rewritten" (absent when
the shape is there), "revealed": [files it un-ignored, not committed], "commit"}; "hooks"
is always there, [] when none. Under --check they say what is left, at the top level beside the failure's
ok/exit/diagnostics when something is (exit 1).

Exit codes: 0 applied, or nothing to do (--check: nothing left) · 1 refused, or (--check)
something left.

Examples:

```
  ape project operator-interface --check
  ape project operator-interface
  ape project operator-interface --json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--check` | bool | `false` | write nothing: exit 0 when nothing is left to do, 1 when something is |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |

## ape prompt

Drive an unattended Claude session from a prompt or a handoff file

```
ape prompt [text] [flags]
```

Run one unattended Claude Code session end-to-end: spawn claude in
an in-process PTY, deliver a prompt (or seed the session from a handoff
document), let it work under the ape bridge's hook supervision, detect
completion via the Stop hook, capture the transcript + per-model
telemetry, and exit with a meaningful status.

Exactly one of the positional <text> or --handoff <file> must be given.

  ape prompt "add a CHANGELOG entry for the latest release"
  ape prompt --handoff development/handoffs/2026-07-13-resume.md
  ape prompt "refactor the parser" --workflow
  ape prompt "big refactor" --ultracode --model "opus[1m]"

Prompt assembly:
  --agent A        the delivered line is "/A --autonomous -- <prompt>"
                   (no agent: the prompt is sent as a plain message).
  --handoff F      the prompt becomes "Read the handoff document at
                   <abs F> and continue the work it describes."
  --ultracode      prepends the "ultracode" keyword to the prompt
                   (session runs workflows by default).
  --workflow       appends an explicit "run this via a workflow"
                   directive. Independent of --ultracode; both compose.

With --agent the line is a slash command, and Claude Code turns a typed
burst past 800 characters into a "[Pasted text #N]" placeholder that
never runs as one. So past 700 characters the prompt is written,
verbatim, to prompt.md in the record directory, and the typed line
names that file (prompt.yaml records it as args_file). A plain prompt is
typed as is, whatever its length.

A prompt that would not arrive as written when typed goes to prompt.md
the same way, with or without --agent, whatever its length: one holding
two or more spaces in a row, a tab, a line break (a newline typed
mid-text submits the line early) or any other control character.
Single-spaced text on one line is typed exactly as before.

After typing, ape presses Enter once claude has drawn the line, then
confirms the submit through the UserPromptSubmit hook, pressing Enter up
to twice more; a line still not submitted fails the session (exit 1).

In an APEX v1 project whose .claude/settings.json registers the stop hook
(ape hook stop), the hook decides each Stop, as under ape task: a Stop it
blocks (background work still running, or a red check on an in-progress
change's branch) continues the turn instead of ending the session, which
completes on the first Stop the hook allows, or at --idle-timeout /
--max-duration. prompt.yaml records each verdict under stop_gate. With
--ignore-project-settings the hook does not run, and the first Stop ends
the session.

Records land under <project>/_output/ape/prompts/<prompt-id>/ (in an
APEX v1 project, _apex/state/runs/prompts/<prompt-id>/) (runlog
streams + copied transcript + prompt.yaml session record) and fold into
the project cost rollup's Prompts bucket.

ape prompt must run from a project root (a directory with
_apex/config.yaml). It makes no commits of its own.

A prompt session is a model session under the nesting rule (D62; see ape
task --help): it starts only outside every ape session (depth 0), and is
refused inside one (exit 2, session.depth — or session.not-allowed from an
apex-loop run, which starts only allow-listed skills with ape task).
--agent apex-loop is refused (it runs as ape task apex-loop), and so is
--agent apex-orchestrator (the operator's interface opens with
/apex-orchestrator in a person's own Claude Code session).

Exit codes: 0 session completed (Stop hook) · 1 idle-timeout, session
failed, or the typed line never submitted · 2 usage or preflight error (no _apex/config.yaml, unresolved
--agent, missing --handoff file, the nesting rule) · 3 the claude REPL never became ready
· 4 claude exited before the Stop hook · 5 an upstream API failure ended the session.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--agent` | string | `—` | Framework agent fronting the session: /<agent> --autonomous -- <prompt> |
| `--cwd` | string | `—` | Project root directory (default: current working dir) |
| `--effort` | string | `—` | Explicit reasoning effort for the session and its sub-agents (low\|medium\|high\|xhigh\|max). Unset: the project's _apex/effort-defaults.yaml per model, or xhigh without one (see ape config effort). |
| `--handoff` | string | `—` | Handoff document to seed the session with (mutually exclusive with the positional prompt) |
| `--idle-timeout` | duration | `0s` | Idle backstop: end the session only after this long with no progress across hooks, transcript growth, or PTY output (e.g. 15m); default 60m |
| `--ignore-project-settings` | bool | `false` | Tell the spawned claude to skip project + local .claude/settings*.json |
| `--max-duration` | duration | `0s` | Wall-clock ceiling regardless of progress (e.g. 8h); off by default (D159: a run showing progress stops only on the idle timeout, ape's limits or a person). The clock resets on each sub-agent boundary, so a sequential batch skill is bounded per item. |
| `--model` | string | `—` | Claude model. A bare family (fable, haiku, mythos, opus, sonnet) resolves to its current generation; sonnet-5 / claude-sonnet-5 / opus[1m] pin explicitly |
| `--output-format` | string | `human` | Output format: human\|json\|yaml (json/yaml = result envelope on stdout, progress on stderr) |
| `--output-style` | string | `—` | Output style pinned on the spawned session (default "Default"). Pass "inherit" to keep whatever style the machine is configured with. |
| `--quiet` | bool | `false` | Suppress the progress stream on stderr |
| `--ultracode` | bool | `false` | Prepend the ultracode keyword (session runs workflows by default) |
| `--workflow` | bool | `false` | Append a directive to run the task through a Claude Code workflow |

## ape rollback

Rollback ape to the previous version

```
ape rollback
```

Restore the backup binary created during the last update.

## ape run

Wait for, or inspect, a run started with --detach

```
ape run
```

A run started with ape task --detach returns an id at once and
continues in the background, orphaned from the shell that started it, so
a tool's time limit on that shell does not reach it.

The ape change and ape milestone verbs that take --wait detach on their
own and have no id here: run the same command again to attach (see their
--help, "Detached runs").

  ape run wait <id>     wait for it; exit with the run's own exit code
                        (75: --wait/--timeout expired, wait again; 76:
                        its supervisor is gone without recording an exit)
  ape run status <id>   show its handle: command, pids, stdout and log
                        files, and the exit code once it has ended
  ape run stop <id>     stop it (gracefully on Linux/macOS, hard on
                        Windows) and wait for its exit to be recorded

Subcommands:

- `status` — Show a detached run's handle
- `stop` — Stop a detached run and wait for its exit to be recorded
- `wait` — Wait for a detached run and exit with its exit code

## ape run status

Show a detached run's handle

```
ape run status <id> [flags]
```

Show a detached run's handle, with its state: running, "ended, exit N", or
"gone without recording an exit".

--json prints the object `ape run wait --json` does instead — id, state
(running, ended or lost), exit_code, log, stdout, run_id, run_dir, depth,
parent_run, stop_reason, and result (the session's last assistant message,
at most 2048 bytes; absent without a transcript) — and exits 0.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root directory (default: current working dir) |
| `--json` | bool | `false` | Print the run as `ape run wait --json` does (id, state, exit_code, …, result) |

## ape run stop

Stop a detached run and wait for its exit to be recorded

```
ape run stop <id> [flags]
```

Stop a run started with --detach, then wait (up to --timeout) for its exit to
be recorded and report it. The supervisor ends itself once the run it
supervises ends: it records the exit, then exits with that code.

On Linux and macOS the stop is graceful: the supervisor forwards SIGTERM, and
the run shuts down as on Ctrl-C — ape change saves its residue, and the
commands the skill started are stopped. On Windows it is a HARD stop: there
is no SIGTERM to forward, so the run's own process is terminated; no residue
is saved and the skill's commands are not stopped.

Only this run's own process is ever signalled. A pid is reused once its
process is gone, so ape first checks the process is still this run's (Unix:
its "ape run supervise --handle …/<id>.json" command line; Windows: an ape
process created after the handle). Anything else is refused, exit 76, and
nothing is signalled.

Exits 0 once the run has ended, whatever its own exit code (ape run wait
reports that); 75 if it has not ended within --timeout; 76 if its supervisor
is gone or the pid is no longer the run's; 0 at once if it had already ended.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root directory (default: current working dir) |
| `--timeout` | duration | `30s` | How long to wait for the run to end after asking it to stop |

## ape run wait

Wait for a detached run and exit with its exit code

```
ape run wait <id> [flags]
```

Wait for a run started with --detach, print where its log is and how it
ended, and exit with the run's own exit code.

--wait (or its older name --timeout) bounds the wait. When it expires with
the run still going, the command exits 75 and the run is untouched: wait
again. That is how a caller under a tool time limit waits for a run longer
than the limit — each wait stays inside it. A loop run dispatches a
skill as `ape task <skill> --args <id> --detach` and then
`ape run wait <id> --wait 9m` until it stops exiting 75 (D61).

Exit 76 means the run's supervisor is gone without recording an exit
(killed, or the machine restarted), so ape cannot report how the run ended
— it does NOT mean the run did nothing: the ape commands its session ran may
have committed state, so read `ape status` before starting it again. A
code of its own, so it is never mistaken for a run that exited 1.

The run's stdout is in its own file (the handle's "stdout"), apart from
stderr (its "log"), so a --output-format json envelope reads clean.

--json prints one object instead of the lines: id, state (running, ended or
lost), exit_code (null until it ends), log, stdout, run_id and run_dir (the
run's own record, once it has one), depth and parent_run (the nesting rule:
2 and the loop's run for a session a loop run started),
stop_reason ("parent_gone" when ape stopped it because that loop run
ended), and result: the session's last assistant message, read from its
transcript, at most 2048 bytes (result_truncated: true when cut). result is
absent when there is no transcript to read. The exit codes are the same.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root directory (default: current working dir) |
| `--json` | bool | `false` | Print the run as one JSON object (id, state, exit_code, log, stdout, depth, parent_run, result, …) |
| `--timeout` | duration | `0s` | Alias of --wait |
| `--wait` | duration | `0s` | Stop waiting after this long (e.g. 9m) and exit 75 if the run is still going; 0 waits until it ends |

## ape sandbox

Provision and operate hardware-isolated Kata VM workspaces (via aped)

```
ape sandbox
```

Provision and operate long-lived, hardware-isolated Kata microVM
workspaces (own guest kernel, KVM) through a rootful aped daemon.

ape drives aped over embedded NATS using the ape.vmm.<node>.> contract; aped
provisions the microVM, composes the workspace home, mints a per-VM telemetry
credential, and owns the workspace registry. ape never runs as root.

  ape sandbox up <name>      Provision a workspace
  ape sandbox ls             List provisioned workspaces
  ape sandbox inspect <name> Show a workspace's live state
  ape sandbox capacity       Show the node's headroom (does another one fit?)
  ape sandbox costs [name]   What Claude sessions inside workspaces cost
  ape sandbox exec <name> -- <cmd>...   Run a command inside a workspace
  ape sandbox forward <name> <port>     Reach a port inside a workspace
  ape sandbox setup <name>     Materialize the project's declared toolchain
  ape sandbox stop <name>      Stop a workspace (free RAM, keep rootfs + state)
  ape sandbox start <name>     Start a stopped workspace
  ape sandbox freeze <name>    Freeze a workspace (cgroup-freeze; RAM resident)
  ape sandbox unfreeze <name>  Unfreeze a frozen workspace
  ape sandbox suspend <name>   Suspend a workspace microVM — not yet on Kata
  ape sandbox down <name>      Tear a workspace down
  ape sandbox framework …      Materialize the framework refs a node can mount
  ape sandbox credentials …    Publish your Claude credentials for workspaces
  ape sandbox egress set …     Change a running workspace's egress allowlist

Point ape at your aped node with APE_NATS_URL + APE_NATS_CREDS (the operator
credential aped mints at startup) and --node. Requires a running aped on a
Linux host with KVM + containerd + Kata.

Subcommands:

- `attach` — Open an interactive shell inside a workspace
- `capacity` — Show the node's workspace headroom (cores, memory, how many more fit)
- `costs` — Show what Claude sessions inside workspaces cost
- `credentials` — Publish your Claude credentials for workspaces to use
- `down` — Tear a workspace down
- `egress` — Inspect and change a workspace's egress allowlist
- `exec` — Run a command inside a workspace
- `forward` — Forward a local port to a port inside a workspace
- `framework` — Manage the APEX framework refs a sandbox node can mount
- `freeze` — Freeze a workspace (cgroup-freeze; guest RAM stays resident)
- `governance` — Manage the governance repo refs a sandbox node can mount
- `inspect` — Show a workspace's live state
- `ls` — List provisioned workspaces
- `setup` — Materialize the project's declared toolchain inside a workspace
- `ssh` — How to ssh into a workspace (use 'ape sandbox forward --ssh')
- `start` — Start a stopped workspace
- `stop` — Stop a workspace (free its RAM, keep its rootfs + state)
- `suspend` — Suspend a workspace microVM (save guest RAM to disk) — not yet supported on Kata
- `unfreeze` — Unfreeze a frozen workspace
- `up` — Provision a Kata workspace

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox attach

Open an interactive shell inside a workspace

```
ape sandbox attach <name>
```

Open an interactive shell inside a workspace, wiring your terminal's
stdin/stdout/stderr to the guest over the aped exec session subjects (PLAN-18 D2,
credit-based flow control; the terminal goes raw and resizes forward on SIGWINCH).

Requires an aped node running the containerd driver (aped run --driver
containerd); a shell-driver node reports the session UNSUPPORTED.

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox capacity

Show the node's workspace headroom (cores, memory, how many more fit)

```
ape sandbox capacity [flags]
```

Report what the target aped node can still take: its cores and memory, how
many workspaces it already carries, how many of those are RUNNING (only those
hold RAM — a stopped workspace keeps its state and frees its memory), and how
many more the free memory holds.

FITS is an estimate, and the number it divides by is printed next to it: guest
memory is set by the node's Kata configuration, which aped does not own, so the
per-workspace size is an assumption rather than a measurement. Disagree with it
by redoing the division on the memory numbers above it.

This is a report, not placement. Choosing which node a workspace lands on is a
fleet concern and is not what this answers.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox costs

Show what Claude sessions inside workspaces cost

```
ape sandbox costs [name] [flags]
```

Report the Claude usage accumulated inside the node's workspaces, per
workspace, all-time. With a name, report only that workspace.

The numbers come from the session transcripts each workspace's composed home
already holds on the node — there is no agent, no telemetry wire, and nothing
to enable. The node does the scan because it owns those homes: they are private
to the daemon, and an operator's ape cannot read them.

A model with no rate in the price table contributes $0 and is called out, so a
total that is a LOWER BOUND is never mistaken for an exact one. The table doing
the pricing is the NODE's, embedded in its aped at build time — so a gap is
fixed by upgrading aped there, not by 'ape costs update' here.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox credentials

Publish your Claude credentials for workspaces to use

```
ape sandbox credentials
```

Publish the host's Claude OAuth credential where the aped node can read it, so
workspaces get your Anthropic session instead of asking you to log in again.

aped-front runs as its own service user with ProtectHome=yes and cannot read
/home/<you>/.claude. Rather than widening your home, this command — which runs as
YOU — publishes the credential into a directory the daemon may read
(/srv/ape-credentials/<user>), and the node is pointed at it with
'aped front --host-home'.

  ape sandbox credentials publish     # hard link: one live credential, shared
  ape sandbox credentials publish --copy   # independent copy: isolated, diverges
  ape sandbox credentials status
  ape sandbox credentials watch      # re-publish automatically after a /login
  ape sandbox credentials revoke

Both modes give a workspace your Anthropic identity — that is what "use my
credentials" means. Use 'revoke' to take it back.

Subcommands:

- `publish` — Publish the host credential to the node's credential root
- `revoke` — Remove the published credential so workspaces stop getting it
- `status` — Show whether a published credential is present and still live
- `watch` — Re-publish automatically whenever your credential is replaced

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox credentials publish

Publish the host credential to the node's credential root

```
ape sandbox credentials publish [flags]
```

Publish ~/.claude/.credentials.json where aped can read it.

Default is a HARD LINK: the workspace and the host share one inode, so a token
refresh on either side is immediately valid on the other, and your file's
permissions are unchanged (the daemon only stat()s it; Kata's virtiofsd does the
I/O as root). --copy makes an independent copy instead, which isolates the
workspace but DIVERGES the first time either side refreshes, because OAuth refresh
tokens rotate.

Re-running is safe and is how you repair a link that decoupled — which happens if
the host rewrites the credential by replacing the file rather than editing it.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--copy` | bool | `false` | Publish an independent copy instead of a hard link (isolated, but diverges on token refresh) |
| `--root` | string | `—` | Credential root the node reads (default: $APE_CREDENTIAL_ROOT or /srv/ape-credentials) |
| `--source` | string | `—` | Credential file to publish (default: ~/.claude/.credentials.json) |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox credentials revoke

Remove the published credential so workspaces stop getting it

```
ape sandbox credentials revoke [flags]
```

Remove the published credential. With a hard link this only drops the extra
name — your ~/.claude/.credentials.json is untouched, since a file survives until
its last link is gone. With a copy it deletes the copy.

Workspaces already running keep the credential that was composed into them until
they are torn down.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--root` | string | `—` | Credential root the node reads |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox credentials status

Show whether a published credential is present and still live

```
ape sandbox credentials status [flags]
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--root` | string | `—` | Credential root the node reads |
| `--source` | string | `—` | Credential file to compare against (default: ~/.claude/.credentials.json) |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox credentials watch

Re-publish automatically whenever your credential is replaced

```
ape sandbox credentials watch [flags]
```

Watch your credential and re-publish it the moment it is replaced.

This closes the one gap in credential sharing that nothing on the node can close. A
`claude /login` REPLACES the credential file rather than editing it, so the published
hard link is left pointing at the old one — and aped cannot notice, because it runs as
another user with ProtectHome=yes and can never read your home. Until something
re-publishes, every workspace keeps using the pre-login token.

Any 'ape sandbox' command re-publishes as a side effect, so in normal use this is already
handled. Run this watcher when you want it handled with no command at all — as a user
service:

  install -D -m0644 deploy/user/ape-credentials-watch.service \
    ~/.config/systemd/user/ape-credentials-watch.service
  systemctl --user enable --now ape-credentials-watch
  sudo loginctl enable-linger $USER   # start at BOOT, not just at first login

It runs as YOU — that is the point: only your own session can read your home. It needs no
aped node, no running workspace, and no publication (with nothing published it idles), so
it is safe to enable before ever publishing.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--install-unit` | bool | `false` | Write that unit to ~/.config/systemd/user/ and exit (safer than redirecting --print-unit) |
| `--interval` | duration | `2s` | How often to check for a replacement |
| `--print-unit` | bool | `false` | Print a systemd --user unit for this watcher (with THIS binary's path) and exit |
| `--root` | string | `—` | Credential root the node reads |
| `--source` | string | `—` | Credential file to watch (default: ~/.claude/.credentials.json) |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox down

Tear a workspace down

```
ape sandbox down <name> [flags]
```

Destroy the workspace microVM and drop its aped registry entry. A
persistent volume (mount: volume) is retained unless --remove-volume is set.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--force` | bool | `false` | Force teardown |
| `--remove-volume` | bool | `false` | Also remove the persistent volume (mount: volume) |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox egress

Inspect and change a workspace's egress allowlist

```
ape sandbox egress
```

Change which domains a RUNNING workspace may reach, without recreating it.

The allowlist is enforced by a host-side CONNECT proxy on a fixed port, so re-pointing
it is a proxy restart on that same port — the workspace keeps running and its
HTTPS_PROXY stays valid. The request is intersected with the node's egress policy
exactly as at create time, so this can narrow or re-shape a grant but never exceed
what the node permits.

  ape sandbox egress set dev --domain github.com --domain proxy.golang.org

Mounts cannot be changed this way (they are fixed in the container's OCI spec at
creation) — use 'down' then 'up', which is cheap because repos, caches and the
framework all live in durable host mounts.

Subcommands:

- `set` — Replace a running workspace's egress allowlist

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox egress set

Replace a running workspace's egress allowlist

```
ape sandbox egress set <name> [flags]
```

Replace the domains a running workspace may reach. The list is REPLACED, not
added to, so it is also how you revoke access: 'set <name>' with no --domain leaves the
workspace with no egress at all.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--domain` | stringArray | `[]` | Domain to allow (repeatable; replaces the current list) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox exec

Run a command inside a workspace

```
ape sandbox exec <name> -- <cmd>...
```

Run a command inside a workspace, streaming its stdout/stderr back over the
aped exec session subjects and returning its exit code.

On an aped node without an interactive backend (the nerdctl shell driver) it
falls back to a request/reply exec that reports only the exit status (output goes
to the node's logs).

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox forward

Forward a local port to a port inside a workspace

```
ape sandbox forward <name> <local>[:<guest>] [flags]
```

Forward a port on THIS machine to a TCP port inside a workspace, so you can
open a browser (or point a client) at something running in there.

  ape sandbox forward dev 8080          # localhost:8080 → workspace :8080
  ape sandbox forward dev 3000:8080     # localhost:3000 → workspace :8080
  ape sandbox forward dev --ssh         # localhost:2222 → workspace :22

Nothing is exposed. The workspace gets no listener and neither firewall changes:
the guest end dials its OWN loopback and the bytes ride the same authenticated
session transport as exec and attach, which aped audits. The forward is private
to you, it is not a public URL, and it does not connect workspaces to each other.

Forwards live with this command, not with the workspace: Ctrl-C ends them and
the workspace is untouched. Run several at once in separate terminals, or the
same command twice for two ports.

--ssh gives you an ssh (and therefore VS Code Remote) target. The image ships
sshd and aped composes ~/.ssh, but nothing starts sshd for you, so start it once
per workspace first:

  ape sandbox exec dev -- sh -c 'mkdir -p /run/sshd && /usr/sbin/sshd'
  ape sandbox forward dev --ssh &
  ssh -p 2222 root@127.0.0.1

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--bind` | string | `127.0.0.1` | Local address to listen on (0.0.0.0 exposes the forward to your network) |
| `--ssh` | bool | `false` | Forward the workspace's sshd (guest :22, local :2222 unless a local port is given) |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox framework

Manage the APEX framework refs a sandbox node can mount

```
ape sandbox framework
```

Manage the materialized APEX framework refs on this host.

A sandbox workspace gets the framework as a READ-ONLY mount at /opt/apex-framework
rather than a baked image layer, so the public ape-sandbox image stays
framework-free and credential-free. This command is the host-side, credentialed
half: it copies one pinned ref out of your local framework checkout into the
node's framework root.

  ape sandbox framework materialize v0.3.1
  ape sandbox framework ls
  ape sandbox up dev --framework-ref v0.3.1

aped never fetches the framework itself: if a requested ref is not materialized,
'ape sandbox up' fails with the command to run. Inside the workspace, consume it
with 'ape framework setup --from-worktree --no-commit --no-fetch --repo /opt/apex-framework'.

Subcommands:

- `ls` — List the framework refs materialized on this host
- `materialize` — Materialize a framework ref into the node's framework root

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox framework ls

List the framework refs materialized on this host

```
ape sandbox framework ls [flags]
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--root` | string | `—` | Root to list (default: $APE_FRAMEWORK_ROOT or /srv/apex-framework) |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox framework materialize

Materialize a framework ref into the node's framework root

```
ape sandbox framework materialize <ref> [flags]
```

Materialize one framework ref (tag, branch, or commit) as a self-contained,
mountable checkout under the framework root.

The ref must ALREADY be present in the local framework repo — this command does
not fetch, so a stale checkout fails loudly instead of silently materializing an
older commit. Fetch first with your own credentials:
  git -C <repo> fetch --tags

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--force` | bool | `false` | Replace an already-materialized ref |
| `--repo` | string | `—` | Local apex_process_framework checkout (default: $APEX_FRAMEWORK_REPO, else ape's own clone) |
| `--root` | string | `—` | Root the node mounts framework refs from (default: $APE_FRAMEWORK_ROOT or /srv/apex-framework) |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox freeze

Freeze a workspace (cgroup-freeze; guest RAM stays resident)

```
ape sandbox freeze <name>
```

Freeze cgroup-freezes the workspace's guest processes: the guest stops
consuming CPU but its RAM stays fully resident, so unfreeze resumes instantly.
This is a freeze, not a VM suspend (see 'ape sandbox suspend').

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox governance

Manage the governance repo refs a sandbox node can mount

```
ape sandbox governance
```

Manage the materialized governance repo refs on this host — the same
delivery as 'ape sandbox framework', for the canonical governance repo the
reconciliation skills read.

A workspace gets it as a READ-ONLY mount at /opt/apex-governance, with
$APEX_GOVERNANCE_REPO pointing there (set by aped, only when it mounts one).

  ape sandbox governance materialize v0.1.2
  ape sandbox governance ls
  ape sandbox up dev --governance-ref v0.1.2

aped never fetches it: if a requested ref is not materialized, 'ape sandbox
up' fails with the command to run.

Subcommands:

- `ls` — List the governance refs materialized on this host
- `materialize` — Materialize a governance ref into the node's governance root

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox governance ls

List the governance refs materialized on this host

```
ape sandbox governance ls [flags]
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--root` | string | `—` | Root to list (default: $APE_GOVERNANCE_ROOT or /srv/apex-governance) |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox governance materialize

Materialize a governance ref into the node's governance root

```
ape sandbox governance materialize <ref> [flags]
```

Materialize one governance ref (tag, branch, or commit) as a self-contained,
mountable checkout under the governance root.

The ref must ALREADY be present in the local governance repo — this command does
not fetch, so a stale checkout fails loudly instead of silently materializing an
older commit. Fetch first with your own credentials:
  git -C <repo> fetch --tags

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--force` | bool | `false` | Replace an already-materialized ref |
| `--repo` | string | `—` | Local governance checkout (default: $APEX_GOVERNANCE_REPO, else the project's governance_repository_path or ape's own clone) |
| `--root` | string | `—` | Root the node mounts governance refs from (default: $APE_GOVERNANCE_ROOT or /srv/apex-governance) |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox inspect

Show a workspace's live state

```
ape sandbox inspect <name> [flags]
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox ls

List provisioned workspaces

```
ape sandbox ls [flags]
```

List provisioned workspaces with their age and last use.

LAST-USED is the last exec, attach or start — a USE signal, not proof of idleness: a
workspace running a long job without anyone reaching in looks untouched. Use it to
decide what to stop (frees RAM, keeps state) or tear down, and read --idle in the
same spirit.

IDLE-STOP is what the node's automatic reaper will do to each workspace: a
duration it asked for, "off" if it is exempt, or "node" for the node's own
setting. The reaper reads the in-guest agent's heartbeat, not LAST-USED, and it
never stops a workspace it has no heartbeat for.

  ape sandbox ls --idle 24h    # only workspaces nobody has touched in 24h

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--idle` | duration | `0s` | Only workspaces not used for at least this long (e.g. 24h) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox setup

Materialize the project's declared toolchain inside a workspace

```
ape sandbox setup <name> [flags]
```

Run the project's toolchain install step inside a running workspace: 'asdf
install' for the runtime versions the project declares, then 'bingo get' for its
pinned Go tools.

The toolchain comes from the .apesandbox.yaml toolchain: section, which should
reference the native files (.tool-versions, .bingo/) rather than duplicate the
versions. The step is idempotent and becomes a no-op — fully offline — once the
durable tool caches are warm; the FIRST run needs the workspace to have egress
(the registries it downloads from must be in its allowlist).

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root holding the descriptor (default: current working directory) |
| `--dry-run` | bool | `false` | Print the setup script instead of running it in the workspace |
| `--sandbox-config` | string | `—` | Path to a non-default .apesandbox.yaml |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox ssh

How to ssh into a workspace (use 'ape sandbox forward --ssh')

```
ape sandbox ssh <name>
```

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox start

Start a stopped workspace

```
ape sandbox start <name>
```

Start a stopped workspace from its retained container + snapshot. A workspace
that is already running is left alone.

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox stop

Stop a workspace (free its RAM, keep its rootfs + state)

```
ape sandbox stop <name>
```

Stop a workspace: kill the guest task while keeping the container and its
snapshot, so 'ape sandbox start' revives it with its filesystem intact.

Choosing between the three:
  freeze  cgroup-freeze — RAM stays RESIDENT, instant unfreeze, lost on reboot
  stop    task killed  — RAM FREED, rootfs + state kept, survives a reboot
  down    destroyed    — rootfs deleted (a 'volume' mount survives unless
                        --remove-volume)

Toolchain and dependency state lives in durable cache mounts, so a stopped —
or even a destroyed — workspace loses nothing that a warm cache can restore.

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox suspend

Suspend a workspace microVM (save guest RAM to disk) — not yet supported on Kata

```
ape sandbox suspend <name>
```

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox unfreeze

Unfreeze a frozen workspace

```
ape sandbox unfreeze <name>
```

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox up

Provision a Kata workspace

```
ape sandbox up <name> [flags]
```

Provision a long-lived Kata workspace named <name> on the target aped node.

aped resolves the profile, composes a per-workspace ~/.claude, mints a per-VM
telemetry credential, and starts the detached microVM. For a host-fs mount the
project at --cwd is sent as the mount source; aped canonicalizes it and
re-checks it against its policy mount-root allow-list before binding it.

A committed .apesandbox.yaml at the project root describes the rest of the
workspace — the repos to mount (each at /workspace/<name>, one flagged main),
extra mounts, and the egress domains to request. --mount flags merge on top of it
(CLI wins by destination). Everything there is a REQUEST: aped re-checks every
source against its mount roots, refuses reserved destinations, and intersects the
egress domains with its own policy, so a project can narrow what a node permits
but never widen it.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cache` | stringSlice | `[]` | Durable tool caches to mount: asdf\|cargo\|go\|npm\|pub (adds to the descriptor's toolchain.caches) |
| `--cwd` | string | `—` | Project root to mount for host-fs (default: current working directory) |
| `--egress-domain` | stringArray | `[]` | Request an egress domain (repeatable; still gated by the node's policy) |
| `--framework-ref` | string | `—` | APEX framework ref to mount read-only (must be materialized on the node) |
| `--governance-ref` | string | `—` | Governance repo ref to mount read-only at /opt/apex-governance (must be materialized on the node) |
| `--idle-stop` | string | `—` | Stop this workspace after it has been idle this long (e.g. 4h), or "off" to exempt it (default: the node's; overrides .apesandbox.yaml lifecycle.idle_stop) |
| `--image` | string | `—` | Image ref override (default: aped's pinned image) |
| `--mount` | string | `—` | Mount mode: host-fs \| volume \| ephemeral (default: host-fs) |
| `--mount-path` | stringArray | `[]` | Extra mount <source>[:<dest>][:ro\|:rw] (repeatable; ro by default; merges with .apesandbox.yaml) |
| `--no-sandbox-config` | bool | `false` | Ignore any .apesandbox.yaml in the project |
| `--profile` | string | `—` | Profile name aped resolves (default: derived from the request) |
| `--runtime` | string | `—` | Runtime handler: kata-qemu \| kata-clh |
| `--sandbox-config` | string | `—` | Path to a non-default .apesandbox.yaml |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape script

Run a Go orchestration script through the yaegi interpreter

```
ape script <file.go> [flags] [-- script-args...]
```

Run a plain Go file inside ape's process under the yaegi interpreter,
with the apescript library injected so the script can drive ape's
primitives — run a task or a prompt (PTY-backed, the same runners the
CLI uses), read manifests, scan transcripts, log, publish events, and
upload blobs — as one deterministic, version-controlled Go file instead
of a shell wrapper around the CLI. RunPipeline returns an error: `ape pipeline`
was retired with APEX v0.

The file must define:

    func Main(ctx context.Context) error

ape evaluates the file, then calls Main. A non-nil error (or a panic,
which is recovered and reported with the yaegi stack) exits 1; SIGINT
cancels the context so the in-flight run tears down cleanly.

Use "-" as the file to read the script from stdin. Everything after a
"--" separator is exposed to the script as apescript.Args().

  ape script ops/nightly.go -- --target ./component-a
  cat ops/nightly.go | ape script -

By default the interpreter is unrestricted (full stdlib — arbitrary
trusted code, same trust level as your shell). --sandbox switches to
yaegi's restricted symbol set, which blocks os/exec, os.Exit, syscall,
and unsafe while keeping the apescript orchestration surface fully
available. See docs/reference/apescript.md for the per-group rules.

A script is a conductor (D62): it runs at depth 0, from a terminal, and the
sessions it starts are depth 1 — an apex-loop among them holds the
project's one loop lock. It refuses inside any ape session (exit 2,
code session.depth), the loop's included, because a script starts
model sessions.

Exit codes: 0 success · 1 the script returned an error, panicked, or a
launched run failed · 2 usage or read error (no file, bad flags), or the
nesting rule.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root directory (default: current working dir) |
| `--events-subject-prefix` | string | `ape.evt` | Subject root for progress events. |
| `--nats-creds` | string | `—` | NATS .creds file; its user identity is baked into every subject (env APE_NATS_CREDS). |
| `--nats-url` | string | `—` | NATS server URL for progress events + transcript upload (env APE_NATS_URL). Empty disables both. |
| `--output-format` | string | `human` | Output format: human\|json\|yaml (json/yaml wrap the run in {result, duration, cost_usd}) |
| `--quiet` | bool | `false` | Suppress apescript.Log output |
| `--sandbox` | bool | `false` | Run the script in the restricted interpreter (blocks os/exec, os.Exit, syscall, unsafe) |
| `--transcript-store` | string | `nats-object` | Transcript blob backend: nats-object\|uri-offload (env APE_TRANSCRIPT_STORE). |
| `--upload-transcripts` | bool | `false` | At run end, upload the transcript set as content-addressed blobs (env APE_UPLOAD_TRANSCRIPTS=1). |

## ape service

Run a NATS-micro job daemon that accepts task/prompt/script jobs over request/reply

```
ape service [flags]
```

Turn this machine into a remotely drivable ape worker (PLAN-14). The
daemon registers a NATS micro service on

  ape.svc.<name>.<project-slug>.<endpoint>

and accepts JSON request/reply jobs: task.run dispatches an ape child
process (headless, PTY-only); job.status / job.list / job.stop manage
them; status / health report the daemon. NATS-micro $SRV.PING /
$SRV.INFO / $SRV.STATS discovery is available for free. prompt.run runs
ape prompt, and script.run ape script (a script_path inside the allowlist,
or script_source only with allow_script_source: true in service.yaml).
pipeline.run is registered and always rejected (VALIDATION): `ape pipeline`
was retired with APEX v0.

Admission is keyed exclusivity, exclusive by default: a job holds its
exclusivity_key (default "") exclusively unless nonexclusive:true. Conflicts
are rejected immediately (BUSY_EXCLUSIVE / BUSY_KEY) — never queued. Requests
naming a project_root outside the allowlist are rejected (PROJECT_NOT_ALLOWED).

The daemon serves the project plus its declared component repositories, read
from _apex/service.yaml (or ~/.ape/service.yaml, or --config):

  project_root: /abs/path/main-project
  allow:
    - /abs/path/main-project
    - /abs/path/component-repo

Jobs and the nesting rule (D62, D83): a task or prompt job is a model
session. Every *.run request carries the caller's stamp, "caller": {depth,
run_id, parent_run, pid}, which `ape service submit` adds from ape's own record
of the session it runs in (see ape service submit --help). The daemon
refuses a request without one (wire v2; a pre-rc.4 client's request is
refused, never run) and one from depth 2 with session.depth (action stop),
spawning nothing; a script job is refused from depth 1 too. From depth 1
only an apex-loop run's job runs — its run must be a running
apex-loop manifest in the job's project_root — and only a task of a
skill on its allow-list, one child at a time (session.not-allowed,
session.child-running): the job's ape task runs under the loop's
session marker, as its child (depth 2, parent_run, in its children, stopped
when it ends). A depth-0 job runs as before. A refusal is a micro error whose
code is the nesting code, its body {v, code, action, message}; job.status
shows the caller. The stamp is the client's word: a raw publish can claim
depth 0.

SECURITY: anyone who can publish on the service subjects can run skills on
this machine. Scope the NATS credential's publish/subscribe permissions to
ape.svc.<name>.<project-slug>.> on the server — that is the real trust
boundary (see docs/how-to/run-ape-as-a-service.md).

Shutdown is graceful: SIGINT/SIGTERM stops accepting new jobs and waits for
in-flight children (indefinitely by default; bound it with --drain-timeout).
A second signal terminates them immediately: each job's whole process tree,
its claude session and that session's commands included, gets SIGTERM and,
half a second later, SIGKILL; the daemon records each job's end and exits,
about a second after the signal. job.stop stops one job the same way. (On
Windows only the child process itself is killed.)

Exit codes: 0 clean shutdown · 1 connect/registration failure · 2 usage or
config error (bad --name, missing/invalid service.yaml, no NATS URL).

Subcommands:

- `submit` — Submit a job to an ape service daemon, stamped with the caller's session depth

Examples:

```
  ape service --nats-url nats://127.0.0.1:4222 --nats-creds ./ape.creds
  ape service --name ci --drain-timeout 5m
  # discovery + a task submission (stamped with this caller's depth):
  nats req '$SRV.PING.ape' ''
  ape service submit task '{"project_root":"/abs/path/myproject","skill":"apex-plan","args":"development/briefs/greeter.md"}'
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--config` | string | `—` | Path to service.yaml (default: <cwd>/_apex/service.yaml, then ~/.ape/service.yaml). |
| `--cwd` | string | `—` | Project root for config resolution (default: current working dir). |
| `--drain-timeout` | duration | `0s` | On shutdown, wait this long for in-flight jobs before terminating them (0 = wait indefinitely; a second signal forces). |
| `--events-subject-prefix` | string | `ape.evt` | Subject root for daemon job lifecycle events. |
| `--name` | string | `ape` | Service name — the <name> subject segment and $SRV discovery name (run several daemons on one cluster with distinct names). |
| `--nats-creds` | string | `—` | NATS .creds file; its user identity is the <user> token on job lifecycle events (env APE_NATS_CREDS). |
| `--nats-url` | string | `—` | NATS server URL (env APE_NATS_URL). Required. |

## ape service submit

Submit a job to an ape service daemon, stamped with the caller's session depth

```
ape service submit <task|prompt|script> <request-json> [flags]
```

Send one task.run, prompt.run or script.run request to an `ape service`
daemon and print its reply (the accepted job's id) on stdout.

The request is the JSON body the endpoint takes (see ape service --help and
docs/reference/events.md); project_root defaults to the current directory.
ape adds the caller stamp every *.run request needs (D83):

  "caller": {"depth": <n>, "run_id": "<run>", "parent_run": "<run>", "pid": <pid>}

computed as `ape task` computes its own nesting depth — the session marker
(APE_SESSION, APE_DEPTH) and the process ancestry — so a session cannot
widen it by scrubbing its environment. Any caller in the given body is
replaced. The daemon refuses a request without a stamp, and one from depth
2, with session.depth (action stop). From depth 1 it accepts only an
apex-loop run's job, a task of a skill on that project's allow-list,
one child at a time (session.not-allowed, session.child-running), and runs
it as that loop run's child (depth 2, parent_run, listed in its
children, stopped with it). From depth 0 it runs the job as before.

The stamp is the client's word: a raw NATS publish can claim depth 0. The
NATS credentials and subject permissions remain the boundary.

The subject is ape.svc.<name>.<slug>.<kind>.run, the slug derived from the
daemon's primary project: --service-root, by default the request's
project_root.

A task of apex-orchestrator is refused before anything is sent (exit 2,
skill.renamed, action stop): since rc.7 it is the operator's interface, opened
with /apex-orchestrator in a person's own Claude Code session, and the
unattended runner is apex-loop (see ape task --help).

Exit codes: 0 accepted (the reply on stdout) · 1 no daemon answered, or NATS
failed · 2 usage, a malformed request, or a refusal (skill.renamed, session.depth,
session.not-allowed, VALIDATION, PROJECT_NOT_ALLOWED) · 3 busy
(session.child-running, session.orchestrator-running, BUSY_EXCLUSIVE,
BUSY_KEY): retry later. A refusal prints the daemon's message with its code.

Examples:

```
  ape service submit task '{"skill":"apex-plan","args":"add a --name flag to greet"}'
  ape service submit task '{"project_root":"/abs/repo","skill":"apex-loop","args":"M1"}' --name ci
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--name` | string | `ape` | The daemon's --name. |
| `--nats-creds` | string | `—` | NATS .creds file (env APE_NATS_CREDS). |
| `--nats-url` | string | `—` | NATS server URL (env APE_NATS_URL). Required. |
| `--service-root` | string | `—` | The daemon's primary project root, for the subject's slug (default: the request's project_root). |
| `--timeout` | duration | `10s` | How long to wait for the daemon's reply. |

## ape sessions

List, prune, or open the URL of live ape sessions

```
ape sessions [flags]
```

Live ape sessions are tracked in ~/.ape/registry.json, and this
subcommand inspects that registry. Every model session ape launches is a row
while it runs — ape task, ape prompt, ape chat, and the change review --run
and ape script sessions that run through them — with its run id, the skill
ape typed, its depth and parent run (the nesting rule, D62), the ape
process running it (PID) and the claude it spawned (claude_pid in JSON).
ape reads those rows to tell which session a command runs under, so a
pruned row only ever belongs to a process that is gone. Rows show the skill
as recorded: a loop run an ape before rc.7 started reads apex-orchestrator,
and counts as apex-loop's for the nesting rule (its lock and children, Q95). Rows an older ape
wrote for `ape pipeline`'s web mode, retired with APEX v0, carry a port and URL.

  ape sessions               Show one row per live session.
  ape sessions prune         Drop rows whose PID is no longer running.
  ape sessions open [<pfx>]  xdg-open the URL of the live session whose
                             cwd starts with <pfx>. Errors if zero or
                             multiple sessions match.

Subcommands:

- `open` — xdg-open the URL of the live session whose cwd matches <project-prefix>
- `prune` — Drop registry rows whose PID is no longer running

Examples:

```
  ape sessions
  ape sessions --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape sessions open

xdg-open the URL of the live session whose cwd matches <project-prefix>

```
ape sessions open [<project-prefix>]
```

Examples:

```
  ape sessions open ~/projects/foo
```

## ape sessions prune

Drop registry rows whose PID is no longer running

```
ape sessions prune [flags]
```

Examples:

```
  ape sessions prune
  ape sessions prune --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape status

Show the current milestone, active changes, blocks, pending human review and spend

```
ape status [flags]
```

Show where the project stands, from the records ape keeps:

  - the current milestone (the first not done): status, gate and its last recorded run,
    budget, and the spend of runs started since it was approved
  - the milestone's start (JSON milestone.base_sha, and base_sha on each milestones[] line
    once approved; D130): HEAD at its first approval — what a gate gets as
    APE_MILESTONE_BASE, for a gate run by hand or a range since the milestone began
  - the milestone's limit stops (JSON milestone.stops: reason, at, run, skill, spent_usd,
    budget_usd, session_usd): sessions ape task stopped at its enforced budget
    (limits.budget: enforce; JSON milestone.budget_enforced), whose measured cost the
    spend counts
  - the milestone's changes blocked in a row (JSON milestone.consecutive_blocks and
    consecutive_blocks_cap, limits.consecutive_blocks) and, when that cap is reached, the
    limit that stops work on it: milestone.diagnostics, in the shape of a --json failure's
    diagnostics — [{code: "milestone.blocks-exceeded", action: "stop", …}] (D49; [] when
    none), and next says so
  - a milestone a person closed with its gate red (`ape milestone close --accept-red`):
    JSON milestones[].accept_red {reason, by, at, gate}; the brief names the last one, and
    the roadmap lines say "accepted red by <who>: <reason>"
  - each milestone's newest gate run (D170): JSON milestones[].last_gate {sha (the commit
    the gate ran at), exit, at, log}, absent before its gate ever ran
  - the checked-out branch (JSON branch; "(on <branch>)" in the first line)
  - tweaks first (trial decision D40, D240): a change with "milestone: none" is a tweak
    (JSON changes[].lane "tweak"; else "milestone"). Approved tweaks (changes[].approved:
    {by, at, plan_digest}) are listed before milestone changes — started ones first, then
    by approval time — and JSON "tweaks" holds the ids of those not blocked, in the order they
    run: the first runs ahead of milestone work, at the loop's next change boundary
    (never in parallel), or alone with `ape task apex-loop --args <id>`, and next
    points at it. A tweak awaiting approval is listed last, with the command a person runs
    (`ape change approve <id> --by <who>`). A tweak's runs (`ape task <skill> --args <tweak-id>`)
    count toward no milestone's spend
  - every change not archived, with its status, blocked reason, red checks from its
    last verify (JSON failing: "T1 (exit 1)", "T2 (vacuous: …)", "S (unrun: $X is unset
    …)" — a red verify of an in-progress change comes from this checkout's
    _apex/state/logs/<id>/last-verify.json), the checks that passed only on a retry
    (JSON flaky; "flaky: T2" in the brief; retries: in _apex/config.yaml), human-review tasks not yet accepted, and for a started change its red
    verifies since it started against limits.attempts (JSON attempts, attempts_cap;
    "(3/5 attempts)"; attempts_total, never reset) — a change the cap blocked shows
    "attempts exceeded" as its reason. The counts are this checkout's
    _apex/state/attempts.json (git-ignored). A started change is worked on its own
    branch, change/<id> (D38; JSON changes[].branch), and its status is read from
    there whatever branch is checked out — a change branch lists the changes it does
    not hold too
  - an answered change (H9): a person's answer to what blocked it (JSON changes[].answer:
    {by, at, text}, kept once it is started again); next has it re-planned and started
  - human review (H4, D47): every change lists awaiting_human (always present in JSON, []
    when none): its human-review tasks no person has accepted, once only the person is
    left — the change is reviewed, or archived. The loop skips such a change. A
    reviewed change awaiting a person is not archived next — next names the accept a
    person runs. Archived changes
    with unaccepted human-review tasks (archived past them by a person) are listed after
    the rest, status "archived", under "Archived, human review not accepted"; they stay
    acceptable (`ape change accept --help`)
  - tools (D59 Q4, D74): every change lists tools, its design-tool tasks
    ("— tool: <name>"), as [{task, name, skill, done}] — done: the task green at the
    latest verify of the current plan, or a human-review one accepted — and
    review_tools, its review_tools: frontmatter, as [{name, skill}]; skill is what
    _apex/config.yaml tools: names ("" when it names none). Both always present ([])
  - acceptances (JSON changes[].accepted: [{task, by, at, evidence, run}]): by a person,
    or by "delegate:<skill>" with its run when _apex/config.yaml human_review delegates
    them (D75; "Accepted by a delegate" in the long output); and each human-review task's
    newest delegate verdict (JSON changes[].delegate: {"T2": {verdict, reason, run, at,
    voided, defects, returned}}): a fail or a voided run leaves the task awaiting a
    person, and the brief says why — unless the fail went back to the change (D188:
    returned, it named a defect in the change's own diff; JSON changes[].delegate_returns:
    {"T2": [{run, reason, defects, at}]}, as state.json records them): the change is
    in-progress again, the brief shows the reason and next names apex-change, and once
    it is reviewed again the task is the delegate's again. JSON changes[].delegable: [{task, reason}] (D152, omitted when none) are the
    awaiting tasks a delegate may take now — reason "configured" (human_review delegates)
    or "docs-default" (a docs change's docs check, change.md docs: true, its diff only
    the docs (release.docs, docs.changelog, docs.user_docs) and the development folder,
    human_review.docs not person); a task whose
    delegate failed (not returned) or was voided is a person's and not listed. next names the delegate
    for the first (`ape change accept <id> <T> --delegate --wait 9m`) before a person
  - spend so far: the sum of finished ape task run manifests (and pipeline runs an older ape recorded)
  - red at baseline, not any change's own (JSON "pre_existing_red": [{change, kind, ref,
    cmd, exit, sha}]): the regression command, and scenario checks of the spec a change
    leaves untouched, that were red when a change was baselined — for the planner to
    roadmap. A change's own task and delta checks red at baseline are normal check-first
    and never listed
  - the base branch (the checked-out one, or the base of the change whose branch is
    checked out) against its upstream (JSON "base_upstream": {branch, upstream, ahead,
    behind}; null without one), from the local remote-tracking ref — ape never fetches.
    v1.0 is one operator per checkout on a local trunk (D45): start and archive refuse
    while the base is behind (base.behind-upstream: pull)
  - work outside the change flow (JSON "outside_flow": [{sha, subject, files}], newest
    first; a count line in the human output): commits on that base branch since the last
    archive squash — else since the v1 epoch, the newest "apex: project init" or "apex:
    lift" commit — that touch paths outside development/ and _apex/, carry no "Change:"
    trailer and have no "apex:" subject. An apex-release commit ("Release:" trailer)
    that touches only the changelog (docs.changelog, else CHANGELOG.md) is not
    listed. Shown, never failed
  - an archive in flight or interrupted by a crash (JSON "archive_journal", with
    "archive_running" when its process is alive; on the first line): status only reports
    it — the next writing command rolls a crashed one back (archive.interrupted; see
    `ape change archive --help`)
  - next: what the way of working says comes next, derived from the state alone; JSON
    "next_why" beside it (and "Why next:" in the long output, not --brief) is its reason,
    one line (Q100) — next stays one string
  - Next (JSON "roadmap_next": ["2026-10-16 <text> (from <id>)", …], [] when none; a
    count line in the human output): the findings noticed out of scope that apex-plan
    triages, as the base branch holds them (`ape next add` writes there from any
    branch) — development/next.md's items, then any the roadmap still holds (a project
    not yet moved by `ape next move`, D217), each once. Not the same field as
    "next", the step above
  - JSON "docs": {changelog, user_docs} from _apex/config.yaml docs: ({} when unset), for
    the skills that keep a changelog and user docs current
  - JSON "config": {commit_types, release_scheme, release_docs, commit_subject} — the
    project's commit types (commit.types, default feat, fix, perf, refactor, docs, test,
    build, ci, chore), the archive's subject template (commit.subject, default
    "{type}: {title}", D193 — the effective one, for skills to read instead of guessing),
    release scheme (release.scheme: semver, the default, date or edition) and the docs a
    release brings up to date (release.docs, default README.md and docs/ where they exist;
    D129), defaults filled in, for apex-change and apex-release (D120)
  - the latest ape task run (JSON "latest_run": {skill, run_id, status, stopped,
    resets, resets_at, message, depth, parent_run}): how it ended, e.g. "stopped:
    usage-limit, resets 2026-10-16T23:00:00+02:00" when a claude.ai usage limit
    stopped it (see ape task --help, exit 14); a session a loop run started
    names that run ("started by <run>"; depth 2, parent_run)
  - JSON "sessions": the model sessions ape launched that run in this project now
    ([{run_id, kind, skill, depth, parent_run, pid, started_at}], [] when none): an
    apex-loop run at depth 1 and its one child at depth 2 (D62). The loop's
    run also carries relay (OI-11): {context_tokens, context_window, context_fill,
    context_relay, relays, relays_cap, relay_of} — its session's context as the newest
    turn of its transcript shows it, over the model's window (context_fill null and
    context_window 0 when the window is unknown: no relay then), the threshold
    (limits.context_relay), the relays so far and their cap (limits.relays), and the
    run it relays; the brief says "context 12 % (relay at 60 %), relays 0/5"

--brief renders at most 25 lines (what the SessionStart hook injects).

The board (D60): in a project with a board (.aboard/, see `ape aboard --help`), ape keeps
one tab, "APEX status" (key ape-status), in step with this report. It is refreshed best
effort after every v1 command that changes what status reports — project init, lift and
accept-config; milestone new, approve, gate and close; decision new and accept; next add;
every change verb but check and diff — and by --board, which refreshes it now and says
where it wrote (JSON "board": {action: posted|wrote|unchanged|no-board|skipped, where,
detail}). A running board is posted to; with none running, .aboard/aboard.json is
rewritten in place. ape never creates a board. The tab is not authoritative: it is a view
of this report, nothing reads it back, and where the two disagree the tab is wrong. A
refresh never changes a command's exit code or stdout (a problem is one line on stderr),
and one that would change nothing writes nothing. The tab is quiet (aboard v0.4.0, D126):
ape's refreshes raise no notification, unread dot or banner. Going quiet is announced once,
when ape creates the tab (or first refreshes one an older ape made). ape sets quiet only
that once: if a person turns it off from the tab's menu, it stays off, and ape's changed
views raise the dot again.

Exit codes: 0 ok · 2 usage. --board exits 0 whatever the board did.

Examples:

```
  ape status
  ape status --brief
  ape status --json
  ape status --board
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--board` | bool | `false` | refresh the project's aboard status tab now and say where it wrote (JSON "board"); the tab is a view, never a record, and quiet: a refresh raises no notification or unread dot |
| `--brief` | bool | `false` | at most 25 lines |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |

## ape task

Run a single framework skill through the interactive PTY runner

```
ape task <skill> [flags]
```

Run one framework skill in one interactive Claude Code session, with
ape's agent prefix, preflight, bridge hooks, run manifest and telemetry,
every parameter passed as a flag. Execution is PTY-interactive only: claude runs as a REPL,
the prompt is typed as keystrokes, and completion is detected via the
bridge Stop hook.

Commit control is two-layered:
  --no-commit     skill layer — tells the skill/framework not to commit, by
                  typing --no-commit into the invocation. Without it none is
                  typed, on either path: APEX v1 skills commit their own work.
  --task-commit   task layer — opt-in git commit of the complete task at
                  the end of the run. Off by default. A bare flag derives
                  the message "ape:task/<skill>".

Run artifacts land under <project>/_output/ape/tasks/<skill>/<run-id>/
(manifest.yaml, per-step ndjson, runlog streams) — in an APEX v1 project
(_apex/state/ exists) under _apex/state/runs/tasks/ instead, a directory
git ignores, where a model clearing _output/ cannot reach.

In an APEX v1 project (development/project.md exists) ape decides the
session's model and effort itself, and records the decision in the run
manifest as launch: {model, model_source, effort, effort_source}:
  model    --model, else _apex/config.yaml models.<skill> ("sonnet" or
           {model: sonnet, effort: high}), else the model of the ape session
           that started this one (APE_SESSION_MODEL), else claude's default
  effort   --effort, else models.<skill>.effort, else the parent session's
           (APE_SESSION_EFFORT; skipped when models.<skill> names a model
           without an effort), else _apex/config.yaml effort:, else nothing
The effort is passed as claude --effort <level>, which sets the session's
effort only; CLAUDE_CODE_EFFORT_LEVEL is never set (an inherited one is
removed) and _apex/effort-defaults.yaml is not used. Sources: flag, config,
parent-session, config-default, default. The session's tools see the
resolved values as APE_SESSION_MODEL / APE_SESSION_EFFORT.

In an APEX v1 project ape first checks the floor the installed framework
sets: when _apex/ape-commands.yaml declares a min_ape_version above this
ape, the session is refused (exit 1, code ape.below-floor) with the
command that updates ape — the same floor `ape framework update` refuses an
install by (exit 11). A dev build or a pseudo-version cannot be compared:
a warning, and the session runs.

In an APEX v1 project ape also enforces the milestone budget, when
_apex/config.yaml says so (it is off by default):
  limits:
    budget: enforce   # or off
With enforce, and a Budget: line on the current milestone (the first not
done, and only when approved), a session that would start with the
milestone's spend already at its budget is refused (exit 1, code
milestone.budget-exceeded); a running session is stopped when the
milestone's spend — its runs since approval, plus this session's live cost
read from its transcripts, sub-agents included, every 30 s (sooner in its
first half minute) — reaches it.
The stop ends the run (exit 1) with termination kind "budget" in the run
manifest (spent_usd, budget_usd, session_cost_usd) and is recorded under
the milestone's stops in _apex/state/limit-stops.json (ignored by git:
nothing is committed, whichever branch the session was on); `ape status` shows it. Raising the Budget:
line (a person's call) lets sessions start again. An unparseable
_apex/config.yaml in a v1 project refuses every session (exit 2) rather
than run it with no limits.

apex-orchestrator is refused (exit 2, code skill.renamed, action stop), and
so is the same skill through ape service submit task and an ape script's
RunTask: since rc.7 (D171) it is the operator's interface, a skill a person
opens in their own Claude Code session by typing /apex-orchestrator, and the
unattended runner of one approved milestone or tweak is apex-loop. The
refusal carries both pointers: `ape task apex-loop --args <M>` (the run's own
--args) and /apex-orchestrator. It is no alias: a stopped run's older resume
line fails loudly instead of starting the wrong skill. A loop run an older
ape recorded as apex-orchestrator still counts as the loop's (its lock, its
children; ape costs groups both names).

A session of apex-loop is also refused (exit 1, code
milestone.blocks-exceeded) when the milestone it runs — --args M<n>, else
the current one — has had limits.consecutive_blocks (default 2) changes
blocked in a row; a person deals with them and re-approves the milestone,
which resets the count. A tweak's run (--args <tweak-id>) is not refused.

--prompt-file <path> reads the prompt from a file, and "-" reads it
from stdin. It is how a printed escalation command carries the
operator's own words: argv runs command substitution on backticks and
reads a leading dash as a flag, so text that a person wrote never goes
through it. The text is held to the same shape a REPL can be typed:
one line, no control characters, no trailing backslash.

--handoff <file> is a shorthand for --prompt: it checks the file
exists and derives the prompt "Read <abs-path> and follow the Resume
Protocol inside it." (the same continuation prompt the /handoff skill
suggests), and is mutually exclusive with --prompt.

--prompt, --prompt-file and --handoff each need --prompt-flag, the skill
flag the text is typed after: without it ape refuses (exit 2) instead of
starting the skill without the text. No APEX skill takes one: resume a
handoff by pasting its continuation line after /clear.

A line longer than 700 characters is not typed. Claude Code turns a
typed burst past 800 characters into a "[Pasted text #N]" placeholder
that never runs as a skill, so ape writes the step's --args and prompt
text, verbatim and with its newlines, to
stages/NN-<stage>/step-NN-<skill>.args.md in the run directory, and
types a line that names that file instead. The step-start event records
it as args_file.

The same file is used, whatever the length, when the --args or prompt
text would not arrive as written if typed: when it holds two or more
spaces in a row, a tab, a line break or any other control character.
The typed line splits --args on whitespace, and a line break submits
early. Leading and trailing whitespace of --args is not part of the
text. Shorter, single-spaced, one-line text is typed exactly as before.

After typing, ape presses Enter once claude has drawn the line, then
confirms the submit through the UserPromptSubmit hook, pressing Enter up
to twice more. A line that is still not submitted ends the run with
termination prompt_not_submitted instead of idling to the timeout.

Where the project declares commit ownership in _apex/commit-owners.csv,
every dispatch is asserted against it. A skill ABSENT from that file
must leave HEAD, the index and the stash reflog unchanged — "git add"
and "git stash" both leave HEAD alone, so HEAD by itself is not the
check. A skill PRESENT in it must produce at least one commit, every one
matching a message format the file declares for it.

No APEX v1 release ships _apex/commit-owners.csv, so a v1 project is not
asserted unless it adds one. A project with NO commit-owners.csv is not
asserted at all: with nothing
declaring which skills commit, neither assertion has a basis, and the
verdict is reported as skipped rather than guessed in either direction.
Rows naming a skill ape never dispatches (the conducting session's own)
are simply never reached. --task-commit is ape's own commit and is not
judged against a skill's declaration.

The verdict is written to the run manifest as commit_contract, so a
failure stays diagnosable after the fact.

A run's commits are the range its HEAD moved over. A commit in it that was
made while another ape session's run in the same checkout went (not this
run's parent loop run, not its own children) cannot be told apart, so
neither run claims it: both list it under commits_shared, in the JSON
envelope and the run manifest, and it is left out of commits and of the
commit-message check.

Usage limits (H20): when a claude.ai plan's usage limit stops the session,
Claude Code draws its limit line ("You've hit your usage limit · resets
11pm (Europe/Madrid)", or "Usage limit reached · limit resets …") and no
hook fires again. ape reads the PTY pane every 15 s; the same line on two
reads with no hook between them ends the run (exit 14) with termination
kind "usage_limit" in the run manifest, resets as printed and resets_at
parsed; `ape status` shows it as the latest run ("stopped: usage-limit,
resets <ts>"). Run again after the reset. Matched against Claude Code
2.1.302's text, unmeasured live.

Notifier: in an APEX v1 project, _apex/config.yaml's optional
  notify: <shell command>
runs (sh -c, 10 s timeout, output dropped, a failure only warned about)
when a run stops on a limit (usage limit, budget) — and, from ape change,
when a change becomes blocked — with APE_EVENT=stop|block, APE_CHANGE
(the change --args names, if any) and APE_REASON in its environment.

Sessions and the nesting rule (D61, D62): every skill of the v1 loop runs
as its own ape task session, and depth counts only the model sessions ape
launched (ape task, ape prompt, ape chat, ape change review --run, and the
ape service task/prompt jobs, which run those commands, decided from the
caller's stamp that ape service submit adds — see ape service --help):
  depth 0  a terminal, a Claude Code session ape did not launch, a shell,
           ape script, the service daemon: may start any session
  depth 1  a session started from depth 0: may start a child only when it is
           ape's own apex-loop run (ape task apex-loop), only a skill on its
           allow-list (apex-plan, apex-change, apex-apply, apex-review,
           apex-release, apex-accept, the human_review: delegate skill, and
           every skill the tools: slots name), and one child at a time
  depth 2  a session a loop run started: starts nothing
A refusal is exit 2, code session.depth or session.not-allowed (action
stop); a second child while one runs is exit 3, session.child-running, and
a second apex-loop run in a project exit 3,
session.orchestrator-running (action retry). Deterministic ape commands are
never limited. ape decides the depth from what it recorded, never from the
model: the session's marker (APE_SESSION, APE_DEPTH) and the process
ancestry — every session it launches is a row in ~/.ape/registry.json
naming the ape process and the claude it spawned, so a scrubbed environment
changes nothing. Every session carries APE_DEPTH, and a child also
APE_PARENT_RUN (the loop's run) and APE_PARENT_PID (the ape process
running it); its manifest records depth and parent_run, and it is stopped,
termination parent_gone, when that process is gone. A loop run's
manifest lists its children and tree_cost_usd when it ends: always, $0 and
no children included, so an absent tree_cost_usd means not computed (totals
stay its own). There is no switch to turn the rule off.

The loop relay (OI-11): an APEX v1 loop session's context grows with its
milestone, so ape relays it. At its next child launch (ape task <skill>,
ape change review --run, ape change tool, ape change accept --delegate)
with its context fill at or over _apex/config.yaml
  limits:
    context_relay: 0.6   # 0 < x <= 0.95; default 0.6
    relays: 5            # relays per loop run; default 5
the launch is refused (exit 2, code session.relay, action stop): the loop
starts nothing, replies and ends, and this process starts a fresh loop
session with the same args — its manifest records relay_of (the run it
relays) and relay (the count), under the same lock; the JSON envelope
carries relays and relay_of. The fill is the newest turn of the loop
session's transcript (input + cache read + cache creation tokens) over its
model's context window; an unknown window is no relay, said once. Past
limits.relays the launch is refused with limits.relays (exit 1, action
person) and the run stops, exit 1, termination relays. Both keys are
guarded (ape project accept-config), like every limit. ape status --json
shows a live loop's fill and relays (sessions[].relay).

A loop run dispatches a skill as
  ape task <skill> --args <id> --detach --output-format json
which prints {"id", "wait", "log", "stdout"} (four lines without json), then
  ape run wait <id> --wait 9m --json
until it stops exiting 75; its JSON carries the session's last message as
result (see ape run wait --help).

Exit codes: 0 success · 1 run failed, idle timeout, the typed line
never submitted, an APEX v1 milestone budget stopped or refused the
session, or a v1 limit or ape floor refused it, or the loop's relays ran
out (limits.relays) · 2 usage or
preflight error, an APEX v0 project (run its pinned v0 ape), apex-orchestrator
(skill.renamed), or the nesting rule refused the session (session.depth,
session.not-allowed), or the loop relay refused a child launch (session.relay) · 3 REPL
never became ready (last pane on stderr), or a session lock another run
holds (session.child-running, session.orchestrator-running) · 5 upstream
API failure · 6 the dispatch violated its declared commit ownership (the run
itself may have succeeded) · 14 a claude.ai usage limit stopped the session.

Examples:

```
  ape task apex-plan --args development/briefs/greeter.md
  ape task apex-loop --args M1 --detach --output-format json
  ape task apex-apply --args <change-id> --model sonnet
  ape task apex-plan --args "add a --name flag to greet" --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--agent` | string | `—` | Framework agent (slash-command) fronting the skill: /<agent> --autonomous -- <skill> ... |
| `--args` | string | `—` | Verbatim skill args appended to the invocation (whitespace-separated) |
| `--commit-allow-dirty` | bool | `false` | Bypass the dirty-tree gate (relevant only with --task-commit) |
| `--cwd` | string | `—` | Project root directory (default: current working dir) |
| `--detach` | bool | `false` | Start the run in the background, detached from this shell, print its id (with --output-format json: {id, wait, log, stdout}), and return at once; then ape run wait <id> --wait 9m waits for it |
| `--effort` | string | `—` | Explicit reasoning effort for the session and its sub-agents (low\|medium\|high\|xhigh\|max). Unset: the project's _apex/effort-defaults.yaml per model, or xhigh without one (see ape config effort). In an APEX v1 project: the session's effort only, passed as claude --effort; unset, models.<skill>, the parent session or _apex/config.yaml effort: decide, else none is passed |
| `--events-subject-prefix` | string | `ape.evt` | Subject root for progress events. |
| `--handoff` | string | `—` | Path to a handoff/context file; derives a "Read <path> and follow the Resume Protocol" --prompt value (mutually exclusive with --prompt) |
| `--idle-timeout` | duration | `0s` | Idle backstop: cancel only after this long with no progress across hook events or transcript growth (e.g. 15m); default 60m. PTY output is NOT an anchor here — see `ape prompt` |
| `--ignore-project-settings` | bool | `false` | Tell the spawned claude to skip project + local .claude/settings*.json |
| `--manifest-dir` | string | `—` | Override the run-artifact base dir (default: <project>/_output/ape/tasks; _apex/state/runs/tasks in an APEX v1 project) |
| `--max-duration` | duration | `0s` | Wall-clock ceiling regardless of progress (e.g. 8h); off by default (D159: a run showing progress stops only on the idle timeout, ape's limits or a person). The clock resets on each sub-agent boundary, so a sequential batch skill is bounded per item. |
| `--model` | string | `—` | Claude model (APEX v1 unset: models.<skill> in _apex/config.yaml, else the parent session's, else claude's default). A bare family (fable, haiku, mythos, opus, sonnet) resolves to its current generation; sonnet-5 / claude-sonnet-5 / opus[1m] pin explicitly |
| `--nats-creds` | string | `—` | NATS .creds file; its user identity is baked into every subject (env APE_NATS_CREDS). |
| `--nats-url` | string | `—` | NATS server URL for progress events + transcript upload (env APE_NATS_URL). Empty disables both. |
| `--no-commit` | bool | `false` | Skill layer: tell the skill/framework not to commit (types --no-commit into the skill invocation; without it none is typed) |
| `--output-format` | string | `human` | Output format: human\|json (json = result envelope on stdout, progress on stderr) |
| `--output-style` | string | `—` | Output style pinned on the spawned session (default "Default"). Pass "inherit" to keep whatever style the machine is configured with. |
| `--prompt` | string | `—` | Run prompt forwarded via --prompt-flag (no APEX v1 skill takes one) |
| `--prompt-file` | string | `—` | File holding the --prompt text; "-" reads stdin. Mutually exclusive with --prompt and --handoff |
| `--prompt-flag` | string | `—` | Skill flag name the --prompt value is forwarded through (spec prompt_flag equivalent) |
| `--quiet` | bool | `false` | Suppress the per-event progress stream |
| `--task-commit` | string | `—` | Task layer: commit the complete task at the end; bare flag derives "ape:task/<skill>" |
| `--transcript-store` | string | `nats-object` | Transcript blob backend: nats-object\|uri-offload (env APE_TRANSCRIPT_STORE). |
| `--upload-transcripts` | bool | `false` | At run end, upload the transcript set as content-addressed blobs (env APE_UPLOAD_TRANSCRIPTS=1). |

## ape transcript

Work with Claude session transcripts

```
ape transcript
```

Transcript utilities. The upload subcommand blob-uploads a session's transcript set over NATS.

Subcommands:

- `upload` — Upload this session's transcript set as content-addressed blobs

## ape transcript upload

Upload this session's transcript set as content-addressed blobs

```
ape transcript upload [flags]
```

Upload the resolved Claude session set (main + sub-agents) as
deduplicated, content-addressed, zstd-compressed blobs, then publish a
companion ape.evt.<user>.<project>.session.<session-id>.transcript-uploaded
event carrying the digest map.

Uploading is idempotent: a blob already present is a cheap no-op (its result
entry is marked existed=true with the same digest), so re-running is safe.

--store selects the backend: nats-object (a NATS JetStream Object Store,
default) or uri-offload (a NATS request returns a signed upload URI; ape
does the HTTPS PUT).

Exit codes: 0 uploaded · 1 upload/publish failed (connected) · 2 usage
error, no NATS configured, or the session was unresolvable.

Examples:

```
  ape transcript upload
  ape transcript upload --store uri-offload --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root for session auto-resolution (default: current working dir). |
| `--events-subject-prefix` | string | `ape.evt` | Subject root for the published event. |
| `--nats-creds` | string | `—` | NATS .creds file; its decoded user identity is baked into every subject (env APE_NATS_CREDS). |
| `--nats-url` | string | `—` | NATS server URL (env APE_NATS_URL). Required — no URL is a usage error (exit 2). |
| `--output-format` | string | `human` | Output format: human\|json (result object on stdout, diagnostics on stderr). |
| `--quiet` | bool | `false` | Suppress the human-mode confirmation line. |
| `--session-id` | string | `—` | Claude session id to report for (default: auto-resolve the current project's newest). |
| `--store` | string | `nats-object` | Blob backend: nats-object\|uri-offload (env APE_TRANSCRIPT_STORE). |
| `--transcript` | string | `—` | Explicit transcript file; the session id is parsed from its name. |

## ape update

Update ape to the latest version

```
ape update [flags]
```

Download and install the latest ape release from GitHub.

Downloads are verified before they are applied: the release's signed
SHA256 manifest is checked against its keyless-cosign Sigstore bundle
(pinning this repository's release workflow identity and the Fulcio
issuer), then the downloaded archive is checked against that trusted
manifest. Verification is fully offline against an embedded Sigstore
trusted root — no cosign binary is required.

It updates the binary that runs it — the ape on PATH — and never a
project's bingo pin (.bingo/ape.mod): that is a tracked, team-wide
change. Inside a pinned project whose pin is behind, it says how to
move the pin; --pin also moves it, committed on its own as
'chore(ape): update ape to vA.B.C'. A launcher that is itself a bingo
pin is not self-updated: --pin moves the project's pin instead.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--pin` | bool | `false` | Also move this project's bingo pin (.bingo/ape.mod) to the latest release and commit it as chore(ape) |

## ape version

Print version information

```
ape version [flags]
```

Print the version, build date, and git commit of the ape binary.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

