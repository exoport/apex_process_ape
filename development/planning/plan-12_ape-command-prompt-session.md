---
plan_id: PLAN-12
created_at: 2026-07-02
status: proposed
tags:
  - new-command
  - pty
  - handoff
  - claude-session
summary: New `ape command` — drive a full Claude Code session from a prompt or a handoff file, headless, through the in-process PTY (`internal/repl`), never `claude -p`. Optional framework agent (delivered as the `/<agent> --autonomous -- …` slash prefix), optional model, a `--workflow` flag that forces the session to run the task through a Claude Code workflow, and a separate `--ultracode` flag that activates ultracode mode (standing workflow-by-default) for the session. Reuses the bridge runtime + runlog + Stop-hook completion + transcript/telemetry machinery from the interactive pipeline runner; records the session under `_output/ape/commands/<id>/`.
origin:
  - 2026-07-02 user request — "a 'command' command, that in a similar way of the task command is able to run a claude code session passing a prompt, or a handoff file, with optional framework agent, optional claude code model, including a --workflow flag to force using claude code workflow."
  - 2026-07-02 user correction — `--workflow` is for workflow activation (make the session run the task via a workflow); a separate `--ultracode` flag activates ultracode mode instead. Two distinct flags, two distinct mechanisms.
  - 2026-07-02 pipeline-internals audit — `ape chat` (`internal/apecmd/chat.go`) already assembles the bridge + runlog scaffold but hands the raw TTY to the user with no prompt injection; the interactive pipeline runner already does PTY prompt injection + Stop-hook completion. `ape command` is the composition of the two.
---

# PLAN-12: `ape command` — prompt/handoff-driven claude session

## Goal

`ape command --handoff development/handoffs/foo.md` (or `--prompt "…"`) runs
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
- No skill/commit semantics — that's `ape task`. `ape command` makes **no
  commits** by itself; whatever the session commits is the session's
  business. (A `--no-commit`-style gate is therefore out of scope.)
- No multi-turn scripted conversations (send prompt → run to Stop → done;
  looping conversations belong to PLAN-15 scripts).

## Design

### Command surface

```
ape command [flags]

  --prompt "<text>"       initial prompt (exactly one of --prompt/--handoff)
  --handoff <file>        handoff document to seed the session with
  --agent <name>          optional framework agent (resolved via .claude/skills)
  --model <model>         optional claude model
  --workflow              force the session to run the task via a Claude Code workflow
  --ultracode             activate ultracode mode (workflow-by-default) for the session
  --idle-timeout <dur>    completion backstop (default: pipeline's 60m)
  --cwd, --quiet, --ignore-project-settings, --output-format   as elsewhere
```

Result object (stdout under `--output-format`): `{command_id, status,
duration, cost_usd, per_model, transcript_paths, session_id}`.

### Prompt assembly (assumptions — flag at review)

- `--agent A --prompt P` → the line sent is `/A --autonomous -- P` (same
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
   `_output/ape/commands/<command-id>/` (`command-id` =
   `YYYYMMDD-HHMMSS-<7hex>`, reusing `runlog.NewChatID`'s shape).
   Layout convention (stated once, applies repo-wide): manifest-bearing
   runs live under `_output/pipelines/` and `_output/tasks/`;
   session-style records live under `_output/ape/` beside the existing
   `_output/ape/chats/` — commands are the latter. The rollup walker
   enumerates all of these trees.
2. `repl.NewSession(ctx, "ape-command-<pid>", cwd, argv)` where argv =
   `claude [prepend…] --dangerously-skip-permissions [--model M]` — identical
   to `buildInteractiveArgv`. `WaitForReady` → `SendCommand(<assembled line>)`.
3. Completion: Stop hook via the bridge (`interactiveCore`'s
   `WaitStepDone` machinery, generalized — see refactor note), with
   `--idle-timeout` and `repl.SessionDone` as backstops. On completion:
   PLAN-10 telemetry scan (main + subagent transcripts), transcript copy,
   `session.yaml`-style record, rollup fold (new `Commands` bucket).
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
wiring and `ape command`. Pipeline behavior unchanged.

## Steps

1. `sessiondriver` extraction (pure refactor PR; pipeline tests green).
2. `command.go` + prompt-assembly unit tests (agent/handoff/workflow
   combinations; mutual-exclusion validation).
3. End-to-end smoke against a fixture project with a trivial prompt
   (mirrors `internal/repl`'s bash-stand-in test pattern for CI; a
   claude-backed manual acceptance run documented in the PR).
4. Rollup `Commands` bucket + `ape costs` row.
5. Docs: `how-to/run-a-handoff.md`, CLI reference regen, README row.

## Acceptance

- `ape command --prompt "create FILE.md with contents X" --no-... --quiet`
  against a sandbox: session runs, Stop hook fires, exit 0, FILE.md exists,
  `_output/ape/commands/<id>/` contains runlog + copied transcript +
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
