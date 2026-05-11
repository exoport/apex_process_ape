# CHANGELOG

## v0.0.9 (2026-05-11)

PLAN-3: every `ape pipeline <name>` invocation now writes a structured
on-disk record of the run. The artifact unblocks the eval-side
per-skill metrics work (apex_process_framework_eval PLAN-9) and gives
real-project users a "what did that run cost" answer that survives the
TUI closing.

### Behavior changes (no CLI flag breakage)

- **Pipeline runs now leave a manifest on disk.** Every invocation —
  TUI mode, `--no-tui`, eval-harness mode — writes
  `<project_root>/_output/pipelines/<name>/<run_id>/manifest.yaml`
  alongside per-step `.ndjson` captures of the raw claude
  stream-json events and a human-readable `pipeline-report.md`.
  Per-step `cost_usd`, `tokens_*`, `num_turns`, and `duration_seconds`
  are extracted from the terminal `result` event in claude's stream;
  totals roll up at the run level. A `latest` symlink at
  `<pipeline_name>/latest` points at the most recent `<run_id>` for
  easy tailing. PLAN-3 / M1-M5.

- **End-of-run summary prints the report path.** Both the TUI and the
  plain-printer (`--no-tui`) finish a run with a stable
  `📊 report: _output/pipelines/<name>/<run_id>/pipeline-report.md`
  line on stdout. CI logs can link straight to the artifact. PLAN-3
  / M6.

### New CLI flags

- `ape pipeline <name> --manifest-dir <path>` — override the manifest
  root (default: `<project>/_output/pipelines`). Used by the eval
  harness to redirect manifests into its own results tree; available
  to anyone who wants pipeline runs in a non-default location.
  PLAN-3 / M6.

### Internals

- `pipeline.RunOptions` gains `ManifestDir`, `DisableManifest`, and
  `ApeVersion` fields. `DisableManifest` is a library-only escape
  hatch for tests / embedded use; it is not surfaced on the CLI.
- Manifest types live in `internal/pipeline/manifest.go`; the
  on-disk YAML schema is the external contract (the eval reads it).
  Schema is versioned (`schema_version: 1`); future additions are
  forward-compatible.
- `runClaude` accepts an optional `io.Writer` to tee the
  stream-json events to disk in parallel with the Observer.
- `pipeline.ReportPathFor(projectRoot, pipelineName, manifestDir)`
  exposes the most recent report path for embedding callers.

### Docs

- New: [docs/reference/pipeline-run-manifest.md](docs/reference/pipeline-run-manifest.md)
  — schema, status enum, metric provenance, forward compatibility,
  cleanup guidance.
- Updated: [docs/reference/pipeline-spec.md](docs/reference/pipeline-spec.md)
  cross-links the manifest reference.

### Verification

- `make lint` zero issues. `go test ./...` clean across all
  packages, including the three new manifest tests
  (`TestRun_EmitsManifest`, `TestRun_FailedStepCaptured`,
  `TestRun_DisableManifestSkipsTree`).

## v0.0.8 (2026-05-11)

A focused follow-up to v0.0.7 that closes every gap the v0.0.7 smoke
surfaced or that PLAN-1 deferred. Eight independently-shippable
items; see [development/planning/plan-2_pipeline-ux-followups.md](development/planning/plan-2_pipeline-ux-followups.md)
for the full rationale.

### Behavior changes (no CLI flag breakage)

- **Confirmed quit now kills the whole `claude` subprocess tree.**
  Pre-v0.0.8, pressing `q` then `y` (or double-Ctrl+C) SIGKILLed the
  immediate `claude` child but any sub-agents it had spawned via the
  `Task` tool were reparented to PID 1 and continued running until
  they exited naturally — burning Anthropic API budget for minutes
  after the user thought the pipeline was dead. v0.0.8 makes the
  child a process-group leader (`Setpgid=true`) and rewires
  `Cmd.Cancel` to deliver SIGTERM to the whole group, with a
  detached escalator goroutine that SIGKILLs the group after a
  500ms grace period. Linux + darwin only; Windows falls back to
  the existing direct-child SIGKILL. PLAN-2 / F1.

- **The pipeline TUI no longer auto-quits when the pipeline finishes.**
  Pre-v0.0.8, the TUI tore down on the last stage's `OnStageEnd` and
  the user dropped back to the shell with no chance to scroll
  through events. v0.0.8 transitions the model into a
  `phaseCompleted` state instead: a synthetic `📊 final report` row
  appends to the stage list, a completion banner replaces the
  keybind hint (`✓ pipeline complete: N/N stages OK` or
  `✗ pipeline failed: M/N FAILED`), and selecting the report row
  populates the event panel with a per-stage summary (glyph · name ·
  duration · event count · last error). Navigation, scroll, and
  render-style cycling all stay wired; `q` exits directly (no
  confirmation modal — there's nothing to cancel). PLAN-2 / F7.

- **Tool-call event lines render paths relative to the project root.**
  Pre-v0.0.8, sandbox prefixes like `/tmp/ape-v007-smoke-c70b/...`
  ate the event-panel column and the actually-informative suffix
  was truncated. v0.0.8 strips the project-root prefix from
  `Read` / `Edit` / `Write` / `Grep` / `Glob` path arguments at the
  renderer; system paths, `$HOME`-relative paths, and
  framework-source paths pass through unchanged. The TUI and
  `--no-tui` plain mode both apply the same logic. PLAN-2 / F6.

- **`PgUp` / `PgDn` scroll works in any mode.** Pre-v0.0.8, the
  scroll keys were gated behind `Pinned` mode and were no-ops in
  the default `Live` mode. v0.0.8 adds a `userScrolled` flag on the
  model: any scroll key suspends auto-tail, new events arrive
  silently in the background, pressing `End` (or paging back to the
  bottom) rejoins the tail. `Enter` (pin) seeds the scroll offset
  to the tail so the pinned panel opens on the latest events.
  PLAN-2 / F8.

- **`r` cycles event-render style: human → raw JSON → both.**
  Documented in `docs/reference/tui-keybindings.md` since v0.0.7,
  finally wired in v0.0.8. Each rendered event now carries the
  original NDJSON line so the raw / both views are zero-cost
  re-renders. The keybind-hint footer surfaces the active style
  label. PLAN-2 / F3.

- **Single-column layout under width 90.** Narrow terminals (tmux,
  kitty splits, side-by-side editors) drop the right-side stage
  column; the stages collapse to a one-row horizontal stepper
  above the event panel, the event panel takes the full width, the
  cursor stage gets wrapped in `[ ]` for visibility. Widens back
  on `WindowSizeMsg` above the threshold. PLAN-2 / F4.

- **`ape pipeline --no-tui --quiet` suppresses the per-event stream.**
  For CI runs where humans only read the failure summary, the
  per-event stream from v0.0.7's `plainObserver` was noise. v0.0.8
  adds `--quiet`, which returns the plain observer to its
  pre-PLAN-1 / I4b shape: stage / step start+end markers and
  failure summaries print, `OnStepLine` is a no-op. Combining
  `--quiet` with the interactive TUI is refused with an actionable
  error. PLAN-2 / F5.

- **30 Hz render throttle on TUI event flushing.** Pre-v0.0.8 the
  per-event re-render cadence was implicit (no measured pain on
  current workloads, but unbounded as terminal multiplexers
  introduce per-frame latency). v0.0.8 buffers incoming stepLines
  into a queue and flushes them in a single Update pass every
  33ms (~30 Hz), independent of incoming line rate. PLAN-2 / F2.

### Notes

- The `docs/explanation/why-streaming-events.md` § "What it cost"
  caveat about orphan sub-agents surviving Ctrl+C is closed by
  F1. The new escalator-goroutine cancel path is exercised by
  `internal/pipeline/runner_unix_test.go` —
  `TestRunClaude_KillsProcessGroupOnCancel` builds a shell shim
  that forks a SIGTERM-trapping grandchild and asserts both PIDs
  are reaped within 1.5s of context cancellation.
- `NewPipelineModel` gains a trailing `projectRoot string`
  parameter for F6. Out-of-tree callers will need a one-line
  update.

## v0.0.7 (2026-05-10) ⚠️ BREAKING

A pipeline-UX pass driven by real v0.0.6 use. The CLI surface and the
Go-API surface both move; see [docs/how-to/framework-setup.md](docs/how-to/framework-setup.md)
for the new install flow and the planning doc
[development/planning/plan-1_pipeline-ux-and-framework-setup.md](development/planning/plan-1_pipeline-ux-and-framework-setup.md)
for the full rationale.

### Breaking changes 💥

- **`ape framework update` is now refresh-only.** The first-install path
  is `ape framework setup`. Strict refusal semantics either way:

  ```text
  $ ape framework update            # fresh project
  Error: framework metadata not found at <path> — run "ape framework setup" to install

  $ ape framework setup             # already-installed project
  Error: framework already installed at <path> — run "ape framework update" to refresh,
  or "ape framework setup --force" to re-bootstrap (resets project_name and extensions)
  ```

  Scripts and CI tooling that call `framework update` for first-time
  installs must branch based on `_apex/framework.yaml` presence. The
  apex_process_framework_eval harness does this in
  `apex_eval/runner.py:_invoke_ape_framework_update`.

- **`pipeline.Observer` gains `OnStepLine(stage, idx, line)`.** Any
  external Observer implementation must add the method. The only known
  implementations live in this repo (`PipelineTUIObserver`,
  `plainObserver`); both are updated.

- **`NewPipelineModel(spec)` → `NewPipelineModel(spec, cancel)`.**
  The TUI model takes a `context.CancelFunc` that the confirmed-quit
  modal invokes to SIGKILL the spawned `claude` subprocess. A `nil`
  cancel is tolerated (test paths) — the modal still renders, but
  confirmed quit exits without subprocess teardown.

- **`internal/framework.Update(ctx, opts)` semantics changed.** It is
  now refresh-only and refuses if `_apex/framework.yaml` is absent.
  Use `framework.Setup(ctx, opts)` for first-time installs. Both
  share an internal `installCore` helper.

### Features ✨

- **`ape framework setup`** — one-time install. Skills + pipelines +
  bootstrap `_apex/config.yaml` via the existing Bubble Tea TUI (or
  `--project-name` + `--extensions` flags, or `--no-bootstrap`).
  Refuses to re-run unless `--force` is passed (which resets the
  bootstrap values).

- **Live pipeline TUI streaming.** `OnStepLine` plumbs newline-delimited
  events from the spawned `claude` subprocess into the TUI as they
  arrive. `internal/tui/event_renderer.go` parses each
  `claude --output-format stream-json` event and renders a one-line
  human summary (`🔧 Read foo.md`, `✎ Drafting ADR table`,
  `↳ ⚠ validation failed`, `✓ skill complete`). No more frozen output
  between stages.

- **Three-panel pipeline TUI.** Top-left ~70% width streams events for
  the cursor's stage; top-right ~30% lists all stages with status
  glyph + duration + cursor; bottom status row summarizes the cursor
  stage. Modes:
  - `Live` (default) — cursor auto-follows the running stage,
    auto-scrolls.
  - `Pinned` — `Enter` pins to the cursor's stage; `PgUp`/`PgDn`
    scroll. `L` or `Esc` returns to Live mode.

  See [docs/reference/tui-keybindings.md](docs/reference/tui-keybindings.md)
  for the full key map.

- **`--no-tui` mode streams the same rendered events.** Timestamped,
  prefixed with stage + skill. Log captures and CI runs get the same
  human-readable feed as the interactive TUI.

- **Quit-confirmation modal.** `q` or `Ctrl+C` mid-run opens a
  `Stop pipeline?` overlay. `y` confirms (cancel + SIGKILL the in-flight
  `claude` subprocess); `n` / `Esc` dismisses. Two Ctrl+C within 1s
  force-quit. Closes the v0.0.6 "TUI quits but subprocess keeps
  running" gap.

### Fixes 🐛

- **`ape framework status` not-installed error.** Previously leaked Go's
  `open …: no such file or directory` trailer underneath the otherwise
  actionable message. Now: single-line, trailer-free. Typed
  `*framework.NotInstalledError` for programmatic callers
  (`errors.As`); still satisfies `errors.Is(err, fs.ErrNotExist)`.

### Internal

- New exit codes: `exitCodeAlreadyInstalled` (6), `exitCodeNotInstalled` (7).
- `internal/framework/install.go` shared `installCore(ctx, opts, doBootstrap)`.
- `internal/pipeline/runner.go` `runClaude` uses `StdoutPipe`/`StderrPipe`
  + `bufio.Scanner` goroutines instead of a captured-string `cmd.Run`.
  Scanner buffer ceiling raised to 1 MiB to accommodate long
  `tool_result` bodies.
- `.goreleaser.yaml` migrated to v2 schema (`version: 2`; `formats:`
  in `format_overrides`) — silences deprecation warnings from
  goreleaser v2 and avoids hard failures in v3.

### Tests

- `internal/framework/install_test.go` — full TestSetup\_\* / TestUpdate\_\*
  rewrite plus refusal-case tests.
- `internal/pipeline/runner_test.go` — TestRunClaude_StreamsLineByLine,
  InterleavesStderr, PropagatesNonZeroExit.
- `internal/tui/pipeline_test.go` — quit-modal state machine + nav
  (cursor moves, Pin freezes, L returns to Live).
- `internal/tui/event_renderer_test.go` — 25+ cases covering every
  tool, every result shape, schema-drift fallback, host extraction,
  truncate helper.

## v0.0.6 (2026-05-10) ⚠️ BREAKING

This release moves pipeline specs out of the binary and adds a first-class
install path for the framework's skills + pipelines + project bootstrap.

### Breaking changes 💥

- **Pipeline specs are no longer embedded into the ape binary.** They now live
  at `<project>/_apex/pipelines/*.yaml` and must be installed before
  `ape pipeline <name>` will work. Existing v0.0.5 installs that ran
  `ape pipeline design` against bare projects will break with:
      pipeline "design" not found at <project>/_apex/pipelines/design.yaml — run
      "ape framework update" to install pipelines from the framework repo

  Migration is one command:
      export APEX_FRAMEWORK_REPO=/path/to/apex_process_framework
      ape framework update

  See [docs/how-to/framework-update.md](docs/how-to/framework-update.md).

- **`LoadSpec(name string)` → `LoadSpec(name, projectRoot string)`.** Internal
  API change in `internal/pipeline`; only relevant if you've imported the
  package directly. Callers that pass an empty `projectRoot` get an explicit
  error with the resolved path.

- **`ape pipeline list` (introduced earlier on this branch) is now `ape pipeline`
  with no positional arg.** `--output-format human|json|yaml` works in list mode
  (no positional). With a name, `ape pipeline <name>` runs the pipeline as
  before. Tab completion still surfaces installed pipelines.

### Features ✨

- **`ape framework update`.** Installs/refreshes the framework's `apex-*` skills
  into `<project>/.claude/skills/` and the canonical pipelines into
  `<project>/_apex/pipelines/`. On first run, opens an interactive Bubble Tea
  prompt to seed `_apex/config.yaml` (project_name + extensions). Headless
  contexts use `--project-name`, `--extensions`, or `--no-bootstrap`.
  Refuses to clobber tracked-but-modified `apex-*` skills without `--force`;
  untracked apex-\* leftovers are safe to overwrite.

- **`ape framework status`.** Reads `<project>/_apex/framework.yaml` and prints
  the installed framework version. With `--repo` or `$APEX_FRAMEWORK_REPO` set,
  fetches the framework HEAD and emits a drift report (hash + tag).

- **`<project>/_apex/framework.yaml`.** New metadata file generated on every
  `framework update` run. Records framework SHA + tag, the ape version that
  performed the install, and the list of installed assets. Should be committed
  alongside the project. Schema:
  [docs/reference/framework-yaml.md](docs/reference/framework-yaml.md).

- **`ape pipeline` (no args).** Lists pipelines installed at
  `<project>/_apex/pipelines/`, with `--output-format human|json|yaml`.

### Internals ⚙️

- New `internal/framework` package implementing the install/status flow:
  copy primitives, git CLI wrappers, metadata schema, two-phase Bubble Tea
  bootstrap TUI, full `Update(ctx, *UpdateOptions)` orchestration. Test
  coverage via `testify/require`: copy primitives, git wrappers against
  ephemeral repos, metadata roundtrip, full Update flow happy path,
  idempotent re-run, stale-skill removal, dirty-framework refusal,
  modified-skill refusal, untracked-skill safe-clobber, missing-subtree
  error, drift detection.

- `internal/pipeline/spec/` (the embedded yaml directory) is gone. The
  three canonical pipelines now live in `apex_process_framework` at
  `framework/_apex/pipelines/` (introduced in framework v0.0.71).

### Documentation 📚

- New [how-to/framework-update.md](docs/how-to/framework-update.md).
- New [reference/pipeline-spec.md](docs/reference/pipeline-spec.md) — formalizes
  the on-disk pipeline YAML schema.
- New [reference/framework-yaml.md](docs/reference/framework-yaml.md).
- New [explanation/why-project-local-pipelines.md](docs/explanation/why-project-local-pipelines.md).
- [how-to/install.md](docs/how-to/install.md) updated with a "next step"
  pointer to `framework update`.

### Compatibility envelope

ape v0.0.6 requires a framework with `framework/_apex/pipelines/` populated
(framework v0.0.71 or later).
