# How to tune long-running sessions

Some sessions legitimately run for a long time: a large refactor, a slow
reasoning span, an `apex-loop` run that works a milestone for hours. ape has
two backstops that stop a hung session without cancelling one that is still
working. This page shows how to tune them.

Both `ape task` and `ape prompt` accept the same two flags:

- `--idle-timeout` (default `60m`): stop after this long with no progress.
- `--max-duration` (off by default): a wall-clock ceiling, whatever the
  progress.

## What counts as progress

The idle window is anchored on real progress. Any of these resets it:

- a bridge hook event;
- the active claude **transcript growing**: its size, plus the transcript
  directory's mtime, so a `/clear` session rotation counts as activity;
- on the `ape prompt` path only, **PTY output bytes**.

A transcript whose **mtime moves while its size does not** is not progress.
Claude Code touches a hung session's transcript roughly once an hour without
writing anything; counting those touches would restart the idle clock for
ever on a dead session. They are still counted, and named in the termination
diagnostic.

A session that keeps writing its transcript runs as long as it makes
progress. With no ceiling set, a session showing progress stops only on the
idle window, ape's own limits (an enforced budget, the plan's usage limit,
the APEX v1 loop limits) or a person.

### Which signal carries your run

- **A sub-agent call is anchored by hooks the whole way through.** Sub-agents
  fire `PreToolUse` and `PostToolUse` from inside themselves, each carrying an
  `agent_id`, so a skill that dispatches sub-agents keeps the window fed no
  matter how long one sub-agent takes.
- **A single foreground tool call is bounded by claude, not by ape.** claude's
  Bash tool has a **120-second default timeout**, so one silent command cannot
  outlive the idle window at default settings. It becomes possible only if you
  raise `BASH_DEFAULT_TIMEOUT_MS` / `BASH_MAX_TIMEOUT_MS`. A backgrounded
  command returns at once and fires `PostToolUse`.
- **A loop run waits on each child in short calls.** `apex-loop` dispatches
  every skill as a detached `ape task` and waits with
  `ape run wait <id> --wait 9m` until it stops exiting 75. Each wait is a Bash
  call that returns within ten minutes, so the loop's own hooks keep firing
  while its child works.

**`ape task` does not watch PTY output**; only `ape prompt` does. PTY bytes
mean "the TUI is animating", which a claude waiting on a child that no longer
exists does as well as one doing the work. An idle cancellation on that path
prints `pty n/a` in its diagnostic, which is how to tell *ape stopped
watching* from *claude stopped working*.

## Raise the idle window for silent tools

The idle window trips only on silence across *every* signal. A tool that
produces no transcript growth for a long stretch is indistinguishable from a
hang, so it can still trip the window. Raise it for those:

```bash
ape task apex-apply --args <change-id> --idle-timeout 2h
ape prompt "run the migration" --idle-timeout 90m
```

`--idle-timeout 0` uses the default.

The commands a change runs (its task checks, the regression command, the
milestone gate) have windows of their own, set under `timeouts:` in
`_apex/config.yaml` rather than with these flags. See
[`_apex/config.yaml`](../reference/config-yaml.md).

## Set a hard ceiling

`--max-duration` is an absolute wall-clock cap, independent of progress. It
is off by default. Set one when you want the worst case bounded anyway:

```bash
# bound one loop run at 8 hours
ape task apex-loop --args M3 --max-duration 8h
```

## The ceiling resets on each sub-agent boundary

A ceiling you set **resets on every sub-agent boundary** (a `SubagentStart` or
`SubagentStop`). A sub-agent completing is real progress, a whole unit of work
finished, so the cap starts counting again for the next one. A skill that
works through items one sub-agent at a time is therefore bounded per item, not
for the whole batch.

A loop run's children are separate ape runs, not sub-agents, so they do not
reset its ceiling. Set one on `ape task apex-loop` only as a bound on the
whole milestone. Each child is an `ape task` of its own, with its own
backstops.

A session that spawns no sub-agents sees a flat wall-clock cap from its start.
There is no flag to set for the per-item behaviour: it applies wherever
sub-agents appear. The one gap: a session that spins sub-agents in a noisy
infinite loop does not trip the ceiling, since each boundary resets it, but
the idle window still catches one that goes silent.

## Read a termination diagnostic

When a backstop trips, ape prints which limit fired, whether the child
`claude` process is still alive, and each progress source's age, so you can
tell a real stall from a mis-tuned window. For example:

```text
interactive step idle for 60m1s without progress (window 60m0s): no progress
across any signal (hook none for 60m1s; transcript none for 60m1s; pty n/a);
transcript touched 2 time(s) without growing, last 13m20s ago;
child pid 12345 alive → stopping
```

- `idle for … without progress`: the **idle window** tripped. Raise
  `--idle-timeout`, or find out why every signal went quiet (`pty n/a` means
  the PTY signal is not watched on this path, not that it was silent).
- `exceeded max-duration …`: the **ceiling** you set tripped. Raise
  `--max-duration`, or drop it (`0`, the default).
- `child … alive` or `exited` tells you whether `claude` was still running
  when ape gave up.
- `transcript touched N time(s) without growing`: the session was hung rather
  than quiet. Shown only when it happened.

ape checks every 30 seconds for the first hour of a session, then every 60
seconds, so early stalls are caught quickly while long runs poll cheaply.

## Related

- [How to run a single skill](run-a-single-skill.md)
- [How to run ape in the background](detach-a-run.md)
- [Choosing between `ape chat`, `ape task`, and `ape prompt`](../explanation/chat-task-prompt.md)
- [CLI reference](../reference/cli.md)
