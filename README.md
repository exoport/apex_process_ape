# ape — APEX Process Engine

`ape` is a single-binary CLI that runs APEX framework pipelines on projects: collapsing six-to-eight skill invocations through Claude Code into one command, managing ADRs, governance patterns, and traits, and giving you a Bubble Tea TUI to watch each step land.

> **Status:** pre-1.0. Public API and command surface may change between minor releases until `v1.0.0`. See [CHANGELOG.md](CHANGELOG.md).

## Why ape

APEX is a planning-and-implementation framework for Claude Code that breaks software work into a sequence of named skills (`apex-create-prd`, `apex-create-architecture`, `apex-create-epics-and-stories`, etc.). On its own, exercising APEX means invoking each skill manually. `ape` collapses those into named pipelines (`design`, `governance`, `epics`, …) with pre-flight prerequisite checks, prompt-quoting safety, and a three-panel Bubble Tea TUI that streams per-skill events live, lets you scroll back through completed stages, and asks for confirmation before quitting.

## Install

The fastest path on Linux x64:

```bash
VERSION=$(curl -fsSL https://api.github.com/repos/diegosz/apex_process_ape/releases/latest | jq -r .tag_name)
curl -fsSL "https://github.com/diegosz/apex_process_ape/releases/download/${VERSION}/ape_linux_amd64.tar.gz" \
  | sudo tar -xz -C /usr/local/bin ape
ape version
```

For macOS, Windows, `go install`, or build-from-source paths, see [docs/how-to/install.md](docs/how-to/install.md).

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

## Commands

| Command                | What it does                                                                             |
| ---------------------- | ---------------------------------------------------------------------------------------- |
| `ape framework setup`  | One-time install: copy skills + pipelines into a project, bootstrap `_apex/config.yaml`. |
| `ape framework update` | Refresh skills + pipelines against the framework repo (preserves config.yaml).           |
| `ape framework status` | Inspect the installed framework version + drift report.                                  |
| `ape pipeline [name]`  | List installed pipelines; with a name, run the named pipeline.                           |
| `ape adr`              | Manage Architecture Decision Records (`list`, `validate`, `new`).                        |
| `ape pattern`          | Manage governance patterns (`list`).                                                     |
| `ape trait`            | Inspect APEX traits (`list`, `show`, `validate`, `conflicts`).                           |
| `ape sync`             | Sync governance artifacts (placeholder — `patterns` and `adrs` coming soon).             |
| `ape bootstrap`        | Bootstrap governance artifacts from declared traits.                                     |
| `ape update`           | Self-update to the latest release.                                                       |
| `ape rollback`         | Roll back to the previously installed binary.                                            |
| `ape version`          | Print version, build date, and git commit.                                               |

Run `ape <command> --help` for command-specific flags.

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
git clone https://github.com/diegosz/apex_process_ape.git
cd apex_process_ape
make help          # available targets
make tools         # build the pinned dev tools (golangci-lint, gofumpt, goreleaser) under $GOBIN
make build         # build ./ape
make test          # run tests with -race
make lint          # golangci-lint (pinned via bingo)
make govulncheck   # scan dependencies for known vulnerabilities
make pre-commit    # run all pre-commit hooks
```

CI runs build + test + lint + govulncheck on every push to `main` and every pull request — see [.github/workflows/ci.yml](.github/workflows/ci.yml).

Tooling is pinned via [bingo](https://github.com/bwplotka/bingo) — the `.bingo/` directory contains a per-tool `.mod` file, and `make lint` / `make fmt` / `make snapshot` build the pinned binary on first use. Bumping a version: `go install github.com/bwplotka/bingo@v0.10.0` (one-time), then `bingo get <module>@<version>`.

Releases are cut by pushing a `v*` tag; the [release workflow](.github/workflows/release.yml) runs goreleaser and publishes artifacts to GitHub Releases. Issues and pull requests welcome.

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
