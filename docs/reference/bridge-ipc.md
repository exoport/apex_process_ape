# Bridge IPC wire schema

This document is the source of truth for the TCP NDJSON protocol
between the parent `ape` process and the bridge subprocess (`ape
mcp-bridge`). The transport is intentionally small so a future plan
can swap TCP+NDJSON for stdlib WebSocket or NATS-embedded without
touching the SSE broker, the MCP runtime, or the orchestrator.

PLAN-5 / C3.

## Layout

- One TCP listener on `127.0.0.1:<random-free-port>`, owned by the
  parent process. The port is published to subprocesses via two env
  vars:
  - `APE_IPC_PORT` — used by `ape mcp-bridge` (declared in the
    inline `--mcp-config` blob).
  - `APE_BRIDGE_PORT` — used by `ape notify` (declared inline in
    each hook `command` via `env(1)`).
- Both vars resolve to the same listener. The orchestrator demuxes
  connections by first-frame type: the first connection sending
  `{"type":"ready"}` is promoted to "the bridge"; every other
  connection is a one-shot `ape notify` writing a single
  `{"type":"hook",...}` frame.
- The listener binds **127.0.0.1 only**. `0.0.0.0` and unspecified
  binds are rejected at construction time (see
  `internal/bridge/broker.Listen` for the matching invariant on the
  HTTP listener).

## Frame format

One JSON object per line, terminated with `\n`. Encoding is UTF-8.
Lines that fail to parse are skipped silently. The total scanner
buffer is 256 KB per line.

Source: `internal/bridge/ipc/ipc.go`.

## Frame types

| `type`            | Direction             | Required fields                  | Notes                                                                    |
| ----------------- | --------------------- | -------------------------------- | ------------------------------------------------------------------------ |
| `ready`           | bridge → parent       | —                                | Handshake. Unblocks the io.Pipe bootstrap goroutine.                     |
| `message`         | parent → bridge       | `content`                        | Wakes a pending `await_message`, or fills the FIFO if none pending.      |
| `reply`           | bridge → parent       | `content`                        | Skill called `reply()`. Broker emits an SSE `reply` event.               |
| `call`            | bridge → parent       | `tool`, `id`                     | Mirror of every MCP tool call. Carries `params` + `result` raw JSON.     |
| `hook`            | `ape notify` → parent | `event`, `session_id`, `payload` | Forwarded hook envelope. `agent_id` for Subagent\* events.               |
| `step-bind`       | parent → bridge       | `session_id`, `step`             | Tells the bridge which step a session id belongs to so calls are tagged. |
| `stop`            | parent → bridge       | —                                | Sent before SIGTERM. The bridge can flush state if it needs to.          |
| `buffer-overflow` | bridge → parent       | `content`                        | Buffer-5 overflow notification. `content` is the dropped message body.   |

### `await_message` deferred-call mirror

`tools/call await_message` produces two `call` frames so the parent
can pair them by `id`:

1. Deferred-entry: `{"type":"call","tool":"await_message","params":{"deferred":true},"id":"..."}`
2. Flush: `{"type":"call","tool":"await_message","params":{"flush":true},"result":{"text":"...","timeout":false},"id":"..."}`

The flush's `result.timeout: true` means the await expired with no
inbound message (the bridge respond with empty text). The orchestrator
emits `await-pending` SSE on the deferred-entry and `await-resolved`
on the flush.

## Timing contracts

- `ready` handshake: parent waits up to 30 s for it before writing
  the bootstrap user-turn anyway (`orchestrator.Options.ConsumeBridgeReadyMs`
  overrides for tests).
- `await_message` default timeout: **240 s** (under the 5-minute
  prompt-cache TTL). Override via `timeout_seconds` MCP parameter.
- `ape notify` dial timeout: **200 ms**. Write deadline: **500 ms**.
  All failure modes exit 0 so the tool loop never stalls.
- IPC reader buffer: 256 KB. Hook envelopes carrying long
  `tool_input.command` values truncate at the broker render layer,
  not on the wire.

## Migration triggers

PLAN-5 design doc §7 lists when to swap NDJSON for something else:

- Fan-out (multiple consumers of one event stream).
- Remote operation (browser on a different machine than ape).
- NATS-style durable replay.

Until one of those is a concrete deliverable, NDJSON stays.
