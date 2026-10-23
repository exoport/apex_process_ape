# `ape status` and `ape project check` JSON

`ape status --json` is how a script or a skill reads where an APEX v1 project stands, and
`ape project check --json` how it reads what is wrong with it. This page lists the fields of
both, taken from ape's JSON types, and the shape every v1 `--json` failure shares. The commands'
help is in cli.md: [`ape status`](cli.md#ape-status), [`ape project check`](cli.md#ape-project-check)
and [`ape project codes`](cli.md#ape-project-codes).

## `ape status`

```bash
ape status            # the report
ape status --brief    # at most 25 lines (what the SessionStart hook injects)
ape status --json     # the fields below, on stdout
ape status --board    # also refresh the project's board tab now (JSON "board")
ape status --wait 9m --until person,stopped --since 41 --json   # wait (below)
```

Status reads the records ape keeps and changes nothing, but for one line of its event log: a
detached run it finds lost is logged `run-lost`, once. Exit 0, or 2 on a usage error. `--board`
exits 0 whatever the board did. With `--until` it waits, and exits 75 when `--wait` runs out
([below](#ape-status---wait)).

A fresh project with one approved milestone gives, in part:

```json
{
  "project": "proj",
  "initialized": true,
  "branch": "main",
  "milestone": {
    "id": "M1",
    "title": "Greeter",
    "status": "approved",
    "status_text": "approved (by Ada Example 2026-10-21T14:52:45Z)",
    "gate": "./development/gates/M1-greeter/gate.sh",
    "approval_self_check": {"sha": "0fd56f53b5f5078d5ccf36d5f833ce8d8e05a433", "exit": 1, "at": "2026-10-21T14:52:45Z", "log": "_apex/state/logs/gate-M1-approve.log"},
    "base_sha": "0fd56f53b5f5078d5ccf36d5f833ce8d8e05a433",
    "spent_usd": 0,
    "budget_enforced": false,
    "stops": [],
    "consecutive_blocks": 0,
    "consecutive_blocks_cap": 2,
    "diagnostics": []
  },
  "changes": [],
  "tweaks": [],
  "next": "plan the next change toward M1 (apex-change), or `ape milestone gate M1` if the goal looks met",
  "next_why": "M1 is approved and no change of it is open",
  "base_upstream": null,
  "outside_flow": [],
  "sessions": []
}
```

### Top-level fields

| Field | Type | What it is |
| --- | --- | --- |
| `project` | string | `project_name` from `_apex/config.yaml`, else the folder's name. |
| `root` | string | The project root. |
| `initialized` | bool | The project has the v1 layout. |
| `branch` | string | The checked-out branch; `change/<id>` on a change's branch, `""` when detached. |
| `milestone` | object or null | The current milestone, the first not done (below). |
| `milestones` | array | Every milestone of the roadmap, one line each (below). |
| `changes` | array | Every change not archived, plus archived ones with unaccepted human-review tasks (below). |
| `archived` | int | How many changes are archived. |
| `tweaks` | array of ids | Approved tweaks not archived or blocked, in the order they run. |
| `pre_existing_red` | array | What was red at a change's baseline that is not the change's own: `{change, kind, ref, cmd, exit, sha}`. A milestone tier's baseline (W2) is `ref: "milestone-regression"`. For the planner to roadmap. |
| `spend` | object | `{total_usd, runs, note}`: the sum of the finished run manifests; a run still in progress is not counted yet. |
| `next` | string | What comes next, derived from the state alone. |
| `next_why` | string | Its reason, one line. |
| `roadmap_next` | array of strings | The Next findings (`development/next.md`, then any the roadmap still holds), as the base branch has them. Not the same as `next`. |
| `docs` | object | `docs:` from `_apex/config.yaml`: `changelog`, `user_docs`, `tested`. `{}` when unset. |
| `base_upstream` | object or null | `{branch, upstream, ahead, behind}`: the base branch against its upstream, from the local remote-tracking ref (ape never fetches). `null` with no upstream. |
| `outside_flow` | array | Commits on the base branch since the last archive squash (else the v1 epoch) that touch paths outside `development/` and `_apex/`, carry no `Change:` trailer and have no `apex:` subject: `{sha, subject, files}`, newest first. Shown, never failed. |
| `archive_journal` | object | Present while an archive is in flight or after one crashed: `{id, base, base_sha, branch, branch_tip, step, written, pid, at}`. |
| `archive_running` | bool | Present and true when that archive's process is alive. |
| `latest_run` | object | The newest `ape task` run (below). Absent before the first. |
| `sessions` | array | The model sessions ape launched that run in this project now (below). |
| `usage` | object or null | The claude.ai plan's usage as the sessions ape launches last read it (v1.1): `{at, five_hour, five_hour_resets_at, seven_day, seven_day_resets_at, run, claude_version, age_seconds}`, the windows as fractions 0..1. `null` before the first reading. The human output says `usage: 5h 0.14 · 7d 0.55 (read 2 min ago)`, or `usage: unknown`. |
| `config` | object | Effective values with defaults filled in: `commit_types`, `release_scheme`, `release_docs`, `commit_subject`. |
| `board` | object | With `--board` only: `{action, where, detail}`, `action` one of `posted`, `wrote`, `unchanged`, `no-board`, `skipped`. |

### `milestone`

| Field | What it is |
| --- | --- |
| `id`, `title`, `status`, `status_text` | From the roadmap. `status` is `proposed`, `approved` or `done`; `status_text` is the whole `Status:` value. |
| `gate` | The `Gate:` command. |
| `budget` | The budget in USD, when the roadmap gives one. |
| `gate_last` | The latest gate run: `exit`, `at`, `sha`, `secs`, `output_sha256`, `cmd`, `log`, `tail`, `spec_checks`, `spec_failed`, `stray_files`, `strays_kept`, `regression`, `reason`, `detail`, `shell_note`, `flaky`, `live_skipped`, and `current` (v1.2): `true` when `ape milestone gate <M>` would return this run's result now instead of running the gate — no run in progress, HEAD and the working tree (outside `_apex/state/`) unchanged since it ended, and the gate not refused; `false` otherwise, always for a run recorded by ape v1.1. A red `Gate:` command skips the live scenario checks (`live_skipped`: how many) and the regression command (`regression.skipped: "gate-red"`). Absent before the gate ran. |
| `approval_self_check` | The latest approval's gate run: `{sha, exit, at, log}`, plus `accept_green: {reason, by, at}` when a person re-approved it green (`milestone approve --accept-green`, v1.2). It is the gate's newest result when its `at` is later than `gate_last.at`, or there is no `gate_last`: the human output and the board then show it, and after an accepted green the next step is `ape milestone close`. A re-approval's `sha` differs from `base_sha`. |
| `base_sha` | The milestone's start, HEAD at its first approval: what every gate run gets as `APE_MILESTONE_BASE`. |
| `spent_usd` | The spend of runs started since it was approved. |
| `budget_enforced` | `limits.budget: enforce` is set. |
| `stops` | Sessions `ape task` stopped at the enforced budget: `{reason, at, run, skill, spent_usd, budget_usd, session_usd}`. |
| `consecutive_blocks`, `consecutive_blocks_cap` | The milestone's changes blocked in a row, and `limits.consecutive_blocks`. |
| `diagnostics` | The limit that stops work on it, in the shape of a failure's diagnostics: `[{code: "milestone.blocks-exceeded", action: "stop", …}]` at the cap, else `[]`. |

### `milestones[]`

| Field | What it is |
| --- | --- |
| `id`, `title`, `status` | From the roadmap. |
| `base_sha` | The milestone's start, once approved. |
| `last_gate` | Its newest gate run, `{sha, exit, at, log}`. Absent before the gate ran. |
| `approval_self_check` | The latest approval's gate run, `{sha, exit, at, log}`, with `accept_green` as under `milestone`. |
| `accept_red` | Present when a person closed it with its gate red: `{reason, by, at, gate}`. |

### `changes[]`

| Field | What it is |
| --- | --- |
| `id`, `title`, `milestone` | From `change.md`. `milestone` is `none` for a tweak. |
| `status` | One of the [change states](change-lifecycle.md#states), or `invalid` when ape cannot read the change (then `error` says why). |
| `lane` | `tweak` or `milestone`. |
| `approved` | A tweak's approval: `{by, at, plan_digest}`. |
| `branch` | `change/<id>`, when the change has one. |
| `blocked_reason` | Why it is blocked. |
| `answer` | A person's answer: `{by, at, text}`. Kept once it is started again. |
| `failing` | The red checks of its last verify, for example `"T1 (exit 1)"`, `"T2 (vacuous: …)"`, `"S (unrun: $X is unset …)"`. |
| `flaky` | Checks that passed only on a retry at its last verify. |
| `verify_sha` | The sha its last verify ran at. |
| `live_skipped` | The live scenario checks its last green verify did not run: neither the change's own nor red at the last gate. Absent when none. |
| `attempts`, `attempts_total`, `attempts_cap` | Red verifies since its last start, ever, and `limits.attempts`. This checkout's counts. |
| `pending_human` | Its human-review tasks not yet accepted. |
| `awaiting_human` | The same tasks, once only a person is left (the change is reviewed or archived). Always present, `[]` when none. The loop skips such a change. |
| `accepted` | Acceptances: `{task, by, at, evidence, run}`. `by` is a person, or `delegate:<skill>` with its `run`. |
| `delegate` | The newest delegate verdict per task: `{"T2": {verdict, reason, run, at, voided, defects, returned}}`. |
| `delegable` | Awaiting tasks a delegate may take now: `{task, reason}`, `reason` `configured` or `docs-default`. Omitted when none. |
| `delegate_returns` | Delegate fails that sent the change back: `{"T2": [{run, reason, defects, at}]}`. |
| `tools` | Design-tool tasks: `{task, name, skill, done}`. Always present. |
| `review_tools` | Its `review_tools:` frontmatter: `{name, skill}`. Always present. |
| `advise` | The files of the `tools.advise` entries its `advise:` frontmatter names, one row per file. Always present, `[]` when none. See [`changes[].advise`](#changesadvise). |

### `changes[].advise`

One row per `load:` file of each [`tools.advise`](config-yaml.md#toolsadvise) entry the change
names. The sessions that `for` names (`change`, `apply`, `review`) Read `path` before they work.

```json
{"name": "nats", "skill": "nats-expert:nats-expert", "for": ["change", "apply", "review"],
 "file": "advise.md", "path": "/home/ann/.claude/plugins/cache/team-tools/nats-expert/0.1.0/skills/nats-expert/advise.md",
 "bytes": 7312, "sha256": "9f2c…"}
```

| Field | What it is |
| --- | --- |
| `name`, `skill` | The `tools.advise` entry, and its skill. |
| `for` | The sessions that read the file. |
| `file` | The `load:` entry, relative to the skill's folder. |
| `path` | The file, absolute: in the plugin's install for a plugin skill, else in the user's or the project's `.claude/skills/<skill>/`. |
| `bytes`, `sha256` | Its size and hash now. Every `ape change verify` records them, without `path`, in `state.json`'s `verify.advise`. |
| `error` | Present instead of `sha256` when the file cannot be read: the entry is not declared, its skill is not installed (`… is unchecked: claude is not on PATH …` when the plugin installs cannot be read), or the file is missing. |

`ape status` resolves them each time it runs; a plugin skill's folder comes from the
`claude plugin` CLI, asked only when a change names such an entry. ape never reads the files for
meaning.

### `latest_run` and `sessions[]`

`latest_run` is `{skill, run_id, status, stopped, resets, resets_at, message, depth, parent_run}`:
how the newest `ape task` run ended. A claude.ai usage-limit stop carries `stopped` and its reset
time, and so does a stop of ape's usage guard (`stopped: "usage_guard"`, the 7-day window's reset).
A session a loop run started has `depth` 2 and names that run in `parent_run`.

Each `sessions[]` entry is `{run_id, kind, skill, depth, parent_run, pid, started_at}`: an
apex-loop run at depth 1, and its one child at depth 2. `kind` is the ape command that launched
it. A session whose launch ape's usage guard holds on the 5-hour window also carries
`held_until`, when it launches (the human output: "held: 5-hour window, launches <ts>"). The
loop's run also carries `relay`:

| `relay` field | What it is |
| --- | --- |
| `context_tokens` | The session's context, as the newest turn of its transcript shows it. |
| `context_window` | The model's window; 0 when unknown. |
| `context_fill` | `context_tokens` over `context_window`; `null` when the window is unknown, and then there is no relay. |
| `context_relay` | The threshold, `limits.context_relay`. |
| `relays`, `relays_cap` | Relays so far, and `limits.relays`. |
| `relay_of` | The run this one relays. |

## `ape status --wait`

```bash
ape status --wait <dur> --until <condition>[,<condition>…] [--since <seq>] [--json]
```

It waits, at most `--wait` (default `9m`, at most `9m30s`, else `usage.wait`), until one of the
conditions holds. The comma means any. The conditions are read from ape's records only: the
[event log](project-events.md), the run handles and locks, and the session registry. No process
is matched.

| Condition | Holds (its level) | Events that make it hold (its edge) |
| --- | --- | --- |
| `person` | The project waits on a person act (`wait.person_actions` is not empty). | `person`, `block` |
| `stopped` | No run of the project is live: no detached run going, no long-run lock held, no session ape launched running there. A detached run whose supervisor is gone counts as stopped, and is logged `run-lost`. | `run-end`, `run-lost`, `stop`, once the level holds |
| `done` | Every milestone of the roadmap is done. A roadmap with none is not. | `milestone-done`, once the level holds |
| `blocked` | A change is blocked. | `block` |
| `event` | (no level) | any event |

The order:

1. **Level first.** When a condition holds on the state now, the call returns at once (`how:
   "level"`). The first condition of `--until` that holds is the one reported.
2. **The events after `--since`.** An event logged after `--since` that makes a condition hold
   returns it (`how: "edge"`), so nothing logged between two calls is missed. Without `--since`,
   only events after the call started count.
3. **The wait.** The log is read every second until `--wait` runs out. Then the call exits 75
   (`how: "timeout"`).

Every exit prints the seq to pass as the next `--since`: `seq: <n>` as the last line, and in JSON
both top-level `seq` and `wait.last_seq`. On an edge it is the returned event's seq, so the events
after it are the next call's. Otherwise it is the newest seq logged. Usage errors (`usage.until`,
`usage.wait`) exit 2.

With `--json` the output is the status object above, plus:

| Field | What it is |
| --- | --- |
| `seq` | The `--since` to pass next. |
| `wait.until` | The conditions, as given. |
| `wait.since` | `--since`, `null` without it. |
| `wait.condition` | The condition that holds; `null` on 75. |
| `wait.how` | `level`, `edge` or `timeout`. |
| `wait.event` | The event line that made it hold (an edge), or the newest event after `--since` that bears on it (a level); `null` when none. |
| `wait.last_seq` | The same as `seq`. |
| `wait.person_actions` | The person acts the project waits on: `[{kind, ref, task, milestone, change, action, reason}]`, `[]` when none. `action` is the phrase the person types. |
| `wait.running` | The live runs' ids (detached runs, `long-run lock`, ape-launched sessions), when `stopped` is waited for; else `[]`. |
| `wait.waited_secs` | How long the call waited. |

The person acts, in this order:

| `kind` | When | `action` |
| --- | --- | --- |
| `milestone approve` | A milestone is proposed. | `approve M2` |
| `change approve` | A tweak awaits approval. | `approve <tweak-id>` |
| `change answer` | A change is blocked. | `answer <id>: <answer>` |
| `change accept` | A human-review task awaits a person, and no delegate may take it now. | `accept T2 of <id>` |
| `project accept-config` | A guarded config block changed with no person accepting it (`config.changed`). | `accept the config` |

From another machine, run ape by its absolute path, since a non-interactive ssh shell may not
have it on `PATH`:

```bash
ssh host 'cd /srv/proj && /home/me/.local/bin/ape status --wait 9m --until person,stopped --since 41 --json'
```

## `ape project check`

```bash
ape project check          # findings on stderr; exit 0 green, 1 findings, 2 usage
ape project check --json   # the same on stdout
```

Green:

```json
{
  "findings": [],
  "notices": [],
  "ok": true
}
```

With findings, `ok` is false and the JSON has the failure shape below, plus `findings` and
`notices`:

```json
{
  "diagnostics": [
    {"severity": "error", "code": "project.check-failed", "message": "5 finding(s)", "fix": "fix them and run `ape project check` again", "action": "person"},
    {"severity": "error", "code": "project.empty-section", "message": "`## Vision` is empty", "fix": "", "action": "person", "file": "development/project.md", "line": 3}
  ],
  "error": "5 finding(s) — fix them and run `ape project check` again",
  "exit": 1,
  "findings": [
    {"file": "development/project.md", "line": 3, "message": "`## Vision` is empty", "code": "project.empty-section"}
  ],
  "notices": [],
  "ok": false
}
```

`findings` are `{file, line, message, code}`. `notices` are not findings and never change the exit:
`{severity: "notice", code, message, fix, action, file, line}`, `[]` when none. A notice also prints
as a `notice: <file>:<line>: …` line on stderr.

## Failures and diagnostics

Every v1 command (project, status, milestone, change, decision) fails in `--json` with one shape:

```text
{"ok": false, "exit": N, "error": "<one line>", "diagnostics": [ … ], …}
```

The first diagnostic is the failure itself; the rest are its findings or conflicts. A
diagnostic is:

| Field | What it is |
| --- | --- |
| `severity` | Always `error` in a failure (there is no warning tier). Notices are `notice`. |
| `code` | Which rule. Stable: a new rule gets a new code, and a code's meaning never changes. |
| `message`, `fix` | For people. `message` is the sentence up to its first ` — `, `fix` the rest (empty when it has none); the human line joins them with ` — `. Findings and notices alike, since v1.1 (until then a finding's `message` also carried its fix). |
| `action` | What to do next (below). |
| `file`, `line` | Where, when it is about a file. |

Branch on `action`:

| `action` | Meaning |
| --- | --- |
| `retry` | Run the same command again. |
| `apply` | apex-apply: the work, or a re-start. |
| `review` | Review again. |
| `replan` | apex-change re-plans. |
| `stop` | The run stops. |
| `person` | A person must act: list it, and go on with other work. |

A code's action is the one `ape project codes` lists for it. Where the code's meaning names a case
that changes it (`milestone.changes-in-flight`), the diagnostic carries the case's action. Exit
statuses: 1 a check failed, 2 usage, 3 conflict (nothing written).

### The list of codes

`ape project codes` prints every code a v1 command can report, with its exit status, its action
and what it means. `--json` gives the same as an array of `{code, exit, action, meaning}`:

```bash
ape project codes
ape project codes --json
```

Read it from the installed ape rather than from a copy: a new rule gets a new code.
