# Bridge security model

`ape task`, `ape prompt` and `ape chat` each bind a local IPC listener on
`127.0.0.1:<random-free-port>` for the session they drive. So does everything
that runs through them: `ape change review --run`, and the `RunTask` and
`RunPrompt` calls of an `ape script`. This page describes what that listener
accepts, the threat model, and its limits.

PLAN-5 / C3.

## Bind contract

- The listener is TCP on **127.0.0.1 only**, on a random free port
  (`127.0.0.1:0`); see `internal/bridge/orchestrator/runtime.go`
  (`BridgeRuntime.Listen`). No HTTP server is started: ape serves no web page,
  no SSE stream and no `/api/*` endpoint.
- Two kinds of local process connect to it, both started by the spawned
  `claude`:
  - the `mcp-bridge` MCP server (`ape mcp-bridge`), which dials the port it
    gets as `APE_IPC_PORT`;
  - the hook command `env APE_BRIDGE_PORT=<port> ape notify --event <event>`,
    which forwards each Claude Code hook envelope to the listener and exits 0
    whatever happens, so a missing listener never stalls the tool loop.
- The wire format is NDJSON frames, described in [bridge-ipc.md](bridge-ipc.md).
- Live sessions are tracked in `~/.ape/registry.json` (`ape sessions` lists
  them).

## What the listener does not have

- **No authentication and no token.** Any process running as the current user
  on the same machine can connect to the port and send frames.
- **No signature on hook envelopes.** The session id in a hook envelope is
  trusted, not verified.

## Accepted threats

| Threat | Status | Reason |
| --- | --- | --- |
| Another process under the same uid connecting to the port and sending frames | **Accepted** | Loopback-only bind. A malicious process under the same uid can already read every file the user can. |
| Network attacker on the local LAN | Mitigated | The 127.0.0.1 bind refuses non-loopback connections at the socket level. |
| A forged hook envelope (`ape notify`) from another process | Accepted | Same uid, same machine. The durable record is the run's `hook-events.jsonl`, which ape writes on its own side. |

## Do-not-run-this-on environments

- Shared-account hosts where one Unix user is used by several people
  (jumpboxes, CI bastions).
- Containers exposing 127.0.0.1 to the host network namespace
  (`docker run --network host` is one example).

## Inside a sandbox workspace

When ape runs *inside* an `ape sandbox` Kata VM workspace (PLAN-16), the
loopback-only bind gets stronger, not weaker: `127.0.0.1` is the workspace VM's
own network namespace, so "another process under the same uid" means another
process **inside that VM** — a hardware boundary (own guest kernel, KVM)
separates it from the host. The "containers exposing 127.0.0.1 to the host
network namespace" caveat above does not apply — a Kata workspace has its own
kernel and netstack, not a shared host one.

Public egress from the workspace is governed separately by the
deny-by-default CONNECT proxy + domain allowlist (`network.authorized_domains`),
audited to `egress-audit.jsonl`. The two compose: the bridge is a private
loopback surface inside the VM; the proxy is the public-egress gate.

See [How to run a sandboxed Kata VM workspace](../how-to/sandbox-workspaces.md)
and the [sandbox profile reference](sandbox-profile.md). Kata workspaces are
Phase 1 of the APEX Process Platform; later phases (Netbird overlay
networking, in-VM NATS workers) live in the `apex_process_platform` repo.

## Reading further

- [bridge-ipc.md](bridge-ipc.md) — the wire schema between ape and the
  processes that connect to the listener.
- `internal/bridge/orchestrator/runtime.go` — the listener and frame routing.
- `internal/bridge/mcp/bridge.go` — the `mcp-bridge` MCP server.
- `internal/apecmd/notify.go` — the hook forwarder.
