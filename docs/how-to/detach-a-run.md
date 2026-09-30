# How to run ape in the background, beyond a tool's time limit

`ape pipeline`, `ape task` and `ape change` accept `--detach`. The command
starts the run in the background, prints an id, and returns at once:

```bash
$ ape task apex-dev-story --detach --prompt "…"
detached: 20260930-031500-ab12cd
wait:     ape run wait 20260930-031500-ab12cd --cwd /path/to/project
log:      /path/to/project/_output/ape/detached/20260930-031500-ab12cd.log
stdout:   /path/to/project/_output/ape/detached/20260930-031500-ab12cd.out
```

The run's **stdout** goes to its own file (`.out`) and its **stderr** to the log
(`.log`). So a detached `ape change --output-format json` leaves its JSON envelope
in the `.out` file as one clean document, for whatever routes on it.

Then wait for it:

```bash
ape run wait 20260930-031500-ab12cd --timeout 25m
```

`ape run wait` exits with the run's own exit code, the same one the command
would have returned in the foreground.

## Why it exists

Claude Code 2.1.285 stops a Bash tool's background command after a time
limit: 30 minutes by default, 2 hours at most with a `timeout`. It sends
SIGTERM, then a hard kill about 3 seconds later, and it stops `setsid` and
`nohup` descendants too. A session that runs an `ape pipeline` that way loses
the pipeline at the limit.

A process orphaned at launch is outside that reach, and `--detach` gives you
one. ape starts a supervisor in its own session and exits straight away, so
the supervisor belongs to nothing that is later stopped. The supervisor runs
the command, sends its output to the log, and records how it ended.

## Waiting under a time limit

`--timeout` bounds each wait. When it expires with the run still going,
`ape run wait` exits **75** and leaves the run alone. Wait again:

```bash
until ape run wait "$id" --timeout 25m; [ $? -ne 75 ]; do :; done
```

Each wait stays inside the tool's limit, and the loop ends with the run's own
exit code. No exit code a run itself returns is 75, so the loop cannot
mistake an outcome for "not yet".

## Other outcomes

- `ape run status <id>` prints the handle as JSON: the command, the
  supervisor's and the run's pids, the log, and, once it has ended, the exit
  code. `state` is `running`, `ended, exit N`, or `gone without recording an
  exit`.
- **A supervisor that is gone without recording an exit** means it was
  killed, or the machine restarted. `run wait` reports that and exits
  **76** rather than waiting forever. That's a code of its own, so it's
  never mistaken for a run that exited 1. The run's own manifest, under
  `_output/ape/tasks/…` and the other run folders, says how far it got.
- **`ape run stop <id>`** stops a detached run, then waits up to
  `--timeout` (30 s by default) for the exit to be recorded. The
  supervisor always ends itself right after the run it supervises ends: it
  waits on the run, writes the exit code into the handle, and exits with
  that code.
  - **Linux and macOS:** a graceful stop, as Ctrl-C would. The supervisor
    forwards SIGTERM to the run, so `ape change` saves its residue and the
    commands the skill started are stopped.
  - **Windows:** a hard stop. There is no SIGTERM to forward, and killing
    the supervisor would end it before it records the exit and orphan the
    run, so ape ends the run's own process with `TerminateProcess`. The
    supervisor then records the exit and ends. Nothing gets a chance to
    clean up: no residue is saved, and the skill's commands keep running.
  - It exits 0 once the run has ended, whatever the run's own code;
    `ape run wait` reports that code.
  - It exits 75 if the run hasn't ended in time.
  - **It only ever signals the run's own process.** A pid is reused once its
    process is gone, so the handle's pid alone proves nothing. Before
    signalling, ape checks the process is still this run's:
    - Linux and macOS: its command line must be
      `ape run supervise --handle …/detached/<id>.json`.
    - Windows: it must be an `ape` process created after the handle was
      written.
    - Anything else is refused: nothing is signalled, and it exits 76. `ape
      run wait` makes the same check, so a reused pid reads as a lost run,
      not a running one.

The handles live under `{output_folder}/ape/detached/`. They are records of
the supervisor, not runs: each run still writes its own manifest where it
always has.
