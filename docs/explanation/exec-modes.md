# Explanation — the per-stage interactive runtime

`ape pipeline` drives `claude` one way: a persistent `claude` REPL per **stage**, running inside a per-stage in-process PTY. Steps in the stage's chain are delivered as real REPL keystrokes by writing bytes to the PTY master end (e.g., the runner types `/clear` between steps, then the agent-prefixed skill slash command). claude exits when the PTY is closed at stage end. Originally implemented over an external `tmux` (PLAN-6 / 2026-05-20); PLAN-8 (2026-05-22) moved the PTY allocation in-process via `github.com/aymanbagabas/go-pty` so the runner works on Linux, macOS, and Windows (Git Bash incl.) without `tmux` on `PATH`.

Until v0.0.35 a second, **programmatic** path existed (one `claude -p <prompt>` per step, selected with `-P` / `--programmatic`, `-I` / `--interactive`, or `--eval`). v0.0.36 (PLAN-9 F2) removed it. This page explains why the per-stage interactive shape earns its complexity and how it works. For the standing rationale behind dropping the programmatic axis entirely, see [why-pty-only.md](why-pty-only.md).

## What changed

Before PLAN-6, every `ape pipeline` invocation (including `--web`) used programmatic exec — `--web` layered the bridge MCP subprocess and hook observability on top of the per-step `claude -p` shape, interactive-looking in the browser but still programmatic underneath. PLAN-6 (2026-05-20) added the interactive per-stage path and made it the default. v0.0.36 removed the programmatic path outright, collapsing the two orthogonal axes (UI × Exec) to a single **UI axis**: where output renders — `tui` (default), `web`, or `none`. See the [invocation matrix](../reference/invocation-matrix.md).

## Why per-stage interactive

Three reasons the per-stage shape earns its complexity:

### 1. Process-spawn cost moves from per-step to per-stage

Every `claude` spawn pays:

- Process fork/exec
- MCP handshake (bridge + any other servers)
- Prompt-cache cold start
- Settings + system-prompt parsing

A pipeline with 13 stages × 4 steps = 52 spawns under the old per-step shape. The per-stage shape collapses that to 13 spawns (one per stage), regardless of chain length.

### 2. Multi-step chains share context within a stage

Skills like `apex-create-prd` use an elicit/respond pattern: the first step gathers user decisions; the second step responds based on those decisions. One REPL per stage means the responding step inherits the prior step's session naturally (set `no-clear: true` to skip the otherwise-default `/clear` that the runner types between steps).

This avoids fighting Anthropic's prompt-cache TTL (5 minutes since March 2026, per PLAN-5 origin notes), which made the old per-step shape pay the cache miss on every chain step.

### 3. The bridge step contract catches misbehaving skills

The per-stage shape enforces a per-step contract (see [step-contract.md](../reference/step-contract.md)). A skill that invokes the wrong agent prefix gets caught at the first `UserPromptSubmit` hook and the run hard-fails. Under the old per-step shape this kind of check was invisible — every step's process was fresh, so there was no in-session state to corrupt or observe.

## How the step contract works

For each step within a stage, the runner types into the stage's PTY:

1. `/clear` between steps (skipped for the first step of a stage, and for any step marked `no-clear: true`).
2. The skill prompt in PAT-25 shape (`/<agent> --autonomous -- <skill> --autonomous ...`).

Each PTY Write types the slash command literally, settles 300ms, then presses Enter — claude's REPL parses it as a real user-typed slash command and invokes the matching skill. The bridge observes a `UserPromptSubmit` hook for each line; the `ContractVerifier` checks the agent + skill prefix against what the runner registered via `BeginStep` and hard-fails on mismatch. `/clear` arrives outside any active-step contract window so the verifier ignores it. See [step-contract.md](../reference/step-contract.md) for the full mechanics.

## What hooks add over `--output-format stream-json`

The runner injects Claude Code hooks (`PreToolUse`, `PostToolUse`, `UserPromptSubmit`, `SubagentStart`, `SubagentStop`, `Stop`) via the inline `--settings` blob; the bridge subprocess receives them and forwards them to ape over IPC. Reasonable question: ape already reads claude's stream output line-by-line — what do hooks give us that the stream events don't?

Three things, two of which are load-bearing.

### 1. `UserPromptSubmit` (load-bearing)

Stream events show the model's **output** — text, tool calls, tool results, the terminal `result` event. They do not show what the **user** typed at the prompt. The step contract has to verify that the agent-prefixed skill prompt arrived in the right shape; that observability only comes from `UserPromptSubmit` hooks. Without them, the bridge cannot enforce the contract, and a misbehaving skill could leak agent identity across step boundaries without detection.

This is the reason hooks exist at all. Drop hooks and the step contract collapses.

### 2. `Stop` as a step-done signal (load-bearing)

The interactive REPL **stays alive** between turns waiting for the next keystroke from the PTY; the process boundary is the stage, not the step. The `Stop` hook fires synchronously per claude-loop iteration (one per "user turn ended"), so the runner subscribes to it as the "model finished this step's response — safe to send the next prompt" signal. Pane output alone can't tell you when to advance.

`apecmd/pipeline_interactive.go` (FeedHook → stepDoneCh) wires the Stop-hook → `WaitStepDone` path; this is what makes the per-stage runtime work at all. A backstop idle-without-`Stop` timeout guards against a hung turn.

### 3. `PreToolUse` / `PostToolUse` synchronous interception (latent)

Hooks fire **before** a tool runs and can block or modify the call (returning non-zero exit from the hook command aborts the tool). Stream events are read-only and arrive after the fact. ape doesn't use this today, but it's the only mechanism by which a future plan could enforce tool-call policy (allowlists, redaction, rate limits). The infrastructure is in place; the policy isn't.

## Related

- [why-pty-only.md](why-pty-only.md) — why the programmatic exec axis was removed.
- [invocation-matrix.md](../reference/invocation-matrix.md)
- [claude-spawn-modes.md](../reference/claude-spawn-modes.md) — how ape delivers prompts to the PTY-hosted REPL.
- [pipeline-yaml-schema.md](../reference/pipeline-yaml-schema.md)
- [step-contract.md](../reference/step-contract.md)
- [bridge-architecture.md](bridge-architecture.md)
