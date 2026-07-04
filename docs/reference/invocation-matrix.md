# Reference — `ape pipeline` invocation matrix

Since v0.0.36 (PLAN-9 F2) `ape pipeline <name>` has a single axis:

- **UI** — where output renders: `tui` (Bubble Tea, default), `web` (HTTP/SSE), or `none` (plain stdout, `--no-tui`).

The exec axis is gone. Every mode executes `claude` as an interactive REPL per stage inside an in-process PTY (prompts delivered as real keystrokes by writing to the PTY master end). The programmatic `claude -p` path and its flags (`-P` / `--programmatic`, `-I` / `--interactive`, `--eval`) were removed — see [why-pty-only.md](../explanation/why-pty-only.md).

The CLI selects one UI. The default is **tui**.

## Matrix

| Invocation            | UI     | Notes                                              |
| --------------------- | ------ | -------------------------------------------------- |
| `ape pipeline <name>` | `tui`  | Default. Bubble Tea panels.                        |
| `--tui`               | `tui`  | Explicit form of the default.                      |
| `--web`               | `web`  | Bridged web UI (HTTP/SSE); mirrors the PTY output. |
| `--no-tui`            | `none` | Plain stdout progress lines (auto on non-TTY).     |

All rows run the same per-stage interactive PTY. The eval harness uses `--no-tui` (and `ape task --output-format json` for single skills); there is no separate byte-locked mode.

## Mutual exclusion

- More than one UI selector is an error: `ape pipeline foo --tui --web` → exit 2. Same for `--tui --no-tui` and `--web --no-tui`.
- The removed exec flags (`-P` / `--programmatic`, `-I` / `--interactive`, `--eval`) error with the removal message and exit 2.

## Invariants

1. **PTY is the only exec mode.** Every run drives a live `claude` REPL; ape never passes `-p`. There is no captured-for-replay code path to keep in sync.
2. **Broker (HTTP/SSE) is web-only.** TUI and `none` UI modes must not start an HTTP listener.
3. **Stage process spawn = clean OS-level context.** Per-step `/clear` is runner-driven (one PTY-typed `/clear` between steps unless the step sets `no-clear: true`); the first step of a stage skips `/clear` because the PTY and `claude` process are fresh by construction.
4. **Step contract is hard-fail** (see [step-contract.md](step-contract.md)).
5. **No external runtime dependency.** The PTY is allocated in-process (`internal/repl/`, backed by `github.com/aymanbagabas/go-pty`), so ape works on Linux, macOS, and Windows (incl. Git Bash via ConPTY) without `tmux` on `PATH`.

Related:

- [claude-spawn-modes.md](claude-spawn-modes.md) — how ape delivers prompts to the PTY-hosted REPL.
- [pipeline-yaml-schema.md](pipeline-yaml-schema.md) — pipeline YAML fields.
- [step-contract.md](step-contract.md) — agent-prefix verification + how `/clear` is driven.
- [../explanation/why-pty-only.md](../explanation/why-pty-only.md) — why the exec axis was removed.
- [../explanation/exec-modes.md](../explanation/exec-modes.md) — the per-stage interactive runtime in depth.
