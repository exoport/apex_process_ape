---
plan_id: PLAN-7
created_at: 2026-05-21
implemented_at: 2026-05-21
status: done
tags:
  - pipeline-tui
  - refactor
  - layout-bug
  - hook-event-rendering
  - interactive-exec
summary: Collapse the two Bubble Tea models — the rich `pipelineModel` (PLAN-2, driving `--tui -P` / `--web -P`'s terminal side) and the limited `InteractiveModel` (PLAN-6 Phase E, driving `--tui`) — into a single model parameterised by an event source. Interactive mode keeps its uniqueness (await-message modal, REPL-spawned claude) but inherits dual panels, cursor + scroll, render-style cycle, narrow-layout fallback, completion banner, final-report row, and the rich tool-call event stream. The hook-event stream the bridge already publishes carries enough structural data (PreToolUse / PostToolUse / Stop / UserPromptSubmit payloads with `tool_name`, `tool_input`, `tool_response`, `prompt`) to render the same `RenderedEvent` shape that stream-json yields today; only an adapter is missing. Phase 0 first fixes the row-budget bug behind the visible border misalignment when the left panel scrolls, so the geometry invariant is locked before refactoring on top of it.
origin:
  - 2026-05-20 user-reported parity gap. Four screenshots in `~/Pictures/Screenshots/` document the diff: `screenshot_2026-05-20_19-41-05.png` (programmatic TUI left+right panels with rich skill events), `screenshot_2026-05-20_11-49-23.png` (interactive TUI's stacked stages + hooks lists), `screenshot_2026-05-20_19-57-03.png` (final-report row at completion, interactive has no equivalent), `screenshot_2026-05-20_19-57-17.png` (border misalignment when left panel scrolls).
  - Investigation 2026-05-21 confirmed the data layer is sufficient — `hook-events.jsonl` payloads (`tool_name`, `tool_input.file_path`, `tool_use_id`, `tool_response`) carry the same structural content stream-json does for tool calls; `InteractiveModel.PushHook` discards everything except `(event_name, step)`. Notes archived in `_output/implementation-notes.html`.
  - Follow-on to PLAN-2 (built the rich TUI) and PLAN-6 Phase E (built the limited interactive TUI as a stopgap; the deferred unification was anticipated in the PLAN-6 plan body).
---

# PLAN-7: Unified pipeline TUI (interactive ≡ programmatic)

## Goal

`--tui` and `--tui -P` converge on a single Bubble Tea model. After PLAN-7 lands, both invocation modes present:

- Dual panels (left = rich event feed, right = stages list).
- Cursor navigation with `↑↓`, pin/live modes with `Enter` / `L`.
- `PgUp` / `PgDn` / `Home` / `End` scroll over a per-stage event history.
- `r` cycles human / raw / both render styles.
- Narrow-layout fallback under 90 columns.
- Completion banner + final-report row on `pipelineDoneMsg`.
- Quit-confirm modal on `q` / Ctrl+C, with double-Ctrl+C bypass.

Interactive mode keeps the surfaces only it needs — the bridge-driven await-message modal with its textinput reply, and the hook-event ingestion path. Programmatic mode is byte-for-byte the same as today.

The user-observed border misalignment under scroll (left panel's bottom border drops below the right's once events overflow the visible window) is fixed as Phase 0 and inherits to every downstream phase.

## Scope — IN

### F0: Row-budget invariant for `composePanelBody`

- **Problem.** Visible in `~/Pictures/Screenshots/screenshot_2026-05-20_19-57-17.png`. Once `renderEventPanel` returns more displayable rows than fit, the left panel's bottom border drops one row below the right panel's.
- **Diagnosis.** Two stacked causes in `internal/tui/pipeline.go`:
  1. `renderEventPanel` writes each event with a trailing `\n`. The body string handed to `pipelinePanelStyle.Height(panelHeight).Render(...)` is therefore `"line1\n...lineN\n"`. Lipgloss counts the trailing newline as a logical row, so the rendered box is `panelHeight + 1` rows tall.
  2. `styleBoth` (PLAN-2 / F3) emits two lines per event but the window-slice math (`for i := start; i < end; i++` where `end := min(start+height, len(events))`) assumes one line per event. With ≥ `height/2` events, the body overflows `Height` regardless of the trailing-newline fix.
- **Fix.** Add a panel-body composer to `internal/tui/pipeline.go`:
  ```go
  // composePanelBody concatenates header + body, trims trailing
  // newlines, and pads or truncates to exactly budget lines so the
  // rendered lipgloss box never grows past Height(panelHeight).
  func composePanelBody(header, body string, budget int) string
  ```
  Both `leftPanel` and `rightPanel` route through it. `renderEventPanel` returns its lines without a trailing newline. For `styleBoth`, slice math switches to a height budget consumed at 2 lines per event (the for-loop tracks an output-line counter, not the event index).
- **Tests.** `pipeline_test.go: TestPanelRowBudget`:
  - 100-event panel, `panelHeight=20`, all three render styles → `strings.Count(rendered, "\n") + 1` exactly equals the budget for each style.
  - Empty events slice → budget rows of padding still produced.
  - Single-event slice with `panelHeight=20` → 1 event row + 19 padding rows, total = budget.
  - Regression case: `len(events) == panelHeight - headerRowReserve` → rendered box height equals `panelHeight`, not `panelHeight + 1`.
- **Out of scope here.** `viewNarrow` uses the same `composePanelBody` and inherits the fix; no separate work item.

### FA: Carve `pipelineModel` into the unified model

- **Problem.** `pipelineModel` and `InteractiveModel` parallel each other on stages / cursor / modal / phase / throttle but diverge on event ingestion and panel layout. Keeping two models forces every future enhancement (e.g. the next render-style addition, a new keybind) to be implemented twice.
- **Fix.** Promote `pipelineModel` to be the only model. Surface area:

  ```go
  type EventSource int
  const (
      SourceStreamJSON EventSource = iota
      SourceHookEvents
  )

  type PipelineModelOption func(*pipelineModel)

  func WithEventSource(s EventSource) PipelineModelOption
  func WithAwaitReplySender(fn awaitReplySender) PipelineModelOption

  func NewPipelineModel(
      spec *pipeline.Spec,
      cancel context.CancelFunc,
      projectRoot string,
      opts ...PipelineModelOption,
  ) pipelineModel
  ```

  Default source is `SourceStreamJSON` so every existing call site (and the test suite) keeps the current behavior with no API change. The `awaitReplySender awaitReplySender` field is nil by default; the await-modal branch in `Update` is unreachable when nil, and `View` skips the modal overlay branch the same way.

- **Field additions to `pipelineModel`.**
  ```go
  source          EventSource
  awaitReplySender awaitReplySender   // nil ⇒ modal disabled
  replyInput      textinput.Model      // zero value when sender == nil
  awaitActive     bool
  // Per-stage in-flight tool calls keyed by tool_use_id, used to
  // attach PostToolUse result excerpts to the originating Pre row
  // (FB). Trimmed on stage end.
  inFlight        map[string]map[string]int  // stageIdx → toolUseID → eventIdx
  ```
- **Message types added.** `hookEventMsg`, `awaitPendingMsg`, `awaitResolvedMsg` (the last two move out of `interactive.go` into `pipeline.go`). `stepLineMsg` stays as-is; both message types feed into the same `events []RenderedEvent` per-stage slice.
- **Tests.**
  - Every existing test in `pipeline_test.go` keeps passing unchanged — `SourceStreamJSON` is the default.
  - New `pipeline_test.go: TestPipelineModelHookEventSource` exercises the constructor with `WithEventSource(SourceHookEvents)` + a synthetic `hookEventMsg` stream; asserts that the per-stage `events` slice fills with the expected `RenderedEvent`s.
  - New `pipeline_test.go: TestAwaitModalRequiresSender` confirms that with `awaitReplySender == nil`, an `awaitPendingMsg` is a no-op (modal stays closed).

### FB: Hook-event renderer (`event_renderer.go`)

- **Problem.** `event_renderer.go` understands stream-json (`RenderEventWithRoot(line, root)`). Interactive mode has bridge hook events with semantically equivalent payload but no parser; `InteractiveModel.PushHook` produces `15:04:05 PreToolUse <step>` lines that throw away the tool name, args, and result.
- **Fix.** Add a sibling function in `internal/tui/event_renderer.go`:
  ```go
  // RenderHookEvent converts a bridge HookEvent into a RenderedEvent
  // matching the shape produced by RenderEventWithRoot. The renderer
  // keeps no state; the caller (pipelineModel) holds the in-flight
  // tool-use map for Pre/Post correlation.
  func RenderHookEvent(h orchestrator.HookEvent, projectRoot string) RenderedEvent
  ```
  Mapping table:
  | `hook_event_name` | `Kind` | `Glyph` | `Body` source |
  | ---------------------- | ----------------- | ------- | ------------------------------------------------------------------------------ |
  | `UserPromptSubmit` | `EventText` | `?` | `prompt`, first line, truncated to event-renderer ceiling |
  | `PreToolUse` | `EventTool` | `🔧` | `tool_name + " " + first path-shaped arg from tool_input` (project-root strip) |
  | `PostToolUse` (ok) | `EventToolResult` | `↳` | `tool_response` first non-empty line, truncated |
  | `PostToolUse` (error) | `EventToolError` | `⚠` | `tool_response` first line, truncated |
  | `Stop` | `EventSuccess` | `✓` | `"skill complete (N turns)"` if turn count available, else `"skill complete"` |
  | `Notification` | `EventSystem` | `·` | `message` field if present |
  | Any other event | `EventSuppressed` | — | — |
  Path-shaped arg detection reuses the existing PLAN-2 / F6 logic (`projectRoot` strip on tokens shared with `Read`/`Write`/`Edit`/`Glob`/`Grep`/`LS`/`NotebookEdit` `file_path`-style keys).
- **Pre/Post correlation.** Pre carries `tool_use_id` in its payload; Post echoes the same id. The `pipelineModel` ingest path (FC) inserts the Pre's event index into the in-flight map, then on the matching Post either:
  1. **Default (decided in question 6):** append the Post as a _new event_ immediately after the Pre's index — render keeps them adjacent because the Post arrives in the same throttle batch in practice and the slice is append-only. If anything is appended between Pre and Post (a `Stop`, a `UserPromptSubmit`), the Post still appends at the tail; the in-flight map is only used to retire the id and to look up Pre's body when the Post wants to suffix the tool name. **No re-ordering of the events slice is performed**, so the Post is "adjacent" only when the bridge delivered them adjacently — which is the common case (Pre and Post for the same tool_use_id are emitted at the same instant by claude's hook chain).
  2. If a Post arrives with no matching Pre (bridge dropped a frame, or events truly out of order), the renderer still produces a row with the tool name elided.
- **`RenderedEvent.Raw`.** PLAN-2 / F3's `styleRawJSON` and `styleBoth` modes read `RenderedEvent.Raw`. For hook events the raw form is the bridge JSON marshalling of the `HookEvent` (one-line compact) — sufficient for the user to inspect what the bridge saw.
- **Tests.** `event_renderer_test.go: TestRenderHookEvent` table-driven against payload fixtures captured from a real `hook-events.jsonl`:
  - `testdata/hook_event_pretooluse_read.json` → `🔧 Read .../config.yaml` (with project-root strip).
  - `testdata/hook_event_posttooluse_ok.json` → `↳ <first line of tool_response>`.
  - `testdata/hook_event_posttooluse_error.json` → `⚠ <first line>` with `EventToolError`.
  - `testdata/hook_event_stop.json` → `✓ skill complete (N turns)`.
  - `testdata/hook_event_user_prompt.json` → `? <prompt first line>`.
  - `testdata/hook_event_unknown.json` → `IsDisplayable() == false`.
    Capture the fixtures from `/home/diegos/_dev/ape-web-sandbox/greeter/_output/pipelines/governance/20260521-132101-c286866/hook-events.jsonl` (verified during investigation to contain all required event types).

### FC: Wire interactive dispatch to the unified model

- **Problem.** `internal/apecmd/pipeline_interactive_tui.go: runWithInteractiveTUI` builds the soon-to-be-deleted `InteractiveModel`. `InteractiveObserver.HookEventFromBridge` calls `m.PushHook(at, event, step)` — a 3-string API that loses the payload.
- **Fix.**
  - `runWithInteractiveTUI` constructs the unified model:
    ```go
    model := tui.NewPipelineModel(spec, runCancel, projectRoot,
        tui.WithEventSource(tui.SourceHookEvents),
        tui.WithAwaitReplySender(func(s string) { rt.SendMessage(s) }),
    )
    ```
  - `InteractiveObserver` shrinks. `HookEventFromBridge` becomes a tea-message send:
    ```go
    func (o *InteractiveObserver) HookEventFromBridge(h orchestrator.HookEvent) {
        o.program.Send(hookEventMsg{hook: h})
    }
    ```
    The full `HookEvent` rides the message; the model invokes `RenderHookEvent` inside `Update` (same throttle path used by `stepLineMsg`).
  - `BridgeRuntimeOptions.OnHook` callback updates accordingly — passes the full `HookEvent` rather than `(at, event, step)`.
- **Step indexing.** Hook events carry `step="stagename/idx-skill"` (verified — sample step from sandbox: `pattern-governance/1-apex-pattern-reconciliation`). The model parses the stage name on ingress in `Update` and routes the rendered event to that stage's `events` slice. If the stage name doesn't match a known stage (defensive — could happen if the bridge fires after `pipelineDoneMsg`), the event is dropped silently.
- **Throttle.** Hook events ride the existing `throttleTickMsg` / `pendingLines` queue. Same 33ms (~30 Hz) ceiling.
- **Tests.**
  - `internal/apecmd/pipeline_interactive_tui_test.go` (new) — smoke that `runWithInteractiveTUI` constructs the unified model with the right options. Compiled-in only; no claude subprocess.
  - End-to-end against the `ape-framework-update` fixture in the eval system (which is intentionally minimal — single stage, single skill, fast to run): `python3 -m apex_eval.cli pipeline --fixture ape-framework-update --name framework-install` after the build. Visual snapshot via `--preserve` to inspect the resulting `_output/pipelines/.../hook-events.jsonl` and confirm the rendered model would match.

### FD: Final-report parity for interactive mode

- **Problem.** Interactive mode quits the moment the pipeline finishes; the user can't review per-stage event counts or scroll through completed stages. Programmatic mode keeps the TUI open with a final-report row.
- **Fix.** Falls out automatically once the model is unified. `phaseCompleted` + the synthetic `📊 final report` row + the completion banner already exist on `pipelineModel`. Per-stage event counts (`len(st.events)`) populate from the hook-event path — the report row shows the same per-stage summary regardless of source.
  Pipeline-level final error in interactive mode is delivered as `pipelineDoneMsg{err}` exactly as today (sent by the `runErrCh` goroutine in `runWithInteractiveTUI`).
- **Tests.** `pipeline_test.go: TestFinalReportHookEventSource` — construct a hook-event-sourced model, ingest a small synthetic stream, send `pipelineDoneMsg{}`, verify:
  - `phase == phaseCompleted`.
  - `cursorIdx == len(stages)` (the final-report row).
  - `View()` contains the per-stage event counts.
  - `q` quits cleanly (returns `tea.Quit`).

### FE: Cleanup

- Delete `internal/tui/interactive.go` (model + view code only).
- Move the await-modal message types (`awaitPendingMsg`, `awaitResolvedMsg`) and the `awaitReplySender` callback type into `internal/tui/pipeline.go` so they live with their consumer.
- Keep `InteractiveObserver` in `internal/tui/` but rename to `BridgeObserver` (it's no longer "Interactive-specific" — it's the adapter for the hook-event source). One source file, ≤200 lines.
- Update `docs/reference/tui-keybindings.md`: collapse the per-mode keybind tables into one. Add a paragraph documenting which keybindings differ by source (the await-modal `Enter`/`Esc` shortcuts only fire when the modal is active).
- CHANGELOG entry under `## Unreleased`:

  ```
  ### Pipeline TUI

  - Unified the interactive (`--tui`) and programmatic (`--tui -P`)
    Bubble Tea models. Interactive mode gains dual-panel rendering,
    cursor + scroll navigation, render-style cycling, narrow-layout
    fallback, and the final-report completion row that programmatic
    mode had since PLAN-2.
  - Fixed left/right panel border misalignment when the event panel
    overflowed its visible window (regression visible since PLAN-2
    landed F3 / styleBoth).
  ```

## Scope — OUT

- **Runner, bridge, commit machinery.** Untouched. The TUI is a consumer of events the bridge already publishes; plan-7 changes only the consumer.
- **Web TUI (`--web` and `--web -P`).** The HTMX server in `internal/web/` keeps its existing template-based renderer. Re-verify the `--web -P` cell of the invocation matrix after FC lands, but no code changes there. If web rendering should also reuse the unified model, that's a future plan (would require bridging Go template rendering with the lipgloss-oriented `RenderedEvent` shape).
- **New keybindings.** The keybind set carries over from PLAN-2 untouched. No new shortcuts.
- **`await_message` reply-input UX changes.** Keep the existing `bubbles/textinput` widget; it just renders inside the unified modal slot.
- **Per-step elapsed time in interactive mode.** PLAN-6 already has session telemetry; surfacing per-step durations in the unified model is a follow-up — Stop hooks carry enough info but the wiring to correlate them with per-step start is non-trivial and orthogonal to layout parity.
- **Cost / token panels.** Outside this plan.
- **Tool-call argument syntax highlighting.** Programmatic mode has none either; cross-mode follow-up if desired.

## Risk register

- **R1 — Hook event ordering vs throttle.** The bridge can in principle deliver `PostToolUse` before its `PreToolUse` under heavy load or buffer-overflow recovery. FB's in-flight map handles missing-Pre case (renders Post with elided tool name); appending-only means the order on screen mirrors the bridge's order, so the worst-case visual is `↳ result` appearing before `🔧 tool`, which still reads correctly.
- **R2 — Width-change reflow.** PLAN-2's `narrowLayoutThreshold` covers the layout flip; `composePanelBody`'s strict budget enforcement protects against geometry drift after a `tea.WindowSizeMsg`. No new risk introduced.
- **R3 — Behavior parity for programmatic mode.** All existing `pipeline_test.go` cases run with `SourceStreamJSON` (the default) and must continue to pass byte-for-byte. The constructor option is purely additive. Verify in CI before merging.
- **R4 — Throttle queue mixing two event types.** `pendingLines []pendingEvent` already carries `RenderedEvent`. After FC, the queue carries events from both `stepLineMsg` and `hookEventMsg` paths but the queue type is unchanged. Single-source-per-run is enforced by `WithEventSource`; double-feeding is a runtime invariant violation worth a defensive `panic` in tests.
- **R5 — Interactive observer rename to `BridgeObserver`.** A grep across the repo confirms only `internal/apecmd/pipeline_interactive_tui.go` and `internal/tui/interactive.go` reference the name. The rename is mechanical; CHANGELOG mention is for users following the code, not an end-user concern.

## Test plan

### Unit tests added or extended

- `internal/tui/pipeline_test.go`
  - `TestPanelRowBudget` — row-budget invariant under all three render styles.
  - `TestPipelineModelHookEventSource` — hook-event ingestion produces expected per-stage events.
  - `TestAwaitModalRequiresSender` — nil sender ⇒ modal unreachable.
  - `TestFinalReportHookEventSource` — completion banner + per-stage counts under hook-event source.
  - `TestPrePostCorrelation` — Pre/Post pair on same `tool_use_id` produces two adjacent rows in the events slice; orphan Post produces a row with elided tool name.
- `internal/tui/event_renderer_test.go`
  - `TestRenderHookEvent` — full table against fixture payloads.
  - Fixtures committed under `internal/tui/testdata/hook_events/`.

### Existing tests that must keep passing

- All of `internal/tui/pipeline_test.go` (unchanged path: `SourceStreamJSON` default).
- All of `internal/tui/event_renderer_test.go` (stream-json renderer unchanged).
- All of `internal/apecmd/*_test.go` — the constructor signature is additive.

### Manual matrix verification

After FC lands, rerun the 11-cell PLAN-6 invocation matrix (documented at the top of the `apex_process_ape@main` continuation notes for 2026-05-21). The cells that must change visibly:

- `design --tui` and `governance --tui` and `epics --tui` — now show dual panels, scroll, render-style cycle, final report.
- `design --tui -P`, `governance --tui -P`, `design --no-tui -P`, `design --no-tui`, `design --web`, `design --web -P`, `design --eval` — no visual change. Byte-for-byte parity with pre-plan-7 against the same fixture is the regression bar.

For each interactive cell, verify:

- Left panel populates with `🔧 Read foo.md` / `↳ <result>` / `? <prompt>` / `✓ skill complete` rows tagged to the correct stage.
- Right panel stages list selectable with `↑↓`.
- `PgUp` / `PgDn` scroll the event panel; left/right borders stay aligned at all scroll positions.
- `r` cycles render styles; left/right borders stay aligned in `styleBoth` (the FB amplifier case).
- `q` opens the quit modal; `y` cancels the run; `Enter` pins the stage.
- On pipeline completion, the `📊 final report` row appears in the stages list and selecting it shows per-stage event counts.

### Fixture coverage

The eval system's `ape-framework-update` fixture is the lightweight smoke target. Its single stage runs in well under a minute, exercises hook events end-to-end, and produces a `hook-events.jsonl` we can diff against the rendered model under the new path.

## Implementation order summary

| Phase | Slug                            | Touches                                                                                 | LOC est. |
| ----- | ------------------------------- | --------------------------------------------------------------------------------------- | -------- |
| F0    | row-budget invariant            | `internal/tui/pipeline.go` (composePanelBody, renderEventPanel, viewNarrow), tests      | ~120     |
| FA    | carve shared model              | `internal/tui/pipeline.go` (options, fields, messages)                                  | ~180     |
| FB    | hook-event renderer             | `internal/tui/event_renderer.go` (+ fixtures + tests)                                   | ~220     |
| FC    | wire interactive dispatch       | `internal/apecmd/pipeline_interactive_tui.go`, `internal/tui/interactive.go` (observer) | ~80      |
| FD    | final-report parity (inherited) | tests only                                                                              | ~40      |
| FE    | cleanup                         | delete `interactive.go` model, rename observer, docs, CHANGELOG                         | ~60      |

Total: ~700 LOC net change, mostly additive; ~150 LOC deleted in FE.

## Open issues / follow-ups (deferred)

- **Per-step elapsed time in interactive mode.** Requires correlating `Stop` hooks back to the matching step start using the session/step telemetry PLAN-6 already collects. Mechanically straightforward; out of scope here to keep plan-7 focused on layout + ingestion parity.
- **Tool-call argument syntax highlighting.** Programmatic mode has none either; would benefit both modes. Cross-mode follow-up.
- **Web renderer unification.** If a future plan wants `--web -P` to share the unified model, the `RenderedEvent` shape is the natural seam — the web renderer would consume the same slice and emit HTMX fragments instead of lipgloss strings. Not in plan-7.
- **`Notification` event mapping.** FB renders these as `EventSystem`. If they prove useful enough to deserve a dedicated glyph / row type, follow-up.

## Post-implementation addenda (2026-05-21)

Recorded after the v0.0.12 release shipped, capturing the deviations from this plan that surfaced during implementation and live verification. The plan body above is the forward-looking artifact; this section is the historical reconciliation. Full narrative — including dead-ends, the `sed` self-reference incident, and the lipgloss `Height()` misunderstanding — lives in `_output/implementation-notes.html`.

### Deviation 1 — F0 needed a second pass (visual wrap, not just logical lines)

The first-pass fix landed `composePanelBody` enforcing an exact logical-line budget and capped `renderEventPanel` output at `height` lines. Unit tests passed. Live `ape pipeline design --tui` against the greeter sandbox still showed misalignment.

Root cause: `composePanelBody` operates in newline-separated lines and can't see what lipgloss will do with individual rows that overflow the panel's content-area width. The right-panel row `"> ✓ create-architecture 7m36s"` (29 visual cells) against a 28-cell content area soft-wrapped inside lipgloss, growing the right panel one row past `panelHeight + 2`.

Second-pass fix:

- `renderStageList` now takes a `width int` (the visual-cell budget per row) and routes each row through a new `truncateForVisualWidth` helper — rune-aware, emoji-safe via `lipgloss.Width`.
- `pipelinePanelStyle` is constructed with `.MaxHeight(panelHeight + 2)` on every panel as a defensive cap. Belt-and-suspenders: if any future content sneaks past per-row truncation, lipgloss hard-caps the box.
- New `TestRenderSmoke_DesignPipelineWrap` reproduces the failure on a 6-stage realistic spec; without the fix it reports `leftPanel=38 rightPanel=39`.

Plan correction: F0's "Diagnosis" identified two causes (trailing newline + `styleBoth` slice math). A third cause — lipgloss soft-wrapping overlong rows — was missed because the original unit tests used synthetic short event bodies and short stage names.

### Deviation 2 — `inFlight` tool-use map dropped

Plan FB called for `pipelineModel.inFlight map[string]map[string]int` to correlate `PreToolUse` and `PostToolUse` events by `tool_use_id`. Implementation found this unnecessary once we accepted append-only ordering (Q6 decision before the plan was written): the bridge delivers Pre and Post adjacently in practice, so the events slice naturally renders them next to each other without any explicit correlation state. The map would only be needed if a future enhancement wants the Post row body to include the Pre row's tool name (e.g. `↳ Read result: ...`); deferred.

`pipelineModel` therefore ships without the `inFlight` field. Plan FA's "Field additions" list is one entry shorter than written.

### Deviation 3 — Hook events drop in interactive mode (caught at live verification)

FC's plan body assumed `HookEvent.Step` would be populated by the time it reached the unified model. It isn't — `ape notify` cannot populate `Step` under tmux because the interactive runner has no step-bind plumbing on the wire. `interactiveCore.FeedHook` was filling `step` locally for its runlog write only, not mutating the caller's `HookEvent` copy. The observer immediately downstream therefore saw `h.Step==""`, `stageFromHookStep("")` returned `""`, and every hook event was dropped on the floor — producing the user-reported empty left panel.

Fix: new `interactiveCore.ActiveStep()` getter (mutex-guarded, mirrors `FeedHook`'s read pattern) plus a two-line backfill in `runWithInteractiveTUI`'s `OnHook` callback:

```go
OnHook: func(h orchestrator.HookEvent) {
    core.FeedHook(h)
    if h.Step == "" {
        h.Step = core.ActiveStep()
    }
    obs.HookEventFromBridge(h)
},
```

No unit test was added at the `OnHook` boundary — that path is integration-shaped (bridge runtime + tmux + `ape notify`) and the model-side `TestPipelineModelHookEventSource` already locks in "hookEventMsg with matching step ⇒ event lands in stage's events slice." The bug was in the wiring above the model; the verification path is manual re-run on the sandbox.

Lesson logged: an `apecmd`-level fake-bridge integration test would catch this class of bug. Deferred — would benefit from a broader fake-bridge harness that PLAN-6 also could have used.

### Files & LOC reconciliation

Plan estimated ~700 LOC net. Actual shipped (`dfd19ec`): **21 files changed, +1678 / −485**. Breakdown:

- Plan-7 LOC budget (700): close to plan's estimate at +920 / −472 for the production code.
- Extra +750 lines: the new `internal/tui/render_smoke_test.go` (visual-wrap reproductions added during the second-pass fix), 7 JSON fixture files under `testdata/hook_events/`, the plan file itself, and CHANGELOG / docs entries.

The deletion count (−485) closely matches the plan's projection (~150 deleted in FE plus stepLineMsg/await-modal refactor displacement in the rewritten pipeline.go).

### Out-of-scope addition: ape version mascot

After PLAN-7 landed, an ASCII-art mascot was added to `ape version` (CLI scope, not pipeline-TUI scope). Captured in CHANGELOG v0.0.12 under "CLI". Mentioned here only because the v0.0.12 release commit (`dfd19ec`) bundles both PLAN-7 and the mascot; separating them would have produced two commits with no functional advantage.

### Release

- Tag: `v0.0.12` (annotated, unsigned — matches v0.0.11 convention).
- GitHub Actions release workflow: run `26249456127`, completed `success`.
- Artifacts: 6 platform archives + `ape_checksums.txt` + cosign keyless Fulcio cert + signature.
- Signature verified against the GitHub OIDC issuer with cosign `verify-blob` — `Verified OK`. Chain: Fulcio cert → checksums signature → binary hashes match.
