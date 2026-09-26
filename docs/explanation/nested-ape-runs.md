# Why `ape` never runs inside a session `ape` started

From v0.2.0, a session-starting command (`ape pipeline`, `ape task`, `ape change`, `ape prompt`, `ape chat` and `ape script`) refuses with exit 2 when it finds itself inside a `claude` session that ape spawned. `ape prompt --agent apex-orchestrator` is refused outright. Project-data commands (`ape story`, `ape sprint`, `ape config`, `ape registry` and the rest) are unaffected: skills run them inside sessions all the time, and they start nothing.

Up to v0.1.x this nesting was supported by design. The orchestrator persona ran as an `ape prompt` session and dispatched `ape task`, `ape pipeline` and `ape change` from inside it. This page records why that path was closed, and what replaced it.

## How ape knows

Every `claude` session ape spawns carries `APE_SESSION=<kind>/<run-id>`, where `<kind>` is `pipeline`, `task`, `change`, `script`, `prompt` or `chat`, and `<run-id>` is that run's id. Claude Code hands its environment to every tool subprocess, so an `ape task` a skill types into the session's Bash tool sees the marker, refuses, and names the owning run:

```
Error: ape task refused: this process is inside the ape session prompt/20260926-120638-929faf6 (APE_SESSION is set), and ape never runs inside an ape-spawned session. Start it from a plain shell or a plain Claude Code session
```

That was measured live: an `ape prompt` session's Bash tool printed its own marker, `ape task` exited 2 with the message above, and `ape config effort` ran normally in the same shell.

The marker is not set by the framework-migration shell runner or by the service daemon, because neither spawns a `claude` session. There is no variable that disables the refusal. A switch a session can flip is a switch a session will flip.

The framework's orchestrator skill checks the same variable and HALTs on activation when it is set.

## Why nesting was closed

Nothing about nesting was broken at the mechanism level. The child got its own transcript, because ape scrubs the parent's `CLAUDE_CODE_*` markers. Hooks could not cross runs, because each run's hook command bakes in its own bridge port. The PATH pin was transitive. What made it fragile was the **outer** run, which could not see the inner one:

- **A foreground call cannot work.** Claude Code's Bash tool times out after about two minutes by default, and ten at most. A dispatch routinely runs longer, and while it blocks, the outer run's idle anchor sees no hook events, no transcript growth and no PTY bytes, so it eventually stops itself.
- **A background shell only worked because of polling.** Every poll was a tool call, and so the outer run's proof of life. Replace the polling with one long sleep and the outer run was killed, with no error naming the cause. On claude 2.1.278 a poll is a `Read` of `…/tasks/<id>.output`, not `BashOutput`, so anything keyed on the tool name misses it.
- **Yielding the turn tore the outer run down.** ape derives completion from the `Stop` hook. A conductor that started an inner run in the background and ended its turn finished its own run, and orphaned the inner one.
- **Two runs raced on one working tree.** The outer run's own dispatches were the likeliest source of the uncommitted work the inner run's pre-flight then refused.

Each of these needed a conductor that behaved exactly right, with the failure silent when it did not. One level of ape removes all four.

## What replaces it

The orchestrator's autonomous mode runs in a **plain** Claude Code session: type `/apex-orchestrator --autonomous -- <request>` into `claude` directly. The orchestrator then runs `ape task`, `ape pipeline` and `ape change`, and each of those is the only ape in its process tree. Each marks its own session, writes its own manifest under `{output_folder}/ape/`, and is governed by its own lifecycle, while the plain session around it has no ape lifecycle to trip over.

## The one exception: the eval's conductor host

The framework eval measures the orchestrator persona itself: whether it routes a defect to the maintenance lane, and whether it halts on an ambiguous scope. That needs ape to host the orchestrator, and `ape prompt --agent apex-orchestrator` is refused. So there is one hidden, eval-only verb, `ape eval conduct`, shaped so it cannot become a second front door:

- it is refused unless `APE_EVAL_HOST=1`, and refused inside an ape session (exit 22 and 23), so it can never nest;
- it types `/apex-orchestrator --autonomous -- <request>` into a PTY session exactly as an operator would type it into a plain one, and that session is the only one ape spawns **without** `APE_SESSION`. The `ape change` or `ape task` the conductor dispatches is therefore the only ape inside it, and marks its own session. The orchestrator's HALT-on-marker check sees an empty variable, as it would in the plain session it stands in for;
- it ends at the first `Stop` with **no background task outstanding**. The conductor backgrounds its dispatch and ends its turn to await it, and Claude Code resumes it with a `<task-notification>` turn when the task finishes. A `Stop` reporting background work is therefore a yield: the host keeps waiting, with the idle timer suspended, while `--max-duration` stays a hard ceiling from the start. This is the opposite of the step gate, which treats a background shell as benign.

Measured live on claude 2.1.283 with a stand-in orchestrator skill. The conductor printed an empty `APE_SESSION`, started `ape task` in the background and ended its turn (`Stop`, `background_tasks: [shell running]`, a yield). It was resumed by the task notification, and its next `Stop` carried `background_tasks: []`, which ended the run with exit 0. The inner task ran to completion.
