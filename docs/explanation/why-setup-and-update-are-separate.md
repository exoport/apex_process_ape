# Why `framework setup` and `framework update` are separate commands

Up to and including ape `v0.0.6`, a single command `ape framework update` did two unrelated jobs:

1. **Initial install** — copy skills + pipelines into a fresh project, seed `_apex/config.yaml` via an interactive Bubble Tea prompt, write `_apex/framework.yaml`.
2. **Subsequent refresh** — re-copy skills + pipelines against a newer framework HEAD, leaving `config.yaml` alone.

ape `v0.0.7` (PLAN-1 / I3) splits them into `ape framework setup` and `ape framework update` with strict refusal semantics either way. This page explains why.

## Two jobs, one verb is a leaky abstraction

The two jobs differ on every axis that matters:

|                             | `setup` (the v0.0.7 name)                          | `update` (the v0.0.7 name)     |
| --------------------------- | -------------------------------------------------- | ------------------------------ |
| Pre-condition               | `_apex/framework.yaml` absent                      | `_apex/framework.yaml` present |
| Touches `_apex/config.yaml` | Yes — seeds it via TUI bootstrap                   | No — preserves existing values |
| Interactive flow            | Yes (project_name + extensions prompt)             | No                             |
| Flags                       | `--project-name`, `--extensions`, `--no-bootstrap` | Just `--no-fetch`, `--force`   |
| Frequency                   | Once per project, ever                             | Every framework version bump   |
| Failure mode if wrong state | n/a (didn't refuse before)                         | n/a (didn't refuse before)     |

Under v0.0.6 the conflation didn't actively _break_ anything, but it had three real costs:

- **Discoverability**: new users had to read docs to learn that `framework update` was also the install command. The verb "update" implies refreshing something that already exists.
- **Scripting confusion**: a CI script calling `framework update` couldn't tell whether the run was a first-install or a refresh. The structured output's `seeded` field implied it, but most callers ignored it.
- **Safety**: a typo in the project name during `update`'s first interactive prompt was easy to make and easy to miss. Re-running `update` didn't re-prompt (because `config.yaml` already existed), so the typo became permanent until the user manually deleted the config file.

## What the split bought

After v0.0.7:

- **`setup` errors loudly if already installed**: `framework already installed at <path> — run "ape framework update" to refresh, or "ape framework setup --force" to re-bootstrap (resets project_name and extensions)`. Users can't accidentally re-bootstrap.
- **`update` errors loudly if not yet installed**: `framework metadata not found at <path> — run "ape framework setup" to install`. Scripts can `setup`-or-`update` deterministically without checking file presence themselves.
- **`--force` semantics are clear**: on `setup`, `--force` means re-bootstrap (resets `project_name` + `extensions`). On `update`, `--force` keeps its existing meaning (bypass framework-clean / project-skills-modified checks).
- **CI scripts get distinct exit codes**: `exitCodeAlreadyInstalled` (6) and `exitCodeNotInstalled` (7) supplement the existing `exitCodeFrameworkValidation` (3) and `exitCodeProjectSkillsModified` (4) so callers can branch.
- **The internal Go API matches**: `framework.Setup(ctx, *UpdateOptions)` and `framework.Update(ctx, *UpdateOptions)` are now distinct functions sharing a private `installCore` helper. The `Update` orchestrator ignores `Bootstrapper`.

## The downside: a coordinated change

This is a breaking CLI change, so every caller that scripts `ape framework update` had to be updated to branch:

```python
# What the eval harness does (apex_eval/runner.py):
framework_yaml = temp_dir / "_apex" / "framework.yaml"
subcommand = "setup" if not framework_yaml.exists() else "update"
```

For the eval that's one helper change; for any other downstream tooling it's a similar two-line patch. The pain is concentrated at the boundary and clearly attributable.

## Path not taken: auto-detect

The most "convenient" alternative would have been a single `framework install` (or `framework sync`) command that auto-detects setup-vs-update based on file presence. We rejected this for the same reasons we rejected the v0.0.6 conflation: it makes the failure modes less informative, the documentation harder to write, and the audit trail in CI logs less useful. Two commands with strict pre-conditions are cheaper than one polymorphic command that hides its branching.

## See also

- [How to set up the framework (first install)](../how-to/framework-setup.md)
- [How to refresh the framework](../how-to/framework-update.md)
- [PLAN-1 in this repo](../../development/planning/plan-1_pipeline-ux-and-framework-setup.md) — the plan that drove the split.
- `internal/framework/install.go` — `Setup`, `Update`, and the shared `installCore` orchestrator.
- `internal/framework/metadata.go` — `NotInstalledError` and `AlreadyInstalledError`.
