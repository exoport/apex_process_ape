# How-to — choose between interactive and programmatic exec

If you're new to PLAN-6 and wondering whether to use the new interactive default or stick with programmatic, this page gives the quick answers. For the why behind the modes, read [exec-modes.md](../explanation/exec-modes.md).

## TL;DR by scenario

| What you're doing                              | Run                                                 |
| ---------------------------------------------- | --------------------------------------------------- |
| First-time pipeline run, normal use            | `ape pipeline <name>` (default — tui + interactive) |
| Eval / CI / stdout-byte-capture pipelines      | `ape pipeline <name> --print`                       |
| Re-running a known-flaky single-step pipeline  | `ape pipeline <name> --tui -P`                      |
| Debugging today's `--web` behaviour            | `ape pipeline <name> --web -P`                      |
| Web UI but with PLAN-6 features                | `ape pipeline <name> --web`                         |
| No UI, just plumb to a log                     | `ape pipeline <name> --no-tui`                      |
| No UI + per-step claude (old `--no-tui` shape) | `ape pipeline <name> --no-tui -P`                   |

## Switching mid-run

You can't. Pick the mode at invocation time. `ape pipeline` doesn't resume from a partial run.

## What you get under interactive (default)

- One `claude` REPL per stage running inside a per-stage `tmux` session; chain steps share the session within a stage (with `/clear` typed between them unless the step sets `no-clear: true`).
- Bridge step-contract verification of the agent-prefix shape (catches `/<wrong-agent>` or `/<wrong-skill>` invocations).
- Hooks captured in `<project>/_output/pipelines/<name>/<run_id>/hook-events.jsonl`.
- TUI panels surface per-stage progress live.
- Attach for live debugging while a run is in flight: `tmux attach -t ape-<stage>-<pid>`.
- Requires `tmux` on `PATH`. Programmatic exec (`-P`, `--print`) has no tmux dependency.

## What you give up under interactive

- The `--print` byte-equivalence contract — for that, pass `--print` explicitly.
- Independence between steps in a stage: if step 1 corrupts the session, step 2 inherits the damage (catch this with `no-clear: true` discipline; see [step-contract.md](../reference/step-contract.md)).

## Migrating an existing pipeline

If your pipeline YAML doesn't mention `commit:` at any level, you're now getting **no commits** (PLAN-6 default is skip). To keep the PLAN-4 per-step commit behaviour, either:

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

- `--tui --web` → "only one UI selector at a time"
- `--print --interactive` → "--print admits no exec modifier"
- `--interactive --programmatic` → "mutually exclusive"

These all exit with code 2 and a one-line error to stderr.

## Related

- [exec-modes.md](../explanation/exec-modes.md)
- [invocation-matrix.md](../reference/invocation-matrix.md)
- [pipeline-yaml-schema.md](../reference/pipeline-yaml-schema.md)
