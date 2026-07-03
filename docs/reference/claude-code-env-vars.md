# Claude Code environment variables (`CLAUDECODE`, `CLAUDE_CODE_*`)

Claude Code heavily utilizes environment variables to control its CLI
behavior, authentication, and execution environment. Among these, the
`CLAUDECODE` variable and others prefixed with `CLAUDE_CODE_*` play critical
roles — especially when dealing with **nested sessions** (one Claude Code
process spawning another), which is exactly the situation every ape run
creates when ape itself is launched from inside a Claude Code session.

> **Scope note.** These variables belong to Claude Code, not ape. They are
> undocumented-or-lightly-documented upstream surface and can change across
> claude-code versions. This page records the behavior ape depends on (and
> defends against); see [How ape handles these](#how-ape-handles-these) for
> ape's own contract.

## 1. The core variable: `CLAUDECODE` and nested-session protection

The most critical environment variable when sessions interact is
**`CLAUDECODE`**.

When you boot up Claude Code, the tool automatically injects `CLAUDECODE=1`
(or sets it to a session identifier) into its active environment.

### Why it matters for nested sessions

If Claude Code attempts to run a bash tool command that invokes *another*
`claude` command (such as a project script like `npm run lint` that calls
`claude plugin validate`, or a recursive script calling `claude -p`), the
child process detects that `CLAUDECODE` is already set in the environment.

- **The guardrail.** To prevent infinite recursive loops, split-brain
  resource competition, and potential context/token crashes, Claude Code
  will immediately abort the child session and throw an error:

  > `Error: Claude Code cannot be launched inside another Claude Code
  > session. Nested sessions share runtime resources and will crash all
  > active sessions. To bypass this check, unset the CLAUDECODE environment
  > variable.`

- **The workaround.** If you are writing a script or running a read-only,
  non-interactive subcommand (like a plugin validator or `--version`) from
  within an active session, you must explicitly clear the variable for that
  specific command:

  ```bash
  CLAUDECODE= claude plugin validate plugin.json
  ```

## 2. Multi-session coordination: `CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS`

While traditional nested interactive sessions are explicitly blocked by
`CLAUDECODE`, Claude Code natively supports multi-session coordination via
**Agent Teams** if explicitly opted in.

- **`CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1`** — enables Claude Code to act
  as a **team lead**. Instead of executing everything sequentially in one
  terminal, the lead session uses coordination tools to spawn multiple
  sub-agents (separate Claude Code instances under the hood) in parallel.
- **How it handles the environment.** When spawning sub-agents (teammates),
  the lead session leverages underlying multiplexers (like `tmux`) or
  handles them in-process. These sub-agents automatically bypass the strict
  `CLAUDECODE` block because they are explicitly managed by the parent
  coordinator rather than run blindly as arbitrary shell commands. They
  inherit the parent's core permission settings and API configurations at
  spawn time.

## 3. Notable `CLAUDE_CODE_*` environment variables

Beyond session nesting, Claude Code reads several `CLAUDE_CODE_` variables
to manage execution limits, configurations, and UX behavior across active
processes.

### Execution and agent sub-processing

| Variable | Effect |
| --- | --- |
| `CLAUDE_CODE_SUBAGENT_MODEL` | Overrides the model identifier used exclusively by worker/sub-agents or background processing loops, allowing heavier tasks to route to Sonnet/Opus and smaller tasks to Haiku. |
| `CLAUDE_CODE_SUBPROCESS_ENV_SCRUB` | When enabled, actively scrubs sensitive tokens and credentials from the environment before spawning child shell processes/sub-commands, so executed tools don't leak tokens into logs. |
| `CLAUDE_CODE_EFFORT_LEVEL` | Controls the default reasoning/thinking budget sent to the API (`low`, `medium`, `high`, `max`, `auto`). Set globally, any sub-session or command follows the same constraint. |

### Context and token limits

| Variable | Effect |
| --- | --- |
| `CLAUDE_CODE_MAX_OUTPUT_TOKENS` | Manually caps the maximum output window size. |
| `CLAUDE_CODE_MAX_CONTEXT_TOKENS` | Overrides the assumed context window length for the session. |
| `CLAUDE_CODE_AUTO_COMPACT_WINDOW` | Adjusts the token thresholds that trigger context compaction (compressing early chat history) during an active multi-turn session. |

### Behavior toggles

| Variable | Effect |
| --- | --- |
| `CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS=1` | Disables experimental beta headers sent to the Anthropic API. Crucial when routing Claude Code through custom LLM gateways (AWS Bedrock, enterprise proxies) that reject unknown headers. |
| `CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1` | Instructs Claude Code to aggressively look for and load parent or adjacent `CLAUDE.md` documentation rules when using the `--add-dir` flag, keeping styling and project rules aligned across split workspaces. |

## How ape handles these

ape spawns `claude` in an interactive PTY for every run
(`internal/repl/repl.go`). When ape itself is launched from inside a Claude
Code session — ubiquitous during development — the child claude would
inherit the parent's nesting markers and treat itself as a nested/child
session. Beyond the hard abort described above, the subtler failure is that
a marker in the `CLAUDE_CODE_*` family **suppresses session-transcript
persistence**: `~/.claude/projects/<cwd>/<sid>.jsonl` is never written,
zeroing every transcript-derived telemetry value. This was the root cause
of the v0.0.28–v0.0.32 zero-telemetry saga.

Since v0.0.32, `repl.ScrubClaudeCodeEnv` strips the following from the
spawned claude's environment so it registers as its own top-level session:

- `CLAUDECODE` — the top-level "running inside Claude Code" flag;
- `CLAUDE_CODE_*` — the whole parent-injected family
  (`CLAUDE_CODE_ENTRYPOINT`, `CLAUDE_CODE_SESSION_ID`,
  `CLAUDE_CODE_CHILD_SESSION`, `CLAUDE_CODE_SSE_PORT`, …). The
  persistence-suppressing marker is in this set; stripping the family is
  robust across claude versions;
- `CLAUDE_EFFORT` — so the child's effort comes from ape's flags, not the
  parent session's inherited effort.

Everything else — `ANTHROPIC_*` auth included — passes through untouched.
The same scrub applies to the inherited-stdio spawn in `ape chat`.

Consequence: if you *want* to set one of the `CLAUDE_CODE_*` variables
above for an ape-spawned claude (e.g. `CLAUDE_CODE_MAX_OUTPUT_TOKENS`), the
scrub removes it along with the nesting markers — configure the equivalent
via claude settings files or ape flags instead.

## Related

- [claude-spawn-modes.md](claude-spawn-modes.md) — how and when ape spawns
  `claude`.
- [bridge-security.md](bridge-security.md) — the bridge's env/settings
  injection surface.
