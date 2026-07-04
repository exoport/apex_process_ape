# How to refresh the framework in a project

`ape framework update` is the refresh command. It re-copies skills + pipelines from a checked-out `apex_process_framework` repo into your project, and refreshes the `framework:` block of `_apex/framework.yaml`. It does **not** touch `_apex/config.yaml` — that's the one-time bootstrap from [`ape framework setup`](framework-setup.md).

Use `update` whenever you bump the framework repo to a new version and want your project to pick up the changes.

## Prerequisites

- ape `v0.0.7` or later — `ape version` to confirm.
- The project must have been set up first via `ape framework setup`. Update refuses to run if `_apex/framework.yaml` is absent.
- A local clone of `apex_process_framework`. Either pass `--repo PATH` on every invocation, or set `$APEX_FRAMEWORK_REPO` once:

  ```bash
  export APEX_FRAMEWORK_REPO=/path/to/apex_process_framework
  ```

- The framework repo must be on `main` and have a clean working tree. To bypass either check, pass `--force`.

## Quickstart

```bash
cd /path/to/your/project
ape framework update
```

What happens:

1. Validates that `_apex/framework.yaml` is **present** (the project is set up). If absent, exits with `Error: framework metadata not found at <path> — run "ape framework setup" to install`.
2. Validates `$APEX_FRAMEWORK_REPO` (repo layout, git state, branch, working tree).
3. Fetches `origin/main` and fast-forwards (skip with `--no-fetch`).
4. Records the framework's HEAD SHA + tag for the metadata file.
5. Removes any existing `<project>/.claude/skills/apex-*` (so removed-from-framework skills disappear locally).
6. Copies all `apex-*` skill directories into `<project>/.claude/skills/`.
7. Copies all framework pipeline YAMLs into `<project>/_apex/pipelines/`.
8. Rewrites `<project>/_apex/framework.yaml` — preserving the `sources.config` block recorded by the original `setup` so `project_name` + `extensions` stay intact.

## What gets touched

| Path                              | Behavior on update                 |
| --------------------------------- | ---------------------------------- |
| `.claude/skills/apex-*/`          | Wiped + reinstalled                |
| `_apex/pipelines/*.yaml`          | Overwritten                        |
| `_apex/config.yaml`               | **NOT touched** (that's setup)     |
| `_apex/config.local.example.yaml` | **NOT touched**                    |
| `_apex/framework.yaml`            | Rewritten (config block preserved) |

Non-`apex-*` entries under `.claude/skills/` are never touched.

## Idempotency

Running `update` twice on a steady-state project is safe and cheap:

- Skills: wiped + reinstalled. Net effect identical when the framework HEAD is unchanged.
- Pipelines: overwritten. Net effect identical.
- `framework.yaml`: rewritten with a fresh `installed_at` timestamp.

The destructive operation that matters — wiping `apex-*` skills — is git-safe: if the project is a git repo and you have uncommitted edits to a tracked `apex-*` skill file, the command refuses without `--force`. Untracked `apex-*` paths are treated as leftovers and get clobbered.

## Inspecting drift

```bash
ape framework status
```

Compares the installed framework version (from `_apex/framework.yaml`) against the framework repo's current HEAD. When the SHAs or tags differ, drift fields are populated and the output suggests running `update`.

## Output formats

All flags work with `--output-format human|json|yaml`:

```bash
ape framework update --output-format json | jq '.summary'
# { "skillsInstalled": 86, "skillsRemoved": 0, "pipelinesInstalled": 3, ... }
```

## Troubleshooting

### `framework metadata not found at <path> — run "ape framework setup" to install`

You haven't run `setup` on this project yet. Update is refresh-only; use [`ape framework setup`](framework-setup.md) for the first install.

### `framework repo path not set`

Pass `--repo /path/to/apex_process_framework` or export `APEX_FRAMEWORK_REPO`.

### `framework repo has uncommitted changes (pass --force to bypass)`

The framework repo must be clean. Either commit/stash the framework-side changes, or pass `--force` to clobber-install from a dirty tree (recorded in `framework.yaml` so the divergence is auditable).

### `framework repo is on branch X (expected main)`

The command refuses to install from a non-`main` branch unless `--force` is passed. This is to prevent accidentally pinning a project to an experimental branch's HEAD.

### `uncommitted changes under .claude/skills/apex-*`

You have local edits to one or more committed framework skills. Either:

- Commit the edits in your project (they'll get clobbered on the next update but at least you have the diff in git).
- Pass `--force` to clobber them now.
- Stash them somewhere outside `.claude/skills/apex-*`.

Untracked `apex-*` paths don't trip this check — only tracked-and-modified ones do.

### `framework branch "main" diverged from origin`

`git merge --ff-only` failed because the framework repo's local main has commits not on the remote. Either rebase manually or pass `--no-fetch` to skip the pull.

## Related

- [How to set up the framework (first install)](framework-setup.md) — run this before `update`.
- [Pipeline spec reference](../reference/pipeline-spec.md) — the YAML shape of `_apex/pipelines/*.yaml`.
- [framework.yaml reference](../reference/framework-yaml.md) — the metadata file written on every install.
- [Why setup and update are separate](../explanation/why-setup-and-update-are-separate.md) — design rationale for the split.
