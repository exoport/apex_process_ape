# Reference — how ape spawns `claude`

This page lists every command that starts a Claude Code session, how the prompt reaches it, and what ape pins on every spawn. ape has one way to drive `claude`: an **interactive REPL attached to a PTY**. It never passes `-p`; see [why-pty-only.md](../explanation/why-pty-only.md).

## Matrix

| Command | How `claude` runs | Prompt delivery |
| --- | --- | --- |
| `ape task <skill>` | REPL in an in-process PTY (`internal/repl/`) | ape types `/<skill> --autonomous [<args>]`, or `/<agent> --autonomous -- <skill> --autonomous [<args>]` with `--agent`, then Enter. Result envelope with `--output-format json`; `--detach` runs it in the background. |
| `ape prompt [<text>]` | REPL in an in-process PTY | ape types the text as a plain message, or `/<agent> --autonomous -- <text>` with `--agent`. `--handoff <file>` types a line that points at the file. |
| `ape chat` | `claude` inherits ape's stdio: your terminal is its PTY | You type directly into claude. |
| `ape change review <id> --run` | a detached `ape task apex-review --args <id>` | As `ape task`. The verb waits up to `--wait` (default 9m) and exits 75 while the session runs; run it again to attach. |
| `ape change tool <id> <name>` | a detached `ape task <skill> --args "<args filled>"` for a `tools:` entry | As `ape task`, with the same `--wait` and exit 75. |
| `ape change accept <id> [T<n>] --delegate` | a detached `ape task <delegate> --args "<args filled>"` | As `ape task`, with the same `--wait` and exit 75. |

Other ways in reach the same runners:

- An `apex-loop` run (`ape task apex-loop --args <M>`) dispatches each child skill as `ape task <skill> --args <id> --detach --output-format json`, then waits with `ape run wait <id> --wait 9m --json` (`ape task --help`).
- `ape script` drives `ape task` and `ape prompt` runs through the apescript library: "the same runners the CLI uses" (`ape script --help`).
- `ape service` task and prompt jobs run those commands (`ape task --help`, nesting rule).

Which of these may start from inside another ape session is the nesting rule; see [nested-ape-runs.md](../explanation/nested-ape-runs.md).

## The rule

```
Every claude spawn is an interactive REPL attached to a PTY. ape never passes -p.
```

`ape task` and `ape prompt` allocate the PTY in-process and launch `claude --dangerously-skip-permissions` with ape's `--settings` and `--mcp-config` (`buildInteractiveArgv`, `internal/pipeline/interactive.go`; `internal/apecmd/prompt.go`). `ape chat` launches the same flags but lets claude inherit your terminal as its controlling PTY.

How a typed line is delivered, including the 700-character limit and the args file, is in [step-contract.md](step-contract.md).

## Bridge role

Every spawn carries ape's bridge hooks in its `--settings`: `PreToolUse`, `PostToolUse`, `UserPromptSubmit`, `SubagentStart`, `SubagentStop`, `SessionStart` and `PreCompact` asynchronously, and `Stop` synchronously. Each runs `ape notify --event <event>` (`internal/bridge/config/settings.go`). They are for observation and completion: the step contract, the `Stop` that ends a step, and the run's hook-event log.

Every spawn also gets ape's status line in its `--settings`: `statusLine: {type: command, command: "<ape> usage record [--run <key>]"}` (v1.1). It records the plan's rate limits from the JSON Claude Code hands a status line, and shows `5h 14% · 7d 55%` (see [`ape usage`](cli.md#ape-usage)). In `--settings` it applies to the session ape launches only: a `statusLine` in the person's own settings files keeps working in their own sessions, and ape never writes those files.

In an APEX v1 project `ape task` also passes the project's own hooks (`ape hook stop`, `ape hook pre-tool`, …) inline in `--settings`, so its sessions have them even when `.claude/settings.json` does not. See [hooks.md](hooks.md).

## tmux terminal identity is stripped on the PTY path (and only there)

`TMUX` and `TMUX_PANE` describe **which terminal** a process is attached
to. For a child on ape's in-process PTY that description is false: the
child's terminal is the PTY ape holds, while the inherited pane address
belongs to ape itself.

Claude Code reads both and records the pane in its session registry
(`~/.claude/sessions/<pid>.json`) as `"tmux": "session:@window.%pane"`,
without checking that the pane is its own controlling terminal. Driving
that recorded address then fails in two directions at once: the control
message lands as literal text in whatever the operator has in that pane,
and the claude on the PTY sees nothing.

So the PTY spawn path strips both. The child's record carries **no `tmux`
key at all**: the only way into that REPL is ape's PTY master, which no
external process can reach, so a tool reading the registry should conclude
there is no keyboard to drive.

**`ape chat` deliberately does not strip them.** It direct-execs claude
onto the user's real terminal, so inside tmux that child genuinely *is*
in the inherited pane and recording the address is correct and useful.
The asymmetry is intentional and is why the filter is a separate,
unexported function in `internal/repl` rather than two more lines in the
shared `ScrubClaudeCodeEnv`: a caller outside that package cannot reach
it, so it cannot be applied to the chat path by accident. A test asserts
the shared scrubber still passes tmux through, so folding the two
together fails loudly.

## The output style is pinned on every spawn

Every session ape starts is a real Claude Code session in the project
root, so without intervention it inherits whatever **output style** the
machine has configured. That is not cosmetic. An output style claims
precedence over other communication and formatting guidance, and a skill's
fenced return blocks, menus and completion summaries are exactly what that
precedence would rewrite. A developer who set `Concise` for their own
conversations would quietly change what every skill run emits, on their
machine only.

So ape writes `"outputStyle": "Default"` into the `--settings` blob it
already builds, on every spawn path. A skill run is machine-consumed
output rather than a conversation, so the pin is the default rather than
an opt-in.

**The key has to be written explicitly.** Claude Code's precedence is
enterprise-managed → `--settings` → project-local → shared-project →
user, and an *omitted* key falls through to the highest file that defines
one. Leaving it out neutralises nothing.

```bash
ape task <skill> --output-style inherit     # keep the machine's style
ape task <skill> --output-style Explanatory # pin a specific one
```

The flag is on `ape task`, `ape prompt` and `ape chat`. The sessions the `ape change` verbs start take no flag, so they get the `ape task` default below.

### Precedence

`ape task`, highest first (`resolveSkillOutputStyle`, `internal/apecmd/outputstyle.go`):

```text
--output-style flag
  > the skill's row in _apex/output-styles.csv   (skill,style; only when the project has the file)
  > "Default"
```

The APEX v1 framework ships no `_apex/output-styles.csv`, so in a v1 project the flag or `Default` decides. `ape prompt` and `ape chat` read no table: the flag, else `Default`.

### Style names are matched case-insensitively

Claude Code is case-**sensitive** here and ignores a name it cannot
resolve *silently*, so `concise` would enrol nothing and report nothing.
Every declaration site terminates at ape, so ape folds a built-in's
spelling before writing the key: `concise`, `CONCISE` and `Concise` are
one declaration. `inherit` is folded the same way.

A name matching no built-in is written **exactly as declared**, so a
custom style still works.

The cost is deliberate: output styles are an *open* namespace, so a
machine may have its own `.claude/output-styles/concise.md`, and folding
shadows it with the built-in. Resolving custom styles first would mean
enumerating user, project, policy and plugin style directories plus the
`forceForPlugin` override, a second implementation of Claude Code's
precedence, which ape refuses to keep.

Because the built-in list is a vendor surface that moves,
**`make check-output-styles`** gates it against the installed Claude Code,
in the same shape as `make check-prices` for the price table. A stale
table fails by halves: lowercase keeps working for the styles ape knows
and silently stops for a newer one.

### Where an unresolvable name is reported

A name that resolves to nothing, a typo or a custom style this machine
does not have, cannot be told apart by ape, so each surface reports rather
than judges:

| Surface | Reported by |
| ------- | ----------- |
| `_apex/output-styles.csv` | `ape doctor --only framework.output_styles` |
| `--output-style`          | a line on stderr at spawn |

Two limits worth knowing:

- **Enterprise-managed settings still outrank `--settings`.** On a
  managed fleet the pin can be overridden, and ape cannot close that.
- **`--ignore-project-settings` is not a substitute.** It passes
  `--setting-sources user`, which drops the project and local files and
  *keeps* the user one, so a user-level style still reaches the session.

Measured against Claude Code 2.1.259: a custom style named in
`--settings` takes effect, an explicit `Default` there overrides the same
style set in a project settings file, and an unknown style name is
silently ignored rather than rejected. "It did not error" is not evidence
that a value was honoured.

## Source of truth in code

| What | Where |
| --- | --- |
| `ape task`'s PTY spawn and `--settings` prepend | `runWithInteractive`, `buildInteractivePrepend` in `internal/apecmd/pipeline_interactive.go` |
| The typed line and the step loop | `internal/pipeline/interactive.go` |
| PTY driver (NewSession / Deliver / …) | `internal/repl/` |
| `ape prompt` spawn | `internal/apecmd/prompt.go` |
| `ape chat` direct exec with stdio inheritance | `internal/apecmd/chat.go` |
| Detached child sessions of the `ape change` verbs | `internal/apecmd/v1_review_run.go`, `v1_tool.go`, `v1_delegate.go` (`childTaskArgv`), `detach.go` |
| `--settings` blob, incl. the output-style pin | `internal/bridge/config/settings.go` |
| Output-style precedence for `ape task` | `internal/apecmd/outputstyle.go`, `internal/outputstyles/` |

## Related

- [step-contract.md](step-contract.md): what `ape task` types into the REPL, and what it checks.
- [claude-code-env-vars.md](claude-code-env-vars.md): the environment every spawn gets, and the effort rule.
- [../explanation/why-pty-only.md](../explanation/why-pty-only.md): why PTY is the only exec mode.
- [../explanation/chat-task-prompt.md](../explanation/chat-task-prompt.md): choosing between `ape chat`, `ape task` and `ape prompt`.
- [bridge-ipc.md](bridge-ipc.md): wire schema for the MCP bridge.
