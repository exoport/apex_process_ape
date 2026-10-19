# Your first APEX v1 project

This walks a new repository from nothing to a project `ape` can drive: the v1
layout, a milestone a person approved, and a green `ape doctor`. It ends where the
framework's skills take over (`apex-plan`, then `apex-orchestrator`), so it needs
no model session and costs nothing.

You need `ape` installed ([How to install ape](../how-to/install.md)), `git`,
Claude Code on `PATH`, and read access to the APEX framework repository, which
`ape framework setup` fetches (its `--help` says from where).

## 1. Check the host

```bash
ape doctor
```

Every host row should read ✅ or ℹ️. Outside a project, the project rows report
INFO: there is nothing to judge yet.

## 2. Install the framework and make the repository an APEX v1 project

```bash
mkdir greeter && cd greeter && git init -q
ape framework setup
ape project init
```

`framework setup` installs the framework's skills (`.claude/skills/apex-*`), its
migrations and `_apex/framework.yaml`, and commits them as `chore(framework):
install APEX framework …`. It comes first, so the install lands before the v1
epoch and `init` finds the skills already there; it ends with
`next: ape project init`. Run in the other order, `init`'s `next:` asks for
`framework setup` instead.

`init` writes `_apex/config.yaml`, `AGENTS.md`, `CLAUDE.md`, the `development/`
folders and `_apex/state/`, merges ape's hooks into `.claude/settings.json`, and
commits all of it as `apex: project init`. That commit is the project's v1 epoch:
`ape project check` judges history from there on.

## 3. Say what the project is

Fill the sections of `development/project.md` (Vision, Users, Scope, Out of
scope, Constraints): one or two lines each is enough. Then:

```bash
ape project check
```

It lists what is still missing. The roadmap has no milestone yet, so it says so.

## 4. Propose and approve a milestone

```bash
ape milestone new "Walking skeleton" --gate "go test ./..." --goal "greets a name"
ape milestone approve M1 --by "$(git config user.name)"
git add -A && git commit -q -m "plan: project.md and M1"
ape project check
```

`approve` is a person's act: ape refuses it inside a model session
(`APE_SESSION` or `CLAUDECODE` set, which includes a `!` command typed in Claude
Code). Run it from a plain terminal. `project check` is now green.

## 5. Look at where the project stands

```bash
ape status
ape doctor --strict
```

`status` shows M1 approved with no changes in flight, and what comes next.
`doctor --strict` runs the host checks plus the v1 project checks (project check,
hooks, plugins, the ape floor, the Claude Code version, the `_apex/state` ignore
rules, whether `.claude/settings.json` is shared through git). Every v1 row reads
OK here, `skills.project` included, since step 2 installed the framework's skills;
`--strict` exits 0 on a healthy project.

## Where to go next

The changes of M1 are planned and delivered by the framework's skills, which call
`ape change new|start|verify|review|archive` for you; `ape change --help` shows
the lifecycle they follow. To run one skill yourself, see
[How to run a single skill with `ape task`](../how-to/run-a-single-skill.md).
