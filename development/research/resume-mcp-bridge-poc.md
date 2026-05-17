# Continuation Prompt — MCP Bridge Deep Research

Use this prompt to resume work after a context reset.

---

## Prompt

We are building `apex_process_ape` — a Go CLI tool at `/home/diegos/_dev/exoar/apex_process_ape/`
that runs APEX skill pipelines. The MCP bridge PoC is complete and validated.

### What was built and validated

`claude_mcp_bridge_poc` at `/home/diegos/_dev/github/diegosz/claude_mcp_bridge_poc/` is a
working Go binary (stdlib only) that:

- Runs an MCP server with two tools: `await_message` (deferred response, non-blocking) and
  `reply` (IPC write → SSE → browser).
- Starts Claude in interactive mode with `--mcp-config <path>` + `--system-prompt` + stdin
  bootstrap (writes a user turn after bridge signals "ready" so Claude enters the loop).
- Serves a Web UI on `:8787` (SSE + POST). The browser sends messages; Claude responds;
  replies appear in the browser. Bidirectional, no channels, works on any Claude plan.

Three bugs found during validation (all fixed, see `4e542d0`):

1. SSE `sseHandler` never flushed headers before blocking → "connecting" forever.
2. `--system-prompt` alone doesn't auto-execute in interactive mode → idle at prompt. Fixed with `io.Pipe` stdin bootstrap.
3. `.mcp.json` auto-discovery in tmpDir is unreliable → use `--mcp-config <path>` explicitly.

Full design: `/home/diegos/_dev/github/diegosz/apex_process_ape/development/research/claude-mcp-bridge.md`

### Research documents

- `claude-channel-bridge.md` — channels investigation (superseded; channels unavailable on Team/Max)
- `claude-mcp-bridge.md` — MCP blocking-tool design (current, updated with new questions)

---

## Your Task

Do deep research on the nine open design questions below. For each one, produce a short
research note (2–4 paragraphs or a comparison table) with a concrete recommendation for
the `ape` production implementation. Where code or config is needed to verify a claim,
write it. Where you need to check Claude Code CLI flags or behaviour, run `claude --help`
or test with the PoC binary.

After the research, update `claude-mcp-bridge.md` with your findings (replace each
"Research needed" paragraph with conclusions), and write a new continuation prompt at
`resume-mcp-bridge-plan.md` that summarises the decisions made and is ready to hand off to
a planning session for `plan-5_apex-run-pipeline.md`.

---

## Nine Open Design Questions

### 1. Timeout behaviour (original)

When `await_message` returns `""` (timeout), Claude must decide what to do.

- **A — Loop silently** (current PoC): check empty string, call `await_message()` again.
- **B — Exit loop:** treat timeout as session-end.
- **C — Heartbeat reply:** call `reply("⏳ still here")` on timeout.
- **D — Configurable:** `await_message` accepts `on_timeout: "loop"|"exit"|"heartbeat"`.

Research: token cost of option A per cycle at default 300s timeout; whether D is worth the
added tool-schema complexity; what the pipeline skill (non-looping) needs vs the chat bridge.

### 2. Pipeline skill bridge detection (original)

`apex-run-pipeline` won't loop on `await_message` continuously. It calls `reply` after each
stage and calls `await_message` only at explicit pause/decision gates. The skill must
gracefully degrade when no bridge is running (plain terminal, no Web UI).

Research: does checking `tools/list` at skill startup for `await_message` reliably detect
bridge presence? What is the latency of `tools/list` in practice? Is there a lighter signal
(e.g., an env var set by `ape serve` that the skill can read)?

### 3. Phase 1 plan scope (original)

Once research is done, write `plan-5_apex-run-pipeline.md` in
`/home/diegos/_dev/github/diegosz/apex_process_ape/development/planning/`.

The plan covers:

- `apex-run-pipeline` conductor skill: Agent tool pattern (same as `apex-story-batch-dev`,
  `apex-lift-project`), replaces per-step `claude -p` calls.
- `--pipeline-summary` flag contract.
- Fail-fast policy: what happens when a stage fails mid-pipeline.
- Context budget: how many stages before context pressure, whether to use sub-agents.
- Phase 2 bridge integration: how the skill calls `reply` and `await_message` when
  the bridge is present.

Existing plans are `plan-1` through `plan-4` (all done). The planning index is at
`development/planning/index.md`.

### 4. Web front-end — HTMX vs Alpine.js vs vanilla JS (new)

Current PoC: vanilla JS, EventSource + fetch, JSON API, no dependencies.

Research:

- **HTMX + `hx-ext="sse"`:** server renders HTML fragments per event; `hx-swap` inserts them.
  Does `hx-sse` auto-reconnect on SSE drop? How does it compose with out-of-order stage updates
  (multiple stages updating in parallel)? What does the Go template side look like for a
  stage-progress card?
- **Alpine.js:** reactive HTML attributes, client-side state, JSON API unchanged.
  Better for rich client interactions (expandable log panels, per-stage badges).
- **Vanilla JS (keep):** zero dependencies, sufficient for the PoC and simple UIs.

Produce a recommendation with a rationale. If HTMX: sketch the SSE event → fragment model.
If Alpine.js: sketch the event handler and DOM binding model.

### 5. NATS embedded vs stdlib WebSocket IPC (new)

Current IPC: bare TCP listener in parent, bridge dials in, NDJSON messages.
Works locally. No fan-out. No remote capability.

Research two alternatives:

**A — NATS embedded** (`nats-server` as a Go library):

- What is the binary size impact of `github.com/nats-io/nats-server/v2` + `nats.go`?
- Can NATS run fully embedded (no external process) in a Go binary?
- How does the remote upgrade path work: swap `nats.Connect("nats://localhost:XXXX")` for
  `nats.Connect("nats://remote:4222")`?
- Can a browser subscribe directly via NATS WebSocket + `nats.js`, eliminating SSE?
- Is NATS JetStream needed for the pipeline use case (persistent stage log replay)?

**B — stdlib WebSocket IPC:**

- Replace TCP+NDJSON with a Go `net/http` WebSocket upgrade (using
  `golang.org/x/net/websocket` or a thin wrapper — still no external dep for stdlib).
  Actually: Go's `net/http` does NOT have a built-in WebSocket — would need
  `golang.org/x/net` or a vendored implementation.
- Lighter than NATS; browser can subscribe directly; still no fan-out.

Produce a comparison table (binary size, dependencies, fan-out, remote, browser-direct,
complexity) and a recommendation.

### 6. Claude Code hooks — format, availability, and use cases (new)

Claude Code fires shell hooks on lifecycle events. Research the exact hook API:

- What events are available? (`PreToolUse`, `PostToolUse`, `Stop`, `SubagentStop`,
  `PreCompact` — confirm the full list and which are stable vs experimental.)
- What JSON does each hook receive on stdin? Reproduce the exact schema by running a test
  session with a hook that writes its stdin to a file.
- Does `PostToolUse` receive both the tool input AND output, or only input?
- Does `PreToolUse` allow blocking a tool call (non-zero exit code)? Is this reliable?
- Do hooks fire inside sub-agent spawns (Agent tool calls spawned by the conductor skill)?
- How to configure hooks from `serve.go`: write `.claude/settings.json` into tmpDir
  alongside `.mcp.json`. What is the settings.json schema for hooks?

Concrete deliverable: a working hook config snippet + `ape notify` stub that reads stdin
and writes the event JSON to a file (for verification), plus a recommendation on whether
hooks replace or complement the `reply` / `await_message` pattern.

### 7. Session transcript capture (new)

Research every mechanism for capturing a Claude interactive session transcript:

- **`~/.claude/` session logs:** does Claude Code write session data to disk after a session?
  Check `~/.claude/projects/`, `~/.claude/sessions/`, or similar. What format?
- **`--output-format json` in interactive mode:** `claude -p` supports JSON output. Does
  interactive mode support any structured output flag? Check `claude --help` exhaustively.
- **Hooks as transcript source:** `PreToolUse` + `PostToolUse` + `Stop` reconstruct the
  tool-use trace. What is missing (Claude's reasoning text between tool calls)?
- **Bridge as transcript sink:** extend `bridge.go` to log every MCP `tools/call` request
  - response with timestamps. How complete is this for the pipeline use case?
- **Conductor skill checkpoint:** `apex-run-pipeline` calls `reply(stage_summary)` after
  each stage. Bridge accumulates into a structured run log. How does this compare to hooks?

Produce a recommendation for: (a) real-time per-tool event streaming, (b) per-stage
summary capture, (c) full session transcript at end of run. Different mechanisms may be
best for each.

---

## Context References

| Path                                                                                          | What                                                    |
| --------------------------------------------------------------------------------------------- | ------------------------------------------------------- |
| `/home/diegos/_dev/github/diegosz/claude_mcp_bridge_poc/`                                     | Validated PoC source                                    |
| `/home/diegos/_dev/github/diegosz/apex_process_ape/`                                          | Production ape repo                                     |
| `/home/diegos/_dev/github/diegosz/apex_process_ape/development/research/claude-mcp-bridge.md` | Design doc (updated)                                    |
| `/home/diegos/_dev/github/diegosz/apex_process_ape/development/planning/`                     | Planning dir (plan-1 through plan-4 done)               |
| `/home/diegos/_dev/github/diegosz/apex_process_framework/framework/_claude/skills/`           | Framework skills (reference for conductor skill design) |
