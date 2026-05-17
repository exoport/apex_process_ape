# Continuation Prompt — implement PLAN-5

Use this prompt to resume work in a fresh session. PLAN-5 (`ape chat` +
`ape pipeline` web mode via MCP bridge) is **approved** and ready to
implement. The plan is the source of truth — read it first; everything
below is orientation, not re-spec.

This repo is `/home/diegos/_dev/github/diegosz/apex_process_ape`.

---

## What you're building, in one paragraph

The bridge that turns `ape pipeline` from a Bubble Tea TUI into a live
web UI. ape stays the orchestrator and continues to spawn one `claude`
invocation per pipeline step; the new code wires those steps through
two MCP tools (`await_message`, `reply`) for bidirectional comms with a
browser, plus passive hook observability via a new `ape notify`
subcommand. Default UX flips: web becomes the no-flag surface, TUI
moves to `--tui`, plain stdout moves to `--print`. A second new command
`ape chat` is a bridged interactive session with no pipeline. All
artefacts land under the project, extending PLAN-3's existing run dir
in place — no eval-consumer break.

---

## Read these first, in order

1. **`development/planning/plan-5_ape-chat-and-pipeline-web.md`** — the
   approved plan. Eight Cs (C1–C8) + C9 (tests/port-mechanics). Read in
   full; do not re-litigate decisions captured in the body or in
   Scope — OUT.
2. **`development/research/claude-mcp-bridge.md`** — the bridge design
   doc. Especially §5 (pipeline integration), §6 (frontend stack), §8
   (hooks via inline `--settings`), §9 (transcript capture + run-log
   layout), §10 (per-project ports), §11 (cost tracking).
3. **`development/research/ui-spike.md`** — verdict that locked the
   frontend stack. Don't re-evaluate. Reference variant lives at
   `/home/diegos/_dev/github/diegosz/claude_mcp_bridge_spike/variant-htmx/`.
4. **PoC source** at `/home/diegos/_dev/github/diegosz/claude_mcp_bridge_poc/`
   commit `4e542d0`. Three files port directly: `bridge.go` →
   `internal/bridge/mcp/`, `ipc.go` → `internal/bridge/ipc/`, `serve.go`
   → `internal/bridge/broker/`. `mock_claude.go` ports to testdata per
   C9. Three load-bearing bugfixes from that commit must carry over:
   SSE explicit `flusher.Flush()`, `stdin io.Pipe` bootstrap, inline
   `--mcp-config '<json>'`. Lose any of them and the bridge silently
   regresses.
5. **`development/planning/plan-3_pipeline-run-manifest.md`** — the
   manifest you're extending in place (NOT moving). v2 schema already
   has per-step `cost_usd` + `tokens_*` fields; PLAN-5 populates them
   from a new data path (per-message `usage` blocks in the session
   JSONL) without bumping the schema.
6. **`development/planning/plan-4_per-step-boundary-commits.md`** — the
   boundary-commit contract you'll co-emit with stage events. A stopped
   step (`POST /api/stop`) does NOT get a boundary commit; everything
   else does.
7. **`internal/pipeline/manifest.go`, `manifest_writer.go`,
   `result_event.go`** — current manifest plumbing. Per-step cost
   currently flows from `claude --output-format stream-json` (gated by
   `--print`); PLAN-5 adds the alternative session-JSONL tail path for
   web/TUI modes.
8. **`internal/apecmd/pipeline.go`** — current `ape pipeline` entrypoint.
   PLAN-5 adds C1's mode-flag dispatch; the per-step execution loop
   largely stays.
9. **`internal/tui/`** — confirm the TUI sits behind a clean boundary so
   flipping it from default-on to `--tui`-opt-in is a small change.

---

## Decisions already locked — do not re-litigate

The plan body has the full rationale; this is the index so you can spot
re-litigation drift. Every row below is in PLAN-5's "Scope — IN" or
"Scope — OUT".

| Topic                                     | Decision                                                                                                                         |
| ----------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------- |
| Orchestration model                       | ape stays the orchestrator. Per-step `claude` invocations. No in-Claude conductor skill.                                         |
| Web mode default                          | `ape pipeline <name>` defaults to web. `--tui` and `--print` are opt-in. **Breaking UX change**, flagged in CHANGELOG.           |
| MCP config delivery                       | Inline `--mcp-config '<json>'` + **`--strict-mcp-config`** always. Project `.mcp.json` is hidden from the bridged session.       |
| Settings delivery                         | Inline `--settings '<json>'`. **Hooks block injected only in web mode** (`BuildSettings(opts{Mode: ModeWeb})`).                  |
| `--ignore-project-settings` flag          | Translates to `--setting-sources user --settings <inline>`.                                                                      |
| Init mechanism                            | `--system-prompt` + stdin `io.Pipe` bootstrap (PoC pattern). Bootstrap text quoted verbatim in PLAN-5 C3.                        |
| `await_message` timeout                   | 240 s default. Not configurable from ape side.                                                                                   |
| Buffer policy for unsolicited `/api/send` | FIFO buffer of up to 5 messages. Overflow drops oldest + logs `buffer-overflow` to `bridge-calls.jsonl`.                         |
| Decision-gate UX                          | Explicit `await-pending` / `await-resolved` SSE events drive a `#decision-gate` form (disabled by default, OOB-swap enables).    |
| Web UI auth                               | Localhost-only (127.0.0.1 binding mandatory). No bearer token in v1. Documented threat model.                                    |
| Browser-close behaviour                   | Step keeps running. Reopen emits a fresh `pipeline-init`; **no backlog replay** (durable record is the JSONL streams).           |
| Cancel UX                                 | `POST /api/stop` → SIGTERM `claude` subprocess. Step marked `stopped`. Pipeline halts. Exit code 137. No boundary commit.        |
| Bridge presence detection                 | Env var `APE_BRIDGE_PORT` + `tools/list` MCP probe. Skills degrade to stdout when absent.                                        |
| Hooks observability                       | `ape notify` subcommand. `async: true` on every hook. Hook→step routing via in-memory `sessionID → step` table.                  |
| Hook event ordering                       | Arrival-order in `hook-events.jsonl`. UI sorts by `ts` within a 1 s window at render time.                                       |
| `PreToolUse` gating schema                | `hookSpecificOutput.permissionDecision: "deny"`. Wiring lands; **rule-set itself is OUT** of PLAN-5.                             |
| IPC transport                             | TCP + NDJSON parent ↔ bridge subprocess. NATS-embedded / stdlib WebSocket migration deferred.                                    |
| Multi-project                             | Random free-port allocation per session. `~/.ape/registry.json` tracks. `ape sessions` lists/prunes/opens.                       |
| Pipeline artefact path                    | **`<project>/_output/pipelines/<name>/<run_id>/`** — PLAN-3's path, extended in place. **Eval consumer does NOT break.**         |
| `ape chat` artefact path                  | **`<project>/_output/ape/chats/<chat-id>/`** — separate convention. `session.yaml` instead of PLAN-3 manifest.                   |
| Run-id collision                          | **Fail loud.** No auto-disambiguate, no overwrite.                                                                               |
| Per-step cost data source (web/TUI)       | Per-message `usage` block in `~/.claude/projects/<hash>/<sid>.jsonl`. Populates existing v2 manifest fields. **No schema bump.** |
| JSONL tail mechanism                      | Polling (200 ms cadence, `APE_COST_TAIL_INTERVAL_MS` undocumented tuning knob). Partial-line buffer. Drain on `stage-end`.       |
| Cost rollup                               | `<project>/_output/ape/cost-rollup.json`. `ape costs` CLI + `/dashboard` web route.                                              |
| Web frontend stack                        | HTMX 2.x + stdlib `html/template` + handwritten `styles.css`. Vendored under `internal/web/assets/vendor/`. No JS toolchain.     |
| PoC port mechanics                        | **Copy** with 1-line attribution header citing commit `4e542d0`. No vendor, no submodule. License notes per C9 if relevant.      |
| Tests                                     | Mock claude ported into `internal/bridge/mcp/testdata/mock_claude/`. Three test classes: unit, bridge integration, CLI smoke.    |

---

## Recommended implementation order

From PLAN-5's phasing note (the plan does not prescribe; this order
minimises rework):

1. **C2** — config builders (`BuildMCPConfig`, `BuildSettings`).
   Smallest unit, no upstream deps, easy to test in isolation. Land
   first so every subsequent C can call them.
2. **C3** — bridge runtime (port PoC into `internal/bridge/`). Without
   this, nothing else compiles end-to-end. Ship with bridge integration
   tests (C9 wiring) on day one — they catch the three load-bearing
   bugfixes in regression.
3. **C1** — CLI surface and mode-flag dispatch. Once `ape mcp-bridge` is
   dial-able, wire `ape chat` first (simpler — no pipeline loop), then
   `ape pipeline` web mode. **Hold the TUI default flip until last** —
   keep `--tui` as the default in `ape pipeline` while the web path is
   landing, then flip in the final release-cycle merge. Add the
   deprecated `--no-tui` alias at flip time.
4. **C4** — hooks observability. `ape notify` subcommand + inline hooks
   block via `BuildSettings`. Easy to test by mocking `APE_BRIDGE_PORT`.
5. **C5** — port registry + `ape sessions` subcommand. Independent of
   the bridge runtime; can land in parallel with C3.
6. **C6** — run artefacts. Extends PLAN-3's writer with the three new
   JSONL streams + the symlink. The chat-artefact path
   (`_output/ape/chats/<id>/`) is its own thing — keep its writer
   separate from the pipeline writer to avoid coupling.
7. **C8** — web UI. Last piece for the user-facing path. Template
   fragments map 1:1 to spike's variant-htmx — port liberally.
8. **C7** — cost tracking. Last because it consumes data the rest
   produce. The JSONL tail goroutine is the meatiest implementation
   surface; isolate it behind a clean interface so it's testable
   against fixture JSONLs.
9. **C9** — tests land incrementally with each C, not as a separate
   phase. The "mock claude in testdata" infrastructure should land
   alongside C3 so bridge integration tests exist from day one.

---

## Critical invariants — fail any of these and the bridge regresses

1. **SSE explicit `flusher.Flush()`** after every `Fprintf` in the
   broker. Lock with a regression test. Without it, dashboards freeze
   on long stage gaps.
2. **`stdin io.Pipe` bootstrap** for any session that needs the
   `await_message` loop (`ape chat`; pipeline steps that include a
   skill calling `await_message`). The synthetic user-turn text is
   `"Start the await_message loop. Call await_message() now.\n"`,
   written after `{"type":"ready"}` arrives over IPC (30 s fallback
   timeout per PoC `serve.go:172`).
3. **Inline `--mcp-config '<json>'`** with `--strict-mcp-config`. Never
   write `.mcp.json` to cwd or tmp.
4. **127.0.0.1 binding** for the broker HTTP listener AND the IPC TCP
   listener. Never `0.0.0.0`, never omitted.
5. **Hooks block injected only when `opts.Mode == ModeWeb`.** In
   `--tui` / `--print`, `BuildSettings` returns `{}` so no `ape notify`
   subprocess spawns per tool call.
6. **`--print` mode byte-equivalence** with today's `--no-tui` output.
   Lock with a CI smoke test before merging C1. Eval consumer
   (`apex_process_framework_eval`) depends on this.
7. **Pipeline run-artefact path unchanged.** New files (`hook-events.jsonl`,
   `bridge-calls.jsonl`, `checkpoints.jsonl`, `transcripts/`) land
   ALONGSIDE `manifest.yaml` at `<project>/_output/pipelines/<name>/<run_id>/`.
   Do not move the directory.
8. **No `Co-Authored-By: Claude` trailer on any commit** (project memory
   `feedback_no_claude_attribution.md`; same rule across this repo and
   `apex_process_framework_eval`).
9. **Run prettier on every markdown file you touch:**
   `npx prettier --write "<file>" --log-level silent`.

---

## Bootstrap content — verbatim from the PoC

**System prompt for `ape chat`** (`ape pipeline` steps use the standard
skill-invocation prompt instead):

```
You are connected to a Web UI. Call await_message() to receive a
message from the user. When it returns a non-empty string, process
it and call reply() with your response. If await_message() returns
an empty string, call it again. Begin by calling await_message() now.
```

**Synthetic user-turn (stdin bootstrap):**

```
Start the await_message loop. Call await_message() now.
```

Source: PoC `serve.go:140–149` (system prompt) and `serve.go:175`
(user-turn). Write the user-turn via `io.Pipe` to claude's stdin after
the bridge signals `{"type":"ready"}` over IPC, with a 30 s timeout
fallback.

---

## Pre-flight checks before you start coding

```bash
cd /home/diegos/_dev/github/diegosz/apex_process_ape
git status --short
go build ./...          # should be clean
go test ./...           # should pass
git log --oneline -5    # confirm you're on top of the right commit
```

`go build ./...` was clean as of 2026-05-17 (the day PLAN-5 was
approved). If it isn't now, fix the regression first — do not stack
new work on a broken tree.

---

## Repo state as of 2026-05-17 (the day PLAN-5 was approved)

`apex_process_ape` working tree (per `git status --short`):

```
 M development/planning/index.md                         # PLAN-5 row added
 M development/research/claude-mcp-bridge.md             # §6 spike redirect
?? development/planning/plan-5_ape-chat-and-pipeline-web.md
?? development/research/claude-channel-bridge.md         # pre-existing untracked
?? development/research/resume-mcp-bridge-plan.md
?? development/research/resume-post-spike.md
?? development/research/resume-ui-spike.md
?? development/research/ui-spike.md
?? development/research/resume-plan-5-implementation.md  # this file
```

None of the above is committed yet — decide before starting whether to
land them as the first commit(s) on this branch, or stage them
alongside the C2 work. Either is reasonable.

Sibling repos:

| Path                                                        | State                                                                                 |
| ----------------------------------------------------------- | ------------------------------------------------------------------------------------- |
| `/home/diegos/_dev/github/diegosz/claude_mcp_bridge_poc/`   | git repo, commit `4e542d0` is the port-source. Read-only reference.                   |
| `/home/diegos/_dev/github/diegosz/claude_mcp_bridge_spike/` | `git init`'d but no commits yet. UI spike reference. `make build` works.              |
| `/home/diegos/_dev/exoar/apex_process_framework_eval/`      | Eval consumer. Reads `_output/pipelines/<name>/<run_id>/manifest.yaml`. Do not break. |

---

## Doc conventions in this repo

- **CLAUDE.md** at repo root and under `docs/` carries project-level
  instructions. Always read them when entering the repo.
- **prettier** formats every markdown file. After editing any `.md`:
  `npx prettier --write "<file>" --log-level silent`.
- **Diátaxis** structure under `docs/` (`how-to/`, `reference/`,
  `tutorials/`, `explanation/`). Place new docs accordingly:
  - `docs/reference/bridge-ipc.md` — IPC wire schema (C3).
  - `docs/reference/bridge-security.md` — threat model (C3).
  - `docs/how-to/run-artefacts.md` — what `_output/` looks like (C6).
  - `docs/explanation/bridge-architecture.md` — port-target for
    `claude-mcp-bridge.md` content as it stabilises.
- **CHANGELOG** — call out the TUI default flip explicitly when C1
  merges (`feature` and `breaking-change` sections).

---

## When to stop and ask

- **Cost-table values.** PLAN-5 C7 says initial prices land with the
  implementation PR, not in the plan. Surface the table for review
  before merging — pricing is the kind of thing that's easy to
  fat-finger and embarrassing to ship wrong.
- **Design drift.** If you discover the bridge design needs revision
  during implementation, **stop and edit `claude-mcp-bridge.md`
  first**, then call out the revision at the top of the PR.
  Do not silently diverge from the design doc.
- **Eval-consumer surface.** Any change that touches the
  `<project>/_output/pipelines/<name>/<run_id>/manifest.yaml` shape —
  even additive — needs a heads-up in
  `apex_process_framework_eval` so its PLAN-9 consumer can adjust.

---

## Context references

| Path                                                                         | What                                                      |
| ---------------------------------------------------------------------------- | --------------------------------------------------------- |
| `development/planning/plan-5_ape-chat-and-pipeline-web.md`                   | Approved plan — source of truth                           |
| `development/research/claude-mcp-bridge.md`                                  | Bridge architecture + every contract                      |
| `development/research/ui-spike.md`                                           | Frontend-stack verdict (locked)                           |
| `/home/diegos/_dev/github/diegosz/claude_mcp_bridge_poc/` (commit `4e542d0`) | Validated PoC — port-source for `internal/bridge/`        |
| `/home/diegos/_dev/github/diegosz/claude_mcp_bridge_spike/variant-htmx/`     | Working reference for the locked web stack                |
| `/home/diegos/_dev/exoar/apex_process_framework_eval/`                       | Eval consumer (PLAN-9). Do not break its manifest reader. |
| Project memory: `feedback_no_claude_attribution.md`                          | No `Co-Authored-By: Claude` trailer on commits            |
| `https://code.claude.com/docs/en/hooks`                                      | Hooks reference (canonical event list + JSON schema)      |
| `https://code.claude.com/docs/en/settings`                                   | Settings precedence + `--setting-sources` semantics       |
| `https://docs.claude.com/en/docs/build-with-claude/prompt-caching`           | Prompt-cache pricing + TTL behaviour                      |
| `https://htmx.org/extensions/sse/`                                           | HTMX SSE extension docs                                   |
