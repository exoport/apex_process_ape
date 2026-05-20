# Reference — when ape uses `claude -p`

Every ape command that talks to claude does so in one of two shapes:

- **Programmatic** — one `claude -p "<prompt>"` subprocess per step. The process exits when claude finishes responding. No REPL, no slash commands, no tmux.
- **Interactive** — one long-lived `claude` REPL per stage running inside a `tmux` session. Prompts are typed into the pane as real REPL keystrokes via `tmux send-keys -l "<prompt>"` + Enter. claude's CLI parses slash commands the normal way, the skill loads, the model executes. The bridge's `Stop` hook signals step-done.

This page is the lookup table for which mode each command + flag combination uses.

## Matrix

| Invocation                                  | UI     | Exec         | `claude -p`? | Prompt delivery                                                                     |
| ------------------------------------------- | ------ | ------------ | ------------ | ----------------------------------------------------------------------------------- |
| `ape pipeline <name>` _(default)_           | tui    | interactive  | **NO**       | `tmux send-keys -l "<prompt>"` + Enter into per-stage tmux session                  |
| `ape pipeline <name> --tui`                 | tui    | interactive  | **NO**       | same as default                                                                     |
| `ape pipeline <name> --interactive` / `-I`  | (UI)   | interactive  | **NO**       | same as default                                                                     |
| `ape pipeline <name> --web`                 | web    | interactive  | **NO**       | tmux send-keys; web UI mirrors via bridge SSE                                       |
| `ape pipeline <name> --no-tui`              | none   | interactive  | **NO**       | tmux send-keys; plain stdout                                                        |
| `ape pipeline <name> -P` / `--programmatic` | (UI)   | programmatic | **YES**      | `claude -p "<prompt>"` per step, fresh process                                      |
| `ape pipeline <name> --tui -P`              | tui    | programmatic | **YES**      | TUI panels + per-step `claude -p`                                                   |
| `ape pipeline <name> --no-tui -P`           | none   | programmatic | **YES**      | plain stdout + per-step `claude -p`                                                 |
| `ape pipeline <name> --web -P`              | web    | programmatic | **YES**      | PLAN-5 web shape: per-step `claude -p` + bridge for hooks + `await_message`/`reply` |
| `ape pipeline <name> --eval`                | none   | programmatic | **YES**      | locked PLAN-5 byte-equivalent; per-step `claude -p`                                 |
| `ape chat`                                  | (tmux) | interactive  | **NO**       | user types directly into the tmux REPL after `tmux attach`                          |

## The rule

```
claude -p  ⇔  Exec axis = programmatic  ⇔  -P / --programmatic / --eval
```

Anything **interactive** (the default for `ape pipeline`; the only mode for `ape chat`) spawns claude into a tmux session and never passes `-p`.

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

| What                                               | Where                                                      |
| -------------------------------------------------- | ---------------------------------------------------------- |
| Flag → runner dispatch                             | `internal/apecmd/pipeline_modes.go`                        |
| Per-step `claude -p` spawn (programmatic path)     | `internal/pipeline/runner.go` (`runClaude`)                |
| Per-stage tmux + `claude` spawn (interactive path) | `internal/pipeline/interactive.go` (`runStageInteractive`) |
| tmux subcommand wrappers                           | `internal/tmux/`                                           |
| `ape chat` tmux spawn-and-attach                   | `internal/apecmd/chat.go`                                  |

## Related

- [invocation-matrix.md](invocation-matrix.md) — full UI × Exec table with all flag combinations
- [step-contract.md](step-contract.md) — what the runner types into the REPL between steps under interactive exec
- [../explanation/exec-modes.md](../explanation/exec-modes.md) — why interactive vs programmatic exists; trade-offs
- [bridge-ipc.md](bridge-ipc.md) — wire schema for the MCP bridge
