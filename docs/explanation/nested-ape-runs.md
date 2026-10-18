# When `ape` runs inside a session `ape` started

A model session is one ape launches: `ape task`, `ape prompt`, `ape chat`, `ape change review --run`, and the `ape service` task and prompt jobs, which run those commands. From APEX v1 rc.3 (decisions D61 and D62), ape allows exactly one structured level of nesting under it:

| Depth | What sits there                                                                                                                                                    | May start a model session?                                                       |
| ----- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ | -------------------------------------------------------------------------------- |
| 0     | Anything outside an ape-launched session: a terminal, a Claude Code session ape did not launch, a shell, an `ape script` program, the `ape service` daemon         | Yes, any                                                                         |
| 1     | A session ape launched from depth 0                                                                                                                                | Only when it is ape's own `apex-orchestrator` run, one child at a time, allow-listed skills only |
| 2     | A session an orchestrator started                                                                                                                                  | No                                                                               |

The allow-list is the core skills (`apex-plan`, `apex-change`, `apex-apply`, `apex-review`, `apex-release`) plus the skills a project declares under `tools:`. `ape change review --run` counts as starting `apex-review`. Deterministic commands (`ape status`, `ape change new|check|start|verify|archive`, `ape milestone gate|close`, `ape next add` and the rest) are never limited: skills run them inside sessions all the time, and they start nothing. `ape script` is a conductor and refuses inside any ape session.

A refusal is exit 2 with code `session.depth` (a session that may not start one) or `session.not-allowed` (an orchestrator starting a skill outside its allow-list), both action `stop`. A second child while one runs is exit 3, `session.child-running`, and a second `apex-orchestrator` run in the same project exit 3, `session.orchestrator-running`, both action `retry`.

## How ape knows

Every `claude` session ape spawns carries `APE_SESSION=<kind>/<run-id>` and `APE_DEPTH`; a child also carries `APE_PARENT_RUN` (the orchestrator's run id) and `APE_PARENT_PID` (the ape process running the orchestrator). Claude Code hands its environment to every tool subprocess, so an `ape task` a skill types into the session's Bash tool sees them.

The environment alone is not trusted: a model can run `env -u APE_SESSION ape task …`. So every session ape launches is also a row in the live-session registry, `~/.ape/registry.json` (what `ape sessions` lists), naming the ape process running it, the `claude` it spawned, the skill ape typed, and its depth. A session-starting command walks its own parent processes, and when one of them is a process a row names, it runs inside that session. Its depth is the larger of the two answers. Rows are matched by pid, never by process name, so a stand-in `claude` (a test or eval stub) gets the same answer a real session does. Whether a session is the orchestrator is decided from ape's record of the skill it typed, never from the model.

What remains open, and is documented rather than chased: a model that scrubs its environment and also double-forks into a daemon leaves no ancestor ape knows. On Windows an ancestor that exited may have had its pid reused, so the walk there is best effort.

An `ape service` daemon is depth 0 and cannot see where a request comes from, so from rc.4 (D83) every job carries the caller's stamp, which `ape service submit` takes from the same record: a request without one, or from depth 2, is refused with `session.depth` and nothing runs, and a depth-1 caller's job runs only for a running `apex-orchestrator` run, under the same allow-list and one-child lock, as that orchestrator's child. The stamp is the client's word: a raw NATS publish can claim depth 0, so the NATS credentials remain that boundary.

There is no variable that disables the rule. A switch a session can flip is a switch a session will flip.

## Children die with their parent, and record it

The orchestrator dispatches every skill detached: `ape task <skill> --args <id> --detach`, then `ape run wait <id> --wait 9m` until it stops exiting 75. The detached child's supervisor checks the orchestrator's ape process every two seconds; when it is gone, it stops the child the way `ape run stop` does, and the child's manifest records `termination: {kind: parent_gone}`. Each child's manifest records `depth: 2` and `parent_run`; `ape status --json` lists the live sessions with their parent, and `ape costs run <orchestrator-run>` and the orchestrator's own manifest list its children with `tree_cost_usd` (written for every orchestrator run once it ends, `0` included, so an absent field means not computed). A manifest's `totals` stay its own session's, so a sum over every manifest counts each child once.

## Why nesting was closed before, and what changed

Up to v0.1.x nesting was open: the orchestrator ran as an `ape prompt` session and dispatched `ape task`, `ape pipeline` and `ape change` from inside it. v0.2.0 closed it outright, because the **outer** run could not see the inner one:

- **A foreground call cannot work.** Claude Code's Bash tool times out after about two minutes by default, and ten at most. While a dispatch blocks, the outer run's idle anchor sees no progress and eventually stops itself.
- **A background shell only worked because of polling.** Every poll was a tool call, and so the outer run's proof of life. One long sleep instead, and the outer run was killed with no error naming the cause.
- **Yielding the turn tore the outer run down.** ape derives completion from the `Stop` hook, so a conductor that started an inner run in the background and ended its turn finished its own run and orphaned the inner one.
- **Two runs raced on one working tree.**

D62 keeps what those lessons bought and allows the one level D61 needs. The structure is ape's, not the model's: only the orchestrator nests, its children are detached and re-attached within the Bash tool's limit (exit 75), one runs at a time, each has its own manifest and lifecycle, and none outlives the orchestrator. v0's failures came from models deciding when to nest; depth, parent and allow-list are set by ape and cannot be widened from inside a session.
