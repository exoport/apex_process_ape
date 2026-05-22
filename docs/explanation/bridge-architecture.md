# Bridge architecture

`ape pipeline --web -P` (web programmatic) connects a browser to a
running Claude Code session via three loosely coupled pieces: an
MCP server (the **bridge**), an SSE broker (the **broker**), and an
orchestrator that owns the subprocess lifecycle and stitches the
two together.

This document is the design narrative for the **web programmatic**
mode. For the wire schema, see
[bridge-ipc.md](../reference/bridge-ipc.md). For the security model,
see [bridge-security.md](../reference/bridge-security.md). PLAN-5 / C3.

> **Note on interactive exec (PLAN-6 tmux pivot 2026-05-20 → PLAN-8 PTY migration 2026-05-22).** Pipeline
> interactive mode (`ape pipeline --tui` / `--no-tui` / `--web` without
> `-P`) and `ape chat` no longer use `await_message` / `reply` as a
> prompt-delivery channel. They run `claude` attached to a PTY and
> deliver prompts as real REPL keystrokes (`internal/repl/` writes the
> slash command to the PTY master + Enter). The bridge is still wired
> for **hook observability** (`PreToolUse`, `PostToolUse`,
> `UserPromptSubmit`, `Stop`, etc.) but `await_message`/`reply` are
> dormant for those modes. Originally implemented over external `tmux`
> (PLAN-6); PLAN-8 moved the PTY in-process via `go-pty` to drop the
> `tmux` runtime dependency and add native Windows support. The `--web -P`
> flow described below is unaffected by either pivot.

## Why an MCP bridge

Claude Code's MCP support lets us expose tools that block. Two are
enough for the web programmatic interactive session:

- `await_message`: holds the pending request id until a browser
  message arrives over IPC, then responds with the text.
- `reply`: non-blocking, forwards the content over IPC; the parent
  publishes an SSE `reply` event for the browser.

This is the loop (web programmatic only): claude → `await_message`
(blocks) → user types in browser → `/api/send` → IPC → bridge →
response delivered → claude calls `reply(...)` → IPC → broker → SSE
→ browser.

The pattern was validated by the PoC at
`/home/diegos/_dev/github/diegosz/claude_mcp_bridge_poc` commit
`4e542d0`. Three production-critical fixes carried over:

1. **SSE explicit flush.** Without `flusher.Flush()` after every
   `Fprintf`, slow producers leave events buffered until the OS
   chunk fills. The dashboard freezes for >30 s gaps. Locked with
   `TestBroker_SSEFlushOnEveryEvent`.
2. **`stdin io.Pipe` bootstrap.** `--system-prompt` alone leaves
   claude idling at the prompt without an initial user turn. The
   parent must write one synthetic turn via `io.Pipe` after the
   bridge signals ready over IPC.
3. **Inline `--mcp-config '<json>'`.** Writing `.mcp.json` to cwd
   breaks the moment the user `cd`s elsewhere. The PoC originally
   did that; the ape port uses inline JSON via argv (always paired
   with `--strict-mcp-config` so project + user MCP servers don't
   leak into the bridged session).

## Components

```
┌──────────────────┐   stdin/stdout MCP    ┌─────────────────┐
│  claude (child)  │ ◀────JSON-RPC ──────▶ │  ape mcp-bridge │
└──────────────────┘                       │   (grandchild)  │
                                           └────────┬────────┘
                                              IPC NDJSON (TCP)
                                                    │
┌────────────────────┐    ───── parent ──── ┌───────▼────────┐
│  Web UI (browser)  │ ◀──── SSE ─────────▶ │  ape (parent)  │
│   HTMX + assets    │   ── POST /api/* ──▶ │  orchestrator  │
└────────────────────┘                       └────────────────┘
                                                    ▲
                                            APE_BRIDGE_PORT
                                                    │
                                              one TCP dial
                                                    │
                                           ┌────────┴────────┐
                                           │  ape notify     │
                                           │  (hook subproc) │
                                           └─────────────────┘
```

- **`ape` (parent)** — owns the IPC listener, broker, runlog writer,
  port registry. Spawns one `claude` subprocess per session (chat)
  or per pipeline step. SIGTERMs the whole process group on Stop so
  the bridge grandchild tears down cleanly.
- **`ape mcp-bridge` (grandchild)** — runs the MCP runtime. Reads
  `APE_IPC_PORT` from env to dial back to the parent. Two blocking
  tools (`await_message`, `reply`) plus standard MCP handshake.
  Mirrors every tool call to the parent for `bridge-calls.jsonl`.
- **`ape notify` (per-hook subprocess)** — spawned by Claude Code
  when a hook fires (PreToolUse, etc.). Dials `APE_BRIDGE_PORT`
  (same IPC listener), writes one `{"type":"hook",...}` frame,
  exits. All failure modes exit 0 so the tool loop never stalls.

## Hooks shape the UI

The inline `--settings` blob wires six hooks (`PreToolUse`,
`PostToolUse`, `UserPromptSubmit`, `SubagentStart`, `SubagentStop`,
`Stop`). Five are `async: true` so the tool loop never waits; `Stop`
is `async: false` because the parent uses it to flush the per-step
run-log before the loop returns.

Hooks are injected **only when `Mode == ModeWeb`**. In `--tui` and
`--eval` modes, `BuildSettings` returns `{}` and `ape notify` never
spawns. PLAN-5 / C4.

## Why this layout

- **One listener, two dial points.** The parent runs a single TCP
  listener and accepts many connections. The first conn sending
  `{"type":"ready"}` is the bridge (long-lived, bidirectional);
  every other conn is a one-shot `ape notify` writing one hook
  frame. Demux by first-frame type — no port-per-purpose sprawl.
- **HTML fragments over the SSE wire.** HTMX SSE extension dispatches
  named events to OOB swap targets in the page. Each fragment is
  self-contained: `<li hx-swap-oob="beforeend:#hooks">…</li>`. The
  broker doesn't know about HTML — it just forwards strings —
  but the orchestrator renders them via the C8 template package.
- **Orchestrator owns lifecycle, broker stays generic.** Stop, Run,
  bootstrap-io, IPC routing all live in `internal/bridge/orchestrator`.
  The broker is a small fan-out HTTP server that the orchestrator
  embeds. This keeps the broker easy to test (`TestBroker_*` in
  `internal/bridge/broker/broker_test.go`) and lets a future plan
  reuse it for /dashboard-only flows without spawning claude.

## What's deferred

- **Pipeline web-mode wiring.** The `ape pipeline <name>` runner
  still uses today's TUI / print path. The web-mode flag plumbing
  is in place (`--tui` is currently inert default; `--eval` is
  the explicit name for what `--no-tui` used to do); the eventual
  flip is held until a follow-up release-cycle merge. Pipeline web
  mode will mount the runlog Writer + per-step JSONL tail through
  the orchestrator the same way `ape chat` does today.
- **Bearer-token auth.** See [bridge-security.md](../reference/bridge-security.md).
- **Backlog replay on reconnect.** Tabs that close miss the
  intervening hooks. The durable record is `hook-events.jsonl`;
  a follow-up plan can render that on reconnect.
- **Cost-table values.** `internal/cost/prices.go` has starter
  numbers marked TODO; confirm against the current Anthropic
  price list before the cost path is load-bearing.
