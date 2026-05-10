# How to install the APEX framework into a project (first time)

`ape framework setup` is the one-time install command. It copies the framework's skills and pipeline specs from a checked-out `apex_process_framework` repo into your project, seeds `_apex/config.yaml` interactively, and records what was installed in `_apex/framework.yaml`. After this command runs, `ape pipeline <name>` works against the project.

For subsequent framework version bumps, use [`ape framework update`](framework-update.md) — it refreshes skills + pipelines without re-bootstrapping config.

## Prerequisites

- ape `v0.0.7` or later — `ape version` to confirm.
- A local clone of `apex_process_framework`. Either pass `--repo PATH` on every invocation, or set `$APEX_FRAMEWORK_REPO` once:

  ```bash
  export APEX_FRAMEWORK_REPO=/path/to/apex_process_framework
  ```

- The framework repo must be on `main` and have a clean working tree (no uncommitted changes). To bypass either check, pass `--force`.
- The project's `_apex/framework.yaml` must **not** already exist — if it does, `setup` refuses to run. Use `update` instead, or pass `--force` to re-bootstrap (which resets `project_name` and `extensions`).

## Quickstart

```bash
cd /path/to/your/project
ape framework setup
```

What happens:

1. Validates that `_apex/framework.yaml` is **absent** (the not-yet-installed signal). If present, the command exits with `Error: framework already installed at <path> — run "ape framework update" to refresh, or "ape framework setup --force" to re-bootstrap …`.
2. Validates `$APEX_FRAMEWORK_REPO` (subtree layout, git state, branch, working tree).
3. Fetches `origin/main` and fast-forwards (skip with `--no-fetch`).
4. Records the framework's HEAD SHA + tag for the metadata file.
5. Opens an interactive Bubble Tea prompt for the project name and which extensions to enable. (Skip the TUI with `--project-name` + `--extensions`, or skip seeding entirely with `--no-bootstrap`.)
6. Removes any pre-existing `<project>/.claude/skills/apex-*` (so leftover skills from a prior install disappear).
7. Copies all `apex-*` skill directories into `<project>/.claude/skills/`.
8. Copies all framework pipeline YAMLs into `<project>/_apex/pipelines/`.
9. Writes `<project>/_apex/framework.yaml` recording what was installed.

## What gets installed

| Path                              | Source                                      | Behavior             |
| --------------------------------- | ------------------------------------------- | -------------------- |
| `.claude/skills/apex-*/`          | `framework/_claude/skills/apex-*/`          | Created              |
| `_apex/pipelines/*.yaml`          | `framework/_apex/pipelines/*.yaml`          | Created              |
| `_apex/config.yaml`               | `framework/_apex/config.yaml` (template)    | **Seeded if absent** |
| `_apex/config.local.example.yaml` | `framework/_apex/config.local.example.yaml` | **Seeded if absent** |
| `_apex/framework.yaml`            | _(generated)_                               | Created              |

Non-`apex-*` entries under `.claude/skills/` are never touched — they belong to your project, not the framework.

## Headless / scripted contexts

When stdout isn't a TTY (CI, scripts, `--output-format json`), `setup` refuses to launch the interactive TUI. Either:

- **Provide the values explicitly** so no prompt is needed:

  ```bash
  ape framework setup \
      --project-name myproject \
      --extensions ext-adrs,ext-features \
      --output-format json
  ```

  `--extensions ""` means "no extensions". Recognized IDs: `ext-adrs`, `ext-patterns`, `ext-capabilities`, `ext-features`.

- **Skip the seed entirely** — install only skills + pipelines:

  ```bash
  ape framework setup --no-bootstrap
  ```

  The result payload reports `sources.config.seeded: false`. You can re-run `setup --force` interactively later to bootstrap config.yaml.

## Re-bootstrapping with `--force`

`ape framework setup --force` re-runs the full install even when `framework.yaml` already exists. Effect:

- `apex-*` skills are wiped + reinstalled (same as `update`).
- Pipelines are overwritten (same as `update`).
- `_apex/config.yaml` is **re-seeded** — `project_name` and `extensions` are reset to whatever you provide (interactively or via flags).

This is destructive to the prior bootstrap values. The legitimate use case is "I made a typo in `project_name` during initial setup and want to start over."

## Inspecting what was installed

```bash
ape framework status
```

Prints the framework version installed and (if `--repo` or `$APEX_FRAMEWORK_REPO` is set) compares against the framework HEAD. See [framework.yaml reference](../reference/framework-yaml.md) for the structured payload shape.

## Output formats

All flags work with `--output-format human|json|yaml`. The structured forms emit the full metadata block plus an install summary suitable for parsing by tooling.

```bash
ape framework setup --project-name X --extensions ext-adrs --output-format json | jq '.summary'
# { "skillsInstalled": 86, "skillsRemoved": 0, "pipelinesInstalled": 3, ... }
```

## Troubleshooting

### `framework already installed at <path>`

You already ran `setup` on this project. Either:

- Run `ape framework update` to refresh skills + pipelines while keeping the existing `project_name` + `extensions`.
- Run `ape framework setup --force` to re-bootstrap from scratch (resets `project_name` and `extensions`).

### `framework repo path not set`

Pass `--repo /path/to/apex_process_framework` or export `APEX_FRAMEWORK_REPO`.

### `framework repo has uncommitted changes`

The framework repo must be clean. Either commit/stash the framework-side changes, or pass `--force` to install from a dirty tree (recorded in `framework.yaml` so the divergence is auditable).

### `framework repo is on branch X (expected main)`

The command refuses to install from a non-`main` branch unless `--force` is passed. This is to prevent accidentally pinning a project to an experimental branch's HEAD.

### `config bootstrap required but no TTY`

You ran `setup` non-interactively (no TTY, or `--output-format json|yaml`) without supplying `--project-name` and `--extensions`, and without `--no-bootstrap`. Pick one of the headless paths above.

## Related

- [How to update the framework](framework-update.md) — refresh-only command, used after the initial setup.
- [Pipeline spec reference](../reference/pipeline-spec.md) — the YAML shape of `_apex/pipelines/*.yaml`.
- [framework.yaml reference](../reference/framework-yaml.md) — the metadata file written on every install.
- [Why project-local pipelines](../explanation/why-project-local-pipelines.md) — design rationale.
