# Explanation — interactive vs programmatic exec modes

`ape pipeline` has two ways to drive `claude`:

- **Programmatic** (PLAN-1 through PLAN-5): one `claude -p <prompt>` subprocess per step. Each step spawns claude fresh, prompt is supplied via `-p`, claude exits when the response completes.
- **Interactive** (PLAN-6): one persistent `claude` REPL per **stage**, running inside a per-stage tmux session. Steps in the stage's chain are delivered as real REPL keystrokes via `tmux send-keys` (e.g., the runner types `/clear` between steps, then the agent-prefixed skill slash command). claude exits when the tmux session is killed at stage end.

This page explains why both modes exist, what each one is for, and the tradeoffs.

## What changed in PLAN-6

Before PLAN-6, every `ape pipeline` invocation (including `--web`) used programmatic exec. `--web` layered the bridge MCP subprocess and hook observability on top of the per-step `claude -p` shape — interactive-looking in the browser but still programmatic underneath.

PLAN-6 separates the two concerns:

- The **UI axis** picks where output renders: `tui` (Bubble Tea), `web` (HTTP/SSE), or `none` (plain stdout).
- The **Exec axis** picks the claude lifetime: per-stage (interactive) or per-step (programmatic).

The default is now **tui + interactive**. See the [invocation matrix](../reference/invocation-matrix.md) for the full set of combinations.

## Why per-stage interactive

Three reasons the per-stage shape earns its complexity:

### 1. Process-spawn cost moves from per-step to per-stage

Every `claude` spawn pays:

- Process fork/exec
- MCP handshake (bridge + any other servers)
- Prompt-cache cold start
- Settings + system-prompt parsing

A pipeline with 13 stages × 4 steps = 52 spawns under programmatic mode. Interactive mode collapses that to 13 spawns (one per stage), regardless of chain length.

### 2. Multi-step chains share context within a stage

Skills like `apex-create-prd` use an elicit/respond pattern: the first step gathers user decisions; the second step responds based on those decisions. Under programmatic mode each step is a fresh claude process, so the context has to be either re-loaded from disk or passed via skill state. Under interactive mode the responding step inherits the prior step's REPL session naturally (set `no-clear: true` to skip the otherwise-default `/clear` that the runner types between steps).

This avoids fighting Anthropic's prompt-cache TTL (5 minutes since March 2026, per PLAN-5 origin notes), which made the per-step shape pay the cache miss on every chain step.

### 3. The bridge step contract catches misbehaving skills

Interactive mode enforces a per-step contract (see [step-contract.md](../reference/step-contract.md)). A skill that invokes the wrong agent prefix gets caught at the first `UserPromptSubmit` hook and the run hard-fails.

Under programmatic mode this kind of check is invisible — every step's process is fresh, so there's no in-session state to corrupt.

## Why keep programmatic

Two reasons:

### 1. `--print` is locked

The eval harness at `apex_process_framework_eval` consumes `ape pipeline <name> --print` output byte-for-byte. Interactive mode injects hooks and changes the stdout stream shape. PLAN-6 invariant #1 locks `--print` to today's programmatic shape forever; future plans cannot violate it without breaking the eval contract.

### 2. Debugging single-step issues

When a skill misbehaves on its own (independent of context), the per-step `claude -p` shape gives a clean reproduction. `ape pipeline foo --tui -P` runs the TUI with programmatic exec — the per-step process is the unit of debugging.

## How the step contract works

For each step within a stage, the runner types into the stage's tmux pane:

1. `/clear` between steps (skipped for the first step of a stage, and for any step marked `no-clear: true`).
2. The skill prompt in PAT-25 shape (`/<agent> --autonomous -- <skill> --autonomous ...`).

Each `tmux send-keys` types the slash command literally, settles 300ms, then presses Enter — claude's REPL parses it as a real user-typed slash command and invokes the matching skill. The bridge observes a `UserPromptSubmit` hook for each line; the `ContractVerifier` checks the agent + skill prefix against what the runner registered via `BeginStep` and hard-fails on mismatch. `/clear` arrives outside any active-step contract window so the verifier ignores it. See [step-contract.md](../reference/step-contract.md) for the full mechanics.

## What hooks add over `--output-format stream-json`

Interactive mode and web mode both inject Claude Code hooks (`PreToolUse`, `PostToolUse`, `UserPromptSubmit`, `SubagentStart`, `SubagentStop`, `Stop`) via the inline `--settings` blob; the bridge subprocess receives them and forwards them to ape over IPC. Reasonable question: ape already streams `claude --output-format stream-json` line-by-line — what do hooks give us that the stream-json events don't?

Three things, two of which are load-bearing for PLAN-6.

### 1. `UserPromptSubmit` (load-bearing)

Stream-json shows the model's **output** — text, tool calls, tool results, the terminal `result` event. It does not show what the **user** typed at the prompt. The PLAN-6 / C4 step contract has to verify that the agent-prefixed skill prompt arrived in the right shape; that observability only comes from `UserPromptSubmit` hooks. Without them, the bridge cannot enforce the contract, and a misbehaving skill could leak agent identity across step boundaries without detection.

This is the reason hooks exist at all in interactive mode. Drop hooks and PLAN-6 / C4 collapses.

### 2. `Stop` as a step-done signal (load-bearing in interactive)

Under programmatic exec (`claude -p <prompt>`), the process exits when the response completes — `cmd.Wait()` is the natural step boundary, and stream-json's terminal `result` event arrives just before. Under interactive exec (no `-p`), claude **stays alive** between turns waiting for the next keystroke in the tmux pane; the process boundary is now the stage, not the step. The `Stop` hook fires synchronously per claude-loop iteration (one per "user turn ended"), so the runner subscribes to it as the "model finished this step's response — safe to send the next prompt" signal. Stream-json's `result` event isn't emitted in interactive mode, so pane output alone can't tell you when to advance.

`apecmd/pipeline_interactive.go` (FeedHook → stepDoneCh) wires the Stop-hook → `WaitStepDone` path; this is what makes the per-stage interactive runtime work at all.

### 3. `PreToolUse` / `PostToolUse` synchronous interception (latent)

Hooks fire **before** a tool runs and can block or modify the call (returning non-zero exit from the hook command aborts the tool). Stream-json events are read-only and arrive after the fact. PLAN-6 doesn't use this today, but it's the only mechanism by which a future plan could enforce tool-call policy (allowlists, redaction, rate limits). The infrastructure is in place; the policy isn't.

### What's partially redundant

Tool-call observability proper: stream-json already emits `tool_use` / `tool_result` events with full params + results. `PreToolUse` / `PostToolUse` hooks duplicate that signal but arrive on the bridge channel rather than mixed into the model's output stream. The duplication is structurally cleaner — the runlog writer has one source of truth for `bridge-calls.jsonl` rather than parsing stream-json — and it's what the web SSE surface consumes for its live activity feed. So the tool-call hooks aren't strictly necessary for correctness, but they pay for themselves in cleaner data flow.

### Why `--print` doesn't get hooks

`--print` is byte-equivalence-locked for the eval consumer (PLAN-6 invariant #1). Injecting hooks would spawn an `ape notify` subprocess per tool call, changing the per-step stdout stream shape (subprocess lifetime, ordering of stderr interleaving, timing of stream-json line emission). The eval at `apex_process_framework_eval` reads `--print` output verbatim and would break.

The trade-off is intentional: `--print` is the captured-for-replay path, not the live-observation path. If you need hooks, run interactive (default) or `--web -P`.

## When to override the default

| Want                                     | Flag                        |
| ---------------------------------------- | --------------------------- |
| Today's per-step debugging               | `--tui -P` or `--no-tui -P` |
| Old `--web` behaviour                    | `--web -P`                  |
| Locked byte-equivalent stdout (eval)     | `--print`                   |
| No UI, but still interactive             | `--no-tui`                  |
| Force interactive even with `-P` context | `--interactive` / `-I`      |

## Related

- [invocation-matrix.md](../reference/invocation-matrix.md)
- [claude-spawn-modes.md](../reference/claude-spawn-modes.md) — concrete lookup table for when `claude -p` is used
- [pipeline-yaml-schema.md](../reference/pipeline-yaml-schema.md)
- [step-contract.md](../reference/step-contract.md)
- [bridge-architecture.md](bridge-architecture.md)
