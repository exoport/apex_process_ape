# Choosing between `ape chat`, `ape task`, and `ape prompt`

ape has three ways to drive a Claude Code session. They look similar —
all three attach the bridge and capture a runlog — but they differ along
two orthogonal axes: **who drives** (a human vs ape, unattended) and
**what runs** (a required framework skill vs a free session).

|                       | `ape chat`                        | `ape task`                          | `ape prompt`                          |
| --------------------- | --------------------------------- | ----------------------------------- | ------------------------------------- |
| **Driven by**         | human at the keyboard             | ape, unattended                     | ape, unattended                       |
| **PTY**               | your terminal (inherited stdio)   | in-process `internal/repl`          | in-process `internal/repl`            |
| **Input**             | live keystrokes, freeform         | a **skill** (required) + args       | a positional prompt or `--handoff`    |
| **Skill / agent**     | neither                           | skill required, agent optional      | no skill; agent optional              |
| **Completion**        | when claude exits                 | Stop hook + progress-aware backstop | Stop hook + progress-aware backstop   |
| **Prompt injection**  | none                              | yes                                 | yes                                   |
| **Commits**           | none                              | two-layer (`--no-commit` / `--task-commit`) | none                          |
| **Telemetry scan**    | no — runlog hooks/calls only      | yes                                 | yes                                   |
| **`--output-format`** | no (interactive)                  | human/json                          | human/json/yaml                       |
| **Artifacts**         | `_output/ape/chats/<id>/` (runlog) | `_output/tasks/<skill>/<run-id>/` (**manifest**) | `_output/ape/prompts/<id>/` (session record) |
| **Exit codes**        | 0/1/2                             | 0/1/2/3                             | 0/1/2/3/**4**                         |

Both `ape task` and `ape prompt` end on the bridge Stop hook. Behind that,
a **progress-aware backstop** (PLAN-19) cancels a step only after a full idle
window (`--idle-timeout`, default 60m) with no progress across *any* signal —
bridge hooks, the transcript growing, or PTY output — not just the old
hook-only 60m timer. A step that is actively working is never cancelled for
being slow; a hard `--max-duration` ceiling (default 3h) is the absolute stop.
See [How to tune long-running steps](../how-to/tune-long-running-steps.md).

## Lineage

`ape prompt` is `ape chat`'s bridge + runlog scaffold with the human
swapped for ape injecting a prompt (or handoff) and waiting on the Stop
hook — **"chat's scaffold + task's autopilot"**. `ape task`, in turn, is
the pipeline runner minus the YAML: one skill, with manifests and
boundary commits.

## When to reach for which

- **`ape chat`** — you want to work with claude interactively in the
  project, with hooks captured for later inspection. You type; claude
  responds; the session ends when you exit.
- **`ape task`** — you want to run exactly one framework skill headless
  (in CI, a script, or the service), with a manifest and optional
  end-of-task commit. The skill is the unit of work.
- **`ape prompt`** — you want to run one free, unattended session from a
  prompt or a handoff document, with no skill and no commits. This is the
  primitive for "run this one autonomous session" that handoff-driven
  resumption and higher-level automation build on.

The recurring source of confusion the naming was meant to reduce: `ape
prompt` is singular and distinct from `ape sessions` (the bridge-session
registry inspector). A prompt session is a single unattended run; it is
not a REPL for humans (that is `ape chat`) and carries no skill/commit
semantics (those are `ape task`).
