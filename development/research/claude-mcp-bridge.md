# Claude MCP Bridge

## Context

`ape pipeline` currently spawns one `claude -p "/{skill} --autonomous ..."` subprocess per
pipeline step — a fresh billed API call per step, no shared context across steps.

The fix has two independent phases:

| Phase | What                                                                                                                    | Blocks on                                                    |
| ----- | ----------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------ |
| 1     | `apex-run-pipeline` conductor skill — replaces `claude -p` with the Agent tool (same pattern as `apex-story-batch-dev`) | Nothing. Fully independent.                                  |
| 2     | `ape` MCP bridge + Web UI — real-time progress display and interactive decisions without `claude -p`                    | Nothing. Channels were investigated and dropped (see below). |

This document covers **Phase 2**.

## Why Not Channels

Claude Code channels (`notifications/claude/channel`) were investigated first. They allow an
MCP server to push events into a running Claude session passively. However:

- Channels are in research preview and require Anthropic backend provisioning.
- The admin org toggle (`claude.ai → Admin settings → Claude Code → Channels`) is a
  pre-authorization signal — it does not self-activate the feature.
- Confirmed unavailable on Claude Team and Claude Max plans (Claude Code v2.1.143) after
  enabling the org toggle and doing a full logout/login.
- No self-serve path exists to unblock this during the research preview.

Full investigation recorded in [`claude-channel-bridge.md`](claude-channel-bridge.md).

## The MCP Blocking-Tool Approach

Standard MCP tool calls flow one way: Claude calls a tool, the MCP server responds.
A **blocking tool call** makes this bidirectional:

1. Claude calls `await_message()` — the MCP server does **not** respond immediately.
2. The MCP server holds the pending request ID in memory.
3. The user types in the Web UI → browser POSTs to ape → ape wakes the pending request.
4. MCP server responds to Claude's tool call with the message text.
5. Claude processes it, calls `reply(content)` — ape broadcasts to browser via SSE.
6. Claude calls `await_message()` again — loop.

No channels capability, no platform provisioning, no special Claude Code version required.
Works on any plan today with standard MCP.

## The Two Tools

### `await_message`

```json
{
  "name": "await_message",
  "description": "Block until a message arrives from the Web UI. Returns the message text. Call this in a loop to handle the interactive session.",
  "inputSchema": {
    "type": "object",
    "properties": {
      "timeout_seconds": {
        "type": "integer",
        "description": "Seconds to wait before returning an empty string. Default 300.",
        "default": 300
      }
    }
  }
}
```

**Return on message:** `{"content": [{"type": "text", "text": "<user message>"}], "isError": false}`

**Return on timeout:** `{"content": [{"type": "text", "text": ""}], "isError": false}`

The empty string signals Claude that no message arrived within the timeout. The skill/session
decides what to do: idle-check, send a heartbeat, or exit the loop gracefully.

### `reply`

```json
{
  "name": "reply",
  "description": "Send a message to the Web UI immediately. Use this to respond to the user or report pipeline progress.",
  "inputSchema": {
    "type": "object",
    "properties": {
      "content": { "type": "string", "description": "Text to display in the Web UI." }
    },
    "required": ["content"]
  }
}
```

Non-blocking. Returns immediately with `{"content": [{"type": "text", "text": "sent"}]}`.

## Architecture

```
ape (parent process)
├── Web UI HTTP server         :8787   browser ← SSE replies, → POST messages
├── IPC TCP listener           :XXXXX  bridge ↔ parent (message queue relay)
│
└── subprocess: claude
      (interactive mode, no -p, no --channels flag)
        │
        └── subprocess (from .mcp.json): ape mcp-bridge
              ├── MCP JSON-RPC over stdio      ←→ Claude Code
              ├── await_message tool: deferred response on IPC "message" event
              └── reply tool: IPC write → parent → SSE → browser
```

### Startup sequence

1. `ape serve` allocates IPC port, starts Web UI server, starts IPC listener.
2. Writes `.mcp.json` pointing to `ape mcp-bridge` with `APE_IPC_PORT`.
3. Starts `claude` interactive (no special flags beyond `--mcp-config`).
4. Claude Code spawns `ape mcp-bridge` from `.mcp.json`.
5. Bridge completes MCP handshake, signals parent "ready" over IPC.
6. Claude receives initial system prompt (see "Loop initialisation" below).
7. Claude calls `await_message()` — bridge holds the pending request.
8. User types in browser → POST → parent → IPC "message" event → bridge responds to pending request.
9. Claude reads response, processes, calls `reply(content)`.
10. Bridge receives `reply` tool call → IPC write → parent → SSE → browser.
11. Claude calls `await_message()` again.

### Data flow

| Direction                 | Transport                                                                          |
| ------------------------- | ---------------------------------------------------------------------------------- |
| Claude → Web UI (reply)   | `reply` tool call → bridge IPC write → parent → SSE → browser                      |
| Web UI → Claude (message) | browser POST → parent → IPC "message" → bridge responds to pending `await_message` |
| Claude ↔ bridge           | MCP JSON-RPC 2.0, NDJSON, stdio                                                    |
| Bridge ↔ parent           | TCP, NDJSON (`{"type":"...","content":"..."}`)                                     |
| Parent ↔ browser          | HTTP + SSE (`GET /api/events`), POST (`/api/send`)                                 |

### Cloud traffic

None. All communication is local: stdio for MCP, TCP for IPC, localhost HTTP for the Web UI.

## Key Implementation Detail — Async Deferred Response

The MCP message loop reads from stdin sequentially (one line at a time). Naively blocking
inside the `await_message` tool handler would freeze the loop, preventing `reply`,
`tools/list`, and `ping` from being handled while waiting.

**Solution:** deferred response with a pending-request slot.

```
bridge internal state:
  pendingAwait: { id: json.RawMessage, deadline: time.Time } | nil
  messageQueue: chan string  (buffered, capacity 1)
```

When `await_message` is called:

1. Parse `timeout_seconds`, compute `deadline = now + timeout`.
2. Store `{id, deadline}` in `pendingAwait`.
3. **Do not respond.** Return from the tool handler without writing to stdout.
4. Continue the scanner loop normally.

When an IPC "message" event arrives (from the Web UI, via the IPC reader goroutine):

1. Read `pendingAwait`; if non-nil, clear it and respond to `id` with the message text.
2. If nil (no pending await), drop the message or buffer it (design choice).

Timeout goroutine (runs continuously):

```
every second:
  if pendingAwait != nil && now > pendingAwait.deadline:
    respond(pendingAwait.id, empty string result)
    pendingAwait = nil
```

This keeps the scanner loop free to handle concurrent requests while `await_message` waits.

## Open Design Questions

### 1. Timeout behaviour

When `await_message` times out (returns `""`), Claude must decide what to do. Options:

- **A — Loop silently:** Claude checks for empty string and calls `await_message()` again.
  Simple, but Claude may waste tokens on the check each cycle.
- **B — Exit the loop:** Claude treats timeout as session-end and commits a summary.
  Clean, but no recovery if the user is just slow.
- **C — Heartbeat reply:** On timeout, Claude calls `reply("⏳ still here — send a message")`.
  Useful UX but chatty.
- **D — Configurable:** `await_message` accepts a `on_timeout: "loop"|"exit"|"heartbeat"` param.

Recommendation: **A** for the PoC (simplest), **D** for production.

### 2. Loop initialisation — how Claude knows to call `await_message`

When `claude` starts interactively with the MCP server loaded, Claude must know to enter
the loop. Options, in increasing complexity:

**Option I — `claude --system "..."` flag:**
Start claude with a system prompt that instructs the loop behaviour.

```bash
claude --system "You are connected to a Web UI. Call await_message() in a loop: receive a
message, process it, call reply() with your response, repeat. Call await_message() now."
```

Clean, no skill needed. Supported by Claude Code CLI (`--system-prompt` flag).

**Option II — Minimal `/bridge-session` skill:**
User types `/bridge-session` in the terminal once. The skill body is three sentences
instructing the loop. Allows richer initial context (e.g. project awareness).

**Option III — MCP `prompts` capability:**
The bridge declares a prompt template `start_session` via `prompts/list`. Claude Code
may surface this as a suggested action. Less reliable as a bootstrap mechanism.

**Option IV — First message as bootstrap:**
The bridge sends a synthetic first message to Claude immediately after "ready" (via a
pre-queued IPC message before Claude calls `await_message` the first time). This message
contains the loop instructions. No `--system` flag needed; works fully from the bridge side.

Recommendation: **Option I** for the PoC (`--system` is easy to wire in `serve.go`),
**Option II** for production (richer context, familiar skill UX).

### 3. Concurrent `await_message` calls

The current design allows at most one pending `await_message` at a time. The pipeline
skill should never call it concurrently. If two calls arrive (e.g. due to a skill bug),
the second one immediately returns empty string (or an error). Document this constraint.

### 4. Message buffering

If the user sends a Web UI message before Claude has called `await_message()` (e.g. during
a long-running step), the message is dropped in the current design. Options:

- Buffer N messages in a queue; `await_message` drains them FIFO.
- Return a 503 from `/api/send` if no pending await (explicit signal to the user).

Recommendation: buffer 1–5 messages for production; drop for the PoC.

### 5. Pipeline integration (Phase 1 + 2 together)

In the full pipeline skill, `await_message` and `reply` serve a different purpose than
the "telegram remote" PoC:

- `reply` is called by the skill after each stage to push a progress summary to the Web UI.
- `await_message` is called only at explicit pause points (e.g. stage failure, user
  confirmation requested). The pipeline does NOT loop on `await_message` continuously.

This means the session does NOT need to enter a message loop for the pipeline case.
The Web UI is a one-way progress display most of the time, with `await_message` used
selectively for interactive gates.

### 6. Web front-end — HTMX, Alpine.js, or vanilla JS

Current PoC uses vanilla JS with `EventSource` + `fetch`. Three realistic alternatives:

**Vanilla JS (current):** Zero dependencies, no build step. Fine for the chat loop PoC.
Weakest fit for pipeline progress: manually constructing HTML for stage cards,
status badges, and collapsible logs is tedious.

**HTMX + `hx-ext="sse"`:** Declarative SSE subscription; `hx-post` for sends.
The server renders HTML fragments instead of JSON — each `reply` event carries a
pre-rendered stage card that `hx-swap` inserts into the DOM. Eliminates all JS business
logic. Excellent fit for Go + `html/template`. Adds ~14 KB (HTMX core + SSE extension),
no build step.
Research needed: does `hx-sse` re-subscribe automatically on reconnect? How does
fragment-per-event compose with multiple simultaneous stage updates?

**Alpine.js (~8 KB, no build step):** Reactive HTML attributes, client-side state.
Better fit if we keep a JSON API and want richer client interactions (expandable log panels,
per-stage status badges that update in place) without React/Vue overhead.

Recommendation: research both HTMX and Alpine.js. HTMX is the stronger fit if the server
controls rendering (Go templates); Alpine.js if the client owns layout logic.

### 7. NATS embedded vs TCP IPC for bridge transport

Current design uses bare TCP + NDJSON between parent and bridge (a private, binary-unsafe
protocol with no fan-out). NATS embedded offers a direct upgrade path:

**NATS embedded server** (`github.com/nats-io/nats-server/v2` as a library):

- Parent starts an in-process NATS server on a random port; bridge and Web UI connect as clients.
- Subjects: `bridge.ready`, `bridge.reply.<session>`, `parent.message.<session>`
- Fan-out at zero cost: logging, monitoring, and the Web UI subscribe independently.
- **Remote upgrade path:** swap `nats.Connect("nats://localhost:XXXX")` for
  `nats.Connect("nats://prod-server:4222")` — no architecture change.
- **Browser direct:** NATS WebSocket gateway (`github.com/nats-io/nats.go` WebSocket transport +
  `nats.js` in the browser) eliminates the SSE+HTTP relay entirely. Browser subscribes
  to `bridge.reply.*` directly.

Trade-offs: significant dependency weight (~30 MB binary growth), operational complexity
for embedded mode, Go module churn. Not "stdlib only".

**Lighter alternative — WebSocket IPC:** Replace TCP+NDJSON with a WebSocket listener.
Lighter than NATS; browser can subscribe directly; no new dependencies beyond stdlib.
No fan-out or remote capability.

Research needed: compare embedded NATS vs stdlib WebSocket IPC on binary size, latency,
and complexity. Evaluate whether "remote operation" (running the bridge on a server while
`ape` runs locally) is a real near-term requirement.

### 8. Claude Code hooks for passive event capture

Claude Code fires shell hooks on lifecycle events: `PreToolUse`, `PostToolUse`, `Stop`,
`SubagentStop`, `PreCompact`. These are configured in `.claude/settings.json`.

```json
{
  "hooks": {
    "PostToolUse": [{ "matcher": ".*", "hooks": [{ "type": "command", "command": "ape notify" }] }],
    "Stop":        [{ "hooks": [{ "type": "command", "command": "ape notify --event stop" }] }]
  }
}
```

`ape notify` reads the tool-event JSON from stdin and forwards it to the bridge via IPC/NATS.

**Use cases:**

- **Passive progress stream:** every `Bash`, `Write`, `Read`, `Agent` tool call appears in
  the Web UI without the skill needing to call `reply()`. Hooks and `reply` are complementary.
- **Stage timing:** `PreToolUse` + `PostToolUse` timestamps reconstruct per-tool latency.
- **Session end:** `Stop` hook fires when Claude stops; can trigger cleanup or summary capture.
- **`PreToolUse` blocking:** hooks can return a non-zero exit code to abort the tool call.
  Possible use: pause-gate enforcement (block a destructive tool until the user approves
  from the Web UI). Experimental — needs testing.

Research needed: exact JSON format hooks receive on stdin (field names, nesting);
which hooks receive tool input vs output vs both; whether `serve.go` can write
`.claude/settings.json` into tmpDir so hooks are session-scoped; whether `PreToolUse`
blocking is reliable in practice.

### 9. Session transcript capture

Claude Code interactive mode does not expose a native transcript API. Options by fidelity:

**a. Hooks (partial transcript):** `PreToolUse` + `PostToolUse` reconstruct the tool-use
trace. Misses Claude's reasoning text between tool calls, but captures all actions.
Combined with `reply` calls (which the bridge already logs), this covers the observable
behaviour of a pipeline run. Real-time; no post-processing needed.

**b. MCP bridge as transcript sink:** The bridge sees every `tools/call` request and response
for the tools it owns. Extend to log ALL MCP interactions (not just `await_message`/`reply`).
This gives a structured record of every bridge tool call with timestamps. Easy to add to
`bridge.go` with zero new dependencies.

**c. `--output-format json` or `--output-file`:** `claude -p` supports `--output-format json`
for structured output, but interactive mode may not. Check `claude --help` for interactive
output flags. Also check whether `~/.claude/projects/<hash>/` stores session transcripts.

**d. Conductor skill checkpoint:** The `apex-run-pipeline` skill calls `reply(stage_summary)`
after each stage. Bridge accumulates these into a structured run log. Coarse-grained but
clean — driven by the skill, no external capture needed.

**e. `script` / `tee` wrapper:** `script -c "claude ..."` captures terminal output including
TUI frames. High noise (ANSI codes, spinners), requires post-processing.

Recommendation: **a + b + d** together give real-time, structured, low-noise coverage.
Research: verify `.claude/` session log location and format; test `PostToolUse` JSON schema
in practice; confirm whether hooks fire inside sub-agent spawns (Agent tool calls).

## Phase Plan

| Phase | Deliverable                                                                           | Status        |
| ----- | ------------------------------------------------------------------------------------- | ------------- |
| PoC   | `claude_mcp_bridge_poc` — Web UI chat loop via `await_message` / `reply` MCP tools    | **validated** |
| 1     | `apex-run-pipeline` conductor skill (Agent tool, no `claude -p`)                      | unblocked     |
| 2     | `ape mcp-bridge` subcommand + Web UI server (uses PoC as foundation)                  | after PoC     |
| 3     | Pipeline-aware `reply` (progress events) + selective `await_message` (decision gates) | after plan    |

## PoC Scope (`claude_mcp_bridge_poc`) — Validated

Validated at `/home/diegos/_dev/github/diegosz/claude_mcp_bridge_poc/` (commit 4e542d0):

- Go binary as a standard MCP server with `await_message` + `reply` tools ✓
- Deferred-response pattern for `await_message` (async, non-blocking scanner) ✓
- Bidirectional Web UI ↔ Claude message flow (no channels, no special flags) ✓
- Loop initialisation via stdin bootstrap after bridge "ready" signal ✓
- Timeout handling (empty-string return, loop continues) ✓

Three implementation bugs found and fixed during validation:

1. **SSE headers not flushed** — `sseHandler` never called `WriteHeader`/`Flush` before
   the blocking select; browser EventSource stayed "connecting" forever.
2. **No bootstrap user turn** — `--system-prompt` alone does not auto-execute in interactive
   mode; Claude idles at the prompt. Fixed with `io.Pipe` stdin: write one user turn after
   bridge "ready", then pipe terminal through.
3. **MCP server not loading** — relying on `.mcp.json` auto-discovery in tmpDir is fragile.
   Fixed with `--mcp-config <path>` flag (a first-class Claude Code CLI flag).
