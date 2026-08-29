# Everything the board can do

A complete surface map: every command, every tab type, every field, and — for
each type — what the human can do in it, since half of using the board well is
telling them what to try.

This file carries JUDGMENT: when to reach for which type, what the human can do,
what not to do. The FACTS — every state field, every control, every gesture, every
endpoint, every command and flag — are emitted by the binary itself into
[reference.generated.md](reference.generated.md), and `ape aboard status` warns when
that file no longer matches. So: read this for how to think, read the generated one
(or `ape aboard capabilities <type>`) for what exists.

Where the two disagree, the generated one is right and this one is stale.

**Two different lists describe what the human can do, and the difference matters
when you are deciding how much to trust them.**

- **`controls`** — the buttons. Each is declared in
  `pkg/aboard/web/views/<type>.spec.json` and the renderer draws it FROM that
  declaration, so the label on screen and the description here are one edit. A
  control that is not declared renders as a visible marker, and the suite fails on
  a declaration nothing uses. This list cannot quietly go stale.
- **`gestures`** — everything with no button behind it: drag, drop, wheel,
  double-click, right-click, type-and-it-saves. Prose, reviewed by people, and
  **not verifiable by anything**. No check can confirm a sentence still describes
  a set of pointer handlers. Treat it as good but not guaranteed, and if you find
  one that is wrong, fix the spec rather than working around it.

That split is why `gestures` got shorter: entries that merely restated a button
moved into that button's own `doc`. The unverifiable half should be as small as
the truth allows.

## Commands

```sh
ape aboard status                              # is a board running here? URL, pid, state file, caps beacon
ape aboard boards                              # every board running on this machine (Linux; exit 2 elsewhere)
ape aboard init --example --gitignore          # create .aboard/, seed it, ignore it
ape aboard serve                               # run the server for this project
ape aboard apply --by "agent-1" < next.json    # compare-and-set write (the only safe write)
ape aboard requests                            # what the human has asked for, oldest first, naming the tab
ape aboard requests done ab199 --by agent-1 --note "redrew the arrow"   # say you acted on one
ape aboard wait --by "agent-1" --timeout 10m   # block until the human presses Notify (exit 0), or give up (exit 3)
ape aboard poke --by "agent-1" --note "go"     # release every waiting session, as the button does
ape aboard watch                               # every change as JSON lines, until interrupted
ape aboard journal --limit 40                  # recent writes: when, who, which tabs
<cmd> 2>&1 | ape aboard log ab126              # stream output into a log tab
ape aboard export ab32 --format csv            # one tab as text, for pasting into a document
ape aboard capabilities ui                     # what this board can do — no server needed
ape aboard recipes list                        # every recipe available here, with scope and shadowing
ape aboard recipes show show-a-structure       # one recipe's body; --template for just the JSON skeleton
ape aboard version                             # which binary is this
make caps | e2e | shot | build | test | lint | status
```

Every command takes `--cwd DIR` on the root, to resolve the project from
somewhere other than the working directory, and `--name N` (env `ABOARD_NAME`) to
address a second, isolated board. `status`, `boards`, `journal`, `requests`,
`recipes list` and `version` take `--output-format human|json|yaml`. `boards` is the one command
that needs no project at all — it asks the process table, so it answers from a
directory that has never held a board, and it is also the one command that cannot
answer everywhere: `/proc` is Linux-only, so on macOS and Windows it exits 2 with
the reason and points at `ape aboard status` per project. The complete flag table, per
command, is in [reference.generated.md](reference.generated.md) — it is generated
from the same declaration the cobra tree is asserted against, so it cannot drift.

Exit codes mean one thing each: **0** done, **1** it ran and failed (no board, a
refused write, a broken connection), **2** usage — a flag or argument the command
cannot act on, caught before anything was contacted — and **3**, which only
`wait` produces: nobody came.

`--for` takes: `poke` (the human's Notify button), `change` (any write),
`tab <id>`, `answer <id>` (that tab changed AND a human did it),
`node <id>=<status>`, or `rendered <id>` (a browser MOUNTED that tab and posted a
receipt). Anything else is refused immediately rather than blocking on something
that will never fire. `rendered` is the one form that is not about a write —
nothing on the board can cause it, so it is a wait for a HUMAN to open the tab.

`--by human` is refused from the CLI. The human's writes come from the browser;
an agent claiming to be them would hide its own tracks in the journal, which is
the one record neither side can rewrite.

## Endpoints

| route | purpose |
|---|---|
| `GET /` | the board UI |
| `GET /aboard.json` | current state |
| `POST /aboard.json` | write, compare-and-set (`__base` is the document's `rev`; `__origin`, `__by`). Same-origin only |
| `GET /events` | SSE: the state changed (`{origin}`), waiter count changed (`{waiters:n}`), and the UI signature (`{ui:{html,css,js}}`) — sent first on every connect so a page notices its own code changed and reloads |
| `GET /health` | `{app, project, port, url, state, pid}` — who owns this port, and which binary is serving |
| `GET /tab/<id>/html` | one `html` tab as a standalone sandboxed document |
| `GET /wait` | long poll: blocks until poked (`?for=poke&timeout=<secs>&by=<label>&note=<why>`) |
| `POST /poke` | release every waiting session — what the Notify button calls |
| `GET /waiters` | `{waiting, waiters:[{by,for,since,until,timeout,note}], lastPoke}` |
| `GET /journal` | `?limit=n` — recent accepted writes, each with who, when, which tabs, and the previous state of each changed tab |
| `GET /watch` | the same writes as newline-delimited JSON, as they happen |
| `POST /log` | `?tab=<id>` — append output to a tab's sidecar log |
| `GET /log` | `?tab=<id>&tail=n` — the last n lines |
| `POST /upload` | an image body; answers `{url}`. Type sniffed from the bytes, 12 MiB cap, server names the file |
| `GET /uploads/<file>` | serve one, always from disk (uploads arrive after the build) |

`--base-path /prefix` moves all of them under that prefix; the shell is served
with the prefix injected, so every fetch, the SSE stream and an html tab's iframe
build from it.

`.aboard/run/instance.json` records the running board. Read it rather than
assuming a port; the port is derived from the project root.

## Document

```jsonc
{ "version": 1,          // server-managed; stamped on every write
  "rev": 41,             // server-managed, and the compare-and-set base
  "updatedAt": "…",      // server-managed; when, for a human reading this
  "lastEditedBy": "…",   // server-managed from --by
  "nextId": 148,         // server-managed id allocator
  "tabs": [ … ] }
```

All five are the server's, not yours. Set none of them — a hand-written `version`
in particular is the one mistake that blanks the entire board rather than one
field, since the browser will not render a schema it does not know.

Ids are board-wide monotonic, never reused, tagged `ab` and with no type prefix:
`ab49`, `ab148`. Allocate with `'ab' + doc.nextId++`. Bare and legacy ids still
parse, so `49` still resolves to `ab49`. Refer to objects as `ab49` in prose —
that is what the tag is for. Form *field* ids stay semantic.

## Tab

| field | meaning |
|---|---|
| `id` | generated, unique board-wide |
| `key` | optional stable handle — find by it to update the same tab next turn |
| `name` | what the user sees; you choose it |
| `type` | picks the renderer |
| `state` | type-specific (below) |
| `stateFrom` | render another tab's state with this type |
| `note` | what the tab is FOR, in whoever's words — read it before acting |
| `touched` | server-set when an agent changed the tab; only the user clears it |
| `pendingRemoval` | `{by, at, reason}` — a removal request the user answers |

You cannot delete a tab and cannot clear `touched`. Both are enforced server-side.

## Types

### dag — hierarchy the user can restructure

```jsonc
{ "columns": ["todo","doing","done"], "height": "72vh", "density": 130,
  "nodes": [ { "id":"ab7", "title":"…", "parent":"ab5", "status":"doing",
               "order":3, "note":"…", "pos":{"x":120,"y":208} } ] }
```

The human can: click to select, **double-click to rename in place**, drag to move,
**drop one node onto another to reparent**, drag background to pan, wheel to zoom,
click empty space to deselect, Fit, Re-layout (discards manual `pos`), Add root,
Add child, Delete (children re-parent, modal confirm), and edit title / note /
status / parent in the panel below the canvas.

`parent` and `status` are independent axes. `pos` only exists when the human
dragged a node — delete it to let the tidy-tree layout place it again. Never
create a `parent` cycle.

### kanban — the same nodes by state

```jsonc
{ "columns": ["backlog","spec","building","review","shipped"],
  "readOnly": false,
  "nodes": [ … same shape as dag … ] }
```

Columns are entirely yours: any names, any count. The human can drag cards
between columns, reorder with `▲▼`, edit a title inline, repoint `parent`, add
and delete nodes.

`heartbeat` — `{by, at, phase: 'working'|'idle', note}` — says who is minding a
tab you own. It pulses while `working` and under 90s old and goes visibly stale
after ten minutes whatever the phase claims, so the timestamps must be real. A
subagent that only exists while it runs should stamp `working` when it starts and
`idle` when it finishes; between tasks, stale is the honest reading.

**`readOnly: true` makes it agent-owned and the human a reader.** Drag, inline
rename, reorder, reparent, add and delete are removed — not disabled — and a
`read-only` badge says why, because a card that can be dragged and snaps back
reads as a bug. Titles, notes, id chips and counts stay. Use it when the board
reflects state you maintain (a work queue you move through) rather than a
question you are asking; use a normal kanban when you want the arrangement
argued with. It is an interface contract, not a permission: the human can still
dismiss the change banner, and nothing stops a write from elsewhere.

Pair a `dag` tab with a `kanban` tab whose `stateFrom` points at it to get both
readings of one dataset.

### diagram — mermaid

```jsonc
{ "source": "graph TD\n    A[\"x\"] --> B[\"y\"]" }
```

23 types render: `flowchart`/`graph`, `sequenceDiagram`, `classDiagram`,
`stateDiagram-v2`, `erDiagram`, `requirementDiagram`, `C4Context`, `journey`,
`gantt`, `timeline`, `kanban`, `pie`, `xychart-beta`, `quadrantChart`,
`radar-beta`, `sankey-beta`, `treemap-beta`, `mindmap`, `gitGraph`, `block-beta`,
`packet-beta`, `architecture-beta`. Only `zenuml` is unavailable.

Colours come from the board tokens automatically, and a diagram RE-RENDERS when
the human switches theme — mermaid bakes literal colours into the SVG, so nothing
else could reach it. **Add colours** in the toolbar appends `classDef` classes
built from the live palette — prefer the ring form (surface fill, coloured border)
over solid fills, which reads better in both themes. Chart types (`pie`,
`xychart`, `radar`) ignore `classDef`.

The block it writes carries the token values as they stand when the button is
pressed, so a diagram whose source was hand-coloured on a dark board keeps those
hexes on a light one. That is the one place a `classDef` is worth re-running the
button on rather than editing by hand.

Hovering a node shows `key — label`, where `key` is the mermaid node id you need
in order to edit the source.

The human can: edit the source, re-render, copy source, hide the editor.

A diagram does not have to be a whole tab: a ` ```mermaid ` fence inside a
`notes` block with `markdown: true` renders through this same loader and theme.
Use a `diagram` TAB when the diagram is the subject and the human should edit its
source; use a fence when it is a figure inside prose.

Gotcha: in `requirementDiagram`, quote any `text:` containing punctuation.

### form — typed answers

```jsonc
{ "id": "ab46", "title": "Cutover", "intro": "Answer and I will act.",
  "fields": [
    { "id":"strategy", "type":"select", "label":"…", "options":["a","b"], "value":"a" },
    { "id":"window", "type":"range", "label":"…", "min":0, "max":60, "step":5, "value":10, "hint":"…" },
    { "id":"keep", "type":"checkbox", "label":"…", "value":true },
    { "id":"name", "type":"text", "label":"…", "value":"", "placeholder":"…" },
    { "id":"notes", "type":"textarea", "label":"…", "value":"" } ] }
```

Five field types. The form has a generated `id`; **field ids stay semantic** —
you choose them and read answers back by them, so keep them stable when you only
reword. A default value may mean "not answered yet".

The human can answer, and Reset answers.

### markup — point at part of an image

```jsonc
{ "layout": "side-by-side",              // or "stacked"
  "images": [
    { "id":"ab1", "src":"uploads/before.png", "caption":"Before", "annotatable":true,
      "regions":[ { "id":"ab2","x":0.47,"y":0.27,"w":0.24,"h":0.19,
                    "note":"…","color":"mark","shape":"ellipse" } ],
      "strokes":[ { "id":"ab3","points":"0.10,0.42 0.11,0.43","note":"","color":"focus" } ] },
    { "id":"ab4", "src":"uploads/after.png", "caption":"After", "annotatable":false } ] }
```

- **Coordinates are normalized 0..1** against each image's own box, never pixels.
  Multiply by the image's real size to name what was marked, and say it back in
  words so the user knows you understood.
- Several images side by side, each with its own marks and coordinate space.
- `annotatable: false` → a reference image with no overlay and no tools.
- `shape`: `"ellipse"`, or absent/`"rect"` for a rectangle.
- `color`: a token **name** (`mark`, `accent`, `focus`, `agent`, `danger`), never
  a hex, so it survives a retheme — and there are now two themes plus whatever a
  project's `.aboard/theme.json` says, so a hex is a colour that is wrong for
  somebody. Absent means the default. The palette is declared, so `ape aboard apply`
  warns when a write names a colour the board does not have, and prints the ones
  it does.
- `strokes[].points` is one `"x,y x,y"` string. Keep the compact form.
- Images the human pastes or drops land in `.aboard/uploads/` and are served from
  `/uploads/<file>`; images you ship with the binary live in its embedded
  `assets/`.

The human can: draw with **region**, **ellipse** or **pen**; **move** a mark;
**resize** a rect or ellipse by its handles; hide/show marks per image; note each
mark; delete one; recolour one, or every mark in a scope via the `Colour ▾`
header (modal confirm); clear all marks (modal confirm).

The legacy single-image shape (`image`, `caption`, `regions`, `strokes` at the
top level) is still read and migrated on load.

### chat — a visible work channel

```jsonc
{ "height": "62vh",
  "messages": [ { "id":"ab5", "at":"2026-08-22T09:14:00Z", "by":"agent-1", "text":"…" } ] }
```

For two agents coordinating where the human can watch and interject. Append only
— do not rewrite or delete others' messages. Use `agent-1` / `agent-2` /
`agent-<role>` for `by` so speakers read as distinct; each distinct `by` gets its
own colour. Messages with `by: "human"` are directed at you.

The human can send messages (Enter to send, Shift+Enter for a newline).

### notes — the plain escape hatch

```jsonc
{ "text": "Free-form text.", "markdown": false }
```

For anything no structure fits: a decision log, a summary, a paste. `markdown:
true` renders it with a Read/Edit toggle. The human can edit it freely and Copy
all. If you rewrite it while they are typing, they get a "reload text" affordance
rather than losing their sentence.

With `markdown: true`, a ` ```mermaid ` fence renders as a diagram — the same
vendored bundle and the same board-token theme the `diagram` tab uses, so a
figure can sit inside a write-up instead of needing a tab of its own. A fence
mermaid cannot parse shows its source verbatim rather than an empty box. Reach
for a `diagram` TAB when the diagram IS the subject and the human should edit its
source; reach for a fence when it is a figure inside prose you intend to promote.

### html — build anything

**Prefer `ui` over `html` whenever `ui` can express it.** Not a style note — three
concrete reasons:

- **It cannot be wrong about the theme.** A `ui` tree is drawn by components that
  read the board's tokens, so contrast, type sizes and dark-theme behaviour are
  correct by construction. Hand-written HTML gets them right only if you remember
  to, and the CSS custom properties are the only thing standing between a widget
  and unreadable text.
- **There is nothing to contain.** No iframe, no CSP, no sandbox to reason about,
  no script for anyone to review. A bad `ui` tree is a bad layout; a bad `html`
  tab is a question about what that script does.
- **It is legible to the next session.** A component tree diffs, and another agent
  can change one node of it. A page of your JavaScript is something the next agent
  must read in full before it dares touch a line.

So: a report, a summary, a dashboard, a small form, stats, a comparison table —
`ui`. Reach for `html` when the INTERACTION is the point and no arrangement of
components gets there: a canvas, a drag-and-drop sorter, a simulation, WebGL, a
custom gesture. If you are reaching for `html` to lay out text and numbers, use
`ui` instead.

```jsonc
{ "html": "<h3>…</h3><script>…</script>", "data": { … }, "height": "62vh" }
```

Served from `/tab/<id>/html` into an iframe with `sandbox="allow-scripts"` (no
`allow-same-origin`) behind a CSP whose `connect-src` is `'none'`. So it can do
anything local — canvas, SVG, WebGL, animation, drag-and-drop — and **nothing
over the network**. That containment is deliberate: the server has no auth, so
anything that could reach it could rewrite the whole board.

**No `alert`, `confirm` or `prompt`.** The sandbox omits `allow-modals`, so all
three are suppressed — `confirm()` returns `false`, `prompt()` returns `null`,
nothing is drawn and nothing is logged — and a widget that asks before acting
neither asks nor acts. Use a `<dialog>` of your own, which the sandbox does not
touch. The same trap caught the board's own shell inside a VS Code panel, where
three gestures were dead for a day; `views/dialog.js` is what it uses now.

It inherits the board palette as CSS custom properties; override freely. Plain
HTML — no build step, no imports, no framework. The palette is `app.css`'s own
`:root`, parsed and injected — **every** token the board has, not a subset that
was accurate once — so `var(--status-doing)` or `var(--accent-dim)` resolves in a
widget exactly as it does in a renderer. `ape aboard capabilities` lists the token
names under `theme`; `app.css` is where they are stated, once, and the manifest
reads them from there.

**Both themes are injected**, not just the one in force, and the parent flips
`data-theme` on the frame when the human switches (and re-sends the project's own
overrides when `.aboard/theme.json` changes) — so a widget that colours from
tokens follows the board with no work, and one that hard-codes a hex is the only
thing on the page that will look wrong in the other theme. If your widget needs
to KNOW, read `document.documentElement.dataset.theme` (`dark` or `light`); do not
sniff a colour.

Inside the frame:

```js
aboard.get()        // the persisted data object
aboard.set(next)    // persist it — the parent writes state.data
aboard.onData(fn)   // fires when it changed elsewhere (another agent, another viewer)
aboard.fit()        // ask the parent to size the frame to the content
```

Reach for this when the interaction *is* the point: a custom sorter, a comparison
matrix, a small simulation. A drag-to-reorder list takes about 40 lines.

The human can: use whatever you built, Reload the frame, and Show source (which
also shows the stored `data`).

### table — typed rows they edit in place

```jsonc
{ "addLabel": "Add finding", "readOnly": false,
  "columns": [ { "id":"file", "label":"File", "type":"text", "width":"22ch", "hint":"path" },
               { "id":"risk", "label":"Risk", "type":"select", "options":["low","high"] },
               { "id":"cost", "label":"Cost", "type":"number" },
               { "id":"ok",   "label":"Done", "type":"checkbox" },
               { "id":"why",  "label":"Why",  "type":"longtext" } ],
  "rows": [ { "id":"ab51", "file":"main.go", "risk":"high", "cost":3, "ok":false, "why":"…" } ] }
```

Five cell types. The human edits in place (saves as they type), sorts by clicking
a header, adds / duplicates / deletes rows, and copies the whole thing as CSV or
markdown. Sort order and column width are per-viewer — never written. `readOnly`
behaves as on the kanban. **Reach for this the moment you are about to write rows
into `notes`.**

### gate — allow, deny, or "not like that"

```jsonc
{ "pending": [ { "id":"ab129", "title":"Delete the staging bucket", "risk":"high",
                 "detail":"…", "command":"aws s3 rb s3://…", "by":"agent-1" } ],
  "decided": [ { "id":"ab129", "title":"…", "verdict":"edit", "reason":"keep the logs",
                 "editedTo":"aws s3 rm s3://…/tmp", "at":"…", "by":"human" } ] }
```

`risk` is `low` / `medium` / `high` and colours the card. The human allows, denies,
or edits the command and then allows — which records `verdict: "edit"` with
`editedTo`, the most useful of the four because it tells you what right looks like
rather than only that you were wrong. A reason is optional on every verdict.

**Nothing here executes.** A decision is a record; the agent that asked reads it
and acts. Pair it with a real wait, or a stale queue will have the human believing
they gated something that already ran:

```sh
ape aboard apply --by "agent-1" < next.json          # add to state.pending
ape aboard wait --by "agent-1" --for "answer ab128"  # block until a human decides
```

### log — output as it happens

```jsonc
{ "source": "ab126", "tail": 400, "follow": true, "height": "46vh" }
```

The lines are NOT in the state document — they live in a sidecar file the server
owns (`.aboard/run/logs/<tab>.log`), because the document is rewritten whole on
every write. Feed it by piping, and the tab picks it up within two seconds:

```sh
go test ./... 2>&1 | ape aboard log ab126
```

The human can follow (scrolling up pauses it, scrolling to the bottom resumes),
filter, and copy. ANSI colour is mapped onto the board's tokens. Polling stops
while the tab is off screen.

### trace — who did what, when

```jsonc
{ "limit": 200, "height": "44vh" }
```

Reads the journal, not the state document: one lane per actor, a dot per accepted
write, click a dot for the tabs it changed, click an actor chip to filter. That
also means history is not something an agent can quietly rewrite. Useful the
moment two sessions share a board — `lastEditedBy` only ever names the last one.

### vote — several participants score the same options

```jsonc
{ "question": "Which cutover?", "scale": 5, "closed": false,
  "options": [ { "id":"dual", "label":"Dual write", "note":"…" } ],
  "ballots": { "agent-1": { "dual": 4 }, "agent-2": { "dual": 2 }, "human": {} } }
```

Write your own key in `ballots`; the human's column is the editable one (click a
number, click it again to clear). A wide spread is called out as "split by n"
rather than averaged away, because the disagreement is the interesting part. Use it
when two sessions propose different things — otherwise whichever agent writes the
summary wins the argument.

### ui — a layout described as data

```jsonc
{ "data": { "count": 18 },
  "root": { "type": "col", "children": [
    { "type": "card", "title": "Today", "accent": true, "children": [
      { "type": "row", "children": [ { "type": "stat", "value": {"bind":"count"}, "label": "shipped", "tone": "accent" } ] },
      { "type": "text", "value": "…" },
      { "type": "button", "id": "again", "label": "Run it again", "intent": "re-run the suite" } ] } ] } }
```

The catalog is 25 components: `col`, `row`, `grid`, `card`, `tabs`, `title`,
`heading`, `text`, `caption`, `badge`, `notice`, `quote`, `code`, `divider`,
`spacer`, `list`, `checklist`, `kv`, `table`, `stat`, `meter`, `image`, `link`,
`button`, `field`. `tone` takes a token NAME (`accent`, `mark`, `agent`, `focus`,
`danger`, `muted`, `dim`), never a hex. `{"bind":"path"}` reads from `state.data`;
a `field` writes back into it; a `button` appends to `state.intents` and executes
nothing.

**Ask for the props rather than guessing them**: `ape aboard capabilities ui` lists
every component with what it reads, including the fixed item shapes (`kv` takes
`pairs[{key, value}]`, not `items[{k, v}]`). An unknown `type` renders a visible
marker; an unknown PROP renders nothing at all, which looks like a styling
problem rather than a mistake — so read the stderr warnings from `ape aboard apply`,
which descend into the tree and name an unknown component, an unknown prop, a
wrong item shape and a `{bind}` that resolves nowhere.

`ape aboard export <tab>` prints a `ui` tree as an indented outline, with every
`{bind}` resolved and every tick and typed answer read out of `state.data` — so
the type you are told to prefer is promotable without a browser. It is the
MATERIAL, not a screenshot: an outline cannot see a layout that is legal and
unreadable, so it is not evidence that the tab renders correctly.

No iframe and no script, so it inherits the board's type, contrast and palette for
free — the trade is a closed catalog. **`ui` for an ordinary shape, `html` when the
interaction itself is the point.**

### stack — several renderers in one tab

```jsonc
{ "blocks": [ { "id":"ab6", "type":"dag", "title":"Dependencies", "state":{ … } },
              { "id":"ab7", "type":"form", "title":"Decide", "state":{ … } },
              { "id":"ab8", "type":"markup", "title":"On screen", "state":{ … } } ] }
```

Blocks render top to bottom, each a full renderer with its own state, each
collapsible. Any type except `stack` — nesting is capped at one level.

Usually the right answer when the shape is "look at this, then decide that": one
composite tab beats three the user has to correlate.

An `html` block works inside a `stack` like any other: the frame is served from
`/tab/<tab>/<block>/html`, and `aboard.set()` writes to that block's own
`state.data`. It used to render blank, because the route required an exact tab of
type `html` and a block's id is `"<tab>/<block>"`.

## Sizing

`state.height` accepts any CSS length (or a number read as px) on `dag`, `chat`,
`html`, `log` and `trace`. Defaults fill the viewport. `dag` also takes `density`
for node spacing, `markup` takes `layout`.

## Waiting for the human

Asking on the board is only half a question — you also have to be there when the
answer lands. Instead of polling the state file, block:

```sh
ape aboard apply --by "agent-1" < next.json      # ask (a form, a markup, a chat message)
ape aboard wait  --by "agent-1" --timeout 15m    # then wait to be told to look
```

While you wait, the board's header shows **notify agent-1** with a lit dot: the
human can see that a session is listening, and pressing it releases you. Nobody
waiting means the button is disabled — a waiter is an open connection, so the
count cannot lie or go stale. The button never says anything but live state; the
acknowledgement of a press ("notified 1 session") is a flash beside it that goes
after a second and a half, because the poke itself changes the waiter count and
repaints the button.

- exit **0** — released. The event (`{event, at, by, note}`) is on stdout; re-read
  the state file and act on what changed.
- exit **3** — timed out. Nobody came. Say so rather than pretending you waited.
- `--for` narrows it: `poke`, `change`, `tab <id>`, `answer <id>` (that tab
  changed AND a human did it), `node <id>=<status>`, `rendered <id>` (a browser
  mounted that tab), `request [<tab>]` (the human has a note waiting for an
  agent). An unknown predicate is refused immediately rather than blocking on
  something that will never fire.
- **`poke` is the default because it is the only predicate that means "I have
  finished".** Every edit is a write, so `change` fires on the first save of an
  interaction — a human filling in a form writes once per field — and an agent
  waiting on it wakes mid-edit and reacts to answers that are not final. Use
  `change` for a board another agent is driving, where every write is final;
  use `poke` for a human.
- `request` is the one form that can be satisfied before you ask, and it answers
  at once when it is: a note left an hour ago is a fact about the document rather
  than an event still to come, and blocking on it would be asking the human to
  write the same note twice. It prints `{"event": "request"}` either way, where
  every other form prints `change`.

Tell the user you are waiting, and say what you are waiting for — a lit button
with no explanation is a mystery. `ape aboard poke` is the same gesture from the
other side, for handing off to another session (`agent-1` finishes, pokes,
`agent-2` wakes).

The browser suite (`make e2e`) drives its own temporary board, so it cannot release
you — that was true of the shell suite it replaced, which poked whatever board it
was aimed at.

## Guarantees you can rely on

- **Compare-and-set on every write.** A stale write is refused with `409`; re-read,
  redo, apply again. Never fall back to `Edit` on a live board.
- **Atomic writes.** Readers never see a half-written file.
- **An agent cannot delete a tab** — a dropped tab is restored as a request.
- **An agent cannot clear `touched`** — only the human's dismiss does.
- **An agent cannot un-ack a chat message.** Once a session stamps `ackBy` on a
  message, the human's edit/delete window on it is closed; dropping the ack would
  reopen a window on something already acted on, so acks are carried forward.
- **An agent cannot clear another actor's `seen` stamp.** `tab.seen` is per-actor
  read state (`{"human":"…","agent-2":"…"}`); a write may set its own key and
  nobody else's.
- **An agent cannot write the human's `requests`.** Those are their notes to you
  about a tab. You may only ADD a `done` stamp to one that exists; creating,
  editing, reordering, deleting or un-stamping is undone and the previous list
  restored — including the ordinary read-modify-write that hands the document back
  without the field. The `by` on a stamp is rewritten to whoever actually wrote it.
- **`nextId` never regresses** and always stays above every id in use.
- **One server per project**, on a port derived from the discovered project root.
- **Live reload.** Your write pings every open board over SSE.
- **A waiter cannot go stale.** `ape aboard wait` holds an open connection, so if the
  session dies the count drops and the Notify button greys out by itself.

## Adding a capability

A renderer is one ES module exporting `mount<Name>(root, ctx)` and returning
`{ refresh() }`, plus one line in the `TYPES` registry in the shell, one in the
browser suite's `MODULES`, and one `pkg/aboard/web/views/<type>.spec.json` — or it
mounts nowhere, is tested nowhere, and no agent ever learns it exists. `ctx` gives
you `state` (this tab's slice), `tab`, `save()`, `nextId()`, and — for a composite
renderer — `types()`, `initFor()`, `mountType()`.

Rules that keep the board coherent: colour only from the tokens in `app.css`
(never a hex) **and declare every one of them in both variants** — a token missing
from `:root[data-theme="light"]` silently inherits the dark value, which renders
as no error at all; keep per-viewer UI state in local JS (never in the state
document), never `innerHTML` with state values, never assign to `ctx.state` (it is
a getter — mutate in place), and in `refresh()` never clobber an input the human
has focus in. Anything that BAKES a token value into its output rather than
referencing it — a rendered SVG, a canvas — has to re-render on the
`aboard:theme` event, because no cascade can reach it.

The web tree is compiled into the binary, so after editing anything under
`pkg/aboard/web/`, rebuild and restart — or run `ape aboard serve --dev` to serve it
from disk while iterating. After editing a `*.spec.json` or a built-in recipe, run
`make caps` and commit what it writes: it regenerates the control module, the
generated reference and the recipe index, then asserts they match.

All of that is **aboard's own checkout**. In a project that only copied this
skill there is no Makefile and no spec to edit; the generated references are
refreshed straight from the binary — see the block under
[SKILL.md](../SKILL.md)'s `ape aboard status` paragraph.
