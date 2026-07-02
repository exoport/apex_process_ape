---
plan_id: PLAN-11
created_at: 2026-07-02
implemented_at: 2026-07-02
status: done
implementation_notes: Shipped in v0.0.27. Deferred from scope — UI selectors on `ape task` (--tui/--web; headless-first per plan) and the real-claude build-tagged integration test (the eval repo's fixture run is the acceptance gate; run it against the released binary). The eval passes NO commit flags — default behavior is byte-identical to its `_invoke_skill` convention.
tags:
  - new-command
  - single-skill
  - pty
  - pipeline-runner
summary: Two changes, sequenced FIRST in the proposed wave to unblock the eval repo. F0 — trust-dialog dismiss + WaitForReady hardening in `internal/repl` (claude's folder-trust modal false-triggers the bare-`❯` ready check → keystrokes eaten → 1h idle; fix = modal registry + real-REPL ready markers + pane-in-error timeout). Then the new `ape task <skill>` command — run a single skill through the interactive PTY runner with everything a pipeline step gets (framework-agent prefix, model selection, skill args, prompt forwarding, preflight, manifest, bridge hooks, telemetry) plus two-layer commit control (`--no-commit` = the framework skill's own no-commit functionality; `--task-commit ["<msg>"]` = opt-in whole-task boundary commit, default off), all parameters passed as CLI flags instead of a pipeline YAML file, plus a stable `--output-format json` result envelope shaped to replace the eval's stream-json result event. Implemented by exporting a single-step Spec constructor from `internal/pipeline` and routing through the existing interactive runner unchanged; run artifacts land under `_output/tasks/<skill>/<run-id>/` and fold into the cost rollup.
origin:
  - 2026-07-02 user request — "a task command that allows us to run a single skill using PTY terminal, in the same way we run pipelines … with a framework agent with arguments and a particular model, all passed with arguments instead of reading a yaml pipeline file, including optional commit message, or --no-commit flag."
  - 2026-07-02 pipeline-internals audit — `Step{Skill, Agent, Model, Args, PromptFlag, Commit}` maps 1:1 onto CLI flags; the only missing seam is an exported Spec constructor (`spec.go` keeps `stages`/`stageMap` unexported, populated only by `LoadSpec`).
  - 2026-07-02 eval-repo spec (apex_process_framework_eval) — reconciled into this plan. Adds F0 (trust-dialog dismiss + WaitForReady hardening: claude-code 2.1.198's folder-trust modal renders `❯` in its menu item, false-triggering `repl.WaitForReady`'s bare-glyph check → prompt keystrokes eaten → 1h idle; `--dangerously-skip-permissions` does not suppress it interactively), the `--output-format json` result envelope shaped to replace the eval's stream-json result event, `--idle-timeout`, exit code 3 (REPL never ready), and the prompt-line byte-parity lock with `assembleInteractivePromptLine`. Sequencing decision: this plan moves FIRST (before PLAN-9/10, which are not actual prerequisites) to unblock the eval's migration off direct `claude -p`.
---

# PLAN-11: `ape task` — single-skill runs without YAML

## Goal

`ape task apex-create-prd --agent apex-agent-pm --model "opus" --prompt "…"`
behaves exactly like a one-step pipeline run: preflight-validated, executed in
an interactive PTY claude REPL, Stop-hook-terminated, telemetry-scanned,
manifest-recorded, and committed at the boundary — no `_apex/pipelines/*.yaml`
authoring required.

## Why now

- **It unblocks the eval.** apex_process_framework_eval's `_invoke_skill`
  still shells out to raw `claude -p` + stream-json; `ape task
  --output-format json` is its PTY replacement. This plan is sequenced
  first in the proposed wave — PLAN-9 (flag removal) and PLAN-10 (per-model
  telemetry) looked like prerequisites but are not: task never grows a
  programmatic branch regardless, and the envelope's aggregate usage fields
  exist today (`stepTelemetryToResultEvent`); per-model lands additively.
- It fixes a live interactive-exec bug for everything (F0): the folder-trust
  modal false-triggers `WaitForReady`, turning first runs in untrusted dirs
  into 1h idle stalls — pipelines included.
- It is the smallest new command and it forges the seam (exported single-step
  Spec) that `ape command` (PLAN-12), the service (PLAN-14), and scripts
  (PLAN-15) all build on.
- It will become the natural "hello world" for the framework (see the
  tutorials note in the docs proposal).

## Non-goals

- No multi-step chains on the CLI (that's what pipeline YAML is for).
- No raw-prompt sessions without a skill (`ape command`, PLAN-12).
- No programmatic exec (removed by PLAN-9).

## Design

### F0: Trust-dialog dismiss + `WaitForReady` hardening (`internal/repl`)

Lands as its own PR before anything else — it de-risks all current
interactive runs, not just `task`.

- **Modal registry:** a `modalSpec{name, match, accept}` table of blocking
  modals claude may show before the REPL accepts input. First entry:
  `trust-folder` (matches "trust this folder" / "Is this a project you"),
  dismissed by `SendText("1")` → settle → `SendEnter` — selecting by number
  rather than relying on the preselected default, robust across versions.
  `dismissBlockingModals(ctx, name, snap)` dismisses at most one known modal
  per call and reports whether it acted. All signatures live in this one
  registry so the next onboarding screen (theme picker, "what's new") is a
  one-line addition, not a re-debug.
- **`replReady`:** replace the bare-`❯` check with signals a menu item
  cannot satisfy — primary: the `bypass permissions on` footer (always
  present in the real REPL because ape always passes
  `--dangerously-skip-permissions`); fallback: an empty prompt line
  `(?m)^\s*❯\s*$` (a menu item is `❯ 1. …`, never empty).
- **`WaitForReady` loop:** capture pane → dismiss known modal (then re-poll)
  → else test `replReady`. On timeout, the error **includes the last pane
  snapshot** so an unrecognized modal is diagnosable at 30s instead of a
  silent 1h idle. `interactiveReadyTimeout` stays 30s.
- **Tests** (fake `CapturePane` via a test seam): trust-then-ready (modal
  for 2 polls, then footer → dismissed once, returns nil);
  menu-is-not-ready (`❯ 1. Yes, I trust this folder` → `replReady` false —
  the regression guard for the exact bug); unknown-modal (timeout error
  contains the pane text).

### Command surface

```
ape task <skill> [flags]

  --agent <name>            framework agent (slash-command) fronting the skill
  --model <model>           claude model (spec `model:` equivalent, e.g. "opus[1m]")
  --args "<string>"         verbatim skill args (spec `args:`)
  --prompt "<text>"         run prompt (same semantics as pipeline --prompt)
  --prompt-flag <flag>      forward --prompt via this skill flag (spec `prompt_flag:`)
  --no-commit               skill-layer directive: tell the skill/framework not
                            to commit (adds skill-level --no-commit on the agent
                            path; the no-agent path already carries it)
  --task-commit ["<msg>"]   opt-in task-layer boundary commit: git-commit the
                            complete task at the end; bare flag derives the
                            message `ape:task/<skill>`
  --commit-allow-dirty      as on pipeline (relevant only with --task-commit)
  --idle-timeout <dur>      idle-without-Stop backstop (default: pipeline's 60m;
                            plain seconds accepted)
  --output-format human|json  json = the result envelope below (--json alias kept)
  --tui | --no-tui | --web  UI selector; default = pipeline's default; --open as on pipeline
                            (UI selectors may trail in a follow-up PR; headless first)
  --manifest-dir, --cwd, --quiet, --ignore-project-settings   as on pipeline
```

**Commit layers — two flags, two layers, composable (user decision
2026-07-02).**

- **Skill layer — `--no-commit`.** Maps to the framework's own
  `--no-commit` skill functionality, delivered in the slash line. Without
  the flag, `assembleInteractivePromptLine`'s existing convention applies
  byte-identically: no-agent path always carries skill-level `--no-commit`;
  agent path relies on framework commit semantics. With the flag, the agent
  path also gets skill-level `--no-commit` (`/{agent} --autonomous --
  {skill} --autonomous --no-commit {args}`); on the no-agent path it is a
  no-op (already present).
- **Task layer — `--task-commit ["<msg>"]`.** Opt-in runner boundary
  commit of the complete task working tree at the end of the run, via the
  existing `commit.go` machinery. Bare flag (cobra `NoOptDefVal`) derives
  `ape:task/<skill>` (mirroring the pipeline's derived
  `ape:<pipeline>/<stage>/<skill>` shape); with a value, that message is
  used. **Default is off** — no task-layer commit unless asked. The
  dirty-tree gate applies only when `--task-commit` is given.

The layers compose: `--no-commit --task-commit "feat: X"` suppresses the
framework's granular commits and produces exactly one whole-task commit.
The eval passes **neither flag** — default behavior is byte-identical to
its current `_invoke_skill` convention (skill-level `--no-commit` on the
no-agent path only, framework semantics on the agent path, no ape boundary
commit) and its own `SKILL:` boundary-commit expectations are untouched. A
parity unit test pins the assembled prompt line for all four flag
combinations.

### Result envelope (`--output-format json`, stdout)

Shaped to drop into the eval as the replacement for the stream-json result
event; all fields map from the existing `stepTelemetryToResultEvent` path:

```json
{
  "skill": "apex-create-prd",
  "agent": "apex-agent-pm",
  "model": "opus[1m]",
  "success": true,
  "exit_code": 0,
  "duration_seconds": 142.3,
  "cost_usd": 0.83,
  "usage": {
    "input_tokens": 0, "output_tokens": 0,
    "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0,
    "num_turns": 0
  },
  "commits": ["SKILL:create-prd"],
  "manifest_path": "_output/tasks/apex-create-prd/<run-id>/manifest.yaml",
  "error": null
}
```

`commits` = subjects of `git rev-list before..after` across the run —
framework-made commits included, not just ape's boundary commit. PLAN-10
later adds a `per_model` block additively.

### Exit codes

`0` success · `1` skill ran but failed / Stop-wait error / idle timeout ·
`2` usage or preflight error (repo convention) · `3` REPL never became
ready (trust-dismiss failed / unknown modal — last pane on stderr).
Registered in PLAN-9's exit-code table when that lands.

### Implementation

1. **`internal/pipeline`: exported constructor.**
   `NewSingleStepSpec(name string, step Step, commit *CommitDirective) *Spec`
   — builds `Spec{Name: name}`, one `Stage{Name: name, Chain: []Step{step}}`,
   populates the unexported `stages`/`stageMap` so `Effective`,
   `PlanStageCommits`, and both runners work untouched. Spec-level
   `Requires` empty; `Ref.Digest` computed over a canonical serialization of
   the step (so the manifest's provenance field stays meaningful).
2. **`internal/apecmd/task.go`** (`newTaskCmd()`, one command per file per
   convention): parse flags → `framework.ResolveSkill` on skill and agent
   (fail fast with the same `PreflightError`/exit-2 semantics as pipeline) →
   build the Spec → reuse the pipeline dispatch: the existing `runConfig` +
   `runWithInteractive` / `runWithInteractiveTUI` / `runWithWeb` are called
   with the synthesized spec. Refactor those helpers to accept a `*Spec`
   instead of a name-to-load where needed (today they load by name; the
   refactor threads an already-loaded spec through — pipeline path calls
   `LoadSpec` first, task path passes the synthetic one).
3. **Artifacts.** Manifest base for tasks: `_output/tasks/<skill>/<run-id>/`
   (same writer, different base dir — `manifest_writer` already takes a base).
   `latest` symlink as for pipelines. `cost.RebuildRollup`/`FoldPipelineRun`
   (PLAN-10) learn the `_output/tasks/` tree; `Rollup` gains a `Tasks` bucket
   keyed by skill.
4. **Eventing hook-in.** Nothing task-specific: PLAN-13's publisher taps
   `RunOptions.Observer`, which the task run inherits.

## Steps

1. **PR-1 — F0** trust-dialog dismiss + `WaitForReady` hardening + the
   three fake-CapturePane tests. Ships alone; immediately de-risks
   existing pipeline runs.
2. **PR-2** — `NewSingleStepSpec` + unit tests (precedence, commit plan,
   digest) and the spec-threading refactor of `runConfig`/dispatch (no
   behavior change for pipeline; existing tests must stay green).
3. **PR-3** — `task.go` command: flag validation tests (mutual exclusions,
   missing skill, agent resolution failure), prompt-line parity tests
   against `assembleInteractivePromptLine` (agent and no-agent paths,
   byte-identical), envelope marshaling, exit-code mapping. Headless first;
   UI selectors may follow.
4. Rollup/manifest base extension + tests.
5. Integration (real-claude, build-tagged like the existing interactive
   tests): `ape task apex-create-prd --agent apex-agent-pm --output-format
   json` in a fresh temp dir with the framework installed → exit 0, valid
   envelope, no idle timeout — the end-to-end proof that F0 + task work
   together.
6. Docs: `how-to/run-a-single-skill.md`, `reference/cli.md` regen,
   README command table row. Consider re-pointing the "first tutorial" at
   `ape task` per the docs proposal.

**Ship gate (eval):** rebuild the eval's pinned ape binary, re-run
`pipeline --fixture ape-gf-hello-world --regenerate`, confirm create-prd
advances past the trust modal (no 1h idle). This is the acceptance test
for the whole trust fix and the eval's migration path off `claude -p`.

## Acceptance

- `ape task apex-shard-doc --args "--doc prd" --no-tui` on a fixture
  project runs one PTY step, writes
  `_output/tasks/apex-shard-doc/<run-id>/manifest.yaml` with telemetry, and
  makes no ape commit (task layer defaults off).
- Same invocation with `--task-commit "chore: shard prd"` produces exactly
  one commit with that message; bare `--task-commit` derives
  `ape:task/apex-shard-doc`.
- Prompt-line parity: all four `--no-commit` × agent/no-agent combinations
  assemble byte-identically to the pinned `assembleInteractivePromptLine`
  convention.
- `ape costs` shows the task bucket after the run.
- Preflight failure (unknown skill) exits 2 before any claude spawn.

## Risks / notes

- The spec-threading refactor touches the pipeline dispatch path — the
  highest-traffic code in the repo. Mitigation: it lands as a pure refactor
  PR (pipeline behavior identical) before the task command PR.
- Sequencing reversed on 2026-07-02: this plan now lands **before** PLAN-9
  (to unblock the eval). `task` is interactive-only from day one — no
  `-P`/`--eval` axis — so PLAN-9's later flag removal doesn't touch it.
  PLAN-10's per-model telemetry extends the envelope additively.
- The modal registry (F0) chases an unstable upstream surface (onboarding
  screens change across claude-code versions); the pane-in-error timeout
  means a new unknown modal costs 30s + a readable diagnosis, not a silent
  hour.
