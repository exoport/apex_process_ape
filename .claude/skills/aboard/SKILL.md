---
name: aboard
description: 'Show the user something on this project''s shared board — a diagram, dependency graph, kanban, chart, annotated screenshot, question form, a channel for talking to another agent, or a bespoke HTML widget — and read back what they changed. Tabs are data you create and name, not a fixed set. Also covers writing .aboard/aboard.json safely when a board is live or another session is editing it, and what the board is for: a local, non-authoritative channel whose conclusions must be promoted into committed artifacts.'
when_to_use: 'Whenever an explanation would land better as a picture or an interactive thing than a paragraph; when a plan or dependency set has structure worth seeing; when you need structured input rather than asking several questions in prose; when you want the user to point at part of an image; when you need to coordinate with another agent session somewhere the user can watch; when the user says "show me", "draw this", "put it on the board", "make me a tab", "what does the board say", "I moved things around", or asks you to react to their edits; before editing .aboard/aboard.json for any reason; and whenever another Claude Code session may be editing the same project''s board.'
argument-hint: "--<recipe-name> optional prompt — run a recipe by name; without a recipe, describe what to show"
---

# The shared board

A local server in this project serves a board that the user and you both edit.
`.aboard/aboard.json` on disk is the single source of truth. You read it with
`Read`, write it with `ape aboard apply`; the user works in a browser tab docked
inside VS Code.

**Tabs are data, not a fixed menu.** A tab is a name you choose, a `type` that
picks a renderer, and its own state. Open one for whatever the moment needs and
remove it when it stops earning its place. The types below are capabilities, not
a list of tabs that exist.

## Arguments

Read `$ARGUMENTS` before anything else. It is either a recipe invocation or a
description of what to show.

**If `$ARGUMENTS` begins with `--<name>`** — a single token after two dashes,
before any other text — that token is a **recipe name**, and everything after it
is the **prompt**.

```sh
ape aboard recipes show <name>
```

Read the body it prints and follow it, treating the remaining text as the
subject. `/aboard --show-a-structure the auth migration` means: run
`ape aboard recipes show show-a-structure`, then apply that method to the auth
migration. The recipe is the method; the rest of the line is what it is about.

If you only want the tab skeleton and not the prose:

```sh
ape aboard recipes show <name> --template
```

That prints the recipe's ` ```aboard-template ` block as JSON and nothing else,
ready to edit and hand to `ape aboard apply`. A recipe with no such block exits
non-zero naming itself, rather than printing an empty document you would apply
as an empty tab.

**If `ape aboard recipes show` exits non-zero, the name is unknown.** Do not guess at
a near-miss. Run:

```sh
ape aboard recipes list
```

and tell the user which names exist here — one line per recipe (name, scope,
description), with the file's path and anything it shadows indented underneath,
which is more than the built-in set: a project can add its own under
`_apex/aboard/recipes/`, `_aboard/recipes/` or `.aboard/recipes/`, and the first
of those four wins on a name collision. [references/recipes.md](references/recipes.md) lists the
built-ins only, which is why `recipes list` is the complete answer.

**Otherwise `$ARGUMENTS` is a plain description** (or empty). Proceed normally:
read the rest of this skill, choose a type, and put the thing on the board. You
can still reach for a recipe on your own initiative — nothing about the `--name`
form is privileged, it is just the shorthand.

## Resuming after a context clear

Four commands, in this order, before touching a tab:

```sh
ape aboard status              # running? which URL? the caps beacon, and how many of
                           # the human's requests are still waiting on an agent
ape aboard requests            # what they have ASKED FOR, oldest first, naming the tab
ape aboard capabilities        # what this board can actually do — every type, every
                           # state field, every control in toolbar order, every
                           # colour token, every gesture, endpoint, command and flag
ape aboard journal --limit 20  # who changed what recently, including other sessions
```

**`ape aboard requests` comes second because it is the only one of the four that is
addressed to you.** It is the human's notes on a tab — fix this, the arrow points
the wrong way, drop the third column — and they were written while nobody was
watching, which is the whole reason there is a command for them rather than a
message. Read them before you decide what to do; an outstanding request beats
whatever you were going to pick up. See [Answering their requests](#answering-their-requests).

`ape aboard status` reports: running (use it), a stale record, or nothing (start it
with `ape aboard serve`). It answers for THIS project only; `ape aboard boards` is the
machine-wide version — every running board, whichever project it belongs to, read
out of the process table. Use it when you have lost track of which board a URL
belongs to, or before assuming a colleague's session is not already up. It is
Linux-only and says so (exit 2) elsewhere, where the answer is `ape aboard status`
inside each project. `status` also prints a `caps` line — the hash of what this board
can actually do. **If it warns that the skill reference was generated for a
different hash, this skill is describing a board that no longer exists**: ask
`ape aboard capabilities dag` (or any type) for the truth rather than reading the
stale file, then regenerate the two generated references with the binary you
have:

```bash
ape aboard capabilities --format md > .claude/skills/aboard/references/reference.generated.md
ape aboard recipes index                > .claude/skills/aboard/references/recipes.md
ape aboard capabilities --check   # 0 when they match the binary
```

Those three run anywhere the binary does. **`make caps` is not the remedy here**:
it is a target in aboard's own checkout, and this skill is copied into projects
that have no such Makefile — which is most of them. Use `make caps` only when you
are working in the aboard repository itself, where it also rebuilds the control
module the renderers import.

`ape aboard capabilities` needs no running server and answers on a fresh checkout, in
a project that never copied this skill. **Do not reconstruct the surface from
memory — ask the binary.** `ape aboard capabilities kanban` is the cheap per-type
version. Reach for it whenever you are about to guess at a field name, and note
that `ape aboard apply` warns on stderr when a write sets state no renderer reads,
which is the failure that otherwise looks like success.

**Never assume a port** — it is derived from the project root. Take the URL from
`ape aboard status` or `.aboard/run/instance.json`, and give it to the user the first
time:

> Board is at http://localhost:41837 — `Ctrl/Cmd+Shift+P` → "Simple Browser: Show" → paste that URL.

(That number is an EXAMPLE. This project derives its own; read it, do not copy it.)

If there is no board at all, `ape aboard init` creates `.aboard/aboard.json` and the
run directory and prints the gitignore line; `ape aboard init --example` seeds it with
15 tabs covering every renderer, each carrying a `note` saying what it
demonstrates (`kanban` twice, and `notes` as a block inside the `stack` tab);
`ape aboard init --gitignore` adds `.aboard/` to the project's `.gitignore`. `ape aboard serve` refuses to start without a state file and
says so.

## Two hard rules

**1. Never `Edit` or `Write` `.aboard/aboard.json` while a board is running.**
Those bypass compare-and-set, so a concurrent change from the browser or another
session is silently destroyed. Instead:

```sh
ape aboard apply --by "agent-1" < /tmp/next-aboard.json
```

Read the file, build the whole new document, apply it. **The document you READ is
the base**: `apply` sends its `rev` as the compare-and-set token, so read-edit-apply
is safe with no extra bookkeeping — as long as you edit the document you read
rather than assembling a fresh one from the schema. Do not fall back to `Edit`.
Direct editing is fine only when `ape aboard status` reports nothing running.

**A `409` is usually not the end of the write.** `apply` re-reads the board, asks
the journal which tabs moved since the base it started from, re-applies your tabs
where the server did not touch them, and retries **once** — so somebody dismissing
a notice in the browser no longer discards the document you built. It prints
`applied … (merged)` and names on stderr whose version it kept. Two cases still
stop, both deliberately:

- **a collision it will not pick a winner for** — usually you and somebody else
  changed the same tab, and it refuses exactly as the browser refuses to merge one
  silently. It also stops when it cannot TELL: the journal records a tab's state
  before a write but not its name, note or type, so a tab somebody else RENAMED
  while you were writing to a different one lands here too. The message says which
  of the two it is. Either way: re-read, redo the edit, apply again — the second
  attempt has the rename in its base and goes straight through.
- **a conflict it cannot reason about** — a `updatedAt` base rather than a `rev`,
  or a journal that has rotated past your base. It says why on stderr and hands
  back the plain refusal.

A document with **no `rev`** has no base, so it would overwrite everything written
since you last looked. `apply` refuses it (exit 2) and names `--force`.
**`--force` writes with no compare-and-set at all** and says so on stderr: right
for repairing a document the browser cannot render, or seeding a board from a
fixture, and wrong every other time — most of all as a way past a `409`, which is
another writer's work, not an obstacle.

`--by` is not decoration: it names you in `lastEditedBy` and on every tab you
touched, which is how the user and any other session tell who did what. Use
**`agent-1`, `agent-2`, `agent-<role>`** — not `claude`, which reads as a single
participant when there may be several. It is what the change banner shows.
`--by human` is refused from the CLI: the human's writes come from the browser,
and an agent claiming to be them would hide its own tracks.

**Check before you write, and read the warnings after.** `ape aboard apply --check`
runs every write-time check and posts nothing — no board need be running, and the
document needs no `rev`, because it asks about CONTENT and not about concurrency.
`--strict` turns any warning into a refusal (exit 1, nothing written), which is
what a loop that must stop rather than ship a wrong tab wants. Neither changes the
default: a warning warns, because a spec can lag its renderer.

The warnings no longer stop at your terminal. The server records them on the
journal entry and shows them to the human on the tab they are about, so a write
that draws an empty box is something you both find out about. That is a reason to
read your own stderr, not a reason to stop: you are the one still holding the
context to fix it, and you are the only one who can stop the write.

**`--label "…"` says WHY.** It rides the write, is stripped before the document is
stored, and lands on the journal entry, where `ape aboard journal`, `ape aboard watch` and
the trace tab print it. Use it for anything a later reader would have to
reconstruct. It is navigation inside a local, rotating file — never a record to
cite in a commit message or a document.

**A duplicate key is refused, not resolved.** If any object in the document sets
the same name twice — the shape a generated or hand-spliced document falls into —
`apply` exits non-zero and names the key, and so does the server. It used to be
taken last-wins, silently, so the field you thought you had set was the other
one. Invalid UTF-8 is refused for the same reason, and a field in the wrong case
(`"ID"` for `"id"`) is simply not matched rather than guessed at.

The shape of every write, with the id allocator and the `upsertTab` helper worth
pasting, is `ape aboard recipes show apply-a-write`.

**2. Never restart a healthy server.** Run `ape aboard status` first. If a board is
already running for this project, **use its URL** — do not kill it and start your
own, because a second session's restart takes the first session's server out from
under it. `ape aboard serve` will not do it for you either: it refuses to start a
second board for the same project and prints where the running one is
(`this project's board is already running at <url> (pid N)`). That refusal is the
answer, not an obstacle — and it is about the BOARD, not the port, so
`--port <something free>` is refused exactly the same way. Restart only after
changing Go code or the embedded web tree, and prefer saying so: it briefly
drops the other session's browser connection.

**Asking is only half a question.** If you need the answer before you can go on,
block for it instead of polling:

```sh
ape aboard wait --by "agent-1" --timeout 15m
```

The board's header then shows *notify agent-1* with a lit dot, and the human
pressing it releases you (exit 0; exit 3 means nobody came). Say what you are
waiting for when you start waiting. Full contract in
[references/capabilities.md](references/capabilities.md#waiting-for-the-human).

## What the board is FOR — and what it must never be

**A local, persistent, non-authoritative channel.** Read those three words
separately, because they are three different claims and only two of them are
obvious:

- **Local.** `.aboard/` is NOT committed in a normal project. The port is derived
  from the project root, the run directory and uploads are per-machine, and
  several developers on one repo each have their own board with their own agents.
  Committing it means a whole-file JSON conflict on every merge, in a blob nobody
  can review, over a conversation that was never theirs. In a real project add
  `.aboard/` to `.gitignore` — one line, which is why the layout puts everything
  under it. (The board's OWN repo is the exception: there, the board is the
  product.)
- **Persistent.** Not versioned is not the same as scratch. A gate request waiting
  on the human, a form half answered, a session parked on `ape aboard wait` — those
  must survive a restart and a week away. Do not treat the file as disposable.
- **Non-authoritative.** If the board and a committed document disagree, **the
  document wins.** The board is where a thing is worked out; the repo is where it
  lands.

Its job is bandwidth, not storage: a picture, a form, a gate and an approval queue
are better interfaces to a decision than a wall of terminal prose, and they are
what make human-in-the-loop bearable rather than exhausting. That is the whole
value. Nothing about that requires the exchange to be preserved.

### Three tiers, and matching the thing to its lifetime

Most of what happens on the board is NOT destined for the repo, and an
indiscriminate "write it all down" rule produces the opposite failure: a project
full of "which of these three?" transcripts and half-decisions that nobody reads
and that go stale. A stale document misleads, which is worse than a missing one.

So there are three places a thing can live, and the skill is choosing correctly
between them:

| tier | lifetime | what belongs there |
|---|---|---|
| **the agent's context** | dies at a context clear | the reasoning in flight |
| **the board** | survives clears, restarts, a week away — local, unversioned | the exchange itself |
| **the repo** | survives everything, shared, reviewed | what someone else would be WRONG without |

**The board is the middle tier, and that is the point of it.** It is working memory
that outlives your context: a design you are getting feedback on, a diagram you are
checking against their mental model, a "pick one of these three pending tasks", a
screenshot they are pointing at, a form half answered. None of that belongs in a
spec document. Its value is consumed by the exchange — it changes what you build
next, and then it is spent. Let it go, or clear it, without ceremony.

The discriminator, when you are unsure:

> **Would a future session, or another developer, be wrong without this?**

- **Yes** → it belongs in the project's own documents. A rule with a reason
  ("never trigger agent sessions from the UI", "the diff renderer is rejected,
  because asking becomes cheap") outlives everything, and a session that does not
  have it will re-derive the idea and propose it again.
- **No, but I need it to finish this** → the board. Say so in the tab's `note`, so
  a session arriving after a context clear knows the tab is live work rather than
  a leftover.
- **Neither** → spend it. A clarifying question that only changed the next hour is
  not documentation. Do not archive it, do not summarise it into a file nobody
  asked for.

A preference expressed once is transient. A decision with a reason is durable.
That difference is usually the whole judgement.

### Where a promoted thing goes — find it, do not invent it

**Every project keeps its decisions somewhere already. Go and look.** Likely homes,
roughly in order: an ADR directory (`docs/adr/`, `docs/decisions/`), a spec or
design document for the area you are working in, `ARCHITECTURE.md`,
`DECISIONS.md`, `CONTRIBUTING.md`, `CLAUDE.md` if the project has one, or the
commit message and PR description of the change that acts on the decision.

Two rules about the target:

- **Prefer the document the decision is ABOUT.** A decision about the cutover
  belongs in the cutover spec, not in a general decisions file, because that is
  where the next person reads before touching it.
- **Do not create a new decisions file when the project has one**, and do not
  create one at all without asking. A second place to look is worse than an
  imperfect first place — and `CLAUDE.md` is not the answer everywhere; in most
  projects it does not exist, and the specs live elsewhere entirely.

### The boundary — when to promote, decided per project

You cannot know in advance which exchanges will turn out durable, so do not try to
notice continuously. Promote at a **boundary**: a named moment where you ask, once,
*"did anything here become a rule?"*

Which boundary is the project's to choose, not the skill's:

- **the commit that acts on it** — natural where work lands in small commits, and
  the message is already answering "what and why";
- **before a tab is cleared or repurposed** — natural where the board carries a
  long-running exchange;
- **the end of a work session, or a PR description** — natural where changes are
  batched;
- **when a spec document is next edited** — natural in spec-led projects.

**Establish which one this project uses, and record that where the project records
decisions.** Then honour it. If nobody has decided, ask — it is a one-line question
and it prevents both failure modes at once.

Two things make a late promotion cheap enough to actually happen.

**The text is one command away.** `ape aboard export <tab-id-or-key>` prints the tab
as markdown for pasting into whatever document the project uses — decisions with
their reasons, answers beside their questions, a node tree, a chat transcript, a
`ui` tree as an indented outline with its `{bind}`s resolved. `--format csv` for
rows. A `log`, an `html` tab or a `trace` says it has no text form instead of
emitting an empty section — the log is a sidecar file, the widget is a page, and
the trace is what `ape aboard journal` prints. It reads the state file from disk, so it needs no running
server. Adapt what it gives you; do not paste it whole into a spec, because a
board tab and a document have different jobs.

**And the reason is still there to copy.** So capture the reason in the structure at the moment of the
exchange — the `gate` verdict's `reason`, a `vote` option's `comments`, the tab
`note`, the `chat` message where they explained it. The decision usually survives
on its own; the reason is what evaporates, and it is the half that stops the
argument recurring.

When you find a verdict with no reason and it looks durable, **ask for it and have
them write it on the decision** — a `gate` row takes a reason after the fact, and
records that it was added late. Do not invent the reason on their behalf and do not
promote a naked decision as though it had one: "allow" with no why is exactly what
gets re-litigated. And weigh a late reason as what it is — reconstructed, not
recorded — which is why the export marks it.

### Which comes first, the board or the document

**Put the cheapest rejectable thing in front of them first.** That is the whole
rule, and it decides the order without you having to pick a mode.

Three questions, in this order:

1. **What is the expensive commitment here?** The spec, the schema, the migration,
   the refactor — whatever costs real effort to produce.
2. **What assumption does it rest on that they could overturn?** Usually the
   *approach*, sometimes a constraint or a priority. Not the details; the thing
   that makes the whole artifact the wrong artifact if it is wrong.
3. **Put THAT on the board, in the cheapest form that can carry a rejection** —
   three bullets and a pick-one, a diagram, a `form` with the two open questions,
   a `gate` request. Then pay for the artifact.

The distinction that does the work: **what goes up first is the decision the
document depends on, not a draft of the document.** "Event-sourced or CRUD — here
is the trade-off in four lines" costs nothing to reject. "Here is my architecture
spec, thoughts?" costs both of you, and costs more than the writing:

- an unagreed document **anchors** them to your framing, so they end up editing
  your structure instead of stating their own;
- and once it exists, **sunk cost** pulls both of you toward keeping it — you
  defend a shape you only chose because something had to be chosen.

That is why "we can always throw it away and rewrite" understates the damage.

**Document first is a derived case, not a different mode.** When the document is
cheap enough to throw away, it IS the cheapest rejectable thing, so write it: a
short design note, a spec where the writing is how you work out the shape, or a
document that already exists and only needs confirming. The expense is the test,
not the format. A one-page note: write it. Forty pages of architecture: agree the
approach first.

Rejected as a test: *"has a shared understanding been reached?"* That is a fact
about their head, and asking yourself to judge it produces confident guesses. The
question above is about **what you are most likely to be wrong about and what it
would cost** — your own uncertainty and your own effort, both of which you can
actually see.

Two more things, whichever order you end up in:

- **Promotion is a rewrite, not an export.** A diagram you argued with carries
  rejected branches, variants and question marks; committed as documentation, the
  next reader cannot tell what was decided from what was merely considered.
  `ape aboard export` gives you the material, not the document.
- **When a document becomes the record, demote the tab**: clear it, or set its
  `note` to say it is superseded and by what. An editable, authoritative-looking
  working copy sitting beside the committed truth, with nothing marking which is
  which, is the shadow-record failure created on purpose.

Keep the argument as well as the outcome only when a rejected option is tempting
enough that someone will propose it again — the same instinct as keeping the
reason with a decision, and why "alternatives considered" earns its place in some
documents and is padding in others.

### The failure it guards against, in both directions

The board LOOKS authoritative — things have ids, an agent maintains it, there is a
heartbeat and a change banner — so both sides start treating it as the record, and
then it is cleared. Watch for:

- a decision whose only trace is a `gate` verdict → put the outcome in the commit
  that acts on it;
- a plan the human corrected by dragging nodes → restate the corrected model in
  prose where the code can see it;
- "as we agreed in ab42" in a commit message or a PR → **never**. That id means
  nothing to anyone else, or to you next month. Cite the artifact, not the tab.

And in the other direction, just as real: do not turn every exchange into a file.
If you find yourself writing a document whose only reader would be you, five
minutes ago, you are promoting something that should have been spent.

Two failure modes to watch for in yourself while the work is still moving, one per
direction:

- **Sprawl** — exchanges accumulating on the board, nothing landing anywhere. The
  tell is a tab you keep adding to without ever asking what it settled.
- **False authority** — a confident document about something nobody agreed to. The
  tell is the human editing your structure rather than telling you their own, or
  correcting details in a shape they never chose.

### What follows from it

- **Assume nothing exists.** Find a tab by `key` and upsert it; tolerate an empty
  board. A board can be cleared between two turns of the same task.
- **Do not build a shadow tracker.** If the project has issues, a `TODO`, or a doc
  that already holds the work list, the board reflects it — it does not replace
  it.
- **To show someone else**, export the tab (markdown, CSV, SVG from its
  right-click menu) and commit that. A genuinely shared board would need auth and
  hosting, and this server deliberately has neither.
- **The journal is local too.** `.aboard/run/journal.jsonl` — `journal.<name>.jsonl`
  if you are on a named board, which keeps its own — tells you who changed
  what on THIS machine. It is not a project audit trail; do not cite it as one.
  It is also the board's only undo: `ape aboard history <tab>` lists what a tab was,
  newest first, and `ape aboard history <tab> --at 1` prints a whole document
  `apply` accepts. Rotation keeps one generation, so the listing says where the
  record ends — read that line before promising the human a restore. A version
  from an entry written since 2026-08-26 (`schema: 2`) restores the tab's NAME,
  note and type as well as its state; an older one carries a state and nothing
  else, and `ape aboard history` marks which is which. Neither restores `touched`,
  `pendingRemoval` or `seen`: putting back a dismissed dot or an answered removal
  request is not an undo.

## Choosing a type

| you want to | type | what the human can do in it |
|---|---|---|
| show a hierarchy the user can restructure | `dag` | drag to move, **drop onto another node to reparent**, double-click to rename, edit title/note/status/parent, add, delete, pan, zoom |
| track work through states | `kanban` | drag between columns (yours: any names, any count), reorder, rename inline, reparent, add, delete, `j/k/h/l` keys; `readOnly: true` makes it yours to write and theirs to read |
| assert a shape: sequence, state machine, ER, gantt, chart | `diagram` | mermaid, 23 types; edit source, hover a node for its key |
| get typed answers | `form` | range, select, checkbox, text, textarea; reset |
| have them point at part of an image | `markup` | several images side by side, **region / ellipse / pen / move / resize**, per-mark colours and notes, hide marks |
| talk to another agent where the user can see it | `chat` | send and interject; each speaker coloured |
| say something no structure fits | `notes` | free text, edit freely; `markdown: true` renders it with a Read/Edit toggle |
| put many typed rows in front of them | `table` | edit cells in place (text, number, select, checkbox, longtext), sort by header, add / duplicate / delete rows, copy CSV or markdown |
| get a yes, a no, or a "not like that" | `gate` | allow / deny / edit-then-allow, each with a reason; pair it with `ape aboard wait --for "answer <tab>"` |
| show output as it happens | `log` | follow, filter, ANSI colour — lines live in a sidecar file, fed by `ape aboard log <id>` |
| show who did what, when | `trace` | one lane per actor, a dot per write, click for detail; reads the journal, not the state file |
| let several participants score options | `vote` | click to score, click again to clear; a wide split is called out rather than averaged |
| lay something out without writing code | `ui` | a component tree drawn by trusted components — no iframe, no script; buttons record intent |
| build a bespoke interactive thing | `html` | anything you write — canvas, drag-and-drop, WebGL — sandboxed, no network |
| show several of the above together | `stack` | collapsible blocks top to bottom |

That table is a summary. **[references/capabilities.md](references/capabilities.md)
is the complete inventory** — every field of every type, every command, every
guarantee. Read it before concluding the board cannot do something.

Three rules of thumb: **`dag` when you want the shape argued with, `diagram` when
the shape is yours to assert**; **`ui` when the layout is ordinary and `html` when
the interaction itself is the point**; and **`table` the moment you find yourself
writing rows into `notes`.** And **`stack` whenever the answer is really
"look at this, then decide this"** — that is most of the time, and one composite
tab beats three tabs the user has to correlate.

**Say who is minding a tab you own.** On a `readOnly` kanban, set
`state.heartbeat = {by, at, phase: 'working'|'idle', note}` when you start and
finish. The strip pulses while `working` and under 90s old, and goes visibly stale
after ten minutes whatever the phase claims — so use real timestamps, or the
indicator lies, which is worse than not having one.

**Hand a decision back without asking them to type.** `state.actions:
[{id,label,intent}]` renders a button strip on any tab; a press appends to
`state.intents` and nothing executes. Read the intents next turn.

**How much room a view takes is your call, not the renderer's:** `state.height`
accepts any CSS length on `dag`, `chat`, `html`, `log` and `trace`. Kanban columns
are yours too — `state.columns` takes any names, any count.

**Prefer `ui` over `html` whenever `ui` can express it.** A `ui` tree is drawn by
trusted components, so it cannot get the theme, the contrast or the type sizes
wrong; there is no iframe or CSP to reason about; and the next session can change
one node of it instead of reading a page of your JavaScript. Reports, summaries,
dashboards, small forms, stats, comparison tables: `ui`.

**Ask for the component's props rather than guessing them** — `aboard
capabilities ui` lists all 25 with what each one reads, including the fixed item
shapes (`kv` takes `pairs[{key, value}]`, not `items[{k, v}]`). Guessing is the
expensive mistake here: an unknown component TYPE draws a visible marker, but an
unknown PROP draws nothing at all, so the tab renders as a titled card wrapping an
empty box and looks like a styling problem. `ape aboard apply` warns about both, and
about a `{bind}` that resolves nowhere.

Reach for `html` only when the INTERACTION is the point and no arrangement of
components gets there — a canvas, a drag-and-drop sorter, a simulation, WebGL. It
gets no network access, so it cannot fetch or exfiltrate; it persists state by
calling `aboard.set()`. If you are reaching for `html` to lay out text and numbers,
use `ui`. An `html` block inside a `stack` works like any other block.

## What travels between projects

The renderers and their spec files are compiled into the binary, so every gesture
and every state field works — and documents itself via `ape aboard capabilities` — in
any project you drop the binary into, with no skill and no server. Built-in
recipes travel the same way. `.aboard/aboard.json` is that project's content and
travels with nothing. This skill travels only if copied in, which is why
`ape aboard status` warns when its generated half no longer matches the binary.

## Colour

**Name a colour, never pick one.** Everywhere a colour is yours to set — a `ui`
node's `tone`, a `markup` mark's `color`, a `diagram` `classDef`, a widget's own
CSS — you write a token NAME (`accent`, `mark`, `agent`, `focus`, `danger`) or
`var(--token)`. `ape aboard capabilities` lists them under `theme`, and `ape aboard apply`
warns when a write names one the board does not have.

The reason is not tidiness. There are **two themes** — dark, the default, and
light, which the human switches to in the topbar — and a project may patch either
from `.aboard/theme.json`. A hex you chose is a colour that is right in one of
those and wrong in the others, and nothing will tell you: an unknown token name
warns, but a perfectly valid `#a4bd00` renders exactly as asked, illegibly, on
somebody's light board.

So: **do not read `.aboard/theme.json` before naming a colour.** The names are
yours and the values are the theme's — that split is the whole point, and looking
up what `--accent` currently resolves to is how an agent talks itself into
hard-coding it. The one thing worth knowing about a project's theme file is that
it exists, which `ape aboard status` tells you without being asked.

## Ids

**Never reuse an id, and never compute one from "highest in this container + 1".**
Take the board-wide counter:

```js
const id = 'ab' + doc.nextId; doc.nextId += 1;
```

Ids are how you and the user refer to things across turns. A reused id silently
re-points an instruction at a different object — which is why the counter is
board-wide and the server refuses to let it regress. An id is unique board-wide,
so it never needs qualifying by tab.

**Write `ab<n>`; read anything.** The `ab` tag (as in aboard) is there so an
id survives being written in a sentence: `ab49` is unmistakably a board object
where `49` is any number at all, and most of what passes between you and the user
is sentences. Say `ab49`, not `49`, whenever you mean the object. Bare `49` and
the retired `n49` and `bb49` still parse everywhere, so a board written before a
tag changed keeps working — every change of tag renamed nothing already on disk.

There is still no TYPE prefix (`node-7`, `tab-3`): that is a closed vocabulary in
a system where you invent new kinds of object, so it would be guessed ad hoc and
stop meaning anything, and the kind is already implied by where the object sits.

### Ids do not travel in both directions

**An id is enough coming FROM the human. It is not enough going TO them.**

That asymmetry is real and it is not about politeness:

- **They say "ab32" → you are fine.** You can read the state file in a second and
  find out exactly what it is. Take the id and act.
- **You say "ab32" → they may have no idea.** They cannot grep the board from
  their head. An id they saw ten minutes ago is still live; an id from yesterday,
  or from before a context clear, is a meaningless token — and they will have to
  go and look it up, which is work you just handed them.

So **when you address the human, name the thing and put the id beside it**:

> the Migration review tab (`ab32`) — its html block still needs a click

not "press the button in ab32". Same for nodes, marks and cards: *"the 'auth'
node (`ab58`)"*, *"the mark on the login screen (`ab168`)"*.

The id still earns its place — it is unambiguous, it survives being pasted, and it
is what you both point at once they know which object it is. It is a HANDLE, not a
NAME, and a handle needs a name attached the first time it appears in a message,
and again after any real gap.

This is the same instinct as never writing "as we agreed in ab42" in a commit
message, and it fails the same way: a reference that means nothing to the reader
is not a reference. The difference is only how long the window lasts — minutes in
conversation, forever in a commit.

Form *field* ids stay semantic and author-chosen (`strategy`, `window`); the
form itself gets a generated id.

## Creating, naming, removing

Give a tab a `key` when it has an ongoing purpose (`"plan"`, `"review"`). Next
turn, find that key and update the same tab instead of opening a second one. Tab
sprawl is the failure mode here.

**Read `tab.note` before you act on a tab, and write one when you make it.** It is
free text saying what the tab is FOR — the intent that its contents cannot carry.
A kanban of eight cards does not say whether it is a wish list or a commitment; a
markup tab does not say what the human was hoping you would notice. The human can
write and edit it (a strip under the tab strip, or the tab's right-click menu, or
the field in the New tab dialog), so treat it as an instruction, not decoration.

**You cannot delete a tab.** A write that drops one has it restored with a
`pendingRemoval` request the user answers in the UI. So do not try to delete —
*ask*, by setting `pendingRemoval: { by, at, reason }` with a reason worth
reading, and tell the user you have asked.

**You cannot clear a `touched` marker.** That marker raises the dot on the tab
and the banner inside it, and only the user dismissing it takes it down. Do not
try; the server carries it forward regardless.

## Answering their requests

`requests` is the one channel on this board that runs the other way: the human
points at a tab and says what is wrong with it. Each entry has an id from the
board allocator, so it can be named in a sentence and answered by name.

```sh
ape aboard requests                       # pending, oldest first, naming the tab
ape aboard requests --tab ab14 --all      # one tab, done ones included
ape aboard requests done ab199 --by agent-1 --note "redrew the arrow"
ape aboard wait --for request             # block until one exists or arrives
ape aboard wait --for "request ab14"      # one tab's
```

Three things about this to hold on to:

- **Write the `--note`.** The stamp is the only feedback the human gets that
  anything happened — it strikes their sentence through on the board and prints
  your name and the reply beside it. "done" tells them nothing they could not
  have guessed; "redrew the arrow, it points at the worker now" is an answer.
- **You may only stamp.** Creating, editing, reordering, deleting or un-stamping
  a request is refused by the server and the previous list restored — guarantee 5,
  the same enforcement `touched` gets. That includes the ordinary case that has
  nothing to do with intent: if you read the whole document, edit a tab and write
  it back without carrying `requests`, the server puts them back and logs it.
  Do not try to "tidy" one away; deleting is theirs.
- **`--for request` answers at once if one is already waiting.** Every other
  predicate is about a write that has not happened; a note left an hour ago is a
  fact about the document, and blocking on it would be asking them to write it
  twice.

A tab's `note` is a different field and does not work this way: it is your brief
statement of what the tab is FOR, written when you open it, and the human may
edit it. Purpose in the `note`, work in `requests` — a purpose rewritten into a
to-do stops being a purpose, and a to-do in the purpose strip has nowhere to
record that it was dealt with.

## Reading their edits

`ape aboard requests` is what they asked for in words. This is everything else — what
they changed by hand. Read `.aboard/aboard.json` and diff against what you last
applied. Highest signal first:

- `nodes[].parent` changed — they restructured your hierarchy. Treat it as a
  correction to your model, not noise.
- `form.fields[].value` — their answers.
- `markup` regions and strokes, plus each mark's `note` — where they pointed.
- a new `requests` entry on any tab — a direct ask, and the highest signal here.
  `ape aboard requests` is the reliable way to find them; a diff is how you notice one
  you have already stamped being deleted, which means they consider it settled.
- any `note` field — often a direct message to you. Read them all.
- `chat` messages with `by: "human"` — they interjected.
- `lastEditedBy: "human"` — there are edits you have not read.
- a tab with no `touched` that you marked earlier — they read and dismissed it.

Say what changed before acting on it:

> You moved "auth" under "platform" and set the downtime slider to 5 minutes — so I'll treat auth as a platform concern and plan for a 5-minute window.

`ape aboard recipes show react-to-their-edits` is the diff, written out.

## Where everything lives

One directory, `.aboard/`, at the project root — found by walking up from the
working directory, or from `--cwd`. One line in `.gitignore` covers all of it.

| path | what it is |
|---|---|
| `.aboard/aboard.json` | the board itself: the document you read and apply |
| `.aboard/aboard.<name>.json` | a second, isolated board (`ape aboard serve --name review`) — it owns every runtime file below as well; `uploads/` and `recipes/` are the two the project keeps. See [references/multi-session.md](references/multi-session.md) |
| `.aboard/uploads/` | images the human pasted or dropped — content, not runtime, and shared by every board in the project (`ape aboard uploads` reads them all, says which tabs mention each file, and prunes the rest behind `--yes`) |
| `.aboard/recipes/` | this project's own recipes, shadowing the built-ins by name — shared too |
| `.aboard/run/instance.json` | port, pid, URL of the running board — the discovery authority |
| `.aboard/run/instance.<name>.json` | the same, per named board |
| `.aboard/run/journal.jsonl` | every accepted write; what `trace`, `ape aboard journal` and `ape aboard history` read (`journal.<name>.jsonl` per named board) |
| `.aboard/run/rendered.json` | mount receipts: what a browser reported it drew, per tab (`ape aboard rendered`; `rendered.<name>.json` per named board) |
| `.aboard/run/logs/<tab>.log` | one sidecar log per `log` tab (`logs/<name>/<tab>.log` per named board) |
| `.aboard/run/shots/` | screenshots from the local browser suite |

The split is content against machine-local runtime: everything under `run/` is
true only for this machine and this moment, which is why nothing there is ever
worth keeping.

Recipes are looked up in four places, first match on a name wins:
`_apex/aboard/recipes/` → `_aboard/recipes/` → `.aboard/recipes/` → built into the
binary. Shadowing is allowed and always reported by `ape aboard recipes list`.

## Reference

- [references/capabilities.md](references/capabilities.md) — **the full surface**:
  every command, endpoint, tab type, field, and what the human can do in each.
  Read this when you want to know what is possible, not just what you remember.
- [references/schema.md](references/schema.md) — `.aboard/aboard.json` and every
  type's state.
- [references/multi-session.md](references/multi-session.md) — two sessions, ports,
  etiquette.
- [references/recipes.md](references/recipes.md) — the built-in recipe index
  (generated). `ape aboard recipes list` is the complete answer for this project.
- [references/reference.generated.md](references/reference.generated.md) — the
  facts, emitted by the binary (generated).

## Pitfalls

- **The web tree is embedded in the binary.** After editing anything under
  `pkg/aboard/web/`, rebuild and restart, or run `ape aboard serve --dev` to serve it
  from disk. Otherwise your change appears to do nothing.
- **A board already running is a success, not an obstacle.** `ape aboard status` tells
  you the URL; use it rather than starting a second server.
- **A new renderer type needs a line in the `TYPES` registry in the shell.**
  A tab whose `type` has no renderer shows a "no renderer" notice.
- **Pen strokes serialise as one `"x,y x,y"` string**, not nested arrays. One
  scribble as arrays was 75% of the file's lines.
- **Per-viewer UI state stays in the browser** — selection, zoom, collapsed
  blocks, marks-hidden, and each tab's scroll position. Never write it into the
  state file.
- **Look at a screenshot before claiming a visual change works.** `make shot`
  then read the image. Several real bugs here passed every DOM and colour
  assertion and were obvious in a picture.
- **Render it and look before you say it is ready.** That applies to what you
  WRITE, not only to renderer code — a tab is a thing on someone's screen, and
  "I put it on the board" is a claim about how it looks. Three sessions in a row
  ended the same way: the agent said it was ready, the human looked, it was wrong.
  `ui` is where this bites, because it fails silently AND successfully: `apply`
  prints `applied` and exits 0 whether the tree renders or draws an empty box.
  Read the stderr warnings (they descend into a `ui` tree and into `stack`
  blocks, so a mistyped prop or a `{bind}` pointing nowhere is named at the write),
  then shoot the tab and read the picture. Neither step is optional, because the
  warnings cannot see a layout that is legal and still unreadable.
- **`ape aboard rendered <tab>` says what the browser actually drew** — the control
  ids on screen, the ones somebody pressed, and any unknown-component marker.
  Two things it is deliberately not: **no receipt means nobody had the tab open**,
  not that it failed; and a control listed there was **reached**, never proved
  correct. It is a third source, after the write warnings and the picture, not a
  replacement for either. `ape aboard wait --for "rendered <tab>"` blocks until a
  browser mounts it — which is waiting for a HUMAN, so say why in `--note`.
