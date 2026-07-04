# Reference

Reference docs are **information** — exhaustive, accurate, neutral. They describe what exists; they don't teach (that's [Tutorials](../tutorials/)) and they don't recommend (that's [How-to guides](../how-to/) and [Explanation](../explanation/)). A reader consults reference when they need a specific fact.

For ape, reference is the surface area: every command, every flag, every config field, every exit code.

## Available reference

- [bridge-ipc.md](bridge-ipc.md) — MCP bridge IPC wire schema.
- [bridge-security.md](bridge-security.md) — bridge bind + threat model.
- [claude-code-env-vars.md](claude-code-env-vars.md) — Claude Code's `CLAUDECODE` / `CLAUDE_CODE_*` environment variables, nested-session protection, and ape's env scrub.
- [claude-spawn-modes.md](claude-spawn-modes.md) — how ape delivers prompts to the PTY-hosted `claude` REPL, per command.
- [framework-yaml.md](framework-yaml.md) — `_apex/framework.yaml` schema.
- [invocation-matrix.md](invocation-matrix.md) — the `ape pipeline` UI selector (`tui` default, `web`, `no-tui`).
- [pipeline-run-manifest.md](pipeline-run-manifest.md) — `manifest.yaml` schema written per pipeline run.
- [pipeline-spec.md](pipeline-spec.md) — internal Go shape of a parsed pipeline.
- [pipeline-yaml-schema.md](pipeline-yaml-schema.md) — `_apex/pipelines/*.yaml` schema (model/agent/commit defaults, `no-clear`, etc.).
- [step-contract.md](step-contract.md) — per-step contract enforced by the bridge verifier.
- [tui-keybindings.md](tui-keybindings.md) — Bubble Tea TUI keybindings.

## Planned reference

- **Command reference.** Every `ape` subcommand, every flag, every argument. Auto-generatable from cobra (`cobra-cli docs`) — likely the right path once the command surface stabilizes.
- **Pipeline catalog.** Every built-in pipeline (`design`, `governance`, `epics`, …) with its stages, the skills each stage runs, and the pre-flight files each pipeline expects.
- **Configuration reference.** The `_apex/config.yaml` schema: every field, type, default, and where it's consumed.
- **Trait reference.** Available APEX traits, their conflicts, and the artifacts they produce.
- **Exit codes.** What each non-zero exit means and which command emits it.

## Writing reference

- Match the structure of the thing you're documenting (commands → command-shaped pages, config → field-shaped pages).
- Be exhaustive within the topic — don't leave out edge cases.
- Be neutral. No recommendations, no opinions, no "you might want X". Save those for [Explanation](../explanation/) or [How-to guides](../how-to/).
- Cross-reference: reference pages should link to the related how-to and explanation pages, not duplicate them.

See the [Diátaxis reference rubric](https://diataxis.fr/reference/).
