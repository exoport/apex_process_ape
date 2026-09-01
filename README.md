# ape — APEX Process Engine

`ape` is a single-binary CLI that runs APEX framework pipelines on projects: collapsing six-to-eight skill invocations through Claude Code into one command, managing ADRs, governance patterns, and traits, and giving you a Bubble Tea TUI to watch each step land.

> **Status:** pre-1.0. Public API and command surface may change between minor releases until `v1.0.0`. See [CHANGELOG.md](CHANGELOG.md).

## Why ape

APEX is a planning-and-implementation framework for Claude Code that breaks software work into a sequence of named skills (`apex-create-prd`, `apex-create-architecture`, `apex-create-epics-and-stories`, etc.). On its own, exercising APEX means invoking each skill manually. `ape` collapses those into named pipelines (`design`, `governance`, `epics`, …) with pre-flight prerequisite checks, prompt-quoting safety, and a three-panel Bubble Tea TUI that streams per-skill events live, lets you scroll back through completed stages, and asks for confirmation before quitting.

## Install

The fastest path on Linux x64:

```bash
VERSION=$(curl -fsSL https://api.github.com/repos/exoport/apex_process_ape/releases/latest | jq -r .tag_name)
curl -fsSL "https://github.com/exoport/apex_process_ape/releases/download/${VERSION}/ape_linux_amd64.tar.gz" \
  | sudo tar -xz -C /usr/local/bin ape
ape version
```

For macOS, Windows, `go install`, or build-from-source paths, see [docs/how-to/install.md](docs/how-to/install.md).

The other recommended install path is to pin a specific release in your project repo using [bingo](https://github.com/bwplotka/bingo):

```bash
# In your project repo:
bingo get -l github.com/exoport/apex_process_ape/cmd/ape@latest 
# or a specific release tag, e.g. v0.0.38
```

## Quickstart

`ape` operates on the working directory. The first time you use ape against an APEX-bootstrapped project, install the framework:

```bash
# One-time: install framework skills + pipelines, seed _apex/config.yaml
# via an interactive Bubble Tea prompt.
export APEX_FRAMEWORK_REPO=/path/to/apex_process_framework
ape framework setup
```

Subsequent framework version bumps refresh in-place:

```bash
# Refresh skills + pipelines against the framework repo's current HEAD.
# Does not touch _apex/config.yaml.
ape framework update
```

Then run pipelines:

```bash
# Run the design pipeline: prd → ux-design → architecture (with shards).
ape pipeline design

# Run governance scaffolding: pattern + adr + capability/feature activation.
ape pipeline governance

# Generate epics + stories from a one-line product brief.
ape pipeline epics --prompt "minimal greeter app, single screen, no auth"

# Disable the TUI for scripted runs (auto-detected on non-TTY).
ape pipeline design --no-tui
```

Pre-flight checks run before any Claude invocation. If a pipeline requires upstream artifacts (e.g., `governance` needs `architecture.md`), `ape` fails fast with a message naming the missing file and the upstream pipeline that produces it.

Every run writes a manifest, a report, and the hook/bridge/checkpoint streams under `{output_folder}/ape/` — the `output_folder` from `_apex/config.yaml`, `_output` by default. ape owns that one subtree and writes nothing outside it; the rest of the output folder is the framework's. See [How to read the output folder](docs/how-to/run-artefacts.md).

## Pipeline TUI

While a pipeline runs, the TUI shows three regions:

- **Top-left, ~70% width** — live event feed for the active or pinned stage. Streams human-readable summaries of `claude` activity (`🔧 Read foo.md`, `✎ Drafting ADR table`, `↳ ⚠ validation failed`, `✓ skill complete`) as they arrive — no more frozen output between stages.
- **Top-right, ~30% width** — ordered stage list. Status glyph (✓/✗/▸/⏳), stage name, elapsed time. Cursor row marked `>`.
- **Bottom strip** — cursor stage's current step, elapsed time, and verdict.

Keybindings:

| Key             | Action                                                                                                                                                               |
| --------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `↑` / `k`       | Move cursor up the stage list.                                                                                                                                       |
| `↓` / `j`       | Move cursor down.                                                                                                                                                    |
| `Enter`         | Pin the event panel to the cursor's stage (Pinned mode).                                                                                                             |
| `L` / `Esc`     | Return to Live mode — cursor snaps to the running stage.                                                                                                             |
| `PgUp` / `PgDn` | Scroll the event panel (Pinned mode only).                                                                                                                           |
| `Home` / `End`  | Jump to first / latest event (Pinned mode).                                                                                                                          |
| `q` / `Ctrl+C`  | Open the quit-confirmation modal. `y` aborts the run and SIGKILLs the in-flight `claude` subprocess; `n` / `Esc` dismisses. Two Ctrl+C presses within 1s force-quit. |

`--no-tui` mode (auto-enabled on non-TTY) streams the same human-friendly events to stdout, prefixed with timestamp + stage + skill:

```
[20:08:42] design · apex-create-architecture · 🔧 Read development/planning/prd/index.md
[20:08:43] design · apex-create-architecture · ✎ Drafting ADR table: 4 candidates
[20:09:18] design · apex-create-architecture · ✓ skill complete (3 turns)
```

## A shared board

`ape aboard` serves a browser UI for a project whose state a human and one or more agent
sessions read and write. Tabs are *data*, not code: an agent opens one for whatever it
needs to show — a graph, a chart, a question form, an annotated screenshot — and reads
back what the human changed.

```bash
ape aboard init --example     # create .aboard/ with a demo board
ape aboard serve              # run the server; prints the URL
ape aboard status             # what is running here, and on which port
```

The board is [aboard](https://github.com/exoport/aboard), a separate public module whose
command tree ape **mounts** rather than reimplements, so every `aboard` subcommand is
available as `ape aboard <cmd>`. Both hosts resolve the same `.aboard/`, derive the same
port from it, and write the same state file — a board started by `ape aboard serve` is
the board a bare `aboard status` reports, and either binary can drive it. You do not need
both installed.

There is also a [VS Code extension](https://github.com/exoport/aboard_vscode) that puts
the board's tabs in the sidebar and the board itself in a panel; its **Start a board**
button runs `ape aboard serve` in a project that has an `_apex/` directory.

Full guide: [How to use the board](docs/how-to/use-the-board.md).

## Isolated dev workspaces

`ape sandbox` provisions a **hardware-isolated Kata microVM per project** — its own guest
kernel over KVM — and works inside it across many sessions:

```bash
ape sandbox up dev            # provision (repos mounted, egress allowlisted)
ape sandbox attach dev        # interactive shell in the VM
ape sandbox exec dev -- go test ./...
ape sandbox stop dev          # free RAM, keep the rootfs + state
ape sandbox down dev          # tear down
```

A committed [`.apesandbox.yaml`](docs/reference/apesandbox-yaml.md) describes the
workspace: which repos to mount (each at `/workspace/<name>`), extra mounts, the egress
domains to request, and the toolchain to materialize. Everything in it is a **request** —
the daemon re-checks every path against its own policy, so a committed file can never
reach a host path an operator has not allowed.

- **Allowlisted egress.** Deny-by-default through an audited CONNECT proxy: no DNS in the
  guest, no route out except that proxy, and every connection recorded.
- **Your Claude session, shared.** A login or token refresh on the host or in any workspace
  converges everywhere — one OAuth session, not a copy per VM.
- **Declared toolchains.** `.tool-versions` + `.bingo` materialize into durable host
  caches, so a rebuild is offline and a workspace is disposable.
- **The `ape` you upgraded to.** The daemon mounts its own `ape` into the workspace, so the
  version you work with inside is the version driving it — not whatever the image was built
  with. A project that wants a specific one still pins it with `bingo`.

Needs a Linux host with KVM + containerd + Kata, and the `aped` daemon: see
[How to run aped](docs/how-to/run-aped.md) and
[How to use sandbox workspaces](docs/how-to/sandbox-workspaces.md). `ape doctor` reports
what is missing.

## Commands

**Framework and pipelines**

| Command                | What it does                                                                               |
| ---------------------- | ------------------------------------------------------------------------------------------ |
| `ape framework setup`  | One-time install: copy skills + pipelines into a project, bootstrap `_apex/config.yaml`.   |
| `ape framework update` | Refresh skills + pipelines against the framework repo (preserves config.yaml).             |
| `ape framework status` | Inspect the installed framework version + drift report.                                    |
| `ape pipeline [name]`  | List installed pipelines; with a name, run the named pipeline.                             |
| `ape planning`         | Show the planning pipeline diagram.                                                        |

**Driving Claude**

| Command                | What it does                                                                               |
| ---------------------- | ------------------------------------------------------------------------------------------ |
| `ape task <skill>`     | Run a single framework skill (no pipeline YAML) through the interactive PTY runner.        |
| `ape prompt [text]`    | Drive an unattended Claude session from a prompt or `--handoff` file; Stop-hook completion. |
| `ape chat`             | Bridged `claude` REPL with hooks captured to a runlog.                                     |
| `ape script <file.go>` | Run a Go orchestration script through the yaegi interpreter with the `apescript` library injected. |
| `ape sessions`         | List, prune, or open the URL of live ape sessions (`open`, `prune`).                       |

See [Choosing between `ape chat`, `ape task`, and `ape prompt`](docs/explanation/chat-task-prompt.md) to pick one.

**Project data** — the records the framework skills read and write. Full guide: [How to work with project data](docs/how-to/work-with-project-data.md).

| Command          | What it does                                                                                     |
| ---------------- | -------------------------------------------------------------------------------------------------- |
| `ape config`     | `resolve` the project's APEX configuration — the folder variables everything else routes through. |
| `ape adr`        | Architecture Decision Records (`list`, `new`, `verify`, `sync`, `update`).                        |
| `ape pattern`    | Governance patterns (`list`, `verify`, `sync`, `update`).                                         |
| `ape feature`    | The feature registry (`list`, `verify`, `sync`, `update`).                                        |
| `ape capability` | The capability registry (`list`, `verify`, `sync`, `update`).                                     |
| `ape registry`   | `verify` or `sync` every record registry at once.                                                 |
| `ape story`      | Story frontmatter projection and verification (`fields`, `verify`).                               |
| `ape sprint`     | Inspect and maintain `sprint-status.yaml` (`check`, `verify`, `reconcile`).                       |
| `ape memory`     | Read the team-memory file without loading it whole (`check`, `index`, `show`).                    |
| `ape deferred`   | The deferred-work record store (`list`, `ingest`, `close`, `verify`, `repair`, `migrate`).        |
| `ape doc`        | Shard, assemble and survey Markdown documents (`shard`, `assemble`, `analyze`, `verify`).         |
| `ape trait`      | Inspect APEX traits (`list`, `show`, `validate`, `conflicts`).                                    |
| `ape bootstrap`  | Bootstrap governance artifacts from declared traits.                                              |

**The board**

| Command       | What it does                                                                                       |
| ------------- | ---------------------------------------------------------------------------------------------------- |
| `ape aboard`  | A shared visual board for a human and one or more agent sessions — `serve`, `status`, `apply`, … |

**Sandbox workspaces**

| Command       | What it does                                                                          |
| ------------- | --------------------------------------------------------------------------------------- |
| `ape sandbox` | Provision hardware-isolated Kata microVM dev workspaces through `aped` (Linux + KVM). |

**Telemetry and NATS**

| Command                 | What it does                                                                    |
| ----------------------- | --------------------------------------------------------------------------------- |
| `ape event <event>`     | Publish a session progress event over NATS (identity + session id baked in).    |
| `ape log <level> <msg>` | Publish a structured log record over NATS.                                      |
| `ape metrics`           | Scan and publish this session's per-model usage metrics over NATS.              |
| `ape transcript upload` | Upload this session's transcript set as content-addressed blobs over NATS.      |
| `ape service`           | Run a NATS-micro job daemon that accepts pipeline/task jobs over request/reply. |

**Diagnostics and maintenance**

| Command        | What it does                                                                                                 |
| -------------- | ---------------------------------------------------------------------------------------------------------------- |
| `ape costs`    | This project's Claude cost rollup; `coverage` audits the price table, `reprice` recomputes stored costs.     |
| `ape doctor`   | Probe the environment and the project; per-check verdict (human/json/yaml). `--only` / `--skip` narrow the run. |
| `ape update`   | Self-update to the latest release.                                                                           |
| `ape rollback` | Roll back to the previously installed binary.                                                                |
| `ape version`  | Print version, build date, and git commit.                                                                   |

Run `ape <command> --help` for command-specific flags, or see the generated [CLI reference](docs/reference/cli.md) for every command, flag, and default.

> `ape sync adrs` / `ape sync patterns` are now `ape adr sync` / `ape pattern sync`. The verb-first spellings still work as hidden pointers.

## Updating

```bash
ape update
```

Self-updates to the latest release. Use `ape rollback` to undo the most recent update. Full details: [docs/how-to/update.md](docs/how-to/update.md).

## Documentation

Full docs follow the [Diátaxis](https://diataxis.fr/) framework — pick the quadrant that matches what you need:

- **[Tutorials](docs/tutorials/)** — learn ape by walking through complete examples.
- **[How-to guides](docs/how-to/)** — recipes for specific tasks (install, update, CI, etc.).
- **[Reference](docs/reference/)** — exhaustive command, pipeline, and config descriptions.
- **[Explanation](docs/explanation/)** — design rationale and conceptual background.

Start at [docs/README.md](docs/README.md) for a guided index.

## Development

```bash
git clone https://github.com/exoport/apex_process_ape.git
cd apex_process_ape
make help          # available targets
make tools         # build the pinned dev tools (golangci-lint, gofumpt, goreleaser) under $GOBIN
make build         # build ./ape and ./aped
make test          # run tests with -race
make lint          # golangci-lint (pinned via bingo)
make govulncheck   # scan dependencies for known vulnerabilities
make pre-commit    # run all pre-commit hooks
make ci-local      # the full pre-push gate — everything CI and the release run
```

CI runs build + test + lint + govulncheck on every push to `main` and every pull request — see [.github/workflows/ci.yml](.github/workflows/ci.yml). The Windows job builds everything but runs `make test-portable`.

### Checking ape against the Claude Code you have

`make ci-local` proves ape is internally consistent. It cannot prove ape still *works*, because ape's real dependency is not a library it pins — it is the `claude` binary on your machine, which auto-updates on a schedule this repo does not control and makes no compatibility promise about its TUI, its flags, its hook payloads, or its transcript format. When one of those moves, nothing errors: ape keeps running and silently stops doing the thing the coupling bought.

```bash
make check-harness
```

Three gates, each reading what the installed Claude Code is *actually* doing: `check-prices` (model ids in local transcripts), `check-hooks` (the hook fields ape's step-completion gates read, judged against a runlog the gate seeds itself with one short unattended session), and `check-claude` (a live PTY session — ready signals, spawn flags, effort level, model aliases, transcript persistence).

None of them run in GitHub CI, which has no `claude`, no auth and no network. `check-prices` reports "not verified" rather than green when it finds no evidence, so **read the output, not just the exit code**.

To read hook drift on a project you have actually run pipelines in, rather than on a seeded one:

```bash
ape doctor --only hooks.contract_drift --strict --cwd ~/work/some-apex-project
```

ape has a second dependency that moves on its own schedule — the APEX framework itself, whose skills call ape subcommands with no fallback branch:

```bash
make check-framework APEX_FRAMEWORK_REPO=/path/to/apex_process_framework
```

That checks the command surface the installed framework declares it requires and the config variables its live template defines.

Details for both: [How to verify a release before tagging](docs/how-to/pre-tag-release.md).

Tooling is pinned via [bingo](https://github.com/bwplotka/bingo) — the `.bingo/` directory contains a per-tool `.mod` file, and `make lint` / `make fmt` / `make snapshot` build the pinned binary on first use. Bumping a version: `go install github.com/bwplotka/bingo@v0.10.0` (one-time), then `bingo get <module>@<version>`.

Releases are cut by pushing a `v*` tag; the [release workflow](.github/workflows/release.yml) runs goreleaser and publishes artifacts to GitHub Releases. Issues and pull requests welcome.

## License

[Apache License 2.0](LICENSE). See [NOTICE](NOTICE) for attribution.
