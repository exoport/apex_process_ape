# Reference — `ape pipeline` invocation matrix

PLAN-6 / C1 defines two orthogonal axes for `ape pipeline <name>`:

- **UI** — where output renders: `none` (plain stdout), `tui` (Bubble Tea), `web` (HTTP/SSE)
- **Exec** — how `claude` is spawned: `programmatic` (one `claude -p` per step) or `interactive` (one `claude` REPL per stage inside a tmux session, prompts delivered as real keystrokes via `tmux send-keys`)

The CLI selects one cell from the matrix. The default is **tui + interactive**.

## Matrix

| Invocation              | UI     | Exec           | Notes                                                              |
| ----------------------- | ------ | -------------- | ------------------------------------------------------------------ |
| `ape pipeline <name>`   | `tui`  | `interactive`  | **NEW default** (PLAN-6).                                          |
| `--tui`                 | `tui`  | `interactive`  | Explicit form of the default.                                      |
| `--interactive` / `-I`  | `tui`  | `interactive`  | Forces interactive when combined with a UI flag.                   |
| `--web`                 | `web`  | `interactive`  | **NEW** (PLAN-6 default). Pre-PLAN-6 `--web` is now `--web -P`.    |
| `--web -P`              | `web`  | `programmatic` | What today's `--web` does (PLAN-5).                                |
| `--no-tui`              | `none` | `interactive`  | **NEW** (PLAN-6). Stops aliasing `--print`.                        |
| `--no-tui -P`           | `none` | `programmatic` | No UI, today's per-step spawn.                                     |
| `--tui -P`              | `tui`  | `programmatic` | TUI panels with PLAN-5 per-step spawn.                             |
| `-P` / `--programmatic` | (UI)   | `programmatic` | Modifier; combine with `--tui` / `--web` / `--no-tui`.             |
| `--print`               | `none` | `programmatic` | **LOCKED** — byte-equivalent with PLAN-5 `--print`. Eval contract. |

## Mutual exclusion

- Multiple UI flags is an error: `ape pipeline foo --tui --web` → exit 2.
- `--print` admits no exec modifier: `--print --interactive`, `--print --programmatic` → exit 2.
- `--interactive` and `--programmatic` together is an error.

## Invariants

1. **`--print` is locked.** It does not construct a `BridgeRuntime`, does not inject hooks, does not change one byte of stdout vs PLAN-5. The eval consumer at `/home/diegos/_dev/exoar/apex_process_framework_eval` depends on this.
2. **Broker (HTTP/SSE) is web-only.** TUI and `none` UI modes must not start an HTTP listener.
3. **Stage process spawn = clean OS-level context** under interactive exec. Per-step `/clear` is runner-driven (one `tmux send-keys "/clear"` between steps unless the step sets `no-clear: true`); the first step of a stage skips `/clear` because the tmux session and `claude` process are fresh by construction.
4. **Step contract is hard-fail** (see [step-contract.md](step-contract.md)).
5. **`tmux` is required for interactive exec.** ape errors clearly if `tmux` is not on `PATH`. Programmatic exec (`-P`, `--print`) has no tmux dependency.

Related:

- [pipeline-yaml-schema.md](pipeline-yaml-schema.md) — pipeline YAML fields
- [step-contract.md](step-contract.md) — agent-prefix verification + how `/clear` is driven
- [../explanation/exec-modes.md](../explanation/exec-modes.md) — why interactive vs programmatic
