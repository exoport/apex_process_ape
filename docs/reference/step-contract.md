# Reference — step contract

PLAN-6 / C4 defines the per-step interaction between the runner and `claude`. The runner spawns `claude` inside an in-process PTY (PLAN-8: `internal/repl/`) and delivers each step's prompt as real REPL keystrokes by writing bytes to the PTY master end. The bridge observes the resulting `UserPromptSubmit` and `Stop` hooks; the `ContractVerifier` checks each step's prompt shape and fires a hard-fail on mismatch.

Since v0.0.36 the PTY-hosted REPL is the only exec mode (the programmatic `claude -p` path was removed — see [why-pty-only.md](../explanation/why-pty-only.md)), so the contract applies to every run.

## What the runner does

For every step within a stage, the interactive runner produces this sequence:

1. **`/clear`** before the next step's prompt — **default-on for every step after the first**.
   - Opt out with `no-clear: true` at the step level when the step depends on the previous step's context (rare).
   - The first step of a stage skips `/clear` because the PTY and `claude` process are fresh by construction.
   - The slash command is typed into the PTY (Write to master + Enter); claude's REPL parses it and resets context.

2. **Agent-prefixed skill prompt** typed via PTY Write + Enter:
   - With agent: `/<step.agent> --autonomous -- <step.skill> --autonomous` (then args, then optional `--prompt <user prompt>`)
   - Without agent: `/<step.skill> --autonomous --no-commit` (then args, then optional `--prompt <user prompt>`)

`/model <X>` switches are **not** sent today; the resolved per-step model is recorded in the manifest but the per-step session keeps whatever model `claude` was launched with. Steps that need a different model run in a different stage.

## What the verifier checks

The `ContractVerifier` subscribes to `UserPromptSubmit` hook events via the bridge and matches each one against the active step's expected agent + skill prefix:

- The runner calls `BeginStep(StepContract{...})` right before typing the skill prompt.
- The first `UserPromptSubmit` after `BeginStep` must match the agent-prefixed shape above. Mismatch fires `OnViolation`.
- Once matched, the contract is satisfied; further `UserPromptSubmit` events for the same step (e.g., a skill that re-prompts itself internally) are silently accepted.
- `EndStep` is called after `WaitStepDone` returns, clearing the active contract so a stray late hook from the previous step doesn't match against a fresh one.

`/clear` between steps fires its own `UserPromptSubmit` hook, but the runner sends it **outside** any active-step window (between the previous step's `EndStep` and the next step's `BeginStep`). The verifier sees `active == nil` and silently ignores it.

## Failure mode

On the first violation, the verifier:

1. Emits a `step-contract-violation` line to stderr with a self-describing reason (expected agent + skill, got prompt).
2. Invokes the registered `OnViolation` callback. In production, the callback cancels the run context.
3. Disables further checks on the current step (one violation per step is enough).

The orchestrator returns a non-zero exit code; the manifest records `status: failed`.

## How `no-clear: true` is used

`no-clear: true` is **step-level only** (not stage- or pipeline-level). Use it on the second-and-later steps of a multi-step chain where context sharing is the point:

```yaml
name: design
stages:
  create-prd:
    chain:
      - skill: apex-create-prd
        agent: apex-agent-pm
      - skill: apex-create-prd-respond
        agent: apex-agent-pm
        no-clear: true # shares context with apex-create-prd
```

## Related

- [invocation-matrix.md](invocation-matrix.md)
- [pipeline-yaml-schema.md](pipeline-yaml-schema.md)
- [bridge-ipc.md](bridge-ipc.md)
- [../explanation/exec-modes.md](../explanation/exec-modes.md)
