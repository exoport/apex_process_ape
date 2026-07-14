---
plan_id: PLAN-19
created_at: 2026-07-14
implemented_at: 2026-07-14
status: done
implementation_notes: Shipped on the `dev` branch (unreleased; CHANGELOG `## Unreleased`). All of D1–D6 landed. D5 is the load-bearing change — the smart `WaitStepDone` (progress anchor + poll cadence + hard cap + diagnostic) now lives once in `internal/sessiondriver` and `interactiveCore` (pipeline/task) delegates to it, finishing the extraction PLAN-12 left shallow. D1 anchors on hooks + transcript size/mtime + transcript-dir mtime (the `/clear`-rotation fallback) and, on the `ape prompt` path only, a PTY-output probe; the pipeline/task path does not install the PTY probe (transcript growth carries the anchor there). D2/D6: `--max-duration` default 3h (`0` disables), 30s→60s poll cadence at the 60m threshold. D3: `--idle-timeout` now wired on `ape pipeline` (was an unused runConfig field), `--max-duration` on pipeline/task/prompt; apescript runners resolve a zero MaxDuration to the 3h default via `resolveMaxDuration`. D4: structured `IdleTimeoutError`/`MaxDurationError` diagnostics naming the tripped limit, per-source ages, and child liveness. Deferred (per Non-goals): no `/proc`/CPU sampling for the pure-silent long tool — `--idle-timeout`/`--max-duration` are the operator's lever. Docs (Step 5) delivered: cli.md regenerated, pipeline-spec "Step completion backstop" section, `docs/how-to/tune-long-running-steps.md`, `chat-task-prompt.md` reconciled, CHANGELOG entry. Follow-up (2026-07-14): the child-liveness probe is now wired on the pipeline/task path too — `InteractiveStepInfo.SessionName` carries the stage's PTY session name and `interactiveCore.OnStepStart` installs `Driver.SetChildAliveProbe`, so their termination diagnostics report `child pid N alive|exited` instead of "child liveness unknown". Diagnostic-only (never a keep-alive signal). The PTY-output anchor remains `ape prompt`-only by design: transcript growth carries the pipeline anchor, and a raw-PTY anchor would risk masking a real stall behind the REPL's cosmetic repaints.
tags:
  - pipeline-runner
  - pty
  - timeout
  - reliability
  - docs
summary: Replace the hook-only idle backstop that terminates interactive steps with an activity-aware one. Today a step is killed when no bridge hook event fires for interactiveStepIdleTimeout (60m, hardcoded) — but the timer is anchored ONLY to hook events, so a step doing a long silent operation (one multi-hour tool call, long model reasoning, huge-context processing) emits no hook for >60m and is cancelled mid-work even though it is actively progressing. Fix in four parts: (D1) reset the idle anchor on real progress signals — the session transcript growing, and optionally PTY output bytes — in addition to hooks; (D2) add a hard wall-clock ceiling flag (--max-duration, default 3h) as the absolute per-step stop, and relax the poll cadence from every 30s to every 60s once a step passes 60m of runtime; (D3) expose --idle-timeout on `ape pipeline` (the field exists in runConfig but was never wired to a flag, so pipelines are stuck at 60m with no knob); (D4) emit a clear diagnostic on termination (idle vs max-duration; child alive?; which progress source last advanced and how long ago) instead of the current silent cancel. Implement the smart WaitStepDone once in internal/sessiondriver and have interactiveCore delegate to it, collapsing the duplicate idle loop introduced by PLAN-12.
origin:
  - 2026-07-14 user report — on a large project, several `ape pipeline` steps were cancelled by the 60-minute backstop because the steps legitimately run longer than an hour. Request — monitor completion more intelligently than a brute timeout (check activity / files changing, re-check periodically, with a hard cap around 3h) rather than a fixed 60m.
  - 2026-07-14 code audit — the 60m is an IDLE window (interactiveStepIdleTimeout, internal/apecmd/pipeline_interactive.go:148), not wall-clock, but WaitStepDone resets its anchor (lastActivity) ONLY in FeedHook (pipeline_interactive.go:224-229). `ape task`/`ape prompt` expose --idle-timeout; `ape pipeline` does not (runConfig.idleTimeout exists at pipeline.go:291 but no cmd.Flags() registration). The identical hook-only loop is duplicated in internal/sessiondriver/driver.go WaitStepDone (the PLAN-12 `ape prompt` path).
  - 2026-07-14 user refinements — default `--max-duration` to **3h** (the hard cap ships ON, not off); and after the first 60m of a step, relax the `WaitStepDone` poll from every 30s to every 60s (tight early stall-detection, cheap polling on long runs) up to the 3h cap.
  - Assumptions marked inline were made at authoring time; flag at review.
---

# PLAN-19: Activity-aware step completion — progress-anchored idle + hard cap

## Goal

An interactive step (pipeline stage step, `ape task`, `ape prompt`) is
terminated by the runner only when it has genuinely stopped making progress,
not merely because the bridge emitted no hook for a fixed window. A step that
is actively working — writing its transcript, streaming to the PTY — is never
cancelled, no matter how long a single operation takes, up to an explicit
hard wall-clock ceiling the user can set.

## Why now

A concrete production incident: on a large project, legitimate pipeline steps
that run over an hour were killed by the 60m backstop. The current mechanism
cannot tell "silently working for 70 minutes" from "hung for 70 minutes",
because it only watches one signal (hooks) and only offers pipelines a fixed
60m with no override.

## Current mechanism (what we are changing)

- `interactiveCore.WaitStepDone` (`internal/apecmd/pipeline_interactive.go`)
  polls every `interactiveStepIdlePoll` (30s) and returns an error —
  cancelling the step — when `time.Since(lastActivity) > idleTimeout`.
- `lastActivity` is set to now **only** in `FeedHook` — i.e. only
  `PreToolUse`/`PostToolUse`/`UserPromptSubmit`/`Stop`/`Subagent*` reset it.
  A stretch with no hook (a long single `Bash` tool call, long reasoning
  between tool calls) is indistinguishable from a stall.
- `idleTimeout` defaults to `interactiveStepIdleTimeout = 60 * time.Minute`.
  `ape task`/`ape prompt` expose `--idle-timeout`; **`ape pipeline` does
  not** (the `runConfig.idleTimeout` field is present but no flag sets it).
- The same hook-only loop is **duplicated** in
  `internal/sessiondriver/driver.go` `WaitStepDone` (the `ape prompt` path
  from PLAN-12), which shares only the telemetry scan with `interactiveCore`,
  not the wait loop.

## Non-goals

- Not changing the default idle window value (stays 60m); this plan changes
  what *counts as activity* and adds an explicit hard cap, not the default.
- No per-tool or per-skill timeouts.
- No process-CPU / `/proc` sampling in v1 (see D1's edge case + Risks) — it is
  the only thing that catches a truly silent long tool (no transcript, no PTY
  output), but it is heavier and non-portable. Noted as a stretch tier, not
  planned for v1.
- No queue/scheduler changes; this is purely the completion-detection backstop.

## Design

### D1: Progress-anchored idle window

Reset the idle anchor on any of these, not just hooks:

1. **Hook event** — unchanged (`FeedHook`).
2. **Transcript growth** — the active claude session transcript
   (`activeTranscript`, already tracked from the UserPromptSubmit hook in
   `interactiveCore`/`Driver`) advancing in size or mtime since the last
   poll. This directly reflects model + tool progress (assistant text,
   `tool_use`, `tool_result`) and is the cheapest robust signal. The poll
   loop already runs every 30s; it stat()s the transcript there.
   - Handle `/clear`-driven session rotation: the transcript path can change
     mid-step (NoClear=false rotates the session id). Anchor on the current
     `activeTranscript` and, when it is empty/rotating, on the mtime of the
     transcript **directory** so a rotation counts as activity rather than a
     gap. (Assumption: dir-mtime is sufficient; validate in step 2.)
3. **PTY output bytes** (optional, second signal) — the `internal/repl`
   session already receives PTY output; expose a monotonic "bytes seen" or
   "last output at" so streaming with no transcript flush still counts as
   activity. (Assumption: a small accessor on the repl session; confirm the
   VT-grid layer exposes a byte/lastwrite hook without racing the reader.)

Any of {hook, transcript-grew, pty-output} within the window keeps the step
alive. Only genuine silence across **all** signals for `idleTimeout` trips it.

### D2: Hard wall-clock ceiling — `--max-duration`

A new per-step absolute cap, independent of activity: `--max-duration <dur>`,
**default `3h`** — the cap ships ON (set `0` to disable, or raise it for
exceptionally long steps). `WaitStepDone` records the step start and returns a
distinct `max-duration exceeded` termination when
`time.Since(start) > maxDuration`, regardless of progress. This bounds a
genuinely stuck-but-noisy step that D1's richer anchor would otherwise keep
alive forever, and makes the worst-case per-step wall-clock predictable (3h)
instead of unbounded.

### D3: Close the `ape pipeline` flag gap

Register `--idle-timeout` on `ape pipeline` (wire `runConfig.idleTimeout` to a
`cmd.Flags().DurationVar`, mirroring `ape task`/`ape prompt`). Register
`--max-duration` on all three (`pipeline`, `task`, `prompt`). This gives
immediate operator control (the reporter could have set `--idle-timeout 3h`
today if the flag existed). Ship D3 as the first, standalone PR — it is a
one-liner that relieves the incident before the smarter logic lands.

### D4: Diagnostic on termination (no more silent kill)

Replace the bare `"interactive step idle for %v without Stop hook"` error with
a structured diagnostic: which limit tripped (idle vs max-duration), whether
the child claude process is still alive, and the last-advanced progress source
+ its age (e.g. "idle 60m0s: no hook / transcript / PTY output; child pid
12345 alive; last transcript write 61m ago → stopping"). Mirrors PLAN-11's
"include the last pane snapshot on timeout" philosophy so the operator can
tell a real stall from a mis-tuned window at a glance.

### D5: Consolidate the wait loop into `sessiondriver`

Move the (now smarter) `WaitStepDone` implementation into
`internal/sessiondriver` as the single owner of the activity anchor + poll +
cap, and have `interactiveCore.WaitStepDone` delegate to it. This finishes the
extraction PLAN-12 left shallow (`interactiveCore` currently shares only
`ScanStep` with the `Driver` and keeps its own idle loop), so the smart logic
is implemented and tested **once**, not twice. Pipeline behavior must be
byte-identical; existing pipeline/task tests stay green (they may need the
timeout-related fields threaded through, but no behavioral change).

### D6: Poll cadence — tight early, relaxed for long runs

`WaitStepDone` polls at `interactiveStepIdlePoll` (30s) for the first 60m of a
step, then relaxes to 60s for the remainder, up to `--max-duration`. Rationale:
30s keeps early-stall detection responsive; once a step is clearly long-lived
the transcript/PTY progress signals change slowly at that scale, so 60s polling
is sufficient and halves the stat()/poll overhead across multi-hour steps. The
existing `idlePollDivisor` scaling for small `--idle-timeout` windows is
unchanged (a short configured window still polls at a quarter of the window,
which is already ≤ 30s).

## Steps

1. **PR-1 (immediate relief):** D3 — `--idle-timeout` on `ape pipeline` +
   `--max-duration` (default `3h`) on pipeline/task/prompt. Flag plumbing +
   help text + reference-doc regen. No idle-anchor behavior change yet, but the
   3h ceiling is live.
2. **PR-2:** D5 refactor — lift `WaitStepDone` into `sessiondriver` as the
   single implementation; `interactiveCore` delegates. Pure refactor, all
   existing tests green.
3. **PR-3:** D1 — transcript-growth anchor (+ optional PTY-output anchor).
   Tests: a fake transcript file that grows on a timer past the idle window
   is NOT terminated; a flat transcript with no hooks IS terminated at the
   window; `/clear` rotation counts as activity.
4. **PR-4:** D2 + D4 + D6 — hard `--max-duration` termination (default 3h),
   the phase-based poll cadence (30s for the first 60m, then 60s), and the
   structured diagnostic. Tests: a step that keeps "progressing" past
   `--max-duration` is terminated with the max-duration reason; the idle path
   reports the idle reason with the progress-source ages; the poll-interval
   selector returns 30s before 60m and 60s after (unit-level, not wall-clock).
5. **Docs (required — this plan ships user-visible flags + a behavior change,
   so docs are a deliverable, not an afterthought):**
   - Regenerate `docs/reference/cli.md` (`go run ./cmd/ape gen-docs --out
     docs/reference/cli.md`) so the new `--idle-timeout` (pipeline) and
     `--max-duration` (pipeline/task/prompt) flags and their help text appear.
   - Update the pipeline/task reference docs (e.g. `docs/reference/pipeline-spec.md`
     and any per-command reference) to describe the completion backstop: it is
     an *idle* window anchored on real progress (hooks + transcript growth +
     PTY output), the 30s→60s poll cadence, and the default `3h` hard cap.
   - Add/extend a how-to for long-running steps (e.g.
     `docs/how-to/tune-long-running-steps.md`): "if a step legitimately runs
     > 1h, the idle window now tracks transcript/PTY progress (not just hooks),
     so active steps are no longer cancelled at 60m; raise `--idle-timeout` for
     pathologically silent tools and adjust the `--max-duration` ceiling
     (default 3h, `0` to disable)."
   - Cross-check `docs/explanation/chat-task-prompt.md` (the sibling-commands
     doc) — update the completion/backstop wording if it references the old
     hook-only 60m behavior.
   - CHANGELOG entry under `## Unreleased`.

## Acceptance

- `ape pipeline --idle-timeout 3h` is honored (regression: the flag exists and
  overrides the 60m default).
- A step whose transcript advances every < idle-window minutes runs past 60m
  without cancellation; a step silent across all signals for the window is
  still cancelled — with a diagnostic naming the idle reason and the last
  progress source + age.
- `--max-duration 90m` terminates a still-"active" step at ~90m with the
  max-duration reason; with no flag, the default `3h` ceiling applies (and
  `--max-duration 0` disables the cap). The poll-interval selector returns 30s
  for the first 60m and 60s thereafter.
- `ape prompt` / `ape task` behave identically (shared `sessiondriver`
  implementation); existing pipeline/task tests unchanged and green.
- **Docs updated:** `docs/reference/cli.md` regenerated with the new flags; the
  timeout/backstop behavior documented in the pipeline/task reference + a
  long-running-steps how-to; `chat-task-prompt.md` reconciled; CHANGELOG entry
  present. (Docs are part of "done" for this plan.)

## Risks / notes

- **Transcript path rotation** (`/clear` between/within steps) is the main
  correctness trap for the growth signal; anchor on the live `activeTranscript`
  and fall back to transcript-dir mtime during rotation.
- **PTY-output accessor** must not race the `internal/repl` reader goroutine;
  keep it a lock-guarded timestamp/counter, or drop the PTY signal if the
  transcript signal proves sufficient in step 3 (transcript alone likely
  covers the reported incident).
- **The pure-silent long tool** (a multi-hour `Bash` with zero output and no
  transcript writes until it returns) is not caught by D1 — only a
  process-liveness/CPU tier would distinguish it from a hang. Out of scope for
  v1; `--idle-timeout` + `--max-duration` are the operator's lever there.
- **D5 touches the highest-traffic runner path.** Mitigation: land it as the
  pure refactor PR-2 (behavior identical, tests green) before any
  timer-behavior change rides on it.
