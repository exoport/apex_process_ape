# Pipeline spec reference

Pipeline specs are YAML files installed at `<project>/_apex/pipelines/*.yaml`. ape reads them to drive the underlying `claude` CLI through a sequence of skill invocations. Each file's basename (minus `.yaml`) is the pipeline name passed on the command line: `_apex/pipelines/design.yaml` is `ape pipeline design`.

## Top-level fields

| Field      | Type            | Required | Description                                                                                                                                    |
| ---------- | --------------- | -------- | ---------------------------------------------------------------------------------------------------------------------------------------------- |
| `name`     | string          | yes      | Must equal the filename without the `.yaml` extension. ape verifies this on load.                                                              |
| `requires` | object          | no       | Pre-flight conditions. See [Requires](#requires).                                                                                              |
| `stages`   | ordered mapping | yes      | The pipeline body. Keys are stage names; values are stage objects. **Order matters** — stages run in declaration order. See [Stages](#stages). |

## `requires`

Pre-flight files (or directories) that must exist before the pipeline runs. ape checks each path relative to the project root and refuses to start if any are missing.

```yaml
requires:
  files:
    - development/planning/architecture
    - development/planning/prd
```

`files` despite the name accepts both files and directories — the check is a `stat`, not a "is regular file" test. Used by the `governance` pipeline to ensure the architecture has already been sharded before governance work begins.

## Stages

Each stage runs a `chain` of skill invocations against the project. ape dispatches each step to the local `claude` CLI and waits for completion before moving to the next step.

```yaml
stages:
  create-prd:
    chain:
      - skill: apex-create-prd
        agent: apex-agent-pm
        model: "opus[1m]"
  shard-prd:
    chain:
      - skill: apex-shard-doc
        args: "--doc prd"
```

### Stage object fields

| Field   | Type           | Required | Description                                                                       |
| ------- | -------------- | -------- | --------------------------------------------------------------------------------- |
| `chain` | array of steps | yes      | One or more skill invocations, run in declaration order. Empty chain is rejected. |

### Step object fields

| Field         | Type           | Required | Description                                                                                                                                                                                             |
| ------------- | -------------- | -------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `skill`       | string         | yes      | Name of the skill to invoke (e.g. `apex-create-prd`). Empty/missing is rejected.                                                                                                                        |
| `agent`       | string         | no       | When set, the call goes through PAT-25 agent passthrough: `/{agent} --autonomous -- {skill} --autonomous`. When unset, the call is direct: `/{skill} --autonomous --no-commit`.                         |
| `model`       | string         | no       | Model passed to claude as `--model {value}`, e.g. `"opus[1m]"`. When unset, claude uses its default.                                                                                                    |
| `args`        | string         | no       | Extra literal CLI flags appended to the skill invocation, whitespace-separated. Example: `"--doc prd"`. Use this for fixed flags only.                                                                  |
| `prompt_flag` | string         | no       | When set together with the runner's `--prompt` flag, ape appends `<prompt_flag> <prompt-value>` to the skill argv. Currently used by `apex-create-epics-and-stories` to receive a user-supplied prompt. |
| `commit`      | bool or string | no       | Per-step commit boundary control (PLAN-4). See [Commits](#commits) below.                                                                                                                               |

## Step completion backstop

A step normally ends when the bridge fires its Stop hook. Two backstops protect against a step that never signals Stop — a stall or a runaway — without cancelling a step that is legitimately still working (PLAN-19).

- **Idle window (`--idle-timeout`, default 60m).** A step is cancelled only after this long with **no progress across any signal**. Progress is anchored on three things, not just hooks: a bridge hook event, the active claude transcript growing (its size or mtime, plus the transcript directory's mtime so a `/clear` session rotation counts as activity), and — on the `ape prompt` path — PTY output bytes. A step that is actively writing its transcript or streaming to the PTY resets the anchor on every poll, so it is **never** cancelled for being slow, no matter how long a single tool call or reasoning span takes. Only genuine silence across every watched signal for the full window trips it. Set `--idle-timeout 0` to fall back to the default; raise it for pathologically silent tools.
- **Hard ceiling (`--max-duration`, default 3h).** An absolute wall-clock cap, independent of progress. It bounds a step that stays noisy but never actually finishes. The clock **resets on every sub-agent boundary** (`SubagentStart`/`SubagentStop`), so a sequential batch step — one sub-agent per item, e.g. `apex-story-batch-dev` — is bounded **per item**, not per batch; a step spawning no sub-agents sees a flat cap from step start. `--max-duration 0` disables the cap. See [How to tune long-running steps § The ceiling is per batch item](../how-to/tune-long-running-steps.md#the-ceiling-is-per-batch-item-not-per-batch).

The poll cadence is 30s for the first hour of a step, then relaxes to 60s for the remainder (a long-lived step's progress signals change slowly at that scale). When either backstop trips, the runner emits a structured diagnostic — which limit fired, whether the child `claude` process is still alive, and each progress source's age — instead of a bare timeout error.

`ape task` and `ape prompt` share the same backstop and flags; see [How to tune long-running steps](../how-to/tune-long-running-steps.md).

## Commits

Every successful step is committed by default with the message `ape:<pipeline>/<stage>/<skill>`. The pipeline YAML can override this per step via the `commit:` field, and the user can disable commits entirely with the `--no-commit` CLI flag.

`commit:` accepts three shapes (omit it for the default):

```yaml
stages:
  prd:
    chain:
      # omitted     → commit with `ape:design/prd/apex-create-prd`
      - skill: apex-create-prd
        agent: apex-agent-pm

      # string      → commit with this verbatim message (no `ape:` prefix)
      - skill: apex-shard-doc
        args: "--doc prd"
        commit: "docs: shard PRD"

      # false       → skip the commit for this step
      - skill: apex-validate-prd
        commit: false

      # true        → synonym for omitting the field
      - skill: apex-implementation-readiness
        commit: true
```

Rejected shapes (load-time errors): multi-line strings, empty strings, mappings, sequences, integers.

### Precedence

The CLI flag `--no-commit` is absolute — it overrides every per-step `commit:` value and turns the whole run into a no-commit run. Run with `--no-commit` to preserve the pre-PLAN-4 dirty-tree shape.

### Dirty-tree gate

When at least one step would commit, ape refuses to start if the working tree has uncommitted changes (`git status --porcelain` is non-empty). Bypass with `--commit-allow-dirty` (commits proceed; first committing step's diff includes the prior WIP) or with `--no-commit` (no commits, gate is moot).

`_output/` should be in your `.gitignore` so ape's manifest tree never trips this gate.

### Failures

If a `git commit` invocation fails (typically a pre-commit hook rejecting the staged content), the pipeline aborts. The step's run-state is recorded as completed, the commit's status is `failed`, the stderr is captured in `commit_error`, and the working tree is left in whatever state git left it. Investigate, clean up, then rerun.

See [Pipeline run manifest](pipeline-run-manifest.md) for the full record of what each commit produced.

## Worked example: a custom 2-stage pipeline

```yaml
# _apex/pipelines/quick-start.yaml
name: quick-start
requires:
  files:
    - _apex/config.yaml
stages:
  create-prd:
    chain:
      - skill: apex-create-prd
        agent: apex-agent-pm
        model: "opus[1m]"
  shard-prd:
    chain:
      - skill: apex-shard-doc
        args: "--doc prd"
```

Run with `ape pipeline quick-start`. Drop the file into `_apex/pipelines/` and it shows up in tab completion automatically.

## Stability and compatibility

The pipeline-spec schema is **stable across patch versions** of ape and across framework versions that ship the canonical pipeline set. Adding a new optional field is allowed; renaming or removing a field requires a deprecation cycle.

Projects that customize pipeline files take responsibility for keeping them compatible with their installed ape version. `ape framework update` overwrites `_apex/pipelines/*.yaml` from the framework repo, so customizations should live in **new** files (e.g. `_apex/pipelines/my-team-flow.yaml`) rather than edits to the canonical three.

## Related

- [How to pass arguments to skills](../how-to/pass-args-to-skills.md) — task-oriented guide to `args:` vs. `prompt_flag:` + `--prompt`.
- [How to install the framework](../how-to/framework-update.md) — `ape framework update` is what installs the canonical pipeline set.
- [Why project-local pipelines](../explanation/why-project-local-pipelines.md) — design rationale for moving from embedded to on-disk specs.
- [Pipeline run manifest](pipeline-run-manifest.md) — every pipeline run writes a structured manifest to `_output/pipelines/<name>/<run_id>/` capturing per-step cost / tokens / duration.
