# `_apex/config.yaml` (APEX v1)

`_apex/config.yaml` is an APEX v1 project's configuration: the models each skill runs on, the
loop's limits, how checks run, what the archive's squash looks like, and the plugins and tool
skills a session may use. This page lists every key ape's v1 reader accepts, with its type,
default and whether it is guarded, and the commands that act on the file.

## The file `ape project init` writes

```yaml
project_name: "greeter"
development_folder: development
# The model and effort each skill's session runs on (D65); change them freely.
models:
  apex-apply: {model: sonnet, effort: high}
  apex-loop: {model: sonnet, effort: high}
  apex-review: {model: opus, effort: medium}
  apex-plan: {model: opus, effort: medium}
  apex-change: {model: opus, effort: medium}
  apex-accept: {model: opus, effort: medium}
  apex-release: {model: sonnet, effort: high}
```

`project_name` is the directory's name. Every other key is absent and takes its default. Init
never overwrites an existing config. It appends the `models:` block to one that has none, and
leaves an existing `models:` alone, warning for each default skill it lacks.

## How it is read

- **An unparseable config refuses every command.** A YAML error, a value ape cannot use, or an
  unknown key inside a block that checks its keys all count. The commands exit 2 with "cannot read
  `_apex/config.yaml`: … — fix the YAML", and `ape task` refuses every session rather than run it
  with no limits.
- **These blocks check their keys:** `timeouts`, `regression`, `checks`, `docs`, `caps`, `commit`,
  `human_review`, `release` and `secrets`. A typo in one of them is an error, not an ignored
  setting.
- **Two blocks report instead of failing.** `tools:` never makes the config unparseable: a
  problem in it is the finding `tools.invalid`, and only its valid entries are used. A wrong
  `marketplaces:` or `plugins:` entry (a bad source, a `ref:` on a directory marketplace, a duplicate, an unknown `allow:` value) is the
  finding `plugins.config`.
- **`limits:` and the top level report instead of failing too.** An unknown key in `limits:` is
  the finding `config.unknown-key`; a top-level key no ape reader knows (neither a key on this
  page nor a framework folder variable) is the notice `config.unknown-top-key`, and is otherwise
  ignored. A top-level `effort:` or `models.<skill>.effort` that is not a level is the finding
  `config.invalid`, so `ape project check` reports it before a launch refuses it.
- **Guarded** blocks can loosen what ape enforces. Since the project's v1 epoch, a commit that
  changes one and is not ape's own (`apex: …`) is the finding `config.changed` until a person runs
  `ape project accept-config --by <who>` or reverts it. Meanwhile `ape change start`, `verify` and
  `archive` refuse with `change.provenance`. [What only a person does](../explanation/person-acts.md#accepting-the-config)
  explains why.

## Keys at a glance

| Key | Type | Default | Guarded |
| --- | ---- | ------- | ------- |
| [`project_name`](#project_name) | string | the directory's name | no |
| [`development_folder`](#development_folder) | path | `development` | no |
| [`models`](#models) | map: skill → model or `{model, effort}` | the seven rows above | no |
| [`effort`](#effort) | effort level | none (Claude Code's own) | no |
| [`vet`](#vet) | `plan` or `off` | `off` | no |
| [`limits`](#limits) | mapping | budget off, attempts 5, attempts_total 15, replans 3, consecutive_blocks 2, context_relay 0.6, relays 5, usage (below) | yes |
| [`regression`](#regression) | command, or `{change, milestone}` | none: no regression net | yes |
| [`timeouts`](#timeouts) | mapping per tier | check 15m idle / 2h max, regression 30m / 2h, gate 30m / 4h | yes |
| [`retries`](#retries) | integer 0–5 | 0 | yes |
| [`checks`](#checks) | `{vacuous_output: [phrases], vacuous_output_extra: [phrases], slow_regression: <duration>}` | five runner phrases; `5m` | yes |
| [`requires`](#requires) | map: name → `env`, `cmd` or `{probe, when}` | none | yes |
| [`caps`](#caps) | map: artefact → size | per artefact | yes |
| [`docs`](#docs) | `{changelog, user_docs, tested}` | none | no |
| [`evidence`](#evidence) | `off` or `archive` | `off` | yes |
| [`commit`](#commit) | `{subject, hooks, types}` | `"{type}: {title}"`, `run`, nine types | yes |
| [`notify`](#notify) | shell command | none | no |
| [`human_review`](#human_review) | mapping | absent: no delegate | yes |
| [`release`](#release) | `{scheme, docs}` | `semver`; `README.md` and `docs/` | yes |
| [`secrets`](#secrets) | `{allow: [{fingerprint, reason}]}` | none | yes |
| [`marketplaces`](#marketplaces-and-plugins) | map: name → source | none | yes |
| [`plugins`](#marketplaces-and-plugins) | list | none | yes |
| [`tools`](#tools) | `{design: [...], review: [...], advise: [...]}` | none | yes |
| [`library`](#library) | `true` or `false` | `false` (absent: not a library) | no |

## `project_name`

The project's name, shown by `ape status`. Absent, the root directory's name.

## `development_folder`

The folder that holds `project.md`, the roadmap, `specs/`, `changes/`, `archive/`, `decisions/` and
`briefs/`, relative to the project root. Default `development`. Every v1 command resolves the
project through it.

## `models`

The model and effort of each skill's session, keyed by skill. An entry is a model name or a
mapping:

```yaml
models:
  apex-review: sonnet                          # model only
  apex-apply: {model: sonnet, effort: high}    # model and effort
```

A bare family word (`fable`, `haiku`, `mythos`, `opus`, `sonnet`) resolves to its current
generation; `sonnet-5`, `claude-sonnet-5` or `opus[1m]` pin one.

`ape task` decides a v1 session's model and effort in this order, and records both with their
source in the run manifest's `launch:`:

| | First that is set |
| --- | --- |
| model | `--model`, then `models.<skill>`, then the model of the ape session that started this one, then Claude Code's default |
| effort | `--effort`, then `models.<skill>.effort`, then the parent session's effort, then the top-level [`effort`](#effort), then none |

The parent's effort is skipped when `models.<skill>` names a model without an effort: that effort
was chosen for another model. The effort goes to `claude --effort <level>`.

`ape project check` reports two kinds of entry that pin nothing:

- `models.unknown-skill`: a key naming no skill. Valid keys are a default above, a project or user
  skill, or a skill `tools:` or `human_review` names.
- `models.unknown-field`: a field other than `model` and `effort`.

`models.effort` is not a skill: the default effort of every session is the top-level `effort:`.
The orchestrator, `apex-orchestrator`, has no row: it runs in your own session (notice
`models.orchestrator-row`, formerly `models.operator-row`, kept as its alias until v1.5; see [The orchestrator and the loop](../explanation/orchestrator-and-loop.md#the-orchestrator-is-your-session)).

Not guarded: a model choice loosens nothing.

## `effort`

The default effort of an ape-launched session that no flag, `models.<skill>` entry or parent
session decides: one of `low`, `medium`, `high`, `xhigh`, `max`. Absent, ape passes no effort and
Claude Code's own setting applies. A value outside the list fails the session's launch, and
`ape project check` reports it as `config.invalid`.
`_apex/effort-defaults.yaml` is not used in a v1 project.

## `limits`

The loop's limits, enforced by ape. A key not in the table below limits nothing, and
`ape project check` reports it as `config.unknown-key`. Guarded: a change to them that no person accepted stops the
loop's next start, verify or archive ([what the guard reads](../explanation/person-acts.md#what-the-guard-reads)).

```yaml
limits:
  budget: enforce          # enforce | off; default off
  attempts: 5              # red verifies per change start
  attempts_total: 15       # red verifies per change, re-starts included
  replans: 3               # re-plans per change
  consecutive_blocks: 2    # blocked changes in a row per milestone
  context_relay: 0.6       # the loop's context fill that relays it
  relays: 5                # relays per loop run
  usage:                   # the usage guard before every ape task launch (v1.1)
    seven_day: 0.85        # stop over this 7-day window fill
    five_hour: 0.95        # hold over this 5-hour fill, with the launch's share
    ceiling: 0.95          # stop when the 7-day fill with the share is over it
    share: auto            # or a fraction
    max_age: 10m           # an older reading is unknown
    off: false             # true: no guard
```

| Key | Type | Default | Effect |
| --- | ---- | ------- | ------ |
| `budget` | `enforce` or `off` | `off` | With `enforce` and a `Budget:` line on the current approved milestone in `roadmap.md`, `ape task` refuses a session once the milestone's spend is at its budget (`milestone.budget-exceeded`, exit 1), and stops a running one when it gets there (termination `budget`, recorded in `_apex/state/limit-stops.json`). A person raises `Budget:` |
| `attempts` | integer ≥ 1 | 5 | The red verify that reaches it blocks the change. A re-plan resets this count |
| `attempts_total` | integer ≥ 1 | 15 | Red verifies across the change's re-starts, since a person last answered it |
| `replans` | integer ≥ 0 | 3 | Re-plans since a person last answered the change. The re-start past it blocks the change |
| `consecutive_blocks` | integer ≥ 1 | 2 | Changes of a milestone blocked in a row. Past it no change of the milestone is planned or started, and `ape task apex-loop` is refused (`milestone.blocks-exceeded`) until a person re-approves the milestone |
| `context_relay` | number, above 0 and at most 0.95 | 0.6 | The loop session's context fill at which its next child launch is refused and ape relays the loop to a fresh session (`session.relay`). Under 100,000 tokens of the loop's window it is the notice `limits.context-relay-low`: a fresh apex-loop session starts at about 46,000 tokens, so it would relay at once |
| `relays` | integer ≥ 0 | 5 | Relays per loop run. The relay past it stops the run (`limits.relays`, exit 1, action person) |
| `usage` | mapping | see below | The usage guard: what `ape task` does before it starts a session, given the plan's usage as the sessions ape launches last read it ([`ape usage`](cli.md#ape-usage)) |

#### `limits.usage`

Before every `ape task` session of an APEX v1 project starts (the loop's children, and the
sessions `ape change` starts, included), ape reads the newest reading of the claude.ai plan's
two windows, as fractions, and decides:

| Reading | Outcome |
| ------- | ------- |
| fresh, under every limit | launch |
| `five_hour` + share over `five_hour` | **hold** until the 5-hour window resets + 1 minute, a line every 10 minutes, then decide again. `ape status` shows the session held, `ape run wait --json` state `held` with `held_until` (exit 75, as for running). Nothing is logged: a hold resolves itself |
| `seven_day` over `seven_day`, or `seven_day` + share over `ceiling` | **stop**: the session is not started. Exit 14, termination `usage_guard` in the run manifest (with `resets_at`), the events `stop` (reason `usage-guard`) and `person` (action: the same command with `--usage-ok`). A person's call |
| no reading, or one older than `max_age` | launch, with "usage unknown (no reading in 10 min)" in the output and in the manifest's `usage.guard.note` |
| `off: true` | launch, nothing checked |

A window whose reset has passed since the reading counts as 0. `ape task --usage-ok` launches
whatever the reading says; it is a person's (refused in a model session, `session.person-only`,
exit 2) and is recorded with the reading. The decision is the run manifest's
[`usage.guard`](pipeline-run-manifest.md#usage--the-plans-usage-over-the-run).

| Key | Type | Default | Effect |
| --- | ---- | ------- | ------ |
| `seven_day` | number, above 0 and at most 1 | 0.85 | Over it, the launch stops for a person |
| `five_hour` | number, above 0 and at most 1 | 0.95 | Over it with the launch's share, the launch holds until the window resets |
| `ceiling` | number, above 0 and at most 1 | 0.95 | The 7-day fill with the launch's share over it stops the launch |
| `share` | `auto`, or a number above 0 and at most 1 | `auto` | The launch's estimated use of each window. `auto`: per window, the skill's median of (last − first reading) over its last 10 recorded runs on this account (`<user cache>/ape/usage-history.json`) whose window did not reset in between; 0.05 below 3 such runs. A number is the share of both windows |
| `max_age` | duration (`10m`, `1h`) | `10m` | A reading older than this is unknown |
| `off` | bool | `false` | `true`: no guard; the readings are still recorded |

A value outside these ranges fails the config's parse, as every limit does, and `ape project
check` reports a key of `limits.usage` it does not know as `config.unknown-key`. Guarded, like
the rest of `limits:`.

[The orchestrator and the loop](../explanation/orchestrator-and-loop.md#the-loop-relay) explains the relay.
An unknown key in this block is the `ape project check` finding `config.unknown-key`.

## `regression`

The project's own test suite, run from the project root: at a change's start (its baseline), at
verify, at a catch-up, and at the milestone gate. It fails only when it is newly red against the
baseline. Absent, there is no regression net. Guarded.

```yaml
regression: make test                                   # both tiers
regression: {change: make test, milestone: make test-all}
```

`change` runs at start and verify, and `milestone` at the gate (else `change`).
`milestone` without `change` is an error. When the two differ, the first change of a milestone to
start also runs `milestone` once and records it (`milestone_regression_baseline` in its
`state.json`), so the gate can report a suite that was red before the milestone's work as
`pre_existing_red` instead of failing on it.

## `timeouts`

How long one check, the regression command and a gate command may run. Each tier is a Go duration
(its max) or `{idle, max}`. A command is killed (exit 124) when it makes no progress for `idle`, or
when it reaches `max`. Progress means new output bytes, or CPU time used by its process group.
Guarded.

```yaml
timeouts:
  check: 20m                       # max 20m, idle the default
  regression: {idle: 10m, max: 1h}
  gate: {idle: 3m}                 # max the default
```

| Tier | Default idle | Default max | Hard ceiling |
| ---- | ------------ | ----------- | ------------ |
| `check` | 15m | 2h | 4h |
| `regression` | 30m | 2h | 8h |
| `gate` | 30m | 4h | 12h |

Any positive duration is accepted. A scalar's idle is the tier default, clamped to the max. An idle
above its max, or a max above the ceiling, is a config error. A command's `--timeout` flag sets
every tier's max, still capped at the ceiling. A timed-out command is not retried.

## `vet`

`plan` has apex-orchestrator vet each plan from a fresh session before it proposes it: it starts
`apex-vet` at depth 0, which writes numbered notes under `development/vet/`, and apex-plan answers
them before the proposal (v1.4, measured as the C1 stage). `off`, or no key, skips the vet. ape runs
nothing for it; it reads the value, and any other value is `config.invalid`. Not guarded: it adds a
review and loosens nothing.

## `retries`

How many times a red check runs again before it counts as red: 0 to 5, default 0. A pass on a retry
is green and recorded `flaky: true`, which `ape status` lists. Vacuous and unrun checks are not
retried. Guarded.

## `checks`

```yaml
checks:
  vacuous_output: ["no tests to run", "0 examples"]   # replaces the default list
  vacuous_output_extra: ["harness unavailable"]       # appended to the list in force (v1.4)
  slow_regression: 10m                                # regression.slow's threshold
```

A check that exits 0 but whose output says it ran nothing is red, reason `vacuous`. The default
phrases are `no tests to run` (go test), `running 0 tests` (cargo), `Ran 0 tests` (unittest before
3.12), `0 passing` (mocha) and `collected 0 items` (pytest). An empty list turns the rule off; an
empty phrase is an error. `vacuous_output_extra` (v1.4) adds the project's own phrases, such as a
harness's skip reason printed under `-v`, after the list in force, so the defaults need not be
copied back. Guarded.

Every check ape runs (verify, the gate, approve's self-check, the regression command) has
`APE_GATE=1` in its environment (v1.4). By convention a test harness that cannot set up what it
needs (a database, a container runtime) fails when it is set, instead of skipping every test and
reporting success. ape only exports it; the harness decides.

`slow_regression` (a Go duration, default `5m`): when `regression:` is a single command and its
last recorded run (a change's baseline or verify, or a milestone's last gate) took longer,
`ape project check` notices `regression.slow`: split it into a fast change tier and the whole suite
as the milestone tier.

## `requires`

What a check needs from the machine, as `NAME: env` or `NAME: cmd`, or, since v1.4, a readiness
probe as `NAME: {probe, when}`:

```yaml
requires:
  RADIX_DSN: env    # a check that mentions $RADIX_DSN needs it set
  docker: cmd       # a check that runs docker needs it on PATH
  dockerd: {probe: "docker info", when: "-tags=[^ ]*integration"}
  chrome: {probe: 'test -x "$CHROME_EXECUTABLE"', when: "--platform chrome"}
```

A probe is for what the command line does not name: a daemon a test suite needs, a browser, a
sibling checkout. `when` is a regular expression over a check's text (a scenario's, a task's, the
`Gate:` command or the regression command); without it the probe binds the checks that name it as
a word. ape runs each bound probe once per command, as a check (its own process group,
`APE_GATE=1`, idle 1 minute, at most 5), with its output in `_apex/state/logs/probe-<NAME>.log`;
when it does not exit 0, every check it binds is `unrun: <NAME> is not ready`. Not ready is never
green, and never a red the product caused. An ape before v1.4 cannot read the mapping form, so a
project using it needs ape 1.4.0.

A check whose requirement is missing is not run. It is red with reason `unrun`, never green.
Guarded.

## `caps`

Artefact size caps, each a default with a hard ceiling. A size is plain bytes or a number with
`B`, `KB`/`KiB` or `MB`/`MiB`, where a KB is 1024 bytes. Guarded.

| Key | Measures | Default | Ceiling |
| --- | -------- | ------- | ------- |
| `project_md` | `development/project.md` | 16 KB | 48 KB |
| `agents_md` | `AGENTS.md` | 16 KB | 48 KB |
| `change_md` | a change's `change.md`, without its `## Notes` | 16 KB | 48 KB |
| `design_md` | a change's `design.md` | 16 KB | 48 KB |
| `spec` | one capability's `spec.md` | 24 KB | 64 KB |
| `evidence_log` | one check's output in an evidence file | 1 MiB | 16 MiB |
| `architecture_md` | `development/architecture.md`, when present | 16 KB | 48 KB |
| `advise` | one `tools.advise` entry's `load:` files together | 8 KB | 16 KB |
| `advise_session` | the advise files one session of a change reads | 16 KB | 32 KB |

A value above its ceiling is `caps.over-ceiling`: `ape project check` reports it, and
`ape change check`, `start` and `archive` refuse until a person fixes the config.

## `docs`

```yaml
docs:
  changelog: CHANGELOG.md
  user_docs: docs/
  tested: [docs/examples/]
```

The changelog and user docs a user-visible change updates: `apex-change` adds the task and
`apex-review` checks it. All keys are optional and project-relative. `ape project check` reports a
configured path that does not exist (`docs.missing`), and `ape status --json` carries the block as
`docs`.

`tested` lists docs the project executes (doc-tests, tested examples). A change touching one is
never docs-only, so verify runs its regression command. Not guarded.

The block is not guarded, but it bounds what the default docs delegate may accept (see
[`human_review`](#human_review)). So while no configured delegate exists and `human_review.docs`
is not `person`, a `docs` block that a commit other than ape's changed and no person accepted, or
that differs uncommitted from HEAD's, is the `ape project check` notice `docs.unaccepted` (action
person): `ape change accept --delegate` refuses a docs change's task until a person runs
`ape project accept-config --by <who>`.

## `evidence`

`off` (the default) keeps check output in the ignored logs only. With `archive`, ape commits the
output with the record it proves:

- at archive, the final verify's checks go into the change's archive folder under `evidence/`;
- at a green `ape milestone close`, the gate run's go into
  `development/archive/milestones/<M>/evidence/`.

There is one file per check, capped at `caps.evidence_log`. Guarded.

## `commit`

The archive's squash commit. Guarded.

```yaml
commit:
  subject: "{type}: {title}"   # placeholders {type}, {title}, {id}
  hooks: run                   # run | skip
  types: [feat, fix, draft]    # the values change.md's type: may take
```

| Key | Default | Effect |
| --- | ------- | ------- |
| `subject` | `"{type}: {title}"` | The squash subject, one line, with no placeholder but the three. With the default, a change with no `type:` gets its title alone |
| `hooks` | `run` | Whether the project's git hooks run on the squash. ape's own record commits skip them either way |
| `types` | `feat`, `fix`, `perf`, `refactor`, `docs`, `test`, `build`, `ci`, `chore` | Replaces the list: lower-case tokens, none twice, at least one |

`ape status --json` reports the list in force as `config.commit_types`, and the subject template as
`config.commit_subject`.

## `notify`

A shell command ape runs on every event it logs in the project's event log,
`_apex/state/events.jsonl`, after the event's line is written:

```yaml
notify: ./scripts/notify.sh
```

The events are `person` (the project starts waiting on a person act), `gate-green`,
`milestone-done`, `run-end` (every `ape task` run), `run-lost`, `stop` (a run stopped on a limit)
and `block` (a change became blocked). It runs under `sh -c` from the project root, with
`APE_EVENT`, `APE_SEQ`, `APE_MILESTONE`, `APE_CHANGE`, `APE_RUN`, `APE_EXIT`, `APE_SKILL`,
`APE_REASON` and `APE_ACTION` (for `person`, the phrase the person types, such as `approve M3`) in
its environment. The timeout is 10 s, its output is dropped, and a failure is one warning line that
never changes the outcome. Not guarded. Every event and field:
[project-events.md](project-events.md); recipes (a desktop notification, the board, a webhook):
[How to hear from a project](../how-to/hear-from-a-project.md).

## `human_review`

Hands a change's human-review tasks to a skill instead of a person. Absent, only a person accepts
one, except for a docs change's docs check.

```yaml
human_review:
  delegate: apex-accept                  # the default when the block is present
  args: "--task {task} --change {id}"    # the default; placeholders {id}, {task}, {assets}, {change}
  model: sonnet                          # optional, else models.<delegate>
  effort: high                           # optional
  docs: person                           # optional: delegate (default) | person
```

A block with any key but `docs:` delegates every human-review task. A block with only `docs:`
delegates no other task. `docs: person` turns off the default delegation of a docs change's
accuracy check.

The delegate skill joins the loop's allow-list. `--delegate` refuses
(`delegate.unaccepted-config`) while the block is a change nobody accepted. Guarded.
See [The orchestrator and the loop](../explanation/orchestrator-and-loop.md#human-review-and-delegates).

## `release`

How `apex-release` proposes the next version, and what a release brings up to date. Guarded.

```yaml
release:
  scheme: semver           # semver | date (YYYY.MM.DD) | edition (the next integer)
  docs: [README.md, docs/]
```

`scheme` defaults to `semver`. `docs` is a list of relative paths, none twice. Absent, it is
`README.md` and `docs/`, those that exist. `ape status --json` reports both, as
`config.release_scheme` and `config.release_docs`.

## `secrets`

Lets values through the commit secret scan by fingerprint. Guarded.

```yaml
secrets:
  allow:
    - {fingerprint: "sha256:<64 hex>", reason: "a test fixture's fake key"}
```

The fingerprint is the sha256 of the secret's exact bytes, as the `commit.secret` refusal prints
it. `reason` is required. An entry counts only once a person has committed it and accepted the
config: the list is read from the newest `apex: config accepted` commit since the v1 epoch, else
the epoch. [Hooks](hooks.md#ape-hook-pre-commit) describes the scan.

## `marketplaces` and `plugins`

The Claude Code plugins the project uses. ape installs them at project scope through the
`claude plugin` CLI, and writes `extraKnownMarketplaces` and `enabledPlugins` into
`.claude/settings.json`. Both guarded.

```yaml
marketplaces:
  team-tools: {source: github, repo: org/claude-plugins, ref: v2}   # or {source: git, url: …}
                                                                    # or {source: directory, path: …}
plugins:
  - {name: nats-expert, marketplace: team-tools, ref: v1.4.0}
  - {name: kb, marketplace: team-tools, allow: [mcp]}
```

| Field | Meaning |
| ----- | ------- |
| `marketplaces.<name>.source` | `github` (needs `repo: <owner>/<repo>`), `git` (needs `url:`) or `directory` (needs `path:`, relative to the project root) |
| `marketplaces.<name>.ref` | The catalog's branch or tag, for `github` and `git`. A directory marketplace has none |
| `plugins[].name`, `marketplace` | The plugin, as `<name>@<marketplace>`. Each listed once |
| `plugins[].ref` | The version the install must report: a leading `v` is ignored, and a commit matches by prefix. Off it, ape refreshes the marketplace and updates the plugin. Empty accepts any version |
| `plugins[].allow` | What the plugin may carry besides skills: `hooks`, `mcp`. A plugin carrying hooks or MCP servers its entry does not allow is `plugin.hooks-or-mcp` (action person) |

Claude Code cannot pin a plugin version from the consumer side. To pin one, point the marketplace
at a ref.

`ape plugins status --outdated` lists, per marketplace, the version tags of its repository newer
than its `ref` (`git ls-remote --tags`), so a team that pins a marketplace tag sees what to bump.

A listed plugin also installed or enabled at user scope (`claude plugin list` scope `user`, or
`enabledPlugins` in the user's `settings.json`) is `plugins.user-scope` (action person): the user
copy, unpinned, stands in for the project's on that machine.

## `tools`

The skills the design and review steps may call, and the expert files the change, apply and
review sessions read, each with the condition that calls for it. Guarded: it widens what the loop
may start, and what text enters every apply session.

```yaml
tools:
  design:
    - {name: ui-mockup, skill: frontend-design:mockup, when: "the change adds or changes a screen",
       args: "{change} --out {assets}", model: sonnet, effort: high}
  review:
    - {name: security, skill: security-lens, when: "the change touches auth, secrets or input parsing",
       paths: ["internal/auth/**"], touches: [auth]}
  advise:
    - {name: nats, skill: nats-expert:nats-expert, when: "the change publishes, consumes or configures NATS",
       paths: ["internal/bus/**"], load: [advise.md], for: [change, apply, review]}
```

An entry is `{name, skill, when}`, with names unique across the three slots. A `design` or
`review` entry has three more optional keys:

- `args:`, the template ape fills into `ape task <skill> --args`, with the placeholders `{id}`,
  `{task}`, `{assets}` and `{change}` and no other;
- `model:`;
- `effort:`, a level.

Every entry may have two more (v1.1):

- `paths:`, globs relative to the project root (`**` any depth);
- `touches:`, capability slugs.

They make `when:` checkable. A change whose planned `paths:` or `touches:` (at
`ape change check`), or whose diff (at `ape change verify`), match an entry it does not list gets
the notice `tools.unmatched`. It is a warning, never a refusal: `when:` stays the planner's call.

A skill is `<plugin>:<skill>` (of a plugin listed in `plugins:`), or a local skill or command:
ape looks under the user's `.claude` (`$CLAUDE_CONFIG_DIR`, else `~/.claude`), then the project's,
and in each for `skills/<skill>/SKILL.md`, then `commands/<skill>.md`. The first found wins, so a
user skill or command wins over a project one of the same name, as in Claude Code. It must be
installed (else `tools.not-installed`). A
`design` or `review` skill must also be slash-invocable (else `tools.not-invocable`). A malformed
block is `tools.invalid`. Every `design` and `review` skill joins the loop's allow-list.
`apex-change` names a tool in a change, and the loop runs it with `ape change tool <id> <name>`.
That starts `ape task <skill>`, which finds a plugin skill the same way: an entry `ape project
check` calls installed also runs. The run of a plugin skill is filed under
`tasks/<plugin>__<skill>/`, because Windows does not allow a `:` in a folder name.

### `tools.advise`

An `advise` entry starts no session and gives no verdict. Its fields:

| Field | Meaning |
| ----- | ------- |
| `load` | Required. Files relative to the skill's folder: the folder holding its `SKILL.md`, in the plugin's install for a plugin skill, else in the user's or the project's `.claude/skills/<skill>/` (a user skill wins, as in Claude Code). For a command it is the `commands/` folder holding `<skill>.md`, shared with every other command ([below](#an-advise-entry-on-a-command)). No `..`, no absolute path |
| `for` | The sessions that read the files: `change`, `apply`, `review`. Default all three. A delegate never reads advise |
| `paths`, `touches` | As above |

`args`, `model` and `effort` are refused on an advise entry. The skill need not be
slash-invocable; `disable-model-invocation: true` keeps it out of every session's skill listing.

#### An advise entry on a command

A command has no folder of its own, so `load:` resolves against the whole `commands/` folder
(`.claude/commands/` in the project, or under the user's `.claude`). `load: [advise.md]` reads
`.claude/commands/advise.md`, whichever command the entry names. Claude Code also lists that file
as a command, `/advise`. Give an expert's files a skill folder, `.claude/skills/<skill>/`.

A change names the entries whose `when:` it matches in its `advise:` frontmatter, as it does
`review_tools:`. Then:

- `ape change check` refuses a name `tools.advise` does not declare (`tools.unknown`), and files over
  `caps.advise` per entry or `caps.advise_session` per session (`tools.too-big`);
- [`ape status --json`](status-json.md) lists each file under `changes[].advise`, with its absolute
  path, bytes and sha256;
- every `ape change verify` records the sha256s in `state.json`.

ape resolves, sizes and hashes the files, and never reads them for meaning.

## `library`

`true` marks a project whose package others download (W7). Absent, or `false`, it is not a
library. ape never writes the key: `ape project init` and `ape project lift` print a suggestion
for a Go module with no `main` package, and the person decides. Not guarded.

With `library: true`:

- a Go library with a `go.mod` at the root keeps one fence `go.mod` in each APEX folder that
  exists (`.claude/`, `_apex/`, the development folder, a root `evidence/`): `ape project fence`
  writes them, `ape project init` and the lift write the missing ones, and `ape project check`
  reports `fence.missing` and `fence.altered`;
- `ape project package-check` packs the library (the Go module zip, npm, Python, Cargo) and fails
  on any file under an APEX folder (`library.package-leak`); apex-release runs it;
- `ape project check` gives the notice `gate.root-module-change` for a gate program that imports
  what its module does not provide.

See [How to publish a library](../how-to/publish-a-library.md).

## Commands that act on the config

| Command | What it does with the config |
| ------- | ---------------------------- |
| [`ape project init`](cli.md#ape-project-init) | Writes the file, or appends `models:` to one without it; installs the listed plugins |
| [`ape project check`](cli.md#ape-project-check) | Reports `config.changed`, `caps.over-ceiling`, `models.*`, `docs.missing`, `tools.*`, `plugins.*`, and for `library: true` `fence.*` |
| [`ape project accept-config --by <who>`](cli.md#ape-project-accept-config) | A person accepts the guarded blocks as committed |
| [`ape project operator-interface`](cli.md#ape-project-operator-interface) | Renames a `models.apex-orchestrator` row to `apex-loop` (migration `v1.0.0_seq-07`) |
| [`ape project fence`](cli.md#ape-project-fence) | With `library: true` and a root `go.mod`, writes and commits the module fences |
| [`ape project package-check`](cli.md#ape-project-package-check) | With `library: true`, packs the library and fails on an APEX folder in it |
| [`ape plugins status`](cli.md#ape-plugins-status) | Reports each listed plugin's install, version against `ref`, enablement and `allow:`, and each `tools:` entry's state and advise files; `--outdated` lists each marketplace's newer tags |
| [`ape plugins sync`](cli.md#ape-plugins-sync) | Makes the installs and `.claude/settings.json` what the config says, committing a changed `settings.json` as `apex: plugins` |
| [`ape doctor --fix`](cli.md#ape-doctor) | Does what `plugins sync` does before the checks run (row `plugins.project`). It fixes nothing else |
| [`ape framework update`](cli.md#ape-framework-update) | Syncs the listed plugins after the install, as `plugins sync` does |
| [`ape config effort`](cli.md#ape-config-effort) | In a v1 project, reports the top-level `effort:` and each `models.<skill>` entry. Its table, `_apex/effort-defaults.yaml`, does not apply there |
| [`ape status --json`](cli.md#ape-status) | Carries `docs`, and `config` with the commit types and subject, and the release scheme and docs |

`ape task` also installs a missing plugin before it spawns. [`ape config pin`](../how-to/pin-a-config-key.md) pins
`evidence_folder`, a key of the APEX v0 config that the v1 reader does not use.
