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
- **Unknown top-level keys are ignored.**
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
| [`limits`](#limits) | mapping | budget off, attempts 5, attempts_total 15, replans 3, consecutive_blocks 2, context_relay 0.6, relays 5 | yes |
| [`regression`](#regression) | command, or `{change, milestone}` | none: no regression net | yes |
| [`timeouts`](#timeouts) | mapping per tier | check 15m idle / 2h max, regression 30m / 2h, gate 30m / 4h | yes |
| [`retries`](#retries) | integer 0–5 | 0 | yes |
| [`checks`](#checks) | `{vacuous_output: [phrases]}` | five runner phrases | yes |
| [`requires`](#requires) | map: name → `env` or `cmd` | none | yes |
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
| [`tools`](#tools) | `{design: [...], review: [...]}` | none | yes |

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
The operator, `apex-orchestrator`, has no row: it runs in your own session (notice
`models.operator-row`; see [The operator and the loop](../explanation/operator-and-loop.md#the-operator-is-your-session)).

Not guarded: a model choice loosens nothing.

## `effort`

The default effort of an ape-launched session that no flag, `models.<skill>` entry or parent
session decides: one of `low`, `medium`, `high`, `xhigh`, `max`. Absent, ape passes no effort and
Claude Code's own setting applies. A value outside the list fails the session's launch.
`_apex/effort-defaults.yaml` is not used in a v1 project.

## `limits`

The loop's limits, enforced by ape. Guarded: a change to them that no person accepted stops the
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
```

| Key | Type | Default | Effect |
| --- | ---- | ------- | ------ |
| `budget` | `enforce` or `off` | `off` | With `enforce` and a `Budget:` line on the current approved milestone in `roadmap.md`, `ape task` refuses a session once the milestone's spend is at its budget (`milestone.budget-exceeded`, exit 1), and stops a running one when it gets there (termination `budget`, recorded in `_apex/state/limit-stops.json`). A person raises `Budget:` |
| `attempts` | integer ≥ 1 | 5 | The red verify that reaches it blocks the change. A re-plan resets this count |
| `attempts_total` | integer ≥ 1 | 15 | Red verifies across the change's re-starts, since a person last answered it |
| `replans` | integer ≥ 0 | 3 | Re-plans since a person last answered the change. The re-start past it blocks the change |
| `consecutive_blocks` | integer ≥ 1 | 2 | Changes of a milestone blocked in a row. Past it no change of the milestone is planned or started, and `ape task apex-loop` is refused (`milestone.blocks-exceeded`) until a person re-approves the milestone |
| `context_relay` | number, above 0 and at most 0.95 | 0.6 | The loop session's context fill at which its next child launch is refused and ape relays the loop to a fresh session (`session.relay`) |
| `relays` | integer ≥ 0 | 5 | Relays per loop run. The relay past it stops the run (`limits.relays`, exit 1, action person) |

[The operator and the loop](../explanation/operator-and-loop.md#the-loop-relay) explains the relay.
Unknown keys in this block are ignored.

## `regression`

The project's own test suite, run from the project root: at a change's start (its baseline), at
verify, at a catch-up, and at the milestone gate. It fails only when it is newly red against the
baseline. Absent, there is no regression net. Guarded.

```yaml
regression: make test                                   # both tiers
regression: {change: make test, milestone: make test-all}
```

`change` runs at start and verify, and `milestone` at the gate (else `change`).
`milestone` without `change` is an error.

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

## `retries`

How many times a red check runs again before it counts as red: 0 to 5, default 0. A pass on a retry
is green and recorded `flaky: true`, which `ape status` lists. Vacuous and unrun checks are not
retried. Guarded.

## `checks`

```yaml
checks:
  vacuous_output: ["no tests to run", "0 examples"]   # replaces the default list
```

A check that exits 0 but whose output says it ran nothing is red, reason `vacuous`. The default
phrases are `no tests to run` (go test), `running 0 tests` (cargo), `Ran 0 tests` (unittest before
3.12), `0 passing` (mocha) and `collected 0 items` (pytest). An empty list turns the rule off; an
empty phrase is an error. Guarded.

## `requires`

What a check needs from the machine, as `NAME: env` or `NAME: cmd`:

```yaml
requires:
  RADIX_DSN: env    # a check that mentions $RADIX_DSN needs it set
  docker: cmd       # a check that runs docker needs it on PATH
```

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

A shell command ape runs when an `ape task` run stops on a limit (a usage limit, the budget), and
when a change becomes blocked:

```yaml
notify: ./scripts/notify.sh
```

It runs under `sh -c` from the project root, with `APE_EVENT` (`stop` or `block`), `APE_CHANGE` and
`APE_REASON` in its environment. The timeout is 10 s, its output is dropped, and a failure is one
warning line that never changes the outcome. Not guarded.

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
See [The operator and the loop](../explanation/operator-and-loop.md#human-review-and-delegates).

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

## `tools`

The skills the design and review steps may call, each with the condition that calls for it.
Guarded: it widens what the loop may start.

```yaml
tools:
  design:
    - {name: ui-mockup, skill: frontend-design:mockup, when: "the change adds or changes a screen",
       args: "{change} --out {assets}", model: sonnet, effort: high}
  review:
    - {name: security, skill: security-lens, when: "the change touches auth, secrets or input parsing"}
```

An entry is `{name, skill, when}`, with names unique across both slots, plus three optional keys:

- `args:`, the template ape fills into `ape task <skill> --args`, with the placeholders `{id}`,
  `{task}`, `{assets}` and `{change}` and no other;
- `model:`;
- `effort:`, a level.

A skill is `<plugin>:<skill>` (of a plugin listed in `plugins:`), a project skill
(`.claude/skills/<skill>/`) or a user skill. It must be installed (else `tools.not-installed`) and
slash-invocable (else `tools.not-invocable`). A malformed block is `tools.invalid`. Every skill
named here joins the loop's allow-list. `apex-change` names a tool in a change, and the loop runs it
with `ape change tool <id> <name>`.

## Commands that act on the config

| Command | What it does with the config |
| ------- | ---------------------------- |
| [`ape project init`](cli.md#ape-project-init) | Writes the file, or appends `models:` to one without it; installs the listed plugins |
| [`ape project check`](cli.md#ape-project-check) | Reports `config.changed`, `caps.over-ceiling`, `models.*`, `docs.missing`, `tools.*`, `plugins.*` |
| [`ape project accept-config --by <who>`](cli.md#ape-project-accept-config) | A person accepts the guarded blocks as committed |
| [`ape project operator-interface`](cli.md#ape-project-operator-interface) | Renames a `models.apex-orchestrator` row to `apex-loop` (migration `v1.0.0_seq-07`) |
| [`ape plugins status`](cli.md#ape-plugins-status) | Reports each listed plugin's install, version against `ref`, enablement and `allow:`, and each `tools:` entry's state |
| [`ape plugins sync`](cli.md#ape-plugins-sync) | Makes the installs and `.claude/settings.json` what the config says, committing a changed `settings.json` as `apex: plugins` |
| [`ape doctor --fix`](cli.md#ape-doctor) | Does what `plugins sync` does before the checks run (row `plugins.project`). It fixes nothing else |
| [`ape framework update`](cli.md#ape-framework-update) | Syncs the listed plugins after the install, as `plugins sync` does |
| [`ape config effort`](cli.md#ape-config-effort) | In a v1 project, reports the top-level `effort:` and each `models.<skill>` entry. Its table, `_apex/effort-defaults.yaml`, does not apply there |
| [`ape status --json`](cli.md#ape-status) | Carries `docs`, and `config` with the commit types and subject, and the release scheme and docs |

`ape task` also installs a missing plugin before it spawns. [`ape config pin`](../how-to/pin-a-config-key.md) pins
`evidence_folder`, a key of the APEX v0 config that the v1 reader does not use.
