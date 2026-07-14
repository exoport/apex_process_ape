# How to tune long-running steps

Some steps legitimately run for a long time — a large-context refactor, a
multi-hour tool call, a slow model reasoning span. ape has two backstops that
protect against a hung step without cancelling one that is still working. This
guide shows how to tune them (PLAN-19).

The backstops apply identically to a pipeline stage step (`ape pipeline`), a
single skill (`ape task`), and an unattended session (`ape prompt`); all three
accept the same two flags.

## What changed

Previously a step was cancelled after 60 minutes with **no bridge hook**, and
the anchor watched hooks *only*. A step doing one long silent operation (a
single multi-hour tool call, long reasoning between tool calls) emitted no hook
for over an hour and was killed mid-work even though it was actively
progressing.

Now the idle window is anchored on **real progress**, not just hooks:

- a bridge hook event (as before);
- the active claude **transcript growing** (its size or mtime, plus the
  transcript directory's mtime so a `/clear` session rotation counts as
  activity);
- on the `ape prompt` path, **PTY output bytes**.

Any of these resets the window. **Active steps are no longer cancelled at
60m** — a step that keeps writing its transcript or streaming to the PTY runs
as long as it keeps making progress, up to the hard ceiling below.

## Raise the idle window for pathologically silent tools

The idle window (`--idle-timeout`, default `60m`) only trips on genuine silence
across *every* signal. A tool that produces no transcript growth and no PTY
output for a long stretch — a truly silent long-running subprocess — is
indistinguishable from a hang, so it can still trip the window. Raise it for
those:

```bash
ape pipeline design --idle-timeout 3h
ape task apex-create-prd --idle-timeout 2h --agent apex-agent-pm
ape prompt "run the migration" --idle-timeout 90m
```

`--idle-timeout` is now available on `ape pipeline` too (it previously existed
only internally, so pipelines were stuck at the 60m default with no knob).
`--idle-timeout 0` uses the default.

## Adjust or disable the hard ceiling

The hard ceiling (`--max-duration`, default `3h`) is an absolute wall-clock cap
per step, independent of progress. It bounds a step that stays noisy but never
actually finishes, keeping the worst-case per-step wall clock predictable.

```bash
# raise the ceiling for an exceptionally long step
ape pipeline design --max-duration 8h

# disable the ceiling entirely (rely on the idle window alone)
ape pipeline design --max-duration 0
```

## Reading a termination diagnostic

When a backstop trips, the runner prints which limit fired, whether the child
`claude` process is still alive, and each progress source's age — so you can
tell a real stall from a mis-tuned window at a glance. For example:

```
interactive step idle for 60m1s without progress (window 60m0s): no progress
across any signal (hook none for 60m1s; transcript none for 60m1s; pty n/a);
child pid 12345 alive → stopping
```

- `idle for … without progress` → the **idle window** tripped. Raise
  `--idle-timeout`, or investigate why every signal went quiet (`pty n/a` means
  the PTY signal is not watched on this path, not that it was silent).
- `exceeded max-duration …` → the **hard ceiling** tripped. Raise
  `--max-duration`, or set it to `0` to disable.
- `child … alive` vs `exited` tells you whether `claude` was still running when
  ape gave up.

The poll cadence is 30s for the first hour of a step, then 60s thereafter, so
early stalls are caught quickly while long runs poll cheaply.

## Related

- [Pipeline spec reference § Step completion backstop](../reference/pipeline-spec.md#step-completion-backstop)
- [Choosing between `ape chat`, `ape task`, and `ape prompt`](../explanation/chat-task-prompt.md)
- [CLI reference](../reference/cli.md)
