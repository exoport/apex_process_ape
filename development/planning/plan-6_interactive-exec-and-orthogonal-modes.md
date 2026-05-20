---
plan_id: PLAN-6
created_at: 2026-05-19
approved_at: 2026-05-19
status: approved
tags:
  - cli-surface
  - interactive-exec
  - breaking-default
  - pipeline-yaml-schema
  - commit-policy
  - hooks-verification
  - drop-ape-chat
  - diataxis-docs
summary: Introduce a true interactive pipeline execution mode (persistent `claude` process per stage, modeled on `orchestrator.Session`) and make it orthogonal to UI choice. Today every mode (`--web`, `--tui`, `--print`) spawns `claude -p` per step (programmatic); `--web` is "programmatic with bridge/hooks layered on", not interactive. PLAN-6 separates the two axes — **UI** (`none` / `tui` / `web`) and **Exec** (`programmatic` / `interactive`) — and makes interactive the default for `ape pipeline`. The Bubble Tea TUI gains hooks observability, `await_message`/`reply`, run-dir artefacts, and stop control by sharing a new `BridgeRuntime` factored out of `orchestrator.Hub` (broker stays web-only). Pipeline YAML grows pipeline-level and stage-level defaults for `commit`, `model`, `agent` with precedence `step > stage > pipeline > default(skip)`; default commit unit is the stage boundary. Hooks-bridge becomes the verification surface for a per-step "step contract": `/clear` is required before every step's skill prompt **unless** the step sets `no-clear: true`; `/model <X>` is required at step boundaries when the model changes; the agent prefix `/{agent} --autonomous -- {skill}` (or plain `/{skill}`) is required at every step's first prompt. All three are hard-fail. `ape chat` is removed; the useful parts of `orchestrator.Session` are absorbed into the per-stage interactive runtime. `--print` byte-equivalence with today's output stays **LOCKED** for the eval consumer. Documentation is reorganized under Diataxis (tutorial / how-to / reference / explanation) to cover the invocation matrix, pipeline YAML schema, interactive exec model, and step contract.
origin:
  - PLAN-5 shipped the bridged web UI as the default for `ape pipeline <name>` and added every primitive PLAN-6 needs: hooks block in inline `--settings` (`internal/bridge/config/settings.go`), bridge MCP subprocess + IPC + run-dir writers (`internal/bridge/orchestrator/hub.go`, `internal/runlog/`), `await_message`/`reply`, stop control via `runCtx`. The bridge primitives are mode-agnostic by construction; only the Hub's HTTP/SSE broker is web-specific.
  - The PLAN-6 kickoff doc at `development/research/resume-plan-6-kickoff.md` framed the work narrowly as "bring `--web` parity to `--tui`". That framing was reopened on 2026-05-19 after surveying `runner.go:344-380`: **every** mode today spawns `claude -p` per step, including `--web`. There is no truly-interactive pipeline surface in the repo — only `ape chat` is interactive, via `orchestrator.Session` with `cmd *exec.Cmd` + `io.Pipe` stdin. PLAN-6 therefore needs to *introduce* interactive pipeline execution, not just port a bridge between UIs. The reframed scope adds two orthogonal axes (UI × Exec), a pipeline-YAML schema revision, a hooks-bridge step contract, and the removal of `ape chat`.
  - The eval harness at `/home/diegos/_dev/exoar/apex_process_framework_eval` reads `--print` output verbatim (PLAN-5 invariant #6). PLAN-6 preserves `--print` byte-equivalence as a hard invariant. The `--no-tui` alias-to-`--print` semantic that PLAN-5 deprecated is fully removed here: `--no-tui` becomes a real UI selector meaning "no UI, but still interactive exec".
  - PLAN-4 boundary commits remain the per-step record-keeping mechanism. PLAN-6 changes *which* boundary the commit fires on (stage by default, not step), but the underlying boundary-commit primitive is unchanged.
  - The Diataxis docs reorganization is debt that PLAN-5 left explicit (`docs/explanation/` and `docs/reference/` exist; `docs/tutorial/` and `docs/how-to/` are sparse). PLAN-6 pays it down where the new surface area lands: invocation matrix, YAML schema, interactive exec model, step contract.
---

# PLAN-6: Interactive pipeline exec + orthogonal UI/exec modes

## Goal

Make `ape pipeline <name>` interactive-by-default in a Bubble Tea TUI, where "interactive" means a single persistent `claude` process per stage (not per step), bridged via MCP so the user sees hooks live, can reply to `await_message`, and can stop the run. Make the UI choice (`none` / `tui` / `web`) orthogonal to the exec choice (`programmatic` / `interactive`). Make pipeline YAML express where commits fire, which model each step uses, and which agent each step runs under, all with pipeline-level and stage-level defaults so most pipelines stop repeating themselves. Make the hooks bridge enforce a per-step contract — `/clear` (unless opted out), `/model` if changing, agent prefix — so a misbehaving skill cannot silently leak context, model state, or agent identity across step boundaries. Remove `ape chat`. Reorganize the docs that cover all of this under Diataxis.

End state: a first-time user runs `ape pipeline design` and a TUI opens showing live stage cards filling in, hooks ticking under each step, `await_message` surfacing as an inline reply prompt, and a stop key killing the in-flight `claude` cleanly. The same run with `--web` opens a browser instead of a TUI; with `--no-tui` produces plain stdout; with `-P` (or `--programmatic`) spawns `claude -p` per step instead of one process per stage; with `--print` produces today's byte-identical stdout for the eval harness. Pipelines declare `commit: true` at top level and get one stage-boundary commit per stage; individual steps override with `commit: "feat: PRD"` or `commit: false`. The bridge hard-fails the run if a step's prompt arrives without the expected `/clear`, `/model`, or agent prefix. `ape chat` no longer exists.

## Why now

1. **The reframe is paid for by PLAN-5's primitives.** Hooks injection, bridge IPC, run-dir writers, stop control, and `await_message`/`reply` already exist. PLAN-6 is wiring + schema + a process-lifetime change, not new infrastructure. The bridge being "mode-agnostic except for the HTTP/SSE broker" is exactly the shape that lets one factoring (the `BridgeRuntime`) unblock TUI + interactive at once.
2. **Programmatic-only is leaving real cost on the floor.** Every step today spawns `claude` from scratch — full process bootstrap, full prompt-cache cold start, full MCP handshake. Per-stage interactive exec moves that cost from per-step to per-stage. On `design` (4 steps × N stages) the saving is material; on multi-step chains like `apex-create-prd`'s elicit/respond loop, sharing context within a stage stops fighting the cache TTL.
3. **The kickoff framing would have locked in the wrong shape.** If PLAN-6 only ported the bridge to TUI without rethinking exec, "interactive" would have stayed a misleading label for "programmatic + bridge". Future plans would inherit that confusion. Cleaner to fix the axes now, before the bridge has a second consumer.
4. **The YAML schema gap is hurting authoring.** Every step in `design.yaml` repeats `model:` and `agent:`. Stage-level commit messages are impossible (commits fire per step). Pipeline authors keep asking "where do I set the model for the whole pipeline" — there's no good answer today.
5. **`ape chat` was a stepping-stone command.** It validated `orchestrator.Session` as a shape; that shape is what PLAN-6 generalizes. With per-stage interactive exec landed, `ape chat` becomes a worse version of running `claude` directly. Removing it shrinks the surface area we have to keep verifying.
6. **Doc debt compounds.** PLAN-5 left `docs/explanation/` and `docs/reference/` filled out but no tutorials and few how-tos. PLAN-6 lands enough new user-facing concepts (invocation matrix, exec modes, step contract, schema) that adding them to a haphazard docs layout would make finding anything harder. Diataxis pays itself off here.

## Scope — IN

### C1: Two orthogonal axes — UI × Exec

The user-visible model becomes:

| Axis     | Values                                                                                        |
| -------- | --------------------------------------------------------------------------------------------- |
| **UI**   | `none` (plain stdout) · `tui` (Bubble Tea) · `web` (HTTP/SSE)                                 |
| **Exec** | `programmatic` (`claude -p` per step, fresh process) · `interactive` (one `claude` per stage) |

Invocation matrix (after the flip):

| Invocation              | UI           | Exec           | Status                                                                                |
| ----------------------- | ------------ | -------------- | ------------------------------------------------------------------------------------- |
| `ape pipeline <name>`   | `tui`        | `interactive`  | **NEW default**                                                                       |
| `--web`                 | `web`        | `interactive`  | NEW (today's `--web` becomes `--web -P`)                                              |
| `--no-tui`              | `none`       | `interactive`  | NEW (today's `--no-tui` aliases `--print`; that alias goes away)                      |
| `-P` / `--programmatic` | `tui`        | `programmatic` | NEW (TUI shell over today's per-step `claude -p`)                                     |
| `--no-tui -P`           | `none`       | `programmatic` | NEW                                                                                   |
| `--web -P`              | `web`        | `programmatic` | What today's `--web` is                                                               |
| `--print`               | `none`       | `programmatic` | **LOCKED** — byte-equivalent with today (PLAN-5 invariant #6, eval consumer contract) |
| `--tui`                 | `tui`        | `interactive`  | Explicit form of the default; accepted for symmetry                                   |
| `--interactive` / `-I`  | (current UI) | `interactive`  | Explicit interactive opt-in; useful with `--no-tui` or `--web`                        |

**Mutual-exclusion rules.**

- Multiple UI flags is an error: `ape pipeline foo --tui --web` → exit 2 with a clear message.
- `--print` and any other UI/exec flag is an error: `--print --tui`, `--print --web`, `--print -I` all exit 2. `--print` is byte-equivalent-locked and admits no modifiers.
- `--no-tui` and `--print` are not the same any more. The PLAN-5 deprecation alias is removed in PLAN-6; emit a one-line stderr hint if someone passes `--no-tui` and gets surprised by interactive behaviour: "`--no-tui` no longer implies `--print`; pass `--print` for plain-stdout programmatic mode."

**Breaking changes to call out in CHANGELOG.**

- Default for `ape pipeline` flips from `web programmatic` (PLAN-5) to `tui interactive` (PLAN-6).
- `--web` now means `web interactive`. Pre-PLAN-6 `--web` behaviour requires `--web -P`.
- `--no-tui` stops aliasing `--print`.
- `ape chat` is removed (see C6).

### C2: Pipeline YAML schema — pipeline + stage-level defaults for `commit`, `model`, `agent`

Today every step repeats `model:` and `agent:`, and `commit:` is step-only. PLAN-6 adds pipeline-level and stage-level defaults with a single precedence chain.

**Schema** (additive; today's YAML still parses):

```yaml
name: design
model: "opus[1m]"            # NEW: pipeline-level default for `model`
agent: apex-agent-pm         # NEW: pipeline-level default for `agent` (optional)
commit: true                 # NEW: pipeline-level commit policy (bool or template string)

stages:
  create-prd:
    model: "opus[1m]"        # NEW: stage-level override (uses pipeline if absent)
    agent: apex-agent-pm     # NEW: stage-level override
    commit: "feat: PRD"      # NEW: stage-level commit message override
    chain:
      - skill: apex-create-prd
        # model, agent, commit inherited from stage → pipeline
      - skill: apex-create-prd-respond
        no-clear: true       # NEW: opt out of /clear before this step (see C4)
```

**Precedence** (highest wins): `step > stage > pipeline > default`. Default for `commit` is `skip` (no commit fires unless something opts in); default for `model` and `agent` has no value — at least one of the three levels must specify if a step needs a non-default model/agent.

**`commit:` semantics.**

- `commit: true` at pipeline level → one commit per **stage boundary**, message defaulting to the stage name. Stage-level `commit:` overrides the message or disables (`commit: false`). Step-level `commit:` is the escape hatch for mid-chain commits and overrides everything else.
- `commit: "<template>"` at pipeline or stage level → template string used as commit message (with stage name interpolation if `{stage}` appears). Step-level template overrides per-step.
- `commit: false` at any level disables commits at that scope.
- `commit:` absent at all levels → today's behaviour (no commit). Existing pipelines without `commit:` keep behaving the same.

**Default commit unit = stage boundary** (one commit per stage capturing the chain's accumulated diff), not per step. This is the headline behaviour change for pipelines that opt into commits. Migration note for the CHANGELOG: pipelines that used PLAN-4-era per-step `commit: true` get the same behaviour at the **stage** level if they move to `commit: true` at pipeline level; per-step is still available with explicit step-level `commit:`.

**Parser changes:** `internal/pipeline/yaml.go` (or wherever the schema lives), `internal/pipeline/commit.go`. New struct fields, precedence resolver, validation that catches "`commit: true` at pipeline level but no `stages` set commit-capturing semantics" (warning, not error).

### C3: Interactive exec runtime — per-stage `claude` session

**Lifetime model.**

- **Process boundary = stage.** One `claude` process per stage, spawned at stage start, terminated at stage end. Crash blast radius = one stage. Stage transitions guarantee OS-level clean context.
- **Session shape blueprint:** `internal/bridge/orchestrator/session.go`. `cmd *exec.Cmd` + `io.Pipe` stdin, same as `ape chat` today. PLAN-6 generalizes this into a per-stage runtime; `ape chat`'s specific `Session` type is retired in C6 once its useful methods migrate.
- **Steps within a stage share the session.** That's the point of interactive mode. Multi-step chains (elicit/respond, e.g. `apex-create-prd`) get a real continuation; the responding step sets `no-clear: true` (see C4).
- **MCP bridge: one per stage.** Each stage spawn = fresh `ape mcp-bridge` subprocess connected to the long-lived orchestrator's TCP IPC port (PLAN-5 `~/.ape/registry.json` model). The orchestrator outlives any single stage; the per-stage bridge is a thin connector.

**Spawn sequence per stage.**

1. Resolve stage's first step's `model` (per C2 precedence).
2. Spawn `claude --model <model> --strict-mcp-config --mcp-config <inline> --settings <inline> --system-prompt "<bootstrap>"`. Bootstrap content per PLAN-5 C3.
3. Stage-level bridge MCP subprocess starts and connects to orchestrator IPC.
4. Write each step's prompt to stdin in sequence, each preceded by `/clear` (unless the step has `no-clear: true`) and `/model <X>` (if the step's model differs from the current session model) and the agent prefix (per C4).
5. Step boundaries within the stage are bridge-observed (UserPromptSubmit, PostToolUse, etc.) — no process spawn.
6. Stage end: send EOF on stdin, wait for clean exit, terminate bridge MCP subprocess, fire stage-boundary commit if applicable.

**Shared runtime factoring.** Per kickoff §First tasks #2, pick option **(a)**: factor a non-broker `BridgeRuntime` out of `orchestrator.Hub` (IPC accept + stop + event channel). Web mode constructs a `Hub` = `BridgeRuntime + broker + page`. TUI mode constructs `BridgeRuntime` directly and subscribes a Bubble Tea observer to its event channel. `none` UI mode (`--no-tui`) constructs `BridgeRuntime` with a stdout writer subscriber. Rationale: makes the runtime the unit of testing, keeps HTTP/SSE strictly inside web mode (PLAN-5 invariant #4), and avoids the "Hub with NoBroker bool" branching alternative.

**Programmatic mode unchanged.** `-P` / `--programmatic` keeps today's `claude -p` per step. The interactive runtime is not constructed; the existing per-step spawn path in `internal/pipeline/runner.go:344-380` is the contract for programmatic exec.

### C4: Hooks bridge step contract — per-step verification

The bridge already observes every `UserPromptSubmit`. PLAN-6 makes it enforce a contract for what each step's first prompt-stream must look like in interactive mode. Programmatic mode keeps today's per-step process-spawn semantics — no in-session verification needed because the process is fresh.

**Step contract (interactive mode only).**

For every step within a stage, the bridge expects, in order:

1. **`/clear` UserPromptSubmit** before the skill prompt — **default-on for every step**, including the first step of a stage. The bridge expects to see `/clear` on the bus before every skill prompt **unless** the step sets `no-clear: true` in YAML. The first step of a stage _also_ benefits from `/clear` even though the process is fresh, because it standardizes the contract (verification has one rule, not two) and costs effectively nothing on a clean session.
2. **`/model <X>` UserPromptSubmit** if the step's resolved model differs from the session's current model. No-op when adjacent steps share a model. Bridge tracks the current model in session state.
3. **Agent-prefixed skill prompt** matching `/<step.agent> --autonomous -- <step.skill>` if `agent:` is set, or `/<step.skill>` otherwise. (Runner.go:352-355 today already generates this prefix; the bridge verifies it.)

**`no-clear: true` opt-out.**

- Step-level only (not stage- or pipeline-level — opting out wholesale would defeat the contract).
- Used by multi-step chains where the step depends on the previous step's context (elicit/respond loops are the canonical case).
- When set, the bridge expects the step's prompt **without** a preceding `/clear`. If it sees one anyway, it's a soft warning (not hard-fail) — `/clear` is always safe to send; the contract is "the bridge knows what the runner intended".

**Failure mode = hard-fail.** Any violation aborts the run, marks the manifest `status: failed`, writes a `step-contract-violation` event to `hook-events.jsonl`, and the orchestrator returns a non-zero exit. No soft-warn for clear/model/agent violations — the whole point of the contract is to make context, model, and agent identity non-negotiable at step boundaries.

**Bridge changes.** New `StepContract` struct passed to the bridge at step start (model expected, agent expected, skill expected, no-clear flag). Bridge's UserPromptSubmit handler walks the contract sequence and aborts on mismatch. Tests in `internal/bridge/orchestrator/contract_test.go` (new).

### C5: TUI parity with web

This is the original PLAN-6 kickoff scope, now landing on top of `BridgeRuntime` from C3. The shopping list is unchanged from `resume-plan-6-kickoff.md`:

- Hooks block injected into the spawned claude's inline `--settings` for `ModeTUI` (extend `internal/bridge/config/settings.go:BuildSettings` to handle TUI like web).
- Bubble Tea views for hooks activity, await prompt, stop confirmation. New views in `internal/tui/`.
- IPC subscriber adapter from `BridgeRuntime` event channel to `tea.Msg`. Throttle/batch layer for hook bursts (PreToolUse/PostToolUse pairs can hit dozens per second; today's TUI re-renders on every msg). Coalesce to ~30fps tick.
- Run-dir writers (`hook-events.jsonl` / `bridge-calls.jsonl` / `checkpoints.jsonl` / `transcripts/`) wired the same way web wires them — `runlog.Writer` lazy-bound on `OnRunDir`, subscribed to the same event stream.
- Stop control: reuse the existing double-Ctrl+C / quit-modal in `tui.PipelineModel`. Same `runCtx` cancellation path as web.
- Async reply input when a skill calls `await_message`. Modal overlay; submit publishes a `reply` event back through `BridgeRuntime`.

**TUI does not start an HTTP listener.** PLAN-5 invariant #4 stays; the broker (HTTP/SSE) is web-only and is not constructed for TUI mode.

### C6: Drop `ape chat`

- Remove `cmd/ape/chat.go` and its registration.
- Remove `internal/bridge/orchestrator/session.go` (`ape chat`-specific `Session` type). Migrate any methods still useful to the per-stage runtime (C3) before deletion.
- Remove `<project>/_output/ape/chats/<id>/` writer and the chat-artefact layout convention.
- Update `~/.ape/registry.json` schema only if it currently records chat sessions — pipeline sessions stay.
- Remove `docs/explanation/` and `docs/reference/` mentions of `ape chat`; redirect any cross-references to the per-stage interactive exec docs (C7).
- Migration note in CHANGELOG: "`ape chat` is removed. For an interactive single-claude session, run `claude` directly; for a bridged interactive pipeline, use `ape pipeline <name>` (default) or `ape pipeline <name> --web`."

### C7: Diataxis docs reorganization

Reorganize `docs/` under the four Diataxis categories. Existing files keep their content; new content lands in the right bucket from the start.

**Target layout:**

```
docs/
├── tutorial/
│   └── first-pipeline.md           NEW — walk-through with `ape pipeline design`
├── how-to/
│   ├── authoring-pipelines.md      NEW — YAML schema cookbook (commit/model/agent precedence)
│   ├── run-artefacts.md            (exists; move from current location if needed)
│   ├── interactive-vs-programmatic.md  NEW — when to use which
│   └── stop-and-recover.md         NEW — stop, replay (link to PLAN-5 future), debug failures
├── reference/
│   ├── invocation-matrix.md        NEW — the UI × Exec table from C1, with mutual-exclusion rules
│   ├── pipeline-yaml-schema.md     NEW — full schema reference incl. C2 additions
│   ├── step-contract.md            NEW — /clear, /model, agent prefix; no-clear opt-out
│   ├── bridge-ipc.md               (exists)
│   ├── bridge-security.md          (exists)
│   └── cli.md                      NEW or updated — every flag and command
└── explanation/
    ├── bridge-architecture.md      (exists)
    ├── exec-modes.md               NEW — why interactive vs programmatic; per-stage process model
    └── design-decisions.md         (exists or new; absorb relevant prose from research/)
```

**Out of scope:** rewriting `docs/explanation/bridge-architecture.md` or `docs/reference/bridge-ipc.md`. They're current and good; PLAN-6 only adds new files and updates cross-references.

**Format.** Every markdown file formatted with `npx prettier --write "<file>" --log-level silent` after edit (project convention).

### C8: Test strategy

Per-component coverage; integration coverage at the `BridgeRuntime` boundary.

- **C2 (YAML schema):** unit tests for precedence resolver (`step > stage > pipeline > default`) covering every combination of present/absent at each level for `commit`, `model`, `agent`. Snapshot test of parsed `design.yaml` and a synthetic multi-stage pipeline.
- **C3 (interactive runtime):** integration tests with a stub `claude` binary that echoes stdin + emits canned hooks. Verify per-stage spawn/teardown, session reuse across steps, EOF on stdin at stage end, MCP bridge subprocess lifecycle.
- **C4 (step contract):** unit tests for the `StepContract` verifier — every combination of (model change y/n, agent set y/n, no-clear y/n). Hard-fail path verified via abort + manifest record + non-zero exit.
- **C5 (TUI parity):** golden-frame tests with `teatest` for the new hooks/await/stop views. Throttle layer verified by feeding a 200-event burst and asserting at most ~30fps emissions.
- **C6 (drop ape chat):** the test suite shrinks (deleted `ape chat` tests). Add a CLI smoke test that `ape chat` exits non-zero with "command removed; see CHANGELOG" message.
- **Invariant guard:** byte-equivalence test for `--print` output against a captured baseline from current main. Run on every PR; any divergence is a hard-fail.

## Scope — OUT

- **Cost panel in TUI.** PLAN-5 deferred this for web; PLAN-6 inherits the deferral. Per-step cost still lands in the manifest post-step.
- **Replay-from-disk for closed UI sessions.** Out of scope (PLAN-5 future).
- **Multi-pipeline composition** (one pipeline depending on another's output). Future plan.
- **Per-stage sandboxing of the orchestrator** (one orchestrator process serves all stages, as today). Stage isolation comes from the `claude` process spawn, not the orchestrator.
- **MCP server additions beyond what PLAN-5 ships.** Only `await_message` / `reply` and the hook tools today; no new MCP tools in PLAN-6.
- **Migrating existing pipeline YAML to use the new defaults.** PLAN-6 keeps today's YAML valid (additive schema). Migration is a per-pipeline editorial choice, not a plan deliverable.
- **Removing the PLAN-5 `--no-tui` deprecation hint** (it stops aliasing `--print`; the stderr hint stays at least one minor version).
- **GUI-based pipeline authoring** (way out of scope).

## Invariants — violating any of these regresses PLAN-6

1. **`--print` byte-equivalence with today's output is LOCKED.** Eval consumer at `/home/diegos/_dev/exoar/apex_process_framework_eval` reads it verbatim (PLAN-5 invariant #6). `--print` must not construct `BridgeRuntime`, must not inject hooks block, must not change one byte of what the runner writes to stdout. Snapshot test enforces.
2. **Broker (HTTP/SSE) is web-only.** TUI and `--no-tui` must not start an HTTP listener. `BridgeRuntime` does not own the broker; the broker is composed onto it only in web mode (PLAN-5 invariant #4).
3. **Stage process spawn = clean OS-level context.** Per-step `/clear` is in-session and bridge-verified; stage-boundary `/clear` is _redundant_ with the spawn but still part of the contract for verification uniformity. Never substitute one for the other.
4. **Run-dir artefact path is unchanged**: `<project>/_output/pipelines/<name>/<run_id>/` (PLAN-3 contract). Per-stage interactive exec writes to the same path with the same JSONL writers.
5. **Step contract is hard-fail.** No soft-warn for `/clear` (when default-on), `/model`, or agent-prefix violations. The manifest records `status: failed` and the process exits non-zero. Only the _redundant_ `/clear` on a `no-clear: true` step is soft-warn.
6. **No `Co-Authored-By: Claude` trailer** on any commit (project memory `feedback_no_claude_attribution.md`).
7. **Markdown formatted with prettier** after every edit: `npx prettier --write "<file>" --log-level silent`.
8. **Today's YAML keeps parsing.** Schema changes are additive. Pipelines without `commit:`/`model:`/`agent:` at pipeline or stage level keep behaving the same (no implicit commits, model/agent stay step-level).
9. **One orchestrator per pipeline run.** Per-stage interactive exec means N `claude` processes and N bridge MCP subprocesses per run, but always exactly one orchestrator (long-lived for the whole pipeline).

## Implementation phasing

Each phase is a complete unit of work; downstream phases can land independently _after_ the upstream phase ships, but not before.

**Phase A — Pipeline YAML schema (C2).**
Schema additions, precedence resolver, parser tests, snapshot of `design.yaml`. No runtime behaviour change yet (commits stay per-step where YAML opts in step-level; the stage-boundary default lights up in Phase D).

**Phase B — `BridgeRuntime` factoring (C3 prep).**
Extract IPC accept + stop + event channel out of `orchestrator.Hub` into a non-broker `BridgeRuntime`. Web mode rewires to `BridgeRuntime + broker + page`. No new modes yet; this is a no-functional-change refactor. Verify with the existing web-mode test suite + a byte-equivalence run of `--print`.

**Phase C — Interactive exec runtime (C3).**
Per-stage `claude` spawn behind a hidden `--interactive` flag. `BridgeRuntime` per stage. MCP subprocess per stage. No UI parity yet (TUI still shows today's two-panel display; web is unchanged). Test with `--interactive --print`-like output (logs to stdout). Sandbox smoke: `ape pipeline design --interactive` in `/home/diegos/_dev/ape-web-sandbox/greeter/` produces a working run with hooks visible in `hook-events.jsonl`.

**Phase D — Step contract verification (C4).**
`StepContract` struct + bridge verifier. Hard-fail paths. Tests for every contract dimension. Stage-boundary commit policy from C2 lights up here (it depends on knowing where stage boundaries are, which the bridge already tracks). After Phase D, `--interactive` is feature-complete from a correctness standpoint.

**Phase E — TUI parity (C5).**
Bubble Tea hooks/await/stop views. `BridgeRuntime`-event-channel → `tea.Msg` adapter. Throttle layer. Run-dir writers wired to TUI mode. Sandbox smoke: `ape pipeline design --interactive --tui` (or just `--tui` after Phase F) shows the same hooks activity as `--web`.

**Phase F — Invocation matrix flip (C1).**
Default becomes `tui interactive`. `--web` becomes `web interactive`. `--no-tui` stops aliasing `--print`. `--interactive` graduates from hidden to documented. `-P`/`--programmatic` lands as the explicit programmatic opt-in. Mutual-exclusion validation. CHANGELOG entries. After Phase F, the surface from C1 is shipping.

**Phase G — Drop `ape chat` (C6).**
Remove `cmd/ape/chat.go`, `internal/bridge/orchestrator/session.go` (after absorbing useful methods into Phase C's runtime), chat-artefact writer, chat-specific tests. Registry cleanup. CHANGELOG migration note.

**Phase H — Diataxis docs (C7).**
File moves + new files (tutorial, how-tos, reference for invocation matrix / schema / step contract, explanation for exec modes). Cross-reference cleanup. Prettier pass.

**Ordering constraint.** Phases A and B are independent of each other but both block C. C blocks D, E, F. G can land any time after C (its dependency is the per-stage runtime absorbing `Session` methods); pragmatically it lands after F so the CHANGELOG entries cluster. H can land any time after F (the docs need the final invocation matrix to describe).

## Sandbox smoke matrix (acceptance shape)

Test bed: `/home/diegos/_dev/ape-web-sandbox/greeter/` with the `design` pipeline. Clean state: `git reset --hard 3676580`.

| Invocation                                        | Expected                                                                                                        |
| ------------------------------------------------- | --------------------------------------------------------------------------------------------------------------- |
| `ape pipeline design`                             | TUI opens, hooks panel populates, stage cards fill in live, stop key works                                      |
| `ape pipeline design --web`                       | Browser tab opens (same as today's `--web` look-and-feel but with one claude per stage)                         |
| `ape pipeline design --no-tui`                    | Plain stdout, but `_output/.../hook-events.jsonl` populated (proves interactive ran)                            |
| `ape pipeline design -P` (or `--programmatic`)    | TUI, but per-step `claude -p` spawn (no `hook-events.jsonl` content beyond what programmatic captures)          |
| `ape pipeline design --print`                     | **Byte-identical** to today's `--print` output. Snapshot diff = empty.                                          |
| `ape pipeline design --web -P`                    | What today's `--web` is.                                                                                        |
| `ape pipeline design --tui --web` (mutex error)   | Exit 2 with "multiple UI flags".                                                                                |
| `ape pipeline design --print --tui` (mutex error) | Exit 2 with "`--print` admits no modifiers".                                                                    |
| Step contract violation (corrupt prompt)          | Run aborts, manifest records `status: failed`, exit non-zero, `step-contract-violation` in `hook-events.jsonl`. |
| `ape chat` (after Phase G)                        | Exit non-zero, "command removed; see CHANGELOG".                                                                |

## Open questions to resolve during implementation (not pre-decided)

- **Throttle layer for TUI hook bursts** — exact framerate and batching strategy. Spike during Phase E; current best guess is "coalesce to ~30fps tick with `tea.Tick`".
- **Stop UX in TUI** — reuse the double-Ctrl+C / quit-modal or add an explicit `s` binding behind a `?`-confirm. PLAN-5 kickoff suggested reuse; decide during Phase E.
- **Per-stage MCP subprocess teardown timing** — kill aggressively at stage end vs. linger for any tail events. Decide during Phase C; tests will surface the answer.
- **`--no-tui` deprecation hint phrasing and lifetime** — the stderr hint stays "at least one minor version" but exact wording TBD during Phase F.
- **CHANGELOG entry organization** — one PLAN-6 entry vs. per-phase. Default: one consolidated entry when Phase H lands.

## Implementation pivot — tmux send-keys (2026-05-20)

Phases A–H of the plan above all shipped. During sandbox bring-up, the per-stage interactive runtime (Phase C) was found to be **structurally broken**: spawning `claude` under a raw PTY with `--system-prompt` instructing the model to loop on `await_message` / `reply` MCP tools, and delivering each step's prompt as the return value of `await_message`, looked correct in isolation but failed end-to-end. Symptoms:

- First attempt (plain stdin pipe): claude REPL refused to initialize without a TTY; no MCP servers ever started.
- Second attempt (`claude -p` + bridge): rejected by the user — defeats the point of PLAN-6.
- Third attempt (PTY + `--system-prompt` + bootstrap `"begin\r"` + `await_message` delivery): bridge connected, ❯ painted, model parked at `await_message`; first step's `SendMessage` reached the model, then the run aborted (`context canceled` within ~150ms) or hung indefinitely (5+ min "Cogitating…" with no tool calls).

Root cause: **slash commands delivered via an MCP tool-result string are not slash commands.** The PAT-25 skill prompt shape (`/<agent> --autonomous -- <skill> --autonomous`) is a claude-CLI-level construct — the CLI parses the leading `/`, looks up the registered skill in `.claude/skills/`, loads `SKILL.md`, and invokes the agent loop. When that same string arrives to the model as the return value of `await_message`, the model can _read_ it but cannot _invoke_ it; the CLI never sees the slash command, the skill never loads. The model is left holding an opaque instruction with no execution path.

The fix is to deliver prompts as **real REPL keystrokes** so claude's CLI parses them the normal way:

- Spawn `claude` inside a per-stage `tmux new-session` (gives a real TTY natively).
- Wait for the `❯` glyph in `tmux capture-pane -p` (claude REPL is up).
- For each step: type the slash command via `tmux send-keys -t <session> -l "<prompt>"`, settle 300ms, press `C-m`. claude's CLI parses, loads the skill, executes. Between steps, type `/clear` first to reset model context (the runner's responsibility; `no-clear: true` opts out).
- `Stop` hook from the bridge still signals step-done; `UserPromptSubmit` hook still fires with the literal slash command typed, so the `ContractVerifier` checks the agent prefix exactly as designed.
- `--system-prompt` is dropped. `await_message` / `reply` MCP tools are no longer used by the model in pipeline mode (the model just runs its normal REPL).
- The `/model <X>` contract rule is dropped permanently — that's a CLI-level switch the model can't invoke, and a future plan can re-add it as a runner-driven `tmux send-keys "/model <X>"` if the use case appears.

`ape chat` was simultaneously rewritten as a thin tmux spawn-and-attach helper: it starts a claude REPL in a named tmux session with the ape bridge wired (hooks captured to `_output/ape/chats/<id>/`) and `exec tmux attach`'s the user to the session. The Bubble Tea chat TUI from the original Phase G follow-up was deleted (it was driven by the same broken `await_message` shape).

### What changed vs. the plan above

| Plan said                                                            | What shipped                                                                                               |
| -------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------- |
| One persistent `claude` per stage, prompts via stdin                 | One persistent `claude` per stage **inside tmux**, prompts via `tmux send-keys -l` + `C-m`.                |
| Three contract rules: `/clear`, `/model X`, agent prefix             | One enforced rule: agent prefix. `/clear` is runner-driven (no verifier check needed); `/model X` dropped. |
| `--system-prompt` instructs model to loop on `await_message`/`reply` | No `--system-prompt`. Model runs its normal REPL.                                                          |
| `ape chat` removed entirely (C6, Phase G)                            | `ape chat` survives as a thin tmux spawn-and-attach helper (chat TUI removed).                             |
| `tui.ChatModel` kept until Phase G removal                           | `internal/tui/chat.go` deleted.                                                                            |
| `creack/pty` dependency used by chat + interactive runner            | Dependency removed (`go.mod` cleaned).                                                                     |

### Why this is a robust shape

1. The slash-command delivery path now matches the actual claude CLI contract — there's no impedance mismatch.
2. Debugging is dramatically easier: `tmux attach -t ape-<stage>-<pid>` shows the live session at any time during a run.
3. The bridge keeps doing what it was always good at — hook observability and `Stop` signalling — without trying to also be a prompt-delivery channel.
4. `tmux` is a standard system dependency on every machine likely to run ape; it's not vendored and doesn't lock us to a Go library's release cadence.

### New invariants from the pivot

- **`tmux` must be on `PATH`** for interactive mode. ape errors clearly if it isn't.
- **No `--system-prompt`** in any interactive-mode argv. The shape went away and shouldn't return.
- **`/clear` is runner-driven, not verifier-enforced.** The runner sends `/clear` between steps (when `!step.NoClear`); the verifier ignores `/clear` UserPromptSubmit events because they arrive outside any active-step contract window.
- **`InteractiveSystemPrompt` and `chatSystemPrompt` are deleted.** The prompts they contained ("call await_message in a loop…") instructed the model into a broken pattern; they must not return.
- **No `creack/pty` import** anywhere in the tree.

## Context references

| Path                                                       | What                                                                     |
| ---------------------------------------------------------- | ------------------------------------------------------------------------ |
| `development/research/resume-plan-6-kickoff.md`            | Original kickoff (narrower scope; superseded by this plan's reframe).    |
| `development/planning/plan-5_ape-chat-and-pipeline-web.md` | PLAN-5 — the primitives PLAN-6 builds on.                                |
| `development/research/claude-mcp-bridge.md`                | Bridge architecture + contracts.                                         |
| `docs/explanation/bridge-architecture.md`                  | Bridge design narrative.                                                 |
| `docs/reference/bridge-ipc.md`                             | IPC wire schema.                                                         |
| `docs/reference/bridge-security.md`                        | Bind + threat model.                                                     |
| `internal/pipeline/runner.go:344-380`                      | Today's per-step `claude -p` spawn (programmatic mode contract).         |
| `internal/bridge/orchestrator/hub.go`                      | Bridge IPC accept + replay + stop. Factoring source for `BridgeRuntime`. |
| `internal/bridge/orchestrator/session.go`                  | `ape chat`'s single-bridge session. Blueprint for per-stage runtime.     |
| `internal/bridge/config/settings.go`                       | `BuildSettings` — hooks injection rule changes here (PLAN-6 C3).         |
| `internal/apecmd/pipeline.go` / `pipeline_web.go`          | Mode dispatch + web reference impl. TUI side gets parity in Phase E.     |
| `internal/runlog/`                                         | JSONL writers; mode-agnostic.                                            |
| `/home/diegos/_dev/ape-web-sandbox/greeter/`               | Live sandbox (clean state: `git reset --hard 3676580`).                  |
| `/home/diegos/_dev/exoar/apex_process_framework_eval/`     | Eval consumer (PLAN-9). `--print` byte-equivalence locked.               |
| Project memory `feedback_no_claude_attribution.md`         | No `Co-Authored-By: Claude` trailer on commits.                          |
