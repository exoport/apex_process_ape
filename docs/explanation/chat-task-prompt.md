# Choosing between `ape chat`, `ape task`, and `ape prompt`

ape has three ways to drive a Claude Code session. They look similar (all
three attach the bridge and capture a runlog), but they differ along two
axes: **who drives** (a person, or ape unattended) and **what runs** (a
required framework skill, or a free session). This page explains the
difference and when to reach for each.

|                       | `ape chat`                        | `ape task`                          | `ape prompt`                          |
| --------------------- | --------------------------------- | ----------------------------------- | ------------------------------------- |
| **Driven by**         | a person at the keyboard          | ape, unattended                     | ape, unattended                       |
| **PTY**               | your terminal (inherited stdio)   | in-process `internal/repl`          | in-process `internal/repl`            |
| **Input**             | live keystrokes, freeform         | a **skill** (required) + `--args`   | a positional prompt or `--handoff`    |
| **Skill**             | none                              | required                            | none; `--agent` optional              |
| **Completion**        | when claude exits                 | Stop hook + progress-aware backstop | Stop hook + progress-aware backstop   |
| **Prompt injection**  | none                              | yes                                 | yes                                   |
| **Commits**           | none of ape's                     | the skill's own; `--no-commit` / `--task-commit` | none of ape's            |
| **Telemetry scan**    | no (runlog hooks and calls only)  | yes                                 | yes                                   |
| **`--output-format`** | no (interactive)                  | human/json                          | human/json/yaml                       |
| **Detach**            | no                                | `--detach`                          | no                                    |
| **Record**            | runlog only                       | `manifest.yaml`                     | `prompt.yaml`                         |
| **Exit codes**        | 0/1/2                             | 0/1/2/3/5/6/14                      | 0/1/2/3/4/5                           |

In an APEX v1 project the records live under `_apex/state/runs/`:
`chats/<chat-id>/`, `tasks/<skill>/<run-id>/` and `prompts/<prompt-id>/`.
Elsewhere they are under `{output_folder}/ape/`. See
[Run artefacts](../how-to/run-artefacts.md).

All three are model sessions under the nesting rule: each starts only outside
every ape session, except that ape's own `apex-loop` run may start
allow-listed skills with `ape task`. See
[When ape runs inside a session ape started](nested-ape-runs.md).

## How a session ends

Both `ape task` and `ape prompt` end on the bridge Stop hook. In an APEX v1
project the project's own stop hook (`ape hook stop`) decides each Stop: one
it blocks, because background work is still running or a check is red,
continues the turn instead of ending the session.

Behind the Stop hook, a **progress-aware backstop** cancels a session only
after a full idle window (`--idle-timeout`, default 60m) with no progress
across *any* watched signal: bridge hooks, the transcript growing, and on the
`ape prompt` path PTY output. A session that is working is never cancelled
for being slow. A wall-clock `--max-duration` ceiling exists, but is off
unless a person sets one: a session showing progress stops only on the idle
window, ape's own limits, or a person.

`ape task` watches hooks and transcript growth but **not** PTY output, on
purpose: a sub-agent call emits hooks throughout, so PTY output would anchor
almost nothing new, while "the TUI is animating" is not evidence that work is
happening. A claude waiting on a child that no longer exists animates exactly
like one doing the work. An idle cancellation prints `pty n/a` for that
reason, which is how to tell *ape stopped watching* from *claude stopped
working*. See [How to tune long-running sessions](../how-to/tune-long-running-steps.md).

## What the exit codes separate

`ape task` has the most outcomes to tell apart, because it carries the APEX v1
limits:

- **1**: the run failed, or a v1 limit refused or stopped it (the ape floor,
  the milestone budget, consecutive blocks, the loop's relays);
- **2**: usage, preflight, a refused skill (`skill.renamed`), or the nesting
  rule;
- **3**: the REPL never became ready, or a session lock another run holds;
- **5**: the API failed upstream, retryable;
- **6**: the dispatch broke a declared commit ownership, though the run may
  have succeeded;
- **14**: a claude.ai usage limit stopped the session.

`ape prompt` has no skill to judge and no v1 limits, but it reports **4**
when claude exits before the Stop hook fires. `ape chat` ends when you exit
claude, so it has only success, failure and usage.

## Lineage

`ape prompt` is `ape chat`'s bridge and runlog scaffold with the person
swapped for ape injecting a prompt (or handoff) and waiting on the Stop hook:
"chat's scaffold plus task's autopilot". `ape task` adds the skill, the run
manifest and the APEX v1 controls: model and effort resolution, the limits,
the nesting rule and the loop relay.

## When to reach for which

- **`ape chat`**: you want to work with claude yourself in the project, with
  hooks captured for later inspection. You type, claude responds, and the
  session ends when you exit.
- **`ape task`**: you want exactly one framework skill run unattended (from a
  terminal, a script, the service, or the loop), with a manifest. The skill is
  the unit of work. In an APEX v1 project this is how every skill of the loop
  runs, and `ape task apex-loop` is the unattended runner of a milestone.
- **`ape prompt`**: you want one free, unattended session from a prompt or a
  handoff document, with no skill and no commits of ape's.

Your own work with APEX v1 happens in none of these: you open
`/apex-orchestrator` in your own Claude Code session, and it starts
`apex-plan`, `apex-change` and `apex-loop` as detached `ape task` runs. See
[The orchestrator and the loop](orchestrator-and-loop.md).

`ape prompt` is singular and distinct from `ape sessions`, the inspector of
the live-session registry. A prompt session is a single unattended run; it is
not a REPL for people (that is `ape chat`) and carries no skill (that is
`ape task`).
