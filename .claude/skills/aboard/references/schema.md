# .aboard/aboard.json

```jsonc
{
  "version": 1,                          // server-managed; the schema the renderers read
  "rev": 41,                             // server-managed; apply uses it as the CAS base
  "updatedAt": "2026-08-22T11:03:09Z",   // server-managed; when, for a human reading this
  "lastEditedBy": "agent-1",             // "human", or whatever --by was passed
  "nextId": 147,                         // server-managed; the id allocator
  "tabs": [ /* … */ ]
}
```

Never set `version`, `rev`, `updatedAt`, `lastEditedBy` or `nextId` yourself — the
server manages all five. Leaving `rev` exactly as you read it is what makes the
conflict check work; a document with no `rev` has no base, and `apply` refuses it
rather than clobbering whatever landed since. `ape aboard apply` replaces the whole
document, so always build from a fresh `Read`.

`rev` is a counter, one per accepted write. `updatedAt` used to be the base and is
not any more: it is a millisecond clock, so two writes inside one millisecond
share a string and a stale base passes — measured, and it cost a human edit.

`version` is in that list because it was NOT, and it cost a whole board: this file
showed `"version": 2` long after the board moved to 3, an agent copied the example
it was reading, and `apply` wrote it through with `applied` and exit 0. The
browser refuses to render a version it does not know, so the human got a blank
board and a pink banner one round trip after being told it was ready. The server
now stamps the field and `apply` warns when a document names the wrong one — but
the reason it is documented here rather than only fixed in code is that a
hand-written `version` is never right for longer than one schema change.

A second, isolated board lives in `.aboard/aboard.<name>.json` with the same
shape; `--name` (env `ABOARD_NAME`) selects it.

## Ids

**Ids are board-wide monotonic, tagged `ab`, and never reused.** Allocate one by
taking `doc.nextId`, using it, and incrementing:

```js
const id = 'ab' + doc.nextId; doc.nextId += 1;
```

Why it matters: renderers used to allocate "highest suffix in this container + 1".
Delete every mark on an image and the next one was `r1` again; delete the last
kanban node and the new one took its id. Any instruction that referenced the old
object then silently pointed at a different one. Since ids are how you and the
user refer to things across turns, that is a correctness bug.

Two consequences of a single counter:

- **An id is unique board-wide, so it never needs qualifying by tab.** This is
  the only thing that works inside a `stack` tab holding two kanbans or two
  images, where the tab cannot disambiguate at all.
- **One namespace tag, no type prefix.** `ab` (as in aboard) is there so an
  id survives being written in prose: `ab147` is unmistakably a board object
  where `147` is any number at all. It says nothing about kind, so it cannot be
  guessed wrong. A per-kind vocabulary (`node-7`, `tab-3`) stays rejected — a
  closed set in a system where you can invent new kinds of object (an `html`
  tab's `data`, a renderer that does not exist yet) gets guessed ad hoc and stops
  meaning anything, and the kind is already implied by where the object sits.
  Ids are strings so DOM attributes and Map keys agree with the document.

Bare (`147`) and legacy (`n7`, `t3`) ids are still recognised wherever they
survive: every parser uses `/^[a-z]*(\d+)$/`. Only writes carry the tag, and the
`ab` migration prefixed ids without renumbering them, so `49` and `ab49` are the
same object.

The server enforces the invariant: `nextId` never decreases, and is always above
every numeric id present — so a hand-edited document or one written before the
counter existed still allocates safely.

**Form FIELD ids are the deliberate exception.** Those are semantic keys you
choose (`strategy`, `window`) and read answers back by — do not generate them.
The form itself does carry a generated `id`, so a form can be referenced
independently of whatever tab or stack block holds it.

## A tab

```jsonc
{
  "id": "ab3",                // "ab<number>"; new ids continue from doc.nextId
  "key": "architecture",      // OPTIONAL stable handle — find by this to update
                              // the same tab next turn instead of opening another
  "name": "Architecture",     // what the user sees; you choose it
  "type": "diagram",          // picks the renderer
  "state": { /* type-specific, see below */ },

  "note": "why this exists",  // OPTIONAL: what the tab is FOR — YOUR brief statement
                              // of its purpose, which the human may edit. READ THIS
                              // FIRST; it carries intent the contents cannot. Write
                              // one when you create a tab. They edit it from the
                              // strip, the tab's right-click menu, or the New tab
                              // dialog.

  "requests": [               // the human's notes TO you about this tab. Theirs to
                              // write; yours only to stamp. `ape aboard requests` lists
    { "id": "ab199",          // them and `ape aboard requests done <id>` answers one.
      "at": "2026-08-26T09:12:00Z",
      "by": "human",
      "text": "the arrow points the wrong way",
      "done": { "by": "agent-1", "at": "…", "note": "flipped it" } }
  ],

  "stateFrom": "ab1",         // OPTIONAL: render another tab's state with this
                              // type — a kanban and a DAG over one dataset

  "touched": {                // set BY THE SERVER when an agent changed the tab
    "by": "agent-1", "at": "…", "note": "optional"
  },
  "pendingRemoval": {         // a removal REQUEST awaiting the user
    "by": "agent-1", "at": "…", "reason": "why this should go"
  }
}
```

Three fields you do not control:

- **`touched`** is stamped by the server on any tab an agent's write changed, and
  cleared only by the user dismissing it. Trying to remove it in a write is
  ignored — the previous marker is carried forward. It drives the dot on the tab.
- **`pendingRemoval`** is how a tab goes away. An agent write that simply drops a
  tab has it restored with this set. You may set it deliberately, with a reason.
  Only the user's answer deletes or keeps.
- **`requests`** are the user's notes to you, and the one thing on a tab that
  flows their way. You may only ADD a `done` stamp to one that already exists;
  creating, editing, reordering, deleting or un-stamping is refused and the list
  restored, exactly as `touched` is — including the ordinary case where you read
  the document, edit something else and hand it back without the field. The `by`
  on a stamp is rewritten to whoever actually wrote it. Reach for the commands
  rather than editing the array: `ape aboard requests`, then
  `ape aboard requests done <id> --by agent-1 --note "what you did"`.

## state, per type

### dag / kanban

```jsonc
{ "columns": ["todo", "doing", "done"],  // ANY names, any count — the agent's call
  "height": "72vh",                      // optional: any CSS length, or a number as px
  "density": 130,                        // dag only, optional: node spacing
  "readOnly": true,                      // kanban only, optional: agent-owned, human reads
  "nodes": [
    { "id": "ab7", "title": "Short label", "parent": "ab5", "status": "doing",
      "order": 3, "note": "Longer text — also where the user writes to you",
      "pos": { "x": 120, "y": 208 } }   // only if the user dragged it; delete to re-tidy
  ] }
```

`readOnly` (kanban) removes every editing affordance rather than disabling it —
no drag, no inline rename, no reorder, reparent, add or delete — and shows a
`read-only` badge saying an agent maintains the board. Reach for it when the
board reports state you own; leave it off when you want the arrangement argued
with. It shapes the interface, it does not enforce anything.

`parent` and `status` are independent axes: kanban groups by `status`, the DAG
reads `parent`. Moving a card between columns never changes the tree. Two tabs —
one `dag`, one `kanban` with `stateFrom` pointing at it — give both readings of
one dataset. Do not create a `parent` cycle.

### table

```jsonc
{ "addLabel": "Add finding", "readOnly": false,
  "columns": [ { "id": "file", "label": "File", "type": "text", "width": "22ch", "hint": "path" },
               { "id": "risk", "label": "Risk", "type": "select", "options": ["low", "high"] },
               { "id": "cost", "label": "Cost", "type": "number" },
               { "id": "ok", "label": "Done", "type": "checkbox" },
               { "id": "why", "label": "Why", "type": "longtext" } ],
  "rows": [ { "id": "ab51", "file": "main.go", "risk": "high", "cost": 3, "ok": false, "why": "…" } ] }
```

Cell types: `text`, `number`, `select`, `checkbox`, `longtext`. Sort order and
column width are per-viewer and never written. `readOnly` as on the kanban.

### gate

```jsonc
{ "pending": [ { "id": "ab129", "title": "…", "risk": "high", "detail": "…",
                 "command": "…", "by": "agent-1" } ],
  "decided": [ { "id": "ab129", "title": "…", "verdict": "allow|deny|edit",
                 "reason": "…", "editedTo": "…", "at": "…", "by": "human" } ] }
```

Nothing executes here — a verdict is a record. Block on it with
`ape aboard wait --for "answer <tabId>"`, or a stale queue lets the human believe
they gated something that already ran.

### log

```jsonc
{ "source": "ab126", "tail": 400, "follow": true, "height": "46vh" }
```

The lines live in a sidecar file (`.aboard/run/logs/<tab>.log`), NOT in this
state: `<cmd> 2>&1 | ape aboard log ab126`.

### trace

```jsonc
{ "limit": 200, "height": "44vh" }
```

Reads `.aboard/run/journal.jsonl` rather than the state document, so the history
is not something an agent can quietly rewrite. Each entry also carries `label`
(why, from `ape aboard apply --label`) and `warnings` (what the write-time checks said
about that write, keyed by tab). Both belong to the WRITE, so neither is ever in a
tab's `state`.

### vote

```jsonc
{ "question": "…", "scale": 5, "closed": false,
  "options": [ { "id": "dual", "label": "Dual write", "note": "…" } ],
  "ballots": { "agent-1": { "dual": 4 }, "human": {} } }
```

Write your own key in `ballots`; the human's column is the editable one.

### ui

```jsonc
{ "data": { "count": 18 },
  "root": { "type": "col", "children": [
    { "type": "card", "title": "Today", "accent": true, "children": [
      { "type": "stat", "value": { "bind": "count" }, "label": "shipped", "tone": "accent" },
      { "type": "button", "id": "again", "label": "Run it again", "intent": "re-run the suite" } ] } ] } }
```

Catalog (25): `col`, `row`, `grid`, `card`, `tabs`, `title`, `heading`, `text`,
`caption`, `badge`, `notice`, `quote`, `code`, `divider`, `spacer`, `list`,
`checklist`, `kv`, `table`, `stat`, `meter`, `image`, `link`, `button`, `field`.
`tone` is a token NAME (`accent`, `mark`, `agent`, `focus`, `danger`, `muted`,
`dim`), never a hex. `field` writes into `state.data`; `button` appends to
`state.intents` and executes nothing. An unknown `type` renders a visible marker;
an unknown PROP renders nothing at all — so run `ape aboard capabilities ui` for each
component's props rather than guessing, and read `ape aboard apply`'s stderr, which
walks the tree and names what it could not resolve.

### diagram

```jsonc
{ "source": "graph TD\n    A[\"x\"] --> B[\"y\"]" }
```

23 mermaid types render: `flowchart`/`graph`, `sequenceDiagram`, `classDiagram`,
`stateDiagram-v2`, `erDiagram`, `requirementDiagram`, `C4Context`, `journey`,
`gantt`, `timeline`, `kanban`, `pie`, `xychart-beta`, `quadrantChart`,
`radar-beta`, `sankey-beta`, `treemap-beta`, `mindmap`, `gitGraph`, `block-beta`,
`packet-beta`, `architecture-beta`. Only `zenuml` is missing.

Colours come from the board tokens automatically. For specific nodes use the
`classDef` block the "Add colours" button inserts; prefer the ring form (dark
fill, coloured border) over solid fills. In `requirementDiagram`, quote any
`text:` containing punctuation or the parser eats the next line.

### form

```jsonc
{ "id": "ab46",                            // the form's own id (generated)
  "title": "Cutover", "intro": "Answer and I will act.",
  "fields": [                            // field ids stay semantic, not generated
    { "id": "strategy", "type": "select", "label": "Strategy",
      "options": ["big bang", "dual write"], "value": "dual write" },
    { "id": "window", "type": "range", "label": "Downtime (min)",
      "min": 0, "max": 60, "step": 5, "value": 10, "hint": "optional clarifier" },
    { "id": "keep", "type": "checkbox", "label": "Keep v1 readable", "value": true },
    { "id": "name", "type": "text", "label": "Call it what?", "value": "", "placeholder": "…" },
    { "id": "notes", "type": "textarea", "label": "Anything else", "value": "" }
  ] }
```

Keep `id`s stable when you only reword, so answers survive. A default value may
mean "not answered yet" — if it matters, ask.

### markup

```jsonc
{ "layout": "side-by-side",              // or "stacked"
  "images": [
    { "id": "ab1", "src": "uploads/before.png", "caption": "Before",
      "annotatable": true,
      "regions": [ { "id": "ab2", "x": 0.472, "y": 0.271, "w": 0.235, "h": 0.186,
                     "note": "this needs a different scale", "color": "mark",
                     "shape": "ellipse" } ],   // absent or "rect" = rectangle
      "strokes": [ { "id": "ab3", "points": "0.101,0.427 0.095,0.423", "note": "" } ] },
    { "id": "ab4", "src": "uploads/after.png", "caption": "After", "annotatable": false }
  ] }
```

- **Coordinates are normalized 0..1** against each image's own box, never pixels.
  Multiply by the image's real dimensions to say what was circled, and describe it
  back in words so the user knows you understood.
- `annotatable: false` makes an image a reference with no overlay — useful for a
  before/after pair where only one side is marked.
- `strokes[].points` is one space-separated `"x,y x,y"` string. Keep the compact
  form; nested arrays bloat the file enormously.
- `color` is a **token name** (`mark`, `accent`, `focus`, `agent`, `danger`), not
  a hex, so it survives a retheme. Absent means the default. The accepted palette
  is declared, and `ape aboard apply` warns — naming the real ones — when a write uses
  a colour the board does not have.
- Images the human pastes or drops land in `.aboard/uploads/`, served from
  `/uploads/<file>`.
- The single-image shape (`image`, `caption`, `regions`, `strokes` at the top
  level) is still read and migrated on load.

The human's tools are region, ellipse, pen, **move** (drag a mark) and **resize**
(handles on a selected rect/ellipse). Marks can be hidden per image. Bulk recolour
lives behind the marks-table colour header and asks for confirmation; so does
clearing marks. The toolbar swatch sets the colour of *new* marks only.

### chat

```jsonc
{ "height": "62vh",                      // optional, as above
  "messages": [
    { "id": "ab5", "at": "2026-08-22T09:14:00Z", "by": "agent-1",
      "text": "Taking the schema work." },
    { "id": "ab6", "at": "…", "by": "human", "text": "Do the migration first." }
] }
```

Append; do not rewrite or delete others' messages. `by` distinguishes speakers —
use `agent-1`, `agent-2`, `agent-<role>` so several agents read as distinct
actors. Avoid `claude`: it reads as one participant when there may be many. Read
messages with `by: "human"` as directed at you.

### notes

```jsonc
{ "text": "Free-form text.", "markdown": false }
```

`markdown: true` renders it with a Read/Edit toggle.

### html

```jsonc
{ "html": "<h3>Rank these</h3><ul id=\"l\"></ul><script>…</script>",
  "data": { "order": ["schema drift", "auth rewrite"] },
  "height": "62vh" }                     // optional; otherwise aboard.fit() sizes it
```

Served from `/tab/<id>/html` into an iframe with `sandbox="allow-scripts"` and a
CSP of `connect-src 'none'` — so it can do anything local (canvas, SVG, WebGL,
animation) and **nothing over the network**. It inherits the board's palette as
CSS custom properties; override freely.

**Do not call `alert`, `confirm` or `prompt` in a widget.** That sandbox has no
`allow-modals`, so all three are suppressed: `confirm()` returns `false`,
`prompt()` returns `null`, nothing is drawn and nothing is logged. A widget that
asks before doing something therefore never asks and never does it, silently.
Draw your own `<dialog>`, which the sandbox does not touch. The board's own
shell hit exactly this inside a VS Code panel; the fix is `views/dialog.js`.

Inside the frame a bridge is available:

```js
aboard.get()            // the persisted data object
aboard.set(next)        // persist it — the parent writes state.data
aboard.onData(fn)       // called when it changed elsewhere
aboard.fit()            // ask the parent to resize the frame to the content
```

So an interactive widget round-trips its state into the board document like any
other tab. Write ordinary HTML — no build step, no framework, no imports.

The initial data is injected as `window.__ABOARD_DATA__`, and the frame talks to
the parent over a `__aboard`-tagged postMessage envelope. Neither is something a
widget should touch directly; use the four calls above.

### stack

```jsonc
{ "blocks": [
    { "id": "ab61", "type": "dag",    "title": "Dependencies", "state": { /* dag state */ } },
    { "id": "ab62", "type": "form",   "title": "Decide",       "state": { /* form state */ } },
    { "id": "ab63", "type": "markup", "title": "On screen",    "state": { /* markup state */ } }
] }
```

Each block is a full renderer with its own state, rendered top to bottom and
collapsible. Any type except `stack` — nesting is capped at one level. This is
usually the right answer when you want "look at this, then decide that".

An `html` block is served from `/tab/<tab>/<block>/html` and its `aboard.set()`
lands in that block's own `state.data`.
