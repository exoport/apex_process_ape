# How-to — pass arguments to skills

A pipeline step invokes a skill with a fixed argv shape. To pass extra arguments — either baked into the pipeline or supplied at the command line — use one of two fields on the step.

## Rule of thumb

| Value source                                        | Use this           |
| --------------------------------------------------- | ------------------ |
| Same value every run, set by the pipeline author    | `args:`            |
| Different value each run, set by the user invoking  | `prompt_flag:` + `--prompt` |

You can combine both on the same step. `args:` lands first; `<prompt_flag> <prompt>` is appended after.

## Fixed args — `args:`

Add a whitespace-separated string of literal flags to the step. The string is appended verbatim to the skill's argv.

```yaml
stages:
  shard-prd:
    chain:
      - skill: apex-shard-doc
        args: "--doc prd"
```

Resulting prompt: `/apex-shard-doc --autonomous --no-commit --doc prd`.

Use this for flags the pipeline author always wants — e.g. `--doc prd`, `--from-status draft`. No shell quoting is involved; the string is split on whitespace and each piece becomes one argv element.

## Runtime args — `prompt_flag:` + `--prompt`

When the *user* should supply a value at invocation time, declare which flag name carries it on the step:

```yaml
stages:
  create-epics:
    chain:
      - skill: apex-create-epics-and-stories
        agent: apex-agent-pm
        prompt_flag: "--prompt"
```

Then run:

```bash
ape pipeline epics --prompt "minimal greeter — add settings page"
```

Resulting prompt: `/apex-agent-pm --autonomous -- apex-create-epics-and-stories --autonomous --prompt minimal greeter — add settings page`.

The value passes through Go's argv directly — embedded quotes, em-dashes, and shell metacharacters survive without escaping.

If the step has no `prompt_flag`, the CLI `--prompt` value is silently ignored for that step. In the canonical pipeline set, only `apex-create-epics-and-stories` opts in.

## What you can't do

- **Per-stage args** — `args:` is step-level only. Repeat it on each step in the chain if needed.
- **Multiple runtime prompts in one run** — `--prompt` is a single value applied to every step that declares `prompt_flag`. Pipelines that need to thread different user inputs into different stages must split into separate `ape pipeline` invocations.
- **Shell-piped input** — ape does not read stdin to populate `--prompt`. Pass the value as a CLI argument.

## Related

- [Pipeline spec reference](../reference/pipeline-spec.md) — full field table for steps.
- [Pipeline YAML schema](../reference/pipeline-yaml-schema.md) — annotated schema example.
