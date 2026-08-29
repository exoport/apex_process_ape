# Two sessions, one project

The default is that sessions **share** a board: one server, one
`.aboard/aboard.json`, one browser tab, both sessions reading and writing. That is
usually what you want — the user sees one board regardless of which session drew
on it.

Three mechanisms keep that from turning into a mess.

## 1. One server per project, and it is not yours to restart

The port is derived from the discovered project root — the nearest ancestor
directory holding `.aboard/` — so a checkout always gets the same port and two
checkouts never collide. Whichever session starts the server, the other finds it.

```sh
ape aboard status          # running? where? whose?
ape aboard serve           # run the server for this project
```

**Run `ape aboard status` before you ever run `ape aboard serve`.** If it reports a board
already running, that is success, not an obstacle — use the URL it prints. `serve`
enforces this rather than trusting you to: a second `ape aboard serve` for the same
project exits non-zero with `this project's board is already running at <url>
(pid N)` instead of taking the port. That refusal is about the BOARD, not the
port: `--port`/`PORT` naming a completely free port is refused just the same,
because `serve` asks this project's instance record — and the process it names,
over `/health` — before it binds anything. A record nothing answers is a killed
board and is overwritten, so a crash does not lock you out.

```
aboard running at http://localhost:41837
  project /home/you/proj
  state   /home/you/proj/.aboard/aboard.json
  pid     577548
  since   2026-08-25T23:50:36Z
  caps    9facfc76   (skill reference current)
```

Killing it and starting your own takes the first session's server out from under
it, and its browser tab with it. Restart only after changing Go code or the
embedded web tree, and prefer saying so, since it briefly drops the other
session's browser connection (the page reconnects on its own — `retry: 1000` on
the SSE stream — but it is still a visible blip).

While iterating on the web tree, `ape aboard serve --dev` serves it from disk so no
rebuild is needed at all.

## 2. Never write the state file directly

This is the one that actually loses work. Direct `Edit`/`Write` has no
compare-and-set. Two sessions reading the same snapshot and both writing means the
second silently erases the first — measured, not hypothetical:

```
session A node present: false
session B node present: true
-> A was silently lost: true
```

Through the server, the same race is refused instead:

```
A applies  -> applied
B applies  -> refused: the board changed since you read it — re-read, redo, apply again
B retries  -> applied; both changes present
```

So always:

```sh
ape aboard apply --by "agent-1" < /tmp/next-aboard.json
```

A `409` is not an error to route around. It means a real change landed while you
were thinking. Re-read `.aboard/aboard.json`, redo your edit on the fresh copy,
apply again.

This holds for writes that arrive at the same instant, not just for one that is
late: the server serialises the whole read-compare-write span, so of several
simultaneous writes off one base exactly one is applied and the others are
refused. A refused write reaches neither the board nor the journal, so a `409` is
also the guarantee that nothing of yours landed halfway.

## 3. Say who you are

`--by` lands in `lastEditedBy` and on every tab you touched. Use `agent-1`,
`agent-2`, or `agent-<role>` — never `claude`, which reads as one participant
when several may be writing:

```sh
ape aboard apply --by "agent-1" < /tmp/next-aboard.json
```

Why it matters: it is what the browser's change banner shows ("agent-1 changed
this tab") and the only way the other session (or the user, later) can tell which
of two agents moved something. `lastEditedBy: "human"` means there are user edits
you have not read yet.

`--by human` is refused from the CLI, and a write that names no actor at all is
recorded as `unknown` with agent-level powers rather than being trusted as the
human. The human writes from the browser; nothing an agent runs should be able to
sign their name.

## Discovery

`.aboard/run/instance.json` is the source of truth for "what is running here":

```json
{
  "app": "aboard",
  "host": "aboard",
  "argv0": "aboard",
  "version": "1.2.0",
  "built": "2026-08-25T19:58:27-03:00",
  "project": "/home/you/proj",
  "port": 41837,
  "url": "http://localhost:41837",
  "state": "/home/you/proj/.aboard/aboard.json",
  "pid": 577548,
  "started": "2026-08-25T23:50:36Z"
}
```

Paths are absolute, and `url` carries the `--base-path` prefix when there is one
— so probe `<url>/health`, never `:<port>/health`. A board served under a prefix
answers nowhere else, and probing the bare root reports a live board as a stale
record, which is the one sentence that sends a session off to restart a healthy
server.

Read it rather than assuming a port. `GET /health` returns the same record, which
is how the binary tells its own board apart from an unrelated process squatting on
the port. A record whose `pid` is dead is stale.

`app` also says which binary is serving: `aboard` for the standalone binary, and a
host-qualified name when the board is mounted inside another CLI. Either is a real
board; both answer the same routes.

Named boards get their own record: `.aboard/run/instance.<name>.json`.

`base` is the URL prefix the board is served under — from `serve --base-path
/prefix` — and it is **absent when there is none**, which is the normal case.
`GET /health` carries the same field. Anything that builds its own request URLs
(an editor extension, a proxy config, a script) has to prepend it; anything that
just follows `url` already has it.

## A third viewer: something that EMBEDS the board

A host can frame the shell and provide its own navigation — an editor extension
with a sidebar tree is the case this was built for. Three things make that work,
and all three are per-VIEWER, so none of them touches `.aboard/aboard.json`:

```
http://localhost:<port>/?chrome=notabs#tab=ab13
```

- **`?chrome=`** — `full` (default) · `notabs` (hide the tab button list, keep the
  topbar, the `+` and the tab note) · `none` (hide the whole head). An
  unrecognised value is `full`. It has to be a URL parameter: the frame is
  cross-origin, so a host can neither inject CSS nor reach the DOM, and two
  viewers of one board must be able to disagree about chrome while agreeing about
  content.
- **`#tab=<id>`** — a fragment change navigates without reloading the page, so
  whatever the human had panned, scrolled or half-typed survives.
- **`{__aboard: 'active', tab: '<id>'}`** — posted to `parent` whenever the active
  tab changes, including the tab the board picks at load and the ones `[`, `]` and
  `1`–`9` reach. A repaint that lands on the same tab says nothing, so a host may
  act on every message. Authenticate it by `event.source`; the board posts with
  `'*'` because a webview's origin is not knowable in advance. Nothing else travels
  this way — the embedder reads `/aboard.json` and `/events` like every other
  client.

Two things an embedder must get right, both of which fail SILENTLY:

- **Send `__by: "human"` on every write.** An absent actor is recorded as
  `unknown` with agent-level powers, so dismissing a marker or answering a removal
  request comes back `200` and does nothing.
- **Read `base` from the instance record or `/health`** before building any URL,
  or a board behind `--base-path` is invisible to you.

If you are the agent working alongside such a host, nothing changes: the board is
still one shared document, and the human's clicks arrive as writes by `human`
whether they came from a browser tab or a panel.

## When sessions should NOT share

A side investigation that must not disturb the main board:

```sh
ape aboard serve --name review
```

That gives a different derived port, `.aboard/aboard.review.json` as its state
file, and its own instance record. The two boards' DOCUMENTS cannot interfere.
Every other command takes the same `--name` (or reads `ABOARD_NAME` from the
environment), so a session working on the named board exports it once instead of
repeating it. Tell the user which URL belongs to which, since they now have two
tabs to keep straight.

Use this sparingly. A shared board is the feature; two boards is a fallback for
when the work genuinely does not overlap.

### What a name qualifies, and the two things it does not

A named board owns everything it writes for **itself**:

| the default board | the board named `review` |
|---|---|
| `aboard.json` | `aboard.review.json` |
| `run/instance.json` | `run/instance.review.json` |
| `run/journal.jsonl` | `run/journal.review.jsonl` |
| `run/rendered.json` | `run/rendered.review.json` |
| `run/logs/<tab>.log` | `run/logs/review/<tab>.log` |

All five are per board for one reason: **tab ids are allocated per BOARD.** Both
documents start at `ab1`, and each of those records is keyed by tab id — so one
shared file held two different tabs under one key. `ape aboard journal` could not say
which board an entry belonged to without reading the tab's name, and
`ape aboard history ab1 --name review` would hand you the DEFAULT board's version of
`ab1` as a document to restore. That is fixed; `journal`, `history`, `watch`,
`rendered` and `log` on a named board are about that board and nothing else.

Two paths stay per PROJECT, deliberately, and neither is keyed by tab id:

| shared | why |
|---|---|
| `uploads/` | an image is content the human pasted; either board may show it |
| `recipes/` | a recipe is a document about the project, not about one board |

So **`ape aboard uploads` reads every board's document**, prints a named board's tab
as `review:ab1`, and `--name` does not narrow it. It used to scan one board's
tabs, which made another board's image look unreferenced and let
`--prune --yes` delete a picture somebody was looking at.

Journal entries written before the split are not migrated and cannot be: the
record does not say which document a write went to. They stay readable and count
as the default board's.

### Which boards are up, anywhere on this machine

`ape aboard status` answers for the project you are in. `ape aboard boards` answers for
the machine:

```sh
ape aboard boards                        # one row per (project, name), full paths
ape aboard boards --output-format json   # the same, for a script
```

It reads the PROCESS TABLE — `/proc`, for an `ape aboard serve` or an
`ape aboard serve` — and then verifies each one exactly as `status` does: the
project's instance record, then `GET /health`. There is no registry file, so
nothing goes stale and nothing needs cleaning up after a crash. A record whose
process has gone is shown as "recorded but not answering" rather than dropped,
and the count of processes it managed to inspect is printed with the result —
"no board found" after 3 processes and after 400 are different answers.

Use it when two sessions may already be running, when a URL is open and you have
lost track of which project it belongs to, and before starting a board in a
project a colleague may already have one in. **`/proc` is Linux only**: elsewhere
the command exits 2 and names `ape aboard status` per project as the alternative.

## Driving the board yourself, in a browser

There is a second kind of session worth knowing about: an agent with a browser,
clicking around the real board to find what nobody thought to test. A DevTools
MCP server (`chrome-devtools-mcp`, attached to a running board with
`--browser-url`) gives click, drag, fill, screenshot, console and network;
Playwright MCP gives accessibility-tree snapshots and click-by-ref instead. Either
is a good way to hunt for the defect that only appears when you actually use the
thing.

It is a COMPLEMENT, not a gate, and the difference matters: agent exploration
repeats itself — Slack's 200-run study found about a fifth of runs reproducing the
same action sequence — so it is expensive per new finding and cannot be the thing
that says a change is safe. The pattern is **explore once, codify forever**:
whatever you find becomes a test in `test/e2e/`, which runs in a second and runs
the same way tomorrow. If you are exploring, do it against a scratch board
(`ape aboard init --example --gitignore` somewhere under `/tmp`), never the one the
human is reading.

## Etiquette summary

- Read before you write, always. The board may have moved.
- Report what the user changed before acting on it, so they know you read it.
- Do not restart a healthy server.
- Do not clear someone else's marks, notes, or nodes unless asked. Additive edits
  are cheap; destructive ones need a reason.
- If you replace `nodes` or `form.fields` wholesale, say so — you may be
  discarding another session's work or the user's answers.
- `ape aboard journal --limit 20` and a `trace` tab are how you tell who did what.
  `lastEditedBy` only ever names the last one.
- Give a non-obvious write a `--label`: it is the one line on a journal entry that
  says WHY, and with several sessions writing, "who" and "which tabs" is rarely
  enough to reconstruct an afternoon. It is navigation inside a local, rotating
  file — do not cite one anywhere permanent, and do not let it stand in for
  telling the human.
