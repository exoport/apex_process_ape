---
plan_id: PLAN-1
created_at: 2026-05-10
implemented_at: 2026-05-10
status: done
tags:
  - pipeline-tui
  - framework-cli
  - ux-pass
  - breaking-change
  - cross-repo
summary: Reshape the ape UX after v0.0.6. Five items — (I1) clean the actionable error when `ape framework status` runs in an uninitialized project; (I2) quit confirmation in the pipeline TUI with subprocess teardown; (I3) split `ape framework update` into a separate `setup` (initial install + bootstrap) and `update` (refresh-only) with strict error semantics either way; (I4) three-panel pipeline TUI (live event stream / stage list / status strip) with iteration over completed stages; (I4b) live per-event streaming from the spawned `claude` subprocess via a new `OnStepLine` observer event, with human-friendly rendering of stream-json events in both TUI and `--no-tui` modes; (I5) README + Diátaxis docs refresh. Coordinated with a single eval-side change to switch the harness from `framework update` to `framework setup` on bare_init fixtures.
origin:
  - 2026-05-10 follow-up session after PLAN-8 closed (project-local pipelines + framework sync) — first round of real-use feedback against v0.0.6.
  - User-reported pain points: opaque metadata-missing error, no quit confirmation in the TUI, stale-step output between stages, conflation of first-time setup with refresh.
---

# PLAN-1: Pipeline UX and framework setup separation

## Goal

Close five UX gaps observed in real use of `ape v0.0.6`, three of them surface-level and two architectural:

1. **I1** — When `ape framework status` runs against a project that never installed the framework, the error text leaks Go's `open …: no such file or directory` underneath an otherwise actionable message. Strip the Go-level trailer; keep the actionable hint.
2. **I2** — The pipeline TUI quits on `q` or `Ctrl+C` without warning, and the in-flight `claude` subprocess keeps running in the background until it returns on its own. Add a confirmation modal; SIGTERM the spawned subprocess on confirmed quit; preserve the existing `Ctrl+C × 2 within 1s` force-quit as an emergency escape.
3. **I3** — `ape framework update` today does two unrelated jobs: first-time setup (install skills, install pipelines, seed `_apex/config.yaml` interactively via the bootstrap TUI, write `_apex/framework.yaml`) and subsequent refresh (re-copy skills + pipelines, refresh `_apex/framework.yaml`). Split into:
   - `ape framework setup` — initial install + bootstrap. Errors if already set up.
   - `ape framework update` — refresh-only. Errors if not yet set up.
     Eval-side harness adjusts to call the right command based on whether `_apex/framework.yaml` exists in the temp dir.
4. **I4** — Replace the current two-panel TUI (stage list + last-step output) with a three-panel layout: top-left live event stream (~70% width), right stage list (~30% width), bottom status strip (2 rows). Add keybindings for iterating over completed stages and pinning the event panel to any of them.
5. **I4b** — Stop waiting for the `claude` subprocess to finish before showing anything. Plumb its stdout pipe through a new `OnStepLine(stage, idx, line)` observer event. Parse the line as a `claude --output-format stream-json` NDJSON event and render a human-friendly one-line summary (`🔧 Read <path>`, `✎ <text>`, `↳ <result>`, `✓ <skill> pass`, etc.). Apply the same rendering in `--no-tui` mode so log capture and CI runs benefit too.
6. **I5** — Reflect all of the above in the README, the Diátaxis tier docs (`docs/{how-to,reference,explanation}/`), and the CHANGELOG. Final step after the code lands.

The net effect: a first-time user runs `ape framework setup` once, sees a TUI bootstrap, and gets a project that's ready for `ape pipeline <name>`. The pipeline TUI no longer freezes on a stale step's output between stages; it streams progress live and lets the user scroll back through completed stages. Quitting is intentional; subprocesses don't survive the user's exit.

## Why now

1. **v0.0.6 just shipped** and the bare_init regen of `ape-gf-hello-world` in the eval (PLAN-8 step 8) exercised the install + pipeline flow end-to-end against real LLM calls. Three of these five items surfaced as concrete pain in that run.
2. **The bootstrap-vs-refresh conflation** in `framework update` is a fault line we can fix cheaply now (one breaking CLI change, one coordinated eval-harness adjustment) before more projects start scripting against it.
3. **The streaming gap** (no live progress between step completions) is the single biggest "is this thing alive?" complaint when a stage runs for 10+ minutes. The Observer interface and the existing TUI architecture support adding a new event with minimal disruption.
4. **The not-installed error** is a tiny but frequent first-run misstep: users get a Go syscall error inside an otherwise helpful message. The cost to fix is trivial; the cost of leaving it is one onboarding paper-cut per new user.

## Scope — IN

### I1: clean `ape framework status` not-installed error

- In `internal/framework/metadata.go`, the `ReadMetadata` function currently wraps `fs.ErrNotExist` with `%w`, producing:
  ```
  framework metadata not found at /path/_apex/framework.yaml — run "ape framework update" to install: open /path/_apex/framework.yaml: no such file or directory
  ```
- Introduce a typed error `NotInstalledError` with `Path string`, satisfying `errors.Is(err, fs.ErrNotExist)` via an `Unwrap()` returning `fs.ErrNotExist` (the sentinel) — so programmatic callers still pattern-match cleanly. `Error()` renders:
  ```
  framework metadata not found at /path/_apex/framework.yaml — run "ape framework setup" to install
  ```
  (Updated wording will say `setup` once I3 lands; pre-I3 the wording stays `update` — minor sequencing detail.)
- Update `TestReadMetadata_ActionableErrorOnMissingFile` to assert the new error text and the typed error.
- `internal/apecmd/framework.go` already prints `Error: %s` to stderr — no change needed there beyond verifying it renders without the trailer.

### I2: quit modal + subprocess teardown

- Add a `modalState` enum to `pipelineModel` in `internal/tui/pipeline.go` (`modalNone`, `modalQuitConfirm`).
- Track Ctrl+C timestamps on `pipelineModel` to support the double-tap force-quit.
- Update the `tea.KeyMsg` branch:
  - First `q` or `Ctrl+C` while not in modal: set `modalQuitConfirm`, record timestamp.
  - Second `Ctrl+C` within 1s: bypass the modal, call the cancel function, `tea.Quit`.
  - In `modalQuitConfirm`: `y` confirms (cancel + quit), `n` / `Esc` dismisses.
- The cancel function for the in-flight `claude` subprocess: the runner already passes `ctx context.Context` to `runClaude`, which is the cmd's own context (via `exec.CommandContext`). The `runWithTUI` caller in `internal/apecmd/pipeline.go` already creates a cancellable context (`context.WithCancel(cmd.Context())`); thread the `cancel` function into the TUI model so a confirmed quit invokes it. `exec.CommandContext` then sends SIGKILL to the subprocess once the context is canceled.
- Refinement worth pursuing: switch the runner's spawn to attach a process group and explicitly `syscall.Kill(-pgid, syscall.SIGTERM)` first, then `SIGKILL` after a grace period. The `claude` subprocess may itself spawn child processes (it routinely launches sub-agents); killing the whole group avoids orphans. Out of scope for v1 if it adds complexity — context cancellation suffices for the common case.
- Render the modal as a centered overlay using `lipgloss.Place` over the dimmed underlying view.
- Tests: model-level unit test that exercises both single-tap modal and double-Ctrl+C escape; integration test that verifies the cancel function is invoked on confirmed quit.

### I3: split `framework update` into `setup` + `update`

- `internal/apecmd/framework.go`:
  - Rename the existing update subcommand to `setup`. Keep the cobra registration as `update` for now to ensure compat-mode error messages (see below); the actual handler logic moves to `newFrameworkSetupCmd`.
  - `setup` handler: validates the metadata file is **absent** (or `--force` is set). On violation, errors with `framework already installed (per _apex/framework.yaml). Run "ape framework update" to refresh, or "ape framework setup --force" to re-bootstrap (resets project_name and extensions).` Exit code `exitCodeAlreadyInstalled`.
  - `update` handler (new shape): validates the metadata file is **present**. On violation, errors with `framework not installed at <path>. Run "ape framework setup" first.` Exit code `exitCodeNotInstalled`. Otherwise refreshes skills + pipelines + rewrites metadata. Does **not** call the bootstrapper.
- `internal/framework/install.go` — split `Update(ctx, *UpdateOptions)` into `Setup` and `Update` orchestrators. Share the lower-level copy primitives (`installSkills`, `installPipelines`). `Setup` reads the bootstrapper; `Update` doesn't.
- Update existing tests in `internal/framework/install_test.go`: rename `TestUpdate_*` covering bootstrap paths to `TestSetup_*`; add tests for the refusal cases (`Setup` against already-installed, `Update` against missing metadata).
- Add an `apex` feature flag concept later if needed; for v0.0.7 the split is unconditional.

### I3 eval-side coordination

- `apex_eval/runner.py`: `_invoke_ape_framework_update` currently calls `ape framework update --project-name X --extensions Y` for `bare_init` fixtures. Branch on the temp dir's `_apex/framework.yaml` presence:
  - File absent → `ape framework setup --project-name X --extensions Y`.
  - File present → `ape framework update`.
- Rename the helper to `_invoke_ape_framework_install` (since it does both setup and update via the branch). Update call sites and tests.
- Bump `ape_min_version` in `fixtures/ape-gf-hello-world/fixture.yaml` and `fixtures/ape-framework-update/fixture.yaml` from `v0.0.6` to `v0.0.7`.
- The harness's `bare_init` step always starts from a fresh temp dir, so in practice `setup` is what runs every stage. Branching is still required because a future fixture might persist state across stages.

### I4: three-panel pipeline TUI

- Replace `pipelineModel`'s flat panel structure with a layout manager:
  - **Top-left pane** (~70% width × height-2): live event log for the currently-active stage (or the pinned stage in review mode). Streams `OnStepLine` events (from I4b) as rendered one-liners. Uses `bubbles/viewport` for scrolling when paused.
  - **Right pane** (~30% width × height-2): ordered stage list. Each row: status glyph, stage name, duration. Cursor cursor for the focused row.
  - **Bottom strip** (2 rows): row 1 = current/selected stage status summary; row 2 = keybind hint footer.
- Modes:
  - **Live** (default while pipeline is running): event panel auto-follows the active stage, auto-scrolls to bottom.
  - **Paused** (SPC pressed): event panel stops auto-scrolling; user can PgUp/PgDn within the active stage's events.
  - **Pinned** (Enter on a stage row): event panel shows the selected stage's full event history; keybind hint footer changes to reflect.
- Keybindings:
  - `↑` / `↓` (or `k` / `j`): move cursor in the stage list.
  - `Enter`: pin event panel to the selected stage.
  - `L` or `Esc`: return to Live mode (auto-follow + auto-scroll).
  - `SPC`: toggle pause / auto-scroll.
  - `PgUp` / `PgDn` / `Home` / `End`: scroll inside the event panel.
  - `r`: cycle event-line rendering: human → raw JSON → both.
  - `q` / `Ctrl+C`: quit modal (per I2).
- Narrow-terminal fallback: when `width < 90` cols at startup or after a SIGWINCH, drop the right pane and render the stage list as a single-line horizontal stepper at the top. Iteration keys still work (cursor moves along the stepper).
- Render performance: 30 Hz cap via a `tea.Tick(33ms)` flush loop. Incoming `OnStepLine` events drop into a model queue; the tick processes them and re-renders once. Bursts of 100+ lines/sec coalesce.
- Tests: model-level state-machine tests for Live → Paused → Pinned → Live; window-resize handler that switches to narrow-terminal fallback; per-event-render-style cycling.

### I4b: live event streaming via new `OnStepLine` observer event

- `internal/pipeline/runner.go`:
  - Extend the `Observer` interface with `OnStepLine(stage string, idx int, line string)`. Backward-incompatible at the interface level; bump the package's documented signature.
  - Refactor `runClaude` to use `exec.Cmd.StdoutPipe()` + a `bufio.Scanner` in a goroutine. For each line: append to the in-memory buffer (still returned to `OnStepEnd`), then `notify(observer, ...)` the line. The `cmd.Stderr` is similarly captured; stderr lines are forwarded with a sentinel prefix or via a separate `OnStepStderr` if useful.
- `internal/tui/pipeline.go`:
  - Add `stepLineMsg{stage string, idx int, line string}`. The TUI observer forwards `OnStepLine` to it.
  - Maintain a per-step ring buffer of rendered events (cap 1000? — see open issues). The "full event history" for a stage = concatenation of its steps' ring buffers.
  - New file `internal/tui/event_renderer.go`: takes a raw JSON line, parses it as the next claude stream-json event, returns a `RenderedEvent{Glyph, Body, Color}` value. Falls through to `RenderedEvent{Glyph: "?", Body: raw, Color: dim}` for any line that doesn't parse.
- Event renderer mapping:
  - `assistant` / `text` content block → `✎ <first line, 80 chars>`
  - `tool_use` name=`Read` → `🔧 Read <relative_path>`
  - `tool_use` name=`Edit` → `🔧 Edit <relative_path>`
  - `tool_use` name=`Write` → `🔧 Write <relative_path>`
  - `tool_use` name=`Bash` → `🔧 Bash <command first 60ch>`
  - `tool_use` name=`Grep` → `🔧 Grep "<pattern>" [<path>]`
  - `tool_use` name=`Glob` → `🔧 Glob "<pattern>"`
  - `tool_use` name=`Task` → `🔧 Task <subagent> "<desc 40ch>"`
  - `tool_use` name=`WebFetch` / `WebSearch` → `🔧 <name> <host>` / `🔧 WebSearch "<query 40ch>"`
  - `tool_use` other → `🔧 <name> <one-line summary>`
  - `tool_result` success on Read/Edit/Write → **suppressed** (noise; success is implicit)
  - `tool_result` success non-trivial → `↳ <summary 80 chars>`
  - `tool_result` error → `↳ ⚠ <error head 60ch>` (red)
  - `result` (skill completion) success → `✓ <skill> pass` + judge score if available
  - `result` (skill completion) error → `✗ <skill> fail · <reason>` (red)
  - 5s idle (no events) → emit `‥` heartbeat dot in-place (dim grey)
- `--no-tui` mode (`internal/apecmd/pipeline.go: runPlain` + `plainObserver`):
  - Add `OnStepLine` to `plainObserver`. Print one line per event, timestamped + prefixed:
    `[20:08:42] design · apex-create-architecture · step-04 · 🔧 Read development/planning/prd/index.md`
  - Auto-select `--no-tui` when stdout is not a terminal (already implemented).
  - Optional `--quiet` flag to suppress the live stream in `--no-tui` and only print per-stage summary at completion.
- Tests:
  - `event_renderer_test.go` with fixture stream-json events for each tool type.
  - `runner_test.go` exercise `OnStepLine` via a fake `claude` shim that emits known stream-json events.
  - `plainObserver` test that verifies the timestamped one-liner format.

### I5: docs refresh

- `README.md` (101 → ~150 lines): replace the `framework update` paragraph with `setup` + `update`; mention the live TUI streaming; mention the quit modal; mention `--no-tui` behavior.
- `docs/how-to/install.md`: clarify the order of operations (install ape binary → run `framework setup` → run `pipeline <name>`).
- `docs/how-to/framework-update.md`: split into `framework-setup.md` (new) and `framework-update.md` (refactor). The new `setup` doc explains the bootstrap TUI prompts (project name, extensions). The refactored `update` doc explains refresh-only semantics and the not-installed error case.
- `docs/reference/pipeline-spec.md`: no spec changes, but cross-link to the new TUI keybind reference.
- `docs/reference/framework-yaml.md`: note the file is written by `setup` and refreshed by `update`.
- `docs/reference/tui-keybindings.md` (new): one-page reference for the pipeline TUI's keybindings (per the I4 list above), the modal behavior (per I2), and the event-line renderer's mapping (per I4b).
- `docs/explanation/why-setup-and-update-are-separate.md` (new): Diátaxis explanation tier — why we split, what each command's contract is, how to think about them as a pair.
- `docs/explanation/why-streaming-events.md` (new): rationale for `OnStepLine` and the human-friendly renderer.
- `docs/README.md`: index entries for the new pages.
- `CHANGELOG.md`: `v0.0.7 — BREAKING:` callout listing I1–I5.

## Scope — OUT

- **Pipeline cancellation rollback** — when the user confirms quit mid-stage, the on-disk state is whatever the partial `claude` invocation left behind. We do not roll back, undo commits, or restore files. Cleanup is the user's job (or a future plan).
- **Multi-pipeline parallelism** — the TUI still runs one stage at a time. No change.
- **Rich stream-json features** — the event renderer covers the common Anthropic-CLI events. Unknown event types fall through to a raw-JSON display rather than failing. We do not attempt to render thinking blocks, citations, or attachments specially.
- **`framework setup --force` mode preservation** — `--force` re-bootstraps from scratch. Existing extensions / project_name in `framework.yaml` are NOT preserved; the user re-enters them. (Avoidable later via a `--preserve-bootstrap` flag if needed.)
- **Auto-detect headless and skip the bootstrap TUI** — already handled by the existing `pickBootstrapper` logic; carry forward into `setup` unchanged.
- **`pipeline` command UX changes** — only the TUI changes. The CLI surface (`ape pipeline [name]`, `--prompt`, `--no-tui`, `--output-format`) stays.

## Implementation steps

Each step is one commit on `main`. Tests pass at each step.

### Step 1 — PLAN doc (this file)

This file. Land first so subsequent commits can reference `PLAN-1`.

### Step 2 — I1 (clean error)

- `internal/framework/metadata.go`: introduce `NotInstalledError` type with `Path string`, `Error()`, `Unwrap() error` (returns `fs.ErrNotExist`). Return it from `ReadMetadata` on the not-exist branch.
- `internal/framework/metadata_test.go`: update `TestReadMetadata_ActionableErrorOnMissingFile` to assert the new typed error and the trailer-free message.
- `internal/framework/install_test.go`: update line 396 assertion if the message wording changes.

Acceptance: `ape framework status` in a brand-new tempdir prints exactly one line of error text, no trailing `open …: no such file or directory`.

### Step 3 — I2 (quit modal + subprocess teardown)

- `internal/tui/pipeline.go`: add modal state, key handlers, double-Ctrl+C tracking, render path.
- `internal/apecmd/pipeline.go`: thread `cancel` through into the model.
- `internal/tui/pipeline_test.go` (new): cover both quit paths.
- Optionally: process-group teardown for `claude` subprocesses. If keeping it simple: just rely on `exec.CommandContext` cancellation. Document this in `docs/explanation/why-streaming-events.md` (added in I5) as a known limitation.

Acceptance: pressing `q` shows the modal; `y` aborts and the `claude` subprocess receives SIGKILL within 100 ms (visible in `ps` or via subprocess test).

### Step 4 — I3 (setup/update split)

- `internal/apecmd/framework.go`: add `newFrameworkSetupCmd`, refactor `newFrameworkUpdateCmd` to refresh-only. Wire both under the parent `framework` cobra command.
- `internal/framework/install.go`: split orchestrators. Share lower-level primitives.
- `internal/framework/install_test.go`: refactor coverage, add refusal-case tests.
- Update `docs/how-to/framework-update.md` minimally to point users at `framework setup` for first install (full docs in I5).

Acceptance: `ape framework setup` in a fresh project bootstraps. Re-running errors with the expected message. `ape framework update` in a fresh project errors with the expected message; after `setup`, `update` refreshes.

### Step 5 — eval-side I3 coordination

- `apex_eval/runner.py`: branch helper on `_apex/framework.yaml` presence; rename to `_invoke_ape_framework_install`.
- `fixtures/ape-gf-hello-world/fixture.yaml` + `fixtures/ape-framework-update/fixture.yaml`: bump `ape_min_version: v0.0.7`.
- `tests/` regress the branching.

Acceptance: `pytest tests/ -q` passes; a manual harness dry-run against the new ape binary chooses `setup` for the bare_init flow.

### Step 6 — I4 + I4b (three-panel TUI + streaming)

- `internal/pipeline/runner.go`: add `OnStepLine` to Observer, refactor `runClaude` to stream.
- `internal/tui/event_renderer.go` (new): stream-json event parser + renderer.
- `internal/tui/event_renderer_test.go` (new): per-event tests against fixture JSON lines.
- `internal/tui/pipeline.go`: model refactor for three-panel layout, modes, keybindings, viewport integration, narrow-terminal fallback.
- `internal/tui/pipeline_test.go`: extend with mode-machine tests, resize handler tests.
- `internal/apecmd/pipeline.go`: extend `plainObserver` with `OnStepLine` for `--no-tui` streaming. Add `--quiet` flag.
- `internal/pipeline/runner_test.go`: update Observer fakes to implement `OnStepLine`. Add a streaming-shim test with fixture stream-json output.

This is the largest commit. Land it self-contained but feel free to follow up with a small touch-up commit if anything surfaces during the docs pass.

### Step 7 — I5 (docs)

Per the scope section. Land last so docs match the actual behavior. Include CHANGELOG entry.

### Step 8 — Release v0.0.7

- Tag `v0.0.7` on ape main.
- Push the tag; the `.github/workflows/release.yml` workflow signs the checksums with cosign and uploads tarballs.
- From the eval, run `make ape-release APE_VERSION=v0.0.7`; smoke-verify the cosign signature and that `ape version` reports `0.0.7`.
- Update eval `_apex/framework.yaml` and related artifacts if needed.
- Mark PLAN-1 status `done` and stamp `implemented_at`.

## Open issues to resolve during implementation

1. **Process-group teardown for subprocesses (I2).** `exec.CommandContext` cancellation sends SIGKILL to the immediate child. If `claude` has forked subagents, they could orphan to PID 1. Decide during step 3: ship simple now (just context cancel) and revisit, OR implement `syscall.SysProcAttr{Setpgid: true}` + kill-by-pgid up-front. Recommendation: simple now; flag as a follow-up.
2. **Event ring buffer cap (I4b).** Per-step output can be hundreds of events in a chatty skill; per-stage can be thousands. Cap at 1000 events per step? Higher with viewport-paged history? Risk: stages with very long output silently drop early events. Recommendation: 2000-event ring per step with a "(N events elided)" marker rendered at the head when exceeded. The full unbounded text remains available via the existing `OnStepEnd(output string)` for log-capture downstreams (eval).
3. **Stderr handling (I4b).** Currently `runClaude` captures stdout+stderr into one buffer. With streaming, do we forward stderr lines through `OnStepLine` with a sentinel prefix, or add a parallel `OnStepStderr`? Recommendation: parallel `OnStepStderr` for cleanliness; TUI can choose to merge them visually.
4. **Render-thrashing safeguard (I4b).** 30 Hz cap via `tea.Tick(33ms)` is the plan. If real-world bursts produce visible lag, drop to 20 Hz or buffer the queue more aggressively. No call until we see actual perf.
5. **Pinned-stage scrolling behavior on new events (I4).** When the user is reviewing stage 2 (pinned), should the active stage's incoming events still update its row in the right panel (yes, obviously) AND should the bottom status strip still update for the active stage (less obvious — could be the pinned stage's summary instead). Recommendation: bottom strip follows the focused row (pinned); right-panel rows always live-update.
6. **Eval-side `setup` vs `update` migration (Step 5).** The harness's `bare_init` flow always starts fresh, so `setup` is what runs every stage. But: there's also the `framework_update` _step_ defined in the fixture chain (see `fixtures/ape-gf-hello-world/fixture.yaml` step `framework_update: true`). Question: should the step name itself rename to `framework_install` (which then dispatches to setup-or-update)? Or keep the step name and just have the helper choose internally? Recommendation: keep the step name (`framework_update`); the helper chooses. Cheaper to land; semantics match how users think about it.

## Risks

- **Cross-repo coordination** (Step 4 + Step 5). Land I3 in ape first, then the eval-side change must follow before the next eval pipeline regen — otherwise `ape framework update` calls from the harness will fail. Mitigated by bumping `ape_min_version: v0.0.7` in the eval; old eval clones won't pick up the new ape binary without re-running `make ape-release`.
- **Stream-json schema evolution.** The event renderer parses claude CLI's NDJSON output, which is governed by the Anthropic SDK and may add new event types. The fall-through-to-raw-JSON path keeps this from becoming a hard failure, but unknown events render uglier. Mitigated by version-pinning the supported stream-json types in `event_renderer.go` and surfacing unknowns as `? <type>` rows. Future plan candidate: feature-detect via a stream-json schema query (not currently exposed by `claude --version`).
- **TUI architectural rewrite (I4 + I4b).** Significant Bubble Tea state-machine surface. Mitigated by tests at each modal/mode boundary and by keeping the narrow-terminal fallback simple. Risk: real-world terminal-emulator quirks (especially with `AltScreen` + `viewport`) — we can ship + iterate.
- **Backwards compatibility of Observer interface.** Adding `OnStepLine` is technically a breaking change for any external Observer implementations. Mitigated by the fact that the only known implementations live in this repo (`plainObserver`, `PipelineTUIObserver`); both get updated in the same commit. Document the interface change in CHANGELOG.
- **Subprocess orphan risk** (per Open issue #1). If `claude` forks subagents that survive context-cancel, they continue consuming Anthropic API budget after the user thought they quit. Visible in the user's API dashboard; embarrassing. Process-group teardown closes this gap; defer if too costly.

## Acceptance — plan-1 done

- `ape framework status` against a fresh tempdir prints a single, trailer-free actionable line.
- `ape pipeline <name>` shows a quit confirmation modal on `q` / `Ctrl+C`; confirmed quit kills the in-flight `claude` subprocess.
- `ape framework setup` first-installs; second run errors clearly. `ape framework update` errors clearly on a fresh project; refreshes on an already-set-up one.
- The pipeline TUI streams human-readable lines as the `claude` subprocess produces them. The right panel cursor moves with `↑/↓`; `Enter` pins; `L`/`Esc` returns to live; `r` cycles render styles.
- `--no-tui` mode prints timestamped, human-readable event lines.
- Docs reflect every change; CHANGELOG calls out the breaking shape (rename of `update` semantics).
- `make lint` + `go test ./...` clean on ape; `pytest tests/ -q` clean on eval.
- ape `v0.0.7` tag pushed; cosign-verified install works from the eval via `make ape-release APE_VERSION=v0.0.7`.

## Verification plan

1. **I1 smoke.** `mktemp -d` + `ape framework status --cwd <dir>` → single-line error, no Go trailer.
2. **I2 smoke.** Run a pipeline; press `q`; modal appears; `y` confirms; verify no orphaned `claude` process via `pgrep -af claude` after the TUI exits.
3. **I3 smoke.** `mktemp -d` + `ape framework setup --repo $APEX_FRAMEWORK_REPO --project-name greeter --extensions ext-adrs`. Confirm files installed. Re-run `setup` → error. Run `update` → refresh succeeds.
4. **I4 + I4b smoke.** Run a pipeline; observe `🔧 Read …` lines streaming live; press `↑↓` to navigate; `Enter` to pin a completed stage; `L` to return to live; `r` to cycle render styles.
5. **I4b `--no-tui` smoke.** `ape pipeline design --no-tui | tee log.txt`; confirm timestamped event lines appear in real time (not buffered to end).
6. **Eval coordination smoke.** From eval cwd: `python3 -m apex_eval.cli pipeline --fixture ape-framework-update --regenerate`. Should call `setup` (since bare_init starts fresh) and succeed.
7. **cosign verify.** From eval: `make ape-release APE_VERSION=v0.0.7` — same end-to-end path that worked for v0.0.6.

## Out of band

- Once I4b's renderer settles, consider a `--format json` flag on `ape pipeline` that emits the parsed events as a single NDJSON stream to stdout. Useful for piping into observability tools. Plan-2 candidate.
- The bootstrap TUI (in `framework setup`) shares lipgloss styling and key-handling patterns with the pipeline TUI; after this plan, refactor a small `internal/tui/common` package to share components (Modal, Stepper, ColorPalette). Plan-3 candidate.
- Process-group teardown (Open issue #1) deserves its own follow-up if we want to close the orphan-subagent gap properly. Plan-4 candidate.
