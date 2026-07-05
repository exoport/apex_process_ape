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
- `chat` — Bridged claude REPL with hooks captured to a runlog
- `costs` — Show this project's Claude cost rollup
- `doctor` — Probe the local environment for prerequisites
- `framework` — Install and inspect APEX framework assets in a project
- `pattern` — Manage governance patterns
- `pipeline` — List or run an APEX pipeline
- `planning` — Show the planning pipeline diagram
- `rollback` — Rollback ape to the previous version
- `sessions` — List, prune, or open the URL of live ape sessions
- `sync` — Sync governance artifacts
- `task` — Run a single framework skill through the interactive PTY runner
- `trait` — Manage and inspect traits
- `update` — Update ape to the latest version
- `version` — Print version information

## ape adr

Manage Architecture Decision Records

```
ape adr
```

Subcommands:

- `list` — List all ADRs
- `new` — Scaffold a new ADR file
- `validate` — Validate ADR files

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

## ape adr validate

Validate ADR files

```
ape adr validate [flags]
```

Examples:

```
  ape adr validate --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

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
| `--ignore-project-settings` | bool | `false` | Tell claude to skip project + local .claude/settings*.json. |
| `--model` | string | `—` | Initial claude model (e.g. "opus[1m]"); falls back to claude's default when empty. |

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
  ape costs update --from <file>     Refresh the price table from a YAML file.
  ape costs roll                     Force a project rollup rebuild from all
                                     run / chat directories.

Subcommands:

- `chat` — Show cost for a single chat session (reads its session.yaml)
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
skills + pipelines). Project-scoped checks degrade to INFO when run
outside a project root.

Exit codes:
  0  every required check passed (warnings allowed unless --strict)
  1  at least one required check failed (or any warning under --strict)

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root to probe (default: current working directory) |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |
| `--skip` | string | `—` | Comma-separated list of check names to skip (e.g. node.binary,npx.binary) |
| `--strict` | bool | `false` | Treat WARN-level findings as failures (exit 1) |

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
- `update` — Refresh framework skills and pipelines against the framework repo

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

Refresh framework skills and pipelines against the framework repo

```
ape framework update [flags]
```

Refresh framework-managed assets in <project>:

  - .claude/skills/apex-*  re-copied from <repo>/.claude/skills
  - _apex/pipelines/*.yaml re-copied from <repo>/_apex/pipelines
  - _apex/framework.yaml   metadata refreshed (preserves project_name +
                           extensions recorded by 'ape framework setup')

Does NOT touch _apex/config.yaml — that's the one-time bootstrap from
'ape framework setup'. To re-bootstrap, pass --force to 'setup'.

Refuses to run when:
  - _apex/framework.yaml is absent (run 'ape framework setup' first)
  - the framework repo is dirty, on a non-main branch, or its
    .claude/skills/apex-* subtree has uncommitted changes (pass
    --force to bypass)

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--force` | bool | `false` | Bypass safety checks (dirty framework, non-main branch, modified project skills) |
| `--no-fetch` | bool | `false` | Skip 'git fetch && merge --ff-only' on the framework repo before reading its state |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

Global flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--cwd` | string | `—` | Project root directory (default: current working dir) |
| `--repo` | string | `—` | Path to a checked-out apex_process_framework repo (default: $APEX_FRAMEWORK_REPO) |

## ape pattern

Manage governance patterns

```
ape pattern
```

Subcommands:

- `list` — List all governance patterns
- `validate` — Validate governance patterns

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

## ape pattern validate

Validate governance patterns

```
ape pattern validate [flags]
```

Examples:

```
  ape pattern validate --output-format json
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--output-format` | string | `human` | Output format: human\|json\|yaml |

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
| `--from` | string | `—` | Skip stages before the named one and start execution there |
| `--ignore-project-settings` | bool | `false` | Tell the spawned claude to skip project + local .claude/settings*.json. Honoured in --web mode. |
| `--manifest-dir` | string | `—` | Override the directory for run manifest artifacts (default: <project>/_output/pipelines) |
| `--no-commit` | bool | `false` | Do not commit anything during the run; leave the working tree dirty. Overrides any `commit:` field in the pipeline YAML. |
| `--no-tui` | bool | `false` | No UI surface: plain stdout progress lines. Exec is still the interactive per-stage claude REPL in an in-process PTY. |
| `--open` | bool | `false` | With --web (or default): xdg-open the broker URL on start. |
| `--output-format` | string | `human` | Output format for list mode (no positional arg): human\|json\|yaml |
| `--prompt` | string | `—` | Optional prompt forwarded to skills that accept it (currently: epics) |
| `--quiet` | bool | `false` | With --no-tui: suppress per-event stream; print only stage/step start/end markers |
| `--tui` | bool | `false` | Bubble Tea TUI (the default; explicit form for scripts). |
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

## ape rollback

Rollback ape to the previous version

```
ape rollback
```

Restore the backup binary created during the last update.

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

## ape sync

Sync governance artifacts

```
ape sync
```

Flags:

| Flag | Type | Default | Description |
| ---- | ---- | ------- | ----------- |
| `--check` | bool | `false` | Check sync status without applying changes |

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
| `--handoff` | string | `—` | Path to a handoff/context file; derives a "Read <path> and follow the Resume Protocol" --prompt value (mutually exclusive with --prompt) |
| `--idle-timeout` | duration | `0s` | Idle-without-Stop backstop (e.g. 15m); default matches pipeline (60m) |
| `--ignore-project-settings` | bool | `false` | Tell the spawned claude to skip project + local .claude/settings*.json |
| `--manifest-dir` | string | `—` | Override the run-artifact base dir (default: <project>/_output/tasks) |
| `--model` | string | `—` | Claude model for the session (e.g. "opus[1m]") |
| `--no-commit` | bool | `false` | Skill layer: tell the skill/framework not to commit (adds skill-level --no-commit on the agent path) |
| `--output-format` | string | `human` | Output format: human\|json (json = result envelope on stdout, progress on stderr) |
| `--prompt` | string | `—` | Run prompt forwarded via --prompt-flag (same semantics as pipeline --prompt) |
| `--prompt-flag` | string | `—` | Skill flag name the --prompt value is forwarded through (spec prompt_flag equivalent) |
| `--quiet` | bool | `false` | Suppress the per-event progress stream |
| `--task-commit` | string | `—` | Task layer: commit the complete task at the end; bare flag derives "ape:task/<skill>" |

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

## ape update

Update ape to the latest version

```
ape update [flags]
```

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

