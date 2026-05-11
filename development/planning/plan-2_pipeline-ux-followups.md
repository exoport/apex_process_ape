---
plan_id: PLAN-2
created_at: 2026-05-10
implemented_at: null
status: proposed
tags:
  - pipeline-tui
  - subprocess-lifecycle
  - perf-tuning
  - narrow-terminals
  - cli-quality-of-life
summary: Five follow-ups deferred during PLAN-1 implementation. Each is independently shippable. F1 — process-group teardown so confirmed quit kills the whole `claude` subprocess tree (not just the direct child), closing the orphan-subagent gap from PLAN-1 / I2 open issue #1. F2 — 30 Hz render throttle via `tea.Tick(33ms)` flush, in case real-world streaming bursts trip visible lag. F3 — render-style cycling (`r` key, human → raw JSON → both), reserved in docs but not yet implemented. F4 — narrow-terminal fallback (horizontal stepper instead of right-side stage list) for terminals under 90 columns. F5 — `--quiet` flag for `--no-tui` mode that suppresses the live-event stream and prints only the per-stage summary at completion.
origin:
  - 2026-05-10 PLAN-1 carry-out — every item was explicitly deferred to "follow-up" in the PLAN-1 plan body or open-issues section, with the rationale documented at the time.
  - F1 in particular addresses the v0.0.7 caveat in docs/explanation/why-streaming-events.md about orphan subagents surviving Ctrl+C.
---

# PLAN-2: Pipeline UX follow-ups (v0.0.7 carry-out)

## Goal

Resolve the five known gaps deferred from PLAN-1 — each a small, independent change — so that ape's pipeline UX hits "no known cosmetic or behavioral debt" before any v1.0 conversation. None of these block end-to-end use of v0.0.7; each closes a specific edge case that's documented or measured to exist.

## Scope — IN

### F1: Process-group teardown for `claude` subagents

- **Problem.** `internal/pipeline/runner.go: runClaude` spawns the `claude` subprocess via `exec.CommandContext`. On context cancellation, Go sends SIGKILL to the immediate child. Claude itself spawns sub-agents via the `Task` tool; those orphan to PID 1 when the parent dies and continue consuming Anthropic API budget until they exit naturally. PLAN-1 / I2 open issue #1 documents this; `docs/explanation/why-streaming-events.md` § "What it cost" cites it explicitly.
- **Fix.** Set `cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}` so the child becomes a process-group leader. On cancellation, send SIGTERM to `-pgid` (the whole group), wait briefly (≈500ms), then SIGKILL `-pgid` if anything survives.
- **Platform.** Linux + macOS use `Setpgid` and `syscall.Kill(-pgid, sig)`. Windows uses job objects; for v1, gate the process-group code behind `//go:build linux || darwin` and let Windows fall back to the existing single-child SIGKILL.
- **Tests.** Spawn a shell shim that forks a child (`(sleep 999) &`), invoke `runClaude` with a context that cancels after 100ms, verify both the immediate child and the grandchild exit within the grace window.

### F2: 30 Hz render throttle in the pipeline TUI

- **Problem.** Per-event re-rendering on the Bubble Tea path is bounded by the program's internal cadence today; no explicit ceiling. Bursts of 100+ stream-json events per second don't actually visibly lag in current measurements, but the headroom is unmeasured outside synthetic tests.
- **Fix.** Buffer incoming `stepLineMsg` events into a queue on the model; flush the queue + force one render every 33ms via a `tea.Tick`. When the queue is empty the tick is a no-op. This caps the render rate at ~30 Hz regardless of incoming line rate.
- **Why now.** Land it before terminal multiplexers (tmux, screen) become the dominant rendering surface and introduce extra latency that compounds. Cheap insurance.
- **Tests.** Synthesized 500-event burst into the model; assert the View() is invoked at most 1+ceil(500/15) times (one initial render plus one per 33ms tick across the burst's wall clock).

### F3: Render-style cycling (`r` key)

- **Problem.** `docs/reference/tui-keybindings.md` reserves `r` for cycling event-line render style (human → raw JSON → both). The keybind is not yet implemented in `internal/tui/pipeline.go: Update`.
- **Fix.** Add `renderStyle` field to `pipelineModel` (enum: `styleHuman`, `styleRawJSON`, `styleBoth`). Wire `r` key to advance the field cyclically. `renderEventPanel` switches on the field: human format unchanged; raw shows the original NDJSON line; both shows a two-line entry per event.
- **Storage cost.** `RenderedEvent` already carries `Glyph` + `Body`. Add `Raw string` (the original line) to support `styleRawJSON` and `styleBoth` without re-parsing.
- **Tests.** Three model snapshots — set state, press `r`, snapshot rendered output for each style cycle; assert the rendered event panel contents match expectations.

### F4: Narrow-terminal horizontal-stepper fallback

- **Problem.** Current layout has hard floors (28-col right column, 30-col left, 6-row panel height). On terminals under 90 cols, the floors keep the layout rendering but text wraps and visual density suffers. The stage list in particular loses its right-column home and wedges next to the event panel.
- **Fix.** When `m.width < 90` at startup or after a SIGWINCH, switch to single-column layout:
  - Event panel takes the full width (left column gone).
  - Stage list becomes a single horizontal row above the event panel: `✓ prd  ✓ shard  ✓ ux  ✓ shard  ▸ arch  · shard-arch  · …`.
  - Bottom status strip and keybind hint stay.
- **Iteration.** Cursor still moves with `↑↓`; in narrow mode the cursor is rendered as a marker character (e.g. `[▸ arch]`) instead of a left-side `>`. Pinned mode unchanged conceptually — `Enter` pins, `PgUp/PgDn` scrolls.
- **Tests.** WindowSizeMsg with width=80; assert the model picks the narrow layout. Width=200; assert the wide layout. Cursor navigation works in both.

### F5: `--quiet` flag for `--no-tui` mode

- **Problem.** `--no-tui` today streams every parsed event to stdout (per PLAN-1 / I4b). For CI runs where humans only read the failure summary, this is noise. Long pipelines can produce thousands of lines that overflow CI log scrollback.
- **Fix.** Add `--quiet` flag to `ape pipeline`. When set:
  - `plainObserver.OnStepLine` is a no-op (the existing pre-I4b behavior).
  - `OnStageStart` / `OnStepStart` still print one-line markers.
  - `OnStepEnd` / `OnStageEnd` print summary lines as today.
- **Compatibility.** `--quiet` only meaningful with `--no-tui` (the TUI's panels aren't affected by the flag). Document this in the flag help text. Reject `--quiet` without `--no-tui` with an actionable error.
- **Tests.** Run a stage with `--no-tui --quiet`; assert stdout contains only start / end markers (no `🔧 / ✎ / ↳` lines).

## Scope — OUT

- **JSON output stream for piping.** A `--format json` flag that emits each parsed event as a single NDJSON line on stdout, for piping into observability tools. PLAN-1 § "Out of band" floated this as a Plan-2 candidate; it's a richer feature than what's bundled here, so it stays separate until someone needs it.
- **TUI common-component refactor.** Shared `internal/tui/common` package for the modal, stepper, color palette used by both the bootstrap TUI (in `framework setup`) and the pipeline TUI. Plan-3 candidate per PLAN-1 § "Out of band".
- **Per-line streaming in non-Anthropic tool subprocesses.** The `OnStepLine` plumbing is generic, but the renderer is `claude --output-format stream-json`-specific. Generalizing for other tool formats is out of scope.
- **Live cancellation grace period as a configurable knob.** F1 uses a fixed 500ms SIGTERM → SIGKILL grace. If real-world subagent shutdown is slower, the grace becomes a flag; not yet.

## Implementation steps

Each item is one commit on `main`, with tests. Order is whatever matches what hurts most in practice; the items don't depend on each other.

### Suggested ordering

1. **F1 (process-group teardown).** Highest value — closes the orphan-subagent gap. Has the largest blast radius, so land first while the diff is small.
2. **F5 (`--quiet`).** Tiny scope (one flag + one observer-method early-return). Often-requested.
3. **F4 (narrow-terminal fallback).** Useful for users running ape inside tmux / kitty / VSCode terminals where width is variable.
4. **F3 (render-style cycling).** UX nicety; docs already say it works, so closing the gap removes user surprise.
5. **F2 (render throttle).** Insurance. No measured pain today.

### Per-item details

- **F1.** New file `internal/pipeline/proc_unix.go` with `//go:build linux || darwin` for the `Setpgid` setup; `proc_windows.go` for the no-op fallback. `runClaude` calls a small `setProcessGroup(cmd)` helper. Cancellation path: on `cmd.Wait` returning, check `ctx.Err()`; if non-nil, escalate to `kill -KILL -pgid` after the grace.
- **F2.** Model field `pendingLines []RenderedEvent`. New tea.Tick at startup; on each tick, drain `pendingLines` into the per-stage events slice + emit a `tea.WindowSizeMsg`-like nudge so the view re-renders once. `stepLineMsg` handler appends to the queue instead of the stage's events slice directly.
- **F3.** Enum + `r` key handler. Renderer change is a single switch on `m.renderStyle` inside `renderEventPanel`.
- **F4.** New `narrowLayout()` View helper; `View()` branches on `m.width < narrowThreshold`. Update keybind hint footer to reflect the narrow-mode controls.
- **F5.** Cobra flag + `Validate` hook to refuse `--quiet` without `--no-tui`. `plainObserver` constructor gains a `quiet bool` param.

## Open issues to resolve during implementation

1. **F1 — Windows fallback semantics.** On Windows, `claude` subprocesses are direct children only; the SIGKILL-via-context path covers them adequately. Decide during F1 PR whether to skip the platform shim entirely or stub a placeholder `setProcessGroup` that returns nil.
2. **F2 — Bubble Tea native throttling.** Some Bubble Tea releases auto-batch `Send` calls into per-frame buffers; before implementing F2, measure whether the framework's existing batching already caps render rate. If yes, F2 collapses to a docs note.
3. **F3 — `Raw string` field bloat.** Storing the raw JSON line on every `RenderedEvent` roughly doubles per-event memory. Confirm bound (≤ 2000 events per step ≈ 4 MB), acceptable.
4. **F4 — Stepper truncation on extra-narrow terminals.** What does the horizontal stepper do at width=60 with 12 stages? Options: ellipsize the stepper (`✓ prd · ✓ shard · ▸ arch · …`); allow horizontal scroll; or stack vertical (loop back to two-row stage list). Decide during F4 PR.
5. **F5 — Interaction with framework_update steps in eval fixtures.** The harness invokes the `framework_update` step kind separately; `--quiet` doesn't apply there. Confirm during F5 that the eval-side captures aren't affected.

## Risks

- **F1.** A buggy SIGTERM-to-pgid loop could orphan the parent before signaling children, defeating the purpose. Mitigated by tests that verify both child and grandchild are gone after cancellation.
- **F2.** Aggressive throttling can make the live stream feel laggy in human eyes — 33ms (~30 Hz) is comfortable but trips up to 67ms (~15 Hz) in worst case if a render takes ≥33ms itself. Mitigated by measuring before shipping.
- **F3.** Raw JSON view exposes internal claude CLI output format to users. If the schema drifts (we already handle this in the renderer's fallback), the raw view stays useful, but users may build expectations. Not a real risk — flag it as "for debugging" in docs.
- **F4.** Width-based layout switch can flicker on terminals that report SIGWINCH for every column change. Mitigated by debouncing the switch (only re-layout when crossing the threshold, not on every width tick).
- **F5.** Quiet `--no-tui` runs could hide useful pre-failure context. Mitigated by always printing `OnStepEnd` failure output even in quiet mode (the FAIL summary already includes the captured stdout).

## Acceptance — plan-2 done

- F1: a synthetic shim that forks a grandchild dies cleanly within 1s of context cancellation.
- F2: a 500-event burst into the model produces ≤16 calls to `View()` over its wall-clock duration.
- F3: pressing `r` cycles render style; rendered output matches snapshot for each style.
- F4: window-resize to 80 cols switches to single-column layout with the horizontal stepper; resize back to 200 restores the wide layout.
- F5: `ape pipeline design --no-tui --quiet` emits start/end markers only; no per-event lines.
- All five items shipped, each with tests; `make lint` clean; `go test ./...` clean.
- ape v0.0.8 tagged with the bundle.

## Verification plan

1. **F1 smoke.** Manual: run a pipeline with a `Task` step (spawns a subagent). Press `q`, confirm. Run `pgrep -af claude`. Expected: zero results within 1s.
2. **F2 smoke.** Stream a synthetic 2000-event burst through a test fixture; visually confirm the TUI tracks live without freezing or skipping.
3. **F3 smoke.** Run any pipeline, press `r` mid-run, observe events shift from `🔧 Read foo.md` to `{"type":"assistant",...}` raw form and back.
4. **F4 smoke.** Open ape in a 60-col terminal; resize during a pipeline run; expect layout to switch without crashes and the stage cursor to remain on the right row.
5. **F5 smoke.** `ape pipeline design --no-tui --quiet | wc -l` should be << the same run without `--quiet`.

## Out of band

- A "v0.0.8" CHANGELOG entry should land alongside the final commit, mirroring the v0.0.7 entry's structure.
- If F1 lands quickly and the orphan-subagent risk feels closed, consider downgrading the v0.0.7 docs/explanation/why-streaming-events.md note that flags the gap.
