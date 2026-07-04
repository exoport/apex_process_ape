# Why PTY-only execution

Up to and including ape `v0.0.35`, `ape pipeline` had two ways to drive `claude`: a **programmatic** path (one `claude -p "<prompt>"` subprocess per step, selected with `-P` / `--programmatic`, `-I` / `--interactive`, or the locked `--eval`) and an **interactive** path (one long-lived `claude` REPL per stage inside an in-process PTY). v0.0.36 (PLAN-9 F2) removes the programmatic axis. Every run now executes `claude` as an interactive REPL inside a per-stage in-process PTY (`internal/repl/`, backed by `github.com/aymanbagabas/go-pty`). The invocation axes collapsed from (UI × Exec) to just **UI**: `--tui` (default), `--web`, `--no-tui`. This page explains why.

## What was removed

The flags `-P` / `--programmatic`, `-I` / `--interactive`, and `--eval` are gone. They remain registered (hidden) for one release so an old invocation gets an actionable message instead of cobra's terse "unknown flag":

```
programmatic mode was removed in v0.0.36; interactive PTY is the only exec mode.
Drop -P/--programmatic, -I/--interactive, and --eval — the run is interactive by
default. See docs/explanation/why-pty-only.md.
```

Passing any of them exits `2` (usage error). Exit codes are centralized (`internal/apecmd/exitcodes.go`): `0` ok, `1` run failed / idle-timeout, `2` usage or preflight error, `3` the REPL never became ready inside the PTY.

## Why PTY is the only mode worth keeping

### 1. Real REPL parity with how users run claude

Under the PTY path, ape types each step into the same `claude` REPL a human would use — the slash command is written to the PTY master end, settles briefly, then Enter is pressed. claude's CLI parses the slash command the normal way, the skill loads, the model executes. There is no second, `-p`-shaped execution path whose behavior can silently diverge from the interactive one users actually see. One code path, one behavior to reason about.

### 2. The hooks/bridge step contract can verify each step

The bridge observes `UserPromptSubmit` and `Stop` hooks emitted by the live REPL. The `ContractVerifier` matches each step's typed prompt against the agent-prefixed skill shape the runner registered, and hard-fails on mismatch (see [step-contract.md](../reference/step-contract.md)). This observability only exists because the prompt is typed into a real session — `claude -p` gives a fresh process per step with no in-session state to verify, so a misbehaving skill leaking agent identity across a boundary went undetected. The `Stop` hook also serves as the "this step finished, safe to send the next prompt" signal, since an interactive REPL stays alive between turns rather than exiting per step.

### 3. Prompt-cache behavior across steps

One REPL per stage means the steps in a stage's chain share a single claude session. Multi-step skills that use an elicit/respond pattern (e.g. `apex-create-prd`) inherit the prior step's context naturally (set `no-clear: true` to skip the `/clear` the runner otherwise types between steps). Per-stage spawning also pays the process-fork / MCP-handshake / prompt-cache cold-start cost once per stage instead of once per step, and stops fighting Anthropic's prompt-cache TTL that the per-step `claude -p` shape hit on every chain step.

### 4. Transcript scanning is the single cost source

Cost and token metrics are derived by scanning the session transcript (`internal/cost/`), not by parsing a per-step `result` event. With one execution path, the transcript scan is the single source of truth for the manifest's `totals`, per-step metrics, and the per-model breakdown — including sub-agent sessions. See [pipeline-run-manifest.md](../reference/pipeline-run-manifest.md).

## Where the eval went

The old eval-harness path was `ape pipeline <name> --eval`, a byte-equivalence-locked programmatic run. That flag is gone. The eval now drives the interactive surfaces directly: `ape task --output-format json` for single-skill runs (a structured result envelope on stdout, progress on stderr) and `ape pipeline <name> --no-tui` for whole-pipeline runs (plain stdout progress lines). Both execute the same interactive PTY the default `--tui` run uses; there is no separate captured-for-replay code path to keep in sync.

## Related

- [invocation-matrix.md](../reference/invocation-matrix.md) — the UI axis: `--tui` (default), `--web`, `--no-tui`.
- [claude-spawn-modes.md](../reference/claude-spawn-modes.md) — how ape delivers prompts to the PTY-hosted REPL.
- [step-contract.md](../reference/step-contract.md) — what the runner types into the REPL between steps.
- [exec-modes.md](exec-modes.md) — the per-stage interactive runtime in depth.
- [bridge-architecture.md](bridge-architecture.md) — the MCP bridge and hook observability.
