# The orchestrator and the loop

An APEX v1 project is driven by two skills with different jobs. `/apex-orchestrator` is
the orchestrator: the skill you open in your own Claude Code session to ask where the project stands,
turn a request into a milestone or a tweak, and start work. `apex-loop` is the unattended runner:
ape starts it as `ape task apex-loop --args <M>`, and it takes one approved milestone or tweak to
its end without asking anyone. This page explains why they are separate, how ape keeps the loop
bounded (the nesting rule and the relay), and how human-review tasks are handed to a delegate.

In these docs the **operator** is you, the person who runs the project: you approve, answer,
accept, tag and push. The **orchestrator** is the `/apex-orchestrator` skill and its session.
(Framework v1.0 and v1.1 docs called that session "the operator".)

## Two skills, two places

| | `/apex-orchestrator` (the orchestrator) | `apex-loop` (the runner) |
| --- | --- | --- |
| Who starts it | You, by typing `/apex-orchestrator` in `claude` in the project | `ape task apex-loop --args <M\|tweak-id>`, usually started by the orchestrator |
| Where it runs | Your own Claude Code session, on the model you chose | A session ape launched, on `models.apex-loop` (sonnet, effort high, by default) |
| What it does | Explains `ape status`, recommends, starts planning and delivery as detached ape runs, brings a blocked change's question to you | Runs each change through apply, verify, review and archive, then the gate, the close and the release |
| Person acts | Proposes them with the exact words to type; runs one only on your words | Never |
| How many | Any | One per project |

`ape project init` writes the line `Talk to APEX: type /apex-orchestrator in Claude Code.` into
`AGENTS.md`. Both skills are `disable-model-invocation: true`: a model never opens one on its own.

## The orchestrator is your session

`/apex-orchestrator` holds no right ape would not give any terminal. It reads `ape status --json`,
explains the state in plain words, and gives options with one recommendation. Every skill it
starts runs in its own ape session, detached:

```bash
ape task apex-plan --args "<request>" --detach --output-format json # prints {id, wait, log, stdout}
ape task apex-loop --args M2 --detach --output-format json
ape run wait <id> --wait 9m --json                                  # exit 75: run it again
```

When it starts the loop, it arms one waiter for that run, a background `ape run wait`, and tells
you when the run ends. You can keep talking to it meanwhile. The run is detached, so a `/clear`
loses nothing.

Approvals, answers and acceptances are yours. The orchestrator ends a proposal with the words to type,
such as `approve M2` or `accept T3 of add-cache`, and runs the command only after you type them.
[What only a person does](person-acts.md) explains how ape checks that.

ape never starts the orchestrator itself:

- **`ape task apex-orchestrator`** is refused with exit 2, code `skill.renamed`, action `stop`.
  The message points at both `ape task apex-loop --args <M>` and `/apex-orchestrator`. It is not an
  alias, so a stopped run's older resume line fails loudly instead of starting the wrong skill. The
  same refusal covers `ape service submit task` and an `ape script`'s `RunTask`.
- **`ape prompt --agent apex-orchestrator`** is refused too, and so is `--agent apex-loop`.
- **No `models` row.** The orchestrator has none in `_apex/config.yaml`. A `models.apex-orchestrator`
  row in a project made before the rename is read as apex-loop's while there is no apex-loop row,
  and `ape project check` lists the notice `models.operator-row`.
  `ape project operator-interface` moves it (the framework's migration `v1.0.0_seq-07` runs it).

When the orchestrator's session gets long, run `/apex-handoff --peer operator`, then `/clear`,
then paste the one line it prints. `operator` is the orchestrator's handoff value, kept under that
name for compatibility. That line starts with `/apex-orchestrator`, so it loads the orchestrator
again. A running loop does not stop.

## The loop is ape's runner

`apex-loop` conducts and does no work itself. It reads `ape status --json`, takes the first row
that applies, and starts each skill in its own ape session:

- `apex-change` to plan;
- `apex-apply` to build;
- `ape change review <id> --run` for the review;
- `ape change tool` for the project's tool skills;
- `apex-release` when the last milestone is done.

It runs `ape change archive`, `ape milestone gate` and `ape milestone close` itself, each with
`--wait 9m --json`. An exit 75 means still running: it runs the same command again to attach.

The loop stops when:

- `ape milestone close` exits 0 (and the release ran when it was due), or a tweak run alone is
  archived;
- an ape diagnostic has action `stop` or `person`;
- a question only a person can answer comes up, which it records with `ape change block`;
- only changes with tasks awaiting a person are left;
- the same `ape status` state came back three times with no progress.

Before it stops for any reason but the first, it blocks its changes that are still draft or
in-progress (never a queued tweak) with the reason, and switches back to the base branch, so
`ape status` shows why the run ended. Verified and reviewed changes stay as they are. To resume, run the same
`ape task apex-loop --args <M>` again.

ape bounds the loop from outside:

- **One loop per project.** A second `apex-loop` run is exit 3, `session.orchestrator-running`.
- **Consecutive blocks.** A loop session is refused (exit 1, `milestone.blocks-exceeded`) once
  `limits.consecutive_blocks` changes of its milestone (default 2) have blocked in a row. A person
  deals with them and re-approves the milestone, which resets the count. A tweak's run is not
  refused.
- **The budget.** With `limits.budget: enforce` and a `Budget:` line on the milestone, a session
  that would start with the milestone's spend at its budget is refused
  (`milestone.budget-exceeded`), and a running one is stopped when the spend reaches it.
- **Attempts and re-plans** per change (`limits.attempts`, `limits.attempts_total`,
  `limits.replans`) block the change itself.

All of these are in the guarded `limits:` block ([`_apex/config.yaml`](../reference/config-yaml.md#limits)):
an edit to them that no person accepted stops the loop at its next start, verify or archive
([what the guard reads](person-acts.md#what-the-guard-reads)).

## Nesting, as it applies to the loop

The loop is the only session ape lets start another model session. Depth counts only the sessions
ape launched:

- **Depth 0** is your terminal, and also your own Claude Code session, which ape did not launch.
  The orchestrator is at depth 0, so it may start `apex-loop`.
- **Depth 1** is the `apex-loop` session. It may start a child only when it is ape's own loop run,
  one child at a time, and only a skill on its allow-list.
- **Depth 2** is the loop's child. It starts nothing.

The allow-list is:

- the core skills `apex-plan`, `apex-change`, `apex-apply`, `apex-review`, `apex-release` and
  `apex-accept`;
- the `human_review:` delegate skill, when it is not `apex-accept`;
- every skill the `tools:` slots name.

`apex-handoff`, `apex-loop` and `apex-orchestrator` are never on it. `ape change review --run`
counts as starting `apex-review`.

The refusals:

- a session that may not start one is exit 2, `session.depth`;
- a skill off the list is exit 2, `session.not-allowed`;
- a second child while one runs is exit 3, `session.child-running`.

A child records `depth` and `parent_run` in its manifest, and is stopped (termination
`parent_gone`) when the loop's ape process is gone. Deterministic commands (`ape status`,
`ape change verify`, `ape milestone gate`, …) are never limited. ape decides depth from its own
records and the process ancestry, never from the model, and nothing turns the rule off.
[When ape runs inside a session ape started](nested-ape-runs.md) has the mechanism.

## The loop relay

Children start fresh, but the loop session lives as long as its milestone, and its context grows
with it. So ape relays the loop to a fresh session at the one point where nothing is lost:
between two children, when the previous child has ended and the next one gets a new session
anyway.

1. At the loop's next child launch (`ape task <skill>`, `ape change review --run`,
   `ape change tool`, `ape change accept --delegate`), ape reads the loop session's context fill:
   the newest turn's prompt tokens over its model's context window.
2. At or over `limits.context_relay` (default 0.6), the launch is refused with exit 2, code
   `session.relay`, action `stop`. The loop starts nothing, replies and ends.
3. The same `ape task apex-loop` process starts a fresh loop session with the same args, under the
   lock it already holds. The fresh session rebuilds its picture from `ape status --json`, as any
   resumed loop does.

Each relayed session has its own manifest under `_apex/state/runs/tasks/apex-loop/<run>/`, its
own cost and its own children. It records:

- `relay_of`, the run it relays;
- `relay`, the count (1 for the first).

The `ape task` JSON envelope carries `relays` and `relay_of`. While the run lives, its relay
record is `_apex/state/runs/relays/<run>.json`. `ape status --json` shows a live loop's fill and
relays under `sessions[].relay`, and the brief prints it as "context 12 % (relay at 60 %),
relays 0/5".

Past `limits.relays` (default 5), the launch is refused with `limits.relays` (exit 1, action
`person`). The run stops, termination `relays`, with a resume line: a person looks at why the loop
needs that much context.

Some sessions are never relayed:

- **An unknown window.** When the model's context window is unknown, the fill is unknown: no
  relay, said once in the run's output.
- **A loop you typed into your own session.** ape did not launch it, so it has no record.
- **A loop that only waits.** Waiting on a long child costs little context.

On a model with a 1M-token window the relay rarely fires: it bounds a runaway session, not routine
growth.

## Human review and delegates

A change's human-review tasks wait for a person: a reviewed change with such tasks is listed under
`awaiting_human`, the loop never archives it, and the milestone cannot close until the tasks are
accepted. A project can hand them to a skill instead:

```yaml
human_review:
  delegate: apex-accept                  # the default when the block is present
  args: "--task {task} --change {id}"    # the default
  model: sonnet                          # optional, else models.<delegate>
  effort: high                           # optional
```

Delegation is off by default: `ape project init` writes no `human_review:` block. One case is on
without it: the docs-accuracy check of a milestone's docs change, a change whose `change.md` says
`docs: true`. It goes to `apex-accept` while the change's diff touches only the user-facing docs
and the development folder. `human_review: {docs: person}` turns that off.

When a reviewed change lists tasks under `delegable` in `ape status --json`, the loop runs this
for each:

```bash
ape change accept <id> T<n> --delegate --wait 9m --json
```

ape starts the delegate skill in its own detached, read-only session. The pre-tool hook lets it
write only `changes/<id>/assets/accept-*` notes. The delegate records its verdict with
`ape change accept <id> T<n> --verdict pass|fail --reason "<text>"`, which only that session may
run. ape then settles it:

- **A pass** is recorded as an acceptance by `delegate:<skill>` with the run's id, never as a
  person, and the commit carries `Accepted-By: delegate:<skill> <T> run <run-id>`.
- **A fail naming a defect in the change's own diff** (`--defect <path>`) sends the change back to
  in-progress once (`delegate.returned`). The loop applies, verifies, reviews and delegates again.
- **Any other fail** leaves the task waiting for a person (`delegate.fail`), with the delegate's
  reason in `ape status`. This covers a fail with no in-diff defect, a second fail, and a session
  that recorded no verdict.

A session that committed, switched branch or wrote anything else has its verdict voided
(`delegate.voided`).

`human_review:` is a guarded block. `--delegate` refuses (`delegate.unaccepted-config`) while a
commit nobody accepted changed it, so a model cannot name a delegate and accept its own work.

`apex-accept` is also a skill a person can run for a second opinion. Its verdict is then advice,
and the person accepts.
