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

## Manifest schema (v1)

```yaml
schema_version: 1
ape_version: 0.0.9
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
  steps_run: 13
  steps_failed: 0
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
```

### Status values

- `running` — written at run start; should not appear in a finalized manifest. If you see it, the run was abandoned without finalization (process crash, hard kill).
- `completed` — every step exited 0 and the terminal `result` event reported `subtype: success`.
- `failed` — at least one step exited non-zero or its terminal `result` event reported a non-success subtype.
- `cancelled` — the run's context was cancelled (e.g. user pressed `q` then `y` in the TUI).

### Metric provenance

Per-step `cost_usd`, `tokens_*`, and `num_turns` are extracted from the terminal `{"type":"result", ...}` event in claude's stream-json output. If no such event is present (degraded path), the metrics are zero but the step still appears in the manifest with the correct duration and status.

### Forward compatibility

Future ape releases may add fields. Consumers should treat unknown fields as opaque and reject only manifests whose `schema_version` is higher than the version they recognize.

## Commits during a run

**ape itself does not run `git commit` at any point during a pipeline.** The manifest is the durable record of what happened; the working tree carries the actual file changes. After a pipeline completes you will typically see:

- a clean `git status` if every step's files were already present and the framework's Commit Policy held (the post-framework-v0.0.73 world), or
- a dirty working tree with all changes from the run, ready for you to inspect and commit yourself.

Framework leaf skills (`apex-create-prd`, `apex-create-architecture`, etc.) are expected to leave their output uncommitted per the framework's Commit Policy — the caller (you, ape, or a future orchestrator) owns the commit boundary. A separate ape proposal (PLAN-4) tracks adding optional per-step boundary commits if you want every step to be its own git commit; until that lands, batch-committing the run's output is the recommended flow.

To inspect what a run produced:

```bash
git status                                # files touched across the whole pipeline
git diff                                  # unstaged changes
cat _output/pipelines/<name>/latest/pipeline-report.md   # per-step summary
```

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
