# How to run an unattended session with `ape prompt`

`ape prompt` drives one Claude Code session end-to-end without a human
at the keyboard: it spawns claude in the in-process PTY, delivers a
prompt (or seeds the session from a handoff document), lets the session
work under the bridge's hook supervision, detects completion via the
Stop hook, captures the transcript + per-model telemetry, and exits with
a meaningful status. It makes **no commits** of its own — whatever the
session commits is the session's business.

## Basic run

```bash
ape prompt "create CHANGELOG.md summarizing the last release"
```

The initial prompt is a **positional** argument. Exactly one of the
positional `<text>` or `--handoff <file>` must be given — supplying both,
or neither, fails with exit code 2 before any claude spawn. `ape prompt`
must run from a project root (a directory containing `_apex/config.yaml`).

## Resuming from a handoff file

```bash
ape prompt --handoff development/handoffs/2026-07-13-resume.md
```

`--handoff F` seeds the session with a short envelope prompt that
references the file by absolute path:

> Read the handoff document at `<abs F>` and continue the work it
> describes.

rather than pasting the (potentially large, markdown-heavy) file through
the PTY — multi-line paste through a REPL is fragile, and the file is
local by definition. The file must exist (exit code 2 otherwise).

## Fronting the session with a framework agent

```bash
ape prompt "refactor the parser" --agent apex-agent-dev
```

`--agent A` wraps the delivered prompt in the same PAT-25 slash prefix
the pipeline uses, minus a skill:

```
/apex-agent-dev --autonomous -- refactor the parser
```

The agent must resolve under `.claude/skills/<name>/SKILL.md` (project)
or `~/.claude/skills/` (user) — an unresolved `--agent` fails preflight
with exit code 2 and never spawns claude. `--agent` composes with
`--handoff` (it wraps the envelope prompt).

## `--workflow` and `--ultracode`

Two independent flags shape how the session runs the work:

- `--workflow` appends an explicit directive to the delivered prompt so
  the session runs the task through a Claude Code workflow.
- `--ultracode` prepends the `ultracode` keyword — Claude Code's
  session-level opt-in that makes the session author and run workflows
  by default for every substantive task.

They compose (`--ultracode` subsumes `--workflow` in practice; passing
both is allowed and harmless):

```bash
ape prompt "big refactor" --ultracode --workflow --model "opus[1m]"
# delivers: ultracode big refactor Run this task using a Claude Code workflow.
```

## Machine-readable result

```bash
ape prompt "add a test" --output-format json
```

Progress streams to stderr; stdout carries only the result envelope
(`--output-format` accepts `human`, `json`, or `yaml`):

```json
{
  "prompt_id": "20260713-120102-abc1234",
  "status": "completed",
  "duration": 142.3,
  "cost_usd": 0.83,
  "per_model": {
    "claude-opus-4-7": { "cost_usd": 0.83, "input_tokens": 15031, "output_tokens": 30682, "num_turns": 26 }
  },
  "transcript_paths": ["_output/ape/prompts/20260713-120102-abc1234/transcripts/abc1234.jsonl"],
  "session_id": "abc1234-..."
}
```

## Artifacts

Each session writes its runlog streams, the copied transcript(s), and a
`prompt.yaml` session record under
`_output/ape/prompts/<prompt-id>/`, where `prompt-id` is
`YYYYMMDD-HHMMSS-<7hex>`. Prompt sessions appear in `ape costs` under the
`prompts` bucket after `ape costs roll`, and a single session's totals
are readable with `ape costs prompt <prompt-id>`.

## Exit codes

| Code | Meaning                                                                  |
| ---- | ------------------------------------------------------------------------ |
| 0    | Session completed — the Stop hook fired.                                 |
| 1    | Session failed or the idle-without-Stop timeout fired (`--idle-timeout`, default 60m). |
| 2    | Usage or preflight error — no `_apex/config.yaml`, unresolved `--agent`, missing `--handoff` file, or both/neither of `<text>`/`--handoff`. |
| 3    | The claude REPL never became ready in the PTY — the last pane snapshot is on stderr. |
| 4    | claude exited before the Stop hook fired.                                |

## `ape chat` vs `ape task` vs `ape prompt`

See [Choosing between `ape chat`, `ape task`, and `ape prompt`](../explanation/chat-task-prompt.md)
for the full comparison. In short: `ape chat` is a human at the keyboard;
`ape task` runs one required framework **skill** (with manifests and
boundary commits); `ape prompt` runs a free session from a prompt or
handoff (no skill, no commits) — "chat's scaffold + task's autopilot".
