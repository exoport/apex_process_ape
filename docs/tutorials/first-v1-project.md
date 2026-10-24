# Your first APEX v1 project

This walks a new repository from nothing to a project `ape` can drive: the v1
layout, a milestone a person approved, and a green `ape doctor`. It ends where the
framework's skills take over (`apex-plan`, then `apex-orchestrator`), so it needs
no model session and costs nothing.

You need `ape` installed ([How to install ape](../how-to/install.md)), `git`,
Claude Code on `PATH`, Go on `PATH` (the milestone's gate below is
`go test ./...`), and read access to the APEX framework repository, which
`ape framework setup` fetches (its `--help` says from where).

ape runs the gate when you approve the milestone. Without Go the gate exits
127, and ape refuses the approval with `milestone.gate-broken`. To follow along
without Go, use a gate your machine can run, such as `test -x ./greet`.

## 1. Check the host

```bash
ape doctor
```

Every host row should read ✅ or ℹ️, except `node.binary` and `npx.binary` on a
machine without Node: they read ⚠️. No APEX v1 skill needs Node, so that warning is
safe to leave. Outside a project, the project rows report INFO: there is nothing to
judge yet.

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

`approve` runs the gate first, and the gate must fail: a gate that already passes
proves nothing the milestone adds, so ape refuses it (`milestone.gate-green`). Here
there is no code yet, so `go test ./...` fails.

`approve` is a person's act. ape refuses it inside a model session
(`APE_SESSION` or `CLAUDECODE` set, which includes a `!` command typed in Claude
Code), so run it from a plain terminal. In your own Claude Code session there is a
second way: type `approve M1` yourself, and the session may then run the command
for you ([What only a person does](../explanation/person-acts.md)).

`project check` is now green.

## 5. Look at where the project stands

```bash
ape status
ape doctor --strict
```

`status` shows M1 approved with no changes in flight, and what comes next.
`doctor --strict` runs the host checks plus the v1 project checks (project check,
hooks, plugins, the ape floor, the Claude Code version, the `_apex/state` ignore
rules, whether `.claude/settings.json` is shared through git). Every v1 row reads
OK here, `skills.project` included, since step 2 installed the framework's skills.

`--strict` turns every warning into a failure, the host rows' included, so it exits 0
only when nothing warns. On a machine without Node, skip the two Node rows:

```bash
ape doctor --strict --skip node.binary,npx.binary
```

## Where to go next

The changes of M1 are planned and delivered by the framework's skills. They call
`ape change new|start|verify|review|archive` for you, following the lifecycle in
[the change lifecycle reference](../reference/change-lifecycle.md). Start the
work in one of two ways:

- Open Claude Code in the project and type `/apex-orchestrator`. This is your own
  session: it plans with you and starts the unattended run.
- From a terminal, run the milestone unattended with `ape task apex-loop --args M1`.

[The orchestrator and the loop](../explanation/orchestrator-and-loop.md) explains the
difference. To run one skill yourself, see
[How to run a single skill with `ape task`](../how-to/run-a-single-skill.md).
