# How to install the APEX framework into a project

`ape framework update` copies the framework's skills and pipeline specs from a checked-out `apex_process_framework` repo into your project, and (on first run) seeds `_apex/config.yaml` interactively. After this command runs, `ape pipeline <name>` works against the project.

## Prerequisites

- ape `v0.0.6` or later — `ape version` to confirm.
- A local clone of `apex_process_framework`. Either pass `--repo PATH` on every invocation, or set `$APEX_FRAMEWORK_REPO` once:

  ```bash
  export APEX_FRAMEWORK_REPO=/path/to/apex_process_framework
  ```

- The framework repo must be on `main` and have a clean working tree (no uncommitted changes). To bypass either check, pass `--force`.

## Quickstart

```bash
cd /path/to/your/project
ape framework update
```

What happens:

1. Validates `$APEX_FRAMEWORK_REPO` (subtree layout, git state, branch, working tree).
2. Fetches `origin/main` and fast-forwards (skip with `--no-fetch`).
3. Records the framework's HEAD SHA + tag for the metadata file.
4. If `<project>/_apex/config.yaml` is absent, opens an interactive Bubble Tea prompt for the project name and which extensions to enable.
5. Removes any existing `<project>/.claude/skills/apex-*` (so removed-from-framework skills disappear locally).
6. Copies all `apex-*` skill directories into `<project>/.claude/skills/`.
7. Copies all framework pipeline YAMLs into `<project>/_apex/pipelines/`.
8. Writes `<project>/_apex/framework.yaml` recording what was installed.

## What gets installed

| Path                              | Source                                      | Behavior on re-run        |
| --------------------------------- | ------------------------------------------- | ------------------------- |
| `.claude/skills/apex-*/`          | `framework/_claude/skills/apex-*/`          | Wiped + reinstalled       |
| `_apex/pipelines/*.yaml`          | `framework/_apex/pipelines/*.yaml`          | Overwritten               |
| `_apex/config.yaml`               | `framework/_apex/config.yaml` (template)    | **Seeded only if absent** |
| `_apex/config.local.example.yaml` | `framework/_apex/config.local.example.yaml` | **Seeded only if absent** |
| `_apex/framework.yaml`            | _(generated)_                               | Always rewritten          |

Non-`apex-*` entries under `.claude/skills/` are never touched — they belong to your project, not the framework.

## Headless / scripted contexts

When stdout isn't a TTY (CI, scripts, `--output-format json`) and `_apex/config.yaml` doesn't exist yet, `ape framework update` refuses to seed the config silently. You must either:

- **Provide the values explicitly** so no prompt is needed:

  ```bash
  ape framework update \
      --project-name myproject \
      --extensions ext-adrs,ext-features \
      --output-format json
  ```

  `--extensions ""` means "no extensions". Recognized IDs: `ext-adrs`, `ext-patterns`, `ext-capabilities`, `ext-features`.

- **Skip the seed entirely** — install only skills + pipelines:

  ```bash
  ape framework update --no-bootstrap
  ```

  The result payload reports `sources.config.seeded: false`. You can run `ape framework update` interactively later to bootstrap config.yaml without re-copying skills (the skills get wiped + reinstalled; the config seed only runs if config.yaml is absent).

## Idempotency

Running the command twice on a steady-state project is safe and cheap:

- Skills: wiped + reinstalled. Net effect identical when the framework HEAD is unchanged.
- Pipelines: overwritten. Net effect identical.
- `config.yaml`: not touched (already exists).
- `framework.yaml`: rewritten with a fresh `installed_at` timestamp.

There's no "dry run" flag. The destructive operation that matters — wiping `apex-*` skills — is git-safe: if the project is a git repo and you have uncommitted edits to a tracked `apex-*` skill file, the command refuses without `--force`. Untracked `apex-*` paths are treated as leftovers from a prior install and get clobbered.

## Inspecting what was installed

```bash
ape framework status
```

Prints the framework version installed and (if `--repo` or `$APEX_FRAMEWORK_REPO` is set) compares against the framework HEAD. See [framework status reference](../reference/framework-yaml.md) for the structured payload shape.

## Output formats

All flags work with `--output-format human|json|yaml`. The structured forms emit the full metadata block plus an install summary suitable for parsing by tooling.

```bash
ape framework update --output-format json | jq '.summary'
# { "skillsInstalled": 86, "skillsRemoved": 0, "pipelinesInstalled": 3, ... }
```

## Troubleshooting

### "framework repo path not set"

Pass `--repo /path/to/apex_process_framework` or export `APEX_FRAMEWORK_REPO`.

### "framework repo has uncommitted changes (pass --force to bypass)"

The framework repo must be clean. Either commit/stash the framework-side changes, or pass `--force` to clobber-install from a dirty tree (recorded in `framework.yaml` so the divergence is auditable).

### "framework repo is on branch X (expected main)"

The command refuses to install from a non-`main` branch unless `--force` is passed. This is to prevent accidentally pinning a project to an experimental branch's HEAD.

### "uncommitted changes under .claude/skills/apex-\*"

You have local edits to one or more committed framework skills. Either:

- Commit the edits in your project (they'll get clobbered on the next install but at least you have the diff in git).
- Pass `--force` to clobber them now.
- Stash them somewhere outside `.claude/skills/apex-*`.

Untracked `apex-*` paths don't trip this check — only tracked-and-modified ones do.

### "framework branch \"main\" diverged from origin"

`git merge --ff-only` failed because the framework repo's local main has commits not on the remote. Either rebase manually or pass `--no-fetch` to skip the pull.

## Related

- [Pipeline spec reference](../reference/pipeline-spec.md) — the YAML shape of `_apex/pipelines/*.yaml`.
- [framework.yaml reference](../reference/framework-yaml.md) — the metadata file written on every run.
- [Why project-local pipelines](../explanation/why-project-local-pipelines.md) — design rationale.
