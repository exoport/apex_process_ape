# How to install the APEX framework into a project (first time)

`ape framework setup` is the one-time install of an APEX framework release into a project. It copies the release's skills and tables into the project, records what it installed in `_apex/framework.yaml`, and commits the result. It writes no `_apex/config.yaml`: for an APEX v1 release, `ape project init` does that next.

For later framework releases, use [`ape framework update`](framework-update.md).

## Prerequisites

- An ape at or above the release's `min_ape_version` (1.0.0 for an APEX v1 release). `ape version` shows yours. Below it, see [ape's minimum version](framework-update.md#apes-minimum-version).
- Access to the framework repository. You do not need a clone of your own: with neither `--repo` nor `$APEX_FRAMEWORK_REPO` set, ape keeps its own clone under the user cache directory, cloned from `$APEX_FRAMEWORK_URL`, else `https://github.com/exoar/apex_process_framework.git`. The repository is private, so the machine needs git credentials for it. To use a clone you already have:

  ```bash
  export APEX_FRAMEWORK_REPO=/path/to/apex_process_framework
  ```

  See [The framework and governance clones are caches](../explanation/framework-and-governance-clones.md) for what ape does to each kind of clone.
- The project is a git repository on a branch, with no modified or staged tracked files, because the install is committed. Untracked files are fine. Pass `--no-commit` to leave the result in the working tree instead.
- `_apex/framework.yaml` does not exist yet. If it does, the framework is installed: use `ape framework update`.

## Install

```bash
cd /path/to/your/project
ape framework setup
ape project init
```

What `setup` does:

1. Refuses if `_apex/framework.yaml` exists (exit 6), and, when committing, if the tree is not clean (exit 4).
2. Fetches the framework clone's tags (skip with `--no-fetch`) and picks the newest final release `vX.Y.Z`. `--version vX.Y.Z` picks one exactly, and `--version vX.Y.Z-rc.N` is the only way to install a release candidate. See [Which release is installed](framework-update.md#which-release-is-installed).
3. Checks this ape against the release's `min_ape_version`. Below it, nothing is written: at a terminal it offers to update ape first and re-runs itself; otherwise it exits 11.
4. Installs the release's files (below) and creates the board.
5. Writes the `.claude/` shape into the root `.gitignore`.
6. Commits everything as `chore(framework): install APEX framework vX.Y.Z`, with `Framework-Version`, `Framework-Commit` and `Generator: ape framework setup` trailers.
7. Ends with `next: ape project init` when the project has no v1 layout yet.

Either order works, and each command names the other in its `next:` line. Running `setup` first keeps the install commit before the project's v1 history starts.

## What gets installed

| Path | What it is |
| --- | --- |
| `.claude/skills/apex-*/` | The framework's skills. Existing `apex-*` directories are removed first, so a skill the release no longer ships disappears. Skills not named `apex-*` are never touched. |
| `_apex/ape-commands.yaml` | The release's manifest. Its `min_ape_version` is the oldest ape the release runs on. |
| `_apex/migrations/*.md` | The release's upgrade entries, which `ape framework update` runs. `setup` installs them and runs none. |
| `_apex/standards/adr-0001.md` | APEX's standard ADR, "Record architecture decisions", which `ape project init` writes as the project's ADR-0001. |
| `_apex/framework.yaml` | What was installed, from which release, by which ape. See the [framework.yaml reference](../reference/framework-yaml.md). |
| `.aboard/` | The project's [board](use-the-board.md), with a `.aboard/.gitignore` that ignores everything beside itself. An existing board is never overwritten. |
| `.gitignore` (repo root) | The `.claude/` shape, below. Created when absent. |

An APEX v1 release ships no pipelines, config template, operating rules or effort table, so a v1 install writes none of them, and its `framework.yaml` records `config.seeded: false`.

### The `.claude/` shape in `.gitignore`

The skills must be committed, but most of `.claude/` is personal. `setup` makes the root `.gitignore` say so:

```gitignore
# APEX (ape): .claude/settings.json and .claude/skills/ are the team's; the rest of .claude/ and CLAUDE.local.md stay personal
.claude/*
!.claude/settings.json
!.claude/skills/
CLAUDE.local.md
```

A line that ignores the whole `.claude` folder is rewritten in place to this shape, so the installed skills are committed even where `.claude/` used to be ignored. A file of yours that the rewrite un-ignores, such as a skill of your own, is listed in the output and left out of the commit: commit it or ignore it by name. The output's `Ignore:` line (JSON `gitignore`) says what was done.

## Then: `ape project init`

`setup` installs the framework. It does not make the repository an APEX v1 project. `ape project init` writes `_apex/config.yaml`, `AGENTS.md`, `CLAUDE.md`, the `development/` skeleton, the hooks in `.claude/settings.json` and the pre-commit secret scan, and commits them as `apex: project init`. `ape project init --help` lists everything it writes. The [first v1 project tutorial](../tutorials/first-v1-project.md) continues from there.

## Headless and scripted runs

A v1 install asks nothing, so it runs the same with or without a terminal. `--project-name` and `--extensions` are not needed; passed, they are ignored, and `setup` says so. Use `--output-format json` or `yaml` for a structured result: `summary` counts what was installed, `next` carries the next command, and `gitignore` says what happened to `.gitignore`. Stdout then holds the report alone: the `commit:` line goes to stderr.

## Re-installing with `--force`

`ape framework setup --force` installs again over an existing `_apex/framework.yaml`, and resets what it recorded from the first setup. To move an installed project to a newer release, use `ape framework update` instead.

## Inspecting what was installed

```bash
ape framework status
```

Prints the installed release, compares it with the newest release in the framework clone, and says where the framework and governance clones are.

## APEX v0 releases

`setup` still installs an APEX v0 release (one whose `min_ape_version` is below 1.0.0, or that ships `_apex/pipelines/` instead of the manifest). For a v0 release it also copies the pipelines, seeds `_apex/config.yaml` (an interactive prompt, or `--project-name` and `--extensions`, or `--no-bootstrap` to skip it; without a terminal and without those flags it exits 5), installs the operating rules and the v0 tables, and adds the `sprint-status.yaml.lock` line to `.gitignore`. A v0 project is served by the v0 ape its `.bingo` pins; its docs are at https://github.com/exoport/apex_process_ape/tree/v0.10.0/docs.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Installed. |
| 3 | The framework source: no release, the `--version` tag does not exist, or the tag is not a release layout (no `.claude/skills/`, or neither `_apex/ape-commands.yaml` nor `_apex/pipelines/`). Also a clone of yours that is behind the release and has local changes (`--force-clone` overwrites them). |
| 4 | The project tree: not a git repository, a detached HEAD, or modified or staged tracked files. |
| 5 | APEX v0 only: no terminal, and no `--project-name`/`--extensions` or `--no-bootstrap`. |
| 6 | Already installed: `_apex/framework.yaml` exists. |
| 11 | This ape is below the release's `min_ape_version`. |
| 12 | The install is complete but the commit failed (a hook failed). The changes are left staged. |

## Troubleshooting

### `framework already installed at <path>`

The project already has `_apex/framework.yaml`. Run `ape framework update`, or `ape framework setup --force` to install again from scratch.

### `framework repo has uncommitted changes` or `framework repo is on branch X (expected main)`

Only with `--from-worktree`, which installs the clone's working tree instead of a release, and needs that clone clean and on `main`. Drop `--from-worktree` to install a release, or pass `--force`.

## Related

- [How to refresh the framework](framework-update.md): every install after the first.
- [framework.yaml reference](../reference/framework-yaml.md): the metadata file `setup` writes.
- [Why setup and update are separate](../explanation/why-setup-and-update-are-separate.md).
- [APEX v1 scope](../explanation/apex-v1-scope.md).
