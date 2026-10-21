# How to hear from a project

An APEX v1 project's loop can run for hours, and most of that time nobody needs to look. The times
someone does are when the loop needs a person (a milestone to approve, a question to answer), when
it stops, and when a milestone is done. ape logs each of these as an event in
`_apex/state/events.jsonl` ([the event log](../reference/project-events.md)). This page shows two
ways to hear of them: a notifier that ape runs on every event, and a wait you run yourself.

## Run a command on every event

Set `notify:` in `_apex/config.yaml`. ape runs it with `sh -c` from the project root after each
event's line is in the log. The event is in its environment: `APE_EVENT`, `APE_SEQ`,
`APE_MILESTONE`, `APE_CHANGE`, `APE_RUN`, `APE_EXIT`, `APE_SKILL`, `APE_REASON` and
`APE_ACTION`. For a `person` event, `APE_ACTION` is the phrase the person types, such as
`approve M3`.

The notifier has 10 seconds, and its output is dropped. If it fails, ape prints one warning line
and carries on. The event is in the log either way.

### A desktop notification

```yaml
notify: 'notify-send "ape: $APE_EVENT $APE_MILESTONE $APE_CHANGE" "${APE_ACTION:-$APE_REASON}"'
```

On macOS, use `osascript -e "display notification \"$APE_ACTION\" with title \"ape: $APE_EVENT\""`
in its place.

### Keep the board's status tab current

```yaml
notify: 'ape status --board >/dev/null'
```

The project's [board](use-the-board.md) tab then shows the state as of the last event, for
example a run ending, which no state-changing command refreshes. ape's state-changing commands
already refresh the tab on their own. Run ape by its absolute path here if the shell that runs
the loop may not have it on `PATH`.

### A webhook

```yaml
notify: >-
  curl -fsS -m 8 -X POST https://hooks.example.com/ape
  -H 'Content-Type: application/json'
  -d "{\"text\": \"$APE_EVENT $APE_MILESTONE $APE_CHANGE: ${APE_ACTION:-$APE_REASON}\"}"
```

`-m 8` keeps curl inside the notifier's 10 seconds. `-f` makes an HTTP error a failure, so ape
warns about it. Values with quotes in them break this hand-written JSON. When that matters, use a
script that encodes it, for example `notify: ./scripts/notify.sh`, which builds the body with
`jq -n --arg e "$APE_EVENT" …`.

`notify:` is one command. To do several things, point it at a script.

## Wait for the project yourself

`ape status --wait` waits for a condition and returns when it holds:

```bash
ape status --wait 9m --until person,stopped --json
```

- `person`: the project waits on a person act.
- `stopped`: nothing of the project is running.
- `done`: every milestone is done.
- `blocked`: a change is blocked.
- `event`: any event at all.

When a condition already holds, the call returns at once with exit 0. Otherwise it waits, at most
`--wait` (at most 9m30s, so that one call stays inside Claude Code's 10-minute Bash limit). It exits
75 when the time runs out.

Every exit prints the seq to pass next as `--since`: the line `seq: <n>`, or `"seq"` in JSON.
Pass it on the next call, and an event logged between the two calls is returned instead of
missed:

```bash
since=0
while :; do
  out=$(ape status --wait 9m --until person,stopped --since "$since" --json)
  code=$?
  since=$(printf '%s' "$out" | jq -r .seq)
  [ "$code" -eq 75 ] && continue      # nothing yet: wait again
  [ "$code" -ne 0 ] && break          # a usage error
  printf '%s' "$out" | jq -r '.wait.condition, (.wait.person_actions[].action)'
  break
done
```

`.wait.person_actions` lists what the project waits on, each with the phrase the person types
(`approve M3`) and why. A person types that phrase in their own Claude Code session, or runs the
command it stands for in a terminal. A script or model that relays it must never run it in the
person's place.

`person`, `stopped`, `done` and `blocked` are levels. While a milestone stays unapproved, each
call with `person` returns at once, and while nothing runs, each call with `stopped` does. Once
you have relayed a person act, wait with `--until event --since <seq>` for the next thing logged,
and read `ape status` to see whether the person has acted. A person act clears its wait without
logging an event of its own.

## Wait for a project on another machine

Run the same wait over ssh. Name ape by its absolute path: a non-interactive ssh shell often does
not read the profile that puts it on `PATH`.

```bash
ssh build-host 'cd /srv/projects/greeter && /home/me/.local/bin/ape status --wait 9m --until person,stopped --since 41 --json'
```

The exit code and the JSON come back through ssh unchanged, so the loop above works as it is with
`ssh build-host '…'` around the `ape` command. Find the path with `command -v ape` on that machine.

## See also

- [The project event log](../reference/project-events.md): every event and field.
- [`ape status --wait`](../reference/status-json.md#ape-status---wait): the conditions and the
  JSON.
- [`notify:`](../reference/config-yaml.md#notify) in `_apex/config.yaml`.
- [How to run ape in the background](detach-a-run.md): `ape run wait` for one detached run.
