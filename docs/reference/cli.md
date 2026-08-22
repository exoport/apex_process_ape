# ape CLI reference

> Generated from the command tree by `make docs-cli` (which runs the hidden
> `ape gen-docs`). Do not edit by hand — change the command definitions in
> `internal/apecmd/` and regenerate. PLAN-9 F4.

## ape

APE — APEX Process Engine CLI

```
ape
```

ape runs APEX framework work against your project through an
interactive Claude Code REPL.

Common commands:
  ape pipeline <name>   Run a multi-stage pipeline (design, governance, epics).
  ape task <skill>      Run a single framework skill without a pipeline YAML.
  ape chat              Open an interactive Claude session in the project.
  ape costs             Show this project's Claude cost rollup.

Also: framework setup/update, doctor, sessions, planning, trait/pattern/adr
inspection. Every claude invocation runs in an in-process PTY — there is no
"claude -p" programmatic path.

Subcommands:

- `adr` — Manage Architecture Decision Records
- `bootstrap` — Bootstrap governance artifacts from traits
- `capability` — Inspect and maintain the capability registry
- `chat` — Bridged claude REPL with hooks captured to a runlog
- `config` — Resolve the project's APEX configuration
- `costs` — Show this project's Claude cost rollup
- `deferred` — The deferred-work record store
- `doc` — Shard, assemble and survey Markdown documents
- `doctor` — Probe the local environment for prerequisites
- `event` — Publish a session progress event over NATS
- `feature` — Inspect and maintain the feature registry
- `framework` — Install and inspect APEX framework assets in a project
- `log` — Publish a structured log record over NATS
- `memory` — Read the team-memory file without loading it whole
- `metrics` — Scan and publish this session's usage metrics over NATS
- `pattern` — Manage governance patterns
- `pipeline` — List or run an APEX pipeline
- `planning` — Show the planning pipeline diagram
- `prompt` — Drive an unattended Claude session from a prompt or a handoff file
- `registry` — Verify or reconcile every record registry at once
- `rollback` — Rollback ape to the previous version
- `sandbox` — Provision and operate hardware-isolated Kata VM workspaces (via aped)
- `script` — Run a Go orchestration script through the yaegi interpreter
- `service` — Run a NATS-micro job daemon that accepts pipeline/task jobs over request/reply
- `sessions` — List, prune, or open the URL of live ape sessions
- `sprint` — Inspect and maintain sprint-status.yaml
- `story` — Project and verify story frontmatter
- `task` — Run a single framework skill through the interactive PTY runner
- `trait` — Manage and inspect traits
- `transcript` — Work with Claude session transcripts
- `update` — Update ape to the latest version
- `version` — Print version information

## ape adr

Manage Architecture Decision Records

```
ape adr
```

Aliases: `adrs`

Subcommands:

- `list` — List all ADRs
- `new` — Scaffold a new ADR file
- `sync` — Reconcile the adrs index against records on disk
- `update` — Apply field deltas to existing adrs index entries
- `verify` — Verify the adrs registry against its index

## ape adr list

List all ADRs

```
ape adr list [flags]
```

Examples:

```
  ape adr list --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape adr new

Scaffold a new ADR file

```
ape adr new <title>
```

## ape adr sync

Reconcile the adrs index against records on disk

```
ape adr sync [flags]
```

Reconcile index.yaml against the records on disk: records with no entry
are added, entries whose id no record claims are removed, and an entry
whose file: no longer resolves is repointed at the record claiming its id.

This is the repair for the findings `verify` reports, and nothing more —
it copies what a record's own frontmatter states and invents no titles,
statuses or any other field. A renamed record keeps its authored entry
rather than being dropped and re-added.

--check makes it a dry run: the same diff, nothing written. generated_at
moves only when something else did.

Examples:

```
  ape adr sync --check
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--check` | bool | `false` | Report the diff without writing |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape adr update

Apply field deltas to existing adrs index entries

```
ape adr update [flags]
```

Apply per-entry field deltas to index.yaml and refresh generated_at.

--updates takes {"<id>": {"<field>": "<value>"}} as a file path or '-' for
stdin. Only entries ALREADY listed may be updated: an unknown id is an
error raised before anything is written, because creating an index entry
is the job of the skill that authors the document it points at.

The rendered index is round-trip parsed before it replaces the file, and
the write is atomic — a crash mid-write cannot truncate an index. Key
order and comments survive, which a PyYAML round trip does not manage.

Exit codes:
  0  applied
  1  unknown id, unreadable updates, or an unwritable index (nothing written)

Examples:

```
  echo '{"ADR-0001":{"status":"superseded"}}' | ape adr update --updates -
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--generated-at` | string | `—` | Timestamp to write as generated_at (default: resolved config timestamp) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--updates` | string | `-` | JSON file with per-entry field deltas, or '-' for stdin |

## ape adr verify

Verify the adrs registry against its index

```
ape adr verify [flags]
```

Exactly four checks, and no others:

  1. registry.orphan_record / registry.phantom_entry
     set equality between the record directory and index.yaml, both
     directions
  2. registry.file_unresolved
     every index file: resolves, relative to the index's own directory
  3. registry.duplicate_id
     duplicate ids, in the index and on disk
  4. registry.record_unparseable
     the record parses as frontmatter at all

No schema validation, no field drift, no tag comparison, no updated_at
comparison — those are judgment, and a verifier that wanders into them
stops being trustworthy.

An index.yaml that is absent while records exist is reported once, as
registry.index_missing: the degenerate case of check 1, not a fifth check.
One finding beats one orphan per record, which would bury the only fact
that matters.

Findings travel in the payload. Exit is 0 even with findings unless
--strict is passed, so this is safe to call from anywhere.

Examples:

```
  ape adr verify --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--strict` | bool | `false` | Exit 1 when there are findings (default: report and exit 0) |

## ape bootstrap

Bootstrap governance artifacts from traits

```
ape bootstrap [flags]
```

Bootstrap a project's governance artifacts by composing traits from the catalog.

Examples:

```
  ape bootstrap --traits go-service,http-api
  ape bootstrap --no-picker --traits go-service --dry-run
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--dry-run` | bool | `false` | Print what would be generated without writing files |
| `--no-picker` | bool | `false` | Disable the interactive trait picker (TUI) |
| `--on-conflict` | string | `first` | Conflict resolution strategy: first\|last\|all\|error |
| `--out` | string | `.` | Output directory for generated artifacts |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--traits` | string | `—` | Comma-separated list of trait names |

## ape capability

Inspect and maintain the capability registry

```
ape capability
```

Aliases: `capabilities`

Subcommands:

- `list` — List capabilities from the registry index
- `sync` — Reconcile the capabilities index against records on disk
- `update` — Apply field deltas to existing capabilities index entries
- `verify` — Verify the capabilities registry against its index

## ape capability list

List capabilities from the registry index

```
ape capability list [flags]
```

Examples:

```
  ape capability list --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape capability sync

Reconcile the capabilities index against records on disk

```
ape capability sync [flags]
```

Reconcile index.yaml against the records on disk: records with no entry
are added, entries whose id no record claims are removed, and an entry
whose file: no longer resolves is repointed at the record claiming its id.

This is the repair for the findings `verify` reports, and nothing more —
it copies what a record's own frontmatter states and invents no titles,
statuses or any other field. A renamed record keeps its authored entry
rather than being dropped and re-added.

--check makes it a dry run: the same diff, nothing written. generated_at
moves only when something else did.

Examples:

```
  ape capability sync --check
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--check` | bool | `false` | Report the diff without writing |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape capability update

Apply field deltas to existing capabilities index entries

```
ape capability update [flags]
```

Apply per-entry field deltas to index.yaml and refresh generated_at.

--updates takes {"<id>": {"<field>": "<value>"}} as a file path or '-' for
stdin. Only entries ALREADY listed may be updated: an unknown id is an
error raised before anything is written, because creating an index entry
is the job of the skill that authors the document it points at.

The rendered index is round-trip parsed before it replaces the file, and
the write is atomic — a crash mid-write cannot truncate an index. Key
order and comments survive, which a PyYAML round trip does not manage.

Exit codes:
  0  applied
  1  unknown id, unreadable updates, or an unwritable index (nothing written)

Examples:

```
  echo '{"CAP-1":{"status":"superseded"}}' | ape capability update --updates -
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--generated-at` | string | `—` | Timestamp to write as generated_at (default: resolved config timestamp) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--updates` | string | `-` | JSON file with per-entry field deltas, or '-' for stdin |

## ape capability verify

Verify the capabilities registry against its index

```
ape capability verify [flags]
```

Exactly four checks, and no others:

  1. registry.orphan_record / registry.phantom_entry
     set equality between the record directory and index.yaml, both
     directions
  2. registry.file_unresolved
     every index file: resolves, relative to the index's own directory
  3. registry.duplicate_id
     duplicate ids, in the index and on disk
  4. registry.record_unparseable
     the record parses as frontmatter at all

No schema validation, no field drift, no tag comparison, no updated_at
comparison — those are judgment, and a verifier that wanders into them
stops being trustworthy.

An index.yaml that is absent while records exist is reported once, as
registry.index_missing: the degenerate case of check 1, not a fifth check.
One finding beats one orphan per record, which would bury the only fact
that matters.

Findings travel in the payload. Exit is 0 even with findings unless
--strict is passed, so this is safe to call from anywhere.

Examples:

```
  ape capability verify --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--strict` | bool | `false` | Exit 1 when there are findings (default: report and exit 0) |

## ape chat

Bridged claude REPL with hooks captured to a runlog

```
ape chat [flags]
```

Spawn claude as a child of ape with the ape bridge attached.
Bridge hooks (PreToolUse, PostToolUse, UserPromptSubmit, Stop, and
friends) are captured to <project>/_output/ape/chats/<id>/ alongside
pipeline runs.

ape chat must be run from a project root (a directory containing
_apex/config.yaml).

While attached:
  /exit, /quit       exit claude (default slash commands)
  Ctrl+D in claude   exits the REPL

ape exits when claude exits. The chat session is bound to this
terminal for its lifetime — there is no detach/reattach. To run
claude in the background, use a real terminal multiplexer
separately (e.g. wrap ape chat in tmux or screen).

Exit codes: 0 success · 1 claude/bridge failure · 2 usage or preflight
error (no _apex/config.yaml, bad cwd).

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working directory). |
| `--effort` | string | `—` | Reasoning effort for the session and its sub-agents (low\|medium\|high\|xhigh\|max). Defaults to claude's native effort when unset. |
| `--ignore-project-settings` | bool | `false` | Tell claude to skip project + local .claude/settings*.json. |
| `--model` | string | `—` | Initial claude model. A bare family (sonnet, opus, haiku) resolves to its current generation; sonnet-5 / opus[1m] pin explicitly. Empty falls back to claude's default. |

## ape config

Resolve the project's APEX configuration

```
ape config
```

Subcommands:

- `resolve` — Resolve _apex/config.yaml + the config.local.yaml overlay

## ape config resolve

Resolve _apex/config.yaml + the config.local.yaml overlay

```
ape config resolve [flags]
```

Walk up from --cwd for _apex/config.yaml, overlay _apex/config.local.yaml
key-wise, and emit the seventeen folder/name variables every framework
skill resolves on activation — plus the four derived ext_* flags, the
absolute paths those folders denote, and the local date/timestamp.

An absent config.local.yaml is a normal outcome (local_overlay_applied:
false). A config.local.yaml that exists but does not parse is a hard
failure, not a fall-back to base values: this resolution is the first act
of every skill, so one typo'd override would otherwise run a whole
pipeline against folders nobody chose.

governance_repository_path names the CANONICAL governance repository that
apex-adr-reconciliation / apex-pattern-reconciliation import from. It is
not an alternative home for this project's own records — those always
live under governance_folder, which is what paths.adrs and paths.patterns
report.

Exit codes:
  0  resolved
  2  a config file exists but is malformed (the message names it)
  4  no _apex/config.yaml in --cwd or any parent

Examples:

```
  ape config resolve --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root to resolve from (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape costs

Show this project's Claude cost rollup

```
ape costs [flags]
```

Reads <project>/_output/ape/cost-rollup.json and prints
totals — today, this week, all-time — broken down per pipeline + chat.

  ape costs                          Project rollup (human / json).
  ape costs run <run-id>             Single pipeline run (reads manifest.yaml).
  ape costs chat <chat-id>           Single chat session (reads session.yaml).
  ape costs prompt <prompt-id>       Single prompt session (reads prompt.yaml).
  ape costs update --from <file>     Refresh the price table from a YAML file.
  ape costs coverage                 Check the price table against the models
                                     Claude Code is actually emitting locally.
  ape costs reprice                  Recompute stored costs from on-disk tokens
                                     using the current price table.
  ape costs roll                     Force a project rollup rebuild from all
                                     run / chat directories.

Subcommands:

- `chat` — Show cost for a single chat session (reads its session.yaml)
- `coverage` — Check the built-in price table against the models Claude Code is actually emitting
- `prompt` — Show cost for a single prompt session (reads its prompt.yaml)
- `reprice` — Recompute stored costs from on-disk token counts using the current price table
- `roll` — Rebuild <project>/_output/ape/cost-rollup.json from on-disk run / chat artefacts
- `run` — Show cost for a single pipeline or task run (reads its manifest.yaml)
- `update` — Persist model price overrides from a YAML file to ~/.ape/prices.yaml

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | human \| json |

## ape costs chat

Show cost for a single chat session (reads its session.yaml)

```
ape costs chat <chat-id> [flags]
```

Examples:

```
  ape costs chat 0a675bc4
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | human \| json |

## ape costs coverage

Check the built-in price table against the models Claude Code is actually emitting

```
ape costs coverage [flags]
```

Sweep the local Claude Code transcripts (~/.claude/projects) and report
how this ape binary prices every model id it finds.

Each observed model resolves to one of:

  exact      a rate for this exact model id (built-in table, an override
             in ~/.ape/prices.yaml, or a dated promotional window)
  family     no exact row — priced from the model's family tier. Close,
             but an approximation, and flagged as one everywhere.
  unpriced   nothing matched. Those turns contribute $0.00 to every
             total, which is not the same as being free.

--strict exits 2 when any observed model is not exactly priced. A sweep
that finds no transcripts exits 0 and says so: absence of evidence is not
coverage, so CI (which has no transcripts) skips rather than passes.

Exit codes:
  0  every observed model exactly priced, or nothing observed
  2  --strict and at least one model is estimated or unpriced

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--days` | int | `30` | Only read transcripts modified in the last N days (0 = all) |
| `--output-format` | string | `human` | human \| json \| yaml |
| `--strict` | bool | `false` | Exit 2 when any observed model is not exactly priced |

## ape costs prompt

Show cost for a single prompt session (reads its prompt.yaml)

```
ape costs prompt <prompt-id> [flags]
```

Examples:

```
  ape costs prompt 20260713-120102-a1b2c3d
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | human \| json |

## ape costs reprice

Recompute stored costs from on-disk token counts using the current price table

```
ape costs reprice [flags]
```

Walk this project's run artefacts and recompute every cost_usd from the
per-model token counts stored alongside it.

Use this after correcting the price table (a new model id added to
internal/cost/prices.yaml, or an override persisted via
`ape costs update --from`) to fix runs that were recorded while the
table was stale.

Artefacts covered:
  _output/{pipelines,tasks}/<name>/<run-id>/manifest.yaml
  _output/ape/prompts/<prompt-id>/prompt.yaml

Chat session.yaml has no per-model breakdown, so there is nothing to
reprice from — those are skipped.

Dry run by default: it prints what would change and touches nothing. Pass
--write to apply, then run `ape costs roll` to refresh the rollup cache.
Only cost_usd scalars are rewritten; key order, comments, and every other
field survive the round-trip.

A model that is STILL unpriced cannot be fixed by repricing — its stored
cost is left alone and the model is listed in the report so you know the
total remains a lower bound.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | human \| json \| yaml |
| `--write` | bool | `false` | Apply the recomputed costs (default: dry run) |

## ape costs roll

Rebuild <project>/_output/ape/cost-rollup.json from on-disk run / chat artefacts

```
ape costs roll
```

## ape costs run

Show cost for a single pipeline or task run (reads its manifest.yaml)

```
ape costs run <run-id> [flags]
```

Examples:

```
  ape costs run 20260703-120102-a1b2c3
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | human \| json |

## ape costs update

Persist model price overrides from a YAML file to ~/.ape/prices.yaml

```
ape costs update [flags]
```

Reads a YAML file in the shape:

  prices:
    claude-opus-4-7:
      base_input: 5.00
      output: 25.00
    claude-sonnet-4-6:
      base_input: 3.00
      output: 15.00

and persists it to ~/.ape/prices.yaml. Subsequent ape invocations
prefer these values over the built-in price table (cost.Prices).
PLAN-5 / C7.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--from` | string | `—` | Path to a YAML file with model price overrides |

## ape deferred

The deferred-work record store

```
ape deferred
```

One file per deferred record under {development_folder}/deferred/, with
YAML frontmatter and a verbatim Markdown body.

This replaces a single 456,144-byte deferred-work.md whose only eviction
mechanism was deletion — git shows 524 records removed in one commit — and
which had grown too large for the skills that append to it to read.

One file per record is not a style choice: the single-store alternative
returns ZERO records when handed one malformed entry, while this shape
loses exactly the one bad file.

The store sits OUTSIDE {implementation_folder} deliberately. Ten skills
glob {implementation_folder}/**/*.md across 17 sites, and 227 record files
under that folder would feed every one of them.

Subcommands:

- `close` — Discharge a record, moving it to closed/
- `ingest` — Store defer bullets as records (bullets on stdin)
- `list` — List deferred records (open by default)
- `migrate` — Convert the legacy deferred-work.md into record files
- `repair` — Complete or retire free-form records (judgment, on opus)
- `verify` — Check the store: invariants, and candidates for a human

## ape deferred close

Discharge a record, moving it to closed/

```
ape deferred close <id> [flags]
```

Mark a record closed and move it to closed/. The record is NEVER deleted:
deletion is what destroyed the audit trail the first time, and an LLM that
cannot see a closed defer re-files it.

The body gains a discharge marker in the shape apex-pattern-reconciliation
established, and keeps everything it already said.

Closing is judgment — it requires re-verifying the record's premises
against HEAD — so this is invoked by a person or a skill, never
automatically. 'ape deferred verify' flags CANDIDATES and never closes one.

Examples:

```
  ape deferred close DW-20260822-a1b2c3 --by "54-2"
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--by` | string | `—` | Story key or reason that discharged it (required) |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape deferred ingest

Store defer bullets as records (bullets on stdin)

```
ape deferred ingest [flags]
```

Read defer bullets and write one record per bullet.

Input comes from --body-file or stdin, NEVER from an argv string: 108 of
109 real bodies contain backticks, which shell-expand inside an argument.
--body-file is the form the framework calls, because
apex-review-story/steps/step-04-present.md:13 restricts shell constructs
inside code blocks and a plain path argument sidesteps the question
entirely. The skill writes the bullets with the Write tool (never a
heredoc) and passes the path.

EXIT-CODE CONTRACT — this command is reachable from apex-review-story's
emit path, where a non-zero exit converts a defer into a patch, raises
unfixed_patches, and DEMOTES THE STORY to in-progress. Therefore:

  - a bullet whose shape is unrecognised is stored verbatim as free-form,
    with a warning on stderr;
  - empty input is a rc-0 no-op;
  - only a genuine setup failure (an unwritable store) fails.

Nothing about the CONTENT of a defer can make this command exit non-zero.

Examples:

```
  ape deferred ingest --story 54-1 --skill apex-review-story --body-file /tmp/defer-54-1.txt
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--body-file` | string | `—` | File holding the bullets (default: stdin) |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--cycle` | int | `0` | Review cycle number |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--skill` | string | `—` | Skill that filed it (e.g. apex-review-story) |
| `--story` | string | `—` | Story key this defer was filed from |

## ape deferred list

List deferred records (open by default)

```
ape deferred list [flags]
```

Project the record set. Open records only unless --status says otherwise:
closed records stay on disk but leave the working set, which is what stops
an LLM re-filing work it already did.

--detail picks how much the human rendering shows; --output-format picks
the encoding. They are separate axes.

Examples:

```
  ape deferred list --owner platform
  ape deferred list --status all --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--detail` | string | `brief` | Human rendering detail: brief\|full |
| `--group` | string | `—` | Only records in this group |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--owner` | string | `—` | Only records with this owner |
| `--path` | string | `—` | Only records anchored under this path prefix |
| `--status` | string | `open` | Which records: open\|closed\|all |
| `--story` | string | `—` | Only records filed from this story |

## ape deferred migrate

Convert the legacy deferred-work.md into record files

```
ape deferred migrate [flags]
```

Convert a single-file deferred-work.md into one file per record.

Four properties, all asserted rather than assumed:

  VERIFIED BEFORE WRITE   N records parsed must equal N files written and
                          every body must survive byte-for-byte, or NOTHING
                          is written. A lossy conversion that passed
                          silently is the one failure here that git cannot
                          undo.
  IDEMPOTENT              detected from disk state — does the store hold
                          records, is the legacy file already a stub. No
                          version marker is stored, so nothing can drift.
  NEVER DELETES THE SOURCE  the legacy file becomes a short signpost; its
                          content stays in git.
  NO COMMIT               the files land in the working tree. You commit
                          them, as one commit or two, however you like.

A record whose tail does not match the expected shape keeps its full text
as the body and takes its title from the first line — 26 of 109 records in
the reference ledger are free-form, so that path always runs. Nothing is
dropped and nothing is guessed at; 'ape deferred verify' flags them, and
'ape deferred repair' completes or retires them.

--recover-deleted mines the ledger's git history for records removed from
it and writes them straight to closed/. The ledger's own preamble
documents 'git log -p' as the recovery route; this automates exactly that.
Failure to read history is a warning, never fatal.

Examples:

```
  ape deferred migrate --dry-run
  ape deferred migrate --recover-deleted
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--dry-run` | bool | `false` | Parse and verify, writing nothing |
| `--from` | string | `—` | Legacy ledger path (default: resolved from config) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--recover-deleted` | bool | `false` | Also recover records removed from the ledger, from git history, into closed/ |

## ape deferred repair

Complete or retire free-form records (judgment, on opus)

```
ape deferred repair [flags]
```

Dispatch the free-form records to a framework skill for completion.

The deterministic migration cannot finish a record that has no fields to
read. Deciding whether a messy note is real work, what it points at, and
whether it is already dead is JUDGMENT — so an LLM does it, in its own
phase, never inside the verified migration. That separation is what keeps
the migration assertable and revertible: a model's output can never
invalidate a byte-identity check.

Mechanism: this spawns apex-deferred-repair on opus through
the same PTY task runner `ape task` uses. The prompt lives in the
framework as a versioned, reviewable skill rather than a Go string
literal, so ape gains no HTTP client, no credentials and no model
constant. apex-defer-repair is accepted as the pre-rename name, so
an older framework install still works.

Three guards this command adds:

  It REFUSES when neither skill is installed, --dry-run included. A real
  dispatch is already caught by the runner's skill preflight, but a dry
  run never reaches the runner — so without this it would print a plan
  naming a skill that does not exist, say "no session spawned", and read
  as fine.

  It REFUSES without a TTY unless --force. It spends real money, and it
  should not do that from a script that did not ask — the same refusal
  'ape framework setup' already makes rather than seeding silently.

  The on-disk record count MUST NOT FALL. "discard never deletes" is an
  absolute rule that otherwise lives only in a prompt, and a prompt is not
  an enforcement mechanism. If records vanish, this says so and names
  them, so you can restore those paths before committing anything.

Nothing is committed.

Examples:

```
  ape deferred repair --dry-run
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--dry-run` | bool | `false` | Show the plan without spawning a session |
| `--force` | bool | `false` | Spawn the session even without a TTY |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape deferred verify

Check the store: invariants, and candidates for a human

```
ape deferred verify [flags]
```

Aliases: `lint`

Two kinds of finding, and the tag on each is the important part:

  confidence: certain    a fact. Schema problems, and related[]/supersedes[]
                         pointing at records that do not exist.
  confidence: candidate  a heuristic, NEVER auto-actionable. A dead anchor
                         (the record may be moot, or the code may just have
                         moved), a trigger naming a story that is now done
                         (the closing condition MAY have fired), a
                         near-duplicate title, a free-form record.

Nothing here ever closes a record. Closing requires re-verifying the
premises against HEAD, which is judgment — and on the reference ledger an
unbiased sample of 20 records found 5 already delivered, including its own
flagship entry, whose defect had been eliminated four weeks earlier.

Exit 0 even with findings; --strict makes it 1.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--strict` | bool | `false` | Exit 1 when there are findings (default: report and exit 0) |

## ape doc

Shard, assemble and survey Markdown documents

```
ape doc
```

Aliases: `docs`

Markdown document operations.

  verify    refuse-to-shard gate: duplicate heading slugs at a level
  shard     split a document into section files, rewriting relative links
  assemble  concatenate the shards back, reversing the rewrite
  analyze   survey a set of source documents: sizes, groups, routing

Subcommands:

- `analyze` — Survey source documents: sizes, groups, routing
- `assemble` — Concatenate shards back into one document
- `shard` — Split a document into section files
- `verify` — Check a document for duplicate heading slugs

## ape doc analyze

Survey source documents: sizes, groups, routing

```
ape doc analyze <path|dir|glob>... [flags]
```

Enumerate source documents and report sizes, estimated tokens, detected
types, suggested groupings, a routing recommendation and a split
prediction.

Inputs may be file paths, directories (walked for .md/.txt/.yaml/.yml/
.json) or glob patterns. node_modules, .git, __pycache__, .venv, .claude,
.cursor and .vscode are never source documents.

Routing is 'single' when the corpus is BOTH at or under the file limit and
at or under the token limit; otherwise 'fan-out'. The split prediction
estimates a distillate at a third of its sources.

Every token number here is bytes/4 — an estimate, labelled as one, and
nothing gates on it. The three thresholds are flags so the boundaries are
testable without building a 15k-token fixture; the defaults are the ones
the framework has always used.

This command carries no --cwd. It resolves nothing from the project's
config — every input is an explicit path argument — so there is no
project for --cwd to select. Relative paths resolve against the
working directory, as they would for any other tool.

Examples:

```
  ape doc analyze _output/handoffs --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--single-max-files` | int | `0` | File count at or below which routing is single (default 3) |
| `--single-max-tokens` | int64 | `0` | Estimated tokens at or below which routing is single (default 15000) |
| `--split-min-tokens` | int64 | `0` | Estimated distillate tokens above which a split is likely (default 5000) |

## ape doc assemble

Concatenate shards back into one document

```
ape doc assemble <dir> <file> [flags]
```

Reverse a shard: concatenate the section files in index.md order and
strip the ../ that sharding added.

index.md supplies the ordering and its preamble, and is never itself
concatenated as a section. A ../ link that predated the shard survives
untouched, because the strip only applies when the target actually resolves
one level up.

A section file named in the index but missing on disk is reported and
skipped rather than fatal — an incomplete assembly you can see beats none.

This command carries no --cwd. It resolves nothing from the project's
config — every input is an explicit path argument — so there is no
project for --cwd to select. Relative paths resolve against the
working directory, as they would for any other tool.

Examples:

```
  ape doc assemble development/planning/prd development/planning/prd.md
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape doc shard

Split a document into section files

```
ape doc shard <file> <dir> [flags]
```

Split a document at a heading level, one file per section, and rewrite
relative links to account for the extra directory depth.

It ALWAYS writes an index.md listing and linking every section file. That
is a contract, not a convenience: apex-shard-doc treats a missing index.md
as proof the command did not complete, and verifies it in its own step. A
replacement that split and rewrote links perfectly but omitted the index
would pass a round-trip test and then fail its real caller.

It REFUSES when two headings slugify to the same value, rather than writing
'foo-2.md' siblings that hardcoded consumers cannot distinguish. Nothing is
written in that case — fix the source document.

A document with no headings at the requested level becomes index.md whole,
so 'assemble' still has something to work from.

This command carries no --cwd. It resolves nothing from the project's
config — every input is an explicit path argument — so there is no
project for --cwd to select. Relative paths resolve against the
working directory, as they would for any other tool.

Examples:

```
  ape doc shard development/planning/prd.md development/planning/prd
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--level` | int | `2` | Heading level to split at |
| `--numbered` | bool | `false` | Prefix filenames with a zero-padded ordinal |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape doc verify

Check a document for duplicate heading slugs

```
ape doc verify <file> [flags]
```

Scan a document for two headings at the same level that slugify to the
same value.

This is a GATE, not a report — the one exception to the verify contract,
and for the same reason as 'ape story verify --file': its caller relies on
the non-zero exit to stop before writing anything. Downstream skills
reference shard files by exact slug, so a deduplicated 'foo-2.md' sibling
would silently break them; the source document has to be fixed instead.

Exit codes:
  0  no duplicate slugs
  1  duplicates found (each pair is printed with its line numbers)

This command carries no --cwd. It resolves nothing from the project's
config — every input is an explicit path argument — so there is no
project for --cwd to select. Relative paths resolve against the
working directory, as they would for any other tool.

Examples:

```
  ape doc verify development/planning/prd.md --level 2
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--level` | int | `2` | Heading level to check |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape doctor

Probe the local environment for prerequisites

```
ape doctor [flags]
```

Probe the local environment for prerequisites and report a per-check
verdict.

Doctor runs a fixed set of checks against the host (claude / git /
node / npx binaries, Playwright host compatibility, ~/.claude
writability) and the project at --cwd (framework metadata, installed
skills + pipelines, and the always-on operating-rules fragment +
CLAUDE.md managed block). Project-scoped checks degrade to INFO when run
outside a project root; the operating-rules checks only hard-fail when a
framework install that manages them has lost the fragment, import, or
apex-orchestrator skill.

Seven checks report on PROJECT DATA rather than on the host: whether the
config resolves at all (nothing else can see the project's artifacts
without it), registry drift, story frontmatter, tracker divergence,
whether the tracker's lock sidecar is gitignored, team-memory size, and
any pending project-data migration. All seven degrade to INFO outside a
project root.

memory.size is one of only two Required checks in that group, deliberately:
a non-required FAIL is downgraded to WARN, so nothing else could surface a
team-memory.md that has passed the Read cap and become unreadable by its
own writer.

Two checks report on the step-completion gates rather than on
prerequisites, because both protect against a failure that is otherwise
silent:

  hooks.contract_drift          whether Claude Code still sends the hook
                                fields the gates read. If it stops, the
                                gates stop firing without erroring.
  framework.terminal_contracts  whether _apex/terminal-contracts.csv is
                                installed, and which skills it enrols. No
                                table means no run is checked for a
                                terminal contract.

Both SKIP or report INFO when there is nothing to judge — absence of
evidence is not coverage.

--only runs just the named checks, so one gate can be scripted on its own:

  ape doctor --only hooks.contract_drift --strict --cwd <project>

That is what "make check-hooks" runs. Note the --cwd: hook drift is
observed from the runlogs ape itself wrote (<project>/_output/tasks), so it
can only be judged against a project ape has actually run pipelines in, not
against the ape repo. An unknown name in --only is an error rather than a
silent no-op — a typo that ran zero checks would exit 0 and read as a pass.

Exit codes:
  0  every required check passed (warnings allowed unless --strict)
  1  at least one required check failed, any warning under --strict, or
     --only named a check that does not exist

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root to probe (default: current working directory) |
| `--only` | string | `—` | Run ONLY these checks (comma-separated). Errors on an unknown name; applied before --skip |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--skip` | string | `—` | Comma-separated list of check names to skip (e.g. node.binary,npx.binary) |
| `--strict` | bool | `false` | Treat WARN-level findings as failures (exit 1) |

## ape event

Publish a session progress event over NATS

```
ape event <event> [--payload <json>|@file|-] [flags]
```

Publish a caller-named progress event for the current Claude session on
ape.evt.<user>.<project>.session.<session-id>.<event>.

The <event> token is caller-chosen (validated [a-z0-9-]+). --payload is
arbitrary JSON, given inline, as @file, or "-" for stdin; it rides the
versioned envelope under "payload" alongside the decoded user identity and
the resolved session id.

The session is resolved as: --session-id → --transcript → APE_SESSION_ID →
the newest transcript for the current project.

Exit codes: 0 published · 1 publish failed (connected) · 2 usage error,
no NATS configured, or session unresolvable.

Examples:

```
  ape event status --payload '{"phase":"implement","pct":60}'
  ape event build-green
  echo '{"pr":42}' | ape event pr-opened --payload -
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root for session auto-resolution (default: current working dir). |
| `--events-subject-prefix` | string | `ape.evt` | Subject root for the published event. |
| `--nats-creds` | string | `—` | NATS .creds file; its decoded user identity is baked into every subject (env APE_NATS_CREDS). |
| `--nats-url` | string | `—` | NATS server URL (env APE_NATS_URL). Required — no URL is a usage error (exit 2). |
| `--output-format` | string | `human` | Output format: human\|json (result object on stdout, diagnostics on stderr). |
| `--payload` | string | `—` | Event payload as JSON: inline, @file, or "-" for stdin. |
| `--quiet` | bool | `false` | Suppress the human-mode confirmation line. |
| `--session-id` | string | `—` | Claude session id to report for (default: auto-resolve the current project's newest). |
| `--transcript` | string | `—` | Explicit transcript file; the session id is parsed from its name. |

## ape feature

Inspect and maintain the feature registry

```
ape feature
```

Aliases: `features`

Subcommands:

- `list` — List features from the registry index
- `sync` — Reconcile the features index against records on disk
- `update` — Apply field deltas to existing features index entries
- `verify` — Verify the features registry against its index

## ape feature list

List features from the registry index

```
ape feature list [flags]
```

Examples:

```
  ape feature list --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape feature sync

Reconcile the features index against records on disk

```
ape feature sync [flags]
```

Reconcile index.yaml against the records on disk: records with no entry
are added, entries whose id no record claims are removed, and an entry
whose file: no longer resolves is repointed at the record claiming its id.

This is the repair for the findings `verify` reports, and nothing more —
it copies what a record's own frontmatter states and invents no titles,
statuses or any other field. A renamed record keeps its authored entry
rather than being dropped and re-added.

--check makes it a dry run: the same diff, nothing written. generated_at
moves only when something else did.

Examples:

```
  ape feature sync --check
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--check` | bool | `false` | Report the diff without writing |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape feature update

Apply field deltas to existing features index entries

```
ape feature update [flags]
```

Apply per-entry field deltas to index.yaml and refresh generated_at.

--updates takes {"<id>": {"<field>": "<value>"}} as a file path or '-' for
stdin. Only entries ALREADY listed may be updated: an unknown id is an
error raised before anything is written, because creating an index entry
is the job of the skill that authors the document it points at.

The rendered index is round-trip parsed before it replaces the file, and
the write is atomic — a crash mid-write cannot truncate an index. Key
order and comments survive, which a PyYAML round trip does not manage.

Exit codes:
  0  applied
  1  unknown id, unreadable updates, or an unwritable index (nothing written)

Examples:

```
  echo '{"FEAT-1-1":{"status":"superseded"}}' | ape feature update --updates -
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--generated-at` | string | `—` | Timestamp to write as generated_at (default: resolved config timestamp) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--updates` | string | `-` | JSON file with per-entry field deltas, or '-' for stdin |

## ape feature verify

Verify the features registry against its index

```
ape feature verify [flags]
```

Exactly four checks, and no others:

  1. registry.orphan_record / registry.phantom_entry
     set equality between the record directory and index.yaml, both
     directions
  2. registry.file_unresolved
     every index file: resolves, relative to the index's own directory
  3. registry.duplicate_id
     duplicate ids, in the index and on disk
  4. registry.record_unparseable
     the record parses as frontmatter at all

No schema validation, no field drift, no tag comparison, no updated_at
comparison — those are judgment, and a verifier that wanders into them
stops being trustworthy.

An index.yaml that is absent while records exist is reported once, as
registry.index_missing: the degenerate case of check 1, not a fifth check.
One finding beats one orphan per record, which would bury the only fact
that matters.

Findings travel in the payload. Exit is 0 even with findings unless
--strict is passed, so this is safe to call from anywhere.

Examples:

```
  ape feature verify --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--strict` | bool | `false` | Exit 1 when there are findings (default: report and exit 0) |

## ape framework

Install and inspect APEX framework assets in a project

```
ape framework
```

Manage the apex_process_framework assets installed at the project root.

  ape framework setup      One-time install: skills + pipelines + bootstrap
                           _apex/config.yaml. Refuses if already installed
                           (pass --force to re-bootstrap).
  ape framework update     Refresh skills + pipelines against the framework
                           repo's current HEAD. Refuses if not yet set up
                           (run setup first).
  ape framework status     Inspect the installed framework version, with
                           optional drift report against the framework repo.

The framework repo path is resolved from --repo or $APEX_FRAMEWORK_REPO.
The project root is resolved from --cwd or the current working directory.

Subcommands:

- `setup` — Initial install of framework skills + pipelines into the project
- `status` — Inspect the installed framework version + drift report
- `update` — Refresh framework skills and pipelines, and run pending project-data migrations

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root directory (default: current working dir) |
| `--repo` | string | `—` | Path to a checked-out apex_process_framework repo (default: $APEX_FRAMEWORK_REPO) |

## ape framework setup

Initial install of framework skills + pipelines into the project

```
ape framework setup [flags]
```

Initial install of framework-managed assets into <project>:

  - .claude/skills/apex-*  copied from <repo>/.claude/skills
  - _apex/pipelines/*.yaml copied from <repo>/_apex/pipelines
  - _apex/config.yaml      seeded (interactive prompt by default;
                           supply --project-name and --extensions to
                           skip the TUI; --no-bootstrap to skip seeding
                           entirely)
  - _apex/framework.yaml   metadata recording what was installed.

Refuses to run when:
  - _apex/framework.yaml already exists (pass --force to re-bootstrap;
    this resets project_name and extensions)
  - the framework repo is dirty, on a non-main branch, or its
    .claude/skills/apex-* subtree has uncommitted changes (pass
    --force to bypass)

Headless contexts: when stdout is not a TTY (or --output-format is not
human) and the project lacks _apex/config.yaml, you must supply
--project-name and --extensions, OR pass --no-bootstrap. Otherwise
'setup' refuses to seed silently.

For subsequent refreshes against a framework version bump, use
'ape framework update'.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--extensions` | string | `—` | Bootstrap value for extensions, comma-separated (e.g. ext-adrs,ext-features). Empty string = none. |
| `--force` | bool | `false` | Bypass safety checks (already installed, dirty framework, non-main branch, modified project skills) |
| `--no-bootstrap` | bool | `false` | Skip _apex/config.yaml seeding entirely |
| `--no-fetch` | bool | `false` | Skip 'git fetch && merge --ff-only' on the framework repo before reading its state |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--project-name` | string | `—` | Bootstrap value for project_name (skips the TUI prompt) |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root directory (default: current working dir) |
| `--repo` | string | `—` | Path to a checked-out apex_process_framework repo (default: $APEX_FRAMEWORK_REPO) |

## ape framework status

Inspect the installed framework version + drift report

```
ape framework status [flags]
```

Read <project>/_apex/framework.yaml and report what was installed.

When --repo or $APEX_FRAMEWORK_REPO is set, also reads the framework
repo's current HEAD (with a best-effort 'git fetch' unless --no-fetch
is passed) and emits drift fields comparing the installed git_hash /
version_tag against current.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--no-fetch` | bool | `false` | Skip the best-effort 'git fetch' against the framework repo |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root directory (default: current working dir) |
| `--repo` | string | `—` | Path to a checked-out apex_process_framework repo (default: $APEX_FRAMEWORK_REPO) |

## ape framework update

Refresh framework skills and pipelines, and run pending project-data migrations

```
ape framework update [flags]
```

Refresh framework-managed assets in <project>:

  - .claude/skills/apex-*  re-copied from <repo>/.claude/skills
  - _apex/pipelines/*.yaml re-copied from <repo>/_apex/pipelines
  - _apex/framework.yaml   metadata refreshed (preserves project_name +
                           extensions recorded by 'ape framework setup')

Then any pending PROJECT-DATA migration (PLAN-25 D10). Migrations run here
rather than as a separate command a skill has to police, so no skill ever
meets an un-migrated project and no skill needs a migration failure path.
This is the right transaction boundary: explicitly invoked, at the moment
framework expectations change, outside the build loop.

THIS COMMAND COMMITS NOTHING — not the install, not the migration, not the
repair. It never has, and that property is worth more than the
convenience: the whole result sits in the working tree for one 'git diff',
and you group it into however many commits you want. The run prints the
paths and the 'git add' line.

Does NOT touch _apex/config.yaml — that's the one-time bootstrap from
'ape framework setup'. To re-bootstrap, pass --force to 'setup'.

  --dry-run     show the framework drift AND the pending migrations,
                writing nothing
  --no-migrate  install framework files only; migrations stay pending, and
                'ape doctor' reports them so the state is visible
  --repair      also run the judgment phase over free-form deferred
                records. OFF by default: it spawns a paid opus session, and
                a file-copying verb should not start doing that silently.

Refuses to run when:
  - _apex/framework.yaml is absent (run 'ape framework setup' first)
  - the framework repo is dirty, on a non-main branch, or its
    .claude/skills/apex-* subtree has uncommitted changes (pass
    --force to bypass)

A migration is skipped (never forced) when ITS OWN paths have uncommitted
changes. The gate is path-scoped rather than whole-tree: those paths are
disjoint from what the install writes, so the two are order-independent,
and unrelated work-in-progress elsewhere does not block anything.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--dry-run` | bool | `false` | Show the framework diff and pending migrations, writing nothing |
| `--force` | bool | `false` | Bypass safety checks (dirty framework, non-main branch, modified project skills) |
| `--no-fetch` | bool | `false` | Skip 'git fetch && merge --ff-only' on the framework repo before reading its state |
| `--no-migrate` | bool | `false` | Install framework files only; leave migrations pending |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--repair` | bool | `false` | Also run the opus judgment phase over free-form deferred records (spends money) |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root directory (default: current working dir) |
| `--repo` | string | `—` | Path to a checked-out apex_process_framework repo (default: $APEX_FRAMEWORK_REPO) |

## ape log

Publish a structured log record over NATS

```
ape log <level> <message> [flags]
```

Publish one structured log record for the current Claude session on
ape.log.<user>.<project>.<session-id>.<level>.

<level> is one of debug|info|warn|error. Extra structured context is passed
as repeated --field key=value pairs. Centralized-logging consumers subscribe
ape.log.> (or per-user/project subtrees — the subject is the routing key).

Exit codes: 0 published · 1 publish failed (connected) · 2 usage error,
no NATS configured, or session unresolvable.

Examples:

```
  ape log info "migration step 3 complete"
  ape log warn "retrying" --field attempt=2 --field endpoint=api
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root for session auto-resolution (default: current working dir). |
| `--field` | stringArray | `[]` | Structured field as key=value (repeatable). |
| `--nats-creds` | string | `—` | NATS .creds file; its decoded user identity is baked into every subject (env APE_NATS_CREDS). |
| `--nats-url` | string | `—` | NATS server URL (env APE_NATS_URL). Required — no URL is a usage error (exit 2). |
| `--output-format` | string | `human` | Output format: human\|json (result object on stdout, diagnostics on stderr). |
| `--quiet` | bool | `false` | Suppress the human-mode confirmation line. |
| `--session-id` | string | `—` | Claude session id to report for (default: auto-resolve the current project's newest). |
| `--transcript` | string | `—` | Explicit transcript file; the session id is parsed from its name. |

## ape memory

Read the team-memory file without loading it whole

```
ape memory
```

team-memory.md outgrew whole-file reading: at 431,950 bytes on the
reference project a Read fails outright ("exceeds maximum allowed size
(256KB)") — including for the retrospective that is instructed to re-read
it before editing it.

  index  what is in there: ordinal, section, date, size, title
  show   the verbatim body of named entries
  check  size against two budgets, from a stat alone

Subcommands:

- `check` — Report team-memory size against the soft budget and hard ceiling
- `index` — List every team-memory entry
- `show` — Print the verbatim body of the named entries

## ape memory check

Report team-memory size against the soft budget and hard ceiling

```
ape memory check [flags]
```

Classify team-memory.md against two budgets, from an os.Stat alone —
the file is never read, which is what makes this cheap enough to run on
every retrospective.

  state: absent      no team-memory.md yet (a fresh project, not a problem)
  state: ok          under the soft budget
  state: over-soft   compaction is due; schedule it at the next epic close
  state: over-hard   approaching Claude Code's 256 KiB Read cap — the file
                     is about to become unreadable by its own writer

EXIT 0 BY DEFAULT, whatever the state. The verdict is the 'state' field,
not the exit code, and that is deliberate: the framework's prose
convention is "on non-zero exit: HALT", so a failing exit here would abort
the retrospective at exactly the moment compaction is most needed — the
gate would break the ceremony it exists to trigger.

--fail-at opts into a non-zero exit for CI, which wants one:
  never  (default) always exit 0
  soft   exit 1 at over-soft or worse
  hard   exit 1 at over-hard

'ape doctor' maps the state to WARN/FAIL itself, so a hard-ceiling breach
is still visible and non-ignorable without this flag.

The token count in the output is bytes/4, an estimate, and nothing gates
on it.

Examples:

```
  ape memory check
  ape memory check --fail-at hard
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--fail-at` | string | `never` | Exit 1 at this state or worse: never\|soft\|hard |
| `--hard` | int64 | `0` | Hard ceiling in bytes (default 204800) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--soft` | int64 | `0` | Soft budget in bytes (default 40960) |

## ape memory index

List every team-memory entry

```
ape memory index [flags]
```

One line per '### ' entry: ordinal, section, date, byte size, title.

Structurally lossless, and carrying no filter, ranking or predicate —
deliberately. Most call sites sit inside '## On Activation', which runs
BEFORE the story is identified, so no predicate keyed on "this story's
domain" could work there. Selection has to be possible from ordinal,
section, date, size and title alone.

A '### ' line inside a fenced code block is content, not an entry. That
matters more than it sounds: counting one would shift every ordinal after
it, and 'show <n>' would then hand back the wrong entry with nothing to
signal the mistake.

Examples:

```
  ape memory index --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape memory show

Print the verbatim body of the named entries

```
ape memory show <n>[,<n>,...] [flags]
```

Print entries by ordinal, in the order asked for, byte-for-byte as they
appear in the file.

Exit codes:
  0  printed
  2  an ordinal the file does not have

Examples:

```
  ape memory show 3,7,12
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |

## ape metrics

Scan and publish this session's usage metrics over NATS

```
ape metrics [flags]
```

Scan the resolved Claude session set (main + sub-agents) and publish a
usage snapshot on ape.metrics.<user>.<project>.<session-id>.

The payload carries per-model token counts (with the ephemeral 5m/1h cache
split), turn count, first/last turn timestamps, and the Claude Code version
— everything needed to reprice against Claude Code API rates at any later
moment (per_model tokens × the price table = cost_usd).

--run-id <id> instead publishes a completed run's manifest totals (a reader
over the run's manifest.yaml), with run_id populated. Republishing is
idempotent; consumers key on (session_id, ts).

Exit codes: 0 published · 1 publish failed (connected) · 2 usage error,
no NATS configured, or the session/run was unresolvable.

Examples:

```
  ape metrics
  ape metrics --output-format json
  ape metrics --run-id 20260709-abc123
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root for session auto-resolution (default: current working dir). |
| `--nats-creds` | string | `—` | NATS .creds file; its decoded user identity is baked into every subject (env APE_NATS_CREDS). |
| `--nats-url` | string | `—` | NATS server URL (env APE_NATS_URL). Required — no URL is a usage error (exit 2). |
| `--output-format` | string | `human` | Output format: human\|json (result object on stdout, diagnostics on stderr). |
| `--quiet` | bool | `false` | Suppress the human-mode confirmation line. |
| `--run-id` | string | `—` | Publish a completed run's manifest totals instead of a live session scan. |
| `--session-id` | string | `—` | Claude session id to report for (default: auto-resolve the current project's newest). |
| `--transcript` | string | `—` | Explicit transcript file; the session id is parsed from its name. |

## ape pattern

Manage governance patterns

```
ape pattern
```

Aliases: `patterns`

Subcommands:

- `list` — List all governance patterns
- `sync` — Reconcile the patterns index against records on disk
- `update` — Apply field deltas to existing patterns index entries
- `verify` — Verify the patterns registry against its index

## ape pattern list

List all governance patterns

```
ape pattern list [flags]
```

Examples:

```
  ape pattern list --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape pattern sync

Reconcile the patterns index against records on disk

```
ape pattern sync [flags]
```

Reconcile index.yaml against the records on disk: records with no entry
are added, entries whose id no record claims are removed, and an entry
whose file: no longer resolves is repointed at the record claiming its id.

This is the repair for the findings `verify` reports, and nothing more —
it copies what a record's own frontmatter states and invents no titles,
statuses or any other field. A renamed record keeps its authored entry
rather than being dropped and re-added.

--check makes it a dry run: the same diff, nothing written. generated_at
moves only when something else did.

Examples:

```
  ape pattern sync --check
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--check` | bool | `false` | Report the diff without writing |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape pattern update

Apply field deltas to existing patterns index entries

```
ape pattern update [flags]
```

Apply per-entry field deltas to index.yaml and refresh generated_at.

--updates takes {"<id>": {"<field>": "<value>"}} as a file path or '-' for
stdin. Only entries ALREADY listed may be updated: an unknown id is an
error raised before anything is written, because creating an index entry
is the job of the skill that authors the document it points at.

The rendered index is round-trip parsed before it replaces the file, and
the write is atomic — a crash mid-write cannot truncate an index. Key
order and comments survive, which a PyYAML round trip does not manage.

Exit codes:
  0  applied
  1  unknown id, unreadable updates, or an unwritable index (nothing written)

Examples:

```
  echo '{"PAT-0001":{"status":"superseded"}}' | ape pattern update --updates -
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--generated-at` | string | `—` | Timestamp to write as generated_at (default: resolved config timestamp) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--updates` | string | `-` | JSON file with per-entry field deltas, or '-' for stdin |

## ape pattern verify

Verify the patterns registry against its index

```
ape pattern verify [flags]
```

Exactly four checks, and no others:

  1. registry.orphan_record / registry.phantom_entry
     set equality between the record directory and index.yaml, both
     directions
  2. registry.file_unresolved
     every index file: resolves, relative to the index's own directory
  3. registry.duplicate_id
     duplicate ids, in the index and on disk
  4. registry.record_unparseable
     the record parses as frontmatter at all

No schema validation, no field drift, no tag comparison, no updated_at
comparison — those are judgment, and a verifier that wanders into them
stops being trustworthy.

An index.yaml that is absent while records exist is reported once, as
registry.index_missing: the degenerate case of check 1, not a fifth check.
One finding beats one orphan per record, which would bury the only fact
that matters.

Findings travel in the payload. Exit is 0 even with findings unless
--strict is passed, so this is safe to call from anywhere.

Examples:

```
  ape pattern verify --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--strict` | bool | `false` | Exit 1 when there are findings (default: report and exit 0) |

## ape pipeline

List or run an APEX pipeline

```
ape pipeline [name] [flags]
```

List or run a named APEX pipeline against the project in the current
working directory.

  ape pipeline                 List installed pipelines (also accepts
                               --output-format human|json|yaml).
  ape pipeline <name>          Run the named pipeline.

Available pipelines are read from <project>/_apex/pipelines/. To
install the canonical set (design, governance, epics) from the
framework repo, run "ape framework update".

Each pipeline is a sequence of stages; each stage is a chain of skill
invocations. ape runs one interactive "claude" REPL per stage inside an
in-process PTY (never "claude -p"): steps are typed as real REPL
keystrokes following PAT-25 passthrough conventions —

    /<agent> --autonomous -- <skill> --autonomous <args>

Skills without an agent skip the passthrough hop:

    /<skill> --autonomous --no-commit <args>

Rendering surface: --tui (default) shows the Bubble Tea panels, --web
serves the bridged web UI, --no-tui prints plain stdout progress lines.

The --prompt flag is forwarded only to skills whose pipeline definition
declares prompt_flag (currently apex-create-epics-and-stories in the
"epics" pipeline). The prompt value passes through as REPL keystrokes
directly, so embedded quotes/specials survive without shell quoting.

Examples:

```
  ape pipeline                       # list installed pipelines
  ape pipeline design                # run the design pipeline (TUI)
  ape pipeline governance --no-tui   # plain stdout progress
  ape pipeline epics --web --open    # bridged web UI, open the browser
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--commit-allow-dirty` | bool | `false` | Bypass the dirty-tree pre-run gate. The first committing step's diff will include any pre-existing uncommitted changes. |
| `--cwd` | string | `—` | Project root directory (default: current working dir) |
| `--effort` | string | `—` | Reasoning effort (low\|medium\|high\|xhigh\|max) applied when a step/stage/pipeline doesn't set an effort field in the YAML. Propagates to sub-agents. Default xhigh when unset everywhere. |
| `--events-subject-prefix` | string | `ape.evt` | Subject root for progress events. |
| `--from` | string | `—` | Skip stages before the named one and start execution there |
| `--idle-timeout` | duration | `0s` | Per-step idle backstop: cancel a step only after this long with no progress across hooks, transcript growth, or PTY output (e.g. 90m). Default 60m. |
| `--ignore-project-settings` | bool | `false` | Tell the spawned claude to skip project + local .claude/settings*.json. Honoured in --web mode. |
| `--manifest-dir` | string | `—` | Override the directory for run manifest artifacts (default: <project>/_output/pipelines) |
| `--max-duration` | duration | `3h0m0s` | Hard wall-clock ceiling per step regardless of progress (e.g. 3h); the clock resets on each sub-agent boundary, so a sequential batch step is bounded per item, not per batch. 0 disables the cap. |
| `--nats-creds` | string | `—` | NATS .creds file; its user identity is baked into every subject (env APE_NATS_CREDS). |
| `--nats-url` | string | `—` | NATS server URL for progress events + transcript upload (env APE_NATS_URL). Empty disables both. |
| `--no-commit` | bool | `false` | Do not commit anything during the run; leave the working tree dirty. Overrides any `commit:` field in the pipeline YAML. |
| `--no-tui` | bool | `false` | No UI surface: plain stdout progress lines. Exec is still the interactive per-stage claude REPL in an in-process PTY. |
| `--open` | bool | `false` | With --web (or default): xdg-open the broker URL on start. |
| `--output-format` | string | `human` | Output format for list mode (no positional arg): human\|json\|yaml |
| `--prompt` | string | `—` | Optional prompt forwarded to skills that accept it (currently: epics) |
| `--quiet` | bool | `false` | With --no-tui: suppress per-event stream; print only stage/step start/end markers |
| `--transcript-store` | string | `nats-object` | Transcript blob backend: nats-object\|uri-offload (env APE_TRANSCRIPT_STORE). |
| `--tui` | bool | `false` | Bubble Tea TUI (the default; explicit form for scripts). |
| `--upload-transcripts` | bool | `false` | At run end, upload the transcript set as content-addressed blobs (env APE_UPLOAD_TRANSCRIPTS=1). |
| `--web` | bool | `false` | Bridged web UI. Explicit form for scripts. |

## ape planning

Show the planning pipeline diagram

```
ape planning
```

Print an ASCII swimlanes view of the greenfield planning pipeline.
Lanes are agent personas; rows are topological depth; `←` lists each
skill's upstream dependencies. Source of truth for edges: the
apex_process_docs planning-pipeline explanation.

## ape prompt

Drive an unattended Claude session from a prompt or a handoff file

```
ape prompt [text] [flags]
```

Run one unattended Claude Code session end-to-end: spawn claude in
an in-process PTY, deliver a prompt (or seed the session from a handoff
document), let it work under the ape bridge's hook supervision, detect
completion via the Stop hook, capture the transcript + per-model
telemetry, and exit with a meaningful status.

Exactly one of the positional <text> or --handoff <file> must be given.

  ape prompt "add a CHANGELOG entry for the latest release"
  ape prompt --handoff development/handoffs/2026-07-13-resume.md
  ape prompt "refactor the parser" --agent apex-agent-dev --workflow
  ape prompt "big refactor" --ultracode --model "opus[1m]"

Prompt assembly:
  --agent A        the delivered line is "/A --autonomous -- <prompt>"
                   (no agent: the prompt is sent as a plain message).
  --handoff F      the prompt becomes "Read the handoff document at
                   <abs F> and continue the work it describes."
  --ultracode      prepends the "ultracode" keyword to the prompt
                   (session runs workflows by default).
  --workflow       appends an explicit "run this via a workflow"
                   directive. Independent of --ultracode; both compose.

Records land under <project>/_output/ape/prompts/<prompt-id>/ (runlog
streams + copied transcript + prompt.yaml session record) and fold into
the project cost rollup's Prompts bucket.

ape prompt must run from a project root (a directory with
_apex/config.yaml). It makes no commits of its own.

Exit codes: 0 session completed (Stop hook) · 1 idle-timeout or session
failed · 2 usage or preflight error (no _apex/config.yaml, unresolved
--agent, missing --handoff file) · 3 the claude REPL never became ready
· 4 claude exited before the Stop hook.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--agent` | string | `—` | Framework agent fronting the session: /<agent> --autonomous -- <prompt> |
| `--cwd` | string | `—` | Project root directory (default: current working dir) |
| `--effort` | string | `—` | Reasoning effort for the session and its sub-agents (low\|medium\|high\|xhigh\|max). Default xhigh when unset. |
| `--handoff` | string | `—` | Handoff document to seed the session with (mutually exclusive with the positional prompt) |
| `--idle-timeout` | duration | `0s` | Idle backstop: end the session only after this long with no progress across hooks, transcript growth, or PTY output (e.g. 15m); default matches the pipeline (60m) |
| `--ignore-project-settings` | bool | `false` | Tell the spawned claude to skip project + local .claude/settings*.json |
| `--max-duration` | duration | `3h0m0s` | Hard wall-clock ceiling regardless of progress (e.g. 3h); the clock resets on each sub-agent boundary, so a batch of sub-agents is bounded per item, not overall. 0 disables the cap. |
| `--model` | string | `—` | Claude model. A bare family (sonnet, opus, haiku) resolves to its current generation; sonnet-5 / claude-sonnet-5 / opus[1m] pin explicitly |
| `--output-format` | string | `human` | Output format: human\|json\|yaml (json/yaml = result envelope on stdout, progress on stderr) |
| `--quiet` | bool | `false` | Suppress the progress stream on stderr |
| `--ultracode` | bool | `false` | Prepend the ultracode keyword (session runs workflows by default) |
| `--workflow` | bool | `false` | Append a directive to run the task through a Claude Code workflow |

## ape registry

Verify or reconcile every record registry at once

```
ape registry
```

Cross-family fan-out over the four record registries — ADRs, patterns,
features and capabilities. Each family also carries these verbs on its own
noun (`ape adr verify`); this is the whole-project view.

Subcommands:

- `sync` — Reconcile every record index against records on disk
- `verify` — Verify every record registry (or a named subset)

## ape registry sync

Reconcile every record index against records on disk

```
ape registry sync [flags]
```

Reconcile index.yaml against the records on disk: records with no entry
are added, entries whose id no record claims are removed, and an entry
whose file: no longer resolves is repointed at the record claiming its id.

This is the repair for the findings `verify` reports, and nothing more —
it copies what a record's own frontmatter states and invents no titles,
statuses or any other field. A renamed record keeps its authored entry
rather than being dropped and re-added.

--check makes it a dry run: the same diff, nothing written. generated_at
moves only when something else did.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--all` | bool | `false` | Reconcile every family (the default when --family is not given) |
| `--check` | bool | `false` | Report the diff without writing |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--family` | stringSlice | `[]` | Families to reconcile: adrs,patterns,features,capabilities |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape registry verify

Verify every record registry (or a named subset)

```
ape registry verify [flags]
```

Exactly four checks, and no others:

  1. registry.orphan_record / registry.phantom_entry
     set equality between the record directory and index.yaml, both
     directions
  2. registry.file_unresolved
     every index file: resolves, relative to the index's own directory
  3. registry.duplicate_id
     duplicate ids, in the index and on disk
  4. registry.record_unparseable
     the record parses as frontmatter at all

No schema validation, no field drift, no tag comparison, no updated_at
comparison — those are judgment, and a verifier that wanders into them
stops being trustworthy.

An index.yaml that is absent while records exist is reported once, as
registry.index_missing: the degenerate case of check 1, not a fifth check.
One finding beats one orphan per record, which would bury the only fact
that matters.

Findings travel in the payload. Exit is 0 even with findings unless
--strict is passed, so this is safe to call from anywhere.

Examples:

```
  ape registry verify --all --output-format json
  ape registry verify --family adrs,patterns
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--all` | bool | `false` | Verify every family (the default when --family is not given) |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--family` | stringSlice | `[]` | Families to verify: adrs,patterns,features,capabilities |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--strict` | bool | `false` | Exit 1 when there are findings (default: report and exit 0) |

## ape rollback

Rollback ape to the previous version

```
ape rollback
```

Restore the backup binary created during the last update.

## ape sandbox

Provision and operate hardware-isolated Kata VM workspaces (via aped)

```
ape sandbox
```

Provision and operate long-lived, hardware-isolated Kata microVM
workspaces (own guest kernel, KVM) through a rootful aped daemon.

ape drives aped over embedded NATS using the ape.vmm.<node>.> contract; aped
provisions the microVM, composes the workspace home, mints a per-VM telemetry
credential, and owns the workspace registry. ape never runs as root.

  ape sandbox up <name>      Provision a workspace
  ape sandbox ls             List provisioned workspaces
  ape sandbox inspect <name> Show a workspace's live state
  ape sandbox capacity       Show the node's headroom (does another one fit?)
  ape sandbox costs [name]   What Claude sessions inside workspaces cost
  ape sandbox exec <name> -- <cmd>...   Run a command inside a workspace
  ape sandbox forward <name> <port>     Reach a port inside a workspace
  ape sandbox setup <name>     Materialize the project's declared toolchain
  ape sandbox stop <name>      Stop a workspace (free RAM, keep rootfs + state)
  ape sandbox start <name>     Start a stopped workspace
  ape sandbox freeze <name>    Freeze a workspace (cgroup-freeze; RAM resident)
  ape sandbox unfreeze <name>  Unfreeze a frozen workspace
  ape sandbox suspend <name>   Suspend a workspace microVM — not yet on Kata
  ape sandbox down <name>      Tear a workspace down
  ape sandbox framework …      Materialize the framework refs a node can mount
  ape sandbox credentials …    Publish your Claude credentials for workspaces
  ape sandbox egress set …     Change a running workspace's egress allowlist

Point ape at your aped node with APE_NATS_URL + APE_NATS_CREDS (the operator
credential aped mints at startup) and --node. Requires a running aped on a
Linux host with KVM + containerd + Kata.

Subcommands:

- `attach` — Open an interactive shell inside a workspace
- `capacity` — Show the node's workspace headroom (cores, memory, how many more fit)
- `costs` — Show what Claude sessions inside workspaces cost
- `credentials` — Publish your Claude credentials for workspaces to use
- `down` — Tear a workspace down
- `egress` — Inspect and change a workspace's egress allowlist
- `exec` — Run a command inside a workspace
- `forward` — Forward a local port to a port inside a workspace
- `framework` — Manage the APEX framework refs a sandbox node can mount
- `freeze` — Freeze a workspace (cgroup-freeze; guest RAM stays resident)
- `inspect` — Show a workspace's live state
- `ls` — List provisioned workspaces
- `setup` — Materialize the project's declared toolchain inside a workspace
- `ssh` — How to ssh into a workspace (use 'ape sandbox forward --ssh')
- `start` — Start a stopped workspace
- `stop` — Stop a workspace (free its RAM, keep its rootfs + state)
- `suspend` — Suspend a workspace microVM (save guest RAM to disk) — not yet supported on Kata
- `unfreeze` — Unfreeze a frozen workspace
- `up` — Provision a Kata workspace

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox attach

Open an interactive shell inside a workspace

```
ape sandbox attach <name>
```

Open an interactive shell inside a workspace, wiring your terminal's
stdin/stdout/stderr to the guest over the aped exec session subjects (PLAN-18 D2,
credit-based flow control; the terminal goes raw and resizes forward on SIGWINCH).

Requires an aped node running the containerd driver (aped run --driver
containerd); a shell-driver node reports the session UNSUPPORTED.

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox capacity

Show the node's workspace headroom (cores, memory, how many more fit)

```
ape sandbox capacity [flags]
```

Report what the target aped node can still take: its cores and memory, how
many workspaces it already carries, how many of those are RUNNING (only those
hold RAM — a stopped workspace keeps its state and frees its memory), and how
many more the free memory holds.

FITS is an estimate, and the number it divides by is printed next to it: guest
memory is set by the node's Kata configuration, which aped does not own, so the
per-workspace size is an assumption rather than a measurement. Disagree with it
by redoing the division on the memory numbers above it.

This is a report, not placement. Choosing which node a workspace lands on is a
fleet concern and is not what this answers.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox costs

Show what Claude sessions inside workspaces cost

```
ape sandbox costs [name] [flags]
```

Report the Claude usage accumulated inside the node's workspaces, per
workspace, all-time. With a name, report only that workspace.

The numbers come from the session transcripts each workspace's composed home
already holds on the node — there is no agent, no telemetry wire, and nothing
to enable. The node does the scan because it owns those homes: they are private
to the daemon, and an operator's ape cannot read them.

A model with no rate in the price table contributes $0 and is called out, so a
total that is a LOWER BOUND is never mistaken for an exact one. The table doing
the pricing is the NODE's, embedded in its aped at build time — so a gap is
fixed by upgrading aped there, not by 'ape costs update' here.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox credentials

Publish your Claude credentials for workspaces to use

```
ape sandbox credentials
```

Publish the host's Claude OAuth credential where the aped node can read it, so
workspaces get your Anthropic session instead of asking you to log in again.

aped-front runs as its own service user with ProtectHome=yes and cannot read
/home/<you>/.claude. Rather than widening your home, this command — which runs as
YOU — publishes the credential into a directory the daemon may read
(/srv/ape-credentials/<user>), and the node is pointed at it with
'aped front --host-home'.

  ape sandbox credentials publish     # hard link: one live credential, shared
  ape sandbox credentials publish --copy   # independent copy: isolated, diverges
  ape sandbox credentials status
  ape sandbox credentials watch      # re-publish automatically after a /login
  ape sandbox credentials revoke

Both modes give a workspace your Anthropic identity — that is what "use my
credentials" means. Use 'revoke' to take it back.

Subcommands:

- `publish` — Publish the host credential to the node's credential root
- `revoke` — Remove the published credential so workspaces stop getting it
- `status` — Show whether a published credential is present and still live
- `watch` — Re-publish automatically whenever your credential is replaced

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox credentials publish

Publish the host credential to the node's credential root

```
ape sandbox credentials publish [flags]
```

Publish ~/.claude/.credentials.json where aped can read it.

Default is a HARD LINK: the workspace and the host share one inode, so a token
refresh on either side is immediately valid on the other, and your file's
permissions are unchanged (the daemon only stat()s it; Kata's virtiofsd does the
I/O as root). --copy makes an independent copy instead, which isolates the
workspace but DIVERGES the first time either side refreshes, because OAuth refresh
tokens rotate.

Re-running is safe and is how you repair a link that decoupled — which happens if
the host rewrites the credential by replacing the file rather than editing it.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--copy` | bool | `false` | Publish an independent copy instead of a hard link (isolated, but diverges on token refresh) |
| `--root` | string | `—` | Credential root the node reads (default: $APE_CREDENTIAL_ROOT or /srv/ape-credentials) |
| `--source` | string | `—` | Credential file to publish (default: ~/.claude/.credentials.json) |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox credentials revoke

Remove the published credential so workspaces stop getting it

```
ape sandbox credentials revoke [flags]
```

Remove the published credential. With a hard link this only drops the extra
name — your ~/.claude/.credentials.json is untouched, since a file survives until
its last link is gone. With a copy it deletes the copy.

Workspaces already running keep the credential that was composed into them until
they are torn down.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--root` | string | `—` | Credential root the node reads |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox credentials status

Show whether a published credential is present and still live

```
ape sandbox credentials status [flags]
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--root` | string | `—` | Credential root the node reads |
| `--source` | string | `—` | Credential file to compare against (default: ~/.claude/.credentials.json) |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox credentials watch

Re-publish automatically whenever your credential is replaced

```
ape sandbox credentials watch [flags]
```

Watch your credential and re-publish it the moment it is replaced.

This closes the one gap in credential sharing that nothing on the node can close. A
`claude /login` REPLACES the credential file rather than editing it, so the published
hard link is left pointing at the old one — and aped cannot notice, because it runs as
another user with ProtectHome=yes and can never read your home. Until something
re-publishes, every workspace keeps using the pre-login token.

Any 'ape sandbox' command re-publishes as a side effect, so in normal use this is already
handled. Run this watcher when you want it handled with no command at all — as a user
service:

  install -D -m0644 deploy/user/ape-credentials-watch.service \
    ~/.config/systemd/user/ape-credentials-watch.service
  systemctl --user enable --now ape-credentials-watch
  sudo loginctl enable-linger $USER   # start at BOOT, not just at first login

It runs as YOU — that is the point: only your own session can read your home. It needs no
aped node, no running workspace, and no publication (with nothing published it idles), so
it is safe to enable before ever publishing.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--install-unit` | bool | `false` | Write that unit to ~/.config/systemd/user/ and exit (safer than redirecting --print-unit) |
| `--interval` | duration | `2s` | How often to check for a replacement |
| `--print-unit` | bool | `false` | Print a systemd --user unit for this watcher (with THIS binary's path) and exit |
| `--root` | string | `—` | Credential root the node reads |
| `--source` | string | `—` | Credential file to watch (default: ~/.claude/.credentials.json) |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox down

Tear a workspace down

```
ape sandbox down <name> [flags]
```

Destroy the workspace microVM and drop its aped registry entry. A
persistent volume (mount: volume) is retained unless --remove-volume is set.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--force` | bool | `false` | Force teardown |
| `--remove-volume` | bool | `false` | Also remove the persistent volume (mount: volume) |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox egress

Inspect and change a workspace's egress allowlist

```
ape sandbox egress
```

Change which domains a RUNNING workspace may reach, without recreating it.

The allowlist is enforced by a host-side CONNECT proxy on a fixed port, so re-pointing
it is a proxy restart on that same port — the workspace keeps running and its
HTTPS_PROXY stays valid. The request is intersected with the node's egress policy
exactly as at create time, so this can narrow or re-shape a grant but never exceed
what the node permits.

  ape sandbox egress set dev --domain github.com --domain proxy.golang.org

Mounts cannot be changed this way (they are fixed in the container's OCI spec at
creation) — use 'down' then 'up', which is cheap because repos, caches and the
framework all live in durable host mounts.

Subcommands:

- `set` — Replace a running workspace's egress allowlist

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox egress set

Replace a running workspace's egress allowlist

```
ape sandbox egress set <name> [flags]
```

Replace the domains a running workspace may reach. The list is REPLACED, not
added to, so it is also how you revoke access: 'set <name>' with no --domain leaves the
workspace with no egress at all.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--domain` | stringArray | `[]` | Domain to allow (repeatable; replaces the current list) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox exec

Run a command inside a workspace

```
ape sandbox exec <name> -- <cmd>...
```

Run a command inside a workspace, streaming its stdout/stderr back over the
aped exec session subjects and returning its exit code.

On an aped node without an interactive backend (the nerdctl shell driver) it
falls back to a request/reply exec that reports only the exit status (output goes
to the node's logs).

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox forward

Forward a local port to a port inside a workspace

```
ape sandbox forward <name> <local>[:<guest>] [flags]
```

Forward a port on THIS machine to a TCP port inside a workspace, so you can
open a browser (or point a client) at something running in there.

  ape sandbox forward dev 8080          # localhost:8080 → workspace :8080
  ape sandbox forward dev 3000:8080     # localhost:3000 → workspace :8080
  ape sandbox forward dev --ssh         # localhost:2222 → workspace :22

Nothing is exposed. The workspace gets no listener and neither firewall changes:
the guest end dials its OWN loopback and the bytes ride the same authenticated
session transport as exec and attach, which aped audits. The forward is private
to you, it is not a public URL, and it does not connect workspaces to each other.

Forwards live with this command, not with the workspace: Ctrl-C ends them and
the workspace is untouched. Run several at once in separate terminals, or the
same command twice for two ports.

--ssh gives you an ssh (and therefore VS Code Remote) target. The image ships
sshd and aped composes ~/.ssh, but nothing starts sshd for you, so start it once
per workspace first:

  ape sandbox exec dev -- sh -c 'mkdir -p /run/sshd && /usr/sbin/sshd'
  ape sandbox forward dev --ssh &
  ssh -p 2222 root@127.0.0.1

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--bind` | string | `127.0.0.1` | Local address to listen on (0.0.0.0 exposes the forward to your network) |
| `--ssh` | bool | `false` | Forward the workspace's sshd (guest :22, local :2222 unless a local port is given) |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox framework

Manage the APEX framework refs a sandbox node can mount

```
ape sandbox framework
```

Manage the materialized APEX framework refs on this host.

A sandbox workspace gets the framework as a READ-ONLY mount at /opt/apex-framework
rather than a baked image layer, so the public ape-sandbox image stays
framework-free and credential-free. This command is the host-side, credentialed
half: it copies one pinned ref out of your local framework checkout into the
node's framework root.

  ape sandbox framework materialize v0.3.1
  ape sandbox framework ls
  ape sandbox up dev --framework-ref v0.3.1

aped never fetches the framework itself: if a requested ref is not materialized,
'ape sandbox up' fails with the command to run. Inside the workspace, consume it
with 'ape framework setup --no-fetch --repo /opt/apex-framework'.

Subcommands:

- `ls` — List the framework refs materialized on this host
- `materialize` — Materialize a framework ref into the node's framework root

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox framework ls

List the framework refs materialized on this host

```
ape sandbox framework ls [flags]
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--root` | string | `—` | Framework root to list (default: $APE_FRAMEWORK_ROOT or /srv/apex-framework) |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox framework materialize

Materialize a framework ref into the node's framework root

```
ape sandbox framework materialize <ref> [flags]
```

Materialize one framework ref (tag, branch, or commit) as a self-contained,
mountable checkout under the framework root.

The ref must ALREADY be present in the local framework repo — this command does
not fetch, so a stale checkout fails loudly instead of silently materializing an
older commit. Fetch first with your own credentials:
  git -C <repo> fetch --tags

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--force` | bool | `false` | Replace an already-materialized ref |
| `--repo` | string | `—` | Local apex_process_framework checkout (default: $APEX_FRAMEWORK_REPO) |
| `--root` | string | `—` | Framework root the node mounts from (default: $APE_FRAMEWORK_ROOT or /srv/apex-framework) |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox freeze

Freeze a workspace (cgroup-freeze; guest RAM stays resident)

```
ape sandbox freeze <name>
```

Freeze cgroup-freezes the workspace's guest processes: the guest stops
consuming CPU but its RAM stays fully resident, so unfreeze resumes instantly.
This is a freeze, not a VM suspend (see 'ape sandbox suspend').

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox inspect

Show a workspace's live state

```
ape sandbox inspect <name> [flags]
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox ls

List provisioned workspaces

```
ape sandbox ls [flags]
```

List provisioned workspaces with their age and last use.

LAST-USED is the last exec, attach or start — a USE signal, not proof of idleness: a
workspace running a long job without anyone reaching in looks untouched. Use it to
decide what to stop (frees RAM, keeps state) or tear down, and read --idle in the
same spirit.

IDLE-STOP is what the node's automatic reaper will do to each workspace: a
duration it asked for, "off" if it is exempt, or "node" for the node's own
setting. The reaper reads the in-guest agent's heartbeat, not LAST-USED, and it
never stops a workspace it has no heartbeat for.

  ape sandbox ls --idle 24h    # only workspaces nobody has touched in 24h

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--idle` | duration | `0s` | Only workspaces not used for at least this long (e.g. 24h) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox setup

Materialize the project's declared toolchain inside a workspace

```
ape sandbox setup <name> [flags]
```

Run the project's toolchain install step inside a running workspace: 'asdf
install' for the runtime versions the project declares, then 'bingo get' for its
pinned Go tools.

The toolchain comes from the .apesandbox.yaml toolchain: section, which should
reference the native files (.tool-versions, .bingo/) rather than duplicate the
versions. The step is idempotent and becomes a no-op — fully offline — once the
durable tool caches are warm; the FIRST run needs the workspace to have egress
(the registries it downloads from must be in its allowlist).

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root holding the descriptor (default: current working directory) |
| `--dry-run` | bool | `false` | Print the setup script instead of running it in the workspace |
| `--sandbox-config` | string | `—` | Path to a non-default .apesandbox.yaml |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox ssh

How to ssh into a workspace (use 'ape sandbox forward --ssh')

```
ape sandbox ssh <name>
```

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox start

Start a stopped workspace

```
ape sandbox start <name>
```

Start a stopped workspace from its retained container + snapshot. A workspace
that is already running is left alone.

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox stop

Stop a workspace (free its RAM, keep its rootfs + state)

```
ape sandbox stop <name>
```

Stop a workspace: kill the guest task while keeping the container and its
snapshot, so 'ape sandbox start' revives it with its filesystem intact.

Choosing between the three:
  freeze  cgroup-freeze — RAM stays RESIDENT, instant unfreeze, lost on reboot
  stop    task killed  — RAM FREED, rootfs + state kept, survives a reboot
  down    destroyed    — rootfs deleted (a 'volume' mount survives unless
                        --remove-volume)

Toolchain and dependency state lives in durable cache mounts, so a stopped —
or even a destroyed — workspace loses nothing that a warm cache can restore.

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox suspend

Suspend a workspace microVM (save guest RAM to disk) — not yet supported on Kata

```
ape sandbox suspend <name>
```

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox unfreeze

Unfreeze a frozen workspace

```
ape sandbox unfreeze <name>
```

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape sandbox up

Provision a Kata workspace

```
ape sandbox up <name> [flags]
```

Provision a long-lived Kata workspace named <name> on the target aped node.

aped resolves the profile, composes a per-workspace ~/.claude, mints a per-VM
telemetry credential, and starts the detached microVM. For a host-fs mount the
project at --cwd is sent as the mount source; aped canonicalizes it and
re-checks it against its policy mount-root allow-list before binding it.

A committed .apesandbox.yaml at the project root describes the rest of the
workspace — the repos to mount (each at /workspace/<name>, one flagged main),
extra mounts, and the egress domains to request. --mount flags merge on top of it
(CLI wins by destination). Everything there is a REQUEST: aped re-checks every
source against its mount roots, refuses reserved destinations, and intersects the
egress domains with its own policy, so a project can narrow what a node permits
but never widen it.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cache` | stringSlice | `[]` | Durable tool caches to mount: asdf\|cargo\|go\|npm\|pub (adds to the descriptor's toolchain.caches) |
| `--cwd` | string | `—` | Project root to mount for host-fs (default: current working directory) |
| `--egress-domain` | stringArray | `[]` | Request an egress domain (repeatable; still gated by the node's policy) |
| `--framework-ref` | string | `—` | APEX framework ref to mount read-only (must be materialized on the node) |
| `--idle-stop` | string | `—` | Stop this workspace after it has been idle this long (e.g. 4h), or "off" to exempt it (default: the node's; overrides .apesandbox.yaml lifecycle.idle_stop) |
| `--image` | string | `—` | Image ref override (default: aped's pinned image) |
| `--mount` | string | `—` | Mount mode: host-fs \| volume \| ephemeral (default: host-fs) |
| `--mount-path` | stringArray | `[]` | Extra mount <source>[:<dest>][:ro\|:rw] (repeatable; ro by default; merges with .apesandbox.yaml) |
| `--no-sandbox-config` | bool | `false` | Ignore any .apesandbox.yaml in the project |
| `--profile` | string | `—` | Profile name aped resolves (default: derived from the request) |
| `--runtime` | string | `—` | Runtime handler: kata-qemu \| kata-clh |
| `--sandbox-config` | string | `—` | Path to a non-default .apesandbox.yaml |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--nats-creds` | string | `—` | operator .creds for aped (env APE_NATS_CREDS) |
| `--nats-url` | string | `—` | aped management NATS URL (env APE_NATS_URL) |
| `--node` | string | `—` | aped node targeted by ape.vmm.<node>.> (env APE_APED_NODE; default: hostname) |

## ape script

Run a Go orchestration script through the yaegi interpreter

```
ape script <file.go> [flags] [-- script-args...]
```

Run a plain Go file inside ape's process under the yaegi interpreter,
with the apescript library injected so the script can drive ape's
primitives — run a pipeline, task, or prompt (all PTY-backed, the same
runners the CLI uses), read manifests, scan transcripts, log, publish
events, and upload blobs — as one deterministic, version-controlled Go
file instead of a shell wrapper around the CLI.

The file must define:

    func Main(ctx context.Context) error

ape evaluates the file, then calls Main. A non-nil error (or a panic,
which is recovered and reported with the yaegi stack) exits 1; SIGINT
cancels the context so the in-flight run tears down cleanly.

Use "-" as the file to read the script from stdin. Everything after a
"--" separator is exposed to the script as apescript.Args().

  ape script ops/nightly.go -- --target ./component-a
  cat ops/nightly.go | ape script -

By default the interpreter is unrestricted (full stdlib — arbitrary
trusted code, same trust level as your shell). --sandbox switches to
yaegi's restricted symbol set, which blocks os/exec, os.Exit, syscall,
and unsafe while keeping the apescript orchestration surface fully
available. See docs/reference/apescript.md for the per-group rules.

Exit codes: 0 success · 1 the script returned an error, panicked, or a
launched run failed · 2 usage or read error (no file, bad flags).

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root directory (default: current working dir) |
| `--events-subject-prefix` | string | `ape.evt` | Subject root for progress events. |
| `--nats-creds` | string | `—` | NATS .creds file; its user identity is baked into every subject (env APE_NATS_CREDS). |
| `--nats-url` | string | `—` | NATS server URL for progress events + transcript upload (env APE_NATS_URL). Empty disables both. |
| `--output-format` | string | `human` | Output format: human\|json\|yaml (json/yaml wrap the run in {result, duration, cost_usd}) |
| `--quiet` | bool | `false` | Suppress apescript.Log output |
| `--sandbox` | bool | `false` | Run the script in the restricted interpreter (blocks os/exec, os.Exit, syscall, unsafe) |
| `--transcript-store` | string | `nats-object` | Transcript blob backend: nats-object\|uri-offload (env APE_TRANSCRIPT_STORE). |
| `--upload-transcripts` | bool | `false` | At run end, upload the transcript set as content-addressed blobs (env APE_UPLOAD_TRANSCRIPTS=1). |

## ape service

Run a NATS-micro job daemon that accepts pipeline/task jobs over request/reply

```
ape service [flags]
```

Turn this machine into a remotely drivable ape worker (PLAN-14). The
daemon registers a NATS micro service on

  ape.svc.<name>.<project-slug>.<endpoint>

and accepts JSON request/reply jobs: pipeline.run and task.run dispatch an
ape child process (headless, PTY-only); job.status / job.list / job.stop
manage them; status / health report the daemon. NATS-micro $SRV.PING /
$SRV.INFO / $SRV.STATS discovery is available for free. prompt.run and
script.run are registered but rejected (VALIDATION) until their runners
ship.

Admission is keyed exclusivity, exclusive by default: a job holds its
exclusivity_key (default "") exclusively unless nonexclusive:true. Conflicts
are rejected immediately (BUSY_EXCLUSIVE / BUSY_KEY) — never queued. Requests
naming a project_root outside the allowlist are rejected (PROJECT_NOT_ALLOWED).

The daemon serves the project plus its declared component repositories, read
from _apex/service.yaml (or ~/.ape/service.yaml, or --config):

  project_root: /abs/path/main-project
  allow:
    - /abs/path/main-project
    - /abs/path/component-repo

SECURITY: anyone who can publish on the service subjects can run pipelines on
this machine. Scope the NATS credential's publish/subscribe permissions to
ape.svc.<name>.<project-slug>.> on the server — that is the real trust
boundary (see docs/how-to/run-ape-as-a-service.md).

Shutdown is graceful: SIGINT/SIGTERM stops accepting new jobs and waits for
in-flight children (indefinitely by default; bound it with --drain-timeout).
A second signal terminates them immediately.

Exit codes: 0 clean shutdown · 1 connect/registration failure · 2 usage or
config error (bad --name, missing/invalid service.yaml, no NATS URL).

Examples:

```
  ape service --nats-url nats://127.0.0.1:4222 --nats-creds ./ape.creds
  ape service --name ci --drain-timeout 5m
  # discovery + a task submission from another host:
  nats req '$SRV.PING.ape' ''
  nats req ape.svc.ape.myproject.task.run '{"project_root":"/abs/path/myproject","skill":"apex-shard-doc"}'
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--config` | string | `—` | Path to service.yaml (default: <cwd>/_apex/service.yaml, then ~/.ape/service.yaml). |
| `--cwd` | string | `—` | Project root for config resolution (default: current working dir). |
| `--drain-timeout` | duration | `0s` | On shutdown, wait this long for in-flight jobs before terminating them (0 = wait indefinitely; a second signal forces). |
| `--events-subject-prefix` | string | `ape.evt` | Subject root for daemon job lifecycle events. |
| `--name` | string | `ape` | Service name — the <name> subject segment and $SRV discovery name (run several daemons on one cluster with distinct names). |
| `--nats-creds` | string | `—` | NATS .creds file; its user identity is the <user> token on job lifecycle events (env APE_NATS_CREDS). |
| `--nats-url` | string | `—` | NATS server URL (env APE_NATS_URL). Required. |

## ape sessions

List, prune, or open the URL of live ape sessions

```
ape sessions [flags]
```

Live ape chat / ape pipeline (web mode) invocations are tracked in
~/.ape/registry.json. This subcommand inspects that registry.

  ape sessions               Show one row per live session.
  ape sessions prune         Drop rows whose PID is no longer running.
  ape sessions open [<pfx>]  xdg-open the URL of the live session whose
                             cwd starts with <pfx>. Errors if zero or
                             multiple sessions match.

Subcommands:

- `open` — xdg-open the URL of the live session whose cwd matches <project-prefix>
- `prune` — Drop registry rows whose PID is no longer running

Examples:

```
  ape sessions
  ape sessions --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape sessions open

xdg-open the URL of the live session whose cwd matches <project-prefix>

```
ape sessions open [<project-prefix>]
```

Examples:

```
  ape sessions open ~/projects/foo
```

## ape sessions prune

Drop registry rows whose PID is no longer running

```
ape sessions prune [flags]
```

Examples:

```
  ape sessions prune
  ape sessions prune --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape sprint

Inspect and maintain sprint-status.yaml

```
ape sprint
```

Three operations on the tracker, with deliberately different contracts:

  check      compare tracker rows against story files; report divergence,
             never pick a winner, always exit 0
  verify     assert one row landed as written (a gate, exit 0/2/3/4/5)
  reconcile  project an epic's row from its story rows (a mutation)

Subcommands:

- `check` — Report divergence between the tracker and story files
- `reconcile` — Project epic rows from their story rows
- `verify` — Verify one tracker row landed as written

## ape sprint check

Report divergence between the tracker and story files

```
ape sprint check [flags]
```

Set-compare sprint-status.yaml's story rows against story files on disk,
and compare each row's status against that story's own frontmatter.

epic-* and *-retrospective rows are classified out: they have no story
file to diverge from. The tracker's 'drafted' is normalised to a story
file's 'ready-for-dev' for comparison only — neither file is touched — or
every drafted story would report a false divergence.

ALWAYS EXITS 0, even with findings, and there is no --strict. Which side of
a divergence is right is judgment, so this reports both values and picks
neither; wiring it into a build loop would stop runs over something no tool
can resolve. It belongs in 'ape doctor' and nowhere else.

Examples:

```
  ape sprint check --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape sprint reconcile

Project epic rows from their story rows

```
ape sprint reconcile [flags]
```

An epic's status is not a fact any skill asserts — it is a projection of
the story rows beneath it. This is the single implementation of that
projection, tracker-only, with no story-file reads:

  rows   = development_status keys matching ^{N}-\d+[-_]
  active = rows whose status is not 'cancelled'

  no rows              -> leave unchanged (never close an epic with none)
  no active rows       -> leave unchanged (all-cancelled is a scope call)
  all active 'done'    -> done
  all active 'backlog' -> backlog
  otherwise            -> in-progress

'blocked' lands in the final clause, so a blocked story holds its epic
open. An unrecognised status can only ever hold an epic open, never close
it, and is named in the output rather than swallowed.

The write is TARGETED: only the matched epic-N line and the body
updated_at change. Comments, key order, story rows and the sync-generated
header are untouched. updated_at moves only on mutation and never
backwards — it is clamped, reported, and never fatal.

The read-modify-write takes an exclusive lock on a sidecar file, because
concurrent per-epic sub-agents reconcile the same tracker and the last
writer would otherwise silently drop a sibling's update.

Exit 0 for every content outcome, including an unrecognised status.
Non-zero only for a genuine I/O failure.

Examples:

```
  ape sprint reconcile --epic 12
  ape sprint reconcile --all --check
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--all` | bool | `false` | Reconcile every epic with rows |
| `--check` | bool | `false` | Report the projection without writing |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--epic` | int | `0` | Reconcile one epic by number |
| `--file` | string | `—` | Tracker path (default: resolved from config) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape sprint verify

Verify one tracker row landed as written

```
ape sprint verify [flags]
```

Re-read the tracker from disk and confirm the write landed: the row
equals --expected, updated_at does not precede created_at, and updated_at
is not earlier than the value in the last committed revision.

That last comparison is the one that matters. A row write can silently
fail to land, and comparing updated_at against created_at alone passes
trivially because both are written in the same operation.

Exit codes, preserved exactly from verify-sprint-status-row.py because two
review skills branch on them:
  0  the row matches and the timestamps are ordered
  2  file unreadable or YAML malformed
  3  key missing from development_status, or its value differs
  4  updated_at precedes created_at (a corrupt write)
  5  updated_at is earlier than the last committed value (a backwards
     write). Deterministically repairable: re-write the field as the
     reported clamp value or later, re-run, and report the clamp. NEVER a
     reason to stop the run.

Exit 1 is deliberately unreachable. In the Python it meant "PyYAML is not
installed" — an environment failure that forced apex-review-story and
apex-code-review to carry an eye-check fallback. A static binary cannot
produce it, which is what lets those fallback branches be deleted.

This command carries no --cwd. --file is required and names the tracker
outright, so nothing is resolved from the project's config and there is no
project for --cwd to select.

Examples:

```
  ape sprint verify --file development/implementation/sprint-status.yaml --key 1-1 --expected done
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--expected` | string | `—` | Status the row must equal (required) |
| `--file` | string | `—` | Path to sprint-status.yaml (required) |
| `--key` | string | `—` | development_status row key (required) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape story

Project and verify story frontmatter

```
ape story
```

Aliases: `stories`

Subcommands:

- `fields` — Project named frontmatter keys across every story
- `verify` — Verify story frontmatter (corpus report, or a single-file gate)

## ape story fields

Project named frontmatter keys across every story

```
ape story fields [flags]
```

Walk the implementation folder, read at most 8 KiB per .md file, stop at
the closing '---', and emit only the named top-level keys plus their
nested blocks. A body is never opened.

A file counts as a story only if it has a story_id — which is what keeps
retrospectives, epic briefs and the deferred-work stub out of the result
without teaching this command about each of them.

The trailer carries files_scanned, stories_matched, per-field presence
counts and bytes_read. Those numbers are the point: "this field is absent
everywhere" and "this field was never looked for" are different answers,
and only the trailer distinguishes them. A field present in zero stories
reports 0 and exits 0.

One unreadable file loses that file and nothing else — it is named in
warnings and the rest are still returned.

Examples:

```
  ape story fields --select story_id,epic,status,features --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--select` | string | `—` | Comma-separated frontmatter keys to emit (required) |

## ape story verify

Verify story frontmatter (corpus report, or a single-file gate)

```
ape story verify [flags]
```

Three check classes over story frontmatter, and no others:

  1. presence  the four required keys, plus the extension-gated ones
               (governance.adrs, governance.patterns, features,
               capabilities) for whichever ext_* flags are set
  2. type      features items must be objects, not bare strings;
               depends_on items must be strings, not YAML floats
  3. refs      every cited ADR / pattern / feature / capability id
               resolves to a record on disk

No enum checks, no contribution vocabulary, no JSON Schema engine. The
contribution field has no normative source, so coercing it would fabricate
a lifecycle edge — a test asserts this command reports nothing for any
value of it.

TWO MODES, with deliberately different contracts:

  corpus (default)  a REPORT. Exit 0 even with findings; --strict makes it
                    1. --strict must never be set from inside
                    apex-review-story, apex-code-review or
                    apex-epic-batch-review: a non-zero exit on those paths
                    converts a defer into a patch and demotes the story.

  --file <path>     a GATE, replacing verify-story-frontmatter.py with its
                    exit codes intact:
                      0  valid
                      2  parse failure (bad YAML, or no --- delimiters)
                      3  a required or extension-conditional key is absent,
                         or an optional key is present but malformed
                    Referential integrity is not asserted here — a single
                    file cannot see the corpus, exactly as the Python
                    could not.

Examples:

```
  ape story verify --output-format json
  ape story verify --file development/implementation/1-1_thing.md --active-extensions ext-adrs
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--active-extensions` | string | `—` | Comma-separated active extensions for --file mode (e.g. ext-adrs,ext-features) |
| `--cwd` | string | `—` | Project root (default: current working dir) |
| `--file` | string | `—` | Verify one story file as a gate (exit 0/2/3) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--strict` | bool | `false` | Exit 1 when there are findings (default: report and exit 0) — NEVER set this from apex-review-story, apex-code-review or apex-epic-batch-review |

## ape task

Run a single framework skill through the interactive PTY runner

```
ape task <skill> [flags]
```

Run one framework skill as a single-step interactive run — everything a
pipeline step gets (agent prefix, preflight, bridge hooks, manifest,
telemetry) with all parameters passed as flags instead of a pipeline
YAML file. Execution is PTY-interactive only: claude runs as a REPL,
the prompt is typed as keystrokes, and completion is detected via the
bridge Stop hook.

Commit control is two-layered:
  --no-commit     skill layer — tells the skill/framework not to commit
                  (the no-agent invocation shape already carries it).
  --task-commit   task layer — opt-in git commit of the complete task at
                  the end of the run. Off by default. A bare flag derives
                  the message "ape:task/<skill>".

Run artifacts land under <project>/_output/tasks/<skill>/<run-id>/
(manifest.yaml, per-step ndjson, runlog streams).

--handoff <file> is a shorthand for --prompt: it checks the file
exists and derives the prompt "Read <abs-path> and follow the Resume
Protocol inside it." (the same continuation prompt the /handoff skill
suggests). It still requires --prompt-flag to actually reach the
skill, and is mutually exclusive with --prompt.

Exit codes: 0 success · 1 run failed or idle timeout · 2 usage or
preflight error · 3 REPL never became ready (last pane on stderr).

Examples:

```
  ape task apex-shard-doc --args "--doc prd"
  ape task apex-create-prd --agent apex-agent-pm --model "opus[1m]" --prompt "a greeter CLI" --prompt-flag --prompt
  ape task apex-create-prd --agent apex-agent-pm --handoff _output/handoffs/2026-07-05-x.md --prompt-flag --prompt
  ape task apex-shard-doc --task-commit "chore: shard prd"
  ape task apex-create-prd --agent apex-agent-pm --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--agent` | string | `—` | Framework agent (slash-command) fronting the skill: /<agent> --autonomous -- <skill> ... |
| `--args` | string | `—` | Verbatim skill args appended to the invocation (whitespace-separated) |
| `--commit-allow-dirty` | bool | `false` | Bypass the dirty-tree gate (relevant only with --task-commit) |
| `--cwd` | string | `—` | Project root directory (default: current working dir) |
| `--effort` | string | `—` | Reasoning effort for the session and its sub-agents (low\|medium\|high\|xhigh\|max). Default xhigh when unset. |
| `--events-subject-prefix` | string | `ape.evt` | Subject root for progress events. |
| `--handoff` | string | `—` | Path to a handoff/context file; derives a "Read <path> and follow the Resume Protocol" --prompt value (mutually exclusive with --prompt) |
| `--idle-timeout` | duration | `0s` | Idle backstop: cancel only after this long with no progress across hooks, transcript growth, or PTY output (e.g. 15m); default matches pipeline (60m) |
| `--ignore-project-settings` | bool | `false` | Tell the spawned claude to skip project + local .claude/settings*.json |
| `--manifest-dir` | string | `—` | Override the run-artifact base dir (default: <project>/_output/tasks) |
| `--max-duration` | duration | `3h0m0s` | Hard wall-clock ceiling regardless of progress (e.g. 3h); the clock resets on each sub-agent boundary, so a sequential batch skill is bounded per item, not per batch. 0 disables the cap. |
| `--model` | string | `—` | Claude model. A bare family (sonnet, opus, haiku) resolves to its current generation; sonnet-5 / claude-sonnet-5 / opus[1m] pin explicitly |
| `--nats-creds` | string | `—` | NATS .creds file; its user identity is baked into every subject (env APE_NATS_CREDS). |
| `--nats-url` | string | `—` | NATS server URL for progress events + transcript upload (env APE_NATS_URL). Empty disables both. |
| `--no-commit` | bool | `false` | Skill layer: tell the skill/framework not to commit (adds skill-level --no-commit on the agent path) |
| `--output-format` | string | `human` | Output format: human\|json (json = result envelope on stdout, progress on stderr) |
| `--prompt` | string | `—` | Run prompt forwarded via --prompt-flag (same semantics as pipeline --prompt) |
| `--prompt-flag` | string | `—` | Skill flag name the --prompt value is forwarded through (spec prompt_flag equivalent) |
| `--quiet` | bool | `false` | Suppress the per-event progress stream |
| `--task-commit` | string | `—` | Task layer: commit the complete task at the end; bare flag derives "ape:task/<skill>" |
| `--transcript-store` | string | `nats-object` | Transcript blob backend: nats-object\|uri-offload (env APE_TRANSCRIPT_STORE). |
| `--upload-transcripts` | bool | `false` | At run end, upload the transcript set as content-addressed blobs (env APE_UPLOAD_TRANSCRIPTS=1). |

## ape trait

Manage and inspect traits

```
ape trait
```

Subcommands:

- `conflicts` — Check for conflicts between traits
- `list` — List all available traits
- `show` — Show details of a trait
- `validate` — Validate a trait YAML file

## ape trait conflicts

Check for conflicts between traits

```
ape trait conflicts <trait1> <trait2> [...] [flags]
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape trait list

List all available traits

```
ape trait list [flags]
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape trait show

Show details of a trait

```
ape trait show <name> [flags]
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape trait validate

Validate a trait YAML file

```
ape trait validate <file> [flags]
```

Examples:

```
  ape trait validate ./mytrait.yaml --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape transcript

Work with Claude session transcripts

```
ape transcript
```

Transcript utilities. The upload subcommand blob-uploads a session's transcript set over NATS.

Subcommands:

- `upload` — Upload this session's transcript set as content-addressed blobs

## ape transcript upload

Upload this session's transcript set as content-addressed blobs

```
ape transcript upload [flags]
```

Upload the resolved Claude session set (main + sub-agents) as
deduplicated, content-addressed, zstd-compressed blobs, then publish a
companion ape.evt.<user>.<project>.session.<session-id>.transcript-uploaded
event carrying the digest map.

Uploading is idempotent: a blob already present is a cheap no-op (its result
entry is marked existed=true with the same digest), so re-running is safe.

--store selects the backend: nats-object (a NATS JetStream Object Store,
default) or uri-offload (a NATS request returns a signed upload URI; ape
does the HTTPS PUT).

Exit codes: 0 uploaded · 1 upload/publish failed (connected) · 2 usage
error, no NATS configured, or the session was unresolvable.

Examples:

```
  ape transcript upload
  ape transcript upload --store uri-offload --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root for session auto-resolution (default: current working dir). |
| `--events-subject-prefix` | string | `ape.evt` | Subject root for the published event. |
| `--nats-creds` | string | `—` | NATS .creds file; its decoded user identity is baked into every subject (env APE_NATS_CREDS). |
| `--nats-url` | string | `—` | NATS server URL (env APE_NATS_URL). Required — no URL is a usage error (exit 2). |
| `--output-format` | string | `human` | Output format: human\|json (result object on stdout, diagnostics on stderr). |
| `--quiet` | bool | `false` | Suppress the human-mode confirmation line. |
| `--session-id` | string | `—` | Claude session id to report for (default: auto-resolve the current project's newest). |
| `--store` | string | `nats-object` | Blob backend: nats-object\|uri-offload (env APE_TRANSCRIPT_STORE). |
| `--transcript` | string | `—` | Explicit transcript file; the session id is parsed from its name. |

## ape update

Update ape to the latest version

```
ape update [flags]
```

Download and install the latest ape release from GitHub.

Downloads are verified before they are applied: the release's signed
SHA256 manifest is checked against its keyless-cosign Sigstore bundle
(pinning this repository's release workflow identity and the Fulcio
issuer), then the downloaded archive is checked against that trusted
manifest. Verification is fully offline against an embedded Sigstore
trusted root — no cosign binary is required.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

## ape version

Print version information

```
ape version [flags]
```

Print the version, build date, and git commit of the ape binary.

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

