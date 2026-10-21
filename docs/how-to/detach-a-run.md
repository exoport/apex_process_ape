# How to run ape in the background

A model session can run for hours, and the shell that starts it may not live
that long: Claude Code's Bash tool cuts a call at 10 minutes. This page shows
how to start a session that survives its shell, how to wait for it in calls
that each stay inside that limit, and how the change and milestone verbs do
the same thing on their own.

## Start a detached session

Of the commands that start a model session, `ape task` takes `--detach`. It
starts the run in the background, prints an id, and returns at once:

```bash
$ ape task apex-loop --args M1 --detach
detached: 20261021-031500-ab12cd
wait:     ape run wait 20261021-031500-ab12cd --cwd /path/to/project
log:      /path/to/project/_apex/state/runs/detached/20261021-031500-ab12cd.log
stdout:   /path/to/project/_apex/state/runs/detached/20261021-031500-ab12cd.out
```

With `--output-format json` it prints `{"id", "wait", "log", "stdout"}`
instead.

The run's stdout goes to its own file (`.out`) and its stderr to the log
(`.log`), so a detached `ape task --output-format json` leaves its result
envelope in the `.out` file as one clean document.

ape starts a supervisor in its own session and exits straight away, so the
supervisor belongs to nothing that is later stopped. The supervisor runs the
command, sends its output to the log, and records how it ended.

## Wait for it

```bash
ape run wait <id> --wait 9m --json
```

`ape run wait` exits with the run's own exit code, the same one the command
would have returned in the foreground. `--wait` bounds one call. When it
expires with the run still going, `ape run wait` exits **75** and leaves the
run alone. Wait again:

```bash
until ape run wait "$id" --wait 9m; [ $? -ne 75 ]; do :; done
```

Each wait stays inside the tool's limit, and the loop ends with the run's own
exit code. `--wait 0` waits until the run ends. `--timeout` is an older name
for `--wait`.

`--json` prints one object: `id`, `state` (`running`, `ended` or `lost`),
`exit_code` (null until it ends), `log`, `stdout`, `run_id` and `run_dir`
(the run's own record, once it has one), `depth` and `parent_run`,
`stop_reason`, and `result`. `result` is the session's last assistant
message, read from its transcript, at most 2048 bytes (`result_truncated:
true` when cut), and absent when there is no transcript to read.

This is how the `apex-loop` run dispatches every skill:

```text
ape task <skill> --args <id> --detach --output-format json
ape run wait <id> --wait 9m --json      # again while it exits 75
```

## Check on it, or stop it

- `ape run status <id>` prints the handle: the command, the supervisor's and
  the run's pids, the log and stdout files, and, once it has ended, the exit
  code. `state` is `running`, `ended, exit N`, or `gone without recording an
  exit`. `--json` prints the object `ape run wait --json` does, and exits 0.
- **A supervisor that is gone without recording an exit** means it was
  killed, or the machine restarted. `ape run wait` reports that and exits
  **76** rather than waiting forever. It does not mean the run did nothing:
  the ape commands its session ran may have committed state, so read
  `ape status` before you start it again.
- **`ape run stop <id>`** stops a detached run, then waits up to `--timeout`
  (30 s by default) for the exit to be recorded.
  - **Linux and macOS:** a graceful stop, as Ctrl-C would. The supervisor
    forwards SIGTERM to the run, and the commands the skill started are
    stopped.
  - **Windows:** a hard stop. There is no SIGTERM to forward, so ape ends the
    run's own process. Nothing gets a chance to clean up, and the skill's
    commands keep running.
  - It exits 0 once the run has ended, whatever the run's own code
    (`ape run wait` reports that code), and 75 if the run has not ended in
    time.
  - It only ever signals the run's own process. A pid is reused once its
    process is gone, so ape first checks the process is still this run's: on
    Linux and macOS its command line must be
    `ape run supervise --handle …/<id>.json`; on Windows it must be an `ape`
    process created after the handle was written. Anything else is refused
    with exit 76, and nothing is signalled.

A child the loop started is stopped when the loop's ape process is gone. Its
handle records `stop_reason: parent_gone`, and its manifest records
termination `parent_gone`.

## Where the handles live

In an APEX v1 project the handles are under `_apex/state/runs/detached/`:
`<id>.json`, `<id>.log` and `<id>.out`. Outside one they are under
`{output_folder}/ape/detached/`. A handle is the supervisor's record, not a
run: the run still writes its own manifest under `tasks/`. See
[Run artefacts](run-artefacts.md).

## The change and milestone verbs run detached on their own

The verbs that run checks or a session do not take `--detach`. They start
their own detached run and wait for it:

- `ape change start`, `verify`, `archive`
- `ape change review --run`, `ape change tool`, `ape change accept --delegate`
- `ape milestone approve`, `gate`, `close`

Each call waits up to `--wait` (default `9m`). A run that ends in time is
printed as usual, with the same exit code and `--json` shape. Otherwise the
call exits **75** and says the run is still running. To attach, run the same
command again:

```bash
ape change verify <change-id> --wait 9m --json
# exit 75: run the same line again
ape change verify <change-id> --wait 9m --json
```

The second call waits again and prints the run's result once it ends. It
never starts a second run. The sessions that `ape change review --run`,
`ape change tool` and `ape change accept --delegate` start are ordinary
`ape task` runs, each with its own manifest.

`ape change start`, `verify` and `archive` and `ape milestone approve`, `gate`
and `close` accept a `--wait` of at most `9m30s`. With `--json` their 75
prints `{"running": true, "verb", "scope", "since", "log", "pid",
"log_tail"}`, where `log_tail` holds the lines the run logged since the last
call. They take the run lock of their change or milestone,
`_apex/state/runs/<id|M>.json`, and log to `_apex/state/runs/<id|M>.log`.
The lock gives two answers:

- **Another of these verbs on the same change or milestone**, while a run
  holds the lock, is refused with
  `run.busy` (exit 3, action retry). The message names the running verb and
  since when. Wait for it, then run yours.
- **A lock whose process is gone** (killed, or the machine restarted) is
  removed and reported as `run.interrupted` (exit 1, action retry). A run
  stopped by SIGTERM or SIGINT kills its checks, records nothing (no attempt,
  no state commit, no gate run) and reports `run.interrupted` too. Run the
  same command again.

A `--wait` outside 0 to 9m30s is a usage error, `usage.wait` (exit 2).

In a Claude Code session, never run these with `run_in_background`, and never
end a turn on an exit 75: run the same command again.

A person act such as `ape milestone approve` attaches only to a run of that
same command line; see `ape milestone approve --help`.

## Related

- [How to run a single skill](run-a-single-skill.md)
- [When ape runs inside a session ape started](../explanation/nested-ape-runs.md)
- [CLI reference: `ape run wait`](../reference/cli.md#ape-run-wait)
