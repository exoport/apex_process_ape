# ape — the APEX v1 engine

`ape` is a single-binary CLI that runs APEX v1 work on a project. It owns the
records of the APEX v1 loop: a person approves a milestone, each change is
planned, applied on its own branch, verified, reviewed in a fresh session and
archived as one squash commit, and a person closes the milestone. The
framework's v1 skills do the work, each in a Claude Code session that ape
drives through a PTY; ape keeps the records (`development/`, `_apex/state/`)
and refuses what a session may not do.

> **Status:** v1.0. It is built for one operator per checkout, working on a
> local trunk; what v1.0 promises, and what it leaves out, is in
> [What APEX v1.0 promises](docs/explanation/apex-v1-scope.md). An APEX v0 project keeps
> the v0 ape its `.bingo` pins until it is lifted to v1
> ([How to lift a v0 project](docs/how-to/lift-a-v0-project.md)). See
> [CHANGELOG.md](CHANGELOG.md).

## Install

The fastest path on Linux x64:

```bash
VERSION=$(curl -fsSL https://api.github.com/repos/exoport/apex_process_ape/releases/latest | jq -r .tag_name)
curl -fsSL "https://github.com/exoport/apex_process_ape/releases/download/${VERSION}/ape_linux_amd64.tar.gz" \
  | sudo tar -xz -C /usr/local/bin ape
ape version
ape doctor
```

For macOS, Windows, `go install` (Go 1.27.3 or later) or a build from source,
see [How to install ape](docs/how-to/install.md).

A project can pin its ape with [bingo](https://github.com/bwplotka/bingo):

```bash
# In your project repo:
bingo get -l github.com/exoport/apex_process_ape/cmd/ape@v1.0.0
```

A bare `ape` run inside that project then runs the pinned version. See
[How to pin ape in a project](docs/how-to/pin-ape-in-a-project.md).

## Quickstart: a new project

```bash
mkdir greeter && cd greeter && git init -q
ape framework setup          # install the framework's v1 skills; ends with "next: ape project init"
ape project init             # development/, _apex/config.yaml, AGENTS.md, hooks; commits "apex: project init"
# fill in development/project.md (vision, users, scope, out of scope, constraints)
ape milestone new "Walking skeleton" --gate "go test ./..." --goal "greets a name"
ape milestone approve M1 --by "$(git config user.name)"   # a person's act: from a plain terminal
```

Then open Claude Code in the project and type `/apex-orchestrator`, or run the
milestone unattended from a terminal with `ape task apex-loop --args M1`.

The [first v1 project tutorial](docs/tutorials/first-v1-project.md) walks
through each step.

## Upgrading an APEX v0 project

```bash
ape update --pin                                         # this ape, and the project's bingo pin (committed)
git rev-parse @{u} >/dev/null 2>&1 || git push -u origin "$(git branch --show-current)"   # the lift needs an upstream
git tag -a apex-v0-final -m "APEX v0 corpus before the v1 lift"
git push origin apex-v0-final                            # the lift needs the tag at HEAD and on the upstream
ape project lift --dry-run                               # what the lift would do, writing nothing
ape framework update                                     # installs v1 and runs the lift
```

Move the pin before tagging: the lift refuses when HEAD is past `apex-v0-final`.

The lift writes a disposition of everything v0 left behind, for `apex-plan` to
review with you. Its verdict on the project's regression checks says whether
the lift broke anything. See
[How to lift a v0 project](docs/how-to/lift-a-v0-project.md).

## How the loop works

1. A person approves a milestone and its gate, a command that must fail until the milestone's work is done.
2. `apex-loop` plans each change (`apex-change`), applies it on `change/<id>` (`apex-apply`) and verifies it (`ape change verify`).
3. A separate session reviews it (`ape change review --run`).
4. `ape change archive` squashes it onto the base branch.
5. The milestone closes when its gate passes (`ape milestone close`), or a person accepts it red.

[The change lifecycle](docs/reference/change-lifecycle.md) lists every state
and verb. [The operator and the loop](docs/explanation/operator-and-loop.md)
explains the `/apex-orchestrator` session and the `apex-loop` runner.

## What only a person does

Approving a milestone or a tweak (and re-approving one with `--accept-green`),
answering a blocked change, accepting a human-review task, a decision or a
changed config, archiving past a pending human review, and closing a milestone
red (`--accept-red`). ape refuses these inside a model session. Run them from a
plain terminal, or type the act (for example `approve M1`) in your own Claude
Code session. See [What only a person does](docs/explanation/person-acts.md).

## Commands

Run `ape <command> --help` for a command's flags, codes and exit codes, or see
the generated [CLI reference](docs/reference/cli.md).

**Project and roadmap**

| Command | What it does |
| --- | --- |
| `ape project` | Create and check an APEX v1 project layout (`init`, `check`, `codes`, `lift`, `accept-config`, …). |
| `ape milestone` | Add, approve, gate and close roadmap milestones. |
| `ape next` | Record a finding for later in `development/next.md`. |
| `ape decision` | Record architecture decisions (ADR-NNNN) in `development/decisions/`. |
| `ape status` | The current milestone, active changes, blocks, pending human review and spend (`--json`). |

**Changes**

| Command | What it does |
| --- | --- |
| `ape change` | The unit of change: new, check, approve (tweaks), start, verify, review, accept, block, answer, archive, tool. |

**Sessions**

| Command | What it does |
| --- | --- |
| `ape task <skill>` | Run a single framework skill in a Claude Code session (`--detach` to run it in the background). |
| `ape run` | Wait for, or inspect, a run started with `ape task --detach`. |
| `ape prompt [text]` | Drive an unattended Claude session from a prompt or a handoff file. |
| `ape chat` | Bridged `claude` REPL with hooks captured to a runlog. |
| `ape script <file.go>` | Run a Go orchestration script through the yaegi interpreter. |
| `ape sessions` | List, prune, or open the URL of live ape sessions. |

See [Choosing between `ape chat`, `ape task`, and `ape prompt`](docs/explanation/chat-task-prompt.md).

**Framework and configuration**

| Command | What it does |
| --- | --- |
| `ape framework` | Install (`setup`), update (`update`) and inspect (`status`) the APEX framework in a project. |
| `ape plugins` | Install the plugins `_apex/config.yaml` lists, and report what they and the tools slot bring. |
| `ape hook` | The Claude Code and git hooks of an APEX v1 project (installed by `ape project init`). |
| `ape config` | Resolve the project's APEX configuration (`resolve`, `effort`, `pin`). |

**Platform**

| Command | What it does |
| --- | --- |
| `ape aboard` | A shared visual board for a human and one or more agent sessions. |
| `ape sandbox` | Provision hardware-isolated Kata microVM workspaces through `aped` (Linux + KVM). |
| `ape service` | A NATS-micro job daemon that accepts task, prompt and script jobs over request/reply. |
| `ape event`, `ape log`, `ape metrics` | Publish session progress events, log records and usage metrics over NATS. |
| `ape transcript` | Work with Claude session transcripts (`upload` over NATS). |

**Maintenance**

| Command | What it does |
| --- | --- |
| `ape costs` | This project's Claude cost rollup; `coverage` audits the price table, `reprice` recomputes stored costs. |
| `ape doctor` | Probe the host and the project; a verdict per check (`--only`, `--skip`, `--strict`, `--fix`). |
| `ape update` | Update ape to the latest release (`--pin` to move a project's bingo pin). |
| `ape rollback` | Roll back to the previously installed binary. |
| `ape version` | Print version, build date and git commit. |

## Where things live

| Path | What it holds |
| --- | --- |
| `development/project.md`, `roadmap.md`, `next.md` | What the project is, its milestones, and findings for later. |
| `development/changes/`, `archive/` | Changes in flight; archived and abandoned changes. |
| `development/specs/`, `decisions/`, `briefs/` | Specs born from changes, ADRs, and each plan's input. |
| `_apex/config.yaml` | Project settings: models, limits, checks, plugins. Some blocks change only with a person's `ape project accept-config` ([reference](docs/reference/config-yaml.md)). |
| `_apex/state/` | ape's own records: `milestones.json` and `decisions.json` are tracked; `runs/`, `logs/` and `hooks/` are ignored. |
| `.claude/settings.json` | The hooks `ape project init` merges in ([reference](docs/reference/hooks.md)). |

Every run ape starts (task, prompt, chat, detached runs, service jobs) records
under `_apex/state/runs/`. See [How to read run artefacts](docs/how-to/run-artefacts.md).

## A shared board

`ape aboard` serves a browser UI for a project whose state a human and one or more agent
sessions read and write. Tabs are *data*, not code: an agent opens one for whatever it
needs to show — a graph, a chart, a question form, an annotated screenshot — and reads
back what the human changed.

```bash
ape aboard init --example     # create .aboard/ with a demo board
ape aboard serve              # run the server; prints the URL
ape aboard serve --detach     # from an agent session: survives the session restarting
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

## Updating ape and the framework

```bash
ape update                     # move ape to its newest release (ape rollback undoes it)
ape framework update --plan    # what a framework update would migrate, doing nothing else
ape framework update           # install the newest framework release and run its migrations
```

A framework release names the oldest ape it works with (`min_ape_version`);
`ape framework setup` and `update` refuse an older ape with exit 11. See
[How to update ape](docs/how-to/update.md) and
[How to update the framework](docs/how-to/framework-update.md).

## Documentation

The docs follow [Diátaxis](https://diataxis.fr/):

- **[Tutorials](docs/tutorials/)** — [your first APEX v1 project](docs/tutorials/first-v1-project.md).
- **[How-to guides](docs/how-to/)** — install, set up and update, run milestones and sessions, lift a v0 project, CI.
- **[Reference](docs/reference/)** — the [CLI reference](docs/reference/cli.md), config, hooks, run manifests.
- **[Explanation](docs/explanation/)** — start with [What APEX v1.0 promises](docs/explanation/apex-v1-scope.md).

Start at [docs/README.md](docs/README.md) for the full index.

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

Seven gates, each reading what the installed Claude Code is *actually* doing: `check-prices` (model ids in local transcripts), `check-output-styles` (the built-in output-style names ape folds declarations onto), `check-hooks` (the hook fields ape's step-completion gates read, judged against a runlog the gate seeds itself with one short unattended session), `check-claude` (a live PTY session — ready signals, spawn flags, effort level, model aliases, foreground sub-agents staying foreground, transcript persistence), `check-task-subagents` (one `ape task` run of a skill that fans out to two foreground sub-agents), `check-agents-md` (the installed Claude Code still loads this repo's `AGENTS.md`), and, last, `check-claude-surface` (the tool list, the binary's `CLAUDE_*` variables and unread CHANGELOG entries, diffed against a reviewed baseline; unread entries are a review to do, not a breakage).

None of them run in GitHub CI, which has no `claude`, no auth and no network. `check-prices` reports "not verified" rather than green when it finds no evidence, so **read the output, not just the exit code**.

To read hook drift on a project ape has run sessions in, rather than on a seeded one:

```bash
ape doctor --only hooks.contract_drift --strict --cwd ~/work/some-apex-project
```

ape has a second dependency that moves on its own schedule — the APEX framework itself, whose skills call ape subcommands with no fallback branch:

```bash
make check-framework APEX_FRAMEWORK_REPO=/path/to/apex_process_framework APEX_PROJECT=/path/to/an/install
```

Against an APEX v1 framework it checks that every `ape` command the framework's skills name resolves on this binary, that the binary meets the framework's `min_ape_version`, and that the standard ADR-0001 matches.

Details for both: [How to verify a release before tagging](docs/how-to/pre-tag-release.md).

Tooling is pinned via [bingo](https://github.com/bwplotka/bingo) — the `.bingo/` directory contains a per-tool `.mod` file, and `make lint` / `make fmt` / `make snapshot` build the pinned binary on first use. Bumping a version: `go install github.com/bwplotka/bingo@v0.10.0` (one-time), then `bingo get <module>@<version>`.

Releases are cut by pushing a `vX.Y.Z` tag (or `vX.Y.Z-rc.N` for a prerelease); the [release workflow](.github/workflows/release.yml) runs goreleaser and publishes artifacts to GitHub Releases. Issues and pull requests welcome.

## License

[Apache License 2.0](LICENSE). See [NOTICE](NOTICE) for attribution.
