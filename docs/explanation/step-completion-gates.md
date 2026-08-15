# Why a `Stop` hook is not proof a step finished

Until v0.0.52, `ape` treated Claude Code's `Stop` hook as step completion, full stop. That is what the hook literally means — *the agent ended its turn* — and for almost every step it is also what you want.

It is not the same thing as *the work is done*.

## The failure

An orchestrating skill spawns a sub-agent, says something like:

> Review agent for story 1-0 is running. Waiting for its result before proceeding to story 1-3.

…and ends its turn. That fires `Stop`. `ape` completed the step, wrote a manifest, printed `stage done`, and exited **0** — with no work performed and no terminal contract emitted. Nothing in the run artifacts marked a failure. Only inspecting the tracker afterwards revealed the tree was byte-identical before and after.

Two real instances, on different Claude Code versions:

| Run | Claude Code | What the harness reported at the `Stop` |
| --- | --- | --- |
| `apex-story-batch-review`, 2026-08-14 | 2.1.232 | a detached teammate still outstanding |
| `apex-story-batch-create`, 2026-07-31 | 2.1.220 | `subagent` "Create Story 1.2 via apex-agent-sm", `status: running` |

Both exited 0 in about a minute. The second reported `status: completed` on a batch that stopped after its second story.

## The signal was already on the wire

Every `Stop` and `SubagentStop` payload carries `background_tasks`: the tasks still running or pending when the turn ended, with foreground work already filtered out by the harness. An entry there is *by construction* detached from the turn that is ending.

Across ape's entire local runlog history — 16,965 `Stop` events spanning Claude Code 2.1.199 → 2.1.233 — exactly **one** carried a non-empty `background_tasks`. It is the 2026-07-31 run above. The false-positive rate of gating on this field, measured against every healthy run we have, is zero.

That makes it a better signal than tracking `SubagentStart`/`SubagentStop` pairs, which was the first design considered:

- It is **atomic with the `Stop`** — one payload snapshot, no cross-event bookkeeping, so hook reordering cannot confuse it. That matters: each hook is a separate `ape notify` process landing on its own goroutine, and `SubagentStop` is async while `Stop` is synchronous.
- It is **immune to a dropped `SubagentStop`**, which would otherwise leave a tracked set non-empty forever and wedge the run.
- It **sees teammates**. A teammate runs through a different code path in the harness and does not appear to emit `SubagentStart` at all — so a pair-tracking gate would have missed the very incident that prompted this work.

## The four gates

**Gate A — the turn boundary.** On `Stop`, classify `background_tasks`:

| Reported | Verdict | Why |
| --- | --- | --- |
| field absent | complete | An older Claude Code. Behave exactly as before this gate existed. |
| empty | complete | The overwhelmingly common case. |
| `teammate` | **fail now** | Provably unresolvable — see below. |
| `subagent`, `cloud session` | defer | Real work that may still report. |
| unrecognised type | defer | See "unknown types" below. |
| `shell`, `monitor`, `MCP task`, `dream`, `auto-mode scan`, `workflow` | ignore | Background work a skill may legitimately leave running. |

A deferred `Stop` withholds the completion signal and re-decides on the next `Stop`. The gate keeps no state across events.

**Gate B — the spawn.** On an Agent-tool `PostToolUse` whose `tool_response.status` is `teammate_spawned`, fail immediately. This overlaps Gate A's teammate branch on purpose: it fires seconds earlier and names the agent that caused it. Two independent observations of one condition, so losing one upstream costs redundancy rather than correctness.

**Gate C — the terminal contract.** Some skills end a run with a machine-readable return block. A run that exits without one never reached its summary step. `ape` does not know the vocabulary — the framework declares it per skill in `_apex/terminal-contracts.csv`:

```csv
skill,pattern
apex-story-batch-dev,^run_status:
apex-story-batch-review,^run_status:
apex-epic-batch-review,^epics:
```

Patterns are matched multi-line against the closing assistant message of the step's **own transcript**, not against the hook payload's `last_assistant_message`. That is deliberate: the transcript is a file `ape` must keep parsing correctly for cost telemetry regardless, so a check built on it survives the harness renaming or dropping a hook field.

`apex-epic-batch-review`'s contract is a different shape entirely, which is why this is a pattern table and not a hard-coded string.

**Gate C is warning-only in v0.0.52.** It depends on a dispatching agent relaying its sub-skill's contract block verbatim; an agent that paraphrases would turn a framework-side quality problem into a hard `ape`-side run failure. One release establishes the base rate first.

**Gate D — detecting the gates going silent.** If Claude Code renames `background_tasks`, nothing errors. Gate A just stops firing and `ape` quietly returns to reporting success on runs that did nothing. A gate that can stop firing unnoticed is worse than no gate, because it converts an absent protection into a believed-present one.

`ape doctor` sweeps the project's own `hook-events.jsonl` and reports whether the fields the gates depend on are still present in recent payloads. Present-but-empty is healthy; absent is drift. This mirrors [`ape costs coverage`](../reference/cli.md), which catches a model id changing under the price table for exactly the same reason — the change lands on the harness's schedule, under an already-released `ape` binary, where no release-time check can see it.

## Why teammates fail fast and background sub-agents do not

A teammate is spawned as an addressable agent that reports through a mailbox. The harness renders its tool result as *"The agent is now running and will receive instructions via mailbox."* The parent is never handed a tool result for it. Waiting cannot resolve that, no matter how long you wait — so the gate fails in milliseconds and says why.

A backgrounded sub-agent is the opposite: it is real, it is working, and it may legitimately run for hours. There is no short timer that can distinguish "still working" from "never coming back", and the two errors are not symmetric — killing a healthy sub-agent abandons real work and can leave half-written artifacts, while waiting too long only costs time. So that branch defers, bounded by the existing idle backstop, which the sub-agent's own hook traffic keeps alive while it works.

The rule that follows: **no path reaches the idle or max-duration backstop for a condition that is decidable at the `Stop`.** Those backstops stay reserved for a genuinely wedged session. A condition the harness has already told us about is never waited out.

## Unknown types defer rather than being ignored

The harness maps its internal task kinds through a display-name table before serialising them (`local_agent` → `subagent`, `in_process_teammate` → `teammate`). An unmapped kind falls through under its raw internal name — so a future agent-like task type would arrive as a string this code does not recognise.

Treating the unknown case as benign would silently re-open the hole these gates exist to close. Treating it as blocking costs, at worst, a deferred step bounded by the idle backstop, and Gate D surfaces it immediately. The asymmetry favours deferring.

## Compatibility

`ape` declares no minimum Claude Code version and does not branch on one. Every gate asserts **positively**: it fires only on evidence that the condition is present. An older harness that sends no `background_tasks`, or never spawns teammates, simply loses the new protection and behaves exactly as `ape` did before — which is why the regression lock for that case is a test, not a version check.

## Related

- [step-contract.md](../reference/step-contract.md) — the per-step prompt contract the runner enforces
- [bridge-architecture.md](bridge-architecture.md) — how hook events reach `ape` at all
- [exec-modes.md](exec-modes.md) — the per-stage interactive runtime
