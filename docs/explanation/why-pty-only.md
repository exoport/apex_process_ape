# Why PTY-only execution

ape drives `claude` one way: as an interactive REPL inside an in-process PTY, with each prompt typed as keystrokes. It never runs `claude -p`. This page explains why that is the only mode worth keeping, how the PTY is held, and what the hooks give ape that claude's output stream cannot.

## What was removed

An older ape also had a **programmatic** path: one `claude -p "<prompt>"` subprocess per step, chosen with `-P` / `--programmatic`, `-I` / `--interactive`, or `--eval`. That path and its flags are gone. Passing one now gets cobra's own error and exit 2:

```
$ ape task <skill> --programmatic
Error: unknown flag: --programmatic
```

## How the PTY is held

ape allocates the PTY in its own process (`internal/repl/`, backed by `github.com/aymanbagabas/go-pty`), which uses Unix PTYs on Linux and macOS and ConPTY on Windows, so no `tmux` binary is needed on `PATH`. It writes keystrokes to the PTY master: the line, then Enter once claude has drawn it. It reads the pane back through a terminal emulator, which is how it knows the REPL is ready and when a line was drawn. `ape chat` is the one exception: it hands claude your terminal, and you type.

A session lives and dies with the ape process that holds it; there is no external attach to a running one. To follow a run, read its step event log or its hook events; to run one in the background, use `ape task --detach` ([detach-a-run.md](../how-to/detach-a-run.md)).

## Why PTY is the only mode worth keeping

### 1. Real REPL parity with how people run claude

ape types each line into the same `claude` REPL a person would use. claude's CLI parses the slash command the normal way, the skill loads, the model executes. There is no second, `-p`-shaped execution path whose behaviour can silently diverge from the interactive one people actually see. One code path, one behaviour to reason about.

### 2. The hooks let ape check each step

The bridge observes the `UserPromptSubmit` and `Stop` hooks the live REPL emits. ape matches the submitted line against the skill shape it typed, and hard-fails on a mismatch ([step-contract.md](../reference/step-contract.md)). That check exists only because the prompt is typed into a real session.

### 3. Transcript scanning is the single cost source

Cost and token figures come from scanning the session transcripts (`internal/cost/`), not from a `result` event. With one execution path, the transcript scan is the one source for a run manifest's totals, its per-step figures and its per-model breakdown, sub-agent sessions included ([the run manifest](../reference/pipeline-run-manifest.md)).

A caller that wants a structured result, the eval included, uses `ape task --output-format json`: a result envelope on stdout, progress on stderr. It drives the same PTY session as a human-format run; there is no captured-for-replay path to keep in sync.

## What hooks add over claude's output stream

ape injects Claude Code hooks (`PreToolUse`, `PostToolUse`, `UserPromptSubmit`, `SubagentStart`, `SubagentStop`, `SessionStart`, `PreCompact`, `Stop`) through the `--settings` it passes, and each one reaches ape through `ape notify` and the bridge. A fair question: what do hooks give that an output stream would not?

### 1. `UserPromptSubmit`: what was actually submitted

An output stream shows the model's **output**: text, tool calls, tool results. It does not show what was typed at the prompt. Two of ape's checks need exactly that:

- the step contract, which matches the submitted line against the skill shape ape typed;
- the submit confirmation: ape presses Enter, then waits for `UserPromptSubmit`, pressing Enter up to twice more, and ends the run as `prompt_not_submitted` if the line never arrives.

### 2. `Stop`: the turn ended

The REPL **stays alive** between turns, waiting for the next keystroke, so the process exiting tells ape nothing. The `Stop` hook is synchronous and fires once per ended turn, so it is the signal that the session finished responding. Pane output alone cannot say when. A `Stop` is still not proof the work is done; [step-completion-gates.md](step-completion-gates.md) explains what ape checks before it takes one, and how an APEX v1 project's stop hook can keep the session going.

### 3. `PreToolUse`: interception before a tool runs

A hook fires **before** a tool runs and can deny the call, which no after-the-fact stream event can. ape's own bridge hooks only observe. In an APEX v1 project, the project's `ape hook pre-tool` does intercept: it denies some edits (under `development/specs/`, `development/archive/` and `_apex/state/`, a change's `state.json`, an existing brief) and some Bash commands (a tag write, a push, a rebase or merge on a change branch, `pkill`). See [hooks.md](../reference/hooks.md).

## Related

- [claude-spawn-modes.md](../reference/claude-spawn-modes.md): every command that starts a session, and how the prompt reaches it.
- [step-contract.md](../reference/step-contract.md): what `ape task` types into the REPL, and what it checks.
- [step-completion-gates.md](step-completion-gates.md): when a `Stop` completes a step.
- [hooks.md](../reference/hooks.md): the hooks of an APEX v1 project.
