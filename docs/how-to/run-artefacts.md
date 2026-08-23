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
├── verify-orchestrator/               ← framework
└── ape/                               ← ape owns this, and only this
    ├── pipelines/<pipeline>/<run-id>/
    ├── tasks/<skill>/<run-id>/
    ├── prompts/<prompt-id>/
    ├── chats/<chat-id>/
    ├── service/
    └── cost-rollup.json
```

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
<project>/_output/ape/pipelines/<pipeline-name>/<run-id>/
├── manifest.yaml        ← PLAN-3 per-step metrics, cost, commit shas
├── report.md            ← human-readable run report
├── hook-events.jsonl    ← one JSON per Claude Code hook (PLAN-5 / C4)
├── bridge-calls.jsonl   ← one JSON per MCP tool call seen by the bridge
├── checkpoints.jsonl    ← stage events + skill `reply()` + commit-made
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
