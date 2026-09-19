# Running `ape` from inside a session `ape` started

A conducting session — one `ape prompt` or `ape task` spawned — sometimes needs to run `ape` itself. An orchestrator persona dispatching `ape task`, a maintenance conductor running `ape change`: the outer run drives a `claude`, and that `claude` shells out to a binary that drives another one. Hooks inside hooks.

This works, and it is designed for rather than tolerated. What constrains it is not the nesting at all — it is the **outer** run's lifecycle.

> Everything below is read from the implementation. At the time of writing no nested dispatch had been measured end to end, and the idle-window half in particular is a prediction. The four mechanisms are each checkable in isolation, so a contradicting measurement identifies which one to fix.

## The four mechanisms that make it work

### 1. The child gets its own session, because ape takes the parent's markers away

`repl.ScrubClaudeCodeEnv` strips `CLAUDECODE`, `CLAUDE_EFFORT` and the whole `CLAUDE_CODE_*` family from the environment of every `claude` ape spawns.

That family is not decoration. It carries the parent's session id **and the marker that suppresses transcript persistence** — a `claude` launched with it inherited writes no transcript of its own. Since the transcript is what ape scans for telemetry, an unscrubbed nested session would run, work, and report nothing.

The scrub exists because ape is *usually* run from inside a Claude Code session already: a person typing `ape pipeline` into their own session is the same shape as a conducting session dispatching one. Authentication (`ANTHROPIC_*`) passes through untouched.

### 2. Hooks cannot cross runs, because the port is not inherited

Each run writes its own `--settings` file, and the hook command in it is literal:

```
env APE_BRIDGE_PORT=<port> <ape-bin> notify --event <event>
```

The port is baked into the command string, and `env(1)` sets it at hook time, so it wins over anything in the environment. An inner run's hooks reach the inner bridge; they have no way to reach the outer one. This is structural, not a coincidence of port allocation.

### 3. Telemetry cannot cross runs, because a run only reads the transcript its own hooks named

The driver learns its transcript path from the `transcript_path` field of its own bridge's `UserPromptSubmit` payload. A run can only read the transcript its own `claude` reported.

The two sessions are unrelated top-level sessions — not parent and sub-agent — so the outer run's `sessions[]` never contains the inner's. `ape costs` counts both runs, once each, which is correct: two runs, two token bills.

### 4. The PATH pin is transitive

`selfpath.Pin` resolves `os.Executable()` through symlinks and prepends a directory whose only entry is `ape` pointing at the real binary. The inner `ape` is launched through that shadow, so its own `os.Executable()` is the same file, and its own pin points there too. At any depth, `ape` inside the session is the binary that started it.

## What actually constrains nesting

### A foreground call cannot work

Claude Code's Bash tool has its own timeout — roughly two minutes by default, ten at the most. A lane run or a dispatch is routinely longer, so the shell dies with the inner run still going.

Even without that, the outer run would time out. The idle anchor resets on a hook event from *this run's* bridge, on *this run's* transcript growing, and on PTY bytes where a probe is installed. A blocked foreground call produces none of them, so the outer run sees silence for the inner run's whole duration and eventually stops itself.

### A background shell, polled, is the shape that works

Start the inner run in a background shell and poll it.

The reason polling works is worth stating, because it reads like a workaround and is not one: **every `BashOutput` poll is a tool call**. It fires `PreToolUse` and `PostToolUse` on the outer bridge and grows the outer transcript. The act of checking whether the inner run has finished is the act that proves the outer run is still working. Replace the polling with something tidier — a single long sleep, a blocking wait — and the outer run goes silent and is killed, with no error that names the cause.

ape also sets `CLAUDE_CODE_DISABLE_BG_SHELL_PRESSURE_REAP` on every spawn, because Claude Code otherwise kills a running background shell on a memory-pressure event. An unattended run cannot see that happen. With the variable set, the shell holding the inner run survives.

### Never yield the turn with an inner run outstanding

ape derives completion from the turn's `Stop` hook. A conductor that starts an inner run in the background and then ends its turn tears down **its own** run — the outer one — while the inner keeps going, orphaned. This is the same rule that applies to an outstanding sub-agent, for the same reason. See [Why a `Stop` hook is not proof a step finished](step-completion-gates.md).

### One `ape` at a time per project

Two concurrent runs race on the working tree, and the second one's pre-flight reads the first one's edits as dirt. Outside nesting this is tidiness; with nesting it is load-bearing, because the outer run's own dispatches are the likeliest source of uncommitted work.

## Where the run artifacts land

Separate by construction — the outer and inner runs write to different roots under `{output_folder}/ape/`:

| Run | Directory |
| --- | --- |
| outer `ape prompt` | `prompts/<id>/` |
| inner `ape change` | `changes/<id>/`, and its dispatch under `tasks/<skill>/<run-id>/` |
| inner `ape task` | `tasks/<skill>/<run-id>/` |

Nothing needs to deduplicate them: each run's manifest describes its own session.
