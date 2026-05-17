# How to read `_output/`

Every `ape pipeline` or `ape chat` invocation drops artefacts under
the project root. This document describes the layout and what each
file is for.

PLAN-5 / C6.

## Pipeline runs

```
<project>/_output/pipelines/<pipeline-name>/<run-id>/
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

```
<project>/_output/ape/chats/<chat-id>/
├── session.yaml         ← small chat-session record (no PLAN-3 manifest)
├── hook-events.jsonl    ← same schema as pipeline runs
├── bridge-calls.jsonl   ← same schema
├── checkpoints.jsonl    ← kinds: chat-start, reply, chat-end
└── transcript.jsonl     ← symlink into ~/.claude/projects/
```

- `<chat-id>` is `YYYYMMDD-HHMMSS-<7-char hash>` where the hash
  mixes timestamp + cwd + pid for cross-process uniqueness.

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

On first run, ape checks whether `_output/` is in the project's
`.gitignore`. If absent:

- **TTY:** ape prompts (`Append _output/ to .gitignore? [y/N]`).
- **Non-TTY:** ape warns on stderr but does not modify the file.

Add `_output/` manually if you prefer not to be asked. The line is
a directory match, so files under `_output/` of any subproject also
gets ignored.

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
