# ape documentation

These docs follow the [Diátaxis](https://diataxis.fr/) framework, which splits documentation into four quadrants by user need. Pick the quadrant that matches what you're trying to do:

| If you want to…                                 | Read…                       |
| ----------------------------------------------- | --------------------------- |
| **Learn** ape by following a guided walkthrough | [Tutorials](tutorials/)     |
| **Solve** a specific problem with ape           | [How-to guides](how-to/)    |
| **Look up** a flag, command, or config field    | [Reference](reference/)     |
| **Understand** why ape is the way it is         | [Explanation](explanation/) |

The four quadrants serve different needs and are written in different styles. Tutorials and how-to guides are practical (action-oriented); reference and explanation are theoretical (cognition-oriented). Tutorials and explanation are study-oriented; how-to guides and reference are work-oriented. See the [Diátaxis compass](https://diataxis.fr/compass/) if you're unsure where a given doc belongs.

## Index

New to APEX v1? Start with [What APEX v1.0 promises](explanation/apex-v1-scope.md) (v1.1 keeps
that promise and adds to it: the repository's `CHANGELOG.md` lists what), then
[the tutorial](tutorials/first-v1-project.md). Every command and flag is in the
[CLI reference](reference/cli.md).

### Tutorials — _learning by doing_

- [Your first APEX v1 project](tutorials/first-v1-project.md) — `framework setup` → `project init` → a milestone a person approves → a green `project check` and `doctor --strict`

### How-to guides — _recipes for specific problems_

**Install and set up**

- [How to install ape](how-to/install.md)
- [How to update ape](how-to/update.md)
- [How to verify a release artifact](how-to/verify.md)
- [How to pin ape in a project](how-to/pin-ape-in-a-project.md) — a bare `ape` honours `.bingo/ape.mod`
- [How to install the APEX framework into a project](how-to/framework-setup.md)
- [How to start an APEX v1 project](how-to/start-a-v1-project.md) — `project init`, new or existing (`--brownfield`)
- [How to refresh the framework in a project](how-to/framework-update.md)
- [How to lift an APEX v0 project to v1](how-to/lift-a-v0-project.md)
- [How to move archived changes to the current archive layout](how-to/move-to-the-archive-layout.md)
- [How to pin a config key the framework falls back on](how-to/pin-a-config-key.md) — `ape config pin`

**Run APEX v1 work**

- [How to run a milestone](how-to/run-a-milestone.md) — new, approve, gate, close
- [How to record a decision](how-to/record-a-decision.md) — ADRs, and findings for later in `next.md`
- [How to run a single skill with `ape task`](how-to/run-a-single-skill.md)
- [How to run ape in the background](how-to/detach-a-run.md) — `ape task --detach`, `ape run wait`, and the verbs that detach on their own
- [How to run an unattended session with `ape prompt`](how-to/run-a-prompt-session.md)
- [How to tune long-running sessions](how-to/tune-long-running-steps.md)
- [How to write and run an `ape script`](how-to/write-ape-scripts.md)
- [How to find and read run artefacts](how-to/run-artefacts.md)
- [How to use plugins and tool skills](how-to/use-plugins-and-tools.md) — `plugins:`, `tools:`, `ape plugins status|sync`
- [How to publish a library from an APEX project](how-to/publish-a-library.md) — `library: true`, `ape project fence`, `ape project package-check`

**Integrate**

- [How to use the board (`ape aboard`)](how-to/use-the-board.md)
- [How to run ape as a service (NATS job daemon)](how-to/run-ape-as-a-service.md)
- [How to publish run progress to NATS](how-to/publish-progress-to-nats.md)
- [How to report from a Claude session](how-to/report-from-a-session.md)
- [How to upload run transcripts as content-addressed blobs](how-to/upload-transcripts.md)

**Sandbox workspaces**

- [How to run a sandboxed Kata VM workspace](how-to/sandbox-workspaces.md)
- [How to use a workspace as your dev container](how-to/devcontainer-workspaces.md)
- [How to run aped (the VM-management daemon)](how-to/run-aped.md)

**Operate**

- [How to keep cost pricing current](how-to/keep-cost-pricing-current.md)
- [How to run `ape doctor` in CI](how-to/run-doctor-in-ci.md)
- [How to verify a release before tagging](how-to/pre-tag-release.md)

### Reference — _technical descriptions_

**APEX v1**

- [CLI reference](reference/cli.md) — every command, flag, and default (generated from the command tree)
- [The change lifecycle](reference/change-lifecycle.md) — every state and `ape change` verb, the squash archive, checks and limits
- [`_apex/config.yaml`](reference/config-yaml.md) — every key, its default, and which blocks a person must accept
- [Hooks](reference/hooks.md) — what `ape project init` registers and what each hook decides
- [`ape status` and `ape project check` JSON](reference/status-json.md)
- [Run manifest](reference/pipeline-run-manifest.md) — the `manifest.yaml` an `ape task` run writes
- [Step contract](reference/step-contract.md) — the line `ape task` types, and how the step completes
- [`_apex/framework.yaml`](reference/framework-yaml.md) — what an install records, its migrations ledger included

**Claude Code**

- [Claude Code environment variables](reference/claude-code-env-vars.md) — what ape sets and scrubs in each spawn, and the effort rules
- [How ape spawns `claude`](reference/claude-spawn-modes.md) — per command

**Scripts, services and NATS**

- [`apescript` library reference](reference/apescript.md) — the surface `ape script` files import
- [`ape service` endpoint contract](reference/service-api.md)
- [NATS subjects & event payloads](reference/events.md)
- [Transcript blob offload](reference/blob-offload.md)
- [Bridge IPC wire schema](reference/bridge-ipc.md) and [bridge security model](reference/bridge-security.md)

**Sandbox**

- [`.apesandbox.yaml`](reference/apesandbox-yaml.md) — the project sandbox descriptor
- [Sandbox profile reference](reference/sandbox-profile.md) — `_apex/sandbox/<name>.yaml`

### Explanation — _the why and the what_

- [What APEX v1.0 promises, and where it stops](explanation/apex-v1-scope.md) — start here
- [What only a person does](explanation/person-acts.md) — the person acts, and typing them in your own Claude Code session
- [The orchestrator and the loop](explanation/orchestrator-and-loop.md) — `/apex-orchestrator` and `ape task apex-loop`
- [When `ape` runs inside a session `ape` started](explanation/nested-ape-runs.md) — the nesting rule
- [Choosing between `ape chat`, `ape task`, and `ape prompt`](explanation/chat-task-prompt.md)
- [Why PTY-only execution](explanation/why-pty-only.md)
- [Why a `Stop` hook is not proof a step finished](explanation/step-completion-gates.md)
- [`ape framework update` installs releases](explanation/framework-updates-from-releases.md)
- [The framework and governance clones are caches](explanation/framework-and-governance-clones.md)
- [Why `framework setup` and `framework update` are separate commands](explanation/why-setup-and-update-are-separate.md)

## Contributing to the docs

When adding a new doc, place it in the quadrant that matches its primary user need, not its topic. A page about ADRs could live in any of the four quadrants depending on whether it's teaching, recipe-giving, listing facts, or explaining design. If a page mixes two purposes, split it. The Diátaxis [needs](https://diataxis.fr/needs/) and [systematic approach](https://diataxis.fr/how-to-use-diataxis/) pages are useful when you're unsure.
