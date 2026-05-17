# Bridge security model

`ape chat` and `ape pipeline` web mode bind a local HTTP server on
`127.0.0.1:<random-free-port>`. This document describes the threat
model and the limits of the v1 implementation.

PLAN-5 / C3.

## Bind contract

- The broker HTTP listener and the IPC TCP listener both bind to
  **127.0.0.1 only**. `0.0.0.0`, `::1` (unless explicitly mapped),
  and unspecified binds are rejected at construction time. This is a
  hard invariant — see `internal/bridge/broker.Listen` for the
  enforcement.
- Random free port allocation per session via `net.Listen("tcp", "127.0.0.1:0")`.
  Cross-project sessions are tracked in `~/.ape/registry.json` so
  `ape sessions` can list and reopen them.

## What the broker does not have

- **No bearer token.** Any process running as the current user on
  the same machine can hit `/api/send`, `/api/stop`, and read the
  SSE stream at `/api/events`.
- **No CSRF protection.** The browser-facing surface accepts unsigned
  JSON `POST`s under the assumption that the only origin reaching
  the bind is a user-trusted local browser.
- **No authentication.** Same reason.

## Accepted threats

| Threat                                                                 | Status       | Reason                                                                                                                                |
| ---------------------------------------------------------------------- | ------------ | ------------------------------------------------------------------------------------------------------------------------------------- |
| Another process under the same uid hitting `/api/send` / `/api/stop`   | **Accepted** | Localhost-only bind. A malicious process under the same uid can already read every file the user can; sending text is a smaller risk. |
| Network attacker on the local LAN                                      | Mitigated    | 127.0.0.1 binding rejects non-loopback connections at the socket level.                                                               |
| Browser tab cached after close                                         | Acknowledged | EventSource auto-reconnect is the only persistence. Closing the browser does not kill the run; reopening reconnects (no backlog).     |
| Hook subprocess (`ape notify`) reaching the bridge from another origin | Accepted     | Same uid, same machine. The hook envelope's session id is trusted but not authenticated — a future plan can sign it if needed.        |
| Stop button mis-fire                                                   | Mitigated    | `hx-confirm` guards the Stop button. SIGTERM (not SIGKILL) gives `claude` time to flush.                                              |

## Do-not-run-this-on environments

- Shared-account hosts where one Unix user is used by multiple
  humans (jumpboxes, CI bastions).
- Multi-user X11 sessions where `xdg-open` of the bridge URL would
  open someone else's browser.
- Containers exposing 127.0.0.1 to the host network namespace
  (rare, but `docker run --network host` is one example).

If your environment matches any of those, a future PLAN-5 follow-up
plan can layer in per-session bearer tokens. The IPC abstraction is
small enough that adding a `Authorization: Bearer <token>` header on
SSE / POST handlers is a one-package change.

## Reading further

- `docs/reference/bridge-ipc.md` — wire schema between parent and
  bridge subprocess.
- `internal/bridge/broker/broker.go` — HTTP handlers, MaxBytesReader
  cap (64 KB on `/api/send`).
- `internal/bridge/orchestrator/session.go` — SIGTERM target is the
  process group on Unix, `Process.Kill()` on Windows.
