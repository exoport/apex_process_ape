# How-to — how ape runs claude (PTY-only)

Since v0.0.36 ape runs `claude` one way: an interactive REPL per stage inside an in-process PTY. The old programmatic `claude -p` path and its flags (`-P` / `--programmatic`, `-I` / `--interactive`, `--eval`) were removed — passing any of them exits `2` with a message pointing at [why-pty-only.md](../explanation/why-pty-only.md). The only choice left is **where output renders**.

## Pick a UI

| What you're doing                             | Run                                     |
| --------------------------------------------- | --------------------------------------- |
| Normal interactive use                        | `ape pipeline <name>` (default — `tui`) |
| Explicit TUI                                  | `ape pipeline <name> --tui`             |
| Web UI (browser, HTTP/SSE)                    | `ape pipeline <name> --web`             |
| No UI, plumb progress to a log (CI, eval)     | `ape pipeline <name> --no-tui`          |
| Single skill, structured result for tooling   | `ape task <skill> --output-format json` |

All of these execute the same per-stage interactive PTY; only the rendering surface differs. `--no-tui` is auto-enabled on a non-TTY.

## What you get on every run

- One `claude` REPL per stage in a per-stage in-process PTY; chain steps share the session within a stage (with `/clear` typed between them unless the step sets `no-clear: true`).
- Bridge step-contract verification of the agent-prefix shape (catches `/<wrong-agent>` or `/<wrong-skill>` invocations).
- Hooks captured under the run's manifest tree.
- No external runtime dependency. The PTY is allocated in-process via `github.com/aymanbagabas/go-pty`, so ape works on Linux, macOS, and Windows (incl. Git Bash via ConPTY) without `tmux` installed.

## Switching mid-run

You can't. `ape pipeline` doesn't resume from a partial run — pick the UI at invocation time.

## Migrating an existing pipeline

If your pipeline YAML doesn't mention `commit:` at any level, you get **no commits** (PLAN-6 default is skip). To keep the PLAN-4 per-step commit behaviour, either:

- Add `commit: true` at pipeline level for one commit per stage:
  ```yaml
  name: design
  commit: true
  stages:
    create-prd:
      chain:
        - skill: apex-create-prd
  ```
- Or keep per-step commits by setting `commit: true` on each step (the PLAN-4 shape; still works).

See [pipeline-yaml-schema.md](../reference/pipeline-yaml-schema.md) for the full precedence rules.

## Mutual-exclusion errors

- `--tui --web` (or any two UI selectors) → "only one UI selector at a time"
- `-P` / `--programmatic`, `-I` / `--interactive`, `--eval` → removed-in-v0.0.36 message

These exit with code 2 and a one-line error to stderr.

## Related

- [why-pty-only.md](../explanation/why-pty-only.md) — why PTY is the only exec mode.
- [exec-modes.md](../explanation/exec-modes.md) — the per-stage interactive runtime in depth.
- [invocation-matrix.md](../reference/invocation-matrix.md)
- [pipeline-yaml-schema.md](../reference/pipeline-yaml-schema.md)
