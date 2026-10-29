# How to run an unattended session with `ape prompt`

`ape prompt` drives one Claude Code session from start to end with nobody at
the keyboard. It spawns claude in an in-process PTY, delivers a prompt (or
seeds the session from a handoff document), lets the session work under the
bridge's hook supervision, detects completion through the Stop hook, captures
the transcript and per-model telemetry, and exits with a meaningful status. It
makes **no commits** of its own: whatever the session commits is the
session's business.

## Basic run

```bash
ape prompt "add a CHANGELOG entry for the latest release"
```

The prompt is a **positional** argument. Exactly one of the positional
`<text>` or `--handoff <file>` must be given. Both, or neither, fails with
exit 2 before claude is spawned. `ape prompt` must run from a project root (a
directory containing `_apex/config.yaml`).

A plain prompt is typed as is, whatever its length. One that would not arrive
as written when typed (two or more spaces in a row, a tab, a line break, or
any other control character) is written verbatim to `prompt.md` in the
record directory, and the typed line names that file. `prompt.yaml` records
it as `args_file`.

After typing, ape presses Enter once claude has drawn the line, then confirms
the submit through the `UserPromptSubmit` hook, pressing Enter up to twice
more. A line still not submitted fails the session (exit 1).

## Resume from a handoff file

```bash
ape prompt --handoff development/handoffs/2026-07-13-resume.md
```

`--handoff <file>` seeds the session with a short prompt that references the
file by absolute path:

> Read the handoff document at `<abs file>` and continue the work it
> describes.

The file is not pasted through the PTY: a multi-line paste into a REPL is
fragile, and the file is local anyway. It must exist (exit 2 otherwise).

## Front the session with a skill

```bash
ape prompt "<prompt>" --agent <skill>
```

`--agent <skill>` wraps the delivered prompt in a slash command:

```text
/<skill> --autonomous -- <prompt>
```

The skill must resolve under `.claude/skills/<name>/SKILL.md` (project) or
`~/.claude/skills/` (user). An unresolved `--agent` fails preflight with exit
2 and never spawns claude. `--agent` composes with `--handoff` (it wraps the
handoff prompt).

With `--agent` the line is a slash command, and Claude Code turns a typed
burst past 800 characters into a `[Pasted text #N]` placeholder that never
runs as one. So past 700 characters the prompt goes to `prompt.md`, as above.

## What is refused

- **`--agent apex-loop`** exits 2: the loop runs as `ape task apex-loop`. See
  [How to run a single skill](run-a-single-skill.md).
- **`--agent apex-orchestrator`** exits 2: it is the operator's interface,
  opened by typing `/apex-orchestrator` in a person's own Claude Code session.
  See [The orchestrator and the loop](../explanation/orchestrator-and-loop.md).
- **`ape prompt` inside any ape-launched session** exits 2, code
  `session.depth`, or `session.not-allowed` from an `apex-loop` run, which
  starts only allow-listed skills with `ape task`. A prompt session starts
  only outside every ape session. See
  [When ape runs inside a session ape started](../explanation/nested-ape-runs.md).

## The stop hook decides when the session ends

In an APEX v1 project whose `.claude/settings.json` registers `ape hook stop`
(`ape project init` does), that hook decides each Stop, as it does under
`ape task`. A Stop it blocks, because background work is still running or a
check is red on an in-progress change's branch, continues the turn instead of
ending the session. The session completes on the first Stop the hook allows,
or at `--idle-timeout` or `--max-duration`.

`prompt.yaml` records the verdicts under `stop_gate`: `deferrals` (the Stops
it blocked), `allowed`, and one event per verdict with its time, verdict,
change and reason.

`ape prompt` passes no hooks inline, so the hook reaches the session only
through the project's settings. With `--ignore-project-settings` it does not
run, and the first Stop ends the session.

## Servers the session leaves running

When the session ends, ape stops any process it left running, such as a
server its Bash tool started in the background, and lists each one under
`session_leftovers` in `prompt.yaml`. Pass `--leftovers report` to record
them and leave them running. `ape task` does the same; see
[`session_leftovers`](../reference/pipeline-run-manifest.md#session_leftovers--what-the-session-left-running).

## `--workflow` and `--ultracode`

Two independent flags shape how the session runs the work:

- `--workflow` appends an explicit directive to the delivered prompt so the
  session runs the task through a Claude Code workflow.
- `--ultracode` prepends the `ultracode` keyword, Claude Code's session-level
  opt-in that makes the session author and run workflows by default.

They compose:

```bash
ape prompt "big refactor" --ultracode --workflow --model opus
```

## Machine-readable result

```bash
ape prompt "add a test" --output-format json
```

Progress streams to stderr. Stdout carries only the result envelope
(`--output-format` accepts `human`, `json` or `yaml`):

```json
{
  "prompt_id": "20261021-120102-abc1234",
  "status": "completed",
  "duration": 142.3,
  "cost_usd": 0.83,
  "per_model": {
    "claude-opus-5": { "cost_usd": 0.83, "input_tokens": 15031, "output_tokens": 30682, "num_turns": 26 }
  },
  "transcript_paths": ["_apex/state/runs/prompts/20261021-120102-abc1234/transcripts/abc1234.jsonl"],
  "session_id": "abc1234-..."
}
```

## Artifacts

Each session writes its runlog streams, the copied transcripts, and a
`prompt.yaml` session record under `_apex/state/runs/prompts/<prompt-id>/` in
an APEX v1 project, and under `{output_folder}/ape/prompts/<prompt-id>/`
elsewhere. `prompt-id` is `YYYYMMDD-HHMMSS-<7hex>`. Prompt sessions appear in
`ape costs` under the prompts bucket after `ape costs roll`, and
`ape costs prompt <prompt-id>` reads one session's totals. See
[Run artefacts](run-artefacts.md).

## Exit codes

| Code | Meaning |
| ---- | ------- |
| 0 | Session completed: the Stop hook fired (and, in a v1 project, the stop hook allowed it). |
| 1 | Session failed, the typed line never submitted, or a backstop fired: the idle window (`--idle-timeout`, default 60m) or a ceiling a person set (`--max-duration`, off by default). |
| 2 | Usage or preflight error: no `_apex/config.yaml`, an unresolved or refused `--agent`, a missing `--handoff` file, both or neither of `<text>` and `--handoff`, or the nesting rule. |
| 3 | The claude REPL never became ready, or the claude startup check found a claude ape cannot drive. The last pane is on stderr, and the raw terminal bytes are saved (`pty-tail.bin` in the record, or the user cache for the startup check). |
| 4 | claude exited before the Stop hook fired. |
| 5 | The session's own turn failed against the API and nothing followed, with `status: upstream_api_error`. Upstream and retryable. |
| 15 | The session lost its Claude Code login (`status: auth_lost`): its turn failed with `authentication_failed` and nothing followed for 3 minutes. A person logs in (`claude /login`) before running it again. |

## `ape chat` vs `ape task` vs `ape prompt`

See [Choosing between `ape chat`, `ape task`, and `ape prompt`](../explanation/chat-task-prompt.md).
In short: `ape chat` is a person at the keyboard; `ape task` runs one required
framework skill, with a manifest; `ape prompt` runs a free session from a
prompt or a handoff, with no skill and no commits.
