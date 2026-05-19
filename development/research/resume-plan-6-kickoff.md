# Continuation Prompt — kick off PLAN-6 (TUI parity with web mode)

Use this prompt to pick up after a `/clear`. PLAN-5 is **done**;
PLAN-6 has **not been drafted yet**. Goal: bring the
orchestrator-aware surface that PLAN-5 added under `--web` (hooks
observability, `await_message` / `reply`, run-dir artefacts,
stop, cost tracking) to `--tui` as well. The Bubble Tea surface
stays; the bridge runtime gets shared between the two modes.

This repo is `/home/diegos/_dev/github/diegosz/apex_process_ape`.

---

## State, in one paragraph

PLAN-5 shipped the bridged web UI as the default for `ape pipeline
<name>`. `--tui` exists as an opt-in but runs the pre-PLAN-5
surface: `pipeline.Run` with no `PrependFlags`, a Bubble Tea
two-panel display fed by the existing pipeline `Observer`. No
bridge subprocess, no hooks block in the spawned claude's
inline `--settings`, no `await_message`, no run-dir
`hook-events.jsonl` / `bridge-calls.jsonl` / `checkpoints.jsonl`
/ `transcripts/`. PLAN-6 closes that gap. Main tip is `1444e95`.
All-tests-pass.

---

## Pre-flight

```bash
cd /home/diegos/_dev/github/diegosz/apex_process_ape
git status --short             # clean on main
git log --oneline -3           # tip 1444e95 or later
go build ./...
go test ./... -count=1 -timeout 60s
```

Rebuild for sandbox after any source edit:

```bash
go build -o /home/diegos/_dev/ape-web-sandbox/.bin/ape ./cmd/ape
```

Compare the two surfaces in the sandbox:

```bash
cd /home/diegos/_dev/ape-web-sandbox/greeter
/home/diegos/_dev/ape-web-sandbox/.bin/ape pipeline design --tui    # today's gap surface
/home/diegos/_dev/ape-web-sandbox/.bin/ape pipeline design --open   # parity target
```

Sandbox clean state: `cd <sandbox>/greeter && git reset --hard 3676580`.

---

## PLAN-6 goal

> There is no reason `ape pipeline` can't deliver the same
> bridge-backed orchestrator surface in the Bubble Tea TUI that
> it does in the web UI. PLAN-6 brings that parity.

Concretely, today's `--tui` is missing:

- Hooks block injected into the spawned claude's `--settings`
  (no `PreToolUse` / `PostToolUse` / `UserPromptSubmit` / etc.
  observability).
- An IPC connection to a bridge MCP subprocess (so no
  `await_message`, no `reply`, no per-call frames captured).
- Run-dir `hook-events.jsonl` / `bridge-calls.jsonl` /
  `checkpoints.jsonl` / `transcripts/` writers.
- A user-driven stop control wired through `runCtx`.
- A per-stage state + hook activity panel parallel to what web
  shows.
- An async reply input when a skill calls `await_message`.

PLAN-6 lands all of those in TUI mode, rendered with Bubble Tea
panels instead of HTMX/SSE.

---

## Where the code lives

| Path                                      | What                                                                                                                                                                 |
| ----------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/apecmd/pipeline.go`             | Mode dispatch (`--web` / `--tui` / `--print`). `runWithTUI` lives here, ~line 244 — the integration point.                                                           |
| `internal/apecmd/pipeline_web.go`         | Reference implementation — bridge + hub + broker + page + observer wiring. The blueprint for TUI parity.                                                             |
| `internal/tui/`                           | Bubble Tea models (`PipelineModel`, observer adapter). New views for hooks / await / stop will land here.                                                            |
| `internal/bridge/orchestrator/hub.go`     | Bridge IPC accept loop + replay + stop + per-event publishing through the broker. Web mode constructs a `Hub` with broker + page; TUI wants the same minus HTTP/SSE. |
| `internal/bridge/orchestrator/session.go` | The `ape chat` single-bridge Session. Useful read for what a non-pipeline bridge runtime looks like.                                                                 |
| `internal/bridge/config/mode.go`          | `Mode` enum (`ModePrint` / `ModeTUI` / `ModeWeb`).                                                                                                                   |
| `internal/bridge/config/settings.go`      | `BuildSettings` — currently returns `{}` for `ModeTUI` / `ModePrint`, only injects hooks for `ModeWeb`. PLAN-6 extends to `ModeTUI`.                                 |
| `internal/bridge/config/mcp.go`           | `BuildMCPConfig` — already mode-agnostic; reuse.                                                                                                                     |
| `internal/bridge/ipc/`                    | TCP / NDJSON wire. Mode-agnostic.                                                                                                                                    |
| `internal/runlog/`                        | `hook-events.jsonl` / `bridge-calls.jsonl` / `checkpoints.jsonl` / `transcripts/` writers. Mode-agnostic; just needs a TUI-side caller.                              |
| `internal/cost/`                          | Per-step cost — should work unchanged once the bridge is up and the manifest path is preserved.                                                                      |

---

## Invariants — fail any of these and PLAN-6 regresses

1. **`--print` byte-equivalence with today's output is locked.**
   The eval consumer at
   `/home/diegos/_dev/exoar/apex_process_framework_eval` reads it
   verbatim (PLAN-5 invariant #6). Do **not** inject hooks block
   or bridge into `ModePrint`.
2. **The broker (HTTP/SSE) is web-only.** TUI must not start an
   HTTP listener. If a bridge IPC listener is started in TUI mode,
   it stays bound to `127.0.0.1` (PLAN-5 invariant #4).
3. **Hooks-block injection rule changes** from "`Mode == ModeWeb`"
   to "`Mode == ModeWeb || Mode == ModeTUI`". The doc comments in
   `internal/bridge/config/settings.go` need updating in lockstep.
4. **Run-dir artefact path unchanged**:
   `<project>/_output/pipelines/<name>/<run_id>/`. PLAN-3 contract.
5. **No `Co-Authored-By: Claude` trailer** on any commit (project
   memory `feedback_no_claude_attribution.md`).
6. **Markdown formatter**: `npx prettier --write "<file>" --log-level silent`
   after every edit.

---

## First tasks for the resumed assistant

1. **Survey the seams.** Read `pipeline_web.go` and the
   `runWithTUI` block in `pipeline.go` side by side. Map every
   piece `runWithWeb` does that `runWithTUI` doesn't:
   - `BuildMCPConfig` + `BuildSettings(ModeWeb)` + `PrependFlags`
   - `orchestrator.NewHub` construction + `Listen` + `Serve`
   - Bridge IPC accept loop (in `Hub.acceptLoop`)
   - Per-stage publish (`onStageStart`, `onStageEnd`,
     `publishStageCard`, `rememberStage` — `ad2e508`)
   - `runlog.Writer` lazy bind on `OnRunDir`
   - Stop button → `runCancel`
   - Cost rollup rebuild on exit
     Every `Mode == ModeWeb` gate is a PLAN-6 change point.

2. **Decide the shared-runtime shape.** Two viable options:
   - **(a)** Factor a non-broker `BridgeRuntime` out of
     `orchestrator.Hub` (IPC accept + stop + event channel) and
     let both web + TUI compose it. Broker becomes one consumer
     of the runtime's event stream; Bubble Tea becomes another.
   - **(b)** Keep `Hub` as-is but make the broker construction
     optional (`HubOptions.NoBroker bool`). TUI gets a Hub without
     a broker and subscribes to events via a Go channel exposed
     on the Hub.
     Pick one and note the trade-offs in the plan; do not bake the
     choice into code before the plan is approved.

3. **Draft `development/planning/plan-6_<slug>.md`** following the
   PLAN-5 frontmatter convention (`plan_id`, `created_at`,
   `status: draft`, scope IN / OUT, invariants, phasing). Update
   `development/planning/index.md`. Run the draft past the user
   before any code change.

4. **Sandbox success criterion.** Default test bed is
   `/home/diegos/_dev/ape-web-sandbox/greeter` with the `design`
   pipeline. A reasonable acceptance shape:
   - `ape pipeline design --tui` shows a hook-activity panel that
     populates as `create-prd` runs (Read/Glob/Bash hooks visible).
   - If a skill calls `await_message` (none in `design` today —
     `apex-create-story` is the canonical one), the TUI exposes an
     input box and `reply` round-trips.
   - Stop key (proposal: `s` after a `?`-confirm modal, or reuse
     the double-Ctrl+C confirm already in `tui.PipelineModel`)
     cancels the run, the in-flight `claude` is killed, and the
     manifest records `status: stopped`.
   - `--web` byte-equivalent run-dir artefacts are produced (same
     four JSONLs + transcripts/).

---

## Open design questions to surface (do not pre-decide)

- **Sub-process bridge or in-process?** Web mode spawns the
  bridge MCP subprocess and the spawned claude connects to it via
  inline `--mcp-config`. TUI could do the same. Cleaner; mirrors
  web. The alternative (skip `await_message` / `reply`, inject
  only hooks) is a smaller scope but loses the await UX.
- **Stop UX.** Reuse the existing double-Ctrl+C / quit-modal in
  `tui.PipelineModel` vs. add an explicit stop binding. Keep
  consistent with web's stop button semantics — same `runCtx`
  cancellation path.
- **Hook flood rate.** Web mode tolerates bursts via SSE flush +
  HTML fragment streaming. TUI re-renders on every tea.Msg; a
  PreToolUse / PostToolUse pair per claude tool call can hit
  dozens per second. May need a batching / throttle layer in the
  Bubble Tea observer.
- **Cost panel.** Web has none in-flight (PLAN-5 "sharp edge").
  TUI should not block on that; per-step cost lands in the
  manifest post-step like today.

---

## Context references

| Path                                                       | What                                                          |
| ---------------------------------------------------------- | ------------------------------------------------------------- |
| `development/planning/plan-5_ape-chat-and-pipeline-web.md` | PLAN-5 — the parity target. Read the appendix + sharp edges.  |
| `development/research/resume-plan-5-post-launch.md`        | PLAN-5 resume doc — sandbox layout, smoke matrix, invariants. |
| `development/research/claude-mcp-bridge.md`                | Bridge architecture + every contract.                         |
| `docs/explanation/bridge-architecture.md`                  | Design narrative for the bridge.                              |
| `docs/reference/bridge-ipc.md`                             | IPC wire schema.                                              |
| `docs/reference/bridge-security.md`                        | Bind + threat model.                                          |
| `docs/how-to/run-artefacts.md`                             | `_output/` layout reference.                                  |
| `/home/diegos/_dev/ape-web-sandbox/greeter/`               | Live sandbox (clean state: `git reset --hard 3676580`).       |
| `/home/diegos/_dev/exoar/apex_process_framework_eval/`     | Eval consumer (PLAN-9). Do not break its manifest reader.     |
| Project memory `feedback_no_claude_attribution.md`         | No `Co-Authored-By: Claude` trailer on commits.               |
