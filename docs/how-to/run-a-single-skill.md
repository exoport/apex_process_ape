# How to run a single skill with `ape task`

`ape task <skill>` runs one framework skill in its own unattended Claude Code
session. ape spawns claude as a REPL in a PTY, types the skill invocation as
keystrokes, waits for the Stop hook, and writes a run manifest. In an APEX v1
project every skill of the loop runs this way: you start `apex-plan` or
`apex-loop` with it, and the loop starts the other skills with it.

## Run a skill

```bash
ape task apex-plan --args "<request>"
ape task apex-loop --args M1
ape task apex-apply --args <change-id>
```

The skill must resolve under `.claude/skills/<name>/SKILL.md` (project) or
`~/.claude/skills/` (user). An unknown skill fails preflight with exit 2 before
claude is spawned.

`--args` is the skill's argument, typed after the skill name. Each v1 skill
says what its argument is: a brief or a request for `apex-plan`, a milestone
or tweak id for `apex-loop`, a change id for `apex-apply` and `apex-review`.

### Long or multi-line arguments

ape does not type a line longer than 700 characters. Claude Code turns a typed
burst past 800 characters into a `[Pasted text #N]` placeholder that never
runs as a skill. So when the line would be longer, ape writes the `--args`
text, verbatim and with its newlines, to a file in the run directory,
`stages/01-<skill>/step-01-<skill>.args.md`, and types a line that names that
file instead. The step-start event records it as `args_file`.

ape uses the same file, whatever the length, when the text would not arrive as
written if typed: two or more spaces in a row, a tab, a line break, or any
other control character. Leading and trailing whitespace of `--args` is not
part of the text. Shorter, single-spaced, one-line text is typed as is.

After typing, ape presses Enter once claude has drawn the line, then confirms
the submit through the `UserPromptSubmit` hook, pressing Enter up to twice
more. A line that is still not submitted ends the run with termination
`prompt_not_submitted` (exit 1) rather than idling to the timeout.

## Run the loop in the background

The loop runs for hours, so start it detached:

```bash
ape task apex-loop --args M1 --detach
ape run wait <id> --wait 9m
```

`--detach` prints the run's id and returns at once. See
[How to run ape in the background](detach-a-run.md).

`apex-orchestrator` is not run with `ape task`. It is your own interface to
the project: you open it by typing `/apex-orchestrator` in your own Claude
Code session, and it starts `apex-loop` for you. `ape task apex-orchestrator`
is refused with exit 2, code `skill.renamed`, and the message names both
`ape task apex-loop --args <M>` and `/apex-orchestrator`. It is not an alias,
so an old resume line fails loudly instead of starting the wrong skill. See
[The orchestrator and the loop](../explanation/orchestrator-and-loop.md).

## Choose the model and effort

In an APEX v1 project ape decides the session's model and effort and records
the decision in the run manifest as
`launch: {model, model_source, effort, effort_source}`:

| | First match wins |
| --- | --- |
| **model** | `--model`; else `models.<skill>` in `_apex/config.yaml`; else the model of the ape session that started this one; else claude's default |
| **effort** | `--effort`; else `models.<skill>.effort`; else the parent session's (skipped when `models.<skill>` names a model without an effort); else the top-level `effort:` in `_apex/config.yaml`; else none |

`models.<skill>` takes a model alone or a model with an effort:

```yaml
models:
  apex-review: opus
  apex-apply: {model: sonnet, effort: high}
```

`ape project init` writes defaults for the core skills. A bare family name
(`opus`, `sonnet`, `haiku`, …) resolves to its current generation;
`sonnet-5`, `claude-sonnet-5` or `opus[1m]` pin one.

The effort is passed as `claude --effort <level>`, which sets the session's
effort only. `CLAUDE_CODE_EFFORT_LEVEL` is never set (an inherited one is
removed), and `_apex/effort-defaults.yaml` is not read. The session's tools
see the resolved values as `APE_SESSION_MODEL` and `APE_SESSION_EFFORT`.

Outside a v1 project, an unset `--effort` comes from the project's
`_apex/effort-defaults.yaml`; see `ape config effort --help`.

## What refuses a session

In an APEX v1 project these checks run before anything is spawned:

- **The ape floor.** When `_apex/ape-commands.yaml` declares a
  `min_ape_version` above this ape, the session is refused (exit 1, code
  `ape.below-floor`) with the command that updates ape. A dev build or a
  pseudo-version cannot be compared: ape warns and runs the session.
- **The milestone budget.** Off by default. Turn it on in
  `_apex/config.yaml`:

  ```yaml
  limits:
    budget: enforce   # or off
  ```

  With `enforce`, and a `Budget:` line on the current milestone (the first not
  done, and only once approved), a session is refused (exit 1, code
  `milestone.budget-exceeded`) when the milestone's spend is already at its
  budget. A running session is stopped when the spend reaches it: its runs
  since approval, plus this session's live cost read from its transcripts
  every 30 seconds, sub-agents included. The stop ends the run (exit 1) with
  termination kind `budget` in the manifest, and is recorded under the
  milestone's stops in `_apex/state/limit-stops.json`; `ape status` shows it.
  Raising the `Budget:` line is a person's call.
- **Consecutive blocks.** `ape task apex-loop` is refused (exit 1, code
  `milestone.blocks-exceeded`) when the milestone it runs has had
  `limits.consecutive_blocks` (default 2) changes blocked in a row. A person
  deals with them and re-approves the milestone, which resets the count. A
  tweak's run is not refused.
- **An unparseable `_apex/config.yaml`** refuses every session (exit 2)
  rather than run it with no limits.
- **An APEX v0 project** is refused (exit 2): it runs its skills on the v0 ape
  its `.bingo` pins.

The `limits:` block is guarded: a commit that changes it and is not ape's own
makes `ape change start`, `verify` and `archive` refuse until a person accepts
it with `ape project accept-config`, so a session cannot loosen its own
limits. See [`_apex/config.yaml`](../reference/config-yaml.md).

## Sessions inside sessions

A session that ape started may start another only when it is ape's own
`apex-loop` run, only a skill on the loop's allow-list, and only one child at
a time. Anything else is refused with exit 2 (`session.depth`,
`session.not-allowed`), or exit 3 when the loop already has a child running
(`session.child-running`) or the project already has a loop run
(`session.orchestrator-running`). There is no switch to turn the rule off. See
[When ape runs inside a session ape started](../explanation/nested-ape-runs.md).

## The loop relay

A loop session's context grows with its milestone, so ape relays it. When the
loop launches its next child with its context fill at or over
`limits.context_relay`, the launch is refused (exit 2, code `session.relay`):
the loop starts nothing, replies and ends, and the `ape task` process running
it starts a fresh loop session with the same args, under the same lock.

```yaml
limits:
  context_relay: 0.6   # 0 < x <= 0.95; default 0.6
  relays: 5            # relays per loop run; default 5
```

The fill is the newest turn of the loop session's transcript (input, cache
read and cache creation tokens) over its model's context window; an unknown
window means no relay. The relayed session's manifest records `relay_of` and
`relay`, and the JSON envelope carries `relays` and `relay_of`. Past
`limits.relays` the launch is refused with `limits.relays` (exit 1) and the
run stops with termination `relays`. `ape status --json` shows a live loop's
fill and relays under `sessions[].relay`. Both keys are guarded, like every
limit.

## Usage limits and the notifier

When a claude.ai plan's usage limit stops the session, Claude Code draws its
limit line and no hook fires again. ape reads the pane every 15 seconds; the
same line on two reads with no hook between them ends the run (exit 14) with
termination kind `usage_limit`, `resets` as printed and `resets_at` parsed.
Run again after the reset. The first limit screen per Claude Code version is
kept, raw, in `<user cache>/ape/claude-limit-<version>.bin`, with the last
usage reading beside it (`.json`); the stop's message names both.

Better not to reach the limit. Every unattended session ape launches records
the plan's usage from its status line (`ape usage` prints the last reading;
`ape chat`, your own session, keeps your status line), and in an
APEX v1 project ape's usage guard reads it before each `ape task` session
starts: over `limits.usage.five_hour` with the launch's share it **holds** the
launch until the 5-hour window resets, and over `limits.usage.seven_day` it
**stops** (exit 14, termination `usage_guard`) and leaves the call to you. An
unknown or stale reading launches, with a note. To launch anyway, from a
terminal:

```bash
ape task apex-loop --args M1 --usage-ok
```

The thresholds, the share and `off` are in
[config-yaml.md](../reference/config-yaml.md#limitsusage).

In an APEX v1 project, every run logs `run-end` in `_apex/state/events.jsonl`
when it ends (and `stop` first when it stopped on a limit), and the optional
`notify:` command in `_apex/config.yaml` then runs (`sh -c`, 10-second timeout,
output dropped) with `APE_EVENT`, `APE_RUN`, `APE_EXIT`, `APE_SKILL`,
`APE_CHANGE`, `APE_REASON` and the rest in its environment. See
[How to hear from a project](hear-from-a-project.md).

## Commit control

APEX v1 skills commit their own work. Two flags change that:

- `--no-commit` (skill layer) types `--no-commit` into the invocation, telling
  the skill not to commit. Without it none is typed.
- `--task-commit ["<msg>"]` (task layer, off by default): ape commits the
  whole working tree at the end of the run. A bare flag derives the message
  `ape:task/<skill>`. The dirty-tree gate applies only with this flag; bypass
  it with `--commit-allow-dirty`.

A run's commits are the range its HEAD moved over. A commit made while
another ape session's run in the same checkout was going (other than this
run's parent loop or its own children) cannot be attributed, so neither run
claims it: both list it under `commits_shared`.

### The commit-ownership assertion

When a project declares commit ownership in `_apex/commit-owners.csv`, ape
asserts every dispatch against it, writes the verdict to the manifest as
`commit_contract`, and exits 6 on a violation. No APEX v1 release ships that
file, so a v1 project is not asserted unless it adds one. A project with no
file is reported as skipped, never as a pass.

APEX v0 projects run the v0 ape their `.bingo` pins; its docs are at
https://github.com/exoport/apex_process_ape/tree/v0.10.0/docs.

## Prompt flags

`--prompt`, `--prompt-file` and `--handoff` forward text through a skill flag
named by `--prompt-flag`. Without `--prompt-flag` ape refuses (exit 2) rather
than start the skill without the text. No APEX v1 skill takes one. To resume
a handoff, paste its continuation line after `/clear`.

`--prompt-file <path>` reads the text from a file, and `-` reads stdin, so
text a person wrote never goes through the shell's argv. The text must be one
line, with no control characters and no trailing backslash.

## Machine-readable result

```bash
ape task apex-plan --args "<request>" --output-format json
```

Progress streams to stderr. Stdout carries only the result envelope:

```json
{
  "skill": "apex-plan",
  "model": "claude-opus-5",
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
  "commits": ["<sha>"],
  "manifest_path": "_apex/state/runs/tasks/apex-plan/20261021-120000-abc1234/manifest.yaml",
  "error": null
}
```

`commits` lists every commit made during the run, the skill's own included,
oldest first. `cache_creation_input_tokens` is the total cache-write count,
and the 5m and 1h fields split it by tier. Other fields appear when they
apply: `model_usage`, `sessions`, `commits_shared`, `commit_contract`,
`relays`, `relay_of`.

## Servers the session leaves running

A session may start a server and leave it running, for example from its Bash
tool with `&`. That server is no longer claude's child once its shell exits.
When the session ends, ape finds every process still carrying the session's
`APE_SESSION` marker and stops its process tree. It prints one `⚠` line and
lists each process under `session_leftovers` in the manifest. With
`--leftovers report`, ape records them and leaves them running. ape's own
detached workers are never touched, nor anything they started: a server a
detached `ape milestone gate` started keeps running when the session ends,
because the worker carries its own `APE_SESSION` marker. Nothing in an
`ape chat` session is touched either. This works on Linux only. See
[`session_leftovers`](../reference/pipeline-run-manifest.md#session_leftovers--what-the-session-left-running).

## Artifacts

In an APEX v1 project each run writes its manifest, per-step event stream and
runlog streams under `_apex/state/runs/tasks/<skill>/<run-id>/`, a directory
git ignores, with a `latest` symlink to the newest run. Elsewhere the same
tree is under `{output_folder}/ape/tasks/`. Override the base with
`--manifest-dir`. See [Run artefacts](run-artefacts.md).

Task runs appear in `ape costs` under `task:<skill>` after `ape costs roll`;
`ape costs run <run-id>` shows one.

## Exit codes

| Code | Meaning |
| ---- | ------- |
| 0 | Success. |
| 1 | The run failed: the skill failed, the idle window (`--idle-timeout`, default 60m) or a ceiling a person set (`--max-duration`) fired, or the typed line never submitted. Also a v1 refusal or stop: `ape.below-floor`, `milestone.budget-exceeded`, `milestone.blocks-exceeded`, `limits.relays`. |
| 2 | Usage or preflight error: an unknown skill, bad flags, an unparseable v1 config, an APEX v0 project, `skill.renamed`, or the nesting rule (`session.depth`, `session.not-allowed`, `session.relay`). |
| 3 | The REPL never became ready (the last pane is on stderr, and the raw bytes are saved beside the run), or a session lock another run holds (`session.child-running`, `session.orchestrator-running`). |
| 5 | The session's own turn failed against the API and nothing followed. Upstream and retryable. |
| 6 | The dispatch violated a declared commit ownership. The run itself may have succeeded. |
| 14 | A claude.ai usage limit stopped the session. Run again after the reset. |

## Related

- [How to tune long-running steps](tune-long-running-steps.md)
- [Choosing between `ape chat`, `ape task`, and `ape prompt`](../explanation/chat-task-prompt.md)
- [CLI reference](../reference/cli.md#ape-task)
