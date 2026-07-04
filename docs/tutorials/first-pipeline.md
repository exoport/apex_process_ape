# Your first pipeline

This tutorial walks you from a fresh install to a completed pipeline run
and its cost breakdown. By the end you will have installed ape, verified
your environment, installed the APEX framework into a project, run the
`design` pipeline, and read the artifacts it produced.

It is a *learning* exercise: follow every step in order on a throwaway
project. For solving a specific problem, see the [how-to
guides](../how-to/); for the why behind PTY-only execution, see
[explanation](../explanation/why-pty-only.md).

## What you need

- A Unix-like shell (Linux or macOS).
- The `claude` CLI on your `PATH`, logged in. ape drives Claude Code — it
  does not talk to the API directly.
- `git`. Pipelines commit their work at stage boundaries by default.

## 1. Install ape

Follow [How to install ape](../how-to/install.md), or grab a release
binary. Confirm it runs:

```console
$ ape --version
ape version 0.0.36
```

## 2. Check your environment

`ape doctor` inspects the things a run depends on — the `claude` binary,
its version, `git`, and your project layout — and tells you what is
missing before a run can fail on it.

```console
$ ape doctor
```

Resolve anything it flags (most commonly: `claude` not found, or not
logged in) before continuing. In CI you can gate on it — see [How to run
`ape doctor` in CI](../how-to/run-doctor-in-ci.md).

## 3. Create a project and install the framework

Pipelines run against a project directory. Make an empty one and turn it
into a git repo:

```console
$ mkdir greeter && cd greeter
$ git init
```

Now install the APEX framework (skills, agents, and the canonical
pipelines) into it:

```console
$ ape framework setup
```

`setup` is the first-install command; later you refresh with `ape
framework update`. The two are deliberately separate — see
[why](../explanation/why-setup-and-update-are-separate.md). After it
finishes you will have an `_apex/` directory (config + pipelines) and a
`.claude/` directory (skills + agents).

Confirm the pipelines are installed:

```console
$ ape pipeline
Pipelines installed at .../greeter/_apex/pipelines:
  design
  epics
  governance
```

## 4. Run the design pipeline

`design` is a multi-stage pipeline: it drafts a PRD, shards it, produces
a UX design and an architecture, sharding each in turn. Each stage runs
an interactive `claude` REPL inside a PTY — you will see a live
three-panel TUI (stages on the left, the model's activity on the right).

```console
$ ape pipeline design
▸ mode: tui (default)
```

Let it run to completion. A few things worth knowing:

- **It commits as it goes.** Each stage boundary makes a git commit
  (`ape:design/<stage>/<skill>`). ape refuses to start on a dirty tree so
  your work-in-progress never gets folded into its commits — commit or
  stash first, or pass `--no-commit` to leave the tree untouched.
- **Rendering is a choice, execution is not.** `--tui` (the default),
  `--web` (a browser UI), and `--no-tui` (plain stdout) only change where
  output renders; every mode runs the same interactive PTY. There is no
  `claude -p` mode — see [why PTY-only](../explanation/why-pty-only.md).
- **Ctrl-C** twice (or once, then confirm) stops the run and tears down
  the `claude` subprocess.

If a step stalls waiting for a first run's folder-trust prompt, ape
dismisses it automatically; a genuinely stuck REPL times out with the
last screen on stderr rather than hanging.

## 5. Read the run artifacts

Every run writes a manifest tree under `_output/pipelines/<name>/`. The
`latest` symlink points at the most recent run:

```console
$ ls _output/pipelines/design/latest/
manifest.yaml   pipeline-report.md   <per-step ndjson logs>   transcripts/
```

- **`manifest.yaml`** — the canonical record: per-stage/per-step status,
  timing, cost, tokens, `num_turns`, the per-model breakdown
  (`model_usage`), per-session usage (`sessions[]`, including any
  sub-agents), and the commit each step made. See the [manifest
  reference](../reference/pipeline-run-manifest.md).
- **`pipeline-report.md`** — a human summary of the same data.
- **`transcripts/`** — copies of the `claude` session transcripts the
  telemetry was scanned from (durable through `~/.claude` rotation).

Open the report first, then dig into `manifest.yaml` when you want the
numbers.

## 6. See what it cost

ape rolls per-run costs up per project. The rollup carries a per-model
breakdown:

```console
$ ape costs
BUCKET            RUNS  COST    INPUT    OUTPUT   CACHE-R
pipeline:design   1     $...    ...      ...      ...

by model:
MODEL             COST    INPUT    OUTPUT   CACHE-R  TURNS
claude-opus-4-8   $...    ...      ...      ...      ...
```

For a single run, pass its id (the `<run-id>` directory name under
`_output/pipelines/design/`):

```console
$ ape costs run 20260704-101530-a1b2c3
```

Cost is derived by scanning the run's transcripts against a built-in
price table; treat it as a close estimate, not a billing statement (see
the note in the manifest reference).

## Where to go next

- Run **one** skill instead of a whole pipeline: [How to run a single
  skill with `ape task`](../how-to/run-a-single-skill.md).
- Look up any command, flag, or default: the [CLI reference](../reference/cli.md).
- Understand the execution model: [why PTY-only](../explanation/why-pty-only.md).
