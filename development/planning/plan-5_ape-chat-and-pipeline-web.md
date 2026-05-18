---
plan_id: PLAN-5
created_at: 2026-05-17
approved_at: 2026-05-17
completed_at: 2026-05-17
status: done
tags:
  - bridge
  - web-ui
  - hooks
  - cost-tracking
  - cli-surface
  - breaking-default
summary: Bring the Claude MCP bridge into ape as the new default UX. Introduces `ape chat` (one bridged interactive session) and flips `ape pipeline <name>` from "Bubble Tea TUI by default" to "web UI by default" — the TUI moves behind `--tui`, plain stdout moves behind `--print`. Each pipeline step is bridged via two MCP tools (`await_message`, `reply`) ported from the validated PoC at `/home/diegos/_dev/github/diegosz/claude_mcp_bridge_poc/` (commit `4e542d0`); ape stays the orchestrator and spawns one `claude` invocation per step as today. Hooks observability lands via a new `ape notify` subcommand wired through inline `--settings`, no on-disk `.claude/settings.json`. MCP config travels inline via `--mcp-config '<json>'` with `--strict-mcp-config` to keep server visibility deterministic. Per-project random TCP ports + `~/.ape/registry.json` keep cross-project sessions distinguishable; `ape sessions` inspects and prunes. Pipeline run artefacts stay at PLAN-3's path (`<project>/_output/pipelines/<name>/<run_id>/`) and are extended in place with `hook-events.jsonl` / `bridge-calls.jsonl` / `checkpoints.jsonl` / `transcripts/`. `ape chat` artefacts live under `<project>/_output/ape/chats/<id>/` (separate convention, ad-hoc sessions). Per-step cost data is read from the per-session JSONL at `~/.claude/projects/<hash>/<sid>.jsonl` and lands in the existing v2 manifest fields (no schema bump); `ape costs` surfaces them. Web frontend is locked: HTMX 2.x + stdlib `html/template` + handwritten `styles.css`, vendored under `internal/web/assets/vendor/` — no Templ, Tailwind, Alpine, or JS toolchain. Stack and SSE wire schema validated by the UI spike (`/home/diegos/_dev/github/diegosz/claude_mcp_bridge_spike/`).
origin:
  - 2026-05-13 → 2026-05-17 research arc captured in `development/research/claude-mcp-bridge.md` (architecture + every contract) and `development/research/claude-channel-bridge.md` (why-not-channels). MCP blocking-tool approach validated by `/home/diegos/_dev/github/diegosz/claude_mcp_bridge_poc/` at commit `4e542d0` — three production-critical fixes (SSE flush, stdin bootstrap for `await_message` loop, inline `--mcp-config` instead of an on-disk `.mcp.json`) landed in that commit and must carry over into the ape port.
  - The earlier "Phase 1 — in-Claude `apex-run-pipeline` conductor skill" framing is abandoned. Two reasons, both recorded in the design doc Context section: Claude Code's one-level sub-agent nesting limit blocks `apex-story-batch-dev` / `apex-story-batch-create` / `apex-lift-project` from running under an Agent-tool conductor; and Anthropic dropped the default prompt-cache TTL from 1 h to 5 min around March 2026, dissolving the shared-context benefit a single long-lived session would have given over per-step sessions. ape remains the orchestrator; per-step `claude` invocations remain the model.
  - 2026-05-17 UI-stack selection spike (`development/research/ui-spike.md`; code now at `/home/diegos/_dev/github/diegosz/claude_mcp_bridge_spike/`) redirected the design doc's previously-expected GOAT-hybrid (Templ + HTMX + Tailwind + Alpine) to htmx-only on bundle-weight, build-step, and diff-per-change evidence. The SSE event schema (`pipeline-init`, `stage-start`, `stage-update`, `stage-end`, `hook`, `reply`) is spike-validated and reused verbatim by this plan, extended with `await-pending` / `await-resolved` / `stopped` per C3.
  - PLAN-3 (per-step manifest) and PLAN-4 (boundary commits) supply the per-step infrastructure this plan attaches to. PLAN-3's v2 manifest already carries per-step `cost_usd` + `tokens_*` fields (`internal/pipeline/manifest.go:121–127`); today those are populated from `claude --output-format stream-json`'s terminal `result` event, which is gated by `--print` (per `claude --help`) and therefore unavailable in interactive bridge mode. PLAN-5 adds a second data-source path that reads per-message `usage` blocks from `~/.claude/projects/<hash>/<sid>.jsonl` and populates the same v2 fields — **no manifest schema bump**.
  - Sibling research docs `development/research/claude-channel-bridge.md`, `development/research/resume-mcp-bridge-plan.md`, `development/research/resume-post-spike.md`, and `development/research/resume-ui-spike.md` are untracked in the working tree as of 2026-05-17 (the bridge research arc was not yet committed when this plan was drafted). Treat their references as authoritative regardless of commit state.
---

# PLAN-5: `ape chat` + `ape pipeline` web mode (MCP bridge)

## Goal

Ship a working `ape chat` plus a web-bridged `ape pipeline` against `claude_mcp_bridge_poc@4e542d0`'s validated design. After this plan, the default `ape pipeline <name>` invocation opens a browser, streams every tool call as it happens, surfaces per-step cost, and accepts decision-gate replies from the user without leaving the page. `ape chat` is the same surface for a single non-pipeline session. The Bubble Tea TUI is still available (`--tui`) and plain stdout is still available (`--print`); they are no longer the default.

End state: a first-time user runs `ape pipeline design` and gets a browser tab showing 13 stage cards filling in live, a hook-tagged activity feed under each card, per-step cost figures, and an inline reply box that activates whenever a skill is blocked on `await_message`. Closing the browser does not kill the run; reopening it reconnects from current state (replay-from-disk is a follow-up). A Stop button on the page SIGTERMs the active `claude` subprocess for explicit cancellation. The eval harness sees no behavior change in `--print` mode — its NDJSON capture path stays intact, and the pipeline run-artefact path (`<project>/_output/pipelines/<name>/<run_id>/`) does not move.

## Why now

1. **The PoC closed every open architectural question.** Bidirectional MCP tool calls work on every plan with no special Claude Code flag; SSE survives long stage gaps with the explicit-flush fix; the stdin-bootstrap loop initiates reliably; inline `--mcp-config` removes the on-disk config dance. The remaining work is wiring, not invention.
2. **The framework's Commit Policy + PLAN-4 left a clean per-step boundary.** Every successful step already produces a deterministic ape-owned commit and a manifest record. Hanging the bridge events, the cost roll-up, and the run-log flush off the same boundary is mostly plumbing.
3. **The UI spike locked the frontend stack.** No remaining build-system or framework decision blocks the web surface; the reference implementation at `/home/diegos/_dev/github/diegosz/claude_mcp_bridge_spike/variant-htmx/` is a working starting point.
4. **The TUI default flip becomes affordable once the web UI is reasonable.** Today the TUI is the only interactive surface; flipping it to opt-in only works if the web UI is at parity-or-better for the common path. The spike + bridge design together cross that threshold.

Default-on web (with `--tui` and `--print` opt-in) is a deliberate breaking UX change. It is the simplest contract: the most useful surface becomes the no-flag surface, and the existing flags become explicit overrides. Mark this in the plan's `tags:` and call it out in CHANGELOG when the plan lands.

## Scope — IN

### C1: CLI surface and default flip

Three commands, four pipeline modes.

**`ape chat`** (new) — one bridged interactive `claude` session, no pipeline.

- Spawns `claude --strict-mcp-config --mcp-config <inline> --settings <inline> --system-prompt "<bootstrap>"`, then writes one synthetic user turn to stdin via `io.Pipe` (PoC bootstrap pattern, validated). Bootstrap-string and system-prompt content are quoted verbatim in C3.
- Web UI is the only surface. Print the URL to stdout; optionally `xdg-open` (off by default; opt-in via `--open`).
- No TUI mode. No `--print` mode. `ape chat` is web-only by definition.

**`ape pipeline <name>`** — defaults to **web** mode (was: TUI mode).

- `--tui` opts in to the existing Bubble Tea TUI (today's default). **Breaking UX change** — flag the deprecation in CHANGELOG and in `ape pipeline --help`.
- `--print` opts in to plain stdout (today's `--no-tui` behaviour).
- `--no-tui` becomes a deprecated alias for `--print`. Print a one-line stderr warning when it's used; remove after one minor version.
- Multiple mode flags is an error (`ape pipeline foo --tui --print` → exit 2 with a clear message).

**Global flag — `--ignore-project-settings`** (boolean, default off).

- Translates to `claude --setting-sources user --settings '<inline-ape-hooks>'`. Only user-global + ape hooks fire; project + local `.claude/settings*.json` are skipped.
- Lives on `ape pipeline` and `ape chat` (both spawn `claude`). Documents precedence: `--ignore-project-settings` only affects how the spawned `claude` loads project settings; it does not affect ape's own configuration loading.

**Exit codes.** Unchanged from today on the pipeline path (success = 0, step failure = non-zero with the step's exit code, ctrl-C = 130, browser-side Stop = 137 — distinguishable from natural step failure). `ape chat` exits 0 on browser-side `/exit`, 130 on ctrl-C, 137 on browser-side Stop.

### C2: Inline config plumbing — no files in cwd or tmp

Two builders in a new package `internal/bridge/config`:

- `BuildMCPConfig(opts) (json.RawMessage, error)` — produces the JSON passed to `--mcp-config`. Exactly one server entry pointing at `ape mcp-bridge`, with the IPC port in `env.APE_IPC_PORT`. Matches the PoC's working shape. The runner **always passes `--strict-mcp-config`** alongside `--mcp-config` so the spawned `claude` ignores any project `.mcp.json` or user-level MCP configuration — ape's bridge is the only MCP server visible to the session, which keeps skill behaviour deterministic across users. Project-MCP integration, if needed in the future, lands via `_apex/config.yaml` declaring servers that `BuildMCPConfig` merges into the inline blob.
- `BuildSettings(opts) (json.RawMessage, error)` — produces the JSON passed to `--settings`. Hooks block per §C4. **Hooks are injected only when `opts.Mode == ModeWeb`**; in `--tui` and `--print` modes the builder returns `{}` so no `ape notify` subprocess is spawned per tool call. The runner asserts: if `opts.Mode == ModeWeb`, `--settings` is non-empty; otherwise `--settings` is omitted from the argv (or passed as `{}` if Claude requires it for `--setting-sources`).

Both functions return `json.RawMessage`; the runner stringifies and passes via the flag. No `os.TempDir` writes, no `.mcp.json` in cwd, no `.claude/settings.json` rewrite. Inline JSON blobs are <1 KB each, well under `MAX_ARG_STRLEN` (128 KB on Linux) and any equivalent on macOS/Windows.

The runner asserts the built JSON parses through Claude Code's expected shape via a smoke test (`tests/integration/bridge_inline_config_test.go` runs `claude --help` and confirms `--mcp-config`, `--settings`, and `--strict-mcp-config` are present — regression-only).

Code path:

```
internal/bridge/
├── config/             # C2 builders (MCP + settings JSON)
│   ├── mcp.go
│   ├── settings.go
│   └── *_test.go
├── broker/             # SSE broker (ports serve.go from PoC)
├── ipc/                # TCP + NDJSON parent ↔ bridge (ports ipc.go from PoC)
└── mcp/                # `ape mcp-bridge` subcommand entry (ports bridge.go from PoC)
    └── testdata/
        └── mock_claude/  # ported from PoC mock_claude.go for integration tests (see C9)
```

### C3: Bridge runtime — port the PoC into `internal/bridge/`

The PoC at `/home/diegos/_dev/github/diegosz/claude_mcp_bridge_poc/` (commit `4e542d0`) is the port-source. Three sub-packages, each owning one PoC file's role:

- `internal/bridge/mcp/` ← PoC `bridge.go`. The `ape mcp-bridge` subcommand. MCP JSON-RPC 2.0 over stdio. Implements `tools/list`, `tools/call` for `await_message` and `reply`, plus `initialize` / `ping`. Deferred-response pattern for `await_message` (PoC validated): a pending-request slot holds the request `id`, the message loop continues, and an IPC "message" event (or a timeout-goroutine tick at 240 s) flushes the response.
- `internal/bridge/ipc/` ← PoC `ipc.go`. TCP NDJSON between parent (`ape chat` / `ape pipeline`) and the bridge subprocess. Frames listed under "IPC wire" below. Documented in `docs/reference/bridge-ipc.md` so the swap-out path to NATS / stdlib WebSocket lives behind a stable boundary.
- `internal/bridge/broker/` ← PoC `serve.go`. HTTP server hosting the endpoints listed under "HTTP surface" below. The SSE-flush fix from PoC commit `4e542d0` is non-negotiable: every published event calls `flusher.Flush()` explicitly; without it long stage gaps will silently buffer.

**Validated bugfixes from `4e542d0` that must carry over:**

1. **SSE flush on every event.** The PoC's `responseWriter` ignored the implicit chunk boundary for slow producers; explicit `flusher.Flush()` after every `Fprintf` is the fix. Without it the dashboard freezes when a step takes more than ~30 s between updates. Lock with a regression test that fails if `flusher.Flush()` is removed from the publish path.
2. **`stdin io.Pipe` bootstrap for `await_message` loop.** `--system-prompt` alone leaves Claude Code idling at the prompt without an initial user turn. The parent must write one synthetic user turn to stdin via `io.Pipe` after the bridge signals ready over IPC.
3. **Inline `--mcp-config '<json>'`.** Verified in `claude --help` to accept a JSON string. The PoC originally wrote an `.mcp.json` to cwd and broke when the user ran from a different directory; the inline form is the only safe option.

**Bootstrap content (PoC verbatim).** Quoted exactly so Phase-2 doesn't reverse-engineer:

- **System prompt** (used by `ape chat`; `ape pipeline` steps use the standard skill-invocation prompt instead):

  ```
  You are connected to a Web UI. Call await_message() to receive a
  message from the user. When it returns a non-empty string, process
  it and call reply() with your response. If await_message() returns
  an empty string, call it again. Begin by calling await_message() now.
  ```

- **Synthetic user-turn (stdin bootstrap; both `ape chat` and pipeline steps that need a loop start):**

  ```
  Start the await_message loop. Call await_message() now.
  ```

  Written via `io.Pipe` after the bridge signals `{"type":"ready"}` over IPC, with a 30 s timeout fallback (per PoC `serve.go:172`).

**MCP tool contracts (lock these in this plan):**

```
await_message(timeout_seconds?: int) → text
  default timeout: 240 (under 5-min prompt-cache TTL)
  return on message: {"content":[{"type":"text","text":"<msg>"}], "isError": false}
  return on timeout: {"content":[{"type":"text","text":""}], "isError": false}
  concurrency: at most one pending await; second call returns "" immediately
  message buffering: bridge holds a FIFO buffer of up to 5 messages that
    arrived while no await was pending. On the next await_message call, the
    head of the buffer is consumed and returned immediately (no timeout
    wait). On buffer full, oldest is dropped (LRU); the broker logs a
    `buffer-overflow` event to bridge-calls.jsonl.

reply(content: string) → "sent"
  non-blocking
  IPC frame: {"type":"reply","content":"<content>"}
```

`timeout_seconds` is not configurable from the ape side — 240 is the only correct default given the 5-minute cache TTL, and per-call overrides invite cache misses. If a future skill needs longer waits, revisit then.

**HTTP surface (broker).**

| Method | Path          | Body                                                                 | Response                                                                       |
| ------ | ------------- | -------------------------------------------------------------------- | ------------------------------------------------------------------------------ |
| GET    | `/`           | —                                                                    | `text/html` — the page shell rendered from `page.tmpl`                         |
| GET    | `/api/events` | —                                                                    | `text/event-stream` — SSE; reconnect emits a fresh `pipeline-init` (see below) |
| POST   | `/api/send`   | `{"content":"<string>"}` (≤ 64 KB, `Content-Type: application/json`) | `204 No Content` on accept; `400` on malformed; `503` on bridge-not-connected  |
| POST   | `/api/stop`   | `{}` (empty body accepted)                                           | `202 Accepted` (SIGTERM dispatched); `409` if no run is active                 |
| GET    | `/dashboard`  | —                                                                    | `text/html` — cost rollup view (see C7)                                        |
| GET    | `/assets/...` | —                                                                    | static files from embedded `internal/web/assets/` (see C8)                     |

**Auth.** Bind to `127.0.0.1` only. **No token, no cookie.** Threat model: any process running under the user's local account can hit `/api/send` and inject text into the active session, and can read the SSE stream. This is accepted as a v1 limitation; document it in `docs/reference/bridge-security.md` so users on shared-account or multi-user environments know the boundary. A future plan can add a per-session bearer token if a real threat surfaces.

**Browser-close behaviour.** Closing the browser tab does **not** affect the run. Server-side state survives (the bridge subprocess, the SSE broker's per-stage state, the in-flight `claude` invocation continue). Reopening the URL produces a fresh `pipeline-init` event so the UI rebuilds the current state; **backlog replay is out of scope** for PLAN-5 (the user sees current stage cards and a fresh hook/reply feed from reconnect onward; the durable record is in the JSONL streams under the run dir). Explicit cancel goes through the Stop button, not browser-close.

**Stop semantics.** `POST /api/stop` triggers SIGTERM to the active `claude` subprocess via the parent's process-group reference. The bridge subprocess catches SIGTERM and closes IPC cleanly; ape captures the partial step in the manifest with `status: stopped`, emits a final `stopped` SSE event, exits the parent with code 137. In pipeline mode, Stop halts the pipeline — subsequent stages do not run. PLAN-4 boundary-commit for the stopped step is **not** taken (the step did not succeed); the run dir is preserved for post-mortem.

**Subprocess lifecycle.** ape spawns `claude` as a child process and the bridge as a grandchild (Claude Code's MCP machinery does that). On ape's exit (clean or signal), ape SIGTERMs its full child process group so both `claude` and the bridge tear down. On bridge crash mid-step, ape detects the IPC EOF, marks the step `errored`, emits an `error` SSE event with the IPC EOF message, and exits non-zero — there is no auto-restart of the bridge.

**IPC wire (parent ↔ bridge subprocess).**

| Direction         | Frame                                                            | Purpose                                                  |
| ----------------- | ---------------------------------------------------------------- | -------------------------------------------------------- |
| bridge → parent   | `{"type":"ready"}`                                               | Handshake; unblocks the io.Pipe bootstrap goroutine      |
| parent → bridge   | `{"type":"message","content":"..."}`                             | Wake a pending `await_message` (or fill its buffer)      |
| bridge → parent   | `{"type":"reply","content":"..."}`                               | Skill called `reply()`; broker SSEs to browser           |
| bridge → parent   | `{"type":"call","tool":"...","params":{...},"result":{...}}`     | Mirror of every tool call seen at the bridge stdio layer |
| (notify) → parent | `{"type":"hook","event":"...","session_id":"...","payload":...}` | `ape notify` forwarded a hook (C4)                       |

The `call` frame is what feeds `bridge-calls.jsonl` (C6); it includes `await_message` deferred-call entries and their eventual flushes (with `result` populated by either a message or the empty-string timeout).

### C4: Hooks observability via `ape notify`

New subcommand `ape notify --event <EventName>`. Behaviour:

```
ape notify --event PostToolUse
  reads JSON envelope from stdin
  reads APE_BRIDGE_PORT from env
  if APE_BRIDGE_PORT unset or empty: exit 0 (drop silently — bridge not running)
  TCP-dial 127.0.0.1:$APE_BRIDGE_PORT
  on dial failure: exit 0 (don't break the tool loop)
  NDJSON-encode {"type":"hook","event":"<EventName>","session_id":"<from envelope>","payload":<envelope>}
  exit 0
```

Hooks block delivered inline via `BuildSettings()` with `WithHooks: opts.Mode == ModeWeb` (C2). In `--tui` and `--print` modes, the settings JSON has no hooks block; `ape notify` is never spawned. This keeps non-web overhead at zero.

Use `async: true` on every hook (Claude Code 2.1.x, January 2026) — a slow `ape notify` must not stall the tool loop. Events wired in the initial ship:

| Event              | `async` | Purpose in UI                                                |
| ------------------ | ------- | ------------------------------------------------------------ |
| `PreToolUse`       | true    | "running Bash …" card (truncated `tool_input.command`)       |
| `PostToolUse`      | true    | result card with truncated `tool_response`                   |
| `UserPromptSubmit` | true    | mirror as inbound bubble in chat surface                     |
| `SubagentStart`    | true    | open a new sub-agent lane (carries `agent_id`, `agent_type`) |
| `SubagentStop`     | true    | close the sub-agent lane                                     |
| `Stop`             | false   | flush + close per-step run-log; let the loop wait briefly    |

Hook events fire inside `Agent` tool spawns. `agent_id` and `agent_type` from the envelope drive per-sub-agent lane rendering in the web UI.

**Hook → step routing.** The bridge maintains an in-memory table `sessionID → currentStep`. The table is updated when ape emits `stage-start` (`sessionID` becomes whatever the spawned `claude` session id is — the bridge learns it via the first hook event OR an explicit `step-bind` IPC frame from the parent, see below). When an `ape notify` arrives, the bridge looks up `payload.session_id` in the table and tags the event with that step in `hook-events.jsonl`. **Late hooks arriving after `stage-end`** still resolve to the step that owned that session id — the table is append-only; old entries are kept for the lifetime of the run. **Hooks whose `session_id` is unknown** (race at step-start, or sub-agent session id not yet registered) get tagged with `"step":null` and logged as-is; they are not dropped.

For deterministic mapping at `stage-start`, ape sends a parent → bridge IPC frame `{"type":"step-bind","session_id":"<id>","step":"<stage>/<skill>"}` immediately after the bridge handshakes ready. The session id comes from the `--resume`/`--session` machinery if available, or from the first observed hook envelope.

**Event ordering caveat.** Because hooks fire async and arrive via TCP fan-in independent of MCP tool stream, the UI may show `PostToolUse` for a call before `PreToolUse` in pathological cases. This is acknowledged but not solved — sort-by-timestamp within a 1 s window at render time is enough for the displayed feed. Storage in `hook-events.jsonl` is arrival-order; consumers that need wall-clock ordering sort on the `ts` field.

**`PreToolUse` gating contract.** Any future destructive-tool gate uses the current schema: `hookSpecificOutput.permissionDecision: "deny"`. The legacy top-level `decision: "block"` was removed for `PreToolUse` in 2026. Write the new format from day one. **This plan ships no gating policy** — the wiring is there, the rule-set is out of scope (a future plan can layer in destructive-Bash-blocking, sensitive-Read-warning, etc.).

`SessionEnd` is intentionally not wired in the initial ship. Run-log flushing happens on `Stop` (per step). When `SessionEnd` surfaces a `end_reason` field that adds value, a follow-up plan can attach.

### C5: Multi-project port allocation + `ape sessions`

Random free-port allocation per `ape chat` / `ape pipeline` invocation. `~/.ape/registry.json` tracks live sessions cross-project so two `ape pipeline` runs in different working directories never collide. Format:

```json
{
  "sessions": [
    {
      "pid": 12345,
      "cwd": "/home/diegos/projects/foo",
      "command": "ape pipeline design",
      "port": 47291,
      "url": "http://127.0.0.1:47291/",
      "started_at": "2026-05-17T10:23:00Z"
    }
  ]
}
```

- **Port allocation:** `net.Listen("tcp", "127.0.0.1:0")` → read `.Addr().(*net.TCPAddr).Port`. Use the bound listener directly; do not close-and-reopen (race). **127.0.0.1 binding is mandatory** — never `0.0.0.0` or omitted (which would default to all interfaces). Same for the IPC listener.
- **Registry writes:** append on startup (via a small file lock — `flock` on Linux/macOS, fall back to write-and-rename atomically on Windows). Best-effort remove on shutdown via `defer`. A separate `pruneDead()` helper (called by `ape sessions` and by every startup) drops rows whose `pid` no longer exists.
- **URL stdout:** every `ape chat` / `ape pipeline` (web mode) prints exactly one line on startup: `web ui: http://127.0.0.1:47291/`. No prefix decoration — easy to copy. Suppressed under `--print` mode (which has no web UI).
- **`ape sessions` subcommand:**
  - `ape sessions` — table of live sessions (project, command, URL, PID, age). Prunes dead PIDs before printing.
  - `ape sessions prune` — manual prune, useful after a crash.
  - `ape sessions open [<project-prefix>]` — `xdg-open` the live session for the prefix-matched project; error if zero or multiple matches.

**Same-project concurrency** is out of scope (Scope — OUT). The registry can record it but `ape pipeline` running twice in the same cwd produces undefined behaviour for boundary commits anyway.

### C6: Run artefacts — extend PLAN-3's layout in place

**Pipeline runs continue to land at `<project>/_output/pipelines/<name>/<run_id>/`** (PLAN-3's existing path). PLAN-5 adds new files alongside the manifest; it does not move the directory. The eval consumer (`apex_process_framework_eval` PLAN-9) sees no path change.

```
<project>/_output/pipelines/<name>/<run_id>/
├── manifest.yaml             # PLAN-3 v2 schema (no breaking change)
├── hook-events.jsonl         # NEW: one JSON per ape-notify forward
├── bridge-calls.jsonl        # NEW: one JSON per MCP tool call seen by the bridge
├── checkpoints.jsonl         # NEW: ape stage events + skill reply() calls
├── report.md                 # PLAN-3 human report
└── transcripts/              # NEW
    ├── step-01-<skill>.jsonl -> ~/.claude/projects/<hash>/<sid>.jsonl
    ├── step-02-<skill>.jsonl -> …
    └── …
```

- **`<run_id>` format:** unchanged from PLAN-3 — `YYYYMMDD-HHMMSS-<7-char hash>` (see `internal/pipeline/manifest_writer.go:231-234`).
- **Run-id collision:** if `<project>/_output/pipelines/<name>/<run_id>/` already exists, **fail loud** before starting the run: `error: run id <id> already exists at <path>; investigate or remove`. No auto-disambiguate, no overwrite. Collisions are not expected — the timestamp + hash mix already encodes per-second uniqueness — so a collision indicates a bug or filesystem-clock issue worth surfacing.
- **Symlinks**, not copies, into `~/.claude/projects/`. Cheap and the canonical location stays canonical.
- **`hook-events.jsonl` schema** — one line per hook: `{"ts":"<rfc3339>","event":"<EventName>","step":"<stage>/<skill>","session_id":"...","agent_id":"...","payload":{...}}`. `step` is `null` for events whose `session_id` is not yet bound (see C4); `agent_id` is `null` for top-level events.
- **`bridge-calls.jsonl` schema** — `{"ts":"<rfc3339>","method":"tools/call","tool":"reply","params":{...},"result":{...}}`. Also captures `tools/list`, `ping`, `initialize`, and `await_message` (the deferred-call entry plus its eventual flush as a separate line with the same `id`).
- **`checkpoints.jsonl` schema** — `{"ts":"<rfc3339>","kind":"stage-start|stage-end|commit-made|pipeline-end|reply|stopped","step":"<stage>/<skill>","payload":{...}}`. `reply` checkpoints carry the verbatim skill `reply()` content.

**`ape chat` artefacts** live at **`<project>/_output/ape/chats/<chat-id>/`** (separate convention — chat sessions are ad-hoc, not pipeline-shaped). Layout:

```
<project>/_output/ape/chats/<chat-id>/
├── session.yaml              # chat metadata (start, end, model, cost roll-up)
├── hook-events.jsonl         # same schema as pipeline runs
├── bridge-calls.jsonl        # same schema
├── checkpoints.jsonl         # same schema (kinds: chat-start, reply, chat-end)
└── transcript.jsonl -> ~/.claude/projects/<hash>/<sid>.jsonl
```

- `<chat-id>` format: `YYYYMMDD-HHMMSS-<7-char hash>`, hash mixes timestamp + cwd + pid for cross-process uniqueness.
- No PLAN-3 manifest equivalent (chats are not pipelines); `session.yaml` is a small purpose-built record.
- The `cost.json` rollup mechanism (C7) reads chat artefacts and pipeline artefacts uniformly.

**`.gitignore` policy.** On first `ape chat` / `ape pipeline` run in a project, if `_output/` is not already gitignored, ape prompts (TTY) or warns (non-TTY) and offers to append `_output/` to the project root's `.gitignore`. Default to "ask" in TTY, "warn-only" in non-TTY. Document in `docs/how-to/run-artefacts.md`.

**User-level state (`~/.ape/`) is unchanged** — port registry, price table, plugin cache stay there. The split is: per-project run history under `<project>/_output/{pipelines,ape/chats}/`, cross-project state under `~/.ape/`.

### C7: Cost tracking — populate existing manifest fields from session JSONL

PLAN-3's v2 manifest already declares per-step `cost_usd` + `tokens_input` + `tokens_output` + `tokens_cache_read` + `tokens_cache_creation` (and matching totals). PLAN-5 **does not change the schema**; it adds a second data-source path that reads per-message `usage` blocks from the session JSONL and fills those existing fields in web/TUI mode (where `--print`/`--output-format stream-json` is unavailable).

**Data source by mode:**

| Mode          | Source                                                                                           | Path                                                        |
| ------------- | ------------------------------------------------------------------------------------------------ | ----------------------------------------------------------- |
| `--print`     | `result` event in stream-json stdout (existing PLAN-3 path, `internal/pipeline/result_event.go`) | unchanged                                                   |
| web / `--tui` | per-`assistant`-message `usage` blocks in `~/.claude/projects/<hash>/<sid>.jsonl` (new)          | `internal/cost/jsonltail.go` (new) tails the symlink target |

**JSONL tail mechanism.** Polling-based for cross-platform simplicity (no inotify, no fsnotify). One goroutine per step:

1. Wait for the transcript symlink to exist (polling at 200 ms intervals; 30 s timeout). Once present, resolve to the underlying file.
2. Open file for read; seek to end-of-file is **not** done — the file may already have content by the time the goroutine starts. Read from byte 0.
3. Buffered reader, line-by-line. Maintain a partial-line buffer: if a read returns no newline, retain the partial bytes and re-read on the next poll tick.
4. For each complete line, attempt `json.Unmarshal` into a small struct: `{Type string; Message struct{Model string; Usage UsageBlock}}`. Skip lines without `type:"assistant"` (user, system, tool_use blocks).
5. On a valid assistant line, compute turn cost (formula below) and accumulate into the step's running totals. The bridge also publishes a `stage-update` SSE event with the new cumulative cost so the dashboard cost column updates live.
6. Polling cadence: 200 ms between EOF-poll attempts. Tunable via `APE_COST_TAIL_INTERVAL_MS` env var, undocumented (debug knob).
7. On `stage-end`: drain one final time (read until consecutive EOF reads with no new bytes for two ticks), then close. Step totals are flushed into the manifest record alongside `commit_sha` (PLAN-4).

**Cost formula** (verified by inspection of session JSONL, design doc §11):

```
turn_cost = BaseInput × input_tokens
          + BaseInput × 1.25 × cache_creation.ephemeral_5m_input_tokens
          + BaseInput × 2.00 × cache_creation.ephemeral_1h_input_tokens
          + BaseInput × 0.10 × cache_read_input_tokens
          + Output    × output_tokens
```

**Price table — `internal/cost/prices.go`.** Hand-curated, keyed by the `model` field on each assistant line:

```go
var Prices = map[string]ModelPrice{
    "claude-opus-4-7":   {BaseInput: ..., Output: ...},  // USD per 1M tokens
    "claude-sonnet-4-6": {BaseInput: ..., Output: ...},
    "claude-haiku-4-5":  {BaseInput: ..., Output: ...},
}
```

Initial values land with the implementation PR (not in this plan). Refresh path: `ape costs update --from <yaml-file>` overwrites the table from a local file. No live API call to Anthropic billing. Unknown model → cost 0, `tokens_*` fields still populate, manifest gains a `cost_note: "unknown model: <model>"` field for that step.

**Project-level rollup.** `<project>/_output/ape/cost-rollup.json` aggregates every pipeline run's manifest totals + every chat's `session.yaml` totals into a single record per pipeline name / per chat / per date bucket. Written by `ape costs roll` (manual) and on every `ape pipeline` / `ape chat` exit (automatic, best-effort: on failure, stderr-warn and continue; do not block exit).

**`ape costs` subcommand:**

- `ape costs` — current project rollup (today / this week / total), broken down per pipeline + chat.
- `ape costs run <run-id>` — single pipeline-run detail (reads manifest.yaml).
- `ape costs chat <chat-id>` — single chat detail.
- `ape costs update --from <file>` — refresh prices.
- `ape costs roll` — manual rollup rebuild.

**`/dashboard` web route** surfaces the same data live during a pipeline run, refreshed on every `stage-end` event. Spec'd minimally in this plan; full UI iteration deferred to a follow-up.

### C8: Web UI — HTMX + stdlib `html/template`, vendored assets, no toolchain

Stack locked by the UI spike (`development/research/ui-spike.md`; working reference at `/home/diegos/_dev/github/diegosz/claude_mcp_bridge_spike/variant-htmx/`). Layout:

```
internal/web/
├── server.go                # HTTP mux, SSE handler, per-connection view state
├── assets.go                # //go:embed assets/* directive lives here
├── views/
│   ├── state.go             # per-connection rolling buffers (last-N hooks per stage)
│   └── stage.go             # StageView struct, hook truncation helpers
├── templates/
│   ├── page.tmpl            # one-time layout: <head>, <body>, mount points
│   ├── fragments.tmpl       # one `{{define}}` per SSE event: stage-card,
│   │                        # hook, reply, decision-gate, cost-ticker,
│   │                        # input-pending, input-resolved, stop-button
│   └── helpers.go           # template.FuncMap: fmtDuration, fmtCost, truncate
└── assets/
    ├── styles.css           # handwritten, ~150 LOC, no preprocessor
    ├── app.js               # inline-helper extraction point; empty for the
    │                        # initial ship — inline onclick is fine
    └── vendor/
        ├── htmx.min.js              # HTMX 2.x core, committed (~17 KB gz)
        └── htmx-ext-sse.min.js      # HTMX SSE extension, committed (~1 KB gz)
```

**SSE wire schema (locked from spike + new bridge events):**

| Event            | Payload (HTML fragment with OOB swap markers)                                                            |
| ---------------- | -------------------------------------------------------------------------------------------------------- |
| `pipeline-init`  | full stage list scaffold; resets `#stages`, `#hooks`, `#replies` on every connection                     |
| `stage-start`    | stage card swap into `#stages`                                                                           |
| `stage-update`   | stage card replacement (status / duration / cost columns)                                                |
| `stage-end`      | stage card replacement (terminal state: pass / fail / stopped) + cost-ticker OOB update                  |
| `hook`           | `<li hx-swap-oob="beforeend:#hooks">…</li>` — one hook line, server-truncated, per-stage scoped via id   |
| `reply`          | `<li hx-swap-oob="beforeend:#replies">…</li>` — one reply line                                           |
| `await-pending`  | NEW: OOB swap of `#decision-gate` → input element enabled + visual cue ("skill is waiting for input")    |
| `await-resolved` | NEW: OOB swap of `#decision-gate` → input element disabled / placeholder restored                        |
| `stopped`        | NEW: OOB swap of `#status` → "Stopped by user" terminal banner; emitted after `POST /api/stop` completes |
| `error`          | NEW: OOB swap of `#status` → "Bridge error: <message>" terminal banner; emitted on bridge IPC EOF        |

**`#decision-gate` element.** A persistent `<form>` in `page.tmpl` containing a single `<input>` + Send button. Disabled by default (the input has `disabled` attribute, the button is greyed). `await-pending` swaps in a fragment that removes `disabled` and adds a visible label ("skill is awaiting input"); `await-resolved` swaps back. Posts go to `/api/send` via `hx-post="/api/send"`. **Submitting while disabled is a no-op** (the form is disabled at the DOM level); submitting while enabled sends content and lets the server decide whether to buffer (per C3's buffer policy) — the UI does not distinguish "consumed immediately" from "buffered" since the visible feedback (the next assistant message) is the same.

**Stop button.** A small `<button>` in the page header. `hx-post="/api/stop"`, `hx-confirm="Stop the active step? Step state will be marked stopped and the run will halt."`. Available only when a pipeline is running (toggled visible/hidden via the same OOB swap mechanism on `pipeline-init` and `stage-end[last]`).

**Per-connection state.** The broker maintains, per SSE connection:

- `current_stage` — pointer to the active stage card so OOB hook swaps land under the right card.
- `hook_buffer[stage]` — rolling buffer of last 20 hook lines per stage (matches spike behaviour).
- `last_seen_seq` — monotonic counter to drop duplicate frames after a reconnect.
- `await_pending` — bool, used to render the initial decision-gate state on reconnect.

On reconnect (EventSource auto-reconnect handled by the SSE extension), the broker emits a fresh `pipeline-init` so the client resets its lists, then re-emits `await-pending` if a gate is currently open. **Backlog replay** ("show me the hooks I missed while the tab was closed") is out of scope; the JSONL stream under `<run-dir>/` is the durable record.

**No JS framework dependency.** Inline `onclick` for the handful of client-only widgets (copy-to-clipboard, modal toggles); helper functions in a single `<script>` block at page foot, or in `assets/app.js` if it crosses ~50 LOC. **Alpine adoption is explicitly deferred** — not in scope for this plan.

**No build step on the end-user side.** `go install github.com/diegosz/apex_process_ape@latest` produces a working binary; vendored assets embedded via `//go:embed assets/*` in `internal/web/assets.go`. Contributor side has no codegen — `go build` is the whole pipeline.

### C9: Test strategy + PoC port mechanics

**Mock claude in testdata.** Port `mock_claude.go` from the PoC into `internal/bridge/mcp/testdata/mock_claude/`. It implements just enough of the MCP stdio protocol to drive bridge integration tests without a real `claude` binary:

- Responds to `initialize`, `tools/list`, `ping`.
- Calls `await_message`, awaits a deferred response, then calls `reply` with a canned string. Loops until stdin EOF.
- Bridge integration tests (`internal/bridge/mcp/integration_test.go`) spawn the mock via `exec.Command(os.Args[0], "-test.run=TestMockClaude")` — the Go-test self-spawning idiom — so no separate binary needs to be built and shipped.

Three classes of test land with this plan:

1. **Unit tests** — `BuildMCPConfig`, `BuildSettings`, JSONL tail line-parser, cost formula, hook→step routing.
2. **Bridge integration tests** — bridge subprocess + mock claude + in-process broker + simulated browser POST. Validates the deferred-response slot, the SSE flush invariant (a test that drops `flusher.Flush()` and asserts the response is buffered fails), the buffer-5 FIFO policy, and the await-pending/await-resolved emission.
3. **CLI smoke** — `ape pipeline --print` byte-equivalence against today's `--no-tui` output (a recorded fixture). Run in CI; gates the C1 merge.

Real-`claude` integration tests are **not** required for CI but are useful for local pre-release validation; place them under `internal/bridge/mcp/integration_real_test.go` with a build tag `realclaude` so they only run when explicitly invoked.

**PoC port mechanics.** Each ported file gets a 1-line attribution header:

```go
// Ported from https://github.com/diegosz/claude_mcp_bridge_poc, commit 4e542d0 (MIT).
```

The PoC repo continues to exist as the validated reference; subsequent PoC commits do not auto-propagate. If the PoC's LICENSE differs from ape's at port-time, capture the relevant terms in `internal/bridge/LICENSE.poc` and reference it in the package doc. No git submodule, no vendoring — the bridge becomes first-class ape code at port time.

## Scope — OUT

- **In-Claude conductor skill** (the abandoned Phase-1 `apex-run-pipeline`). Reasoning documented in `origin:` above and design doc Context section. Do not revisit in this plan.
- **Backlog replay on reconnect.** Reconnecting after a tab close shows current state only — `connected` + `pipeline-init` + per-stage status (the stage-state replay landed post-launch in `ad2e508`), not the full history of past hook / call / reply events. The durable record for those streams is the JSONL files under the run dir; a follow-up plan can render them on reconnect.
- **Bearer-token auth for the web UI.** Localhost-only with documented threat model is the v1 stance; a follow-up plan can add per-session tokens if the threat surfaces.
- **Remote bridge operation.** "Run `ape` here, view the UI from a different machine" is out. The IPC abstraction (`internal/bridge/ipc/`) is small enough that a future plan can swap TCP NDJSON for stdlib WebSocket or NATS-embedded without touching the SSE broker — codify the wire shape in `docs/reference/bridge-ipc.md` to keep that path open.
- **NATS-embedded or WebSocket IPC migration.** Defer until fan-out (multiple consumers of one event stream) or remote operation is a concrete deliverable. Design doc §7 records the migration triggers.
- **Channels protocol.** Research-preview, plan-gated, not self-serve. The MCP blocking-tool approach supersedes channels for ape's needs; `claude-channel-bridge.md` is the historical record.
- **Multiple bridge sessions within the same project.** Per-project port registry is cross-project only. Same-project concurrency interacts badly with boundary commits — punt.
- **Destructive-tool gating policy.** The `PreToolUse` wiring lands in C4 with `hookSpecificOutput.permissionDecision: "deny"`; the rule-set itself (which Bash patterns to block, which Read paths to warn on) is a separate plan.
- **Islands-pattern client-heavy widgets.** The design doc §6 "future route" subsection documents the migration path for collaborative-editor / complex-DnD / code-editor widgets when one appears. No concrete candidate today.
- **TUI parity work.** The Bubble Tea TUI's behaviour is frozen at PLAN-2's end-state. `--tui` keeps the existing surface; no per-step cost column, no hook drawer, no new features in TUI mode. Web is where new surface work lands.
- **Project-MCP merge.** `--strict-mcp-config` is used always — project `.mcp.json` and user MCP servers are hidden from the bridged session. If a real need surfaces, a follow-up plan adds `_apex/config.yaml`-driven MCP-server merging.

## Notes for implementation phasing

This plan does not prescribe a phase split. A reasonable execution order is C2 → C3 (bridge runtime first, because every other C depends on it being callable from `ape chat`) → C1 (CLI surface, once `ape mcp-bridge` is dial-able) → C4 (hooks) → C5 (port registry) → C6 (run artefacts) → C8 (web UI) → C7 (cost tracking, last because it consumes data the rest produce). C9 (tests) lands incrementally with each piece. The TUI default flip in C1 should land last in the user-visible release — flag it explicitly in the merging order and in CHANGELOG.

The eval harness (`apex_process_framework_eval`) consumes `ape pipeline --print` today and reads from `<project>/_output/pipelines/<name>/<run_id>/`. Both contracts are preserved by this plan; verify with a CI smoke test before merging C1 (byte-equivalence of `--print` output) and C6 (the new files land _alongside_ `manifest.yaml`, not replacing it).

## Implementation appendix — post-launch refinements (2026-05-17)

After `plan: PLAN-5 → done` (`89c525e`), live testing on the
`/home/diegos/_dev/ape-web-sandbox/greeter` sandbox surfaced a
batch of UI / wiring fixes. These are in scope of PLAN-5 — every
item is closing a gap in the originally-scoped surface, not new
feature work. Commits land on `main`.

| Commit    | Surface         | Note                                                                                                                                                       |
| --------- | --------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `7a48fc8` | C8              | SSE sentinel listener — page stuck on "connecting…" because no element with `sse-swap` listed `hook`/`reply`/`await-*`/etc.                                |
| `de212ca` | C1              | `runWithWeb` prints `Error: <msg>` on stderr — cobra's `SilenceErrors=true` was swallowing the dirty-tree-gate's actionable message.                       |
| `86e6906` | C8              | Status-banner flip moved from sentinel `hx-on::sse-open` to `<body>` + JS document-level fallback. Sentinel events don't propagate downward to a sibling.  |
| `75e5d1d` | C8 + C6         | Pre-populated stage scaffold (all stages from spec on first paint), enriched activity feed (TS · event · tool · summary, colour-coded), mode-aware page.   |
| `5395159` | C9 (diagnostic) | `APE_DEBUG_ARGV` env var prints the full claude argv before spawn. Stable diagnostic knob alongside `APE_COST_TAIL_INTERVAL_MS`.                           |
| `1faa1ab` | C8              | Flexbox row layout, auto-scroll activity (MutationObserver), terminal `pipeline-end` banner with "completed in N" / "failed: …".                           |
| `a55ddb9` | C1 / C8         | Stop button actually cancels pipeline. `runCtx` is split from `hubCtx`; `stopFn` cancels `runCtx`. `<ul>/<li>` → `<div>/<div class=hook-row>` for clarity. |
| `7c96773` | C8              | `Cache-Control: no-store` on `/assets/*`. Browsers were serving stale `styles.css` across rebuilds. `!important` on `.hook-row` layout as belt-and-braces. |
| `47b1c03` | C8              | OOB-carrier fix: `hx-swap-oob="beforeend:X"` strips the carrier element. `.hook-row` wrapped inside another `<div hx-swap-oob>` so it survives.            |
| `3d0887c` | C8 + C6         | Project-relative paths in the activity feed (`<projectRoot>/` stripped from summaries). Visible centred completion banner above stages on `pipeline-end`.  |
| `ad2e508` | C3 + C8         | Per-stage state replay on SSE subscribe. First stage's `stage-start` fires ~20 ms after `pipeline.Run`, before any browser subscribes — was silently lost. |

### Sharp edges remaining (deliberately not blocking)

- **Session-id discovery is mtime-based.** `ape chat`'s
  `cost.FindSessionJSONL` picks the newest `*.jsonl` under
  `~/.claude/projects/` modified after the chat startedAt. If
  Claude Code ever documents `--session <id>` we can switch to
  exact-id discovery (`internal/cost/scan.go` comment notes the
  trigger).

- **Live SSE cost ticker for in-flight stages is not wired.** Per-step
  cost lands in `manifest.yaml` after each step ends (PLAN-3 result
  event still works in --web because each step's claude is still
  spawned with `-p ... --output-format stream-json`). A future
  enhancement could tail the session JSONL in real time and update
  a per-stage cost column over SSE.

- **Backlog replay on reconnect is partial.** `connected`,
  `pipeline-init`, and per-stage state are replayed on every new
  subscription (`ad2e508`); past hook / call / reply events are
  not. The durable record for the unreplayed streams is the JSONL
  files under the run dir.

- **The activity feed is unbounded in DOM size.** For a 13-stage
  pipeline the feed routinely hits a few thousand rows. Browser
  performance is fine in practice but a rolling-window (last N
  rows) might be worth a future tweak if a longer pipeline pushes
  past comfortable.
