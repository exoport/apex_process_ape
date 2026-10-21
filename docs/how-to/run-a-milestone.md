# How to run a milestone

A milestone is one section of `development/roadmap.md` with a gate: a command that is red until
the milestone's work is done. This page walks one milestone through
`ape milestone new|approve|gate|close`: write the gate, propose it, approve it, let the loop
deliver the changes, and close it. ape writes the milestone's `Status:` line and commits every
step itself, as `apex: <M> …` with the trailer `Milestone: <M>`; no milestone command leaves the
tree dirty. Each verb's full rules are in [cli.md](../reference/cli.md#ape-milestone).

## Who runs what

| Command | Who | Notes |
| --- | --- | --- |
| `ape milestone new` | apex-plan, or you | Proposes. |
| `ape milestone approve <M> --by <who>` | a person | Refused in a model session. |
| `ape milestone approve <M> --accept-green "<reason>" --by <who>` | a person | Re-approval only. |
| `ape milestone gate <M>` | anyone; apex-loop runs it | Records the result. |
| `ape milestone close <M>` | anyone; apex-loop runs it on a green gate | Done only when it exits 0. |
| `ape milestone close <M> --accept-red "<reason>" --by <who>` | a person | Closes on a red gate. |

The person's acts are refused when ape runs under a model (`APE_SESSION` or `CLAUDECODE` set, a
`!` line inside Claude Code included): exit 2, `session.person-only`. Run them from a terminal
outside Claude Code, or type the phrase in your own Claude Code session, in the same turn the
model runs the command: `approve M1`, or `accept red on M1`. ape then records `--by` as your
`git config user.name` with your words. See [What only a person does](../explanation/person-acts.md).

Every milestone command records on the base branch. On a change branch (`change/<id>`), `new`,
`approve`, `gate` and `close` are refused with `branch.change-branch`: `git switch <base>` first.

## 1. Write the gate

The gate is one command, run from the project root with `sh`. It must exist, and run, and be red
when the milestone is approved:

- Put its scripts in `development/gates/<M>-<slug>/`, and helpers shared between gates in
  `development/gates/lib/`. A gate that runs a script elsewhere gets the notice
  `gate.outside-gates` from `ape project check`.
- A script the milestone's work would create cannot be the gate: it does not exist yet. Write a
  gate that exists and fails until the work is done, such as `test -x ./greet || exit 1`, not a
  bare `./greet` (which exits 127).
- Write it for POSIX `sh`: `source` exits 127 under dash. Use `.`.
- Every gate run gets `APE_MILESTONE=<M>` and `APE_MILESTONE_BASE`, the milestone's start (HEAD
  at its first approval). A gate can test what changed since, for example
  `git diff --quiet "$APE_MILESTONE_BASE" HEAD -- docs/ && exit 1`.

Commit the gate. Approval runs it from the working tree, committed or not, but
`ape change start` needs a clean tree (ignored files aside). When apex-plan plans the milestone,
it writes and commits the gate.

## 2. Propose it

```bash
ape milestone new "Greeter" --gate "./development/gates/M1-greeter/gate.sh" --goal "greets a name"
```

```text
M1 — Greeter: proposed (a person approves with `ape milestone approve M1 --by <who>`)
```

This appends `## M1 — Greeter` with `Status: proposed`, the `Gate:` line and a `Goal:` line, and
commits it as `apex: M1 proposed`. `--budget N` adds a budget in USD (list-price equivalent).
Without `--goal` the Goal is a placeholder, and approval refuses the milestone as not well-formed
(`milestone.malformed`, with `roadmap.no-goal`) until you write it.

## 3. Approve it

```bash
ape milestone approve M1 --by "$(git config user.name)"
```

Before recording anything, approve runs the gate once against the current tree, under
`timeouts.gate`, and judges the result:

| Gate run | Result |
| --- | --- |
| exit 0 | Refused: `milestone.gate-green`. A green gate proves nothing the milestone adds. |
| a gate file missing, a shell gate file that fails `bash -n`, exit 126 or 127, a `requires:` entry missing, or a timeout | Refused: `milestone.gate-broken`. |
| any other exit | Approved. The run is recorded as the approval's `self_check`. |

Both refusals have action `replan`: re-plan the gate (apex-plan), then approve again. A refusal
writes nothing, and its message ends with the gate's last output lines:

```text
Error: M2's gate is already green on the current tree (exit 0 in 0.0s at 0cd8425) — a green gate proves nothing the milestone adds: re-plan the gate (apex-plan) so it is red until the milestone's work is done, then approve again
Error: M3's gate cannot run: it exited 127 (a command it runs was not found). Last output (_apex/state/logs/gate-M3-approve.log): sh: 1: greet-tool: not found — ...
```

### The approval record

Approval writes `Status: approved (by <who> <ts>)` into the roadmap and an `approval` record into
`_apex/state/milestones.json`, committed together as `apex: M1 approved`. ape trusts the record,
never the `Status:` line alone. The record holds:

| Field | What it is |
| --- | --- |
| `by`, `at` | Who approved, and when. |
| `gate_cmd` | The `Gate:` command, exactly as written. |
| `gate_files` | The SHA-256 of each script the command runs: an executable word that is a path (`./scripts/e2e.sh`), or the script given to an interpreter (`bash scripts/gate.sh`). Arguments, data files, commands inside `$(…)`, bare command names and absolute paths are not hashed. |
| `gate_files_sha256` | A digest of the gate files. |
| `gate_own_files` | The gate's own files beside its scripts, each with its SHA-256 (below). |
| `base_sha` | HEAD at the first approval, the milestone's start. A re-approval keeps it. It is `APE_MILESTONE_BASE` for every gate run. |
| `self_check` | The approval's red run: `at`, `sha`, `exit`, `secs`, `log`, `output_sha256`. |

`gate_own_files` freezes the files a gate reads, one level deep:

- every file under the milestone's gate folders, `development/gates/<M>-*/`;
- every file under `development/gates/lib/`;
- every file a recorded script sources (`. <path>`, `source <path>`, literal or
  `"$(dirname "$0")/<path>"`);
- every literal relative path a recorded script names under its own folder, when that folder is
  neither the project root nor the development folder (`scripts/checks/m4.checks` beside
  `scripts/m4.sh`).

Never the files a gate checks elsewhere (`README.md`, `docs/`): those are the milestone's work.

From approval on, a change that edits a recorded gate file or own file is refused at verify and
archive with `change.touches-gate` (action `replan`). Only a person or apex-plan changes a gate.

## 4. Deliver the changes

Changes start only under an approved milestone with an approval record. The loop plans, applies,
verifies, reviews and archives them. Start it from `/apex-orchestrator` in your own Claude Code
session, or from a terminal:

```bash
ape task apex-loop --args M1 --detach --output-format json
```

See [The operator and the loop](../explanation/operator-and-loop.md) and
[The change lifecycle](../reference/change-lifecycle.md).

## 5. Run the gate

```bash
ape milestone gate M1
```

The gate is green only when all of these pass:

- the `Gate:` command;
- every scenario check of the whole spec (human-review ones are not run);
- the project's regression command, when `_apex/config.yaml` sets `regression:` (its `milestone`
  tier, else the one command). It only needs to be not newly red: it is judged against the
  earliest baseline any change of the milestone recorded for the same command. With no such
  baseline, red fails the gate.

Each runs under its timeout tier (`timeouts.gate`, `timeouts.check`, `timeouts.regression`), and
`--timeout` sets the max for all three. Files a run leaves outside `development/` are undone and
reported as `stray_files`, with a copy under `_apex/state/strays/`.

The run goes to `_apex/state/logs/gate-<M>.json`, which git ignores and `ape status` shows. When
the result differs from the recorded one (green or red), or is the first, `milestones.json` is
committed as `apex: <M> gate green` or `apex: <M> gate red`. The gate never changes the
milestone's status. Exit 0 is green, exit 1 red.

### The gate changed since approval

`gate` and `close` refuse a gate that differs from the approved one: its command changed, or a
recorded gate file or own file is gone or changed.

```text
Error: M1's gate changed since Ada Example approved it: development/gates/M1-greeter/gate.sh changed — a person reviews the gate and approves it again: `ape milestone approve M1 --by <who>`
```

That is `milestone.gate-changed` (action `person`). `ape project check` reports it too. A person
reviews the gate and approves again, which commits `apex: M1 re-approved` and keeps the
`Status:` line and `base_sha`. A re-approval also runs the red check above. Approving an approved
milestone records it again only when there is a reason to:

- ape holds no record of the approval (`milestone.status-unrecorded`);
- the gate changed;
- the gate runs a file the record does not hold;
- the milestone's changes were blocked in a row (this resets `limits.consecutive_blocks`).

Otherwise approving is a no-op. A file created under the gate folder since the approval is not
drift: the next re-approval records it.

### A fixed gate that is green: `--accept-green`

When the milestone's work is done and a person or apex-plan fixed the gate after it, the fixed
gate is green. A plain re-approval refuses a green gate, and `gate` and `close` refuse the changed
gate. State that case:

```bash
ape milestone approve M1 --accept-green "<why the fixed gate is green>" --by "$(git config user.name)"
ape milestone close M1
```

The green run is recorded as `self_check` beside `accept_green {reason, by, at}`, committed as
`apex: M1 re-approved (gate green: <reason>)`. On a proposed milestone the flag is a usage error
(exit 2, `usage.accept-green`): a first approval's gate must be red. A gate fixed before the work
is done and already green proves nothing: re-plan it instead.

## 6. Close it

```bash
ape milestone close M1
```

```text
M1 gate GREEN in 0.0s at 1801afd: `./development/gates/M1-greeter/gate.sh` + 0 spec scenario check(s)
M1 status: done
gate folder archived: development/gates/M1-greeter/ -> development/archive/milestones/M1/gate/
```

Close refuses while a change of the milestone is in-progress, verified or reviewed
(`milestone.changes-in-flight`; archive or block it first). A tweak in flight does not hold it. It
also refuses without an approval record or with a changed gate, as `gate` does. Otherwise it runs
the gate, and only on exit 0 writes `Status: done (<ts> <sha>)`, committed with `milestones.json`
as `apex: M1 done`. A milestone is done when this command exits 0, never because anyone says so.

In the close commit:

- **The gate folders move.** Each `development/gates/<M>-*/` folder moves, as a rename with every
  file unchanged, to `development/archive/milestones/<M>/gate/` (several folders each into
  `gate/<folder>/`). `development/gates/lib/` stays, and so does a gate outside
  `development/gates/`. `milestones.json` records each move under `gate_archived: [{from, to}]`.
  A `gate/` already there refuses the close before the gate runs (`milestone.gate-archive-exists`).
- **The evidence, with `evidence: archive`.** With `evidence: archive` in `_apex/config.yaml`
  (default off), a green close writes one file per check to
  `development/archive/milestones/<M>/evidence/`: command, exit, duration, output SHA-256 and the
  output, capped at `caps.evidence_log` (default 1 MiB).

### Closing on a red gate

A person can close a milestone whose gate is red, for a release that ships with known defects:

```bash
ape milestone close M3 --accept-red "ships with the known RF drift; fixed in M4" --by "$(git config user.name)"
```

Everything else is checked as without the flag. `milestones.json` records
`accept_red {reason, by, at, gate}`. The `Status:` line reads
`done (accepted red by <who> <ts> <sha>)`, committed as `apex: <M> done (accepted red)`, and
`ape status` shows it. A green gate closes as usual and accepts nothing.

Tagging the release is a person's act, after the close.

## Long runs and exit 75

`approve`, `gate` and `close` run in a detached worker that holds the milestone's run lock,
`_apex/state/runs/<M>.json`, with its output in `_apex/state/runs/<M>.log`. The call waits up to
`--wait` (default 9m, at most 9m30s; `0` returns at once). A run that ends in time prints its
result with its usual exit code.

Otherwise the call exits **75**, still running. **Run the same command again to attach**: it waits
again and prints the result once the run ends, and it never starts a second run.

| Exit and code | Meaning |
| --- | --- |
| 75 | Still running: run the same command again. |
| 3, `run.busy` | Another of these verbs holds the milestone's lock. |
| 1, `run.interrupted` | The run's process is gone, or a signal stopped it. Nothing was recorded. Run it again. |
| 2, `usage.wait` | `--wait` is outside 0–9m30s. |

The run lock records a person's act, so attaching to it needs no new grant. A model session never runs these verbs with `run_in_background`, and never ends a turn
on an exit 75. For runs started with `ape task --detach`, see
[How to detach a run](detach-a-run.md).

## Exit codes

| Command | 0 | 1 | 2 |
| --- | --- | --- | --- |
| `new` | proposed (prints the id) | no `roadmap.md`, or HEAD on a change branch | usage |
| `approve` | approved, or nothing to record | no such milestone, done, malformed, the gate green or broken, a change branch, `session.by-mismatch` | usage, or a model session with no grant |
| `gate` | green | red, no such milestone, no approval record, a changed gate, a change branch | usage |
| `close` | done | red (without `--accept-red`), changes in flight, not approved, no record, a changed gate, an archived `gate/` in the way, a change branch | usage, or `--accept-red` from a model session |

`ape project codes` lists every diagnostic code with its exit status and action.
