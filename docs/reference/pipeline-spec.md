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

| Field         | Type   | Required | Description                                                                                                                                                                                             |
| ------------- | ------ | -------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `skill`       | string | yes      | Name of the skill to invoke (e.g. `apex-create-prd`). Empty/missing is rejected.                                                                                                                        |
| `agent`       | string | no       | When set, the call goes through PAT-25 agent passthrough: `/{agent} --autonomous -- {skill} --autonomous`. When unset, the call is direct: `/{skill} --autonomous --no-commit`.                         |
| `model`       | string | no       | Model passed to claude as `--model {value}`, e.g. `"opus[1m]"`. When unset, claude uses its default.                                                                                                    |
| `args`        | string | no       | Extra literal CLI flags appended to the skill invocation, whitespace-separated. Example: `"--doc prd"`. Use this for fixed flags only.                                                                  |
| `prompt_flag` | string | no       | When set together with the runner's `--prompt` flag, ape appends `<prompt_flag> <prompt-value>` to the skill argv. Currently used by `apex-create-epics-and-stories` to receive a user-supplied prompt. |

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

- [How to install the framework](../how-to/framework-update.md) — `ape framework update` is what installs the canonical pipeline set.
- [Why project-local pipelines](../explanation/why-project-local-pipelines.md) — design rationale for moving from embedded to on-disk specs.
