# How to run a single skill with `ape task`

Run one framework skill without authoring a pipeline YAML — everything a
pipeline step gets (framework-agent prefix, preflight, bridge hooks,
manifest, telemetry), with the parameters passed as flags. Execution is
interactive-PTY only: claude runs as a REPL, the prompt is typed as
keystrokes, and completion is detected via the bridge Stop hook.

## Basic run

```bash
ape task apex-shard-doc --args "--doc prd"
```

The skill must resolve under `.claude/skills/<name>/SKILL.md` (project)
or `~/.claude/skills/` (user) — unknown skills fail preflight with exit
code 2 before any claude spawn.

## With a framework agent, model, and prompt

```bash
ape task apex-create-prd \
  --agent apex-agent-pm \
  --model "opus[1m]" \
  --prompt "a greeter CLI" --prompt-flag --prompt
```

`--agent` fronts the skill exactly like a pipeline step:
`/apex-agent-pm --autonomous -- apex-create-prd --autonomous …`.
`--prompt-flag` names the skill flag the `--prompt` value is forwarded
through (the `prompt_flag:` spec field equivalent).

## Resuming from a handoff file

```bash
ape task apex-create-prd \
  --agent apex-agent-pm \
  --handoff _output/handoffs/2026-07-05-x-handoff.md --prompt-flag --prompt
```

`--handoff <file>` is a shorthand for `--prompt`: it checks the file
exists and derives the prompt `Read <abs-path> and follow the Resume
Protocol inside it.` — the same continuation prompt the `/handoff`
skill itself suggests when it writes a handoff doc. It still needs
`--prompt-flag` to actually reach the skill, and is mutually exclusive
with `--prompt` (exit code 2 if both are given, or if the file doesn't
exist). It intentionally forwards a pointer to the file rather than
inlining its contents — the prompt is typed into the REPL as literal
keystrokes, and a multi-line value risks submitting early.

## Commit control — two independent layers

- `--no-commit` — **skill layer**: tells the skill/framework not to
  commit. The no-agent invocation shape already carries it by
  convention; with `--agent` it is injected into the skill invocation.
- `--task-commit ["<msg>"]` — **task layer**, off by default: ape
  commits the complete task working tree at the end of the run. A bare
  flag derives the message `ape:task/<skill>`.

They compose: `--no-commit --task-commit "feat: X"` suppresses the
framework's granular commits and produces exactly one whole-task
commit. The dirty-tree gate applies only when `--task-commit` is given
(bypass with `--commit-allow-dirty`).

## Machine-readable result

```bash
ape task apex-create-prd --agent apex-agent-pm --output-format json
```

Progress streams to stderr; stdout carries only the result envelope:

```json
{
  "skill": "apex-create-prd",
  "agent": "apex-agent-pm",
  "model": "opus[1m]",
  "success": true,
  "exit_code": 0,
  "duration_seconds": 142.3,
  "cost_usd": 0.83,
  "usage": {
    "input_tokens": 15031,
    "output_tokens": 30682,
    "cache_read_input_tokens": 1703661,
    "cache_creation_input_tokens": 195953,
    "cache_creation_5m_input_tokens": 61200,
    "cache_creation_1h_input_tokens": 134753,
    "num_turns": 26
  },
  "commits": ["SKILL:create-prd"],
  "manifest_path": "_output/tasks/apex-create-prd/20260702-120000-abc1234/manifest.yaml",
  "error": null
}
```

`commits` lists every commit made during the run (framework commits
included), oldest first. Cost and usage come from the session
transcript scan — the same telemetry pipeline steps record.
`cache_creation_input_tokens` is the total ephemeral cache-write count;
`cache_creation_5m_input_tokens` and `cache_creation_1h_input_tokens`
break it into the two tiers (added in v0.0.37, additive — the total is
their sum). The same split appears on each `model_usage` entry.

## Artifacts

Each run writes `manifest.yaml`, per-step ndjson, and runlog streams
under `_output/tasks/<skill>/<run-id>/` (a `latest` symlink tracks the
newest run). Task runs appear in `ape costs` under `task:<skill>` after
`ape costs roll`.

## Exit codes

| Code | Meaning                                                                  |
| ---- | ------------------------------------------------------------------------ |
| 0    | Success.                                                                 |
| 1    | Skill ran but failed, Stop-wait error, or idle timeout (`--idle-timeout`). |
| 2    | Usage or preflight error (unknown skill/agent, bad flags).               |
| 3    | REPL never became ready — trust-dialog dismissal failed or an unknown modal blocked; the last pane snapshot is on stderr. |
