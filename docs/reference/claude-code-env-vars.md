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
| `CLAUDE_CODE_EFFORT_LEVEL` | Controls the reasoning/thinking budget sent to the API (`low`, `medium`, `high`, `xhigh`, `max`). Set globally, any sub-session or command follows the same constraint. **ape sets this itself**, but only for an explicit `--effort` / pipeline `effort:` value, or `xhigh` on a project with no effort table. Otherwise the table rides in `--settings`, per model. See below. |

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
- `CLAUDE_CODE_EFFORT_LEVEL` (removed via the `CLAUDE_CODE_` prefix, along
  with the legacy `CLAUDE_EFFORT` alias) — so the child's reasoning effort
  comes from ape's own resolution, not the parent session's inherited level.

Everything else — `ANTHROPIC_*` auth included — passes through untouched.
The same scrub applies to the inherited-stdio spawn in `ape chat`.

### Reasoning effort (`CLAUDE_CODE_EFFORT_LEVEL`)

The inherited `CLAUDE_CODE_EFFORT_LEVEL` is always stripped first. What ape
puts back depends on where the effort comes from (see
[Pipeline spec § Reasoning effort](pipeline-spec.md#reasoning-effort)):

- **An explicit effort** (`step.effort ?? stage.effort ?? pipeline.effort ?? --effort`
  on `ape pipeline` / `ape task` / `ape change`, or `--effort` on `ape prompt`)
  is re-injected as this variable. It is **process-wide**: it reaches every
  sub-agent the session spawns and outranks any per-model setting.
- **No explicit effort, with `_apex/effort-defaults.yaml`**: ape sets **no**
  variable. The table rides in the spawn's `--settings` as
  `modelSettings.<model>.effortLevel` plus a top-level `effortLevel`, so each
  model, sub-agents included, runs at its family's row. The variable would
  flatten them all to one level, which is exactly what the table exists to avoid.
- **Neither**: `xhigh`, process-wide, which is what ape did before the table
  existed.
- **`ape chat`** is interactive: it injects the variable **only when
  `--effort` is given**, and takes no table, leaving claude's native effort
  untouched.

Consequence: if you *want* to set one of the `CLAUDE_CODE_*` variables
above for an ape-spawned claude (e.g. `CLAUDE_CODE_MAX_OUTPUT_TOKENS`), the
scrub removes it along with the nesting markers — configure the equivalent
via claude settings files or ape flags instead.

### Background-shell pressure reap (`CLAUDE_CODE_DISABLE_BG_SHELL_PRESSURE_REAP`)

The second variable ape injects, on **every** spawn path, always set to `1`.

Claude Code registers a `memoryPressure` handler for each background shell
task unless this variable is set. When the handler fires it kills the still
running task — status `killed`, reason `memory_pressure`. The event comes
from Bun, off a kernel PSI trigger, and it has been observed firing on a
development host while the PSI averages read `0.00` with ~18 GB available,
killing two background watchers of a session that was not short of memory.

ape turns it off because an ape run is unattended: a background command
killed mid-step is not an error any ape gate can see — the step simply never
finishes. (It is the suspected, unproven cause of a 2h20m stall in a
framework eval run, where a background sub-agent's command was announced and
never completed, with no shell process behind it.)

The trade: under real memory pressure a spawned session's background shells
keep running where Claude Code would have killed them. The kernel's OOM
killer remains the backstop.

There is no flag to ask for the reap back, and setting the variable yourself
does nothing — the scrub above removes it with the rest of the family, and
Claude Code reads it for truthiness, so even `0` disables the reap.

Because a variable ape exports is worthless the moment Claude Code stops
reading it, `make check-claude` carries a `bg_shell_reap_switch` subtest: it
reads the installed binary and fails if the variable has vanished, if the
memory-pressure handler it guards has vanished, or if the two no longer sit
together.

### Foreground sub-agents stay foreground (`CLAUDE_CODE_FORK_SUBAGENT`)

The third variable ape injects, set to `0` on every unattended spawn: `ape
pipeline`, `ape task`, `ape change`, `ape script` and `ape prompt`. Not on
`ape chat`, where a person drives the session and gets Claude Code's own
default.

In an interactive session, which every ape spawn is, Claude Code's
fork-subagent gate is on by default, and while it is on the Agent tool
launches **every** call async, including one that passes
`run_in_background: false`. The caller gets "Async agent launched" instead of
the sub-agent's result, and has to wait for a completion notice. Only
coordinator mode, this variable set to false, or a non-interactive (`-p`)
session turns the gate off. That was read in the 2.1.280 and 2.1.283 binaries
and measured under `ape prompt` on 2.1.283: `run_in_background: false`
returned `async_launched` without the variable and `completed`, with the
result inline, with it.

ape turns the gate off because the framework's skills dispatch foreground
sub-agents and read their results, which is the Agent tool's documented
contract. Under the forced-async default a framework eval run lost about 93
minutes to a poll loop that held every completion notice back.

What it does not change: a call that asks for the background, or that
**omits** `run_in_background`, still launches async. Claude Code's `fork`
sub-agent type is unavailable in ape's sessions.

As with the reap switch, exporting the variable yourself does nothing under
ape, because the scrub removes it. `make check-claude`'s
`foreground_agent_sync` subtest spends one Sonnet turn with a Haiku sub-agent
to assert that a `run_in_background: false` call comes back `completed` in a
session ape spawned.

### `PATH`: `ape` inside a session is *this* ape

One more variable is added rather than removed. Every spawned session gets a
one-entry directory at the front of `PATH` in which `ape` is the binary
that spawned it, and it is removed when the session is reaped.

Without it, the ~69 framework skill files that run `ape …` lines resolve
`ape` through the operator's `PATH` — which is whatever the machine has
installed, and need not be the binary running the dispatch. Observed on a
development machine with both `~/go/bin/ape` and `/usr/local/bin/ape`
present: `ape` 0.0.67 spawned a session and `ape version` *inside* that
session reported **0.0.56**.

That matters because the framework declares an `ape` **version floor**. A
skill running a pre-floor binary inside a dispatch by the post-floor one
makes the floor unenforceable from the inside, and it does so silently —
a stale binary that is merely old still has the commands, still emits
valid output, and simply answers about a world where the newer checks do
not exist. A *missing* command would have errored and been caught.

The pin applies to the PTY path and to `ape chat` alike; unlike the tmux
scrub, there is no asymmetry, because this is about which binary `ape`
names rather than which terminal the child is attached to. If the pin
cannot be made, the run says so and proceeds unpinned — proceeding
unpinned *silently* is the failure it exists to prevent.

The framework's own eval harness reached the same remedy independently
(`apex_eval/runner.py:_pin_ape_on_path`), after observing v0.0.52 answering
for a much newer binary on the same machine.

## Related

- [claude-spawn-modes.md](claude-spawn-modes.md) — how and when ape spawns
  `claude`.
- [bridge-security.md](bridge-security.md) — the bridge's env/settings
  injection surface.
