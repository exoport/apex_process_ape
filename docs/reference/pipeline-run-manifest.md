# Pipeline run manifest

Every `ape pipeline <name>` invocation writes a structured on-disk record of the run. The record lives under the project root so it survives the TUI closing, supports `--no-tui` and eval-harness invocations identically, and gives downstream tooling (notably the [apex_process_framework_eval](https://github.com/diegosz/apex_process_framework_eval) consumer) a stable contract.

## Layout

```
<project_root>/_output/pipelines/<pipeline_name>/<run_id>/
  manifest.yaml             # canonical schema (this document)
  pipeline-report.md        # human-readable summary rendered from the manifest
  stages/
    01-<stage_name>/
      step-01-<skill>.ndjson  # raw claude stream-json events for the step
      step-02-<skill>.ndjson
      ...
    02-<stage_name>/...
```

A symlink at `<pipeline_name>/latest` points at the most recent `<run_id>`. On filesystems that don't support symlinks the symlink is best-effort.

`<run_id>` is `YYYYMMDD-HHMMSS-<7-char-hash>` (UTC). The hash mixes the run start time, the pipeline name, and the project root, so concurrent invocations against the same project do not collide.

## Manifest schema (v2)

Schema history:

- **v1** (ape v0.0.9) — initial PLAN-3 manifest with per-step metrics.
- **v2** (ape v0.0.10+) — PLAN-4 commit fields on `StepRecord` (`commit_sha`, `commit_message`, `commit_status`, `commit_error`) plus `totals.commits_made`.

Since v2, additional fields have been added **without bumping the schema version** — they are optional (`omitempty`) and v2 readers ignore them:

- `totals.num_turns` and per-step `num_turns` — turn counts derived from the transcript scan.
- `totals.model_usage` and per-step `model_usage` — per-model cost/token breakdown (PLAN-10 D5, ape v0.0.36+), keyed by model id.
- per-step `sessions[]` — per-claude-session usage: the step's main REPL session plus any sub-agent (Agent tool) sessions observed via `SubagentStart` / `SubagentStop`.
- `claude_version` — the resolved `claude --version` at run start (best-effort).
- per-step `telemetry_note` — a diagnosability breadcrumb explaining why numeric fields are zero.

Forward-compatible: v2 readers should accept v1 manifests (the new fields are optional `omitempty`) and treat unrecognized additive fields as opaque.

```yaml
schema_version: 2
ape_version: 0.1.0
pipeline:
  name: design
  source: /home/foo/myproject/_apex/pipelines/design.yaml
  digest: sha256:5a4f...        # sha256 of the source file at run start
project_root: /home/foo/myproject
run_id: 20260511-094530-a0d06c8
started_at: 2026-05-11T09:45:30Z
ended_at: 2026-05-11T10:38:12Z
duration_seconds: 3162.4
status: completed             # running | completed | failed | cancelled
totals:
  cost_usd: 4.83
  tokens_input: 412334
  tokens_output: 28910
  tokens_cache_read: 187420
  tokens_cache_creation: 9211
  num_turns: 214
  steps_run: 13
  steps_failed: 0
  commits_made: 13
  model_usage:                 # per-model breakdown, summed across steps (additive)
    claude-opus-4-8:
      cost_usd: 4.11
      tokens_input: 380012
      tokens_output: 24110
      tokens_cache_read: 170220
      tokens_cache_creation: 8100
      num_turns: 180
    claude-sonnet-4-6:
      cost_usd: 0.72
      tokens_input: 32322
      tokens_output: 4800
      tokens_cache_read: 17200
      tokens_cache_creation: 1111
      num_turns: 34
stages:
  - index: 1
    name: prd
    started_at: 2026-05-11T09:45:30Z
    ended_at: 2026-05-11T09:58:11Z
    duration_seconds: 760.5
    status: completed
    steps:
      - index: 1
        skill: apex-create-prd
        agent: apex-agent-pm     # omitted when the step has no agent
        args: ""
        prompt: ""
        model: ""
        started_at: 2026-05-11T09:45:30Z
        ended_at: 2026-05-11T09:58:11Z
        duration_seconds: 760.5
        status: completed
        exit_code: 0
        cost_usd: 1.42
        tokens_input: 84012
        tokens_output: 8910
        tokens_cache_read: 41208
        tokens_cache_creation: 2811
        num_turns: 47
        events_path: stages/01-prd/step-01-apex-create-prd.ndjson
        commit_sha: a0d06c8
        commit_message: "ape:design/prd/apex-create-prd"
        commit_status: committed
        commit_error: ""
        model_usage:                 # this step's per-model breakdown (additive)
          claude-opus-4-8:
            cost_usd: 1.42
            tokens_input: 84012
            tokens_output: 8910
            tokens_cache_read: 41208
            tokens_cache_creation: 2811
            num_turns: 47
        sessions:                    # per-claude-session usage (main + sub-agents)
          - session_id: 0a675bc4
            cost_usd: 1.20
            tokens_input: 72000
            tokens_output: 7600
            num_turns: 40
          - session_id: 9f31ab02     # sub-agent (Agent tool) session
            parent_session_id: 0a675bc4
            cost_usd: 0.22
            tokens_input: 12012
            tokens_output: 1310
            num_turns: 7
```

### Status values

- `running` — written at run start; should not appear in a finalized manifest. If you see it, the run was abandoned without finalization (process crash, hard kill).
- `completed` — every step exited 0 and the terminal `result` event reported `subtype: success`.
- `failed` — at least one step exited non-zero or its terminal `result` event reported a non-success subtype.
- `cancelled` — the run's context was cancelled (e.g. user pressed `q` then `y` in the TUI).

### Metric provenance

Since v0.0.36 every run drives an interactive `claude` REPL inside a PTY (see [why-pty-only.md](../explanation/why-pty-only.md)), so there is no per-step terminal `result` event to read. Per-step `cost_usd`, `tokens_*`, `num_turns`, `model_usage`, and `sessions[]` are derived by scanning the session transcript (`internal/cost/`). Transcript scanning is the single cost source, and it attributes usage per model and per claude session (including sub-agent sessions spawned via the Agent tool). If the transcript is unavailable at scan time, the numeric fields are zero, `telemetry_note` explains why, and the step still appears with the correct duration and status.

### Per-model breakdown and `ape costs`

`totals.model_usage` and per-step `model_usage` feed the project-wide cost rollup (`<project>/_output/ape/cost-rollup.json`). `ape costs` reads that rollup: its human output adds a **by model** table, and `ape costs --output-format json` includes a `per_model` map on the top-level rollup and on each pipeline / task / chat bucket. Two readers inspect a single record directly rather than the aggregate:

- `ape costs run <run-id>` — reads that run's `manifest.yaml` and prints its totals plus per-model breakdown.
- `ape costs chat <chat-id>` — reads a chat's `session.yaml`.

### Forward compatibility

Future ape releases may add fields. Consumers should treat unknown fields as opaque and reject only manifests whose `schema_version` is higher than the version they recognize.

## Commits during a run

**ape commits per step by default** (PLAN-4, v0.0.10+). Every successful step that produced a diff lands as its own git commit, with the message `ape:<pipeline>/<stage>/<skill>` unless the pipeline YAML's `commit:` field overrides it. Each commit's SHA is recorded on the corresponding `StepRecord`.

Opt out with `--no-commit`:

```bash
ape pipeline design --no-commit
```

That preserves the pre-PLAN-4 shape: zero commits during the run, dirty working tree at completion, the manifest is the durable record.

### Per-step commit fields

| Field            | When set                                                        |
| ---------------- | --------------------------------------------------------------- |
| `commit_sha`     | non-empty only when `commit_status == committed`                |
| `commit_message` | the message used (derived or YAML-explicit)                     |
| `commit_status`  | enum, see below                                                 |
| `commit_error`   | non-empty only when `commit_status == failed` (captured stderr) |

### `commit_status` values

- `committed` — git commit succeeded; `commit_sha` is set.
- `no-op` — would have committed but the working tree was clean (step produced no diff).
- `skipped-by-flag` — pipeline-level `--no-commit` was passed.
- `skipped-by-spec` — pipeline YAML had `commit: false` for this step.
- `skipped-step-failed` — the underlying step exited non-zero; no commit attempted.
- `skipped-cancelled` — the run's context was cancelled before this step's commit boundary.
- `failed` — `git commit` invocation returned non-zero; `commit_error` carries the stderr; pipeline was aborted.
- `deferred-to-stage` — step ran inside a stage-boundary stage (PLAN-6 / C2 stage-level `commit:`). The chain's accumulated diff is folded into the stage-end commit, which is attributed to the last step in the chain. Earlier steps in such a stage all carry this status.

### Inspecting a run's commits

```bash
git log --oneline --grep '^ape:design/'      # all commits from the latest `design` run (or any pipeline named `design`)
git show <sha>                                # full diff of one step
cat _output/pipelines/design/latest/pipeline-report.md
```

Tip: ape's per-step commit messages are designed for `git log --grep '^ape:<pipeline>/'` to retrieve them. If a project also commits with `ape:` prefixes outside of pipeline runs, narrow the grep to `^ape:<pipeline>/<stage>/`.

### Dirty-tree gate

When commits are enabled, ape refuses to start if `git status --porcelain` is non-empty at runner-start. Bypass with `--commit-allow-dirty` (commits proceed; first committing step's diff includes the prior WIP) or with `--no-commit` (no commits at all; gate is moot).

`_output/` should be in your `.gitignore` so the manifest tree itself never trips the gate.

## Reading a manifest from code

The manifest is plain YAML; any YAML library will parse it. The reference Go types live in [`internal/pipeline/manifest.go`](../../internal/pipeline/manifest.go) (unexported package, but the YAML is the canonical contract — re-deriving types from the schema is fine).

## Choosing a different location

`ape pipeline --manifest-dir <path>` overrides the manifest root. Pass an absolute path or one relative to the project root; ape writes `<path>/<pipeline_name>/<run_id>/` underneath. The eval harness uses this flag to redirect manifests into the eval's own results tree.

## Disabling the manifest

There is no end-user flag to disable the manifest. The library-level `RunOptions.DisableManifest` exists for tests and embedded usage but is not exposed via the CLI; the cost of always writing is small (a few KB plus the raw NDJSON sizes already paid in compute).

## Cleanup

ape never deletes old runs. Reclaim disk with:

```bash
rm -rf _output/pipelines/<pipeline_name>/<old_run_id>
# or wipe a whole pipeline's history:
rm -rf _output/pipelines/<pipeline_name>
```

Most projects gitignore `_output/`; the runs accumulate there until explicitly removed.
