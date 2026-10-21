# Reference

Reference docs are **information** — exhaustive, accurate, neutral. They describe what exists; they don't teach (that's [Tutorials](../tutorials/)) and they don't recommend (that's [How-to guides](../how-to/) and [Explanation](../explanation/)). A reader consults reference when they need a specific fact.

For ape, reference is the surface area: every command, every flag, every config field, every exit code.

## Available reference

- [cli.md](cli.md) — every command, flag, and default (generated from the command tree; run `make docs-cli` to regenerate, and `make docs-cli-check` — which GitHub CI also runs — to verify it is in sync).
- [change-lifecycle.md](change-lifecycle.md) — the states of a change and every `ape change` verb, the squash archive, checks and limits.
- [config-yaml.md](config-yaml.md) — `_apex/config.yaml` in an APEX v1 project: every key, its default, and the guarded blocks.
- [hooks.md](hooks.md) — the Claude Code and git hooks `ape project init` registers, and what each decides.
- [status-json.md](status-json.md) — `ape status --json` and `ape project check --json`.
- [pipeline-run-manifest.md](pipeline-run-manifest.md) — the run manifest (`manifest.yaml`) an `ape task` run writes.
- [step-contract.md](step-contract.md) — the line `ape task` types and the contract the bridge verifier enforces on it.
- [framework-yaml.md](framework-yaml.md) — `_apex/framework.yaml`, including the migrations ledger.
- [claude-code-env-vars.md](claude-code-env-vars.md) — Claude Code's `CLAUDECODE` / `CLAUDE_CODE_*` environment variables, nested-session protection, ape's env scrub, and the effort rules.
- [claude-spawn-modes.md](claude-spawn-modes.md) — how ape delivers prompts to the PTY-hosted `claude` REPL, per command.
- [apescript.md](apescript.md) — the `apescript` library `ape script` files import.
- [service-api.md](service-api.md) — the `ape service` `ape.svc` endpoint contract: every endpoint subject, request/reply body, error code, admission, and the allowlist/script security boundary.
- [events.md](events.md) — NATS subject taxonomy and event/log/metrics payload schemas.
- [blob-offload.md](blob-offload.md) — the `ape.blob.uri-request` transcript-offload request/reply contract.
- [bridge-ipc.md](bridge-ipc.md) — MCP bridge IPC wire schema.
- [bridge-security.md](bridge-security.md) — the bridge's local listener and threat model.
- [apesandbox-yaml.md](apesandbox-yaml.md) — `.apesandbox.yaml`: the committed per-project sandbox descriptor and the request-vs-grant boundary aped enforces over it.
- [sandbox-profile.md](sandbox-profile.md) — `_apex/sandbox/<name>.yaml`.

Exit codes are listed in each command's `--help` (and so in [cli.md](cli.md)); diagnostic codes in `ape project codes`.

## Writing reference

- Match the structure of the thing you're documenting (commands → command-shaped pages, config → field-shaped pages).
- Be exhaustive within the topic — don't leave out edge cases.
- Be neutral. No recommendations, no opinions, no "you might want X". Save those for [Explanation](../explanation/) or [How-to guides](../how-to/).
- Cross-reference: reference pages should link to the related how-to and explanation pages, not duplicate them.

See the [Diátaxis reference rubric](https://diataxis.fr/reference/).
