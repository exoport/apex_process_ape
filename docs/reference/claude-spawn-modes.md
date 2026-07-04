# Reference — how ape spawns `claude`

Since v0.0.36 (PLAN-9 F2), ape has a single way to drive `claude`: an **interactive REPL inside an in-process PTY**. The programmatic `claude -p` path and its flags (`-P` / `--programmatic`, `-I` / `--interactive`, `--eval`) were removed — see [why-pty-only.md](../explanation/why-pty-only.md). ape never passes `-p`.

- **`ape pipeline`** — one long-lived `claude` REPL per stage running inside a per-stage in-process PTY (`internal/repl/`, `github.com/aymanbagabas/go-pty`). Prompts are typed into the PTY as real REPL keystrokes by writing bytes to the master end + Enter. claude's CLI parses slash commands the normal way, the skill loads, the model executes. The bridge's `Stop` hook signals step-done.
- **`ape task`** — same interactive PTY, driving a single skill instead of a stage chain.
- **`ape chat`** — one long-lived `claude` REPL where claude inherits ape's stdio (the user's terminal is its controlling PTY); the user types directly into claude.

This page is the lookup table for how each command delivers prompts.

## Matrix

| Invocation                        | UI     | Prompt delivery                                                                   |
| --------------------------------- | ------ | --------------------------------------------------------------------------------- |
| `ape pipeline <name>` _(default)_ | tui    | PTY Write of `<prompt>` + Enter into per-stage in-process PTY                      |
| `ape pipeline <name> --tui`       | tui    | same as default                                                                   |
| `ape pipeline <name> --web`       | web    | PTY Write; web UI mirrors via bridge SSE                                           |
| `ape pipeline <name> --no-tui`    | none   | PTY Write; plain stdout                                                            |
| `ape task <skill>`                | none   | PTY Write; result envelope / progress via stdout (see `--output-format`)          |
| `ape chat`                        | (term) | user types directly into claude; claude inherits ape's stdio (terminal = its PTY) |

## The rule

```
Every claude spawn is an interactive REPL attached to a PTY. ape never passes -p.
```

`ape pipeline` and `ape task` allocate the PTY in-process via `internal/repl/`; `ape chat` lets claude inherit the user's terminal as its controlling PTY.

## Mutual exclusion (`ape pipeline` errors with exit 2)

- `--tui --web`, `--tui --no-tui`, `--web --no-tui` — only one UI selector at a time.
- Any of the removed exec flags (`-P` / `--programmatic`, `-I` / `--interactive`, `--eval`) — errors with the removal message pointing at [why-pty-only.md](../explanation/why-pty-only.md).

## Bridge role

The MCP bridge (`internal/bridge/orchestrator/`) stays wired for **hook observability** (`UserPromptSubmit`, `Stop`, `PreToolUse`, `PostToolUse`, `SubagentStart`, `SubagentStop`) in every mode. Under `--web` it additionally carries prompt/reply traffic for the browser via `await_message` / `reply`.

## Source of truth in code

| What                                              | Where                                                      |
| ------------------------------------------------- | ---------------------------------------------------------- |
| UI-flag → mode resolution                         | `internal/apecmd/pipeline_modes.go`                        |
| Per-stage PTY + `claude` spawn (interactive path) | `internal/apecmd/pipeline_interactive.go`                  |
| PTY driver (NewSession / SendCommand / …)         | `internal/repl/`                                           |
| `ape chat` direct exec with stdio inheritance     | `internal/apecmd/chat.go`                                  |

## Related

- [invocation-matrix.md](invocation-matrix.md) — the UI axis (`tui` default, `web`, `no-tui`).
- [step-contract.md](step-contract.md) — what the runner types into the REPL between steps.
- [../explanation/why-pty-only.md](../explanation/why-pty-only.md) — why PTY is the only exec mode.
- [../explanation/exec-modes.md](../explanation/exec-modes.md) — the per-stage interactive runtime in depth.
- [bridge-ipc.md](bridge-ipc.md) — wire schema for the MCP bridge.
