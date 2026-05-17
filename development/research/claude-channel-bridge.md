# Claude Channel Bridge

> **Superseded — 2026-05-16.**
> The Claude Code channels protocol is platform-gated and not self-serve activatable.
> Confirmed unavailable on Claude Team and Claude Max plans after enabling the org toggle
> and doing a full logout/login (Claude Code v2.1.143). Channels require Anthropic backend
> provisioning that is not publicly accessible during the research preview.
>
> The channel bridge architecture is replaced by a standard-MCP blocking-tool approach
> that achieves the same bidirectional communication without any special provisioning.
> See [`claude-mcp-bridge.md`](claude-mcp-bridge.md).
>
> This document is kept for record of the investigation and the channel protocol details,
> which remain valid for when/if channels become generally available.

---

## Problem

`ape pipeline` currently spawns one `claude -p "/{skill} --autonomous ..."` subprocess per pipeline step. Each invocation opens a fresh Claude session billed as an independent API call. A six-stage design pipeline creates six separate billing events, and every step starts from a cold context with no shared state from prior steps.

## Options Investigated

### Option A — Conductor skill (Agent tool, no `claude -p`)

A new `apex-run-pipeline` skill reads `_apex/pipelines/*.yaml` and orchestrates steps using Claude's native **Agent tool** — the same pattern already used by `apex-story-batch-dev`, `apex-story-batch-create`, and `apex-lift-project`. Each step is a foreground sub-agent (synchronous, blocks until complete). The parent skill captures a compact YAML summary per step, discards verbose output, and owns all git commits.

- No `claude -p` anywhere. One interactive Claude session drives the full pipeline.
- Sub-agents inherit the parent's context. Steps can observe accumulated state.
- Proven pattern: `apex-lift-project` already spawns 10+ sequential sub-agents.
- **Constraint:** sub-agents cannot spawn further sub-agents (one nesting level). Existing pipeline YAMLs (design, governance, epics) contain only leaf skills — not batch orchestrators — so this constraint does not affect current pipelines.

### Option B — `ape` as MCP server

`ape` gains `ape mcp-serve`, exposing `report_progress` and `await_decision` tools. The pipeline skill drives steps and calls these tools between sub-agent spawns. Keeps the TUI. Requires significant new surface in `ape`.

### Option C — Relay skill (thin wrapper per step)

`ape` calls a relay skill that builds the prompt string. Still uses `claude -p` internally. Low value-add.

### Option D — Skill + IPC progress protocol

Skill runs steps, writes progress to a named pipe. `ape` TUI reads the pipe. Requires a new IPC protocol and concurrent process management.

**Selected: Option A** eliminates the billing problem completely. Options B–D are TUI/UX enhancements that can layer on top.

## Architecture

### Phase 1 — `apex-run-pipeline` conductor skill

```
User: /apex-run-pipeline --pipeline design --autonomous
  │
  ├── reads _apex/pipelines/design.yaml
  ├── preflight: checks requires.files exist
  │
  └── for each stage → for each step in chain:
        Agent tool (foreground, model: sonnet)
          if step.agent: /{agent} --autonomous -- {skill} --autonomous --pipeline-summary
          else:          read {skill}/SKILL.md inline with --autonomous --pipeline-summary
        ← compact YAML summary returned
        ← on failure: record, continue (or abort)
  │
  └── boundary commit: "pipeline: design"
```

The `--pipeline-summary` flag signals to each skill that it is running as a pipeline step and should emit a compact return block instead of prose. The orchestrator appends this flag automatically; skill authors opt in by handling it. Skills that do not recognise the flag ignore it gracefully.

Return contract per step:

```yaml
skill: apex-create-prd
status: pass | fail
artifact: development/planning/prd.md
key_decisions: [...]
error: null | "..."
```

### Phase 2 — Channel bridge for Web UI feedback

Rather than replacing `ape`'s TUI, this phase adds a browser-based Web UI that shows real-time pipeline progress and supports interactive decisions — without requiring `claude -p`.

#### How it works

Claude Code **channels** (research preview, Claude Code ≥ v2.1.80) allow a local MCP server to push events into a running Claude session and receive replies back. The channel server is spawned as a stdio subprocess by Claude Code; all communication is local (no cloud routing).

```
ape (parent process)
├── Web UI HTTP server          :8787   browser ↔ WebSocket/SSE
├── IPC TCP listener            :XXXXX  bridge ↔ parent
│
└── subprocess: claude --dangerously-load-development-channels server:ape-bridge
                  (interactive, no -p)
                    │
                    └── subprocess (from .mcp.json): ape channel-bridge
                          ├── MCP JSON-RPC over stdio  ←→ Claude Code
                          └── TCP newline-JSON          ←→ ape parent
```

#### Startup sequence (no `claude -p`)

1. `ape pipeline design` starts IPC listener, Web UI server, writes `.mcp.json`
2. Execs `claude --dangerously-load-development-channels server:ape-bridge` (interactive mode)
3. Claude Code spawns `ape channel-bridge` from `.mcp.json`
4. Bridge completes MCP initialisation, then sends `{"type":"ready"}` to parent over IPC
5. Parent receives `ready`, injects setup instruction via channel notification
6. Claude receives `<channel source="ape-bridge">` event, starts `/apex-run-pipeline`
7. Skill runs — all steps via Agent tool, no `claude -p`
8. After each step the skill calls the channel `reply` tool with a progress event
9. Bridge forwards reply to parent over IPC → parent broadcasts to Web UI via SSE
10. User decisions in the Web UI → parent → IPC → bridge → channel notification → skill reacts

#### Data flow summary

| Direction                   | Transport                                                                                      |
| --------------------------- | ---------------------------------------------------------------------------------------------- |
| Claude → Web UI (progress)  | skill calls `reply` tool → bridge IPC → parent → SSE → browser                                 |
| Web UI → Claude (decisions) | browser POST → parent → IPC → bridge → `notifications/claude/channel` → `<channel>` in context |
| Claude ↔ bridge             | MCP JSON-RPC 2.0, newline-delimited, stdio                                                     |
| Bridge ↔ parent             | TCP, newline-delimited JSON (`{"type":"...","content":"..."}`)                                 |
| Parent ↔ browser            | HTTP + SSE (`GET /api/events`), POST (`/api/send`)                                             |

#### Cloud traffic

None. Claude Code and the channel bridge communicate over stdio (local subprocess). The IPC socket and Web UI are localhost-only. The only Anthropic traffic is the normal Claude inference (API key auth), which exists regardless.

## Protocol Details

### MCP wire format (stdio)

Newline-delimited JSON (NDJSON). Each message is one complete JSON object followed by `\n`. No content-length framing (MCP stdio ≠ LSP).

### Channel capability declaration

The bridge declares this capability in the `initialize` response to be recognised as a channel:

```json
{
  "capabilities": {
    "experimental": { "claude/channel": {} },
    "tools": {}
  }
}
```

### Channel notification (bridge → Claude)

```json
{
  "jsonrpc": "2.0",
  "method": "notifications/claude/channel",
  "params": { "content": "message text", "meta": {} }
}
```

Claude receives this as `<channel source="ape-bridge">message text</channel>` injected into context.

### Reply tool (Claude → bridge)

Claude calls the `reply` MCP tool. Bridge handles `tools/call` with `name: "reply"`, extracts `arguments.content`, forwards to parent over IPC.

### `.mcp.json` registration

```json
{
  "mcpServers": {
    "ape-bridge": {
      "command": "/absolute/path/to/ape",
      "args": ["channel-bridge"],
      "env": { "APE_BRIDGE_PORT": "34567" }
    }
  }
}
```

Claude Code invocation:

```bash
claude --dangerously-load-development-channels server:ape-bridge
```

## Go Implementation

The channel bridge is standard MCP over stdio. No JavaScript SDK required; the protocol is language-agnostic JSON-RPC 2.0. Go options:

| Library                                  | Notes                                                             |
| ---------------------------------------- | ----------------------------------------------------------------- |
| `github.com/modelcontextprotocol/go-sdk` | Official SDK, maintained with Google. Recommended for production. |
| `github.com/mark3labs/mcp-go`            | Widely used community library, good Claude Code track record.     |
| stdlib only                              | Viable for the PoC (~150 lines). No external deps.                |

`ape` currently has no MCP infrastructure (clean slate). The `channel-bridge` subcommand is a new ~150-line file using the official Go SDK.

## IPC Message Protocol (parent ↔ bridge)

Newline-delimited JSON over TCP.

```
bridge → parent:
  {"type":"ready"}                        bridge fully initialised
  {"type":"reply","content":"..."}        Claude called reply tool

parent → bridge:
  {"type":"inject","content":"..."}       push message into Claude context
  {"type":"shutdown"}                     graceful teardown
```

## Phase Plan

| Phase | Deliverable                                                                   | Removes `claude -p`? |
| ----- | ----------------------------------------------------------------------------- | -------------------- |
| PoC   | `claude_channel_bridge_poc` — minimal Web UI ↔ interactive Claude via channel | — (validation only)  |
| 1     | `apex-run-pipeline` conductor skill                                           | ✅                   |
| 2     | `ape channel-bridge` subcommand + Web UI server                               | ✅ (TUI/Web UI)      |
| 3     | Interactive decisions via Web UI (await_decision pattern)                     | ✅                   |

The PoC (see `github.com/diegosz/claude_channel_bridge_poc`) validates the MCP channel plumbing, the `.mcp.json` registration syntax, and the bidirectional Web UI ↔ Claude message flow before any pipeline logic is involved.

## Open Items for the Plan

- **Channels are platform-gated — not self-serve (confirmed hard blocker as of 2026-05-16).** Tested on Claude Code v2.1.143 across two machines and two plan types (Claude Team / EXO org, Claude Max individual). In both cases `--channels` and `--dangerously-load-development-channels` are silently ignored with "Channels are not currently available". Enabling the org toggle at `claude.ai → Admin settings → Claude Code → Channels` and doing a full logout/login had no effect. The research preview requires Anthropic to provision access on their backend; the admin UI toggle is a pre-authorization signal, not a self-serve activation. **Phase 2 (channel bridge) is blocked until Anthropic provisions the org.** File a request at `github.com/anthropics/claude-code/issues`. Phase 1 (`apex-run-pipeline` conductor skill) is fully independent and unblocked.
- Confirm `--dangerously-load-development-channels server:<name>` works with a Go binary once provisioning is granted (PoC `mock-claude` mode validates the bridge code in the meantime).
- Decide whether `apex-run-pipeline` reads pipeline YAML from `_apex/pipelines/` or accepts an inline spec.
- Define the `--pipeline-summary` return contract per skill category (collaborative vs. single-output).
- Decide fail-fast vs. continue-on-error policy per pipeline.
- Context budget ceiling: estimate token cost of N compact summaries for the longest pipeline (governance, 8 steps).
