# How to start an APEX v1 project

`ape project init` makes a git repository an APEX v1 project: it writes the v1 skeleton, merges
ape's hooks into `.claude/settings.json`, installs git's pre-commit secret scan, and commits the
result as `apex: project init`. This page shows how to use it on a new repository and on an
existing codebase that never used APEX, what it writes, and how to run it again to repair a
project. To be walked through a first project step by step, follow the
[first v1 project tutorial](../tutorials/first-v1-project.md) instead. To move an APEX v0 project
to v1, see [How to lift an APEX v0 project to v1](lift-a-v0-project.md).

## Before you start

- `ape` 1.0.0 or later on `PATH`. git's pre-commit hook that init installs runs the `ape` it finds
  on `PATH` (`exec ape hook pre-commit`), so an older ape without `ape hook` first on `PATH`
  fails every commit that is not ape's own with `unknown command "hook"`.
- A git repository on a branch. init commits what it writes.
- Access to the APEX framework repository, which `ape framework setup` fetches. See
  [How to install the APEX framework into a project](framework-setup.md#prerequisites).

## A new repository

Install the framework's skills first, then run init:

```bash
cd /path/to/new-project
git init
ape framework setup
ape project init
```

`framework setup` commits the skills as `chore(framework): install APEX framework vX.Y.Z` and
ends with `next: ape project init`. init then writes the skeleton and commits it.

Either order works, and each command names the other in its `next:` line. Run setup first: then
its commit comes before the v1 epoch, and a setup run after init makes a commit that is not ape's
own, so git's pre-commit hook runs for it.

## An existing codebase

In a repository that already has code and history, but never used APEX:

```bash
cd /path/to/existing-project
ape framework setup
ape project init --brownfield
```

`--brownfield` changes nothing init writes. It only records, in `_apex/state/project.json`, that
the project existed before APEX (`"brownfield": true`, beside `initialized_at` and `base_sha`, the
commit init started from). That file is ignored by git, so the record stays in this checkout, and
no ape command reads it.

What init does differently in an existing repository comes from what is already there, with or
without the flag:

- Your own files are never overwritten. An existing `CLAUDE.md`, `AGENTS.md`,
  `_apex/config.yaml` or `.gitignore` gets only the lines listed in
  [What init never overwrites](#what-init-never-overwrites).
- An existing `AGENTS.md` gets the `Project context:` line, but not the line
  ``Talk to APEX: type `/apex-orchestrator` in Claude Code.``. Add it with
  `ape project operator-interface`, which commits it as `apex: operator interface (seq-07)`.
- Your uncommitted work stays as it was. The `apex: project init` commit holds only what init
  wrote or amended: modified and untracked files of yours are not in it.
- The history before the `apex: project init` commit is not judged. `ape project check` and
  `ape status` read the project's history from that commit on.

Then adopt the project with `apex-plan`, giving it a request rather than a brief. Open Claude Code
in the project and type `/apex-orchestrator`, and ask it to plan; or type `/apex-plan <request>`
yourself; or, from a terminal:

```bash
ape task apex-plan --args "<request>"
```

On an existing project, apex-plan describes what exists and rewrites nothing. It fills
`development/project.md` and `AGENTS.md` (carrying the rules your `CLAUDE.md` and `AGENTS.md`
already state, word for word), sets `regression:` in `_apex/config.yaml` to the project's own
check command, puts what already fails into `development/next.md`, and proposes milestones with
gates. It commits as `plan: <summary>` and ends with the words for you to type: `approve M1`, and
`accept the config` when it changed `_apex/config.yaml`. `regression` is a guarded block: until a
person accepts the change (`ape project accept-config --by <you>`), `ape project check` reports
`config.changed`, and `ape change start`, `verify` and `archive` refuse. See [What only a person does](../explanation/person-acts.md),
[How to run a milestone](run-a-milestone.md), and
[The operator and the loop](../explanation/operator-and-loop.md).

## What init writes

init creates each of these only where it is absent:

| Path | What it is | Committed |
| --- | --- | --- |
| `_apex/config.yaml` | `project_name` (the folder's name), `development_folder: development`, and the `models:` defaults for each skill ape starts. See the [config.yaml reference](../reference/config-yaml.md). | yes |
| `AGENTS.md` | The project map skeleton, with the `Project context:` line that imports `development/project.md` and the `Talk to APEX` line. | yes |
| `CLAUDE.md` | The single line `@AGENTS.md`. | yes |
| `development/project.md` | The skeleton: Vision, Users, Scope, Out of scope, Constraints. | yes |
| `development/roadmap.md` | `# Roadmap` and the line pointing at `next.md`. | yes |
| `development/next.md` | `# Next`: findings to triage, not milestones. Written only with a new roadmap. | yes |
| `development/specs/`, `changes/`, `archive/`, `decisions/`, `briefs/` | Empty folders, each kept with a `.gitkeep`. | yes |
| `development/decisions/0001-record-architecture-decisions.md` | ADR-0001, APEX's standard ADR, accepted, and recorded in `_apex/state/decisions.json`. See [How to record a decision](record-a-decision.md). | yes |
| `_apex/state/` | ape's records. Its `.gitignore` is a whitelist: `milestones.json` and `decisions.json` tracked, the rest ignored. See [How to find and read run artefacts](run-artefacts.md). | the whitelist and `decisions.json` |
| `_apex/state/project.json` | init's record: `initialized_at`, `brownfield`, `base_sha`. | no (ignored) |
| `hot/.gitignore` | `*`: `hot/` holds the project's secrets, which checks read at run time. | no (ignored by itself) |
| `.claude/settings.json` | ape's hooks, merged into the file; other keys and hooks are kept. See the [hooks reference](../reference/hooks.md). | yes |
| `.gitignore` | The `.claude/` shape: `.claude/settings.json` and `.claude/skills/` shared, the rest of `.claude/` and `CLAUDE.local.md` personal. Created when absent. | when it was absent, or tracked and clean |
| `<hooks dir>/pre-commit` | git's pre-commit hook, which runs `ape hook pre-commit`, the commit secret scan. `<hooks dir>` is `core.hooksPath` when set, else `.git/hooks`. | no (git never versions hooks) |

The commit is `apex: project init`. The JSON output lists the paths in `created`, `kept`,
`hooks_added`, and `amended`.

## What init never overwrites

A file or folder that exists is kept as it is, with these exceptions:

- **`CLAUDE.md`** without the line `@AGENTS.md` gets it appended, and nothing else.
- **`AGENTS.md`** with no `@development/project.md` outside code gets the `Project context:` line
  after its first `#` heading (or at the top), and nothing else.
- **`_apex/config.yaml`** with no top-level `models:` gets the defaults block appended, its own
  text kept byte for byte. If the result would not parse, the file is restored and init warns.
  The amendment is committed only when the config was tracked and clean before; otherwise it is
  left for you to commit, and JSON `amended_uncommitted` lists it. An existing `models:` is left
  alone: init warns for each skill it has no entry for, since that skill then runs on Claude
  Code's default model.
- **`.claude/settings.json`**: a hook of ape's that was altered (another matcher, `async`, a
  shorter stop timeout) is replaced by ape's. A file that is not valid JSON stops init with exit
  1: fix it and run init again.
- **`.gitignore`**: a line that ignores the whole `.claude` folder is rewritten in place to the
  `.claude/` shape, since git never reads a negation under an ignored folder. Your other lines are
  kept.
- **git's pre-commit hook**: one that is not ape's is kept and never edited. `ape project check`
  then lists `hooks.pre-commit-absent` with the line to add to it.

## Running init again

init is idempotent: on a project that is in shape it writes nothing and commits nothing. Run it
again to repair:

- **Hooks.** A missing or altered ape hook in `.claude/settings.json` is restored and committed as
  `apex: project init`.
- **A fresh clone.** git does not clone hooks, so a new checkout has no pre-commit hook. init
  installs it, and recreates the ignored `hot/.gitignore` and `_apex/state/project.json`, with no
  commit.
- **An older project.** An `AGENTS.md` without the `Project context:` line gets it, and a config
  without `models:` gets the defaults.

```bash
ape project init
```

Run the repair from a terminal. In a model session (`APE_SESSION` or `CLAUDECODE` set) on a
project that is already v1, init repairs the same way but commits nothing: an
`apex: project init` commit restarts the project's v1 epoch and would hide every finding before
it, so only a person's init makes one after the first. It prints a warning, and `--json` lists the
files in `commit_withheld`. The session's own commit takes them, under its own subject. A
project's first init commits from a session as from a terminal, which is how apex-plan starts
a new project. See [What the guard reads](../explanation/person-acts.md#what-the-guard-reads).

With `--json`, the output is one object on stdout. Two fields tell you what to do next:

- `next`: the next step, the same line the human output ends with.
- `warnings`: what init could not settle itself, such as a `models:` block with no entry for a
  skill, or defaults it could not append. They are printed on stderr in the human output.

`pre_commit` (`path`, `status`: `installed`, `present` or `kept`) and `gitignore` (`action`:
`added`, `rewritten` or `already`) say what init did with git's hook and the `.gitignore`.
`scope` states the v1.0 scope: one operator per checkout, on a local trunk. `ape change start`
and `ape change archive` refuse while the base branch is behind its upstream.

## Then: check the project

The last line init prints is its `next:`. While no `.claude/skills/apex-*` is installed, it asks
for `ape framework setup` first. With the skills in place, it names apex-plan and
`ape project check`.

```bash
ape project check
ape doctor --strict
```

`ape project check` lists every finding with its fix. Right after init it lists the empty
sections of `development/project.md` and a roadmap with no milestone; once you, or apex-plan,
fill them, it exits 0. `ape project codes` explains each code.

`ape doctor --strict` runs the host checks and the v1 project rows (`project.check`,
`hooks.project`, `hooks.prompt`, `hooks.pre-commit`, `state.gitignore`, `settings.shared` and
others), and exits 1 on any warning. On a host without Node, the host rows `node.binary` and
`npx.binary` warn, so `--strict` exits 1. No APEX v1 skill needs Node; skip them:

```bash
ape doctor --strict --skip node.binary,npx.binary
```

See [How to run `ape doctor` in CI](run-doctor-in-ci.md#apex-v1-project-findings) for each v1 row.
