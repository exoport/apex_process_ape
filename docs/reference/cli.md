# ape CLI reference

> Generated from the command tree by `make docs-cli` (which runs the hidden
> `ape gen-docs`). Do not edit by hand — change the command definitions in
> `internal/apecmd/` and regenerate. PLAN-9 F4.

## ape

APE — APEX Process Engine CLI

```
ape
```

ape runs APEX framework work against your project through an
interactive Claude Code REPL.

Common commands:
  ape pipeline <name>   Run a multi-stage pipeline (design, governance, epics).
  ape task <skill>      Run a single framework skill without a pipeline YAML.
  ape chat              Open an interactive Claude session in the project.
  ape costs             Show this project's Claude cost rollup.

Also: framework setup/update, doctor, sessions, planning, trait/pattern/adr
inspection. Every claude invocation runs in an in-process PTY — there is no
"claude -p" programmatic path.

Subcommands:

- `aboard` — A shared visual board for a human and one or more agent sessions
- `adr` — Manage Architecture Decision Records
- `bootstrap` — Bootstrap governance artifacts from traits
- `capability` — Inspect and maintain the capability registry
- `change` — The unit of change: new, check, start, verify, review, accept, block, archive
- `chat` — Bridged claude REPL with hooks captured to a runlog
- `config` — Resolve the project's APEX configuration
- `context` — Inspect the project-context document
- `costs` — Show this project's Claude cost rollup
- `decision` — Record architecture decisions (ADR-NNNN) in development/decisions/
- `deferred` — The deferred-work record store
- `doc` — Shard, assemble and survey Markdown documents
- `doctor` — Probe the local environment for prerequisites
- `event` — Publish a session progress event over NATS
- `feature` — Inspect and maintain the feature registry
- `framework` — Install and inspect APEX framework assets in a project
- `governance` — Answer governance questions about a set of paths
- `hook` — Claude Code hooks for APEX v1 projects (registered by `ape project init`)
- `log` — Publish a structured log record over NATS
- `memory` — Read the team-memory file without loading it whole
- `metrics` — Scan and publish this session's usage metrics over NATS
- `milestone` — Add, approve, gate and close roadmap milestones
- `pattern` — Manage governance patterns
- `pipeline` — List or run an APEX pipeline
- `planning` — Show the planning pipeline diagram
- `project` — Create and check an APEX v1 project layout
- `prompt` — Drive an unattended Claude session from a prompt or a handoff file
- `registry` — Verify or reconcile every record registry at once
- `release` — Project the framework's release record
- `rollback` — Rollback ape to the previous version
- `run` — Wait for, or inspect, a run started with --detach
- `sandbox` — Provision and operate hardware-isolated Kata VM workspaces (via aped)
- `script` — Run a Go orchestration script through the yaegi interpreter
- `service` — Run a NATS-micro job daemon that accepts pipeline/task jobs over request/reply
- `sessions` — List, prune, or open the URL of live ape sessions
- `sprint` — Inspect and maintain sprint-status.yaml
- `status` — Show the current milestone, active changes, blocks, pending human review and spend
- `story` — Project and verify story frontmatter
- `task` — Run a single framework skill through the interactive PTY runner
- `trait` — Manage and inspect traits
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

## ape adr

Manage Architecture Decision Records

```
ape adr
```

Aliases: `adrs`

Subcommands:

- `backfill` — Fill absent required fields of existing adrs index entries from their records
- `list` — List all ADRs
- `new` — Scaffold a new ADR file
- `sync` — Reconcile the adrs index against records on disk
- `update` — Apply field deltas to existing adrs index entries
- `verify` — Verify the adrs registry against its index

## ape adr backfill

Fill absent required fields of existing adrs index entries from their records

```
ape adr backfill [flags]
```

Complete the index entries that already exist. A field the family's index
schema requires that is ABSENT from an entry is copied in from that
entry's record: its frontmatter, or its file name for slug. It is the same
rule sync uses when it adds an entry.

It does nothing else. No entry is added, removed or repointed, a value
that is present is never changed (even an empty one), and file: is never
filled, because an entry with no file is sync's repair. That narrowness is
the point: this runs unattended as a framework migration on
`ape framework update`, where a structural index change is not wanted.

A required field the record has no value for stays absent and is reported
as a gap. An entry whose record is absent or unreadable is also a gap.
Gaps are not pending work: nothing on disk can close them, so re-running
finds the same ones.

--check writes nothing and answers in its exit code: 0 when there is
nothing to fill, 1 when fills are pending. Those two are the only answers
about the index. Anything else means the check itself failed: 2 for an
unknown family or an index or record that cannot be read, 4 with no
project config.

Examples:

```
  ape adr backfill --check
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--check` | bool | `false` | Write nothing; exit 0 when nothing is fillable, 1 when fills are pending (2 or 4 if the check itself failed) |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape adr list

List all ADRs

```
ape adr list [flags]
```

Examples:

```
  ape adr list --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape adr new

Scaffold a new ADR file

```
ape adr new <title>
```

## ape adr sync

Reconcile the adrs index against records on disk

```
ape adr sync [flags]
```

Reconcile index.yaml against the records on disk: records with no entry
are added, entries whose id no record claims are removed, and an entry
whose file: no longer resolves is repointed at the record claiming its id.

This is the repair for the findings `verify` reports, and nothing more —
it copies what a record's own frontmatter states and invents no titles,
statuses or any other field. A renamed record keeps its authored entry
rather than being dropped and re-added.

An added entry carries every field of the family's index schema the record
has a value for: its frontmatter, or its file name for slug. A required
field with no value is left out and listed in the change's missing. An
entry that is already listed is never completed here; that is
`backfill`.

--check makes it a dry run: the same diff, nothing written, and the answer
in the exit code: 0 when there is nothing to change, 1 when changes are
pending, 2 when the check itself failed (an unknown family, or an index
that cannot be read), 4 with no project config. Withheld removals alone
exit 0: sync cannot make them. generated_at moves only when something
else did.

Examples:

```
  ape adr sync --check
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--check` | bool | `false` | Report the diff without writing; exit 0 nothing to change, 1 changes pending (2 or 4 if the check itself failed) |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape adr update

Apply field deltas to existing adrs index entries

```
ape adr update [flags]
```

Apply per-entry field deltas to index.yaml and refresh generated_at.

--updates takes {"<id>": {"<field>": <value>}} as a file path or '-' for
stdin. Only entries ALREADY listed may be updated: an unknown id is an
error raised before anything is written, because creating an index entry
is the job of the skill that authors the document it points at.

A value is either a SCALAR or a LIST OF SCALARS. A scalar is written as
text, so a status of "no" stays a string and an id of "0001" keeps its
zeros instead of becoming a bool or an int. A list is written as a YAML
sequence — depends_on: [FEAT-1-1, FEAT-1-2] — which is what the
dependency fields hold. Anything else (a nested object, or a list
containing one) is refused, naming the entry and field, rather than
written in a form that cannot be read back.

Re-running an update is how a field is repaired: the value is replaced
outright, so a dependency list that an older ape stored as the quoted
string '[FEAT-1-2 FEAT-1-3]' becomes a real sequence again without
hand-editing index.yaml.

The rendered index is round-trip parsed before it replaces the file, and
the write is atomic — a crash mid-write cannot truncate an index. Key
order and comments survive, which a PyYAML round trip does not manage.

Exit codes:
  0  applied
  1  unknown id, unreadable updates, or an unwritable index (nothing written)

Examples:

```
  echo '{"ADR-0001":{"status":"superseded"}}' | ape adr update --updates -
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--generated-at` | string | `—` | Timestamp to write as generated_at (default: resolved config timestamp) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--updates` | string | `-` | JSON file with per-entry field deltas, or '-' for stdin |

## ape adr verify

Verify the adrs registry against its index

```
ape adr verify [flags]
```

Exactly five checks, and no others:

  1. registry.orphan_record / registry.phantom_entry
     set equality between the record directory and index.yaml, both
     directions
  2. registry.file_unresolved
     every index file: resolves, relative to the index's own directory
  3. registry.duplicate_id
     duplicate ids, in the index and on disk
  4. registry.record_unparseable
     the record parses as frontmatter at all
  5. registry.entry_incomplete
     every index entry carries each field its family's index schema
     requires (file: excepted: its absence is check 2).
     `ape registry backfill --all` fills the ones its record states

Check 5 is presence only. No type or value validation, no field drift, no
tag comparison, no updated_at comparison — those are judgment, and a
verifier that wanders into them stops being trustworthy.

An index.yaml that is absent while records exist is reported once, as
registry.index_missing: the degenerate case of check 1, not a fifth check.
One finding beats one orphan per record, which would bury the only fact
that matters.

Findings travel in the payload. Exit is 0 even with findings unless
--strict is passed, so this is safe to call from anywhere.

Examples:

```
  ape adr verify --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--strict` | bool | `false` | Exit 1 when there are findings (default: report and exit 0) |

## ape bootstrap

Bootstrap governance artifacts from traits

```
ape bootstrap [flags]
```

Bootstrap a project's governance artifacts by composing traits from the catalog.

Examples:

```
  ape bootstrap --traits go-service,http-api
  ape bootstrap --no-picker --traits go-service --dry-run
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--dry-run` | bool | `false` | Print what would be generated without writing files |
| `--no-picker` | bool | `false` | Disable the interactive trait picker (TUI) |
| `--on-conflict` | string | `first` | Conflict resolution strategy: first\|last\|all\|error |
| `--out` | string | `.` | Output directory for generated artifacts |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--traits` | string | `—` | Comma-separated list of trait names |

## ape capability

Inspect and maintain the capability registry

```
ape capability
```

Aliases: `capabilities`

Subcommands:

- `backfill` — Fill absent required fields of existing capabilities index entries from their records
- `list` — List capabilities from the registry index
- `sync` — Reconcile the capabilities index against records on disk
- `update` — Apply field deltas to existing capabilities index entries
- `verify` — Verify the capabilities registry against its index

## ape capability backfill

Fill absent required fields of existing capabilities index entries from their records

```
ape capability backfill [flags]
```

Complete the index entries that already exist. A field the family's index
schema requires that is ABSENT from an entry is copied in from that
entry's record: its frontmatter, or its file name for slug. It is the same
rule sync uses when it adds an entry.

It does nothing else. No entry is added, removed or repointed, a value
that is present is never changed (even an empty one), and file: is never
filled, because an entry with no file is sync's repair. That narrowness is
the point: this runs unattended as a framework migration on
`ape framework update`, where a structural index change is not wanted.

A required field the record has no value for stays absent and is reported
as a gap. An entry whose record is absent or unreadable is also a gap.
Gaps are not pending work: nothing on disk can close them, so re-running
finds the same ones.

--check writes nothing and answers in its exit code: 0 when there is
nothing to fill, 1 when fills are pending. Those two are the only answers
about the index. Anything else means the check itself failed: 2 for an
unknown family or an index or record that cannot be read, 4 with no
project config.

Examples:

```
  ape capability backfill --check
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--check` | bool | `false` | Write nothing; exit 0 when nothing is fillable, 1 when fills are pending (2 or 4 if the check itself failed) |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape capability list

List capabilities from the registry index

```
ape capability list [flags]
```

Examples:

```
  ape capability list --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape capability sync

Reconcile the capabilities index against records on disk

```
ape capability sync [flags]
```

Reconcile index.yaml against the records on disk: records with no entry
are added, entries whose id no record claims are removed, and an entry
whose file: no longer resolves is repointed at the record claiming its id.

This is the repair for the findings `verify` reports, and nothing more —
it copies what a record's own frontmatter states and invents no titles,
statuses or any other field. A renamed record keeps its authored entry
rather than being dropped and re-added.

An added entry carries every field of the family's index schema the record
has a value for: its frontmatter, or its file name for slug. A required
field with no value is left out and listed in the change's missing. An
entry that is already listed is never completed here; that is
`backfill`.

--check makes it a dry run: the same diff, nothing written, and the answer
in the exit code: 0 when there is nothing to change, 1 when changes are
pending, 2 when the check itself failed (an unknown family, or an index
that cannot be read), 4 with no project config. Withheld removals alone
exit 0: sync cannot make them. generated_at moves only when something
else did.

Examples:

```
  ape capability sync --check
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--check` | bool | `false` | Report the diff without writing; exit 0 nothing to change, 1 changes pending (2 or 4 if the check itself failed) |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape capability update

Apply field deltas to existing capabilities index entries

```
ape capability update [flags]
```

Apply per-entry field deltas to index.yaml and refresh generated_at.

--updates takes {"<id>": {"<field>": <value>}} as a file path or '-' for
stdin. Only entries ALREADY listed may be updated: an unknown id is an
error raised before anything is written, because creating an index entry
is the job of the skill that authors the document it points at.

A value is either a SCALAR or a LIST OF SCALARS. A scalar is written as
text, so a status of "no" stays a string and an id of "0001" keeps its
zeros instead of becoming a bool or an int. A list is written as a YAML
sequence — depends_on: [FEAT-1-1, FEAT-1-2] — which is what the
dependency fields hold. Anything else (a nested object, or a list
containing one) is refused, naming the entry and field, rather than
written in a form that cannot be read back.

Re-running an update is how a field is repaired: the value is replaced
outright, so a dependency list that an older ape stored as the quoted
string '[FEAT-1-2 FEAT-1-3]' becomes a real sequence again without
hand-editing index.yaml.

The rendered index is round-trip parsed before it replaces the file, and
the write is atomic — a crash mid-write cannot truncate an index. Key
order and comments survive, which a PyYAML round trip does not manage.

Exit codes:
  0  applied
  1  unknown id, unreadable updates, or an unwritable index (nothing written)

Examples:

```
  echo '{"CAP-1":{"status":"superseded"}}' | ape capability update --updates -
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--generated-at` | string | `—` | Timestamp to write as generated_at (default: resolved config timestamp) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--updates` | string | `-` | JSON file with per-entry field deltas, or '-' for stdin |

## ape capability verify

Verify the capabilities registry against its index

```
ape capability verify [flags]
```

Exactly five checks, and no others:

  1. registry.orphan_record / registry.phantom_entry
     set equality between the record directory and index.yaml, both
     directions
  2. registry.file_unresolved
     every index file: resolves, relative to the index's own directory
  3. registry.duplicate_id
     duplicate ids, in the index and on disk
  4. registry.record_unparseable
     the record parses as frontmatter at all
  5. registry.entry_incomplete
     every index entry carries each field its family's index schema
     requires (file: excepted: its absence is check 2).
     `ape registry backfill --all` fills the ones its record states

Check 5 is presence only. No type or value validation, no field drift, no
tag comparison, no updated_at comparison — those are judgment, and a
verifier that wanders into them stops being trustworthy.

An index.yaml that is absent while records exist is reported once, as
registry.index_missing: the degenerate case of check 1, not a fifth check.
One finding beats one orphan per record, which would bury the only fact
that matters.

Findings travel in the payload. Exit is 0 even with findings unless
--strict is passed, so this is safe to call from anywhere.

Examples:

```
  ape capability verify --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--strict` | bool | `false` | Exit 1 when there are findings (default: report and exit 0) |

## ape change

The unit of change: new, check, start, verify, review, accept, block, archive

```
ape change
```

A change is development/changes/<id>/change.md (written by the model) plus state.json
(written only by ape). Its lifecycle:

  new → draft ──start──▶ in-progress ──verify green──▶ verified ──review pass──▶ reviewed ──archive──▶ archived
                              ▲   │                        │                         │
                              └───┴── verify red ◀─────────┴──── review block ◀──────┘
  any status but archived ──block──▶ blocked ──start──▶ in-progress
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
  - verify, review, accept and block record a started change's state, and need HEAD on
    change/<id> (else exit 1, code change.wrong-branch: `git switch change/<id>`)
  - new, and the milestone and decision-accept records, need HEAD off every change
    branch (code branch.change-branch: `git switch <base>`)
  - archive rebases change/<id> onto the base branch and squash-merges it; abandon
    deletes it. The base branch only ever receives verified, reviewed changes.
  - a change's status is read from its own branch when HEAD is elsewhere, so
    `ape status` on the base branch shows every change's real status

Exit codes for every subcommand: 0 ok · 1 check failed (one-line remediation) · 2 usage ·
3 conflict (another change claims the same requirement, or the delta no longer applies;
nothing written).

Subcommands:

- `abandon` — Move a change to the archive as abandoned, merging no delta; commit
- `accept` — Accept a human-review task
- `archive` — Rebase change/<id> onto its base, squash-merge it with the delta and the archive move as one commit
- `block` — Set blocked, with the reason shown by ape status
- `check` — Validate change.md: shape, caps, delta against specs, a check on every task
- `diff` — Show the specs as archive would leave them (unified diff per capability)
- `new` — Validate the id and write the change.md skeleton and state.json (draft)
- `review` — Record a review verdict, or run the review in its own fresh Claude Code session
- `start` — Check green + milestone approved → create and check out change/<id>, record the baseline, set in-progress
- `verify` — Run the task checks and the effective spec at HEAD; verified iff all exit 0

## ape change abandon

Move a change to the archive as abandoned, merging no delta; commit

```
ape change abandon <id> --reason "<text>" [flags]
```

Drop a change: its folder moves to development/archive/<YYYY-MM-DD>-<id>/ with
status abandoned and the reason recorded in state.json; its delta is NOT merged into
specs/. Committed as "apex: <id> abandoned" with trailer "Change: <id>". The id stays
taken. Abandon a blocked change only after planning its replacement, or with the reason
none is needed.

A started change (D38): abandon runs from its branch change/<id> — then it switches to the
base_branch — or from any other non-change branch; the tree must be clean (uncommitted
files inside the change's own folder are committed on its branch first). The archived
folder holds the change's files as its branch last had them, the move is committed on the
base branch, change/<id> is deleted and its last commit recorded as branch_tip in the
archived state.json. None of the change's code reaches the base branch. A draft never
started (no branch) is abandoned on the branch HEAD is on, as before.

Exit codes: 0 abandoned · 1 no such change, archived, HEAD on another change's branch,
dirty tree, or git failed · 2 usage · 3 the archive folder already exists.

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

Accept a human-review task

```
ape change accept <id> --task <T> --by <who> [flags]
```

Record that a person accepted a human-review task (one whose check is "human-review").
Archive requires every human-review task accepted, unless --allow-pending-human.
HEAD must be on the change's branch, change/<id> (D38; else exit 1, code
change.wrong-branch): the acceptance is committed there.

Exit codes: 0 recorded · 1 the task has a command check, or HEAD not on change/<id> ·
2 usage (no such task).

Examples:

```
  ape change accept add-tenant-delete --task T2 --by diego
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--by` | string | `—` | who accepts (required) |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |
| `--task` | string | `—` | task id, e.g. T2 (required) |

## ape change archive

Rebase change/<id> onto its base, squash-merge it with the delta and the archive move as one commit

```
ape change archive <id> [flags]
```

Archive a reviewed change onto its base branch (trial decision D38):

  1. needs status reviewed (review pass) and the plan frozen at start unchanged; human-
     review tasks must be accepted, unless --allow-pending-human (recorded)
  2. runs with HEAD on change/<id>, or on another non-change branch (it switches to
     change/<id>); the tree must be clean — uncommitted files inside the change's own
     folder (review.md a review left) are first committed on change/<id> as
     "apex: <id> notes"; anything else refuses (code tree.dirty)
  3. refused if code changed on change/<id> since the review (review.stale)
  4. rebases change/<id> onto the base branch as it is now; on a conflict the rebase is
     aborted, the change is blocked naming the conflicting files (committed on its
     branch), and archive exits 3 (archive.rebase-conflict) — nothing reaches the base
  5. re-runs verify when the code moved since it — the base had moved (red → the change
     goes back to in-progress on its branch, exit 1, counted as an attempt; a green one
     keeps it reviewed: the review judged the change's own code)
  6. applies the delta to development/specs/: ADDED appends, MODIFIED replaces, REMOVED
     deletes; a new capability gets specs/<cap>/spec.md (Purpose from the change's Why).
     Any conflict — including a MODIFIED that would drop a scenario the current spec has,
     or change its check, or a MODIFIED/REMOVED requirement whose spec block changed since
     start (another change archived first: re-plan and re-start) — → exit 3 and nothing
     reaches the base branch
  7. switches to the base branch, `git merge --squash change/<id>`, writes the specs,
     moves the folder to development/archive/<YYYY-MM-DD>-<id>/ (status archived,
     branch_tip = change/<id>'s last commit) and commits everything as ONE commit —
     subject the change's # title, trailer "Change: <id>" (hooks skipped: every code
     commit on the branch already ran them); then deletes change/<id>. If anything fails
     on the base branch, it is reset to where it was and change/<id> checked out again.

The base branch gets exactly one commit per archived change; ape's status commits stay on
the change branch, squashed away.

Exit codes: 0 archived (prints the commit) · 1 not reviewed, re-verify red, stale review,
pending human-review, dirty tree, no change branch, or git failed · 2 usage · 3 the
rebase conflicts or the delta no longer applies.

Examples:

```
  ape change archive add-tenant-delete
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--allow-pending-human` | bool | `false` | archive with human-review tasks unaccepted (recorded in state.json) |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |
| `--timeout` | duration | `15m0s` | limit for each check command (exit 124 when exceeded) |

## ape change block

Set blocked, with the reason shown by ape status

```
ape change block <id> --reason "<text>" [flags]
```

Set a change blocked. The reason is what `ape status` shows: what blocks it, or the
question only a person can answer. A blocked change lets the stop hook allow a stop.
`ape change start <id>` resumes it. A started change is blocked on its branch,
change/<id> (D38; else exit 1, code change.wrong-branch), where it stays — re-plan it
there and start it again, or abandon it; a change with no branch (a draft) is blocked on
the branch HEAD is on, unless that is another change's.

Exit codes: 0 blocked · 1 no such change, archived, or the wrong branch · 2 usage.

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

  - frontmatter id = folder, milestone in roadmap.md, touches non-empty kebab slugs,
    decisions exist in decisions/, paths are valid globs
  - # title; ## Why and ## Out of scope non-empty; ## Delta; ## Tasks
  - change.md ≤ 8 KB and design.md ≤ 8 KB ("split this change")
  - ## Delta: "### ADDED|MODIFIED|REMOVED [<capability>] Requirement: <name>" blocks, or
    the single line "none — <reason>". The capability must be in touches. ADDED names must
    not exist in specs/<capability>/spec.md yet; MODIFIED/REMOVED must name an existing
    heading exactly. ADDED/MODIFIED carry ≥ 1 "#### Scenario:" with exactly one
    "- check: <command> | human-review"; REMOVED carries "Reason:". A MODIFIED block
    keeps every scenario of the requirement it replaces, each with the same check
    (it may add scenarios); changed behaviour is REMOVED + ADDED under a new name.
    A name that nearly matches an existing heading (case, spacing, a typo) gets a
    "did you mean"; an ADDED that nearly matches one is refused.
  - nothing archive would drop: no prose before the first delta block, nothing but
    "Reason:" in a REMOVED block
  - a delta that REMOVES every requirement of a capability needs "retire: [<cap>]" in
    the frontmatter (archive then deletes its spec); retire: lists only such capabilities
  - ## Tasks: "- T<n> <what> — check: <command> | human-review", no checkboxes, unique ids

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
capability diffs from /dev/null). For review and for handoff: what the living spec will
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
ape change new <id> --milestone <M> --touches a,b [--paths globs] [flags]
```

Create development/changes/<id>/ with a change.md skeleton (frontmatter id, milestone,
touches, paths; # title; ## Why, ## Out of scope (pre-filled with the milestone's Out of
scope bullets), ## Delta, ## Tasks, ## Notes) and state.json with status draft, and
commit both as "apex: <id> draft" (trailer "Change: <id>"). Filling change.md in is the
author's own commit. A change is planned on the base branch: refused while HEAD is on a
change branch, change/<other> (exit 1, code branch.change-branch; `git switch <base>`).

<id> is a verb-led kebab slug, ^[a-z][a-z0-9-]{2,39}$, unique across active AND archived
changes (development/archive/, and an older ape's development/changes/archive/).
--touches lists the capabilities the change adds to or modifies (kebab slugs; new ones
are fine — a capability is born from its first change). --paths are globs used for
overlap detection. Overlap (shared touches or paths) with another unarchived change
is a warning, not a failure.

Then fill change.md and run `ape change check <id>`.

Exit codes: 0 ok · 1 id taken (incl. archive), milestone not in roadmap, or HEAD on a
change branch · 2 usage.

Examples:

```
  ape change new add-tenant-delete --milestone M2 --touches tenants --paths 'internal/api/**'
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |
| `--milestone` | string | `—` | milestone id from roadmap.md (required) |
| `--paths` | string | `—` | comma-separated globs, for overlap detection |
| `--touches` | string | `—` | comma-separated capability slugs (required) |

## ape change review

Record a review verdict, or run the review in its own fresh Claude Code session

```
ape change review <id> (--verdict pass|block --findings <file> | --run [--wait 9m] [--model M]) [flags]
```

Record the review of a verified change. --findings names the review file (normally
development/changes/<id>/review.md); it must exist and is recorded by path.

  pass   needs a green verify whose code has not moved since → status reviewed
  block  → status in-progress (fix, verify, review again)

HEAD must be on the change's branch, change/<id> (D38; else exit 1, code
change.wrong-branch): the verdict is committed there.

--run reviews the change in its own Claude Code session — a separate process with an
empty context and its own hooks, never a sub-agent of the session that built it:
ape starts `ape task apex-review --args <id>` detached (survives the caller's shell),
and that session writes review.md and records the verdict with --verdict/--findings.
It is the one sanctioned exception to the nesting rule: allowed from inside an ape
session, never from inside a review session (depth 1), skill fixed to apex-review.
  - on the change's branch: --run needs HEAD on change/<id>, the session runs in this
    checkout and records its verdict there; a session that leaves HEAD elsewhere
    has its verdict voided and the branch checked out again
  - read-only: the session runs with APE_REVIEW=<id>, and `ape hook pre-tool` denies
    every edit inside the project except changes/<id>/review.md (outside the project,
    e.g. a throwaway copy to break, is allowed)
  - post-run assertion: a session that committed or left anything but review.md (and
    the state.json ape commits) has its verdict voided (recorded under review_voided)
  - --wait (default 9m, under the Bash tool's 10-minute cap) bounds one call; exit 75
    means still running: call `ape change review <id> --run` again to keep waiting —
    it attaches to the running session, it never starts a second one
  - the session's cost is its own ape task manifest (ape status sums it)
  - model and effort: --model/--effort; else _apex/config.yaml models.apex-review
    ("apex-review: sonnet" or "apex-review: {model: sonnet, effort: high}"; a config
    model without an effort does not take the asking session's effort); else the
    model and effort of the ape session that asked (so a review runs like the arm
    that requested it); else, for the effort, _apex/config.yaml effort:; else claude's
    defaults. The effort is passed as claude --effort. The review session's manifest
    records each and its source under review_launch (and, the same, under launch).

Exit codes: 0 recorded / review passed · 1 not verified, code changed since the verify,
review block (--run: findings in JSON "outcome"), verdict voided, or no verdict · 2 usage
(bad verdict, findings file missing, --run inside a review session) · 75 --run: still
running, call again.

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
| `--wait` | duration | `9m0s` | --run: how long this call waits for the session (exit 75 when it is still running) |

## ape change start

Check green + milestone approved → create and check out change/<id>, record the baseline, set in-progress

```
ape change start <id> [flags]
```

Start a draft (or resume a blocked) change:

  1. `ape change check <id>` must be green, and its milestone approved
  2. refused with exit 3 if another active change (in-progress, verified, reviewed —
     not blocked; read from its own branch) also ADDs, MODIFIES or REMOVES one of the
     same requirements
  3. creates the branch change/<id> at HEAD and checks it out (trial decision D38): the
     tree must be clean (ignored files aside), HEAD on a branch (not detached, not another
     change's change/<other>); records that branch as base_branch, and base_sha (HEAD)
     and started_at (both kept when resuming a blocked change). A refused start leaves no
     branch and HEAD where it was.
  4. runs every check verify will run once, as the baseline (which were already green):
     the command tasks and the effective spec's scenario checks; from the project root
     with sh -c; output under _apex/state/logs/<id>/. With "regression: <cmd>" in
     _apex/config.yaml (the project's own test suite) it runs that too, the same way, and
     records it in state.json (regression_baseline: cmd, exit, output sha256, sha); a red
     regression baseline is recorded, it does not block start
  5. sets in-progress, freezes the plan (SHA-256 of change.md outside ## Why and ## Notes), fingerprints
     the spec block of every requirement it MODIFIES or REMOVES, commits state.json
     ("apex: <id> in-progress") on change/<id>, and resets the attempts count since start
     (_apex/state/attempts.json)

All of the change's work — code commits, ape's status commits — then happens on
change/<id>. Start on a change whose branch exists, from another branch (the base, say),
checks change/<id> out first (clean tree); when the change is started and nothing needs
re-baselining it only resumes there (JSON "resumed": true). Re-start keeps base_branch.
A blocked change is re-planned on its branch (change.md there), then started again.

Re-start: once the plan is frozen, verify and archive refuse while change.md differs from
it anywhere but ## Why and ## Notes (frontmatter, title, Out of scope, Delta, Tasks, …). Running start again on an in-progress, verified or reviewed change
re-baselines: recorded under "replans" in state.json, verify and review cleared, new or
changed checks baselined now (unchanged ones keep their original baseline, and so does the
regression command unless it changed), fingerprints re-taken. It applies when the plan
changed, when a spec block the change modifies or removes was changed by another change
since start, or when the regression command has no baseline for it (configured or changed
since start); otherwise it is refused.

Exit codes: 0 started (or resumed) · 1 check red, milestone not approved, wrong status,
dirty tree, detached HEAD, HEAD on another change's branch, or git failed · 2 usage ·
3 requirement conflict with another active change.

Examples:

```
  ape change start add-tenant-delete
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |
| `--timeout` | duration | `15m0s` | limit for each check command (exit 124 when exceeded) |

## ape change verify

Run the task checks and the effective spec at HEAD; verified iff all exit 0

```
ape change verify <id> [flags]
```

Run, at HEAD, every task's command check and every scenario check of the effective
spec — the spec as it will be once this change is archived: each current requirement the
change does not MODIFY or REMOVE (regression), plus the scenarios of its own ADDED and
MODIFIED blocks. human-review checks are not run. A command shared by several checks runs
once. Results (sha, exit, seconds, output sha256; kind, capability, requirement, scenario
for scenario checks) are recorded in state.json; output under _apex/state/logs/<id>/.
What a check leaves in the tree outside development/ — files it created, tracked files it
changed — is undone after it and reported (JSON "stray_files"); files that were there
before it ran are never touched.
With "regression: <cmd>" in _apex/config.yaml, the regression command runs too (recorded
in the results with kind "regression") and is judged against the change's regression
baseline (JSON "regression": {cmd, exit, baseline_exit, pre_existing_red, failed}): it
fails verify only when it is newly red — green at baseline, red now — or when the change
has no baseline for this command (re-start records one). Red at baseline and still red
is reported (pre_existing_red), not failed: the planner roadmaps it.
The working tree must hold no uncommitted changes outside development/ — commit first
(trailer "Change: <id>"): a verify belongs to a sha. HEAD must be on the change's branch,
change/<id> (D38; else exit 1, code change.wrong-branch).

All checks exit 0 → status verified (a reviewed change whose code has not moved
since its review stays reviewed), state.json committed. Any red → the failing checks' last
lines are printed. Green at baseline: the change's own checks (tasks, delta scenarios) that
already passed when they were baselined, before the code, are listed (JSON
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
Refused while change.md differs from the plan frozen at start outside ## Why and ## Notes
(re-start first). human-review tasks are never run; those not yet accepted are listed
as pending.

Exit codes: 0 all command checks green · 1 a check is red (the attempts cap reached
included), uncommitted code, or wrong status · 2 usage.

Examples:

```
  ape change verify add-tenant-delete
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |
| `--timeout` | duration | `15m0s` | limit for each check command (exit 124 when exceeded) |

## ape chat

Bridged claude REPL with hooks captured to a runlog

```
ape chat [flags]
```

Spawn claude as a child of ape with the ape bridge attached.
Bridge hooks (PreToolUse, PostToolUse, UserPromptSubmit, Stop, and
friends) are captured to <project>/_output/ape/chats/<id>/ alongside
pipeline runs.

ape chat must be run from a project root (a directory containing
_apex/config.yaml).

While attached:
  /exit, /quit       exit claude (default slash commands)
  Ctrl+D in claude   exits the REPL

ape exits when claude exits. The chat session is bound to this
terminal for its lifetime — there is no detach/reattach. To run
claude in the background, use a real terminal multiplexer
separately (e.g. wrap ape chat in tmux or screen).

Exit codes: 0 success · 1 claude/bridge failure · 2 usage or preflight
error (no _apex/config.yaml, bad cwd).

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
session when no step, stage, pipeline or --effort value is declared.

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

In an APEX v1 project the table applies to ape pipeline only: ape task
passes claude --effort from --effort, models.<skill>.effort, the parent
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
pipeline against folders nobody chose.

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

## ape context

Inspect the project-context document

```
ape context
```

project-context.md is the standards document every skill loads whole —
architecture, ADR and pattern passes, both story batches, the reviewers.
It grows by append, and it meets the same two Read limits team-memory.md
does: past ~25,000 tokens (about 72 KB of markdown) a Read returns only
part of it, and at Claude Code's 256 KiB byte cap a Read fails outright.
The same two budgets apply. For a file skills load whole, the soft
budget (~14k tokens) is the one that keeps it inside a single Read.

  check  size against the soft budget and hard ceiling, from a stat alone

Subcommands:

- `check` — Report project-context size against the soft budget and hard ceiling

## ape context check

Report project-context size against the soft budget and hard ceiling

```
ape context check [flags]
```

Classify {development_folder}/project-context.md against two budgets,
from an os.Stat alone — the file is never read, which is what lets the
generator take the measurement BEFORE the full read it would otherwise
die on.

  state: absent      no project-context.md yet (the governance pipeline
                     writes it; before that, not a problem)
  state: ok          under the soft budget
  state: over-soft   compaction is due; past ~72 KB a whole-file Read is
                     already partial
  state: over-hard   compaction is overdue: far past what one Read shows
                     and nearing Claude Code's 256 KiB byte cap, where a
                     Read fails outright

The budgets are the pair 'ape memory check' already enforces on
team-memory.md, from one set of constants rather than two restatements of
the numbers: soft 40960 B, hard 204800 B.

Only the canonical path is measured. Reader skills carry a
'**/project-context.md' fallback for a lifted project that put the file
somewhere else; a size gate has to name the file it measured, so an
'absent' verdict here prints the path it looked at rather than searching
for another candidate.

EXIT 0 BY DEFAULT, whatever the state — the same contract 'ape memory
check' keeps, and for the same reason: the framework's prose convention
is "on non-zero exit: HALT", so a failing exit would abort the generator
at exactly the moment compaction is due.

--fail-at opts into a non-zero exit for CI, which wants one:
  never  (default) always exit 0
  soft   exit 1 at over-soft or worse
  hard   exit 1 at over-hard

The token count in the output is bytes/2.9 (the ratio measured on APEX
markdown), an estimate, and nothing gates on it.

Examples:

```
  ape context check
  ape context check --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--fail-at` | string | `never` | Exit 1 at this state or worse: never\|soft\|hard |
| `--hard` | int64 | `0` | Hard ceiling in bytes (default 204800) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--soft` | int64 | `0` | Soft budget in bytes (default 40960) |

## ape costs

Show this project's Claude cost rollup

```
ape costs [flags]
```

Reads <project>/_output/ape/cost-rollup.json and prints
totals — today, this week, all-time — broken down per pipeline + chat.

  ape costs                          Project rollup (human / json).
  ape costs run <run-id>             Single pipeline run (reads manifest.yaml).
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
- `roll` — Rebuild <project>/_output/ape/cost-rollup.json from on-disk run / chat artefacts
- `run` — Show cost for a single pipeline or task run (reads its manifest.yaml)
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

Artefacts covered:
  _output/{pipelines,tasks}/<name>/<run-id>/manifest.yaml
  _output/ape/prompts/<prompt-id>/prompt.yaml

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

Rebuild <project>/_output/ape/cost-rollup.json from on-disk run / chat artefacts

```
ape costs roll
```

## ape costs run

Show cost for a single pipeline or task run (reads its manifest.yaml)

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

Subcommands:

- `accept` — Accept a proposed ADR and fix its text
- `new` — Allocate the next ADR number and write the skeleton

## ape decision accept

Accept a proposed ADR and fix its text

```
ape decision accept <ADR-NNNN> --by <who> [flags]
```

Set a proposed ADR's Status to accepted and record the text it was accepted with in
_apex/state/decisions.json; both are committed as "apex: ADR-NNNN accepted". From
then on `ape project check` fails if the ADR's text (everything but its Status line)
changes: supersede it instead. An ADR accepted by hand is held to the text git first
committed it accepted with.

A person's acceptance is recorded on the base branch (D11, D38): refused on a change's
branch, change/<id> (code branch.change-branch; `git switch <base>` first).

Exit codes: 0 accepted · 1 no such ADR, not proposed, malformed, or HEAD on a change
branch · 2 usage.

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

## ape deferred

The deferred-work record store

```
ape deferred
```

One file per deferred record under {development_folder}/deferred/, with
YAML frontmatter and a verbatim Markdown body.

This replaces a single 456,144-byte deferred-work.md whose only eviction
mechanism was deletion — git shows 524 records removed in one commit — and
which had grown too large for the skills that append to it to read.

One file per record is not a style choice: the single-store alternative
returns ZERO records when handed one malformed entry, while this shape
loses exactly the one bad file.

The store sits OUTSIDE {implementation_folder} deliberately. Ten skills
glob {implementation_folder}/**/*.md across 17 sites, and one record file
per deferred item under that folder would feed every one of them.

Subcommands:

- `close` — Discharge a record, moving it to closed/
- `discard` — Retire a record as never-needed, moving it to closed/
- `ingest` — Store defer bullets as records (bullets on stdin)
- `list` — List deferred records (open by default)
- `migrate` — Convert the legacy deferred-work.md into record files
- `recover` — Recover ledger records deleted before the migration, into closed/
- `repair` — Complete or retire free-form records (judgment, on opus)
- `verify` — Check the store: invariants, and candidates for a human

## ape deferred close

Discharge a record, moving it to closed/

```
ape deferred close <id> [flags]
```

Mark a record closed and move it to closed/. The record is NEVER deleted:
deletion is what destroyed the audit trail the first time, and an LLM that
cannot see a closed defer re-files it.

The body gains a discharge marker in the shape apex-pattern-reconciliation
established, and keeps everything it already said.

Closing is judgment — it requires re-verifying the record's premises
against HEAD — so this is invoked by a person or a skill, never
automatically. 'ape deferred verify' flags CANDIDATES and never closes one.

Examples:

```
  ape deferred close DW-20260822-a1b2c3 --by "54-2"
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--by` | string | `—` | Story key or reason that discharged it (required) |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape deferred discard

Retire a record as never-needed, moving it to closed/

```
ape deferred discard <id> [flags]
```

Mark a record discarded and move it to closed/. The record is NEVER
deleted, exactly as with 'close'.

DISCARD IS NOT CLOSE. They record incompatible claims:

  close    the work was done       -> resolved_by, resolved_at
  discard  the work was not needed -> discard_reason, discard_evidence

Using 'close' for a discard asserts that work happened, and it also
APPENDS A DISCHARGE MARKER TO THE BODY — which breaks the byte-identity
the migration's whole verification design exists to protect. This command
leaves the body untouched.

Nothing here re-derives the judgment. Deciding a deferred item is moot
requires re-verifying its premises against HEAD, so --reason is required
and --evidence carries what was checked. 'ape deferred verify' flags
CANDIDATES and never discards one.

The result is visible as 'ape deferred list --status discarded', and under
'--status closed' too, since both statuses have left the working set.

Examples:

```
  ape deferred discard DW-20260822-a1b2c3 \
    --reason "superseded by the 91-2 rewrite" \
    --evidence "pkg/a.go:12 no longer exists at HEAD"
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--evidence` | string | `—` | What was checked to establish that |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--reason` | string | `—` | Why the work is not needed (required) |

## ape deferred ingest

Store defer bullets as records (bullets on stdin)

```
ape deferred ingest [flags]
```

Read defer bullets and write one record per bullet.

Input comes from --body-file or stdin, NEVER from an argv string: 108 of
109 real bodies contain backticks, which shell-expand inside an argument.
--body-file is the form the framework calls, because
apex-review-story/steps/step-04-present.md:13 restricts shell constructs
inside code blocks and a plain path argument sidesteps the question
entirely. The skill writes the bullets with the Write tool (never a
heredoc) and passes the path.

EXIT-CODE CONTRACT — this command is reachable from apex-review-story's
emit path, where a non-zero exit converts a defer into a patch, raises
unfixed_patches, and DEMOTES THE STORY to in-progress. Therefore:

  - a bullet whose shape is unrecognised is stored verbatim as free-form,
    with a warning on stderr;
  - empty input is a rc-0 no-op;
  - only a genuine setup failure (an unwritable store) fails.

Nothing about the CONTENT of a defer can make this command exit non-zero.

Examples:

```
  ape deferred ingest --story 54-1 --skill apex-review-story --body-file /tmp/defer-54-1.txt
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--body-file` | string | `—` | File holding the bullets (default: stdin) |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--cycle` | int | `0` | Review cycle number |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--skill` | string | `—` | Skill that filed it (e.g. apex-review-story) |
| `--story` | string | `—` | Story key this defer was filed from |

## ape deferred list

List deferred records (open by default)

```
ape deferred list [flags]
```

Project the record set. Open records only unless --status says otherwise:
closed records stay on disk but leave the working set, which is what stops
an LLM re-filing work it already did.

Three statuses exist, not two. A record is open, closed (the work was done)
or discarded (the work was never needed) — and those last two are different
claims, so they are not interchangeable. --status closed matches BOTH, since
both have left the working set; --status discarded narrows to just the
discards.

--detail picks how much the human rendering shows; --output-format picks
the encoding. They are separate axes.

--id <id> reads ONE record by id, open or closed, ignoring every filter.
It is the by-id read the store has had internally and no command
exposed, and it is what a caller naming a record — ape change --fixes
<id> — needs to check that the id it was handed is real. An unknown id
exits 2 rather than printing an empty list, because "no such record"
and "no records match" are different answers.

Examples:

```
  ape deferred list --owner platform
  ape deferred list --status all --output-format json
  ape deferred list --id DW-20260919-a1b2c3 --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--detail` | string | `brief` | Human rendering detail: brief\|full |
| `--group` | string | `—` | Only records in this group |
| `--id` | string | `—` | Read one record by id, open or closed, ignoring every other filter |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--owner` | string | `—` | Only records with this owner |
| `--path` | string | `—` | Only records anchored under this path prefix |
| `--status` | string | `open` | Which records: open\|closed\|discarded\|all (closed includes discarded) |
| `--story` | string | `—` | Only records filed from this story |

## ape deferred migrate

Convert the legacy deferred-work.md into record files

```
ape deferred migrate [flags]
```

Convert a single-file deferred-work.md into one file per record.

Four properties, all asserted rather than assumed:

  VERIFIED BEFORE WRITE   every significant line of the ledger must come
                          back out in something this writes — a record
                          body, a record's source_heading, or the preamble
                          file — N records parsed must equal N files
                          written, and every body must survive a round
                          trip, or NOTHING is written. The first of those
                          three is checked against the LEDGER, not just
                          against the parser's own output: a conversion
                          that silently lost text while reporting success
                          is the one failure here that git cannot undo.
                          Line endings are the single normalisation — a
                          CRLF ledger yields LF bodies.
  IDEMPOTENT              detected from disk state — does the store hold
                          records, is the legacy file already a stub. No
                          version marker is stored, so nothing can drift.
  NEVER DELETES THE SOURCE  the legacy file becomes a short signpost; its
                          content stays in git, and its preamble is kept
                          verbatim beside the records.
  NO COMMIT               the files land in the working tree. You commit
                          them, as one commit or two, however you like.

Each record carries the ledger heading it sat under verbatim, in
source_heading, alongside the source/source_story/created extracted from
it. A heading holds more than three fields can take, and that remainder is
often attribution.

A record whose tail does not match the expected shape keeps its full text
as the body and takes its title from the first line, with NO fields parsed
from it — 'free_form: true' means nothing in it was interpreted. Any real
ledger has records that land there. What fraction is not quoted here on
purpose: it is a property of your corpus, and a number measured against
someone else's would only invite you to trust it. 'ape deferred verify'
flags them and 'ape deferred repair' completes or retires them.

The migration MOVES cited text out of the folder it was living in. A
repo-wide gate scoped to that folder — anchor counts, citation ratchets —
will drop the moment this lands, without anything regressing. The
completion output says how much moved so that drop is explainable.

Re-migrating after a parser fix: record ids are derived from the ledger,
so a corrected parse yields different ids and the two sets cannot be
merged. Restore the ledger from git and delete the store directory; this
refuses to run into a store that still holds records rather than
interleaving two migrations on disk.

HISTORY RECOVERY IS ON BY DEFAULT, AND IS FIRST-RUN-ONLY. The migration
mines the ledger's git history for records removed from it and writes them
straight to closed/ as tombstones. The ledger's own preamble documents
'git log -p' as the recovery route; this automates exactly that. Failure to
read history is a warning, never fatal.

It defaults on because the choice is not symmetric. Recovery only ever
writes to closed/, so it cannot put anything in the working set; the cost
is one 'git show' per revision touching the ledger. But it runs only on the
FIRST migration — afterwards this command reports 'already migrated' and
returns before the recovery step — so skipping it forfeits that history
permanently. One project lost 180 recoverable records that way.

Pass --no-recover-deleted to skip it. Use 'ape deferred recover' to run it
against a store that has already been migrated.

Examples:

```
  ape deferred migrate --dry-run
  ape deferred migrate --recover-deleted
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--dry-run` | bool | `false` | Parse and verify, writing nothing |
| `--from` | string | `—` | Legacy ledger path (default: resolved from config) |
| `--no-recover-deleted` | bool | `false` | Skip history recovery. This is a ONE-WAY choice: after the migration the history is unreachable |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--recover-deleted` | bool | `true` | Recover records removed from the ledger, from git history, into closed/ (default true) |

## ape deferred recover

Recover ledger records deleted before the migration, into closed/

```
ape deferred recover [flags]
```

Mine the legacy ledger's git history for records that were removed from
it, and write them to closed/ as tombstones.

The ledger's only eviction mechanism was deletion — closing a record
destroyed its own audit trail — which is the failure the record store
exists to end. This is the repair for the history that predates it.

WHY THIS IS A SEPARATE COMMAND. 'ape deferred migrate --recover-deleted'
does the same work, but only on the FIRST migration: afterwards migrate
reports 'already migrated' and returns before the recovery step, so the
flag is silently inert. The route out of that ("restore the ledger, remove
the store, re-migrate") costs every close, discard and repair edit made
since the migration. This does not.

SAFE TO RE-RUN. Recovery only ever writes to closed/, so it can never add
anything to the open working set. It de-duplicates against what is on disk
— open records AND existing tombstones — so a second run recovers nothing.

The ledger path is normally a stub by now. That is fine: git history is
read THROUGH the path, not out of the file at it, so every revision that
held the real ledger is still reachable. A repo with no history for that
path is a warning, never fatal.

Nothing is committed.

Examples:

```
  ape deferred recover --dry-run
  ape deferred recover
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--dry-run` | bool | `false` | Report what would be recovered, writing nothing |
| `--from` | string | `—` | Legacy ledger path (default: resolved from config) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape deferred repair

Complete or retire free-form records (judgment, on opus)

```
ape deferred repair [flags]
```

Dispatch the free-form records to a framework skill for completion.

The deterministic migration cannot finish a record that has no fields to
read. Deciding whether a messy note is real work, what it points at, and
whether it is already dead is JUDGMENT — so an LLM does it, in its own
phase, never inside the verified migration. That separation is what keeps
the migration assertable and revertible: a model's output can never
invalidate a byte-identity check.

Mechanism: this spawns apex-deferred-repair on opus through
the same PTY task runner `ape task` uses. The prompt lives in the
framework as a versioned, reviewable skill rather than a Go string
literal, so ape gains no HTTP client, no credentials and no model
constant. apex-defer-repair is accepted as the pre-rename name, so
an older framework install still works.

Three guards this command adds:

  It REFUSES when neither skill is installed, --dry-run included. A real
  dispatch is already caught by the runner's skill preflight, but a dry
  run never reaches the runner — so without this it would print a plan
  naming a skill that does not exist, say "no session spawned", and read
  as fine.

  It REFUSES without a TTY unless --force. It spends real money, and it
  should not do that from a script that did not ask — the same refusal
  'ape framework setup' already makes rather than seeding silently.

  The on-disk record count MUST NOT FALL. "discard never deletes" is an
  absolute rule that otherwise lives only in a prompt, and a prompt is not
  an enforcement mechanism. If records vanish, this says so and names
  them, so you can restore those paths before committing anything.

Nothing is committed.

Examples:

```
  ape deferred repair --dry-run
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--dry-run` | bool | `false` | Show the plan without spawning a session |
| `--force` | bool | `false` | Spawn the session even without a TTY |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape deferred verify

Check the store: invariants, and candidates for a human

```
ape deferred verify [flags]
```

Aliases: `lint`

Two kinds of finding, and the tag on each is the important part:

  confidence: certain    a fact. Schema problems, related[]/supersedes[]
                         pointing at records that do not exist, and an open
                         record whose own body says it was discharged.
  confidence: candidate  a heuristic, NEVER auto-actionable. A dead anchor
                         (the record may be moot, or the code may just have
                         moved), a trigger naming a story that is now done
                         (the closing condition MAY have fired), a
                         near-duplicate title, a free-form record.

An open record that says it was discharged is TWO checks, not one, because
the two mean opposite things and need opposite remediations:

  deferred.closure_marker_in_open_record     the work was DONE
  deferred.superseded_marker_in_open_record  the work was OVERTAKEN, never
                                             done — a discard, not a close

Branch on the check name. Re-deriving it from the message is what a consumer
got backwards, closing records whose whole point was that nobody did them.

Nothing here ever closes a record. Closing requires re-verifying the
premises against HEAD, which is judgment — and on the reference ledger an
unbiased sample of 20 records found 5 already delivered, including its own
flagship entry, whose defect had been eliminated four weeks earlier.

Exit 0 even with findings; --strict makes it 1.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--strict` | bool | `false` | Exit 1 when there are findings (default: report and exit 0) |

## ape doc

Shard, assemble and survey Markdown documents

```
ape doc
```

Aliases: `docs`

Markdown document operations.

  verify    refuse-to-shard gate: duplicate heading slugs at a level
  shard     split a document into section files, rewriting relative links
  assemble  concatenate the shards back, reversing the rewrite
  analyze   survey a set of source documents: sizes, groups, routing

Subcommands:

- `analyze` — Survey source documents: sizes, groups, routing
- `assemble` — Concatenate shards back into one document
- `shard` — Split a document into section files
- `verify` — Check a document for duplicate heading slugs

## ape doc analyze

Survey source documents: sizes, groups, routing

```
ape doc analyze <path|dir|glob>... [flags]
```

Enumerate source documents and report sizes, estimated tokens, detected
types, suggested groupings, a routing recommendation and a split
prediction.

Inputs may be file paths, directories (walked for .md/.txt/.yaml/.yml/
.json) or glob patterns. node_modules, .git, __pycache__, .venv, .claude,
.cursor and .vscode are never source documents.

Routing is 'single' when the corpus is BOTH at or under the file limit and
at or under the token limit; otherwise 'fan-out'. The split prediction
estimates a distillate at a third of its sources.

Every token number here is bytes/2.9 — the ratio Claude Code's own count
gives on APEX markdown, and the one ape memory check uses. It is still an
estimate, labelled as one. The three thresholds are flags so the boundaries are
testable without building a 15k-token fixture; the defaults are the ones
the framework has always used.

This command carries no --cwd. It resolves nothing from the project's
config — every input is an explicit path argument — so there is no
project for --cwd to select. Relative paths resolve against the
working directory, as they would for any other tool.

Examples:

```
  ape doc analyze _output/handoffs --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--single-max-files` | int | `0` | File count at or below which routing is single (default 3) |
| `--single-max-tokens` | int64 | `0` | Estimated tokens at or below which routing is single (default 15000) |
| `--split-min-tokens` | int64 | `0` | Estimated distillate tokens above which a split is likely (default 5000) |

## ape doc assemble

Concatenate shards back into one document

```
ape doc assemble <dir> <file> [flags]
```

Reverse a shard: concatenate the section files in index.md order and
strip the ../ that sharding added.

index.md supplies the ordering and its preamble, and is never itself
concatenated as a section. A ../ link that predated the shard survives
untouched, because the strip only applies when the target actually resolves
one level up.

A section file named in the index but missing on disk is reported and
skipped rather than fatal — an incomplete assembly you can see beats none.

This command carries no --cwd. It resolves nothing from the project's
config — every input is an explicit path argument — so there is no
project for --cwd to select. Relative paths resolve against the
working directory, as they would for any other tool.

Examples:

```
  ape doc assemble development/planning/prd development/planning/prd.md
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape doc shard

Split a document into section files

```
ape doc shard <file> <dir> [flags]
```

Split a document at a heading level, one file per section, and rewrite
relative links to account for the extra directory depth.

It ALWAYS writes an index.md listing and linking every section file. That
is a contract, not a convenience: apex-shard-doc treats a missing index.md
as proof the command did not complete, and verifies it in its own step. A
replacement that split and rewrote links perfectly but omitted the index
would pass a round-trip test and then fail its real caller.

It REFUSES when two headings slugify to the same value, rather than writing
'foo-2.md' siblings that hardcoded consumers cannot distinguish. Nothing is
written in that case — fix the source document.

A document with no headings at the requested level becomes index.md whole,
so 'assemble' still has something to work from.

This command carries no --cwd. It resolves nothing from the project's
config — every input is an explicit path argument — so there is no
project for --cwd to select. Relative paths resolve against the
working directory, as they would for any other tool.

Examples:

```
  ape doc shard development/planning/prd.md development/planning/prd
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--level` | int | `2` | Heading level to split at |
| `--numbered` | bool | `false` | Prefix filenames with a zero-padded ordinal |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape doc verify

Check a document for duplicate heading slugs

```
ape doc verify <file> [flags]
```

Scan a document for two headings at the same level that slugify to the
same value.

This is a GATE, not a report — the one exception to the verify contract,
and for the same reason as 'ape story verify --file': its caller relies on
the non-zero exit to stop before writing anything. Downstream skills
reference shard files by exact slug, so a deduplicated 'foo-2.md' sibling
would silently break them; the source document has to be fixed instead.

Exit codes:
  0  no duplicate slugs
  1  duplicates found (each pair is printed with its line numbers)

This command carries no --cwd. It resolves nothing from the project's
config — every input is an explicit path argument — so there is no
project for --cwd to select. Relative paths resolve against the
working directory, as they would for any other tool.

Examples:

```
  ape doc verify development/planning/prd.md --level 2
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--level` | int | `2` | Heading level to check |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape doctor

Probe the local environment for prerequisites

```
ape doctor [flags]
```

Probe the local environment for prerequisites and report a per-check
verdict.

Doctor runs a fixed set of checks against the host (claude / git /
node / npx binaries, Playwright host compatibility, ~/.claude
writability) and the project at --cwd (framework metadata, installed
skills + pipelines, and the always-on operating-rules fragment +
CLAUDE.md managed block). Project-scoped checks degrade to INFO when run
outside a project root; the operating-rules checks only hard-fail when a
framework install that manages them has lost the fragment, import, or
apex-orchestrator skill.

Eight checks report on PROJECT DATA rather than on the host: whether the
config resolves at all (nothing else can see the project's artifacts
without it), registry drift, story frontmatter, tracker divergence,
whether the tracker's lock sidecar is gitignored, whether ape's own run
subtree is gitignored, team-memory size, and any pending project-data
migration. All eight degrade to INFO outside a project root.

The two "is it gitignored" rows report and never write. .gitignore is the
operator's file; a tool that edits it uninvited is worse than one that
points. Both also distinguish COMMITTED from merely unignored, because an
ignore line does not untrack anything — on a project that already
committed the path, adding the line changes nothing at all.

memory.size is one of only two Required checks in that group, deliberately:
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
                                can provide "ape story fields" with an
                                older flag set and still pass, so this is
                                a version-skew guard and not a statement
                                of version compatibility. A framework that
                                ships no manifest predates the contract
                                and the check reports a skip.
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
(<project>/_output/ape), so it can only be judged against a project ape has
actually run pipelines in, not against the ape repo. This is the
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

## ape feature

Inspect and maintain the feature registry

```
ape feature
```

Aliases: `features`

Subcommands:

- `backfill` — Fill absent required fields of existing features index entries from their records
- `list` — List features from the registry index
- `sync` — Reconcile the features index against records on disk
- `update` — Apply field deltas to existing features index entries
- `verify` — Verify the features registry against its index

## ape feature backfill

Fill absent required fields of existing features index entries from their records

```
ape feature backfill [flags]
```

Complete the index entries that already exist. A field the family's index
schema requires that is ABSENT from an entry is copied in from that
entry's record: its frontmatter, or its file name for slug. It is the same
rule sync uses when it adds an entry.

It does nothing else. No entry is added, removed or repointed, a value
that is present is never changed (even an empty one), and file: is never
filled, because an entry with no file is sync's repair. That narrowness is
the point: this runs unattended as a framework migration on
`ape framework update`, where a structural index change is not wanted.

A required field the record has no value for stays absent and is reported
as a gap. An entry whose record is absent or unreadable is also a gap.
Gaps are not pending work: nothing on disk can close them, so re-running
finds the same ones.

--check writes nothing and answers in its exit code: 0 when there is
nothing to fill, 1 when fills are pending. Those two are the only answers
about the index. Anything else means the check itself failed: 2 for an
unknown family or an index or record that cannot be read, 4 with no
project config.

Examples:

```
  ape feature backfill --check
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--check` | bool | `false` | Write nothing; exit 0 when nothing is fillable, 1 when fills are pending (2 or 4 if the check itself failed) |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape feature list

List features from the registry index

```
ape feature list [flags]
```

Examples:

```
  ape feature list --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape feature sync

Reconcile the features index against records on disk

```
ape feature sync [flags]
```

Reconcile index.yaml against the records on disk: records with no entry
are added, entries whose id no record claims are removed, and an entry
whose file: no longer resolves is repointed at the record claiming its id.

This is the repair for the findings `verify` reports, and nothing more —
it copies what a record's own frontmatter states and invents no titles,
statuses or any other field. A renamed record keeps its authored entry
rather than being dropped and re-added.

An added entry carries every field of the family's index schema the record
has a value for: its frontmatter, or its file name for slug. A required
field with no value is left out and listed in the change's missing. An
entry that is already listed is never completed here; that is
`backfill`.

--check makes it a dry run: the same diff, nothing written, and the answer
in the exit code: 0 when there is nothing to change, 1 when changes are
pending, 2 when the check itself failed (an unknown family, or an index
that cannot be read), 4 with no project config. Withheld removals alone
exit 0: sync cannot make them. generated_at moves only when something
else did.

Examples:

```
  ape feature sync --check
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--check` | bool | `false` | Report the diff without writing; exit 0 nothing to change, 1 changes pending (2 or 4 if the check itself failed) |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape feature update

Apply field deltas to existing features index entries

```
ape feature update [flags]
```

Apply per-entry field deltas to index.yaml and refresh generated_at.

--updates takes {"<id>": {"<field>": <value>}} as a file path or '-' for
stdin. Only entries ALREADY listed may be updated: an unknown id is an
error raised before anything is written, because creating an index entry
is the job of the skill that authors the document it points at.

A value is either a SCALAR or a LIST OF SCALARS. A scalar is written as
text, so a status of "no" stays a string and an id of "0001" keeps its
zeros instead of becoming a bool or an int. A list is written as a YAML
sequence — depends_on: [FEAT-1-1, FEAT-1-2] — which is what the
dependency fields hold. Anything else (a nested object, or a list
containing one) is refused, naming the entry and field, rather than
written in a form that cannot be read back.

Re-running an update is how a field is repaired: the value is replaced
outright, so a dependency list that an older ape stored as the quoted
string '[FEAT-1-2 FEAT-1-3]' becomes a real sequence again without
hand-editing index.yaml.

The rendered index is round-trip parsed before it replaces the file, and
the write is atomic — a crash mid-write cannot truncate an index. Key
order and comments survive, which a PyYAML round trip does not manage.

Exit codes:
  0  applied
  1  unknown id, unreadable updates, or an unwritable index (nothing written)

Examples:

```
  echo '{"FEAT-1-1":{"status":"superseded"}}' | ape feature update --updates -
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--generated-at` | string | `—` | Timestamp to write as generated_at (default: resolved config timestamp) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--updates` | string | `-` | JSON file with per-entry field deltas, or '-' for stdin |

## ape feature verify

Verify the features registry against its index

```
ape feature verify [flags]
```

Exactly five checks, and no others:

  1. registry.orphan_record / registry.phantom_entry
     set equality between the record directory and index.yaml, both
     directions
  2. registry.file_unresolved
     every index file: resolves, relative to the index's own directory
  3. registry.duplicate_id
     duplicate ids, in the index and on disk
  4. registry.record_unparseable
     the record parses as frontmatter at all
  5. registry.entry_incomplete
     every index entry carries each field its family's index schema
     requires (file: excepted: its absence is check 2).
     `ape registry backfill --all` fills the ones its record states

Check 5 is presence only. No type or value validation, no field drift, no
tag comparison, no updated_at comparison — those are judgment, and a
verifier that wanders into them stops being trustworthy.

An index.yaml that is absent while records exist is reported once, as
registry.index_missing: the degenerate case of check 1, not a fifth check.
One finding beats one orphan per record, which would bury the only fact
that matters.

Findings travel in the payload. Exit is 0 even with findings unless
--strict is passed, so this is safe to call from anywhere.

Examples:

```
  ape feature verify --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--strict` | bool | `false` | Exit 1 when there are findings (default: report and exit 0) |

## ape framework

Install and inspect APEX framework assets in a project

```
ape framework
```

Manage the apex_process_framework assets installed at the project root.

  ape framework setup      One-time install: skills + pipelines + bootstrap
                           _apex/config.yaml. Refuses if already installed
                           (pass --force to re-bootstrap).
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
  - _apex/pipelines/*.yaml copied from <repo>/_apex/pipelines
  - _apex/config.yaml      seeded (interactive prompt by default;
                           supply --project-name and --extensions to
                           skip the TUI; --no-bootstrap to skip seeding
                           entirely)
  - _apex/framework.yaml   metadata recording what was installed.

The install is committed as 'chore(framework): install APEX framework
vX.Y.Z' with Framework-Version / Framework-Commit / Generator trailers.
--no-commit leaves it in the working tree instead.

Refuses to run when:
  - _apex/framework.yaml already exists (pass --force to re-bootstrap;
    this resets project_name and extensions)
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

Headless contexts: when stdout is not a TTY (or --output-format is not
human) and the project lacks _apex/config.yaml, you must supply
--project-name and --extensions, OR pass --no-bootstrap. Otherwise
'setup' refuses to seed silently.

For subsequent refreshes against a framework version bump, use
'ape framework update'.

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
  - _apex/pipelines/*.yaml re-copied from <repo>/_apex/pipelines
  - _apex/framework.yaml   metadata refreshed (preserves project_name +
                           extensions recorded by 'ape framework setup')

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
that changed nothing commits nothing. When this ape is older than the
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
                install, no fetch, no migration. The plan is the list the
                project will hold AFTER this update: the installed entries
                overlaid by the repo's, with a SOURCE column marking the
                ones the update brings, so a migration can be read before
                it runs. The repo is read as it stands, unfetched. It
                distinguishes pending / applied / half-applied /
                cannot-tell rather than collapsing them, because a runner
                that reads cannot-tell as pending re-applies things
  --dry-run     show the framework drift AND the pending migrations,
                writing nothing
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

Exit codes: 2 usage; 3 the framework source (no release, missing tag,
build layout); 4 the project tree; 7 not installed; 11 ape below
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

## ape governance

Answer governance questions about a set of paths

```
ape governance
```

Subcommands:

- `match` — Which stories own these paths, and what that means for a maintenance change

## ape governance match

Which stories own these paths, and what that means for a maintenance change

```
ape governance match [flags]
```

Answer, for each path, which stories' File Lists claim it and what
follows for the lean maintenance lane.

A story owns a path when its "### File List" names it as a whole path
token. A (planned) or (deferred) entry claims nothing — it records an
intention, not a change — and a mention under "## Tasks" is advisory
and is never read here at all.

The owner's status decides:

  in-progress, review              veto — the path is owned
  the two statuses disagree        veto — fail-closed
  no status on either side         veto — ownership not established
  blocked                          Carries: <story-key> <path>
  backlog, ready-for-dev           Carries: <story-key> <path>
  done, cancelled                  Carries: <story-key> <path>
  no owner                         nothing

The status is read from the story file and its tracker row together,
after normalisation — the tracker writes "drafted" where a file reads
"ready-for-dev" — and where one side is missing the other is used.
More than five finished owners collapse to one "shared <path> (<N>
owners)" row, because a commit message is read by a person.

Paths come from --path (repeatable), --paths-file, or stdin. Never a
comma-separated list: a path may contain a comma, and a prescan lists
many.

This answers the OWNER question only. There is no adrs or patterns
field, not even an empty one: an empty list would read as "nothing
applies", where the truth is that this ape does not answer that.

Always exits 0 when it could answer. The verdict is the veto field, not
the exit code — a skill reading a non-zero exit as a content verdict is
the failure mode the framework's own rules warn about.

Examples:

```
  ape governance match --path internal/repl/pty.go --path docs/reference/cli.md
  ape governance match --paths-file changed.txt --output-format json
  git diff --name-only | ape governance match --paths-file -
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--path` | stringArray | `[]` | A path to match (repeatable) |
| `--paths-file` | string | `—` | File holding one path per line; "-" reads stdin |

## ape hook

Claude Code hooks for APEX v1 projects (registered by `ape project init`)

```
ape hook
```

Claude Code hook handlers. Each reads the hook payload (JSON) on stdin.

  session-start   SessionStart: adds `ape status --brief` as context
  stop            Stop and SubagentStop: blocks a stop while the one in-progress change
                  has a red command check (at most 3 times in a row)
  pre-tool        PreToolUse (Edit|Write|MultiEdit|NotebookEdit|Bash): denies edits to
                  development/specs/**, changes/*/state.json, archive/**, _apex/state/**,
                  and Bash commands that create, delete or push a git tag

Every hook exits 0; a block or deny is expressed in its JSON output.

In a session ape task launches in a v1 project, the three hooks also come inline
in ape's --settings (with --inline), so a hand-edited settings.json cannot drop
them; the settings.json copy then does nothing, so each event is decided once.

Subcommands:

- `pre-tool` — PreToolUse hook: deny edits to specs, state.json, the archive and _apex/state, and git tag writes
- `session-start` — SessionStart hook: inject `ape status --brief` as additional context
- `stop` — Stop/SubagentStop hook: block a stop while the in-progress change's checks are red

## ape hook pre-tool

PreToolUse hook: deny edits to specs, state.json, the archive and _apex/state, and git tag writes

```
ape hook pre-tool [flags]
```

PreToolUse hook for Edit|Write|MultiEdit|NotebookEdit|Bash. Denies, with
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny",
"permissionDecisionReason":"… specs change only through ape change archive"}}, an edit
whose target is under development/specs/, is development/changes/*/state.json, is under
development/archive/, or is under _apex/state/.

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
at once. APE_ALLOW_TAG=1 in the session's environment allows tag writes.

Anything else: no output.

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

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | project directory (default: the payload's cwd) |

## ape hook stop

Stop/SubagentStop hook: block a stop while the in-progress change's checks are red

```
ape hook stop [flags]
```

Stop and SubagentStop hook. When HEAD is on a change's branch, change/<id>, that
change is in-progress (read from the working tree), and any of its command checks exits
non-zero now, prints {"decision":"block","reason":"<the failing checks' last lines and
what to do>"}. Allows (prints nothing) when HEAD is on no change branch (the base
branch: planning, milestones), when the change is not in-progress (blocked, verified,
reviewed), when every check is green, or after 3 consecutive blocks (the count resets
when the payload's stop_hook_active is false).

The verdict is also written to _apex/state/hooks/<session>[-<agent>].json, which
is how `ape task` learns that a Stop it saw did not end the turn.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | project directory (default: the payload's cwd) |
| `--timeout` | duration | `15m0s` | limit for each check command (exit 124 when exceeded) |

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

## ape memory

Read the team-memory file without loading it whole

```
ape memory
```

team-memory.md outgrew whole-file reading. Past ~25,000 tokens (about
72 KB of markdown) a Read returns only part of the file, with a paging
banner; at 431,950 bytes on the reference project it fails outright
("exceeds maximum allowed size (256KB)") — including for the
retrospective that is instructed to re-read it before editing it.

  index  what is in there: ordinal, section, date, size, title
  show   the verbatim body of named entries
  check  size against two budgets, from a stat alone

Subcommands:

- `check` — Report team-memory size against the soft budget and hard ceiling
- `index` — List every team-memory entry
- `show` — Print the verbatim body of the named entries

## ape memory check

Report team-memory size against the soft budget and hard ceiling

```
ape memory check [flags]
```

Classify team-memory.md against two budgets, from an os.Stat alone —
the file is never read, which is what makes this cheap enough to run on
every retrospective.

  state: absent      no team-memory.md yet (a fresh project, not a problem)
  state: ok          under the soft budget
  state: over-soft   compaction is due; schedule it at the next epic close
  state: over-hard   compaction is overdue: far past what one Read shows
                     (~25k tokens, about 72 KB) and nearing Claude Code's
                     256 KiB byte cap, where a Read fails outright

EXIT 0 BY DEFAULT, whatever the state. The verdict is the 'state' field,
not the exit code, and that is deliberate: the framework's prose
convention is "on non-zero exit: HALT", so a failing exit here would abort
the retrospective at exactly the moment compaction is most needed — the
gate would break the ceremony it exists to trigger.

--fail-at opts into a non-zero exit for CI, which wants one:
  never  (default) always exit 0
  soft   exit 1 at over-soft or worse
  hard   exit 1 at over-hard

'ape doctor' maps the state to WARN/FAIL itself, so a hard-ceiling breach
is still visible and non-ignorable without this flag.

The token count in the output is bytes/2.9 (the ratio measured on APEX
markdown), an estimate, and nothing gates on it.

Examples:

```
  ape memory check
  ape memory check --fail-at hard
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--fail-at` | string | `never` | Exit 1 at this state or worse: never\|soft\|hard |
| `--hard` | int64 | `0` | Hard ceiling in bytes (default 204800) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--soft` | int64 | `0` | Soft budget in bytes (default 40960) |

## ape memory index

List every team-memory entry

```
ape memory index [flags]
```

One line per '### ' entry: ordinal, section, date, byte size, title.

Structurally lossless, and carrying no filter, ranking or predicate —
deliberately. Most call sites sit inside '## On Activation', which runs
BEFORE the story is identified, so no predicate keyed on "this story's
domain" could work there. Selection has to be possible from ordinal,
section, date, size and title alone.

A '### ' line inside a fenced code block is content, not an entry. That
matters more than it sounds: counting one would shift every ordinal after
it, and 'show <n>' would then hand back the wrong entry with nothing to
signal the mistake.

Examples:

```
  ape memory index --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape memory show

Print the verbatim body of the named entries

```
ape memory show <n>[,<n>...] | <n> [<n>...] [flags]
```

Print entries by ordinal, in the order asked for, byte-for-byte as they
appear in the file.

Ordinals may be one comma list (3,7,12), separate arguments (3 7 12), or a
mix (3,7 12): all three ask for the same entries in the same order.

Exit codes:
  0  printed
  2  an ordinal the file does not have, or one that is not a number

Examples:

```
  ape memory show 3,7,12
  ape memory show 3 7 12
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |

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
done (<ts> <sha>). Every one of those writes is committed by ape (roadmap.md, plus
_apex/state/milestones.json when it exists) as "apex: <M> proposed|approved|done"
with trailer "Milestone: <M>". milestones.json holds each gate's result (green|red)
and is committed, as "apex: <M> gate green|red", only when that result changes; the
latest run of any result is in the gitignored _apex/state/logs/gate-<M>.json.
No milestone command leaves the tree dirty.

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
ape milestone approve <M> --by <who> [flags]
```

Approve a proposed milestone: its Status: line becomes "approved (by <who> <ts>)".
Changes can start only under an approved milestone. Committed as "apex: <M> approved".
Approving an approved milestone is a no-op; a done one is refused. Refused on a change
branch (see `ape milestone --help`).

Exit codes: 0 ok · 1 no such milestone, done, malformed (Gate/Goal missing), or HEAD on a
change branch · 2 usage.

Examples:

```
  ape milestone approve M1 --by eval
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--by` | string | `—` | who approves (required) |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |

## ape milestone close

Run the gate; on exit 0 write Status: done (<ts> <sha>)

```
ape milestone close <M> [flags]
```

Close an approved milestone. Refused while a change of this milestone is in-progress,
verified or reviewed (archive or block it first) — each change's status read from its own
branch, change/<id> — and on a change branch (see `ape milestone --help`). Otherwise runs the gate (recorded as by
`ape milestone gate`) and, only on exit 0, writes "Status: done (<ts> <sha>)" into
roadmap.md, committed with milestones.json as "apex: <M> done". Green means the gate command
AND every scenario check of the spec pass, AND the regression command (when configured) is
not newly red against the milestone's earliest baseline for it — with no baseline, red fails
(see `ape milestone gate`). A red gate is recorded as
by `ape milestone gate`. A milestone is done when this command exits 0 — never because
anyone says so.

Exit codes: 0 done · 1 gate red, changes in flight, not approved, or HEAD on a change
branch · 2 usage.

Examples:

```
  ape milestone close M1
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |
| `--timeout` | duration | `15m0s` | limit for each check command (exit 124 when exceeded) |

## ape milestone gate

Run the milestone's gate and record the result

```
ape milestone gate <M> [flags]
```

Run the milestone's Gate: command from the project root, and every scenario check of
the whole spec beside it as the regression suite (human-review not run; output under
_apex/state/logs/gate-<M>/), and the project's "regression: <cmd>" from _apex/config.yaml
when one is configured. The gate is green only when all pass — the regression command
only needs not to be newly red: it is judged against the earliest regression baseline,
for the same command, recorded by any change of the milestone (active or archived);
red there and red now is reported (JSON run.regression.pre_existing_red), not failed.
When no change of the milestone has a baseline for the command, a red regression fails
the gate: nothing shows it was red before the milestone's work. Files the gate
or a check leaves outside development/ are undone and reported ("stray_files"). The run (exit, sha, time,
output hash, last lines) goes to _apex/state/logs/gate-<M>.json (gitignored; shown by
`ape status`). When the result differs from the recorded one (green vs red), or is
the first, _apex/state/milestones.json is updated and committed as
"apex: <M> gate green|red". Does not change the milestone's status — `ape milestone close` does.
Refused on a change branch: the gate judges the base branch (see `ape milestone --help`).

Exit codes: 0 gate green · 1 gate red (last output lines shown), no such milestone, or HEAD
on a change branch · 2 usage.

Examples:

```
  ape milestone gate M1
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |
| `--timeout` | duration | `15m0s` | limit for each check command (exit 124 when exceeded) |

## ape milestone new

Append a milestone (Status: proposed) to roadmap.md

```
ape milestone new "<title>" --gate "<cmd>" [--budget N] [flags]
```

Append "## M<n> — <title>" to development/roadmap.md with Status: proposed, the gate
command, the budget (USD list-price equivalent, when given) and a Goal: line. The id is
the next free M<n>. Write the Goal paragraph and the Out of scope bullets afterwards
(or pass --goal); the Status: line is ape's. Committed as "apex: <M> proposed".

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

## ape pattern

Manage governance patterns

```
ape pattern
```

Aliases: `patterns`

Subcommands:

- `backfill` — Fill absent required fields of existing patterns index entries from their records
- `list` — List all governance patterns
- `sync` — Reconcile the patterns index against records on disk
- `update` — Apply field deltas to existing patterns index entries
- `verify` — Verify the patterns registry against its index

## ape pattern backfill

Fill absent required fields of existing patterns index entries from their records

```
ape pattern backfill [flags]
```

Complete the index entries that already exist. A field the family's index
schema requires that is ABSENT from an entry is copied in from that
entry's record: its frontmatter, or its file name for slug. It is the same
rule sync uses when it adds an entry.

It does nothing else. No entry is added, removed or repointed, a value
that is present is never changed (even an empty one), and file: is never
filled, because an entry with no file is sync's repair. That narrowness is
the point: this runs unattended as a framework migration on
`ape framework update`, where a structural index change is not wanted.

A required field the record has no value for stays absent and is reported
as a gap. An entry whose record is absent or unreadable is also a gap.
Gaps are not pending work: nothing on disk can close them, so re-running
finds the same ones.

--check writes nothing and answers in its exit code: 0 when there is
nothing to fill, 1 when fills are pending. Those two are the only answers
about the index. Anything else means the check itself failed: 2 for an
unknown family or an index or record that cannot be read, 4 with no
project config.

Examples:

```
  ape pattern backfill --check
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--check` | bool | `false` | Write nothing; exit 0 when nothing is fillable, 1 when fills are pending (2 or 4 if the check itself failed) |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape pattern list

List all governance patterns

```
ape pattern list [flags]
```

Examples:

```
  ape pattern list --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape pattern sync

Reconcile the patterns index against records on disk

```
ape pattern sync [flags]
```

Reconcile index.yaml against the records on disk: records with no entry
are added, entries whose id no record claims are removed, and an entry
whose file: no longer resolves is repointed at the record claiming its id.

This is the repair for the findings `verify` reports, and nothing more —
it copies what a record's own frontmatter states and invents no titles,
statuses or any other field. A renamed record keeps its authored entry
rather than being dropped and re-added.

An added entry carries every field of the family's index schema the record
has a value for: its frontmatter, or its file name for slug. A required
field with no value is left out and listed in the change's missing. An
entry that is already listed is never completed here; that is
`backfill`.

--check makes it a dry run: the same diff, nothing written, and the answer
in the exit code: 0 when there is nothing to change, 1 when changes are
pending, 2 when the check itself failed (an unknown family, or an index
that cannot be read), 4 with no project config. Withheld removals alone
exit 0: sync cannot make them. generated_at moves only when something
else did.

Examples:

```
  ape pattern sync --check
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--check` | bool | `false` | Report the diff without writing; exit 0 nothing to change, 1 changes pending (2 or 4 if the check itself failed) |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape pattern update

Apply field deltas to existing patterns index entries

```
ape pattern update [flags]
```

Apply per-entry field deltas to index.yaml and refresh generated_at.

--updates takes {"<id>": {"<field>": <value>}} as a file path or '-' for
stdin. Only entries ALREADY listed may be updated: an unknown id is an
error raised before anything is written, because creating an index entry
is the job of the skill that authors the document it points at.

A value is either a SCALAR or a LIST OF SCALARS. A scalar is written as
text, so a status of "no" stays a string and an id of "0001" keeps its
zeros instead of becoming a bool or an int. A list is written as a YAML
sequence — depends_on: [FEAT-1-1, FEAT-1-2] — which is what the
dependency fields hold. Anything else (a nested object, or a list
containing one) is refused, naming the entry and field, rather than
written in a form that cannot be read back.

Re-running an update is how a field is repaired: the value is replaced
outright, so a dependency list that an older ape stored as the quoted
string '[FEAT-1-2 FEAT-1-3]' becomes a real sequence again without
hand-editing index.yaml.

The rendered index is round-trip parsed before it replaces the file, and
the write is atomic — a crash mid-write cannot truncate an index. Key
order and comments survive, which a PyYAML round trip does not manage.

Exit codes:
  0  applied
  1  unknown id, unreadable updates, or an unwritable index (nothing written)

Examples:

```
  echo '{"PAT-0001":{"status":"superseded"}}' | ape pattern update --updates -
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--generated-at` | string | `—` | Timestamp to write as generated_at (default: resolved config timestamp) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--updates` | string | `-` | JSON file with per-entry field deltas, or '-' for stdin |

## ape pattern verify

Verify the patterns registry against its index

```
ape pattern verify [flags]
```

Exactly five checks, and no others:

  1. registry.orphan_record / registry.phantom_entry
     set equality between the record directory and index.yaml, both
     directions
  2. registry.file_unresolved
     every index file: resolves, relative to the index's own directory
  3. registry.duplicate_id
     duplicate ids, in the index and on disk
  4. registry.record_unparseable
     the record parses as frontmatter at all
  5. registry.entry_incomplete
     every index entry carries each field its family's index schema
     requires (file: excepted: its absence is check 2).
     `ape registry backfill --all` fills the ones its record states

Check 5 is presence only. No type or value validation, no field drift, no
tag comparison, no updated_at comparison — those are judgment, and a
verifier that wanders into them stops being trustworthy.

An index.yaml that is absent while records exist is reported once, as
registry.index_missing: the degenerate case of check 1, not a fifth check.
One finding beats one orphan per record, which would bury the only fact
that matters.

Findings travel in the payload. Exit is 0 even with findings unless
--strict is passed, so this is safe to call from anywhere.

Examples:

```
  ape pattern verify --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--strict` | bool | `false` | Exit 1 when there are findings (default: report and exit 0) |

## ape pipeline

List or run an APEX pipeline

```
ape pipeline [name] [flags]
```

List or run a named APEX pipeline against the project in the current
working directory.

  ape pipeline                 List installed pipelines (also accepts
                               --output-format human|json|yaml).
  ape pipeline <name>          Run the named pipeline.

Available pipelines are read from <project>/_apex/pipelines/. To
install the canonical set (design, governance, epics) from the
framework repo, run "ape framework update".

Each pipeline is a sequence of stages; each stage is a chain of skill
invocations. ape runs one interactive "claude" REPL per stage inside an
in-process PTY (never "claude -p"): steps are typed as real REPL
keystrokes following PAT-25 passthrough conventions —

    /<agent> --autonomous -- <skill> --autonomous <args>

Skills without an agent skip the passthrough hop:

    /<skill> --autonomous --no-commit <args>

Stages and steps may declare the extensions they need
(requires-extensions). ape reads the project's effective extensions
once at run start and skips — and records as skipped — every stage or
step whose extensions are not configured, so governance runs ADR,
pattern, capability and feature governance as configured.
--ignore-extensions runs everything.

Rendering surface: --tui (default) shows the Bubble Tea panels, --web
serves the bridged web UI, --no-tui prints plain stdout progress lines.

The --prompt flag is forwarded only to skills whose pipeline definition
declares prompt_flag; no canonical framework pipeline declares it, so
the flag only reaches a step in a pipeline you have added it to. The
prompt value passes through as REPL keystrokes directly, so embedded
quotes/specials survive without shell quoting.

Examples:

```
  ape pipeline                       # list installed pipelines
  ape pipeline design                # run the design pipeline (TUI)
  ape pipeline governance --no-tui   # plain stdout progress
  ape pipeline epics --web --open    # bridged web UI, open the browser
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--commit-allow-dirty` | bool | `false` | Bypass the dirty-tree pre-run gate. The first committing step's diff will include any pre-existing uncommitted changes. |
| `--cwd` | string | `—` | Project root directory (default: current working dir) |
| `--detach` | bool | `false` | Start the run in the background, detached from this shell, print its id, and return at once; `ape run wait <id>` waits for it |
| `--effort` | string | `—` | Explicit reasoning effort (low\|medium\|high\|xhigh\|max) applied when a step/stage/pipeline does not set one in the YAML. Process-wide, sub-agents included. Unset: the project's _apex/effort-defaults.yaml per model, or xhigh without one (see ape config effort). |
| `--events-subject-prefix` | string | `ape.evt` | Subject root for progress events. |
| `--from` | string | `—` | Skip stages before the named one and start execution there (a stage skipped for its extensions starts the run at the next one that is not) |
| `--idle-timeout` | duration | `0s` | Per-step idle backstop: cancel a step only after this long with no progress across hook events or transcript growth (e.g. 90m). Default 60m. PTY output is NOT an anchor here — see `ape prompt`. |
| `--ignore-extensions` | bool | `false` | Run every stage and step regardless of its `requires-extensions:` gate, and check every conditional preflight file |
| `--ignore-project-settings` | bool | `false` | Tell the spawned claude to skip project + local .claude/settings*.json. Honoured in --web mode. |
| `--manifest-dir` | string | `—` | Override the directory for run manifest artifacts (default: <project>/_output/ape/pipelines) |
| `--max-duration` | duration | `3h0m0s` | Hard wall-clock ceiling per step regardless of progress (e.g. 3h); the clock resets on each sub-agent boundary, so a sequential batch step is bounded per item, not per batch. 0 disables the cap. |
| `--nats-creds` | string | `—` | NATS .creds file; its user identity is baked into every subject (env APE_NATS_CREDS). |
| `--nats-url` | string | `—` | NATS server URL for progress events + transcript upload (env APE_NATS_URL). Empty disables both. |
| `--no-commit` | bool | `false` | Do not commit anything during the run; leave the working tree dirty. Overrides any `commit:` field in the pipeline YAML. |
| `--no-tui` | bool | `false` | No UI surface: plain stdout progress lines. Exec is still the interactive per-stage claude REPL in an in-process PTY. |
| `--open` | bool | `false` | With --web (or default): xdg-open the broker URL on start. |
| `--output-format` | string | `human` | Output format for list mode (no positional arg): human\|json\|yaml |
| `--output-style` | string | `—` | Output style pinned on the spawned session (default "Default"). Pass "inherit" to keep whatever style the machine is configured with. |
| `--prompt` | string | `—` | Optional prompt forwarded to steps whose pipeline declares prompt_flag (no canonical pipeline does) |
| `--prompt-file` | string | `—` | File holding the --prompt text; "-" reads stdin. Mutually exclusive with --prompt |
| `--quiet` | bool | `false` | With --no-tui: suppress per-event stream; print only stage/step start/end markers |
| `--transcript-store` | string | `nats-object` | Transcript blob backend: nats-object\|uri-offload (env APE_TRANSCRIPT_STORE). |
| `--tui` | bool | `false` | Bubble Tea TUI (the default; explicit form for scripts). |
| `--upload-transcripts` | bool | `false` | At run end, upload the transcript set as content-addressed blobs (env APE_UPLOAD_TRANSCRIPTS=1). |
| `--web` | bool | `false` | Bridged web UI. Explicit form for scripts. |

## ape planning

Show the planning pipeline diagram

```
ape planning
```

Print an ASCII swimlanes view of the greenfield planning pipeline.
Lanes are agent personas; rows are topological depth; `←` lists each
skill's upstream dependencies. Source of truth for edges: the
apex_process_docs planning-pipeline explanation.

## ape project

Create and check an APEX v1 project layout

```
ape project
```

Subcommands:

- `check` — Validate every v1 shape and size cap; list all failures
- `codes` — List the diagnostic codes v1 commands report in --json failures
- `init` — Create the development/ skeleton and merge ape's hooks (never overwrites)

## ape project check

Validate every v1 shape and size cap; list all failures

```
ape project check [flags]
```

Validate the whole project and list every failure, not just the first.

  development/project.md   # <name>, ## Vision, ## Users, ## Scope, ## Out of scope,
                           ## Constraints (each non-empty); ≤ 150 lines and ≤ 8 KB
  AGENTS.md                present, ≤ 120 lines; CLAUDE.md (if any) has the line @AGENTS.md
  development/roadmap.md   # Roadmap, ≥ 1 "## M<n> — <title>" with Status (ape-written),
                           Gate, Goal; Budget a number when present
  specs/<cap>/spec.md      # title, Purpose:, "### Requirement: <name>" each with ≥ 1
                           "#### Scenario:" and exactly one "- check: <cmd> | human-review"
                           per scenario; ≤ 20 KB ("split into two capabilities")
  decisions/NNNN-<slug>.md # ADR-NNNN: title, Status, Date, Context/Decision/Consequences,
                           Enforced by
  changes/<id>/            every non-archived change, as `ape change check`
  layout                   no development/changes/archive/ (an older ape's archive: move
                           its folders to development/archive/), no development/.apex/
                           (an older ape's state: move milestones.json and decisions.json
                           to _apex/state/, delete it; `ape project init` then adds
                           _apex/state/.gitignore)
  .claude/settings.json    ape's hooks registered as `ape project init` writes them:
                           hooks.missing / hooks.altered (matcher, type, async, a
                           stop timeout below 1800 s); `ape project init` restores them
  plugins                  _apex/config.yaml marketplaces/plugins: settings.json's
                           extraKnownMarketplaces/enabledPlugins match them, and (when
                           claude is on PATH) each plugin is installed at project scope
                           at its ref (plugins.drift; plugins.config for a bad config);
                           `ape doctor --fix` puts them right

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
"diagnostics": [{"severity", "code", "message", "fix", "file"?, "line"?}], ...}: the
first diagnostic is the failure itself, the rest its findings or conflicts. Branch on
"code"; "message" and "fix" are for people. Codes are stable: a new rule gets a new
code, and a code's meaning never changes. severity is always "error" (no warning tier).
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
(one exception: the @AGENTS.md line appended to a CLAUDE.md that lacks it).

  _apex/config.yaml                  project_name, development_folder (also read, never
                                     written: regression, models, effort, and limits —
                                     budget: enforce|off (default off; see ape task --help),
                                     attempts: N red verifies per start (default 5; see
                                     ape change verify --help))
  AGENTS.md                          the project map (skeleton)
  CLAUDE.md                          "@AGENTS.md"; an existing CLAUDE.md without that line
                                     gets it appended, and nothing else
  development/project.md             vision, users, scope, out of scope, constraints (skeleton)
  development/roadmap.md             "# Roadmap"
  development/specs/                 empty: no backfill — a spec is born from its first change
  development/changes/               the changes in flight
  development/archive/               archived and abandoned changes
  development/decisions/
  _apex/state/                       ape's own records (never edited by a model; never touched
                                     by ape framework update): milestones.json, decisions.json
                                     tracked; logs/, runs/, hooks/ ignored. Its .gitignore is a
                                     whitelist (*, !.gitignore, !milestones.json, !decisions.json)

Hooks merged into .claude/settings.json (existing keys and hooks are kept):
  SessionStart          ape hook session-start
  Stop, SubagentStop    ape hook stop          (timeout 1800 s: it runs the change's checks)
  PreToolUse            ape hook pre-tool      (matcher Edit|Write|MultiEdit|NotebookEdit|Bash:
                                                the edit guard, and the git tag guard)
A hook that was altered (another matcher, async, a shorter stop timeout) is replaced
by the one above; running init again changes nothing. ape task passes the same
hooks inline as well, so its sessions have them even when settings.json does not.

Everything init wrote (and settings.json when it merged hooks) is committed as
"apex: project init"; files that existed before are not touched and not committed.

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
the marketplace does not serve at ref fails with the command to run.

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
  ape prompt "refactor the parser" --agent apex-agent-dev --workflow
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
typed as is.

After typing, ape presses Enter once claude has drawn the line, then
confirms the submit through the UserPromptSubmit hook, pressing Enter up
to twice more; a line still not submitted fails the session (exit 1).

Records land under <project>/_output/ape/prompts/<prompt-id>/ (in an
APEX v1 project, _apex/state/runs/prompts/<prompt-id>/) (runlog
streams + copied transcript + prompt.yaml session record) and fold into
the project cost rollup's Prompts bucket.

ape prompt must run from a project root (a directory with
_apex/config.yaml). It makes no commits of its own.

Exit codes: 0 session completed (Stop hook) · 1 idle-timeout, session
failed, or the typed line never submitted · 2 usage or preflight error (no _apex/config.yaml, unresolved
--agent, missing --handoff file) · 3 the claude REPL never became ready
· 4 claude exited before the Stop hook.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--agent` | string | `—` | Framework agent fronting the session: /<agent> --autonomous -- <prompt> |
| `--cwd` | string | `—` | Project root directory (default: current working dir) |
| `--effort` | string | `—` | Explicit reasoning effort for the session and its sub-agents (low\|medium\|high\|xhigh\|max). Unset: the project's _apex/effort-defaults.yaml per model, or xhigh without one (see ape config effort). |
| `--handoff` | string | `—` | Handoff document to seed the session with (mutually exclusive with the positional prompt) |
| `--idle-timeout` | duration | `0s` | Idle backstop: end the session only after this long with no progress across hooks, transcript growth, or PTY output (e.g. 15m); default matches the pipeline (60m) |
| `--ignore-project-settings` | bool | `false` | Tell the spawned claude to skip project + local .claude/settings*.json |
| `--max-duration` | duration | `3h0m0s` | Hard wall-clock ceiling regardless of progress (e.g. 3h); the clock resets on each sub-agent boundary, so a batch of sub-agents is bounded per item, not overall. 0 disables the cap. |
| `--model` | string | `—` | Claude model. A bare family (fable, haiku, mythos, opus, sonnet) resolves to its current generation; sonnet-5 / claude-sonnet-5 / opus[1m] pin explicitly |
| `--output-format` | string | `human` | Output format: human\|json\|yaml (json/yaml = result envelope on stdout, progress on stderr) |
| `--output-style` | string | `—` | Output style pinned on the spawned session (default "Default"). Pass "inherit" to keep whatever style the machine is configured with. |
| `--quiet` | bool | `false` | Suppress the progress stream on stderr |
| `--ultracode` | bool | `false` | Prepend the ultracode keyword (session runs workflows by default) |
| `--workflow` | bool | `false` | Append a directive to run the task through a Claude Code workflow |

## ape registry

Verify or reconcile every record registry at once

```
ape registry
```

Cross-family fan-out over the four record registries — ADRs, patterns,
features and capabilities. Each family also carries these verbs on its own
noun (`ape adr verify`); this is the whole-project view.

Subcommands:

- `backfill` — Fill absent required fields of existing index entries from their records
- `restore-headers` — Rebuild a headerless record's frontmatter from its index entry
- `sync` — Reconcile every record index against records on disk
- `verify` — Verify every record registry (or a named subset)

## ape registry backfill

Fill absent required fields of existing index entries from their records

```
ape registry backfill [flags]
```

Complete the index entries that already exist. A field the family's index
schema requires that is ABSENT from an entry is copied in from that
entry's record: its frontmatter, or its file name for slug. It is the same
rule sync uses when it adds an entry.

It does nothing else. No entry is added, removed or repointed, a value
that is present is never changed (even an empty one), and file: is never
filled, because an entry with no file is sync's repair. That narrowness is
the point: this runs unattended as a framework migration on
`ape framework update`, where a structural index change is not wanted.

A required field the record has no value for stays absent and is reported
as a gap. An entry whose record is absent or unreadable is also a gap.
Gaps are not pending work: nothing on disk can close them, so re-running
finds the same ones.

--check writes nothing and answers in its exit code: 0 when there is
nothing to fill, 1 when fills are pending. Those two are the only answers
about the index. Anything else means the check itself failed: 2 for an
unknown family or an index or record that cannot be read, 4 with no
project config.

Examples:

```
  ape registry backfill --all --check
  ape registry backfill --all
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--all` | bool | `false` | Every family (the default when --family is not given) |
| `--check` | bool | `false` | Write nothing; exit 0 when nothing is fillable, 1 when fills are pending (2 or 4 if the check itself failed) |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--family` | stringSlice | `[]` | Families to backfill: adrs,patterns,features,capabilities |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape registry restore-headers

Rebuild a headerless record's frontmatter from its index entry

```
ape registry restore-headers [flags]
```

Give a headerless record back the frontmatter its index entry already
states. This is `sync` run in the other direction, and it is the
non-destructive answer to the one finding pair that had none.

A record with no frontmatter block claims no id. So `verify` reports it
twice — registry.record_unparseable for the record, registry.phantom_entry
for the index entry nothing appears to claim — and `sync` reads that
entry as dead and offers to delete it. That entry is the last copy of the
record's id, title, type, status, version and dates: deleting it turns a
missing header into an unrecoverable loss, on a file that was on disk the
whole time. `sync` now withholds those removals; this is what clears
them.

Every value is copied verbatim from the entry — sequences like `tags`
included — minus the two keys that are index bookkeeping (`file`,
`slug`), plus the `output_document` self-reference every record in
these families carries. Nothing is invented.

IT WILL NOT TOUCH A RECORD THAT HAS A FRONTMATTER BLOCK, even one that
fails to parse. Overwriting a header someone authored to satisfy a checker
is a different and far worse operation than giving a headerless file the
header its index says it always had; that case is reported and left for a
person.

--check makes it a dry run.

Examples:

```
  ape registry restore-headers --check
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--all` | bool | `false` | Every family (the default when --family is not given) |
| `--check` | bool | `false` | Report what would be written, without writing |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--family` | stringSlice | `[]` | Families to repair: adrs,patterns,features,capabilities |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape registry sync

Reconcile every record index against records on disk

```
ape registry sync [flags]
```

Reconcile index.yaml against the records on disk: records with no entry
are added, entries whose id no record claims are removed, and an entry
whose file: no longer resolves is repointed at the record claiming its id.

This is the repair for the findings `verify` reports, and nothing more —
it copies what a record's own frontmatter states and invents no titles,
statuses or any other field. A renamed record keeps its authored entry
rather than being dropped and re-added.

An added entry carries every field of the family's index schema the record
has a value for: its frontmatter, or its file name for slug. A required
field with no value is left out and listed in the change's missing. An
entry that is already listed is never completed here; that is
`backfill`.

--check makes it a dry run: the same diff, nothing written, and the answer
in the exit code: 0 when there is nothing to change, 1 when changes are
pending, 2 when the check itself failed (an unknown family, or an index
that cannot be read), 4 with no project config. Withheld removals alone
exit 0: sync cannot make them. generated_at moves only when something
else did.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--all` | bool | `false` | Reconcile every family (the default when --family is not given) |
| `--check` | bool | `false` | Report the diff without writing; exit 0 nothing to change, 1 changes pending (2 or 4 if the check itself failed) |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--family` | stringSlice | `[]` | Families to reconcile: adrs,patterns,features,capabilities |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape registry verify

Verify every record registry (or a named subset)

```
ape registry verify [flags]
```

Exactly five checks, and no others:

  1. registry.orphan_record / registry.phantom_entry
     set equality between the record directory and index.yaml, both
     directions
  2. registry.file_unresolved
     every index file: resolves, relative to the index's own directory
  3. registry.duplicate_id
     duplicate ids, in the index and on disk
  4. registry.record_unparseable
     the record parses as frontmatter at all
  5. registry.entry_incomplete
     every index entry carries each field its family's index schema
     requires (file: excepted: its absence is check 2).
     `ape registry backfill --all` fills the ones its record states

Check 5 is presence only. No type or value validation, no field drift, no
tag comparison, no updated_at comparison — those are judgment, and a
verifier that wanders into them stops being trustworthy.

An index.yaml that is absent while records exist is reported once, as
registry.index_missing: the degenerate case of check 1, not a fifth check.
One finding beats one orphan per record, which would bury the only fact
that matters.

Findings travel in the payload. Exit is 0 even with findings unless
--strict is passed, so this is safe to call from anywhere.

Examples:

```
  ape registry verify --all --output-format json
  ape registry verify --family adrs,patterns
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--all` | bool | `false` | Verify every family (the default when --family is not given) |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--family` | stringSlice | `[]` | Families to verify: adrs,patterns,features,capabilities |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--strict` | bool | `false` | Exit 1 when there are findings (default: report and exit 0) |

## ape release

Project the framework's release record

```
ape release
```

A release slice is an operator-declared set of epics, living in
sprint-status.yaml's release_slices: and active_slice: keys. A release
record is one file per release under {planning_folder}/releases/, whose
frontmatter asserts that release's status.

  status  project the records and the slices they decide

Subcommands:

- `status` — Report the declared slices and the state each record asserts

## ape release status

Report the declared slices and the state each record asserts

```
ape release status [flags]
```

Read sprint-status.yaml's release_slices: and active_slice: keys and,
for each slice, the frontmatter of {planning_folder}/releases/release-<id>.md.

Two things this reads and never computes.

THE STATUS IS THE RECORD'S. A release's status is asserted in exactly one
place — the record's own 'status:' field — so a slice is released when its
record says so and by no other route. A release_slices: entry deliberately
carries no status of its own, which is what stops a shipped release being
counted as unshipped by a writer that only ever wrote 'declared'. An
absent releases/ folder, an absent record and an UNREADABLE one all mean
"not released"; the unreadable one is reported as unreadable rather than
given a status it never asserted.

ONLY THE FRONTMATTER. The record's body carries nine assembled tables,
including the gate table with its per-row PASS / RED / NOT-RUN / PENDING
results. Those belong to apex-release-record, and re-deriving them from a
Markdown table here would put a second source of truth behind the
release's own verdict. What is reported about gates is the declaration —
how many, how many required — never a result. The verdict is in the
frontmatter already: 'status:' asserts it, 'blocking:' says why it is not
'prepared', 'acceptance:' says why a run that reached no verdict reached
one.

The active scope resolves the way the framework's own prose does:
active_slice names a declared slice, or — absent, empty, or naming a slice
release_slices: does not carry — the scope is every epic not inside a
released slice. Epics are enumerated from tracker rows, so with NO tracker
the epic sets come back null rather than empty: "no tracker to enumerate
from" and "no epics" are different answers, and only one of them is true.

EXIT 0 ALWAYS. The record's 'status:' is the verdict, not the exit code —
a projection that halted on one bad record could not report the others.

Examples:

```
  ape release status
  ape release status --output-format json
  ape release status --slice v1.2.0
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--slice` | string | `—` | Report one slice by id instead of every declared slice |

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

A run started with --detach (ape pipeline|task|change --detach) returns an id at
once and continues in the background, orphaned from the shell that started
it, so a tool's time limit on that shell does not reach it.

  ape run wait <id>     wait for it; exit with the run's own exit code
                        (75: --timeout expired, wait again; 76: its
                        supervisor is gone without recording an exit)
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

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root directory (default: current working dir) |

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

--timeout bounds the wait. When it expires with the run still going, the
command exits 75 and the run is untouched: wait again. That is how a caller
under a tool time limit waits for a run longer than the limit — each wait
stays inside it.

A run whose supervisor is gone without recording an exit (killed, or the
machine rebooted) is reported as such, exit 76, rather than waited on
forever — its own code, so it is never mistaken for a run that exited 1.

The run's stdout is in its own file (the handle's "stdout"), apart from
stderr (its "log"), so a --output-format json envelope reads clean.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root directory (default: current working dir) |
| `--timeout` | duration | `0s` | Stop waiting after this long (e.g. 25m) and exit 75 if the run is still going; 0 waits until it ends |

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
primitives — run a pipeline, task, or prompt (all PTY-backed, the same
runners the CLI uses), read manifests, scan transcripts, log, publish
events, and upload blobs — as one deterministic, version-controlled Go
file instead of a shell wrapper around the CLI.

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

Exit codes: 0 success · 1 the script returned an error, panicked, or a
launched run failed · 2 usage or read error (no file, bad flags).

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

Run a NATS-micro job daemon that accepts pipeline/task jobs over request/reply

```
ape service [flags]
```

Turn this machine into a remotely drivable ape worker (PLAN-14). The
daemon registers a NATS micro service on

  ape.svc.<name>.<project-slug>.<endpoint>

and accepts JSON request/reply jobs: pipeline.run and task.run dispatch an
ape child process (headless, PTY-only); job.status / job.list / job.stop
manage them; status / health report the daemon. NATS-micro $SRV.PING /
$SRV.INFO / $SRV.STATS discovery is available for free. prompt.run and
script.run are registered but rejected (VALIDATION) until their runners
ship.

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

SECURITY: anyone who can publish on the service subjects can run pipelines on
this machine. Scope the NATS credential's publish/subscribe permissions to
ape.svc.<name>.<project-slug>.> on the server — that is the real trust
boundary (see docs/how-to/run-ape-as-a-service.md).

Shutdown is graceful: SIGINT/SIGTERM stops accepting new jobs and waits for
in-flight children (indefinitely by default; bound it with --drain-timeout).
A second signal terminates them immediately.

Exit codes: 0 clean shutdown · 1 connect/registration failure · 2 usage or
config error (bad --name, missing/invalid service.yaml, no NATS URL).

Examples:

```
  ape service --nats-url nats://127.0.0.1:4222 --nats-creds ./ape.creds
  ape service --name ci --drain-timeout 5m
  # discovery + a task submission from another host:
  nats req '$SRV.PING.ape' ''
  nats req ape.svc.ape.myproject.task.run '{"project_root":"/abs/path/myproject","skill":"apex-shard-doc"}'
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

## ape sessions

List, prune, or open the URL of live ape sessions

```
ape sessions [flags]
```

Live ape chat / ape pipeline (web mode) invocations are tracked in
~/.ape/registry.json. This subcommand inspects that registry.

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

## ape sprint

Inspect and maintain sprint-status.yaml

```
ape sprint
```

Three operations on the tracker, with deliberately different contracts:

  check      compare tracker rows against story files; report divergence,
             never pick a winner, always exit 0
  verify     assert one row landed as written (a gate, exit 0/2/3/4/5)
  reconcile  project an epic's row from its story rows (a mutation)

Subcommands:

- `check` — Report divergence between the tracker and story files
- `reconcile` — Project epic rows from their story rows
- `verify` — Verify one tracker row landed as written

## ape sprint check

Report divergence between the tracker and story files

```
ape sprint check [flags]
```

Set-compare sprint-status.yaml's story rows against story files on disk,
and compare each row's status against that story's own frontmatter.

A story file is one that declares story_id, or one a tracker story row
names. The second is a brownfield story with no frontmatter: its status is
its body's Status: line. A named file stating no status anywhere is
reported as row_without_story, saying so rather than claiming no file.

epic-* and *-retrospective rows are classified out: they have no story
file to diverge from. The tracker's 'drafted' is normalised to a story
file's 'ready-for-dev' for comparison only — neither file is touched — or
every drafted story would report a false divergence.

ALWAYS EXITS 0, even with findings, and there is no --strict. Which side of
a divergence is right is judgment, so this reports both values and picks
neither; wiring it into a build loop would stop runs over something no tool
can resolve. It belongs in 'ape doctor' and nowhere else.

Examples:

```
  ape sprint check --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape sprint reconcile

Project epic rows from their story rows

```
ape sprint reconcile [flags]
```

An epic's status is not a fact any skill asserts — it is a projection of
the story rows beneath it. This is the single implementation of that
projection, tracker-only, with no story-file reads:

  rows   = development_status keys matching ^{N}-\d+[-_]
  active = rows whose status is not 'cancelled'

  no rows              -> leave unchanged (never close an epic with none)
  no active rows       -> leave unchanged (all-cancelled is a scope call)
  all active 'done'    -> done
  all active 'backlog' -> backlog
  otherwise            -> in-progress

'blocked' lands in the final clause, so a blocked story holds its epic
open. An unrecognised status can only ever hold an epic open, never close
it, and is named in the output rather than swallowed.

The write is TARGETED: only the matched epic-N line and the body
updated_at change. Comments, key order, story rows and the sync-generated
header are untouched. updated_at moves only on mutation and never
backwards — it is clamped, reported, and never fatal.

The read-modify-write takes an exclusive lock on a sidecar file, because
concurrent per-epic sub-agents reconcile the same tracker and the last
writer would otherwise silently drop a sibling's update.

Exit 0 for every content outcome, including an unrecognised status.
Exit 2 for a usage error: neither --epic nor --all, both at once, an
epic number below 1, or no tracker resolved and no --file. Exit 1 only
for a genuine I/O failure.

If the project has a board (see 'ape aboard'), reconcile also refreshes a
'Sprint' tab in it — story and epic counts, what is in flight, and what is
blocked, all derived from the tracker. That is how a long or autonomous run
can be watched without interrupting it: reconcile already runs at every
boundary that moves a story, so it is the thing that OBSERVES tracker
changes rather than the thing that causes them.

Two write paths, chosen by whether a board answers. A board that is
listening is POSTed to, which is the only way a page already showing the
board is pushed the change, and which gets the write a real compare-and-set.
When nothing is listening the file is written directly, so the tab is
current before anyone starts a board. A server that IS there and refuses —
a 409, a timeout — stops the refresh rather than writing around it.

The refresh is STRICTLY BEST-EFFORT and cannot affect this command. A
board that is absent, unreadable or unwritable changes neither the exit
code nor a byte of stdout; anything it has to say goes to stderr. That is
not politeness: reconcile sits on mutation paths where a non-zero exit is
read as a content verdict, so a broken board would otherwise convert a
defer into a patch and demote a story. --check skips it entirely, since a
dry run that writes to a board is not a dry run. Nothing here creates a
board: starting one, and initialising one, are yours to do.

Examples:

```
  ape sprint reconcile --epic 12
  ape sprint reconcile --all --check
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--all` | bool | `false` | Reconcile every epic with rows |
| `--check` | bool | `false` | Report the projection without writing |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--epic` | int | `0` | Reconcile one epic by number |
| `--file` | string | `—` | Tracker path (default: resolved from config) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape sprint verify

Verify one tracker row landed as written

```
ape sprint verify [flags]
```

Re-read the tracker from disk and confirm the write landed: the row
equals --expected, updated_at does not precede created_at, and updated_at
is not earlier than the value in the last committed revision.

That last comparison is the one that matters. A row write can silently
fail to land, and comparing updated_at against created_at alone passes
trivially because both are written in the same operation.

Exit codes, preserved exactly from verify-sprint-status-row.py because two
review skills branch on them:
  0  the row matches and the timestamps are ordered
  2  file unreadable or YAML malformed
  3  key missing from development_status, or its value differs
  4  updated_at precedes created_at (a corrupt write)
  5  updated_at is earlier than the last committed value (a backwards
     write). Deterministically repairable: re-write the field as the
     reported clamp value or later, re-run, and report the clamp. NEVER a
     reason to stop the run.

Exit 1 is deliberately unreachable. In the Python it meant "PyYAML is not
installed" — an environment failure that forced apex-review-story and
apex-code-review to carry an eye-check fallback. A static binary cannot
produce it, which is what lets those fallback branches be deleted.

This command carries no --cwd. --file is required and names the tracker
outright, so nothing is resolved from the project's config and there is no
project for --cwd to select.

Examples:

```
  ape sprint verify --file development/implementation/sprint-status.yaml --key 1-1 --expected done
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--expected` | string | `—` | Status the row must equal (required) |
| `--file` | string | `—` | Path to sprint-status.yaml (required) |
| `--key` | string | `—` | development_status row key (required) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape status

Show the current milestone, active changes, blocks, pending human review and spend

```
ape status [flags]
```

Show where the project stands, from the records ape keeps:

  - the current milestone (the first not done): status, gate and its last recorded run,
    budget, and the spend of runs started since it was approved
  - the milestone's limit stops (JSON milestone.stops: reason, at, run, skill, spent_usd,
    budget_usd, session_usd): sessions ape task stopped at its enforced budget
    (limits.budget: enforce; JSON milestone.budget_enforced), whose measured cost the
    spend counts
  - the checked-out branch (JSON branch; "(on <branch>)" in the first line)
  - every change not archived, with its status, blocked reason, red checks from its
    last verify, human-review tasks not yet accepted, and for a started change its red
    verifies since it started against limits.attempts (JSON attempts, attempts_cap;
    "(3/5 attempts)"; attempts_total, never reset) — a change the cap blocked shows
    "attempts exceeded" as its reason. The counts are this checkout's
    _apex/state/attempts.json (git-ignored). A started change is worked on its own
    branch, change/<id> (D38; JSON changes[].branch), and its status is read from
    there whatever branch is checked out — a change branch lists the changes it does
    not hold too
  - spend so far: the sum of finished ape task/pipeline run manifests
  - red at baseline, not any change's own (JSON "pre_existing_red": [{change, kind, ref,
    cmd, exit, sha}]): the regression command, and scenario checks of the spec a change
    leaves untouched, that were red when a change was baselined — for the planner to
    roadmap. A change's own task and delta checks red at baseline are normal test-first
    and never listed
  - next: what the way of working says comes next, derived from the state alone

--brief renders at most 25 lines (what the SessionStart hook injects).

Exit codes: 0 ok · 2 usage.

Examples:

```
  ape status
  ape status --brief
  ape status --json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--brief` | bool | `false` | at most 25 lines |
| `--cwd` | string | `—` | project directory (default: the nearest parent with _apex/config.yaml or development/) |
| `--json` | bool | `false` | machine output (JSON on stdout) |

## ape story

Project and verify story frontmatter

```
ape story
```

Aliases: `stories`

Subcommands:

- `fields` — Project named frontmatter keys across every story
- `verify` — Verify story frontmatter and shape (corpus report, or a single-file gate)

## ape story fields

Project named frontmatter keys across every story

```
ape story fields [flags]
```

Walk the implementation folder, read at most 8 KiB per .md file, stop at
the closing '---', and emit only the named top-level keys plus their
nested blocks. A body is never opened.

A file counts as a story only if it has a story_id — which is what keeps
retrospectives, epic briefs and the deferred-work stub out of the result
without teaching this command about each of them.

The trailer carries files_scanned, stories_matched, per-field presence
counts and bytes_read. Those numbers are the point: "this field is absent
everywhere" and "this field was never looked for" are different answers,
and only the trailer distinguishes them. A field present in zero stories
reports 0 and exits 0.

One unreadable file loses that file and nothing else — it is named in
warnings and the rest are still returned.

Examples:

```
  ape story fields --select story_id,epic,status,features --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--select` | string | `—` | Comma-separated frontmatter keys to emit (required) |

## ape story verify

Verify story frontmatter and shape (corpus report, or a single-file gate)

```
ape story verify [flags]
```

Three check classes over story frontmatter, and no others:

  1. presence  the four required keys, plus the extension-gated ones
               (governance.adrs, governance.patterns, features,
               capabilities) for whichever ext_* flags are set
  2. type      features items must be objects, not bare strings;
               depends_on items must be strings, not YAML floats
  3. refs      every cited ADR / pattern / feature / capability id
               resolves to a record on disk

No enum checks, no contribution vocabulary, no JSON Schema engine. The
contribution field has no normative source, so coercing it would fabricate
a lifecycle edge — a test asserts this command reports nothing for any
value of it.

--file mode adds the STORY-SHAPE classes over the body:

  story.section_missing           a section the derived set requires is
                                  absent. The set is a function of the
                                  resolved config alone (## Feature Scope
                                  on ext_features, the two governance
                                  headers on ext_adrs / ext_patterns); an
                                  empty section is accepted, a missing
                                  header is not
  story.file_list_marker          a File List entry with no status marker,
                                  an unknown one, or the forbidden
                                  em-dash-prose form in place of one
  story.placeholder_residue       a template placeholder surviving at
                                  status: review. Debug Log References'
                                  "_No issues encountered._" is a terminal
                                  convention, never residue
  story.compliance_table_header   a compliance table whose header row is
                                  not the declared shape
  story.gcc_line_form             a Governance Compliance Criteria line
                                  that is not
                                  "- [ ] **[ID]** {instruction} -- {scope}"
  story.adrs_considered           adrs_applicable does not bind against
                                  the recomputed tag-match candidate count
  story.adr_unresolved            a governance.adrs id that resolves to no
                                  ADR at HEAD

And one class that is REPORTED WITHOUT GATING:

  story.adr_not_accepted          a cited ADR that resolves but is not
                                  accepted. Exit 0 — a story may
                                  legitimately cite a proposed ADR

The two governance classes need the project's ADR corpus, which is found
by walking up from the story's own path. Against a story outside any
project they SKIP and say so; they never silently pass.

TWO MODES, with deliberately different contracts:

  corpus (default)  a REPORT, over frontmatter only. Exit 0 even with
                    findings; --strict makes it 1. --strict must never be
                    set from inside apex-review-story, apex-code-review or
                    apex-epic-batch-review: a non-zero exit on those paths
                    converts a defer into a patch and demotes the story.
                    The shape classes are deliberately NOT run here: the
                    corpus scan reads at most 8 KiB per file and never
                    opens a body, which is what keeps a 465-story sweep at
                    67 KB instead of 23.5 MB.

  --file <path>     a GATE, replacing verify-story-frontmatter.py with its
                    exit codes intact:
                      0  valid
                      2  parse failure (bad YAML, or no --- delimiters)
                      3  a required or extension-conditional key is absent,
                         or an optional key is present but malformed
                      4  the keys are fine and the BODY is not — one of the
                         story-shape classes above
                    3 wins over 4 when a file has both; the keys are the
                    more fundamental failure. Referential integrity across
                    the four families is not asserted here — a single file
                    cannot see the corpus, exactly as the Python could
                    not — though the ADR family alone is, by the walk
                    described above.

--fix REPAIRS ONE CLASS AND SAYS SO. A depends_on item that decoded as a
number is re-quoted: same characters, one right answer, no second source
needed. Nothing else is touched, and the findings --fix does not own are
reported as remaining rather than dropped.

The two classes it declines look mechanical and are not. A features item
needs a contribution, whose only source is the prose table inside the
feature record — and where that table has no row for the story, the
disagreement IS the defect. A missing features:/capabilities: key needs a
value, and [] claims the story contributes to nothing, which is a registry
question. Both belong to apex-frontmatter-repair, which may read documents
and judge. Every touched file is re-verified and rolled back if its
finding count did not fall.

Examples:

```
  ape story verify --output-format json
  ape story verify --file development/implementation/1-1_thing.md --active-extensions ext-adrs
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--active-extensions` | string | `—` | Comma-separated active extensions for --file mode (e.g. ext-adrs,ext-features) |
| `--check` | bool | `false` | With --fix: report the repairs without writing |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--file` | string | `—` | Verify one story file as a gate (exit 0/2/3/4) |
| `--fix` | bool | `false` | Repair the findings with a single derivable answer (depends_on quoting), and report the rest |
| `--include-advisory` | bool | `false` | Also report the advisory classes (story.requirement_ids_missing). Off by default: they fire on every story until a backfill completes, and `ape doctor` reds may not be worked around. |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--strict` | bool | `false` | Exit 1 when there are findings (default: report and exit 0) — NEVER set this from apex-review-story, apex-code-review or apex-epic-batch-review |

## ape task

Run a single framework skill through the interactive PTY runner

```
ape task <skill> [flags]
```

Run one framework skill as a single-step interactive run — everything a
pipeline step gets (agent prefix, preflight, bridge hooks, manifest,
telemetry) with all parameters passed as flags instead of a pipeline
YAML file. Execution is PTY-interactive only: claude runs as a REPL,
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
the milestone's "stops" in _apex/state/milestones.json, committed alone as
"apex: <M> stopped: budget"; `ape status` shows it. Raising the Budget:
line (a person's call) lets sessions start again. An unparseable
_apex/config.yaml in a v1 project refuses every session (exit 2) rather
than run it with no limits.

--prompt-file <path> reads the prompt from a file, and "-" reads it
from stdin. It is how a printed escalation command carries the
operator's own words: argv runs command substitution on backticks and
reads a leading dash as a flag, so text that a person wrote never goes
through it. The text is held to the same shape a REPL can be typed:
one line, no control characters, no trailing backslash.

--handoff <file> is a shorthand for --prompt: it checks the file
exists and derives the prompt "Read <abs-path> and follow the Resume
Protocol inside it." (the same continuation prompt the /handoff skill
suggests). It still requires --prompt-flag to actually reach the
skill, and is mutually exclusive with --prompt.

A line longer than 700 characters is not typed. Claude Code turns a
typed burst past 800 characters into a "[Pasted text #N]" placeholder
that never runs as a skill, so ape writes the step's --args and prompt
text, verbatim and with its newlines, to
stages/NN-<stage>/step-NN-<skill>.args.md in the run directory, and
types a line that names that file instead. The step-start event records
it as args_file. Shorter lines are typed exactly as before.

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

A project with NO commit-owners.csv is not asserted at all: with nothing
declaring which skills commit, neither assertion has a basis, and the
verdict is reported as skipped rather than guessed in either direction.
Rows naming a skill ape never dispatches (the conducting session's own)
are simply never reached. --task-commit is ape's own commit and is not
judged against a skill's declaration.

The verdict is written to the run manifest as commit_contract, so a
failure stays diagnosable after the fact.

Exit codes: 0 success · 1 run failed, idle timeout, the typed line
never submitted, or an APEX v1 milestone budget stopped or refused the
session · 2 usage or
preflight error · 3 REPL never became ready (last pane on stderr) ·
5 upstream API failure · 6 the dispatch violated its declared commit
ownership (the run itself may have succeeded).

Examples:

```
  ape task apex-shard-doc --args "--doc prd"
  ape task apex-create-prd --agent apex-agent-pm --model "opus[1m]" --prompt "a greeter CLI" --prompt-flag --prompt
  ape task apex-create-prd --agent apex-agent-pm --handoff _output/handoffs/2026-07-05-x.md --prompt-flag --prompt
  ape task apex-shard-doc --task-commit "chore: shard prd"
  ape task apex-create-prd --agent apex-agent-pm --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--agent` | string | `—` | Framework agent (slash-command) fronting the skill: /<agent> --autonomous -- <skill> ... |
| `--args` | string | `—` | Verbatim skill args appended to the invocation (whitespace-separated) |
| `--commit-allow-dirty` | bool | `false` | Bypass the dirty-tree gate (relevant only with --task-commit) |
| `--cwd` | string | `—` | Project root directory (default: current working dir) |
| `--detach` | bool | `false` | Start the run in the background, detached from this shell, print its id, and return at once; `ape run wait <id>` waits for it |
| `--effort` | string | `—` | Explicit reasoning effort for the session and its sub-agents (low\|medium\|high\|xhigh\|max). Unset: the project's _apex/effort-defaults.yaml per model, or xhigh without one (see ape config effort). In an APEX v1 project: the session's effort only, passed as claude --effort; unset, models.<skill>, the parent session or _apex/config.yaml effort: decide, else none is passed |
| `--events-subject-prefix` | string | `ape.evt` | Subject root for progress events. |
| `--handoff` | string | `—` | Path to a handoff/context file; derives a "Read <path> and follow the Resume Protocol" --prompt value (mutually exclusive with --prompt) |
| `--idle-timeout` | duration | `0s` | Idle backstop: cancel only after this long with no progress across hook events or transcript growth (e.g. 15m); default matches pipeline (60m). PTY output is NOT an anchor here — see `ape prompt` |
| `--ignore-project-settings` | bool | `false` | Tell the spawned claude to skip project + local .claude/settings*.json |
| `--manifest-dir` | string | `—` | Override the run-artifact base dir (default: <project>/_output/ape/tasks; _apex/state/runs/tasks in an APEX v1 project) |
| `--max-duration` | duration | `3h0m0s` | Hard wall-clock ceiling regardless of progress (e.g. 3h); the clock resets on each sub-agent boundary, so a sequential batch skill is bounded per item, not per batch. 0 disables the cap. |
| `--model` | string | `—` | Claude model (APEX v1 unset: models.<skill> in _apex/config.yaml, else the parent session's, else claude's default). A bare family (fable, haiku, mythos, opus, sonnet) resolves to its current generation; sonnet-5 / claude-sonnet-5 / opus[1m] pin explicitly |
| `--nats-creds` | string | `—` | NATS .creds file; its user identity is baked into every subject (env APE_NATS_CREDS). |
| `--nats-url` | string | `—` | NATS server URL for progress events + transcript upload (env APE_NATS_URL). Empty disables both. |
| `--no-commit` | bool | `false` | Skill layer: tell the skill/framework not to commit (types --no-commit into the skill invocation; without it none is typed) |
| `--output-format` | string | `human` | Output format: human\|json (json = result envelope on stdout, progress on stderr) |
| `--output-style` | string | `—` | Output style pinned on the spawned session (default "Default"). Pass "inherit" to keep whatever style the machine is configured with. |
| `--prompt` | string | `—` | Run prompt forwarded via --prompt-flag (same semantics as pipeline --prompt) |
| `--prompt-file` | string | `—` | File holding the --prompt text; "-" reads stdin. Mutually exclusive with --prompt and --handoff |
| `--prompt-flag` | string | `—` | Skill flag name the --prompt value is forwarded through (spec prompt_flag equivalent) |
| `--quiet` | bool | `false` | Suppress the per-event progress stream |
| `--task-commit` | string | `—` | Task layer: commit the complete task at the end; bare flag derives "ape:task/<skill>" |
| `--transcript-store` | string | `nats-object` | Transcript blob backend: nats-object\|uri-offload (env APE_TRANSCRIPT_STORE). |
| `--upload-transcripts` | bool | `false` | At run end, upload the transcript set as content-addressed blobs (env APE_UPLOAD_TRANSCRIPTS=1). |

## ape trait

Manage and inspect traits

```
ape trait
```

Subcommands:

- `conflicts` — Check for conflicts between traits
- `list` — List all available traits
- `show` — Show details of a trait
- `validate` — Validate a trait YAML file

## ape trait conflicts

Check for conflicts between traits

```
ape trait conflicts <trait1> <trait2> [...] [flags]
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape trait list

List all available traits

```
ape trait list [flags]
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape trait show

Show details of a trait

```
ape trait show <name> [flags]
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape trait validate

Validate a trait YAML file

```
ape trait validate <file> [flags]
```

Examples:

```
  ape trait validate ./mytrait.yaml --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

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

