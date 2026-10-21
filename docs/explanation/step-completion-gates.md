# Why a `Stop` hook is not proof a step finished

This page explains how ape decides that an `ape task` step is done, and why a `Stop` hook alone is not enough. Claude Code's `Stop` hook means *the agent ended its turn*. For almost every step that is also what you want. It is not the same thing as *the work is done*.

In an APEX v1 project a second decision sits on top of ape's own gates: the project's `ape hook stop` can block a `Stop`, and then the session continues. That is [the last section](#v1-the-stop-hook).

## The failure

An orchestrating skill spawns a sub-agent, says something like:

> Review agent for story 1-0 is running. Waiting for its result before proceeding to story 1-3.

…and ends its turn. That fires `Stop`. A runner that trusts it completes the step, writes a manifest, and exits **0**, with no work performed and no terminal contract emitted. Nothing in the run artifacts marks a failure. Only inspecting the project afterwards shows the tree byte-identical before and after.

Two real instances, both batch skills of the APEX v0 framework, on different Claude Code versions:

| Claude Code | What the harness reported at the `Stop` |
| --- | --- |
| 2.1.232 | a detached teammate still outstanding |
| 2.1.220 | a `subagent` creating the next story, `status: running` |

Both exited 0 in about a minute. The second reported `status: completed` on a batch that stopped after its second story.

## The signal is already on the wire

Every `Stop` and `SubagentStop` payload carries `background_tasks`: the tasks still running or pending when the turn ended, with foreground work already filtered out by the harness. An entry there is *by construction* detached from the turn that is ending.

Across ape's local runlog history at the time, 16,965 `Stop` events spanning Claude Code 2.1.199 → 2.1.233, exactly **one** carried a non-empty `background_tasks`: the 2.1.220 run above. Measured against every healthy run, the false-positive rate of gating on this field was zero.

That makes it a better signal than tracking `SubagentStart`/`SubagentStop` pairs:

- It is **atomic with the `Stop`**: one payload snapshot, no cross-event bookkeeping, so hook reordering cannot confuse it. That matters: each hook is a separate `ape notify` process landing on its own goroutine, and `SubagentStop` is async while `Stop` is synchronous.
- It is **immune to a dropped `SubagentStop`**, which would otherwise leave a tracked set non-empty forever and wedge the run.
- It **sees teammates**. A teammate runs through a different code path in the harness and does not appear to emit `SubagentStart` at all, so a pair-tracking gate would miss the very incident above.

## The four gates

**Gate A — the turn boundary.** On `Stop`, classify `background_tasks`:

| Reported | Verdict | Why |
| --- | --- | --- |
| field absent | complete | An older Claude Code. Behave exactly as without this gate. |
| empty | complete | The overwhelmingly common case. |
| `teammate` | **fail now** | Provably unresolvable — see below. |
| `subagent`, `cloud session` | defer | Real work that may still report. |
| unrecognised type | defer | See "unknown types" below. |
| `shell`, `monitor`, `MCP task`, `dream`, `auto-mode scan`, `workflow` | ignore | Background work a skill may legitimately leave running. |

A deferred `Stop` withholds the completion signal and re-decides on the next `Stop`. The gate keeps no state across events.

**Gate B — the spawn.** On an Agent-tool `PostToolUse` whose `tool_response.status` is `teammate_spawned`, fail immediately. This overlaps Gate A's teammate branch on purpose: it fires seconds earlier and names the agent that caused it. Two independent observations of one condition, so losing one upstream costs redundancy rather than correctness.

**Gate C — the terminal contract.** Some skills end a run with a machine-readable return block, and a run that exits without one never reached its summary step. ape does not know the vocabulary: a framework declares it per skill in `_apex/terminal-contracts.csv` (`skill,pattern` rows). Patterns are matched multi-line against the closing assistant message of the step's **own transcript**, not against the hook payload's `last_assistant_message`, because the transcript is a file ape must keep parsing correctly for cost telemetry anyway, so a check built on it survives the harness renaming or dropping a hook field.

Gate C is warning-only: it depends on a dispatching agent relaying its sub-skill's block verbatim, and an agent that paraphrases would turn a framework-side quality problem into a hard ape-side failure. The APEX v1 framework ships no `terminal-contracts.csv`, so in a v1 project Gate C checks nothing. The verdict's field is described in [the run manifest](../reference/pipeline-run-manifest.md#reading-the-contract-field).

**Gate D — detecting the gates going silent.** If Claude Code renames `background_tasks`, nothing errors. Gate A just stops firing, and ape quietly returns to reporting success on runs that did nothing. A gate that can stop firing unnoticed is worse than no gate, because it converts an absent protection into a believed-present one.

`ape doctor --only hooks.contract_drift --cwd <project>` sweeps the project's own hook-event logs and reports whether the fields the gates depend on are still present in recent payloads. Present-but-empty is healthy; absent is drift. This mirrors `ape costs coverage`, which catches a model id changing under the price table for the same reason: the change lands on the harness's schedule, under an already-released ape binary, where no release-time check can see it coming.

Drift that has **already** landed is a different question, and `make check-hooks` answers it at release time. It seeds its own corpus, one short unattended `ape prompt` session in a throwaway project, and judges it at once, so the verdict never depends on finding a project someone happened to run. The session deliberately spawns a sub-agent, because `tool_response` is only counted on Agent-tool `PostToolUse` and `agent_id` only on `SubagentStop`: a seed that merely answered a question would check one field of three and still report green.

## Why teammates fail fast and background sub-agents do not

A teammate is spawned as an addressable agent that reports through a mailbox. The harness renders its tool result as *"The agent is now running and will receive instructions via mailbox."* The parent is never handed a tool result for it. Waiting cannot resolve that, however long you wait, so the gate fails in milliseconds and says why.

A backgrounded sub-agent is the opposite: it is real, it is working, and it may legitimately run for hours. No short timer can distinguish "still working" from "never coming back", and the two errors are not symmetric: killing a healthy sub-agent abandons real work and can leave half-written artifacts, while waiting too long only costs time. So that branch defers, bounded by the idle backstop, which the sub-agent's own hook traffic keeps alive while it works.

The rule that follows: **no path reaches the idle or max-duration backstop for a condition that is decidable at the `Stop`.** Those backstops stay reserved for a genuinely wedged session. A condition the harness has already reported is never waited out.

## Unknown types defer rather than being ignored

The harness maps its internal task kinds through a display-name table before serialising them (`local_agent` → `subagent`, `in_process_teammate` → `teammate`). An unmapped kind falls through under its raw internal name, so a future agent-like task type would arrive as a string this code does not recognise.

Treating the unknown case as benign would silently re-open the hole these gates exist to close. Treating it as blocking costs, at worst, a deferred step bounded by the idle backstop, and Gate D surfaces it immediately. The asymmetry favours deferring.

## Compatibility

ape declares no minimum Claude Code version and does not branch on one. Every gate asserts **positively**: it fires only on evidence that the condition is present. An older harness that sends no `background_tasks`, or never spawns teammates, simply loses the protection and behaves as if the gates were not there, which is why the regression lock for that case is a test, not a version check.

## v1: the stop hook

Gates A to D are ape's reading of the `Stop`. In an APEX v1 project the project's own Stop hook, `ape hook stop` (installed by `ape project init` for `Stop` and `SubagentStop`, with a 1800 s timeout), also has a say, and it can do something ape's gates cannot: **block** the `Stop`. Claude Code then continues the turn with the hook's reason, instead of the session ending.

In a session ape launched (one carrying `APE_SESSION`), the hook blocks (`ape hook stop --help`):

- **while background work runs.** The main session's `Stop` is blocked while `background_tasks` lists anything, of any type, a backgrounded shell and a monitor included. The reason tells the model to wait for the work in this turn, and next time to run a long command in the foreground with a 600000 ms timeout. Gate A ignores a shell or a monitor; the hook does not, so in a v1 project a session cannot end its turn and leave its own background command behind.
- **while a check is red on an in-progress change.** When HEAD is on `change/<id>` and that change is in progress, the hook re-runs the checks that were red at the change's last verify (before any verify, the task checks). If one still exits non-zero, it blocks with the failing checks' last lines. A check too slow to re-run within the hook's budget is judged from the last verify instead, or, when the work moved since, the hook tells the model to run `ape change verify <id> --wait 9m`.

It allows every other `Stop`: HEAD on no change branch, a change not in progress, nothing red, every re-run check green. After 3 consecutive blocks it allows the `Stop`, so a session cannot be held forever.

ape follows those verdicts (`internal/apecmd/v1_stopgate.go`):

- **`ape task`** passes the project's hooks inline in its `--settings`, so the hook runs on every `Stop` whatever `.claude/settings.json` says. On each `Stop` ape waits for the hook's verdict, matched one-to-one and in order from the hook's log under `_apex/state/hooks/`. Only a `Stop` the hook does not block completes the step. A blocked one is a turn the session continues, and ape keeps waiting.
- **`ape prompt`** does the same when `.claude/settings.json` registers the hook. With `--ignore-project-settings` the hook does not run, and the first `Stop` ends the session (`ape prompt --help`).
- Each verdict is recorded under `stop_gate` in the run manifest (or `prompt.yaml`): how many Stops were deferred, how many allowed, and each verdict with its change and reason. See [the run manifest](../reference/pipeline-run-manifest.md#stop_gate--the-v1-stop-hooks-verdicts).

In a person's own Claude Code session the hook never blocks and runs no check. It allows the `Stop` with a message shown to the person when the change in progress had red checks at its last verify, or when background work is still running.

The idle timeout and `--max-duration` still bound a session the hook keeps alive. The hook's full rules, and the other hooks of a v1 project, are in [hooks.md](../reference/hooks.md).

## Related

- [step-contract.md](../reference/step-contract.md): the per-step prompt contract `ape task` enforces.
- [hooks.md](../reference/hooks.md): the v1 project hooks.
- [why-pty-only.md](why-pty-only.md): why the session is a REPL ape types into.
