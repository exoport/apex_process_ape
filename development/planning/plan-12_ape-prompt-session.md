---
plan_id: PLAN-12
created_at: 2026-07-02
implemented_at: 2026-07-14
status: done
implementation_notes: Shipped in v0.0.45. Landed as `ape prompt` (renamed 2026-07-13 from the planned `ape command`): positional prompt arg or `--handoff`, optional `--agent`/`--model`, `--workflow`/`--ultracode`, `--idle-timeout`, exit code 4, records under `_output/ape/prompts/<id>/`, rollup `prompts` bucket. Reuses the bridge/runlog/Stop-hook/telemetry scaffold via a new `internal/sessiondriver.Driver`. Deviation from the original plan: the sessiondriver extraction was **shallow at first** — `interactiveCore` kept its own duplicate idle-wait loop and shared only the telemetry scan with the Driver. PLAN-19 D5 later consolidated the wait loop into `sessiondriver` as the single owner, so the extraction this plan started is now complete. The service-side `prompt.run` contract rename (`command.run`→`prompt.run`, `KindCommand`→`KindPrompt`) and its wiring landed with PLAN-14.
tags:
  - new-command
  - pty
  - handoff
  - claude-session
summary: New `ape prompt` — drive a full Claude Code session from a prompt or a handoff file, headless, through the in-process PTY (`internal/repl`), never `claude -p`. Optional framework agent (delivered as the `/<agent> --autonomous -- …` slash prefix), optional model, a `--workflow` flag that forces the session to run the task through a Claude Code workflow, and a separate `--ultracode` flag that activates ultracode mode (standing workflow-by-default) for the session. Reuses the bridge runtime + runlog + Stop-hook completion + transcript/telemetry machinery from the interactive pipeline runner; records the session under `_output/ape/prompts/<id>/`.
origin:
  - 2026-07-02 user request — "a 'command' command, that in a similar way of the task command is able to run a claude code session passing a prompt, or a handoff file, with optional framework agent, optional claude code model, including a --workflow flag to force using claude code workflow."
  - 2026-07-02 user correction — `--workflow` is for workflow activation (make the session run the task via a workflow); a separate `--ultracode` flag activates ultracode mode instead. Two distinct flags, two distinct mechanisms.
  - 2026-07-02 pipeline-internals audit — `ape chat` (`internal/apecmd/chat.go`) already assembles the bridge + runlog scaffold but hands the raw TTY to the user with no prompt injection; the interactive pipeline runner already does PTY prompt injection + Stop-hook completion. `ape prompt` is the composition of the two.
  - 2026-07-13 rename — verb `ape command` → `ape prompt`. "command" collided with the general notion of ape subcommands (everything is a command); `ape session` was ruled out because `ape sessions` (the PLAN-5 bridge-session registry inspector) already exists — singular/plural would be a constant confusion. Follow-on design tweaks from the rename: the initial prompt becomes a **positional** arg (`ape prompt "…"`), so the old `--prompt` flag is gone; `--handoff` stays as the file-seeded alternative; session records move to `_output/ape/prompts/<id>/`; the result envelope's `command_id` becomes `prompt_id`; the rollup bucket is `Prompts`. The service-side contract rename (`command.run` → `prompt.run`, `KindCommand` → `KindPrompt`) should follow for consistency but is tracked as a separate decision (it touches the published `ape.svc` NATS contract + tests).
---

# PLAN-12: `ape prompt` — prompt/handoff-driven claude session

## Goal

`ape prompt --handoff development/handoffs/foo.md` (or `ape prompt "…"`) runs
an unattended Claude Code session end-to-end: spawn claude in a PTY, deliver
the prompt, let it work under the bridge's hook supervision, detect
completion via the Stop hook, capture transcript + per-model telemetry
(PLAN-10), and exit with a meaningful status. It is the primitive for "run
this one autonomous session" that the service (PLAN-14) and scripts
(PLAN-15) invoke.

## Why now

Handoff-driven session resumption is already part of the team's workflow
(handoff documents produced by one session, consumed by the next); today that
requires a human to paste into an interactive `claude`. This automates the
loop, PTY-only.

## Non-goals

- Not a REPL for humans — that's `ape chat` (unchanged).
- No skill/commit semantics — that's `ape task`. `ape prompt` makes **no
  commits** by itself; whatever the session commits is the session's
  business. (A `--no-commit`-style gate is therefore out of scope.)
- No multi-turn scripted conversations (send prompt → run to Stop → done;
  looping conversations belong to PLAN-15 scripts).

## Design

### Command surface

```
ape prompt [<text>] [flags]

  <text>                  initial prompt, positional (exactly one of <text>/--handoff)
  --handoff <file>        handoff document to seed the session with
  --agent <name>          optional framework agent (resolved via .claude/skills)
  --model <model>         optional claude model
  --workflow              force the session to run the task via a Claude Code workflow
  --ultracode             activate ultracode mode (workflow-by-default) for the session
  --idle-timeout <dur>    completion backstop (default: pipeline's 60m)
  --cwd, --quiet, --ignore-project-settings, --output-format   as elsewhere
```

Result object (stdout under `--output-format`): `{prompt_id, status,
duration, cost_usd, per_model, transcript_paths, session_id}`.

### Prompt assembly (assumptions — flag at review)

- `--agent A "<text>"` → the line sent is `/A --autonomous -- <text>` (same
  PAT-25 shape the pipeline uses for agent-fronted steps, minus a skill).
- `--handoff F`: the session is seeded with a short envelope prompt that
  references the file: `Read the handoff document at <abs-path F> and
  continue the work it describes.` — rather than pasting the (potentially
  large, markdown-heavy) file content through the PTY. Rationale: multi-line
  paste through a REPL is fragile (bracketed-paste support in the target CLI
  is version-dependent) and the file is local by definition. `--agent`
  composes: `/A --autonomous -- <envelope>`.
- `--workflow` appends an explicit workflow directive to the delivered
  prompt ("run this with a workflow" — Claude Code's documented user-side
  opt-in for multi-agent workflow orchestration on that task).
- `--ultracode` prepends the `ultracode` keyword to the delivered prompt —
  Claude Code's standing session-level opt-in that makes the session author
  and run workflows by default for every substantive task. The two flags are
  independent (`--ultracode` subsumes `--workflow` in practice; passing both
  is allowed and harmless). **Assumption to confirm for both**: if a stronger
  mechanism exists by implementation time (e.g., a settings key), use that
  instead; each flag's contract is the observable session behavior, not the
  delivery mechanism.

### Execution

1. Scaffold exactly as `runWithInteractive` does for one stage:
   `orchestrator.NewBridgeRuntime` + `buildInteractivePrepend` (bridge
   `--mcp-config`/`--settings` with hooks) + `runlog.New` under
   `_output/ape/prompts/<prompt-id>/` (`prompt-id` =
   `YYYYMMDD-HHMMSS-<7hex>`, reusing `runlog.NewChatID`'s shape).
   Layout convention (stated once, applies repo-wide): manifest-bearing
   runs live under `_output/pipelines/` and `_output/tasks/`;
   session-style records live under `_output/ape/` beside the existing
   `_output/ape/chats/` — prompt sessions are the latter. The rollup walker
   enumerates all of these trees.
2. `repl.NewSession(ctx, "ape-prompt-<pid>", cwd, argv)` where argv =
   `claude [prepend…] --dangerously-skip-permissions [--model M]` — identical
   to `buildInteractiveArgv`. `WaitForReady` → `SendCommand(<assembled line>)`.
3. Completion: Stop hook via the bridge (`interactiveCore`'s
   `WaitStepDone` machinery, generalized — see refactor note), with
   `--idle-timeout` and `repl.SessionDone` as backstops. On completion:
   PLAN-10 telemetry scan (main + subagent transcripts), transcript copy,
   `session.yaml`-style record, rollup fold (new `Prompts` bucket).
4. Exit codes — aligned with the **shipped PLAN-11 table**
   (`internal/apecmd/task.go:21`): 0 session completed (Stop hook), 1
   idle-timeout / session failed, 2 preflight (agent unresolved / handoff
   missing), 3 REPL never became ready (PLAN-11 convention — not reusable
   for anything else), 4 claude died before Stop (new; this plan's
   addition). Registered in PLAN-9's exit-code table.

### Refactor note

`interactiveCore` (`internal/apecmd/pipeline_interactive.go`) assumes
stage/step labels. Extract the reusable slice — hook fan-out, transcript
binding, telemetry baseline, step-done signaling — into a small
`sessiondriver` helper (internal), consumed by both the pipeline runner
wiring and `ape prompt`. Pipeline behavior unchanged.

## Steps

1. `sessiondriver` extraction (pure refactor PR; pipeline tests green).
2. `prompt.go` + prompt-assembly unit tests (agent/handoff/workflow
   combinations; mutual-exclusion validation of `<text>` vs `--handoff`).
3. End-to-end smoke against a fixture project with a trivial prompt
   (mirrors `internal/repl`'s bash-stand-in test pattern for CI; a
   claude-backed manual acceptance run documented in the PR).
4. Rollup `Prompts` bucket + `ape costs` row.
5. Docs: `how-to/run-a-handoff.md`, CLI reference regen, README row, **and
   publish the chat/task/prompt comparison table below** (see "Docs: the
   sibling-commands comparison table") — the user explicitly asked for this
   table to land in the docs once `ape prompt` ships.

## Docs: the sibling-commands comparison table

Capture this table in the docs when `ape prompt` ships — an explanation-quadrant
doc (`docs/explanation/chat-task-prompt.md`) is the natural home; also worth a
condensed version in the CLI reference. It disambiguates the three ways ape
drives a claude session, which is the recurring source of confusion the rename
was meant to reduce.

Two orthogonal axes separate them: **who drives** (a human vs ape, unattended)
and **what runs** (a framework skill vs a free session).

| | `ape chat` | `ape task` | `ape prompt` |
| --- | --- | --- | --- |
| **Driven by** | human at the keyboard | ape, unattended | ape, unattended |
| **PTY** | your terminal (inherited stdio) | in-process `internal/repl` | in-process `internal/repl` |
| **Input** | live keystrokes, freeform | a **skill** (required) + args | a positional prompt or `--handoff` |
| **Skill / agent** | neither | skill required, agent optional | no skill; agent optional |
| **Completion** | when claude exits | Stop hook + idle backstop | Stop hook + idle backstop |
| **Prompt injection** | none | yes | yes |
| **Commits** | none | two-layer (`--no-commit` / `--task-commit`) | none |
| **Telemetry scan** | no — runlog hooks/calls only | yes (PLAN-10) | yes (PLAN-10) |
| **`--output-format`** | no (interactive) | human/json | human/json |
| **Artifacts** | `_output/ape/chats/<id>/` (runlog) | `_output/tasks/<skill>/<run-id>/` (**manifest**) | `_output/ape/prompts/<id>/` (session record) |
| **Exit codes** | 0/1/2 | 0/1/2/3 | 0/1/2/3/**4** |

Lineage worth stating in the doc: `ape prompt` is `ape chat`'s bridge+runlog
scaffold with the human swapped for ape injecting a prompt/handoff and waiting
on the Stop hook — "chat's scaffold + task's autopilot." `ape task` is the
pipeline runner minus the YAML (one skill, with manifests and boundary
commits).

## Acceptance

- `ape prompt "create FILE.md with contents X" --no-... --quiet`
  against a sandbox: session runs, Stop hook fires, exit 0, FILE.md exists,
  `_output/ape/prompts/<id>/` contains runlog + copied transcript +
  telemetry with per-model numbers.
- `--handoff` variant with a real handoff doc resumes the described work.
- `--workflow` demonstrably changes session behavior (workflow invocation
  visible in the transcript); `--ultracode` likewise (ultracode opt-in
  acknowledged by the session).
- Killing claude mid-run exits 4; an unresolvable `--agent` exits 2 with no
  spawn.

## Risks

- Handoff-by-reference (envelope prompt) depends on the session actually
  reading the file — acceptable; the envelope is explicit, and it's the same
  contract as human-driven handoff consumption today.
- `--workflow` / `--ultracode` mechanisms (prompt directive / `ultracode`
  keyword) are soft contracts; each pinned by an acceptance test so drift is
  caught.
