# How to find and read run artefacts

Every `ape task`, `ape prompt` and `ape chat`, and every change or milestone
verb that runs checks, leaves records in the project. This page shows where
they are, which of them git tracks, and what each file is for.

## In an APEX v1 project: `_apex/state/`

An APEX v1 project keeps ape's records in `_apex/state/`, a folder only ape
writes: the edit hook denies it to models, and `ape framework update` never
touches it. `ape project init` creates it.

```text
<project>/_apex/state/
├── .gitignore            ← a whitelist: *, !.gitignore, !milestones.json, !decisions.json
├── milestones.json       ← tracked: approvals, gate records, consecutive blocks
├── decisions.json        ← tracked: the decision records
├── project.json          ← ignored: init's record (initialized_at, brownfield, base_sha)
├── attempts.json         ← ignored: red verifies per change
├── limit-stops.json      ← ignored: sessions stopped at a limit, per milestone
├── logs/                 ← ignored: check output (logs/<change-id>/, gate-<M>-approve.log)
├── hooks/                ← ignored: the Stop hook's per-session verdicts and logs
├── strays/               ← ignored: copies of files a check left behind and ape undid
└── runs/                 ← ignored: ape's run records
    ├── tasks/<skill>/<run-id>/
    ├── prompts/<prompt-id>/
    ├── chats/<chat-id>/
    ├── detached/<id>.json, <id>.log, <id>.out
    ├── <change-id|M>.json, <change-id|M>.log
    ├── sessions/
    ├── relays/
    ├── delegate/
    ├── service/
    └── cost-rollup.json
```

Only `milestones.json` and `decisions.json` are tracked. Everything else is
ignored by the folder's own whitelist `.gitignore`, so a run never dirties
the tree, and a file ape adds there later is ignored unless the whitelist
names it.

What sits in `runs/`:

| Path | What it is |
| --- | --- |
| `tasks/<skill>/<run-id>/` | One `ape task` run, with a `latest` symlink beside the run directories. See [Task runs](#task-runs). |
| `prompts/<prompt-id>/` | One `ape prompt` session. See [Prompt sessions](#prompt-sessions). |
| `chats/<chat-id>/` | One `ape chat` session. See [Chat sessions](#chat-sessions). |
| `detached/` | The handle, log and stdout of each `ape task --detach` run. See [How to run ape in the background](detach-a-run.md). |
| `<change-id\|M>.json`, `.log` | The run lock and log of a change or milestone verb running detached (`ape change verify`, `ape milestone gate`, …). |
| `sessions/` | The nesting rule's locks: one `apex-loop` run per project, one child at a time. |
| `relays/` | One record per live loop run, for the loop relay. Removed when the run ends. |
| `delegate/` | One verdict per human-review delegate run (`ape change accept --delegate`). |
| `service/` | `ape service`'s job logs. |
| `cost-rollup.json` | The project cost rollup `ape costs` reads, rebuilt by `ape costs roll`. |

Handles, locks, relays and verdicts are not runs: each session they belong to
writes its own manifest under `tasks/`.

### `.gitignore`

`ape project init` also manages one block of the root `.gitignore`: it keeps
`.claude/settings.json` and `.claude/skills/` tracked (the team's) and ignores
the rest of `.claude/` and `CLAUDE.local.md` (personal). See
`ape project init --help`.

## Task runs

```text
_apex/state/runs/tasks/<skill>/
├── latest -> <run-id>
└── <run-id>/
    ├── manifest.yaml           ← the run: launch, totals, commits, termination
    ├── pipeline-report.md      ← the manifest rendered for a person
    ├── harness.yaml            ← the Claude Code version that produced the run
    ├── hook-events.jsonl
    ├── bridge-calls.jsonl
    ├── checkpoints.jsonl
    ├── pty-tail-<skill>.bin    ← only when claude never became ready
    ├── stages/01-<skill>/
    │   ├── step-01-<skill>.ndjson   ← the event stream
    │   └── step-01-<skill>.args.md  ← only when the arguments were not typed
    └── transcripts/
```

- `<run-id>` is `YYYYMMDD-HHMMSS-<7-char hash>`. ape refuses to start a run
  whose directory already exists.
- **`manifest.yaml` is the summary**, and `stages/…/step-01-<skill>.ndjson` is
  the event stream that produced it. When a run fails, the stream is the file
  to open. See [the manifest reference](../reference/pipeline-run-manifest.md).
- **`step-01-<skill>.args.md`** holds the `--args` text when it was too long to
  type or would not have arrived as written. See
  [How to run a single skill](run-a-single-skill.md#long-or-multi-line-arguments).
- **`pty-tail-<skill>.bin` exists only when claude never became ready**: a
  pre-REPL screen ape could not get past, or a timeout. It holds the last
  64 KiB of raw terminal output, escape sequences included: the bytes the
  pane in the error was drawn from. Keep it with the bug report; `cat -v`
  shows the sequences.

Override the base directory with `--manifest-dir`. `ape script` writes here
too: a script's sessions are tasks.

## Prompt sessions

```text
_apex/state/runs/prompts/<prompt-id>/
├── prompt.yaml          ← the session record: status, model, cost, tokens, stop_gate
├── prompt.md            ← only when the prompt was not typed
├── harness.yaml
├── hook-events.jsonl
├── bridge-calls.jsonl
├── checkpoints.jsonl
├── pty-tail.bin         ← only when claude never became ready
└── transcripts/
```

`prompt.yaml` carries the full per-model token breakdown, which is why
`ape costs reprice` can recompute a prompt's cost after a price-table
correction. Read one with `ape costs prompt <prompt-id>`. Its `stop_gate`
records the stop hook's verdicts; see
[How to run an unattended session](run-a-prompt-session.md#the-stop-hook-decides-when-the-session-ends).

## Chat sessions

`ape chat` runs claude as a direct child of ape with stdio inherited; the
bridge is wired for hook observability.

```text
_apex/state/runs/chats/<chat-id>/
├── harness.yaml
├── hook-events.jsonl
├── bridge-calls.jsonl
└── checkpoints.jsonl
```

- `<chat-id>` is `YYYYMMDDTHHMMSSZ` (UTC).
- claude's own transcript stays at
  `~/.claude/projects/<encoded-cwd>/<session-id>.jsonl`; the chat directory
  holds no copy.

## Outside a v1 project: `{output_folder}/ape/`

In a project without `_apex/state/`, ape writes the same run trees under
`{output_folder}/ape/`, a subtree of the framework's output folder. The
folder's path comes from `output_folder` in `_apex/config.yaml`: `_output` by
default, and `_output` too when the config is absent or does not parse.

```text
<project>/_output/
└── ape/
    ├── tasks/<skill>/<run-id>/
    ├── prompts/<prompt-id>/
    ├── chats/<chat-id>/
    ├── detached/
    ├── service/
    └── cost-rollup.json
```

ape writes nothing in the output folder outside `ape/`, and does not manage
its `.gitignore`: whether run artefacts are committed is the project's call.
Ignore `_output/` (or whatever `output_folder` names) as a whole, or just
`_output/ape/`.

APEX v0 projects run the v0 ape their `.bingo` pins; its docs are at
https://github.com/exoport/apex_process_ape/tree/v0.10.0/docs.

## Cross-project state

```text
~/.ape/
└── registry.json        ← live ape sessions across all projects
```

`ape sessions` lists them, `ape sessions prune` drops dead pids. The nesting
rule reads the same registry; see
[When ape runs inside a session ape started](../explanation/nested-ape-runs.md).

In the user cache directory (`~/.cache/ape/` on Linux,
`~/Library/Caches/ape/` on macOS, `%LocalAppData%\ape\` on Windows):

```text
ape/
├── version-check.json          ← the background update check
├── claude-contract.json        ← claude versions this ape has verified it can drive
└── claude-probe-<version>.bin  ← only when a claude failed that check: its raw PTY bytes
```

**The claude startup check.** The first `ape task` or `ape prompt` on a
Claude Code version this ape has not seen spawns it once in a fresh, untrusted
scratch directory, through the trust dialog to a ready REPL, spending no
tokens, and records the pair in `claude-contract.json`. A claude that fails
stops the run with the reason, the screen and the saved bytes (exit 3),
because every run on it would meet the same screen. One the check cannot
judge (claude draws nothing before the deadline, as when offline or logged
out) is reported as a warning and the run continues; nothing is recorded, so
it is checked again next time. A new ape re-checks too. `APE_CLAUDE_PROBE=off`
skips the check and says so on every run. Deleting `claude-contract.json`
forces a re-check.

## File schemas

- `hook-events.jsonl`: one JSON per line,
  `{"ts","event","step","session_id","agent_id","payload"}`. `step` is `null`
  for events whose session id has not yet been bound to a step.
- `bridge-calls.jsonl`: one JSON per line,
  `{"ts","method","tool","params","result","session_id","id"}`. Every MCP
  tool call the bridge sees, including `tools/list`, `ping` and `initialize`.
- `checkpoints.jsonl`: one JSON per line, `{"ts","kind","step","payload"}`.

## Reading further

- [Run manifest reference](../reference/pipeline-run-manifest.md)
- [Bridge IPC](../reference/bridge-ipc.md): the wire that feeds the JSONL files.
- [Bridge security](../reference/bridge-security.md): bind and auth model.
