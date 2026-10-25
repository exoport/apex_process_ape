# When `ape` runs inside a session `ape` started

A model session is one ape launches: `ape task`, `ape prompt`, `ape chat`,
`ape change review --run`, and the `ape service` task and prompt jobs, which
run those commands. This page explains the one level of nesting ape allows
under them, how ape knows a session's depth, and why the rule is ape's
rather than the model's (decisions D61 and D62).

| Depth | What sits there | May start a model session? |
| ----- | --------------- | -------------------------- |
| 0 | Anything outside an ape-launched session: a terminal, a Claude Code session ape did not launch, a shell, an `ape script` program, the `ape service` daemon | Yes, any |
| 1 | A session ape launched from depth 0 | Only when it is ape's own `apex-loop` run (`ape task apex-loop`), only a skill on its allow-list, one child at a time |
| 2 | A session a loop run started | No |

The allow-list is `apex-plan`, `apex-change`, `apex-apply`, `apex-review` and
`apex-accept` (never `apex-release`, D524), the skill `human_review:` names as its
delegate, and every skill the `tools:` slots name. `ape change review --run`
counts as starting `apex-review`, and `ape change tool` and `ape change accept
--delegate` start their skills through a detached `ape task` the same way, so
the same rule applies to them. `apex-handoff`, `apex-loop` and
`apex-orchestrator` are not on the list: a handoff is a person's tool, a loop
starting another loop is exactly the nesting the rule exists to prevent, and
`apex-orchestrator` opens only in a person's own session.

Deterministic commands (`ape status`, `ape change new|check|start|verify|archive`,
`ape milestone gate|close`, `ape next add` and the rest) are never limited:
skills run them inside sessions all the time, and they start nothing.
`ape script` is a conductor and refuses inside any ape session.

A refusal is exit 2 with code `session.depth` (a session that may not start
one) or `session.not-allowed` (a loop run starting a skill outside its
allow-list), both action `stop`. A second child while one runs is exit 3,
`session.child-running`, and a second `apex-loop` run in the same project
exit 3, `session.loop-running`, both action `retry`.

`session.loop-running` was `session.orchestrator-running` until v1.3: the loop
was named `apex-orchestrator` before rc.7, and that name now belongs to the
operator's skill. The old code is kept as an alias until v1.5: the refusal
names both (`code session.loop-running, action retry; formerly
session.orchestrator-running`), a diagnostic carries the old one in `alias`,
and `ape project codes` lists it as a row of its own.

## The orchestrator's session is depth 0

You talk to APEX v1 by typing `/apex-orchestrator` in your own Claude Code
session. ape did not launch that session, so it is depth 0, like a terminal:
it may start any session. That is how the orchestrator starts `apex-plan`,
`apex-change` and `apex-loop` for you, each as a detached `ape task` run at
depth 1. Only the loop goes on to start children. The orchestrator itself is
never an ape run: `ape task apex-orchestrator` is refused with
`skill.renamed`, and `ape prompt --agent apex-orchestrator` too. See
[The orchestrator and the loop](orchestrator-and-loop.md).

## How ape knows

Every `claude` session ape spawns carries `APE_SESSION=<kind>/<run-id>` and
`APE_DEPTH`; a child also carries `APE_PARENT_RUN` (the loop's run id) and
`APE_PARENT_PID` (the ape process running the loop). Claude Code hands its
environment to every tool subprocess, so an `ape task` a skill types into the
session's Bash tool sees them.

The environment alone is not trusted: a model can run
`env -u APE_SESSION ape task …`. So every session ape launches is also a row
in the live-session registry, `~/.ape/registry.json` (what `ape sessions`
lists), naming the ape process running it, the `claude` it spawned, the skill
ape typed, and its depth. A session-starting command walks its own parent
processes, and when one of them is a process a row names, it runs inside that
session. Its depth is the larger of the two answers. Rows are matched by pid,
never by process name, so a stand-in `claude` (a test or eval stub) gets the
same answer a real session does. Whether a session is the loop is decided from
ape's record of the skill it typed, never from the model.

What remains open, and is documented rather than chased: a model that scrubs
its environment and also double-forks into a daemon leaves no ancestor ape
knows. On Windows an ancestor that exited may have had its pid reused, so the
walk there is best effort.

An `ape service` daemon is depth 0 and cannot see where a request comes from,
so every job carries the caller's stamp, which `ape service submit` takes
from the same record. A request without one, or from depth 2, is refused with
`session.depth` and nothing runs. A depth-1 caller's job runs only for a
running `apex-loop` run, under the same allow-list and one-child lock, as
that loop's child. The stamp is the client's word: a raw NATS publish can
claim depth 0, so the NATS credentials remain that boundary.

There is no variable that disables the rule. A switch a session can flip is a
switch a session will flip.

## Children die with their parent, and record it

The loop dispatches every skill detached:
`ape task <skill> --args <id> --detach`, then `ape run wait <id> --wait 9m`
until it stops exiting 75. The detached child's supervisor checks the loop's
ape process every two seconds; when it is gone, it stops the child the way
`ape run stop` does, and the child's manifest records
`termination: {kind: parent_gone}`. Each child's manifest records `depth: 2`
and `parent_run`; `ape status --json` lists the live sessions with their
parent, and `ape costs run <loop-run>` and the loop's own manifest list its
children with `tree_cost_usd`. That field is written for every loop run once
it ends, `0` included, so an absent field means not computed. A manifest's
`totals` stay its own session's, so a sum over every manifest counts each
child once.

A loop session that has filled its context is relayed rather than nested: its
next child launch is refused with `session.relay`, the session ends, and the
same `ape task` process starts a fresh loop session under the same lock. See
[How to run a single skill](../how-to/run-a-single-skill.md#the-loop-relay).

## Why the rule is this strict

Open nesting, where a conductor session dispatched ape runs from inside
itself whenever the model chose, failed in four ways, because the **outer**
run could not see the inner one:

- **A foreground call cannot work.** Claude Code's Bash tool times out after
  about two minutes by default, and ten at most. While a dispatch blocks, the
  outer run's idle anchor sees no progress and eventually stops itself.
- **A background shell only worked because of polling.** Every poll was a
  tool call, and so the outer run's proof of life. One long sleep instead, and
  the outer run was killed with no error naming the cause.
- **Yielding the turn tore the outer run down.** ape derives completion from
  the `Stop` hook, so a conductor that started an inner run in the background
  and ended its turn finished its own run and orphaned the inner one.
- **Two runs raced on one working tree.**

D62 keeps what those lessons bought and allows the one level D61 needs. The
structure is ape's, not the model's: only the loop nests, its children are
detached and re-attached within the Bash tool's limit (exit 75), one runs at
a time, each has its own manifest and lifecycle, and none outlives the loop.
The earlier failures came from models deciding when to nest; depth, parent
and allow-list are set by ape and cannot be widened from inside a session.
