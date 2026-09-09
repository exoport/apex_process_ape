# Reference — pipeline YAML schema

Pipelines are YAML files at `<project>/_apex/pipelines/<name>.yaml`. PLAN-6 / C2 adds pipeline-level and stage-level defaults for `model`, `agent`, and `commit`; PLAN-4 / C1's step-level `commit` keeps working unchanged.

This page documents every field, the [precedence rules](#precedence), and migration notes from the PLAN-4 schema.

## Top-level (pipeline) fields

```yaml
name: design # required; must equal the filename without .yaml
model: opus # pipeline-level default (PLAN-6 / C2); bare family → current generation
agent: apex-agent-pm # pipeline-level default (PLAN-6 / C2)
commit: true # pipeline-level commit policy (PLAN-6 / C2)
requires: # optional pre-flight check
  files: ["product-brief-*.md"]
stages: { ... } # required
```

### `model`, `agent`

Strings. Empty = no pipeline-level default. Inherited by stages that don't override.

### `commit`

Accepts the same shapes as PLAN-4's step-level `commit`:

- `commit: true` — commit at stage boundary with the derived message (`ape:<pipeline>/<stage>`).
- `commit: false` — no commit at this scope.
- `commit: "feat: ..."` — commit at stage boundary with the literal message. `{stage}` interpolation is reserved for future use.
- omitted — inherit from the next-higher scope (none, in this case → no commit).

## Stage fields

```yaml
stages:
  create-prd:
    model: sonnet # stage-level override (PLAN-6 / C2)
    agent: apex-agent-pm # stage-level override (PLAN-6 / C2)
    commit: "feat: PRD" # stage-level override (PLAN-6 / C2)
    chain:
      - skill: apex-create-prd
        # ...
```

Stage-level `model` / `agent` / `commit` override the pipeline-level defaults for steps in this stage's chain.

## Step fields

```yaml
chain:
  - skill: apex-create-prd # required
    agent: apex-agent-pm # optional; overrides stage+pipeline
    model: claude-sonnet-5 # optional; overrides stage+pipeline (explicit id pins the generation)
    args: "--doc prd" # fixed CLI flags appended to skill prompt
    prompt_flag: "--prompt" # forwards user-supplied --prompt
    commit: true # step-level commit (PLAN-4 / C1)
    no-clear: true # opt out of /clear before this step (PLAN-6 / C4)
```

### `no-clear`

Boolean. Defaults to `false`. When `true`, the step is exempt from the default-on `/clear` that the interactive runner produces before every step's skill prompt. Use this on the second-and-later steps of a multi-step chain where context sharing within a stage is the point.

Step-level only. Pipeline- and stage-level `no-clear` are not supported (opting out wholesale would defeat the step contract).

## Precedence

For each step the runner resolves the effective `model`, `agent`, and `commit` using:

```
step.<field>  →  stage.<field>  →  pipeline.<field>  →  default
```

- `model` / `agent`: default is empty string (claude picks its own default model; skill runs without an agent prefix).
- `commit`: default is **skip** (no commit fires).

### `model` resolves per step but applies per stage

A stage is **one** `claude` process. It is launched with the model its
**first** step resolves to, the rest of the chain is typed into that same
session, and ape sends no `/model` — so a later step declaring a
different model runs on the first step's regardless.

That limit used to be invisible: the declared value was written to the
manifest, the TUI and the `step-start` event as though it had been
applied. It is now reported instead.

- Before the run, `ape pipeline` prints a warning naming the stage, the
  model it launches on, and each step that declares another.
- `ape doctor --only pipelines.project` reports the same thing on demand.
- In the manifest, `model` is what the step **ran on**; `model_declared`
  appears beside it only when the spec asked for something else.

To actually get two models, split the stage at its model boundary — the
stage is the unit of process, so it is also the unit of model.

### `output-style`

Pipeline- and stage-level only, for the same reason: `--settings` is
fixed at launch. See
[claude-spawn-modes.md](claude-spawn-modes.md#the-project-can-declare-a-style-per-skill-and-per-stage)
for the full precedence, including the per-skill table `ape task` reads.

### Unknown keys

A key ape does not read is **ignored**, not rejected — a newer framework
may ship one an older ape has not learned. Since a silently-dropped key
is indistinguishable from a typo, `ape pipeline` and
`ape doctor --only pipelines.project` both name any key outside the
schema above, with its line number.

### Commit boundary

The boundary depends on which level set the value:

| Level that set `commit` | Boundary the commit fires on |
| ----------------------- | ---------------------------- |
| step                    | step boundary                |
| stage                   | stage boundary               |
| pipeline                | stage boundary               |
| none                    | (no commit)                  |

- Stage-level / pipeline-level `commit` produces **one commit per stage**, capturing the stage chain's accumulated diff.
- Step-level `commit` is the escape hatch for mid-chain commits. A step's explicit `commit: false` disables both step- and stage-boundary commits for that step.

### `commit: false` precedence

`commit: false` at any level is the authoritative "skip" for that scope:

- step `false` → step opts out (no commit anywhere for this step's stage if the step's `false` is the highest-precedence opinion).
- stage `false` → stage boundary skipped regardless of pipeline opt-in.
- pipeline `false` → no commits anywhere unless a stage or step opts in.

## Migration from PLAN-4

| PLAN-4 shape (per-step commit) | PLAN-6 equivalent (pipeline-level)                                              |
| ------------------------------ | ------------------------------------------------------------------------------- |
| Every step has `commit: true`  | Pipeline-level `commit: true` (one commit per stage)                            |
| Step-specific commit messages  | Stage-level `commit: "msg"` or per-step                                         |
| Some steps `commit: false`     | Leave step-level `false` in place; pipeline-level `true` is overridden per-step |

YAML that doesn't mention `commit:` at any level keeps parsing and now produces **no commits** (default for PLAN-6 is skip). PLAN-4 implicitly committed every step via the `CommitModeDefault` zero value of an omitted field; the new default is documented in CHANGELOG.

## Related

- [invocation-matrix.md](invocation-matrix.md)
- [step-contract.md](step-contract.md)
- [pipeline-spec.md](pipeline-spec.md) — the authoring-facing view of the same schema
- [Your first pipeline](../tutorials/first-pipeline.md) — running a pipeline end to end
