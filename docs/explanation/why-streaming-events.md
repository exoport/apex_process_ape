# Why the pipeline TUI streams live events

Up to and including ape `v0.0.6`, the pipeline TUI updated its "output" panel only at the boundary of each skill — when the spawned `claude` subprocess exited and the runner emitted `OnStepEnd` with the full captured stdout. Between boundaries, the screen sat frozen for minutes at a time. ape `v0.0.7` (PLAN-1 / I4 + I4b) changes that: the TUI now streams human-readable progress lines as `claude` produces them. This page explains why and the trade-offs we accepted.

## What "frozen" actually meant

A typical skill — say `apex-create-architecture` — runs for 60–300 seconds, with the model orchestrating dozens of tool calls (`Read`, `Edit`, `Write`, `Bash`, sometimes `Task` to spawn a subagent). Under v0.0.6, none of that activity reached the screen until the skill returned. The user had three signals:

- The stage list's spinner glyph (▶) indicating the step was running.
- Elapsed time ticking up.
- The previous step's truncated output, frozen in the right panel.

When stages chained (six skills in `design`, eight in `governance`), the user spent most of the run staring at stale content from a step that finished minutes ago, while the _next_ step ran silently for another minute.

The reported pain was consistent: "is this thing alive?" and "I can't tell what step it's on inside the skill."

## What streaming changes

The runner now plumbs the subprocess's stdout pipe through a `bufio.Scanner` and emits one `Observer.OnStepLine(stage, idx, line)` event per newline. `claude --output-format stream-json` writes one JSON event per line — `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read",...}]}}` and similar — so every tool call, every model text block, every result lands in the Observer as it happens.

The TUI parses each line through `tui.RenderEvent` and appends a one-line summary to the active stage's event feed:

```
🔧 Read  development/planning/prd/index.md
🔧 Read  development/planning/architecture/index.md
✎  Drafting ADR table: 4 candidates
🔧 Edit  development/planning/architecture.md
✓  apex-create-architecture · 3.5/5.0 pass
```

The frozen-screen problem disappears. The same renderer also feeds `--no-tui` mode, so log captures and CI runs get the live feed too.

## Trade-offs

### What we gained

- Live "is this thing alive" signal at the granularity of every tool call.
- Visible progress inside a skill (read which files it's looking at, see when it pivots from reading to writing).
- Audit trail in `--no-tui` log captures that's far more useful than the previous once-per-step dumps.

### What it cost

- **Backwards compatibility**: the `pipeline.Observer` interface gained `OnStepLine`. All implementations had to add it. In v0.0.7 only `PipelineTUIObserver` and `plainObserver` exist; both were updated. Future external implementations have to do the same.
- **Slight CPU uptick** during streaming bursts (~1-2% on one core). Negligible on modern hardware.
- **Render cadence**: bursts of 100+ events/sec from the model can trigger frequent re-renders. v0.0.7 leaned on Bubble Tea's internal throttling; v0.0.8 lands an explicit 33ms (~30Hz) flush loop (PLAN-2 / F2) so the headroom is bounded rather than implicit.
- ~~**No live cancellation of orphans**~~ (closed in v0.0.8): pre-v0.0.8, when the user confirmed quit, the immediate `claude` child was killed via context cancellation + `exec.CommandContext`'s SIGKILL but any sub-agents spawned through the `Task` tool orphaned to PID 1 and continued running until they exited naturally. v0.0.8 (PLAN-2 / F1) makes the child a process-group leader (`Setpgid=true`) and rewires `Cmd.Cancel` to SIGTERM the whole group, with an escalator goroutine that SIGKILLs the group after a 500ms grace. `internal/pipeline/runner_unix_test.go` regresses the bug via a shim that forks a SIGTERM-trapping grandchild and asserts both PIDs are reaped within 1.5s of cancellation. Linux + darwin only; Windows falls back to the pre-v0.0.8 direct-child SIGKILL.

### What we didn't try

- **Per-line raw display.** Showing every `{"type":"..."}` JSON event would technically be "live," but it'd flood the screen with structure that's not directly useful to a human. The renderer's value-add is the noise filter (suppress trivial `tool_result` successes, collapse multi-block assistant messages to first-line summaries) plus the icon/colour mapping.
- **A separate streaming-only mode.** Some CLIs offer `--json` to dump raw events for tool consumption. We deferred this; the same use case is served by piping `--no-tui` through a future structured-output flag (Plan-2 candidate).

## When streaming doesn't help

If the model takes a very long time on a _single_ tool call (a slow Bash, a large WebFetch), the live feed will sit on the `🔧 Bash …` line for the duration. Streaming makes the _boundary_ visible; it doesn't speed up the work inside a tool. A heartbeat row (`‥`) appears after 5 seconds of no events, mostly to confirm the subprocess hasn't deadlocked.

## See also

- [Pipeline TUI keybindings](../reference/tui-keybindings.md) — what the user can do while events stream.
- [How to run a pipeline](../how-to/framework-setup.md) — the install path that precedes any `ape pipeline` call.
- [PLAN-1 in this repo](../../development/planning/plan-1_pipeline-ux-and-framework-setup.md) — the plan that drove the streaming work.
- `internal/tui/event_renderer.go` — the parser + renderer that produces the human-readable lines.
- `internal/pipeline/runner.go` — `runClaude` and the new `OnStepLine` plumbing.
