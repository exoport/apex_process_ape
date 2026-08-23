# How to read `_output/ape/`

Every `ape pipeline`, `ape task`, `ape prompt` or `ape chat` invocation drops
artefacts under the project root. This document describes the layout and what
each file is for.

PLAN-5 / C6.

## What ape owns

The output folder is **the framework's**. Its path comes from `output_folder`
in `_apex/config.yaml` — `_output` by default, but a project may point it
anywhere — and the skills write handoffs, briefs and verify reports there.

ape resolves that variable and owns exactly one subtree of it,
**`{output_folder}/ape/`**, writing nothing outside it. Shown below at the
default:

```text
<project>/_output/                     ← framework-owned
├── handoffs/                          ← framework
├── governance/                        ← framework
├── functionality/                     ← framework
├── planning/                          ← framework
├── implementation/                    ← framework
├── framework-requests/                ← framework
├── retrospective/                     ← framework
├── ux-mockups/                        ← framework
├── ux-wireframes/                     ← framework
├── defer-*.md, retro-epic-*.md, …     ← framework (loose files, no dir)
└── ape/                               ← ape owns this, and only this
    ├── pipelines/<pipeline>/<run-id>/
    ├── tasks/<skill>/<run-id>/
    ├── prompts/<prompt-id>/
    ├── chats/<chat-id>/
    ├── service/
    └── cost-rollup.json
```

### Two path shapes

Run kinds nest in one of two ways, which is why the paths are not uniform:

| Shape | Kinds | Path |
| --- | --- | --- |
| **Grouped** | pipelines, tasks | `<root>/<group>/<run-id>/` — grouped by pipeline name or skill name |
| **Flat** | prompts, chats | `<root>/<id>/` — the id is unique on its own |

A grouped kind has something worth grouping by: you run the `design` pipeline
many times, and want its runs together. A prompt or chat has no such natural
bucket. `service/` is neither — it holds `<job-id>.log` files, not run
directories.

> **A project that renames `output_folder`** gets its ape artifacts under
> that folder too — `build/artifacts/ape/…` for `output_folder:
> build/artifacts`. When the config is absent or does not parse (a bare
> directory, a syntax error), ape falls back to the framework's own default,
> `_output`.

> **Moved in v0.0.53.** Pipeline and task runs used to live at
> `_output/pipelines/` and `_output/tasks/`, as siblings of the framework's
> own content, and ape ignored `output_folder` entirely. `ape framework
> setup|update` relocates them into `{output_folder}/ape/` automatically — whole run directories at a time, never
> overwriting, and reporting any run whose destination is already taken
> rather than merging it. `ape doctor` reports a project that still needs it
> (`runs.legacy_layout`), and `ape framework update --dry-run` shows what
> would move. Until a project is relocated, cost rollups and the
> hook-contract check read a project with no history — nothing is lost, it
> is just somewhere ape no longer looks.

## Pipeline runs

```
<project>/_output/ape/pipelines/<pipeline-name>/
├── latest -> <run-id>       ← symlink to the most recent run (see below)
└── <run-id>/
    ├── manifest.yaml        ← PLAN-3 per-step metrics, cost, commit shas
    ├── report.md            ← human-readable run report
    ├── hook-events.jsonl    ← one JSON per Claude Code hook (PLAN-5 / C4)
    ├── bridge-calls.jsonl   ← one JSON per MCP tool call seen by the bridge
    ├── checkpoints.jsonl    ← stage events + skill `reply()` + commit-made
    ├── stages/
    │   └── <NN>-<stage>/
    │       └── step-<NN>-<skill>.ndjson  ← the per-step event stream
    └── transcripts/
        ├── step-01-<skill>.jsonl  ← symlink into ~/.claude/projects/<hash>/<sid>.jsonl
        ├── step-02-<skill>.jsonl  ← …
        └── …
```

- `<run-id>` is `YYYYMMDD-HHMMSS-<7-char hash>` (PLAN-3 shape, unchanged).
- Collisions **fail loud**. ape refuses to start a run if
  `<run-id>` already exists. No auto-disambiguate, no overwrite.
- Transcripts are **symlinks**, not copies. The canonical Claude Code
  session JSONL stays under `~/.claude/projects/` and ape's run-dir
  references it. Deleting the source breaks the symlink — that's the
  trade for not double-storing transcripts.
- **`latest` sits one level above the run dir**, beside its siblings — not
  inside one. It is the path to reach for interactively, and the one most
  docs quote:

  ```bash
  cat _output/ape/pipelines/design/latest/report.md
  ```

  Tasks have one too (`tasks/<skill>/latest`). Prompts and chats do not:
  their roots are flat, so there is no group to hang a pointer off.
- **`stages/` is where per-step detail lives.** `manifest.yaml` is the
  summary; `stages/<NN>-<stage>/step-<NN>-<skill>.ndjson` is the event
  stream that produced it. When a step fails, this is the file to open.

## Task runs

`ape task <skill>` runs a single framework skill — the single-skill analogue
of a pipeline run, grouped by skill name rather than pipeline name:

```text
<project>/_output/ape/tasks/<skill>/
├── latest -> <run-id>
└── <run-id>/
    ├── manifest.yaml        ← same schema as a pipeline manifest, one step
    ├── hook-events.jsonl
    ├── bridge-calls.jsonl
    ├── checkpoints.jsonl
    └── transcripts/
```

No `stages/`: a task is one step, so there is no stage tree to build. Override
the base directory with `--manifest-dir`.

`ape script` writes here too — a script's spawned steps are tasks.

## Prompt runs

`ape prompt` drives an unattended session. Its root is flat — the prompt id is
unique on its own, so there is nothing to group by:

```text
<project>/_output/ape/prompts/<prompt-id>/
├── prompt.yaml          ← the session record: status, model, cost, tokens
├── hook-events.jsonl
├── bridge-calls.jsonl
├── checkpoints.jsonl
└── transcripts/
```

`prompt.yaml` carries the full per-model token breakdown, which is why
`ape costs reprice` can recompute a prompt's cost after a price-table
correction. Read one with `ape costs prompt <id>`.

## Chat sessions

`ape chat` runs claude as a direct child of ape with stdio inherited
(claude shares ape's controlling terminal); the bridge is wired for
hook observability over a separate TCP port. Artefacts:

```
<project>/_output/ape/chats/<chat-id>/
├── hook-events.jsonl    ← same schema as pipeline runs
├── bridge-calls.jsonl   ← same schema (mostly `initialize` calls in chat)
└── checkpoints.jsonl    ← reserved; chat doesn't write anything here today
```

- `<chat-id>` is `YYYYMMDDTHHMMSSZ` (UTC ISO-8601-style).
- No `session.yaml` and no `transcript.jsonl` symlink today — the
  chat surface is a thin direct-exec with stdio inheritance (PLAN-8
  PTY migration, 2026-05-22; before that, a tmux spawn-and-attach
  under PLAN-6, 2026-05-20). claude's own transcript still lives at
  `~/.claude/projects/<encoded-cwd>/<session-id>.jsonl`.
- **A chat therefore contributes no cost.** `ape costs chat <id>` and the
  chat rows of the cost rollup both read `session.yaml`, so with nothing
  writing it they find nothing. Use `ape costs` for the project total from
  pipeline and task manifests, which are written.

## Service job logs

`ape service` runs a NATS job daemon. Unlike every other kind, this root holds
plain files rather than run directories — one log per accepted job:

```text
<project>/_output/ape/service/
└── <job-id>.log
```

The jobs themselves are pipelines or tasks, so their artefacts land under
`pipelines/` or `tasks/` as usual. This directory is just the daemon's own
stdout/stderr capture, and it is transient — nothing reads it back.

## Cross-project state

```
~/.ape/
├── registry.json        ← live ape sessions across all projects
└── (other state)
```

Run `ape sessions` to list, `ape sessions prune` to drop dead PIDs,
`ape sessions open <pfx>` to xdg-open the URL of a live session.

## Cost rollup

```
<project>/_output/ape/cost-rollup.json
```

Aggregates every pipeline run's manifest totals + every chat
session.yaml totals into per-name / per-day buckets. Read with
`ape costs`. Rebuilt on every successful `ape pipeline` / `ape chat`
exit (best-effort — failure prints a warning, does not block exit).

## .gitignore policy

**ape does not manage this.** Whether run artefacts are committed is the
project's decision, and the output folder is the framework's directory
rather than ape's, so ape neither adds an entry nor prompts for one.

The framework's own preflight already prescribes ignoring the **whole**
output folder, so on an APEX project this is normally already done for you:

```gitignore
_output/          # or whatever output_folder names
```

A directory match covers `{output_folder}/ape/` along with the framework's
own subdirectories. If you want the framework's artefacts tracked but not
ape's run history, ignore just ape's subtree instead:

```gitignore
_output/ape/
```

> The one `.gitignore` entry ape *does* manage is unrelated to this: `ape
> framework setup|update` adds the tracker lock sidecar
> (`sprint-status.yaml.lock`), because that file is a side effect of `ape
> sprint reconcile` taking an advisory lock and is never meaningful to
> commit.

## File schemas

- `hook-events.jsonl`: one JSON per line —
  `{"ts","event","step","session_id","agent_id","payload"}`.
  `step` is `null` for events whose session id has not yet been
  bound by a `step-bind` IPC frame.
- `bridge-calls.jsonl`: one JSON per line —
  `{"ts","method","tool","params","result","session_id","id"}`.
  Captures every MCP tool call seen by the bridge, including
  `tools/list`, `ping`, and `initialize`. `await_message` produces
  two paired lines (deferred-entry + flush) with the same `id`.
- `checkpoints.jsonl`: one JSON per line —
  `{"ts","kind","step","payload"}`. Kinds:
  `stage-start | stage-end | commit-made | pipeline-end | reply | stopped | chat-start | chat-end`.

## Reading further

- `docs/reference/pipeline-run-manifest.md` — PLAN-3 manifest details.
- `docs/reference/bridge-ipc.md` — IPC wire that feeds the JSONLs.
- `docs/reference/bridge-security.md` — bind / auth model.
