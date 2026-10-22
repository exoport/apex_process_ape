# The change lifecycle

A change is `development/changes/<id>/change.md`, written by a model or a person, plus
`state.json`, written only by ape. This page lists a change's states, every `ape change` verb with
what it needs and what it does, and the rules the verbs enforce: the branch per change, the squash
archive, the tweak lane, the catch-up onto a moved base, the checks, and the loop limits. Each
verb's full rules are in [cli.md](cli.md#ape-change), the configuration keys in
[`_apex/config.yaml`](config-yaml.md), and the codes in `ape project codes`.

## States

| State | Meaning |
| --- | --- |
| `draft` | Written by `ape change new`, planned on the base branch. No branch yet. |
| `in-progress` | Started on its branch `change/<id>`. The work, and every red verify, happen here. |
| `verified` | Every command check passed at a sha. |
| `reviewed` | A separate review session passed the verified work. |
| `archived` | Squash-merged into the base branch, folder moved to the archive. |
| `blocked` | Stopped, with a reason `ape status` shows. Takes no part in start's conflict check. |
| `answered` | A person answered what blocked it. The loop re-plans it and starts it again. |
| `abandoned` | Moved to the archive with no delta merged. The id stays taken. |

```text
new → draft ──start──▶ in-progress ──verify green──▶ verified ──review pass──▶ reviewed ──archive──▶ archived
                            ▲   │                        │                         │
                            └───┴── verify red ◀─────────┴──── review block ◀──────┘
any status but archived ──block──▶ blocked ──start──▶ in-progress
blocked ──answer (a person)──▶ answered ──re-plan, start──▶ in-progress
any status but archived ──abandon──▶ abandoned (moved to the archive, no delta merged)
```

A status is a command ape ran at a sha: models never set one. Every status change is committed by
ape as `state.json` alone, with the subject `apex: <id> <status>` and the trailer
`Change: <id>`. Skills never stage `state.json`.

## The verbs

"Who" is who runs the verb in the framework's skills. A person may run any verb from a terminal.
Rows marked **person** are refused in a model session (exit 2, `session.person-only`) unless the
person typed the phrase in their own Claude Code session that turn. See
[What only a person does](../explanation/person-acts.md).

| Verb | Needs | Does | Who |
| --- | --- | --- | --- |
| `new <id> --milestone <M>\|none --touches a,b [--paths globs] [--type t]` | HEAD off every change branch; a free id; `--type` with `--milestone none` | Writes the `change.md` skeleton and `state.json` (`draft`), commits `apex: <id> draft` | apex-change |
| `check <id>` | — | Validates `change.md`: shape, caps, the delta against the specs, a check on every task. Writes nothing. | apex-change; anyone |
| `approve <id> --by <who>` | A tweak, green `check`, not started | Records `approved {by, at, plan_digest}` on the base branch, `apex: <id> approved` | **person** (`approve <tweak-id>`) |
| `start <id>` | Green `check`; the milestone approved with a record, or the tweak approved for its current plan; a clean tree; no requirement conflict; the base not behind its upstream | Creates and checks out `change/<id>`, runs the baseline, freezes the plan, sets `in-progress` | apex-loop, apex-apply |
| `verify <id>` | HEAD on `change/<id>`; no uncommitted work outside `development/`; the plan unchanged | Runs the task checks and the effective spec. All green: `verified`. Any red: an attempt. | apex-apply, apex-loop |
| `review <id> --verdict pass\|block --findings <file>` | HEAD on `change/<id>`; for pass, a green verify the work has not moved since | pass: `reviewed`. block: back to `in-progress`. | the review session (apex-review) |
| `review <id> --run` | HEAD on `change/<id>`; a verified change, or a reviewed one whose review is stale (it is verified again first) | Starts `ape task apex-review --args <id>` in its own read-only session, which records the verdict | apex-loop |
| `accept <id> T<n> --by <who> [--evidence …]` | A human-review task; the change started, reviewed or archived | Records the acceptance (in flight, on the base branch, or in the archived `state.json`) | **person** (`accept T<n> of <id>`) |
| `accept <id> T<n> --delegate` | A `human_review:` delegate configured, or a docs change; the task awaiting a person | Starts the delegate (apex-accept) in its own session; on pass ape records the acceptance by `delegate:<skill>` | apex-loop |
| `accept <id> T<n> --verdict pass\|fail --reason "<text>"` | Inside the session `--delegate` started for that task | Writes the delegate's verdict file; ape records the outcome | the delegate session |
| `block <id> --reason "<text>"` | Not archived | Sets `blocked`; counts toward the milestone's consecutive blocks | apex-change, apex-apply, apex-loop |
| `answer <id> --text "<answer>" --by <who>` | A blocked change | Sets `answered`, records `answer {by, at, text}`; restarts the `replans` and `attempts_total` counts | **person** (`answer <id>: <answer>`) |
| `abandon <id> --reason "<text>"` | Not archived; a clean tree | Moves the folder to the archive as `abandoned`, merges no delta, deletes the branch | apex-loop |
| `archive <id>` | `reviewed`; the review not stale; every human-review task accepted; the base not behind its upstream | Catches up, re-verifies when needed, applies the delta, squash-merges | apex-loop |
| `archive <id> --allow-pending-human` | As above, with human-review tasks unaccepted | Archives anyway, recorded in `state.json` | **person** (`allow pending human on <id>`) |
| `diff <id>` | — | Prints the specs as archive would leave them, a unified diff per capability. Writes nothing. | apex-review, apex-accept; anyone |
| `tool <id> <name> [--task T<n>]` | A `tools.design` or `tools.review` entry in `_apex/config.yaml` | Runs the entry's skill for the change in its own session | apex-loop |

`check`, `diff`, `approve`, `accept`, `block`, `answer` and `abandon` find a change wherever its
records live, from any branch. They commit there without a checkout when HEAD is elsewhere: HEAD,
the index and the working tree stay as they were. They are refused when that branch is checked
out in another worktree (`git.base-checked-out`).

`start`, `verify` and `archive` run in a detached worker, and `review --run`, `accept --delegate`
and `tool` start a session. All of them wait up to `--wait` (default 9m) and exit 75 while still
running: run the same command again to attach. `start`, `verify`, `archive` and `review --run`
refuse a `--wait` above 9m30s (`usage.wait`); `tool` and `accept --delegate` take any value. See
[How to run a milestone](../how-to/run-a-milestone.md#long-runs-and-exit-75).

## Branch per change

- `start` creates `change/<id>` from the branch HEAD is on, recorded as `base_branch` with
  `base_sha`, and checks it out. A start on a change whose branch exists checks it out again.
- The work commits and ape's status commits land on `change/<id>`. `verify` and `review` refuse
  elsewhere (`change.wrong-branch`: `git switch change/<id>`). `start` and `archive` switch to it
  themselves.
- `new`, and the milestone and `decision accept` records, need HEAD off every change branch
  (`branch.change-branch`).
- A change's status is read from its own branch, so `ape status` on the base branch shows every
  change's real status.
- The model never rebases, merges or cherry-picks on a change branch: `ape hook pre-tool` denies
  it. ape does the rebasing.

## The squash archive

`ape change archive` puts exactly **one commit** on the base branch per change:

1. It catches the change up with a moved base (below), and re-runs verify when the work moved since
   it or the verify is stale. A red re-verify sends the change back to `in-progress` and counts as
   an attempt.
2. It applies the delta to `development/specs/`: ADDED appends, MODIFIED replaces, REMOVED deletes;
   a new capability gets `specs/<cap>/spec.md`. Any conflict exits 3, and nothing reaches the base
   branch.
3. It switches to the base branch, runs `git merge --squash change/<id>`, writes the specs, and
   moves the folder unchanged to `development/archive/changes/<YYYY-MM-DD>-<id>/`. With
   `evidence: archive` it also writes one evidence file per check of the final verify. All of
   this is one commit.
4. It keeps the branch tip as `refs/apex/archive/<id>` (recorded as `tip_ref` in the archived
   `state.json`), so the history behind the squash survives `git gc`, and deletes `change/<id>`.
   The ref is local: ape never pushes it.

The commit's subject comes from `commit.subject` in `_apex/config.yaml`. The default is
`{type}: {title}`, from the change's `type:` and `# title`; `{id}` is also available. The body
lists the branch commits' own subjects (not ape's `apex:` ones), their trailers but `Change:`, and
then `Change: <id>`. A subject that would start `apex:` is refused at `change check`, `start` and
`archive` (`change.subject-reserved`): that prefix marks ape's own records
([What the guard reads](../explanation/person-acts.md#what-the-guard-reads)). For example:

```text
feat: Add farewell

- feat: --bye

Change: add-farewell
```

The project's git hooks run on this commit (`commit.hooks: run`, the default; `skip` opts out). A
hook that refuses rolls archive back (`archive.hook-failed`). If anything fails on the base branch,
it is reset to where it was and `change/<id>` checked out again. A crash mid-archive leaves
`_apex/state/archive-journal.json`. The next v1 command that writes rolls it back and exits 1 with
`archive.interrupted`: archive again.

`abandon` also moves the folder to the archive and deletes the branch, but merges nothing: the
branch tip is kept as `refs/apex/abandoned/<id>`, and none of the change's work reaches the base.

## The tweak lane

A change with `milestone: none` is a tweak, a person's request outside the roadmap:

```bash
ape change new fix-login-redirect --milestone none --type fix --touches auth
```

- `--type` is required (`usage.type`): `fix` for a defect, `feat` for a small feature, or another
  of `commit.types` (default `feat`, `fix`, `perf`, `refactor`, `docs`, `test`, `build`, `ci`,
  `chore`). A milestone's change defaults to `feat`.
- A person approves it with `ape change approve <id> --by <who>` on the base branch, in place of
  an approved milestone. The approval is of the plan as it is: a plan changed since is refused at
  start until approved again.
- It counts toward no milestone gate, regression baseline, spend or budget.
- Tweaks are a queue: planned and approved between runs, never overlapping one. An approved tweak
  runs ahead of milestone work at the loop's next change boundary, or alone with
  `ape task apex-loop --args <id>`. `ape status` lists approved tweaks first.

## Catching up with a moved base

When the base branch has moved past `base_sha` (a tweak or another change landed), `start` (on a
change that has its branch), `verify` and `archive` first catch the change up:

1. The tree must be clean. Uncommitted files in the change's own folder are committed on its
   branch; anything else is `tree.dirty`.
2. ape runs `git rebase <base_branch>` on `change/<id>`.
3. It re-runs the baseline at the **new base tip**, never at the rebased HEAD, and marks the last
   verify stale. A verified or reviewed change goes back to `in-progress` (archive keeps it
   `reviewed`). It commits `apex: <id> rebased onto <base>`.

A conflict aborts the rebase. ape keeps the old tip as `refs/apex/pre-rebase/<id>/<n>`, recreates
the branch at the base tip, and applies the branch's net diff without committing. That leaves
the conflict markers in the tree with HEAD on `change/<id>`: exit 3, `change.conflicted`, with the
files listed. Nothing reaches the base. apex-apply resolves them, runs `git add`, and commits with
the trailer `Change: <id>`. Verify refuses until no unmerged path or marker remains
(`change.unresolved-conflict`), and the review runs again. When apex-apply cannot resolve them, it
blocks the change and the loop re-plans it.

`start` and `archive` also refuse while the base branch is behind its upstream
(`base.behind-upstream`: pull). ape compares with the local remote-tracking ref and never fetches.

## Checks

### Where checks come from

- **Task lines** in `## Tasks`: `- T<n> <what> — check: <command>`, or `— check: human-review`. A
  check meant to pass before the change is `- T<n> <what> — regression: <command>`. A design-tool
  task names its tool: `- T<n> <what> — tool: <name> — check: …`.
- **Scenario checks** of the effective spec: the spec as it will be once the change is archived,
  for the capabilities in `touches:`. Every scenario carries exactly one
  `- check: <command> | human-review`. Verify runs those, plus any other scenario whose check is
  identical to a task's. The milestone gate runs the whole spec.
- **The regression command**, when `_apex/config.yaml` sets `regression:`.

Checks run from the project root with `sh -c`, output under `_apex/state/logs/<id>/`.
`human-review` checks never run: they wait for an acceptance.

### Live checks

A check that needs something live, such as a service, a network or a long end-to-end run, is
written `- check (live): <command>` on a scenario, or `— check (live): <command>` on a task.
`live` is the only kind. Any other, live on `human-review`, or a kind on `regression:` is the
finding `check.bad-kind` (action replan).

| Run | Live checks it runs |
| --- | --- |
| `verify` (and the baseline at `start`) | The change's own: its live tasks and the live scenarios of its delta. Also a live scenario red at the last gate of any milestone. The rest are counted as `live_skipped`. |
| `milestone gate`, `Gate:` command green | All of them. |
| `milestone gate`, `Gate:` command red | None, and no regression command: the gate is red already. JSON `run.live_skipped: N` and `run.regression.skipped: "gate-red"`. |
| `milestone close` | All of them, whatever the `Gate:` command did. |
| `milestone gate --spec-only` | None (`live_skipped`). |

`ape status` lists what was skipped and why. A live check's evidence written under `development/`
is never undone as a stray file.

### Green before the work: vacuous checks

At `start` ape runs every check once as the baseline. When the change has an ADDED or MODIFIED
delta, a task check that already passes before any work proves nothing: `start` refuses with
`change.vacuous-check` and leaves no branch. Rewrite the check to fail until the work is done, or
mark it `— regression:`. Checks of the change that passed at baseline are listed as
`green_at_baseline` for the review.

### Ran nothing: vacuous and unrun

| Result | When | Recorded as |
| --- | --- | --- |
| vacuous | Exit 0, but the output says nothing ran (`checks.vacuous_output`; default "no tests to run", "running 0 tests", "Ran 0 tests", "0 passing", "collected 0 items"; `[]` turns it off) | red, reason `vacuous`, exit -2 |
| unrun | A `requires:` entry is missing: the check mentions `$NAME` and `NAME` is unset (`NAME: env`), or runs `NAME` and it is not on `PATH` (`NAME: cmd`) | not run; red, reason `unrun`, exit -3 |

Vacuous and unrun checks are not retried.

### Timeouts

Each command runs under its tier's `timeouts:` in `_apex/config.yaml`, a Go duration (its max) or
`{idle, max}`:

| Tier | Default idle | Default max | Hard ceiling |
| --- | --- | --- | --- |
| `check` | 15m | 2h | 4h |
| `regression` | 30m | 2h | 8h |
| `gate` | 30m | 4h | 12h |

A command that makes no progress (no output, no CPU time in its process group) for `idle`, or still
runs at `max`, is killed with its process group: red, exit 124, reason `idle` or `ceiling`. It is
never retried. A command's `--timeout` flag sets every tier's max, capped at the ceiling.

### The regression command and its baselines

`regression:` is one command, or two tiers: `regression: {change: <cmd>, milestone: <cmd>}`. The
change tier runs at start and verify; the milestone tier (else the change tier) runs at the gate.

- At `start` the regression command runs as part of the baseline, recorded in `state.json` as
  `regression_baseline` (cmd, exit, output SHA-256, sha, and the failing tests it names). A red
  baseline is recorded and does not block start.
- At `verify` it fails only when it is **newly red**: green at baseline and red now, or a test fails
  now that did not fail at baseline (`new_failures`). Red at both is `pre_existing_red`, which is
  reported, not failed, for the planner to roadmap. With no baseline for the command, it fails;
  re-starting the change records one.
- Failing tests are read by name from `go test`, gotestsum, pytest, cargo, jest, and flutter or
  dart test output.
- A change whose diff touches only user-facing docs (`release.docs`, `docs.changelog`,
  `docs.user_docs`) and `development/` skips the regression command, and only that. Paths under
  `docs.tested` are never docs-only.
- At the milestone gate the command is judged against the earliest baseline any change of the
  milestone recorded for it. A tweak never supplies one. When the milestone tier differs from the
  change tier, the milestone's first change to start also runs the milestone tier once and records
  it as `milestone_regression_baseline`, so red then and red now at the gate is
  `pre_existing_red` (W2).
- `ape project check` notices `regression.slow` (action replan) when `regression:` is a single
  command and its last recorded run took longer than `checks.slow_regression` (default 5m): every
  start and verify pays for the whole suite, so split it into tiers.

### What the checks cost

The gate records each scenario check's run time in `_apex/state/logs/gate-<M>.json`
(`scenario_secs`, kept out of `milestones.json`, the gate's output and `ape status`).
`ape project check` reads the newest one:

- `checks.duplicate-run` (notice, action replan): two or more scenario checks start with the same
  command, up to the first top-level `&&` or `|`, differ after it, and each ran over a minute at
  the last gate. ape runs a shared command once only when the whole check is the same, so the gate
  runs that command once per check. The notice names the scenarios and their time per gate. Give
  the command one scenario and have the others check what it leaves under `development/`, or merge
  the scenarios. ape memoizes nothing. A shared command in a live check is noticed whatever its
  time.
- `check.live-suggested` (notice, action replan): a scenario check that is not live ran over a
  minute at the last gate. Every verify touching its capability runs it, so mark it
  `check (live):` if it needs something live.

### Flaky checks

With `retries: <n>` (0–5, default 0), a red check runs again up to n times. A pass on a retry is
green, recorded with `"flaky": true` and `tries`, and listed by `ape status`.

### Stray files

What a check leaves in the tree outside `development/` (files it created, tracked files it changed)
is undone after it and reported as `stray_files`. A copy of each, as the check left it, is kept
under `_apex/state/strays/` (`strays_kept`). Files that were there before the check ran are never
touched.

### Advise files and unmatched tools

A change that names [`tools.advise`](config-yaml.md#toolsadvise) entries in its `advise:`
frontmatter has their files hashed at each verify. JSON `advise` lists them as `ape status` does,
and `state.json`'s `verify.advise` records `{name, skill, file, bytes, sha256}`, with no path. An
expert updated in the middle of a change shows as a new sha256.

A `tools:` entry with `paths:` that a file the change's commits touched matches, or with
`touches:` the change names, and that the change does not list, is the notice `tools.unmatched`
(JSON `notices`). It never changes the outcome. `ape change check` gives the same notice against
the change's planned `paths:`.

## Limits

All under `limits:` in `_apex/config.yaml`:

| Key | Default | What happens at the limit | Code, action |
| --- | --- | --- | --- |
| `attempts` | 5 | The red verify that reaches it blocks the change. Counted since the last start or re-start, in `_apex/state/attempts.json`. Re-plan, then `start` resumes with a fresh count. | `change.attempts-exceeded`, replan |
| `attempts_total` | 15 | Red verifies across all starts since a person last answered. The change is blocked: re-planning did not converge. | `change.attempts-total-exceeded`, person |
| `replans` | 3 | A re-plan past it since a person last answered blocks the change instead. | `change.replans-exceeded`, person |
| `consecutive_blocks` | 2 | Blocks of one milestone's changes in a row, kept in `milestones.json`. At the cap, `change new` and `change start` of the milestone, and `ape task apex-loop` for it, refuse. A person deals with the blocked changes and re-approves the milestone, which resets the count. An archived change also resets it. | `milestone.blocks-exceeded`, stop |

A red verify of an `in-progress` change commits nothing: it keeps its last verify record, and the
attempt is counted in the ignored `attempts.json`. `retries:` is not a limit: it re-runs a red check
within one verify.

A verb that leaves the change blocked (`block`, verify at its attempts limit, a rebase conflict)
logs `block` in the [project event log](project-events.md), then runs the optional `notify:`
command with `APE_EVENT=block`, `APE_CHANGE=<id>`, `APE_MILESTONE` and `APE_REASON=<reason>`, best
effort. Every verb that changes what `ape status` reports also logs a `person` event for each
person act the project newly waits on, such as `answer <id>: <answer>` for a blocked change.

## Refusals that protect the records

| Code | When | Action |
| --- | --- | --- |
| `change.touches-gate` | The change's commits since `base_sha` edit a gate a person approved: a recorded gate file or own file of its milestone (for a tweak, of any approved milestone). Refused by verify before any check runs, and by archive before it rebases. Restore the files on the branch, or reply `gate defect: <evidence>`. | replan |
| `change.plan-drift` | `change.md` differs from the plan frozen at start, outside `## Why` and `## Notes`. Re-start to re-baseline, or restore the plan. | apply |
| `change.provenance` | Since the v1 epoch, a commit wrote `development/specs/**` outside an archive squash (`spec.edited`), or a `state.json` outside an `apex:` commit or a squash (`state.forged`). Also refused while a guarded block of `_apex/config.yaml` changed in a commit no person accepted (`config.changed`). A person reverts the commit, or accepts the config change with `ape project accept-config --by <who>`. | person |
| `review.stale` | Archive: the work changed on `change/<id>` since the review. `ape change review <id> --run` verifies it again, then reviews it. | review |

## Exit codes

Every `ape change` verb: 0 ok · 1 a check failed (one-line remediation) · 2 usage, or a person's
act from a model session · 3 conflict: another change claims the same requirement, the delta no
longer applies, or the change does not rebase onto its moved base · 75 still running (the detached
and session verbs).

`--json` failures carry the codes and actions described in
[`ape status` and `ape project check` JSON](status-json.md#failures-and-diagnostics).
