# How to run ape in the background, beyond a tool's time limit

`ape pipeline`, `ape task` and `ape change` accept `--detach`. The command
starts the run in the background, prints an id, and returns at once:

```bash
$ ape task apex-dev-story --detach --prompt "…"
detached: 20260930-031500-ab12cd
wait:     ape run wait 20260930-031500-ab12cd --cwd /path/to/project
log:      /path/to/project/_output/ape/detached/20260930-031500-ab12cd.log
```

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
  killed, or the machine restarted. `run wait` reports that and exits 1
  rather than waiting forever. The run's own manifest, under
  `_output/ape/tasks/…` and the other run folders, says how far it got.
- **Stopping a detached run:** send the supervisor (`supervisor_pid` in the
  handle) a SIGTERM. It passes the signal on, the run shuts down the way a
  Ctrl-C would, saving `ape change`'s residue and stopping what the skill
  started, and the exit code is recorded.

The handles live under `{output_folder}/ape/detached/`. They are records of
the supervisor, not runs: each run still writes its own manifest where it
always has.
