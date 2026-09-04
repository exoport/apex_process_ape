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

The MCP bridge (`internal/bridge/orchestrator/`) stays wired for **hook observability** (`UserPromptSubmit`, `Stop`, `PreToolUse`, `PostToolUse`, `SubagentStart`, `SubagentStop`, and since v0.0.60 `SessionStart` and `PreCompact`) in every mode. Under `--web` it additionally carries prompt/reply traffic for the browser via `await_message` / `reply`.

## tmux terminal identity is stripped on the PTY path (and only there)

`TMUX` and `TMUX_PANE` describe **which terminal** a process is attached
to. For a child on ape's in-process PTY that description is false: the
child's terminal is the PTY ape holds, while the inherited pane address
belongs to ape itself.

Claude Code reads both and records the pane in its session registry
(`~/.claude/sessions/<pid>.json`) as `"tmux": "session:@window.%pane"`,
without checking that the pane is its own controlling terminal. Driving
that recorded address then fails in two directions at once: the control
message lands as literal text in whatever the operator has in that pane,
and the claude on the PTY sees nothing.

ape itself was never affected — the runner writes to its own PTY master
and never reads the field — which is why this went unnoticed. The damage
lands on external consumers of the registry.

So the PTY spawn path strips both. The correct outcome is that the
child's record carries **no `tmux` key at all**: the only way into that
REPL is ape's PTY master, which no external process can reach, so a tool
reading the registry should conclude there is no keyboard to drive.

**`ape chat` deliberately does not strip them.** It direct-execs claude
onto the user's real terminal, so inside tmux that child genuinely *is*
in the inherited pane and recording the address is correct and useful.
The asymmetry is intentional and is why the filter is a separate,
unexported function in `internal/repl` rather than two more lines in the
shared `ScrubClaudeCodeEnv` — a caller outside that package cannot reach
it, so it cannot be applied to the chat path by accident. A test asserts
the shared scrubber still passes tmux through, so folding the two
together fails loudly.

## The output style is pinned on every spawn

Every session `ape` starts is a real Claude Code session in the project
root, so without intervention it inherits whatever **output style** the
machine has configured. That is not cosmetic. An output style claims
precedence over other communication and formatting guidance, and that is
exactly what the APEX framework leans on at the end of a run: the fenced
return contracts a batch orchestrator parses to reconstruct status and
paths, the guided menus and HALT prompts, and the completion summaries
the eval asserts on. A developer who set `Concise` for their own
conversations would quietly change what every skill run emits — on their
machine only, in a way no test on another machine would reproduce.

So `ape` writes `"outputStyle": "Default"` into the `--settings` blob it
already builds, on every spawn path. A skill run is machine-consumed
output rather than a conversation, so the pin is the default rather than
an opt-in.

**The key has to be written explicitly.** Claude Code's precedence is
enterprise-managed → `--settings` → project-local → shared-project →
user, and an *omitted* key falls through to the highest file that defines
one. Leaving it out neutralises nothing.

```bash
ape task <skill> --output-style inherit     # keep the machine's style
ape task <skill> --output-style Explanatory # pin a specific one
```

The flag is on `ape task`, `ape pipeline`, `ape prompt` and `ape chat`.

Two limits worth knowing:

- **Enterprise-managed settings still outrank `--settings`.** On a
  managed fleet the pin can be overridden, and `ape` cannot close that.
- **`--ignore-project-settings` was never the answer.** It passes
  `--setting-sources user`, which drops the project and local files and
  *keeps* the user one — so a user-level style still reaches the session.
- **`--eval` is deliberately exempt.** `ModeEval` returns a byte-empty
  settings blob under PLAN-6 invariant #1, which locks spawn-shape
  equivalence with an external consumer. Nothing that needs the pin
  reaches that path.

This was verified against Claude Code 2.1.259 rather than taken from the
docs, which only ever describe omitting the key: a custom style named in
`--settings` takes effect, an explicit `Default` there overrides the same
style set in a project settings file, and an unknown style name is
silently ignored rather than rejected — so "it did not error" is not
evidence that a value was honoured.

## Source of truth in code

| What                                              | Where                                                      |
| ------------------------------------------------- | ---------------------------------------------------------- |
| UI-flag → mode resolution                         | `internal/apecmd/pipeline_modes.go`                        |
| Per-stage PTY + `claude` spawn (interactive path) | `internal/apecmd/pipeline_interactive.go`                  |
| PTY driver (NewSession / SendCommand / …)         | `internal/repl/`                                           |
| `ape chat` direct exec with stdio inheritance     | `internal/apecmd/chat.go`                                  |
| `--settings` blob, incl. the output-style pin     | `internal/bridge/config/settings.go`                       |

## Related

- [invocation-matrix.md](invocation-matrix.md) — the UI axis (`tui` default, `web`, `no-tui`).
- [step-contract.md](step-contract.md) — what the runner types into the REPL between steps.
- [../explanation/why-pty-only.md](../explanation/why-pty-only.md) — why PTY is the only exec mode.
- [../explanation/exec-modes.md](../explanation/exec-modes.md) — the per-stage interactive runtime in depth.
- [bridge-ipc.md](bridge-ipc.md) — wire schema for the MCP bridge.
