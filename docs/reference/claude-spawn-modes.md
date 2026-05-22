# Reference — when ape uses `claude -p`

Every ape command that talks to claude does so in one of two shapes:

- **Programmatic** — one `claude -p "<prompt>"` subprocess per step. The process exits when claude finishes responding. No REPL, no slash commands, no PTY.
- **Interactive** — one long-lived `claude` REPL per stage running inside an in-process PTY (`internal/repl/`, `github.com/aymanbagabas/go-pty`). Prompts are typed into the PTY as real REPL keystrokes by writing bytes to the master end + Enter. claude's CLI parses slash commands the normal way, the skill loads, the model executes. The bridge's `Stop` hook signals step-done.

This page is the lookup table for which mode each command + flag combination uses.

## Matrix

| Invocation                                  | UI     | Exec         | `claude -p`? | Prompt delivery                                                                     |
| ------------------------------------------- | ------ | ------------ | ------------ | ----------------------------------------------------------------------------------- |
| `ape pipeline <name>` _(default)_           | tui    | interactive  | **NO**       | PTY Write of `<prompt>` + Enter into per-stage in-process PTY                       |
| `ape pipeline <name> --tui`                 | tui    | interactive  | **NO**       | same as default                                                                     |
| `ape pipeline <name> --interactive` / `-I`  | (UI)   | interactive  | **NO**       | same as default                                                                     |
| `ape pipeline <name> --web`                 | web    | interactive  | **NO**       | PTY Write; web UI mirrors via bridge SSE                                            |
| `ape pipeline <name> --no-tui`              | none   | interactive  | **NO**       | PTY Write; plain stdout                                                             |
| `ape pipeline <name> -P` / `--programmatic` | (UI)   | programmatic | **YES**      | `claude -p "<prompt>"` per step, fresh process                                      |
| `ape pipeline <name> --tui -P`              | tui    | programmatic | **YES**      | TUI panels + per-step `claude -p`                                                   |
| `ape pipeline <name> --no-tui -P`           | none   | programmatic | **YES**      | plain stdout + per-step `claude -p`                                                 |
| `ape pipeline <name> --web -P`              | web    | programmatic | **YES**      | PLAN-5 web shape: per-step `claude -p` + bridge for hooks + `await_message`/`reply` |
| `ape pipeline <name> --eval`                | none   | programmatic | **YES**      | locked PLAN-5 byte-equivalent; per-step `claude -p`                                 |
| `ape chat`                                  | (term) | interactive  | **NO**       | user types directly into claude; claude inherits ape's stdio (terminal = its PTY)   |

## The rule

```
claude -p  ⇔  Exec axis = programmatic  ⇔  -P / --programmatic / --eval
```

Anything **interactive** (the default for `ape pipeline`; the only mode for `ape chat`) spawns claude attached to a PTY and never passes `-p`. Pipeline interactive mode allocates the PTY in-process via `internal/repl/`; `ape chat` lets claude inherit the user's terminal as its controlling PTY.

## Mutual exclusion (`ape pipeline` errors with exit 2)

- `--tui --web` together — multiple UI flags.
- `--eval` with any of `--interactive`, `-P`, `--tui`, `--web`, `--no-tui` — `--eval` admits no modifier.
- `--interactive` and `-P` together — mutually exclusive exec choices.

## Bridge role by mode

The MCP bridge (`internal/bridge/orchestrator/`) stays wired in most modes, but its job changes:

| Mode                          | Bridge used for                                                                              |
| ----------------------------- | -------------------------------------------------------------------------------------------- |
| interactive (any UI)          | Hook observability only (`UserPromptSubmit`, `Stop`, `PreToolUse`, `PostToolUse`, …)         |
| programmatic + `--web -P`     | Hook observability **plus** prompt delivery via `await_message` / `reply` (PLAN-5 web shape) |
| programmatic + `-P` (non-web) | Hook observability optional; no `await_message`                                              |
| `--eval`                      | Bridge not wired at all (byte-equivalence lock for the eval consumer)                        |

## Source of truth in code

| What                                              | Where                                                      |
| ------------------------------------------------- | ---------------------------------------------------------- |
| Flag → runner dispatch                            | `internal/apecmd/pipeline_modes.go`                        |
| Per-step `claude -p` spawn (programmatic path)    | `internal/pipeline/runner.go` (`runClaude`)                |
| Per-stage PTY + `claude` spawn (interactive path) | `internal/pipeline/interactive.go` (`runStageInteractive`) |
| PTY driver (NewSession / SendCommand / …)         | `internal/repl/`                                           |
| `ape chat` direct exec with stdio inheritance     | `internal/apecmd/chat.go`                                  |

## Related

- [invocation-matrix.md](invocation-matrix.md) — full UI × Exec table with all flag combinations
- [step-contract.md](step-contract.md) — what the runner types into the REPL between steps under interactive exec
- [../explanation/exec-modes.md](../explanation/exec-modes.md) — why interactive vs programmatic exists; trade-offs
- [bridge-ipc.md](bridge-ipc.md) — wire schema for the MCP bridge
