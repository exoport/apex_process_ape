# Pipeline run manifest

Every `ape pipeline <name>` invocation writes a structured on-disk record of the run. The record lives under the project root so it survives the TUI closing, supports `--no-tui` and eval-harness invocations identically, and gives downstream tooling (notably the [apex_process_framework_eval](https://github.com/diegosz/apex_process_framework_eval) consumer) a stable contract.

## Layout

```
<project_root>/_output/ape/pipelines/<pipeline_name>/<run_id>/
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
- `tokens_cache_creation_5m` / `tokens_cache_creation_1h` on `totals`, each step, and every `model_usage` entry — the ephemeral cache-write split (PLAN-10 D1, ape v0.0.37+). `tokens_cache_creation` is unchanged and stays the **sum** of the two tiers, which price differently (5m ≈ 1.25× base input, 1h ≈ 2.00×); the split fields simply expose that breakdown. Consumers that only track total cache creation keep reading `tokens_cache_creation` and ignore the split.
- per-step `sessions[]` — per-claude-session usage: the step's main REPL session plus any sub-agent (Agent tool) sessions observed via `SubagentStart` / `SubagentStop`.
- `claude_version` — the resolved `claude --version` at run start (best-effort).
- per-step `telemetry_note` — a diagnosability breadcrumb explaining why a numeric field is zero or approximate. Two causes: the transcript was unavailable / had no complete assistant turn (everything zero), or a model had no price in the table (tokens and turns correct, `cost_usd` a lower bound). More than one cause is joined with `; `.
- per-step `model_declared` (ape v0.0.68+) — present **only** when the spec's resolved `model:` for that step is not the one the step ran on. A stage is one `claude` process launched with its first step's model and ape sends no `/model`, so a later step's `model:` cannot take effect. `model` now records what ran; `model_declared` records what was asked for. Absent on every step where the two agree, so its presence is itself the signal. Older manifests carry the *declared* value in `model` with no way to tell whether it was applied — see [pipeline-yaml-schema.md](pipeline-yaml-schema.md#model-resolves-per-step-but-applies-per-stage).
- per-step `context_window` and per-`model_usage`-entry `context_window` (ape v0.0.60+) — the model's usable context in tokens, from ape's maintained table. **Omitted when unknown**, never defaulted. See [reading the `context_window` fields](#reading-the-context_window-fields) — there are two of them and they can legitimately disagree.
- per-step `contract` (ape v0.0.60+) — the terminal-contract verdict: `present`, `missing`, or `no-transcript`. **Omitted** when the step's skill declares no contract in the framework's `_apex/terminal-contracts.csv`, so the field is present exactly when the skill was enrolled. See [reading the `contract` field](#reading-the-contract-field) before using it as a quality metric — it measures TEXT, not work.

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
timestamp: "20260511094530"   # the framework's `timestamp` for this run (additive)
ended_at: 2026-05-11T10:38:12Z
duration_seconds: 3162.4
status: completed             # running | completed | failed | cancelled
totals:
  cost_usd: 4.83
  tokens_input: 412334
  tokens_output: 28910
  tokens_cache_read: 187420
  tokens_cache_creation: 9211       # sum of the two tiers below
  tokens_cache_creation_5m: 3111    # additive split (PLAN-10 D1); 5m + 1h == tokens_cache_creation
  tokens_cache_creation_1h: 6100
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
      tokens_cache_creation_5m: 2500
      tokens_cache_creation_1h: 5600
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
        model: ""                # what the step RAN on: its stage's launch model
        model_declared: ""       # only when the spec asked for a different one (see below)
        effort: ""               # step-level `effort:`; omitted when unset (resolved default is xhigh)
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
        tokens_cache_creation_5m: 811
        tokens_cache_creation_1h: 2000
        num_turns: 47
        events_path: stages/01-prd/step-01-apex-create-prd.ndjson
        commit_sha: a0d06c8
        commit_message: "ape:design/prd/apex-create-prd"
        commit_status: committed
        commit_error: ""
        contract: present            # omitted when the skill is not enrolled
        context_window: 1000000      # the window of the model THIS STEP was spawned with
        model_usage:                 # this step's per-model breakdown (additive)
          claude-opus-4-8:
            cost_usd: 1.42
            tokens_input: 84012
            tokens_output: 8910
            tokens_cache_read: 41208
            tokens_cache_creation: 2811
            tokens_cache_creation_5m: 811
            tokens_cache_creation_1h: 2000
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

Cost and tokens can also disagree in the other direction. Pricing uses a table compiled into `ape` (`internal/cost/prices.yaml`), and Claude Code can introduce a model id that table does not carry. When that happens the token counts stay exact while `cost_usd` is either **estimated** from the model's family tier or **zero** — and the step's `telemetry_note` says which, naming the model and the turn count. A zero `cost_usd` beside non-zero `tokens_*` therefore means *unpriced*, not free. `ape costs reprice` recomputes those figures from the stored per-model tokens once the table is corrected; see [how to keep cost pricing current](../how-to/keep-cost-pricing-current.md).

### Per-model breakdown and `ape costs`

`totals.model_usage` and per-step `model_usage` feed the project-wide cost rollup (`<project>/_output/ape/cost-rollup.json`). `ape costs` reads that rollup: its human output adds a **by model** table, and `ape costs --output-format json` includes a `per_model` map on the top-level rollup and on each pipeline / task / chat bucket. Two readers inspect a single record directly rather than the aggregate:

- `ape costs run <run-id>` — reads that run's `manifest.yaml` and prints its totals plus per-model breakdown.
- `ape costs chat <chat-id>` — reads a chat's `session.yaml`.

### Reading the `context_window` fields

`peak_tokens / context_window` is only as good as the divisor, so both the number and its absence are contracts.

**This is a maintained table, not a measurement of a run.** Claude Code reports the real per-model window on the stream-json result event's `modelUsage[].contextWindow`. ape is [PTY-only by design](../explanation/why-pty-only.md) and does not use that surface, so the reported window cannot reach ape on any path ape drives — not as a deferred feature, but as a consequence of the PTY invariant. Do not label these fields as reported. What they give you is a single, correctable source for the divisor instead of a second hand-maintained copy per consumer.

The values come from the Models API — `GET /v1/models/{id}` returns `max_input_tokens`, which *is* the context window (the object has no `context_window` field) — but they are curated by hand from it, the same standing as ape's rates. Everything from Opus 4.6 and Sonnet 4.6 onward is 1M at standard pricing; 200000 is the pre-4.6 window. Treat a window as correct-as-of-the-release, not as live truth: if a model shipped after your `ape` build, its window may be absent (reported as absent, never defaulted) or stale.

**There are two fields, and both honour a `[1m]` suffix:**

| field                                  | resolved from                                        |
| -------------------------------------- | ---------------------------------------------------- |
| `steps[].context_window`               | the step's effective `--model` string                |
| `steps[].model_usage[].context_window` | the raw model spelling in the transcript, per bucket  |

A suffixed and unsuffixed model are the same model at the same price, so ape folds them into one `model_usage` bucket keyed by the base id — correct for cost, and it would be wrong for context. Since v0.0.60 the bucket's window is read from the raw spelling *before* that fold, so a `claude-sonnet-4-5[1m]` step reports 1000000 in both fields rather than 200000 in one of them.

**Which to divide by** is now a question about scope, not accuracy. `steps[].context_window` is the step's own window and the right denominator for a step-level ratio — a step whose sub-agents ran other models has several per-model entries and no one of them speaks for the step. Use a per-model entry when you want that model's share.

One case records **no** per-model window: a single bucket holding turns of two different sizes, which happens when a step's sub-agents run a different context variant than the step was spawned with. No single number is right for it, and the nearest wrong one is off by 5×, so it is omitted like any other unknown.

The gap has narrowed on current models without going away. `opus[1m]` was the motivating case back when the Opus base window was 200K; from Opus 4.6 / Sonnet 4.6 onward 1M is the base and the two agree. The models that kept a 200K default with a 1M opt-in are where it still matters.

**Absent means unknown.** A model ape has no window for gets no field at all — not a zero. Render "could not look", never a plausible ratio. Defaulting to 200k is the specific failure these fields exist to retire: a stale 200k entry that outlived a 1M window inflated every affected step's occupancy fivefold, and the metric built on it had to be retracted. For the same reason there is **no family fallback**: a family price that is off lands in the right order of magnitude and is flagged as an estimate, while a family window that is off produces a precise-looking ratio wrong by the ratio of the two windows — and the guess would fail toward the smaller one.

`ape doctor`'s `cost.price_table_coverage` row names any model it saw in recent transcripts that has no known window. A window can be corrected without a new ape binary, the same way a price can:

```bash
ape costs update --from corrected.yaml   # rows accept `context_window:` beside base_input / output
```

### Reading the `contract` field

Some APEX skills end a run with a machine-readable return block — a story-batch skill's `run_status:`, an epic-batch review's `epics:`. The framework declares which skills promise one, and the pattern that recognises it, in `_apex/terminal-contracts.csv`. After each step ape matches the closing assistant message against that skill's pattern and records the verdict.

| value           | meaning                                                            |
| --------------- | ------------------------------------------------------------------ |
| `present`       | the closing message matched the skill's pattern                     |
| `missing`       | a closing message was read and it did not match                     |
| `no-transcript` | the skill is enrolled but no closing message could be read at all   |
| *(omitted)*     | the skill declares no terminal contract — there was nothing to check |

`no-transcript` is deliberately distinct from `missing`: "looked and it was not there" and "could not look" are different facts, and only the first is evidence about the run.

**This is telemetry about text, not about work.** The check reads the closing message of the step's **main** claude session, and that message is a *relay* whenever anything below it did the work — which, for the batch skills this table enrols, is usually:

- under `--agent`, the runner types `/<agent> --autonomous -- <skill> …`, so the agent skill closes the session and the sub-skill's block appears only if the agent relayed it verbatim;
- with no agent at all, a batch skill that fans out to Agent-tool sub-agents has the same shape — the subs' own transcripts are separate files (see `sessions[]`) and the main session's closing message summarises them.

So `missing` means *the closing text did not match*, **not** *the skill failed to emit its contract*. Conflating the two would blame a framework-side quality problem for what may be a relay artifact. Any decision to make this fail a run has to separate them first; it is warn-only today for exactly that reason.

Two limits on the denominator:

- **Only completed steps have a record.** A step whose wait failed — idle timeout, detached agent, dead session — gets no `StepRecord` at all, so it carries no verdict either way. A rate computed from this field is a rate over completed steps.
- **`no_clear: true` steps share the previous step's transcript.** If such a step added no assistant turn of its own, the message read is its predecessor's, which can record a `present` that belongs to the previous step.

The same verdict is also written to the run's `checkpoints.jsonl` as a `contract` row carrying the skill, the status and the diagnostic text.

### `timestamp` is not a second `started_at`

`started_at` is an RFC-3339 UTC instant for telemetry, read straight from
the wall clock. `timestamp` is the framework's own variable — local
wall-clock `YYYYMMDDHHMMSS` — and it is **issued monotonically**: every
issue returns the later of the wall clock and the project's persisted
floor, so a machine whose clock is behind cannot stamp a record backwards.
The two can therefore disagree, and when they do the disagreement is the
point. Empty when the run had no project to issue against.

See [work with project data](../how-to/work-with-project-data.md) for the
floor's persistence and its fresh-clone seed.

### Forward compatibility

Future ape releases may add fields. Consumers should treat unknown fields as opaque and reject only manifests whose `schema_version` is higher than the version they recognize. `timestamp` was added this way — additive under `schema_version: 2`, absent from older manifests.

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
cat _output/ape/pipelines/design/latest/pipeline-report.md
```

Tip: ape's per-step commit messages are designed for `git log --grep '^ape:<pipeline>/'` to retrieve them. If a project also commits with `ape:` prefixes outside of pipeline runs, narrow the grep to `^ape:<pipeline>/<stage>/`.

### Dirty-tree gate

When commits are enabled, ape refuses to start if `git status --porcelain` is non-empty at runner-start. Bypass with `--commit-allow-dirty` (commits proceed; first committing step's diff includes the prior WIP) or with `--no-commit` (no commits at all; gate is moot).

The resolved `{output_folder}/ape/` should be in your `.gitignore` so the manifest tree itself never trips the gate. `ape doctor --only output.ape_ignored` reports whether it is — including the worse case where the tree is already **committed**, which an ignore line alone does not fix (git keeps tracking what is in the index; it needs `git rm -r --cached` too). ape reports and never edits `.gitignore` itself.

## Reading a manifest from code

The manifest is plain YAML; any YAML library will parse it. The reference Go types live in [`internal/pipeline/manifest.go`](../../internal/pipeline/manifest.go) (unexported package, but the YAML is the canonical contract — re-deriving types from the schema is fine).

## Choosing a different location

`ape pipeline --manifest-dir <path>` overrides the manifest root. Pass an absolute path or one relative to the project root; ape writes `<path>/<pipeline_name>/<run_id>/` underneath. The eval harness uses this flag to redirect manifests into the eval's own results tree.

## Disabling the manifest

There is no end-user flag to disable the manifest. The library-level `RunOptions.DisableManifest` exists for tests and embedded usage but is not exposed via the CLI; the cost of always writing is small (a few KB plus the raw NDJSON sizes already paid in compute).

## Cleanup

ape never deletes old runs. Reclaim disk with:

```bash
rm -rf _output/ape/pipelines/<pipeline_name>/<old_run_id>
# or wipe a whole pipeline's history:
rm -rf _output/ape/pipelines/<pipeline_name>
```

Most projects gitignore `_output/`; the runs accumulate there until explicitly removed.
