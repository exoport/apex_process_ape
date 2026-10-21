# The project event log (`_apex/state/events.jsonl`)

In an APEX v1 project, ape appends one JSON line to `_apex/state/events.jsonl` for every event it
notifies about, and only then runs the project's [`notify:`](config-yaml.md#notify) command.
[`ape status --wait`](status-json.md#ape-status---wait) reads this log. It is not the NATS event
stream; that is [events.md](events.md).

## The file

- `_apex/state/events.jsonl`, created on demand. The state folder's whitelist `.gitignore`
  ignores it, so it is never committed and each checkout has its own.
- One event per line, oldest first.
- `seq` is strictly increasing per project, with no gap. It is taken under an exclusive lock on
  `_apex/state/events.lock`, because several ape processes write at once (a detached verify beside
  the loop).
- At 10 MB the log is renamed `events.1.jsonl`, replacing the one before, and the next line starts
  a new `events.jsonl`. One rolled log is kept, and `seq` carries on from it.
- A line a crash cut short is skipped by readers, and the next line starts on a line of its own.
- A project that is not APEX v1 gets no log.

## A line

Every field is present, `""` or `null` when it does not apply. `skill` is added for an event about
an `ape task` run.

```json
{"seq":42,"at":"2026-10-21T14:52:45Z","event":"run-end","milestone":"M1","change":"add-wave","run":"20261021-145000-a1b2c3d","exit":0,"reason":"","action":"","skill":"apex-apply"}
```

| Field | What it is |
| --- | --- |
| `seq` | The event's number in the project. |
| `at` | When it was logged, RFC 3339, UTC. |
| `event` | One of the events below. |
| `milestone` | The milestone it is about (`M<n>`). For a change, the change's milestone (`""` for a tweak). For a run, the milestone its `--args` names or its change's. |
| `change` | The change it is about. |
| `run` | The run's id: its manifest's `run_id`, else, for a detached run that never wrote one, its handle id. |
| `exit` | `run-end`: the run's exit code. `null` otherwise. |
| `reason` | Why, one line: the blocked reason, the stop message, the termination kind, … |
| `action` | `person`: the phrase the person types, e.g. `approve M3`, `accept T2 of add-wave`, `answer add-wave: <answer>`. `""` otherwise. |
| `skill` | The skill of an `ape task` run; absent otherwise. |

## The events

| `event` | When | Who logs it |
| --- | --- | --- |
| `person` | The project starts waiting on a person act it was not waiting on. One event per new act; an act that is resolved and comes back is new again. | Every v1 command that changes what `ape status` reports, after it runs, and ape's git pre-commit hook, which every commit runs. The set last seen is `_apex/state/person-actions.json`. |
| `gate-green` | A milestone's gate turns green: red, or never run, before. Not for a green that stays green. | `ape milestone gate`, and `ape milestone close` when its run turns it green, after the commit. |
| `milestone-done` | ape closes a milestone, green or accepted red. | `ape milestone close`, after the close commit. |
| `run-end` | An `ape task` run ends, attached or detached. `exit` is its exit code; `reason` its termination kind or stop reason. | The run itself, after its manifest's end. For a detached run, its supervisor, once the handle has the exit. |
| `run-lost` | A detached run's supervisor is gone without recording an exit. Once per run. | The first of `ape status` (with or without `--until`), `ape run wait` and `ape run status` to see it. It creates `<id>.lost` beside the handle. |
| `stop` | An `ape task` run stops on a limit (usage limit, budget). Logged before its `run-end`. | The run. |
| `block` | A change becomes blocked (`ape change block`, verify at the attempts limit, a rebase conflict). | The command that blocked it. |

The person acts that `person` covers are listed in
[`ape status --wait`](status-json.md#ape-status---wait).

## The notifier's environment

`notify:` runs under `sh -c` from the project root, after the line is written, with:

| Variable | Value |
| --- | --- |
| `APE_EVENT` | `event` |
| `APE_SEQ` | `seq`; empty if the line could not be written |
| `APE_MILESTONE` | `milestone` |
| `APE_CHANGE` | `change` |
| `APE_RUN` | `run` |
| `APE_EXIT` | `exit`, empty when `null` |
| `APE_SKILL` | `skill` |
| `APE_REASON` | `reason` |
| `APE_ACTION` | `action` |

It has a 10 s timeout and its output is dropped. A failure, of the notifier or of the log write, is
one warning line on stderr and never changes a command's outcome.

See also: [How to hear from a project](../how-to/hear-from-a-project.md).
