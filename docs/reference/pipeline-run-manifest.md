# Run manifest

Every `ape task <skill>` run writes `manifest.yaml`, the on-disk record of the run: what ran, on which model and effort, what it cost, how it ended, and, in an APEX v1 project, where it sits in the session tree. The sessions the `ape change` verbs and an `apex-loop` run start are `ape task` runs, so each has one too. Downstream tooling, notably the [apex_process_framework_eval](https://github.com/diegosz/apex_process_framework_eval) consumer, reads it as a stable contract. This page is that contract. (`ape prompt` and `ape chat` keep their own records, `prompt.yaml` and `session.yaml`.)

## Layout

In an APEX v1 project (`_apex/state/` exists) runs live under `_apex/state/runs/`, which git ignores and a model's edits cannot reach. Elsewhere they live under the output folder (`output_folder`, default `_output`):

```
_apex/state/runs/tasks/<skill>/<run_id>/       (APEX v1 project)
<output_folder>/ape/tasks/<skill>/<run_id>/    (anywhere else)
  manifest.yaml                    # this document
  pipeline-report.md               # human-readable summary rendered from the manifest
  stages/
    01-<skill>/
      step-01-<skill>.ndjson       # the step's event log
      step-01-<skill>.args.md      # only when the arguments went to a file (step-contract.md)
```

plus the run's runlog streams. A symlink at `<skill>/latest` points at the most recent `<run_id>`; on filesystems without symlinks it is best-effort.

A plugin skill, `<plugin>:<skill>`, is filed under the folder `<plugin>__<skill>`, because Windows does not allow a `:` in a folder name. The manifest keeps the skill's name.

`<run_id>` is `YYYYMMDD-HHMMSS-<7-char-hash>` (UTC). The hash mixes the start time in nanoseconds, the skill and the project root, so concurrent runs against the same project do not collide.

`ape task --manifest-dir <path>` moves the base directory; ape writes `<path>/<skill>/<run_id>/` underneath. The eval uses it to put manifests in its own results tree.

## Schema

`schema_version` is `2`. Every field added since is **additive**: optional, omitted when empty, and ignored by a reader that does not know it. Consumers should treat unknown fields as opaque and reject only a manifest whose `schema_version` is higher than the one they understand.

A manifest an older ape wrote for an `ape pipeline` run (APEX v0) has the same schema, with one stage per pipeline stage and a few pipeline-only fields; the v0 docs are at https://github.com/exoport/apex_process_ape/tree/v0.10.0/docs.

An `ape task` run in an APEX v1 project, here an `apex-apply` session that an `apex-loop` run started:

```yaml
schema_version: 2
ape_version: 1.0.0
claude_version: 2.1.302 (Claude Code)
file_read_max_output_tokens: 30000
pipeline:
  name: apex-apply              # the run's name: the skill
  source: /home/foo/myproject/_apex/pipelines/apex-apply.yaml   # see "pipeline" below
  digest: ""
project_root: /home/foo/myproject
run_id: 20261021-094530-a0d06c8
started_at: 2026-10-21T09:45:30Z
timestamp: "20261021114530"     # the framework's `timestamp` for this run
ended_at: 2026-10-21T10:21:02Z
duration_seconds: 2132.4
status: completed               # running | completed | failed | cancelled
totals:
  cost_usd: 3.12
  tokens_input: 212334
  tokens_output: 18910
  tokens_cache_read: 1874200
  tokens_cache_creation: 92110      # sum of the two tiers below
  tokens_cache_creation_5m: 31110
  tokens_cache_creation_1h: 61000
  num_turns: 96
  steps_run: 1
  steps_failed: 0
  commits_made: 0               # ape's own commits only: see "Commits"
  model_usage:
    claude-sonnet-5-5:
      cost_usd: 3.12
      tokens_input: 212334
      tokens_output: 18910
      tokens_cache_read: 1874200
      tokens_cache_creation: 92110
      tokens_cache_creation_5m: 31110
      tokens_cache_creation_1h: 61000
      num_turns: 96
stages:
  - index: 1
    name: apex-apply
    started_at: 2026-10-21T09:45:30Z
    ended_at: 2026-10-21T10:21:02Z
    duration_seconds: 2132.4
    status: completed
    steps:
      - index: 1
        skill: apex-apply
        args: add-tenant-delete
        model: sonnet
        effort: high
        effort_source: config
        started_at: 2026-10-21T09:45:30Z
        ended_at: 2026-10-21T10:21:02Z
        duration_seconds: 2132.4
        status: completed
        exit_code: 0
        cost_usd: 3.12
        tokens_input: 212334
        tokens_output: 18910
        tokens_cache_read: 1874200
        tokens_cache_creation: 92110
        tokens_cache_creation_5m: 31110
        tokens_cache_creation_1h: 61000
        num_turns: 96
        events_path: stages/01-apex-apply/step-01-apex-apply.ndjson
        context_window: 1000000
        model_usage: {}           # this step's per-model breakdown, same shape as totals.model_usage
        sessions:                 # per-claude-session usage: the main session, then sub-agents
          - session_id: 0a675bc4
            cost_usd: 2.80
            tokens_input: 190000
            tokens_output: 16600
            num_turns: 80
          - session_id: 9f31ab02
            parent_session_id: 0a675bc4
            cost_usd: 0.32
            tokens_input: 22334
            tokens_output: 2310
            num_turns: 16
commit_contract:
  skill: apex-apply
  declared: false
  asserted: false
  skip_reason: <why no assertion ran>
  ok: false
launch:
  model: sonnet
  model_source: config
  effort: high
  effort_source: config
  parent: <the APE_SESSION of the loop session>
depth: 2
parent_run: 20261021-090012-5be21f0
usage:                       # v1.1: the plan's usage over the run
  before: {at: 2026-10-21T09:01:12Z, five_hour: 0.13, five_hour_resets_at: 2026-10-21T14:00:00Z, seven_day: 0.75, seven_day_resets_at: 2026-10-23T18:00:00Z}
  after: {at: 2026-10-21T09:41:40Z, five_hour: 0.19, five_hour_resets_at: 2026-10-21T14:00:00Z, seven_day: 0.76, seven_day_resets_at: 2026-10-23T18:00:00Z}
  guard: {decision: launch, reading: {…}, share_five_hour: 0.05, share_seven_day: 0.05, share_source: default}
```

### Run fields

| Field | Meaning |
| --- | --- |
| `schema_version`, `ape_version` | The schema (2) and the ape that wrote the run. |
| `claude_version` | `claude --version` at run start; empty when it could not be read. |
| `file_read_max_output_tokens` | The per-Read token cap ape pinned on the session (`CLAUDE_CODE_FILE_READ_MAX_OUTPUT_TOKENS`, 30000), set in the spawn environment and in ape's `--settings` `env`. |
| `pipeline` | `name` is the run's name, the skill for a task run. `source` names `_apex/pipelines/<skill>.yaml`, a file a task run does not have, so `digest` is empty. Both are filled for a pipeline run an older ape wrote. |
| `project_root`, `run_id` | The project and the run's id (see Layout). |
| `started_at`, `ended_at`, `duration_seconds` | Wall-clock instants (RFC 3339, UTC) and the run's length. |
| `timestamp` | The framework's `timestamp` for this run; see [below](#timestamp-is-not-a-second-started_at). |
| `status` | See [Status values](#status-values). |
| `totals` | Sums over completed steps: cost, tokens, turns, `steps_run`, `steps_failed`, `commits_made`, and `model_usage` keyed by model id. |
| `stages` | One stage with one step for a task run. Step fields are below. |
| `commit_contract` | The commit-ownership verdict; see [Commits](#commits). |
| `commits_shared` | Commits in this run's range that another ape session's run in the same checkout may have made; see [Commits](#commits). |
| `termination` | Why a run ended other than by finishing; see [below](#termination--why-a-run-ended). |
| `stop_gate` | APEX v1: the project's stop hook's verdicts on this run's Stops; see [below](#stop_gate--the-v1-stop-hooks-verdicts). |
| `launch` | APEX v1: the model and effort the session started with, and what decided each; see [below](#launch--the-v1-model-and-effort-decision). |
| `review_launch` | Only on a review session `ape change review --run` started: the review's own launch decision, in the same shape as `launch`. |
| `depth`, `parent_run` | APEX v1 nesting rule: `depth` is 1 for a run a terminal, a script or the service daemon started, 2 for one an `apex-loop` run started. `parent_run` is that loop run's id. |
| `usage` | The claude.ai plan's usage over the run, and the usage guard's decision; see [below](#usage--the-plans-usage-over-the-run). |
| `relay_of`, `relay` | Only on a loop session ape relayed: the loop run it relays, and how many relays that loop run has had with this one (1 for the first). |
| `children`, `tree_cost_usd` | Only on an `apex-loop` run, written when it ends; see [below](#children-and-tree_cost_usd). |
| `session_leftovers` | The processes the session left running after it ended, and what ape did to each; see [below](#session_leftovers--what-the-session-left-running). Absent when there were none. |
| `transcript_blobs`, `upload_status` | Present only with `--upload-transcripts`: each uploaded transcript's content-addressed reference, and `ok`, `partial` or `failed`. |

### Step fields

| Field | Meaning |
| --- | --- |
| `index`, `skill`, `agent`, `args`, `prompt` | What was typed: the skill, the `--agent` fronting it (omitted when none), the `--args` and the prompt text. |
| `model` | The model the session was launched with; empty when ape passed none and claude chose. |
| `effort`, `effort_source` | The effort the session ran at, and the rule that produced it. In an APEX v1 project the source is the launch decision's (`flag`, `config`, `parent-session`, `config-default`, `default`). Elsewhere it is `flag` (`--effort`), `table` (`_apex/effort-defaults.yaml`) or `legacy-default` (no table, so `xhigh` process-wide). See [claude-code-env-vars.md](claude-code-env-vars.md#reasoning-effort-claude_code_effort_level). |
| `started_at`, `ended_at`, `duration_seconds`, `status`, `exit_code` | The step's lifecycle. |
| `cost_usd`, `tokens_*`, `num_turns` | The step's usage, from the transcript scan. `tokens_cache_creation` is the sum of `tokens_cache_creation_5m` and `tokens_cache_creation_1h`, which price differently. |
| `model_usage` | The step's usage per model, keyed by model id. |
| `sessions[]` | Usage per claude session: the step's main session, and every Agent-tool sub-agent session (`parent_session_id` set). |
| `events_path` | The step's event log, relative to the run directory. |
| `commit_sha`, `commit_message`, `commit_status`, `commit_error` | Only with `--task-commit`; see [Commits](#commits). |
| `telemetry_note` | Why a numeric field is zero or approximate: no transcript or no complete assistant turn (everything zero), or a model with no price (tokens and turns exact, `cost_usd` a lower bound). Several causes are joined with `; `. |
| `context_window` | The usable context of the model the step was launched with; see [below](#reading-the-context_window-fields). |
| `contract` | The terminal-contract verdict; see [below](#reading-the-contract-field). |

### Status values

- `running` — written at run start; should not appear in a finalized manifest. If you see it, the run was abandoned without finalization (process crash, hard kill).
- `completed` — the step finished.
- `failed` — the step failed, or a backstop or limit ended the run; `termination` says which.
- `cancelled` — the run's context was cancelled (Ctrl-C, or a parent's deadline).

### `usage` — the plan's usage over the run

v1.1. Every unattended session ape launches records the claude.ai plan's rate limits from its status line
(`ape usage record`, see [`ape usage`](cli.md#ape-usage)). An `ape task` run's manifest gets, when
it ends:

| Field | Meaning |
| --- | --- |
| `before`, `after` | The first and the last reading the run's session recorded: `{at, five_hour, five_hour_resets_at, seven_day, seven_day_resets_at}`, the windows as fractions 0..1. `before` is taken at the session's first API response, not before the launch. Absent when the session recorded none (a plan without rate limits, a session that never reached the API). |
| `guard` | APEX v1: the usage guard's decision before the launch: `decision` (`launch`, `unknown`, `stop`, `off`, `usage_ok`), `reading` (the reading decided on), `share_five_hour`, `share_seven_day` and `share_source` (`config`, `auto`, `default`), `held_until` and `held_seconds` when the launch was held on the 5-hour window first, `usage_ok` for a person's `--usage-ok`, and `note` ("usage unknown (no reading in 10 min)"). |

#### The usage files: `<user cache>/ape/usage.json`

The readings live outside every project, under `os.UserCacheDir()` (on Linux `$XDG_CACHE_HOME/ape`, by default `~/.cache/ape`):
they belong to the claude.ai account the person is signed in with, one set per OS user.
`usage.json` is the newest reading any session recorded (`internal/usage/usage.go`):

```json
{
  "at": "2026-10-21T09:41:40Z",
  "five_hour": 0.19,
  "five_hour_resets_at": "2026-10-21T14:00:00Z",
  "seven_day": 0.76,
  "seven_day_resets_at": "2026-10-23T18:00:00Z",
  "run": "20261021-090012-5be21f0",
  "claude_version": "2.1.306"
}
```

| Field | Meaning |
| --- | --- |
| `at` | When the status line ran, UTC (RFC 3339). |
| `five_hour`, `seven_day` | The windows' use as a fraction 0..1. Claude Code's status-line JSON gives `rate_limits.<window>.used_percentage` as a whole number 0–100; ape stores it over 100. `null` when the reading did not carry that window. |
| `five_hour_resets_at`, `seven_day_resets_at` | The window's reset. Claude Code gives `resets_at` in epoch seconds; ape stores RFC 3339, UTC. `null` when not given. |
| `run` | The ape run whose session recorded the reading; `""` for a session launched without a run record. |
| `claude_version` | The status-line JSON's `version`. |

The file is absent until a session ape launched gets its first API response: before that
Claude Code's JSON has no `rate_limits` key, and a reading with neither window records nothing.
A reading older than the one on disk is not written, and neither is one equal to it within 30 s.
The manifest's `before`/`after` and the history entries carry the same shape.

- `usage-runs/<launch key>.json` — one in-flight launch's `{run, skill, first, last}` readings, taken into the manifest's `before`/`after` and removed when the run ends (files a killed ape left are removed after 7 days).
- `usage-history.json` — a list of ended runs' `{skill, run, at, before, after}` (only runs with both readings; the newest 500), which `limits.usage.share: auto` learns from.

### `termination` — why a run ended

Present only when a run ended for a reason other than finishing; absent on a
completed run, where `status` already says everything.

```yaml
termination:
  kind: idle_timeout          # see the table below
  message: "interactive step idle for 1h0m0s without progress (window 1h0m0s): …"
  diagnostic: "last progress hook 1h0m0s ago (hook 1h0m0s ago; transcript none for 3h44m; pty n/a); child pid 4242 alive"
  last_source: hook           # idle_timeout only: hook | transcript | pty | none
  idle_seconds: 3600.0        # idle_timeout only
  window_seconds: 3600.0      # idle_timeout only
  elapsed_seconds: 14400.0    # max_duration; prompt_not_submitted: how long ape pressed Enter
  max_seconds: 10800.0        # max_duration only
  quiet_seconds: 90.0         # api_error only
  spent_usd: 41.20            # budget only: the milestone's spend
  budget_usd: 40.00           # budget only: the milestone's Budget: line
  session_cost_usd: 6.10      # budget only: this session's live cost
  resets: "11pm (Europe/Madrid)"          # usage_limit: the reset as Claude Code printed it; usage_guard: resets_at's local time
  resets_at: 2026-10-16T23:00:00+02:00    # usage_limit: parsed, absent when it does not parse; usage_guard: the 7-day window's reset
```

| `kind` | Meaning | `ape task` exit |
| --- | --- | --- |
| `idle_timeout` | No progress on any watched signal for a full `--idle-timeout` window. | 1 |
| `max_duration` | The `--max-duration` ceiling, progress or not. | 1 |
| `api_error` | claude's own upstream-failure text: a failure of the harness, not of the step. | 5 |
| `prompt_not_submitted` | ape typed the line and claude never submitted it: no `UserPromptSubmit` through the Enter press and the two retries. `message` says whether the line was drawn and quotes the input row. | 1 |
| `repl_not_ready` | claude never came up inside the PTY within the 30-second window: an unknown blocking modal, a trust dialog ape could not get past, or a claude that drew nothing. Claude Code's Settings Warning (a settings file holds a value it skips, such as an invalid permission rule) ends the wait at once instead: ape never presses Continue, which would run without the skipped rule, and `message` carries the warning's lines. `message` carries the last pane; the raw PTY bytes are saved beside the run. `status` is `failed`. A `--max-duration` shorter than the window records `max_duration` instead, and a run you cancel while claude is still starting stays `cancelled`. | 3 |
| `budget` | APEX v1, `limits.budget: enforce`: the milestone's spend reached its budget, and ape stopped the session. | 1 |
| `usage_limit` | A claude.ai plan's usage limit stopped the session: the same limit line on two pane reads 15 s apart with no hook between them. `ape status` shows "stopped: usage-limit, resets <ts>". Matched against Claude Code 2.1.302's text. | 14 |
| `usage_guard` | APEX v1: ape did not start the session. Its usage guard read the plan's 7-day window over `limits.usage.seven_day`, or with the launch's share over `limits.usage.ceiling` ([config-yaml.md](config-yaml.md#limitsusage)). `message` names the reading, the share, the reset and the `--usage-ok` override; `stages` is empty. `ape status` shows "stopped: usage-guard (not started: the 7-day window), resets <ts>". A person's call. | 14 |
| `parent_gone` | APEX v1: the `apex-loop` run that started this session ended first (its ape process is gone), and ape stopped the session. | 1 |
| `relays` | APEX v1: a loop session's context reached `limits.context_relay` again after `limits.relays` relays, and ape stopped the loop run instead of relaying it once more. A person's call. | 1 |
| `cancelled` | The context was cancelled. | 1 |
| `error` | Any other run error. | 1 |

The exit codes are `ape task`'s (`taskExitCode`, `internal/apecmd/task.go`; `ape task --help`).

**Read it together with the zeros.** `totals` counts *completed* steps, and a
step stopped mid-flight records none, so a terminated run can show
`steps: []`, `cost_usd: 0` and `num_turns: 0` while its work is real, committed
and visible in `git log` and in `commit_contract`. `termination` is what tells
that run apart from one that did nothing. For a `budget` stop,
`session_cost_usd` is the session's cost as ape measured it live, which the
totals do not hold.

Two things in `diagnostic` are worth knowing how to read:

- **`pty n/a`** means ape was not *watching* PTY output on this path. `ape task`
  deliberately does not, only `ape prompt` does. It does **not** mean claude
  stopped drawing: it is the difference between *ape stopped watching* and
  *claude stopped working*.
- **`transcript touched N time(s) without growing`** is the signature of a hung
  session: Claude Code touches a wedged session's transcript about hourly
  without writing a byte.

### `launch` — the v1 model and effort decision

Present on every `ape task` session of an APEX v1 project, absent elsewhere.

| Field | Meaning |
| --- | --- |
| `model`, `model_source` | The model passed as `--model`, and what decided it: `flag` (`--model`), `config` (`_apex/config.yaml` `models.<skill>`), `parent-session` (the ape session that started this one), or `default` (nothing passed; claude's own default, and `model` is omitted). |
| `effort`, `effort_source` | The level passed as `claude --effort`, and what decided it: `flag`, `config` (`models.<skill>.effort`), `parent-session`, `config-default` (the top-level `effort:`), or `default` (nothing passed). |
| `parent` | The `APE_SESSION` of the session that started this one; for a review, the session that asked for it. |

The resolution order is in `ape task --help` and [claude-code-env-vars.md](claude-code-env-vars.md#reasoning-effort-claude_code_effort_level). An `ape change` verb that starts a session decides its model and effort first and passes what it decided as `--model` and `--effort`, so that session's `launch` sources read `flag`; a review session's `review_launch` keeps the verb's own decision and sources.

### `stop_gate` — the v1 stop hook's verdicts

In an APEX v1 project whose settings register `ape hook stop` (and in every `ape task` session, which passes the project's hooks inline), ape waits for that hook's verdict on each of the main session's Stops. A Stop the hook blocks continues the turn instead of ending the run. Absent when no verdict was consulted.

```yaml
stop_gate:
  deferrals: 2        # Stops the hook blocked: turns the session continued
  allowed: 1
  events:
    - at: 2026-10-21T10:02:11Z
      verdict: block
      change: add-tenant-delete
      reason: "<the hook's reason>"
    - at: 2026-10-21T10:20:58Z
      verdict: allow
```

`ape prompt` records the same verdicts under `stop_gate` in `prompt.yaml`. Why the hook blocks is in [step-completion-gates.md](../explanation/step-completion-gates.md#v1-the-stop-hook).

### `session_leftovers` — what the session left running

A server a session's Bash tool backgrounded (`cmd &`, `nohup`, `setsid`) is no longer a child of claude once its shell exits, so stopping claude and everything below it does not stop it. When the session has ended and claude has been stopped, ape looks for every process still carrying the session's `APE_SESSION=<kind>/<run-id>` environment entry, which names this run alone:

```yaml
session_leftovers:
  - pid: 48211
    command: ./bin/taskforge serve --port 8080
    started: 2026-10-23T10:14:02Z
    action: killed          # killed | kept | kill-failed
```

- With the default `--leftovers kill`, each one's process tree gets SIGTERM, then SIGKILL after 2 s. `kill-failed` means it was still running after that.
- With `--leftovers report`, they are recorded as `kept` and left running.
- Only the topmost process of each leftover tree is listed. The processes below it stop with it.
- Never touched: ape itself and its ancestors, zombies, processes whose environment ape cannot read (another user's), and ape's own detached processes. Those are the long verbs' `ape __run` workers and the `ape run supervise` supervisor of a detached run, everything in their sessions and process groups, and everything below them.
- Nor anything a long verb's worker started, even a server its gate script double-forked out of the worker's tree and session (`( setsid server & )`). The worker carries its own marker, `APE_SESSION=<kind>/<run-id>/worker/<worker-run-id>`, which is never the session's entry: a detached gate outlives the session that started it, and so do its servers. The nesting rule reads that marker as the session it names, so a gate script's `ape task` is refused exactly as the session's would be.
- A process that cleared its environment (`env -i`, a container) carries no marker and is not found.
- Linux only. On macOS and Windows nothing is looked for or recorded.

ape prints one `⚠` line on stderr when it finds any. `ape prompt` records the same list in `prompt.yaml`. `ape chat` never looks: what a person's session starts is theirs.

### `children` and `tree_cost_usd`

Written when an `apex-loop` run ends, children or not:

```yaml
children:
  - run: 20261021-094530-a0d06c8
    skill: apex-apply
    cost_usd: 3.12
    status: completed
tree_cost_usd: 7.94     # this run's totals.cost_usd plus its children's
```

`children` are the runs whose manifests name this run as `parent_run`. `totals` stay the loop session's own, so a reader that sums every manifest counts each child once. `tree_cost_usd` is written for every loop run, $0 and childless included, so its absence only ever means "not computed" (any other run, or one still running), never zero. Each relayed session of a loop run has its own manifest, cost and children.

### Commits

APEX v1 skills commit their own work. ape commits only when you pass `--task-commit`: one commit of the whole task at the end, with the message you give, or `ape:task/<skill>` for a bare flag.

- **`totals.commits_made` counts ape's own commits only**, so `commits_made: 0` on a run whose skill committed six times is correct. The skill's commits are in `git log`.
- **`commit_contract`** is the commit-ownership verdict, written when the project has `_apex/commit-owners.csv`. A skill absent from that file must leave HEAD, the index and the stash alone; a skill present must commit at least once, each subject matching a format the file declares. Without the file nothing is asserted: `asserted: false` with a `skip_reason`, never a pass. No APEX v1 release ships the file, so a v1 project is not asserted. A violation exits 6, and the run itself may have succeeded.

  | Field | Meaning |
  | --- | --- |
  | `skill` | The dispatched skill. |
  | `declared` | Whether the file lists the skill, so which assertion ran. |
  | `asserted`, `skip_reason` | Whether an assertion ran, and why not. |
  | `ok`, `violations` | The verdict, and each failed check as `<check>: <message>`. |
  | `subjects` | The commit subjects observed across the dispatch. |

- **`commits_shared`** lists the subjects of commits in this run's range made while another ape session's run in the same checkout was going (not this run's parent loop run, not its own children). git cannot say which run made them, so neither claims them: both list them here, and they are left out of the commit check.

With `--task-commit`, ape refuses to start on a dirty tree unless you pass `--commit-allow-dirty`, and records on the step:

| Field | When set |
| --- | --- |
| `commit_sha` | only when `commit_status` is `committed` |
| `commit_message` | the message used |
| `commit_status` | `committed`; `no-op` (the tree was clean); `failed` (`git commit` failed); `skipped-step-failed`; `skipped-cancelled` |
| `commit_error` | only when `commit_status` is `failed`: the captured stderr |

### Metric provenance

Every run drives an interactive `claude` REPL inside a PTY (see [why-pty-only.md](../explanation/why-pty-only.md)), so there is no terminal `result` event to read. `cost_usd`, `tokens_*`, `num_turns`, `model_usage` and `sessions[]` come from scanning the session transcripts (`internal/cost/`), which attributes usage per model and per claude session, sub-agents included. If a transcript is unavailable at scan time, the numeric fields are zero, `telemetry_note` says why, and the step still has its duration and status.

Cost and tokens can also disagree the other way. Pricing uses a table compiled into ape (`internal/cost/prices.yaml`), and Claude Code can introduce a model id the table does not carry. Then the token counts stay exact while `cost_usd` is either **estimated** from the model's family tier or **zero**, and `telemetry_note` says which. A zero `cost_usd` beside non-zero `tokens_*` means *unpriced*, not free. `ape costs reprice` recomputes the figures from the stored per-model tokens once the table is corrected; see [how to keep cost pricing current](../how-to/keep-cost-pricing-current.md).

### Per-model breakdown and `ape costs`

`totals.model_usage` and per-step `model_usage` feed the project's cost rollup, `cost-rollup.json` under ape's root (`_apex/state/runs/` in an APEX v1 project, `<output_folder>/ape/` elsewhere). `ape costs` reads the rollup; its human output adds a **by model** table, and `--output-format json` includes a `per_model` map. Two readers inspect one record directly:

- `ape costs run <run-id>` reads that run's `manifest.yaml` and prints its totals and per-model breakdown.
- `ape costs chat <chat-id>` reads a chat's `session.yaml`, and `ape costs prompt <prompt-id>` a prompt's `prompt.yaml`.

### Reading the `context_window` fields

`peak_tokens / context_window` is only as good as the divisor, so both the number and its absence are contracts.

**This is a maintained table, not a measurement of a run.** Claude Code reports the real per-model window on the stream-json result event's `modelUsage[].contextWindow`. ape is [PTY-only by design](../explanation/why-pty-only.md) and does not use that surface, so the reported window cannot reach ape on any path ape drives. Do not label these fields as reported. What they give you is one correctable source for the divisor instead of a hand-maintained copy per consumer.

The values come from the Models API (`GET /v1/models/{id}` returns `max_input_tokens`, which *is* the context window), curated by hand, with the same standing as ape's prices. Treat a window as correct as of the release: a model that shipped after your ape build may have no window (reported as absent, never defaulted) or a stale one.

**There are two fields, and both honour a `[1m]` suffix:**

| field                                  | resolved from                                        |
| -------------------------------------- | ---------------------------------------------------- |
| `steps[].context_window`               | the step's effective `--model` string                |
| `steps[].model_usage[].context_window` | the raw model spelling in the transcript, per bucket  |

A suffixed and an unsuffixed model are the same model at the same price, so ape folds them into one `model_usage` bucket keyed by the base id. That is right for cost and would be wrong for context, so the bucket's window is read from the raw spelling *before* the fold.

**Which to divide by** is a question of scope. `steps[].context_window` is the step's own window and the right denominator for a step-level ratio: a step whose sub-agents ran other models has several per-model entries, and no one of them speaks for the step. Use a per-model entry for that model's share.

One case records **no** per-model window: a bucket holding turns of two different sizes, which happens when a step's sub-agents run a different context variant than the step was launched with. No single number is right for it, so it is omitted like any other unknown.

**Absent means unknown.** A model ape has no window for gets no field at all, not a zero. Render "could not look", never a plausible ratio. There is **no family fallback**: a family window that is off produces a precise-looking ratio wrong by the ratio of the two windows.

`ape doctor`'s `cost.price_table_coverage` row names any model seen in recent transcripts that has no known window. A window can be corrected without a new ape binary, the same way a price can:

```bash
ape costs update --from corrected.yaml   # rows accept `context_window:` beside base_input / output
```

### Reading the `contract` field

Some skills end a run with a machine-readable return block. A framework that declares which skills promise one, and the pattern that recognises it, does so in `_apex/terminal-contracts.csv`; after the step ape matches the closing assistant message against that skill's pattern and records the verdict. The APEX v1 framework ships no such file, so in a v1 project the field is absent.

| value           | meaning                                                            |
| --------------- | ------------------------------------------------------------------ |
| `present`       | the closing message matched the skill's pattern                     |
| `missing`       | a closing message was read and it did not match                     |
| `no-transcript` | the skill is enrolled but no closing message could be read at all   |
| *(omitted)*     | the skill declares no terminal contract: there was nothing to check |

**This is telemetry about text, not about work.** The check reads the closing message of the step's **main** session, which is a relay whenever something below it did the work: an `--agent` that fronted the skill, or Agent-tool sub-agents whose own transcripts are separate files (see `sessions[]`). So `missing` means *the closing text did not match*, **not** *the skill failed to emit its contract*. It is warn-only for that reason. Only completed steps have a record, so a rate computed from this field is a rate over completed steps.

### `timestamp` is not a second `started_at`

`started_at` is an RFC 3339 UTC instant for telemetry, read straight from
the wall clock. `timestamp` is the framework's own variable, local
wall-clock `YYYYMMDDHHMMSS`, and it is **issued monotonically**: every
issue returns the later of the wall clock and the project's persisted
floor (`timestamp.state` under ape's root), so a machine whose clock is
behind cannot stamp a record backwards. The two can therefore disagree,
and when they do the disagreement is the point. Empty when the run had no
project to issue against.

## Reading a manifest from code

The manifest is plain YAML; any YAML library parses it. The reference Go types live in [`internal/pipeline/manifest.go`](../../internal/pipeline/manifest.go). The package is internal, but the YAML is the contract, so re-deriving types from this page is fine.

There is no flag to disable the manifest.

## Cleanup

ape never deletes old runs. Reclaim disk by removing run directories:

```bash
rm -rf _apex/state/runs/tasks/<skill>/<old_run_id>     # APEX v1 project
rm -rf _output/ape/tasks/<skill>/<old_run_id>          # elsewhere, with the default output folder
```

`_apex/state/.gitignore` keeps runs out of git in a v1 project; `ape doctor` checks it (`state.gitignore`).

## Related

- [step-contract.md](step-contract.md): what a run types and checks.
- [status-json.md](status-json.md): `ape status --json`, which shows a run that stopped on a limit.
- [../how-to/run-a-single-skill.md](../how-to/run-a-single-skill.md): running `ape task`.
- [../explanation/nested-ape-runs.md](../explanation/nested-ape-runs.md): depth, parents and children.
