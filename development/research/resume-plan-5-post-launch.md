# Continuation Prompt — resume PLAN-5 post-launch testing

Use this prompt to pick up after a `/clear`. PLAN-5 is **done**;
this doc orients on the sandbox testing setup and the small set of
edges that remain.

This repo is `/home/diegos/_dev/github/diegosz/apex_process_ape`.

---

## State, in one paragraph

PLAN-5 (`ape chat` + `ape pipeline --web` via MCP bridge) shipped
across 13 commits on `main` ending at `89c525e plan: PLAN-5 → done`,
then 10 follow-up commits (`7a48fc8` through `3d0887c`) closed the
UI / wiring gaps that live testing surfaced. The current `main`
tip is `3d0887c`. `ape pipeline <name>` now defaults to the web
UI; `--tui` and `--print` are opt-in. All-tests-pass; the sandbox at
`/home/diegos/_dev/ape-web-sandbox/greeter` has been exercised end
to end (design pipeline, 6 stages, 6 commits on the project's git).

---

## Reading order (skim, don't re-litigate)

1. **`development/planning/plan-5_ape-chat-and-pipeline-web.md`** —
   the plan. Read the **Implementation appendix** at the bottom; it
   summarises the post-launch fix batch with one row per commit.
2. **`development/research/resume-plan-5-implementation.md`** — the
   original resume doc from the first implementation pass. Most of
   it is now historical context, but the "Decisions already locked"
   table is still authoritative.
3. **`CHANGELOG.md` Unreleased section** — user-facing summary,
   including the **breaking UX change** (web is now the default
   for `ape pipeline <name>`).
4. **`docs/explanation/bridge-architecture.md`** — design narrative.
5. **`docs/reference/bridge-{ipc,security}.md`** — wire schema +
   threat model.

---

## Sandbox

The PLAN-5 test sandbox lives at:

```
/home/diegos/_dev/ape-web-sandbox/
├── .bin/ape                       ← prebuilt binary (rebuild after every code change)
└── greeter/                       ← project root, git-initialized
    ├── .claude/skills/apex-*      ← framework v0.0.78
    ├── .gitignore                 ← _output/ in
    ├── _apex/{config,framework,pipelines/}.yaml
    ├── CLAUDE.md
    └── development/planning/…     ← prd.md, ux-design.md, architecture.md appear here as pipeline runs
```

The sandbox's git history rewinds easily — `git reset --hard
3676580` reverts to "framework installed, fixture base, \_output/
ignored", the clean starting state for a fresh design-pipeline run.
Six `ape:design/<stage>/<skill>` commits land on top of that during
a successful run.

### Rebuild after touching ape source

```bash
cd /home/diegos/_dev/github/diegosz/apex_process_ape
go build -o /home/diegos/_dev/ape-web-sandbox/.bin/ape ./cmd/ape
```

The web assets (`internal/web/assets/*`, `internal/web/templates/*`)
are embedded via `//go:embed` so they cycle on every build. The
broker now serves `/assets/*` with `Cache-Control: no-store`, so
the browser refetches on every page load — **no hard-refresh is
required** between rebuilds.

### Run

```bash
cd /home/diegos/_dev/ape-web-sandbox/greeter
/home/diegos/_dev/ape-web-sandbox/.bin/ape pipeline design --open
```

`--open` runs `xdg-open` on the broker URL. Without it, the URL
prints on stderr (`web ui: http://127.0.0.1:<port>/`) and you open
the tab manually. `--tui` and `--print` are the explicit
opt-outs.

### Smoke-test matrix

```bash
ape pipeline design --open      # web (default)
ape pipeline design --tui       # Bubble Tea TUI (pre-PLAN-5 surface)
ape pipeline design --print     # plain stdout (eval / CI capture)
ape pipeline design --no-tui    # deprecated alias for --print (stderr warning)
ape pipeline design --web --tui # mutex error, exit 2

ape sessions                    # list live sessions, prune dead PIDs
ape sessions open greeter       # xdg-open the live session

ape chat --open                 # one bridged interactive claude session
ape costs                       # rollup since PLAN-5 / C7
ape costs roll                  # rebuild from on-disk artefacts
APE_DEBUG_ARGV=1 ape pipeline design  # surface full claude argv
```

---

## What to look at post-resume

The user has been driving live tests on the sandbox. After `/clear`
there's no specific to-do — you're resuming as on-call for whatever
the user tests next. The likely shape of incoming questions:

| Symptom                                          | First place to look                                                                                                    |
| ------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------- |
| Page stuck on "connecting…"                      | Browser DevTools → Network → `/api/events` should be 200, `text/event-stream`. Cache-Control: no-store on `/assets/*`. |
| Activity rows merge into prose                   | DevTools → Elements on `#hooks` — children should be `<div class="hook-row …">`. Bug was OOB carrier; see `47b1c03`.   |
| Stop button doesn't stop                         | `runCtx` vs `hubCtx`: `runCtx` is what pipeline.Run uses; `stopFn` must cancel it. See `a55ddb9`.                      |
| Silent `ape exit 1`                              | Cobra `SilenceErrors=true`; check that the failing path explicitly prints to stderr. See `de212ca`.                    |
| Cost rollup shows $0                             | `runWithWeb` runs `cost.RebuildRollup` on exit; per-step cost lands via PLAN-3's stream-json result event.             |
| Stage cards / banners don't update on completion | `pipeline-end` SSE event carries three OOB swaps: status, banner, activity row. 300 ms sleep before broker shutdown.   |

---

## Sharp edges (from the plan appendix)

These are known and intentional. Don't "fix" without a real signal:

- **Session-id discovery is mtime-based** in `cost.FindSessionJSONL`.
  Switch to `--session <id>` lookup if/when Claude Code documents
  a stable flag.
- **Live SSE cost ticker for in-flight stages is not wired.**
  Per-step cost lands after each step ends, via PLAN-3 result
  event. Real-time ticker is a future enhancement.
- **Backlog replay on reconnect is out.** Fresh `pipeline-init` +
  `connected` only; the durable record is JSONL.
- **Activity feed DOM is unbounded.** Browser is fine in practice
  for a 13-stage pipeline; rolling-window is a future tweak.

---

## Invariants — fail any of these and PLAN-5 regresses

Pulled forward from the original resume doc:

1. **SSE explicit `flusher.Flush()`** after every `Fprintf` in the
   broker. Locked by `TestBroker_SSEFlushOnEveryEvent`.
2. **`stdin io.Pipe` bootstrap** for `ape chat` — synthetic
   user-turn written after the bridge signals ready, with a 30 s
   timeout fallback.
3. **Inline `--mcp-config '<json>'`** with `--strict-mcp-config`.
   Never write `.mcp.json` to cwd or tmp.
4. **127.0.0.1 binding** for the broker HTTP listener AND the IPC
   TCP listener. `broker.Listen` rejects any other host.
5. **Hooks block injected only when `opts.Mode == ModeWeb`.** In
   `--tui` / `--print`, `BuildSettings` returns `{}`.
6. **`--print` mode byte-equivalence** with today's `--no-tui`
   output. Eval consumer (`apex_process_framework_eval`) depends
   on this.
7. **Pipeline run-artefact path unchanged.** `manifest.yaml`,
   `hook-events.jsonl`, `bridge-calls.jsonl`, `checkpoints.jsonl`,
   `transcripts/` all under
   `<project>/_output/pipelines/<name>/<run_id>/`. Do not move
   the directory.
8. **No `Co-Authored-By: Claude` trailer on any commit**
   (project memory `feedback_no_claude_attribution.md`).
9. **Run prettier on every markdown file you touch:**
   `npx prettier --write "<file>" --log-level silent`.

---

## Build + test before answering anything

```bash
cd /home/diegos/_dev/github/diegosz/apex_process_ape
git status --short              # should be clean on main
git log --oneline -5            # tip should be 3d0887c (or later)
go build ./...                  # should be clean
go test ./... -count=1 -timeout 60s   # should all pass
```

If any of those are red, fix them before stacking new work.

---

## When to stop and ask

- **Cost-table values.** `internal/cost/prices.go` is current as
  of 2026-05-17 per Anthropic's public price page. Any rate change
  needs explicit confirmation against
  `https://platform.claude.com/docs/en/docs/about-claude/pricing`
  before being committed.
- **Eval-consumer surface.** Any change touching
  `<project>/_output/pipelines/<name>/<run_id>/manifest.yaml`
  shape — even additive — needs a heads-up in
  `/home/diegos/_dev/exoar/apex_process_framework_eval` so its
  PLAN-9 consumer can adjust.
- **Cobra `SilenceErrors=true` paths.** Any new subcommand that
  returns an error from `RunE` must print its own `Error: <msg>`
  to stderr first, or use the explicit
  `os.Exit(exitCodePreflightFailed)` shape for typed errors.
  Otherwise the user sees `exit 1` with no explanation.

---

## Context references

| Path                                                       | What                                                      |
| ---------------------------------------------------------- | --------------------------------------------------------- |
| `development/planning/plan-5_ape-chat-and-pipeline-web.md` | Plan — read the Implementation appendix at the bottom     |
| `development/research/resume-plan-5-implementation.md`     | Original resume doc (decisions table still authoritative) |
| `development/research/claude-mcp-bridge.md`                | Bridge architecture + every contract                      |
| `development/research/ui-spike.md`                         | Frontend-stack verdict (locked)                           |
| `CHANGELOG.md` Unreleased section                          | User-facing changelog incl. breaking UX flip              |
| `docs/explanation/bridge-architecture.md`                  | Design narrative                                          |
| `docs/reference/bridge-ipc.md`                             | IPC wire schema                                           |
| `docs/reference/bridge-security.md`                        | Bind + threat model                                       |
| `docs/how-to/run-artefacts.md`                             | `_output/` layout reference                               |
| `/home/diegos/_dev/ape-web-sandbox/greeter/`               | Live sandbox (clean state: `git reset --hard 3676580`)    |
| `/home/diegos/_dev/exoar/apex_process_framework_eval/`     | Eval consumer (PLAN-9). Do not break its manifest reader. |
| Project memory: `feedback_no_claude_attribution.md`        | No `Co-Authored-By: Claude` trailer on commits            |
