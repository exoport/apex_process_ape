# Continuation Prompt — resume PLAN-6 interactive-mode debugging

> **RESOLVED — 2026-05-20.** The debugging effort below was superseded by
> a design pivot from PTY + `--system-prompt` + MCP `await_message`
> prompt delivery to **tmux send-keys** for real REPL keystroke
> delivery. See the "Implementation pivot — tmux send-keys" section of
> `development/planning/plan-6_interactive-exec-and-orthogonal-modes.md`
> for the root cause analysis and shipped design. Sandbox acceptance
> confirmed against the greeter project on 2026-05-20:
> `ape pipeline design` (tui), `--web`, and `--no-tui` all complete
> end-to-end. Commits: `e1584b2` (the pivot) + `3adf420` (defer-order
> hang fix). This doc is preserved as a record of the failed attempts
> and the diagnostic path that led to the pivot; do not act on its
> "What to actually do first" section — that work is done.

Use this prompt to pick up after a `/clear`. PLAN-6 implementation is
**code-complete** but interactive mode (`ape pipeline <name>` default,
`--tui`, `--no-tui`, `ape chat`) is **not working end-to-end** in the
sandbox. The bridge connects but the first step never completes; the
run aborts within ~1 second with `context canceled`. This doc captures
exactly where we are, what's been tried, and the next moves.

This repo is `/home/diegos/_dev/github/diegosz/apex_process_ape`.

The user (Diego) was explicit: **do not use `claude -p`**. The whole
point of PLAN-6 interactive mode is one long-lived `claude` process
per stage driven by the bridge's `await_message`/`reply` MCP loop —
NOT per-step `claude -p` spawns. PLAN-5 web mode still uses `-p`
(that's the `--web -P` cell of the matrix) and works fine; do not
regress it.

---

## State, in one paragraph

PLAN-6 shipped Phase A→H code: YAML schema (model/agent/commit at
pipeline/stage levels), `BridgeRuntime` factored out of `Hub`, the
interactive per-stage runner, `ContractVerifier` for `UserPromptSubmit`
hooks, apecmd wiring (`pipeline_interactive.go`,
`pipeline_interactive_tui.go`, `--web` with `interactive` flag),
invocation matrix flip (default = tui+interactive), `ape chat`
brought back as a thin TUI, Diataxis docs. Full test suite passes
(`go test ./...`). Web programmatic mode (`--web -P`) still works.
**Interactive mode does not.** The symptom: `pty.Start(claude)` succeeds,
claude paints its REPL banner, the bridge subprocess connects + sends
`TypeReady` within ~830ms, BUT the first step's prompt sent via
`rt.SendMessage` never gets a `Stop` hook back; ~150ms after the step
starts, the run aborts with `wait done: context canceled`.

---

## Reading order

1. **`development/planning/plan-6_interactive-exec-and-orthogonal-modes.md`** —
   the plan. Read § "Why now" + § C3 (interactive runtime) + § C4 (step
   contract). The C4 contract has been **simplified during the refactor**:
   `/clear` and `/model X` rules dropped (those are CLI-level slash
   commands, not deliverable via MCP). Only agent-prefix verification
   remains.
2. **`internal/pipeline/interactive.go`** — `runStageInteractive`. Note
   the `pty.Start` + `"begin\r"` bootstrap + `WaitBridgeReady` wait.
3. **`internal/apecmd/pipeline_interactive.go`** — `interactiveCore`
   - `waitBridgeReady` (now waits for `RuntimeEventAwaitPending`, not
     just `TypeReady`).
4. **`internal/bridge/orchestrator/contract.go`** — simplified verifier
   (agent-prefix only).
5. **Past commits referenced via git log** — the implementation arc
   has not been committed yet. All work is uncommitted in the working
   tree on `main`. The user has not asked for a commit pass yet.

---

## Sandbox

```
/home/diegos/_dev/ape-web-sandbox/
├── .bin/ape                       ← built binary (rebuild after every code change)
└── greeter/                       ← project root
    ├── _apex/                     ← config
    ├── development/planning/      ← product brief
    └── _output/                   ← run artefacts (manifests + runlog)
```

**Sandbox clean state:** `git -C /home/diegos/_dev/ape-web-sandbox/greeter reset --hard 3676580`.

**Build command:**

```bash
cd /home/diegos/_dev/github/diegosz/apex_process_ape
go build -o /home/diegos/_dev/ape-web-sandbox/.bin/ape ./cmd/ape
```

**Test invocations:**

```bash
pkill -f "claude" 2>/dev/null   # always start clean

# The failing case — interactive mode (any UI variant):
APE_INTERACTIVE_DEBUG=1 /home/diegos/_dev/ape-web-sandbox/.bin/ape pipeline design --no-tui > /tmp/ape.stdout 2> /tmp/ape.stderr
APE_INTERACTIVE_DEBUG=1 /home/diegos/_dev/ape-web-sandbox/.bin/ape pipeline design       > /tmp/ape.stdout 2> /tmp/ape.stderr
APE_INTERACTIVE_DEBUG=1 /home/diegos/_dev/ape-web-sandbox/.bin/ape pipeline design --web > /tmp/ape.stdout 2> /tmp/ape.stderr
APE_INTERACTIVE_DEBUG=1 /home/diegos/_dev/ape-web-sandbox/.bin/ape chat                  > /tmp/ape.stdout 2> /tmp/ape.stderr

# The known-working cases (regression smoke):
/home/diegos/_dev/ape-web-sandbox/.bin/ape pipeline design --web -P    # PLAN-5 web shape, works
/home/diegos/_dev/ape-web-sandbox/.bin/ape pipeline design --print     # locked path, must stay byte-equivalent
```

`APE_INTERACTIVE_DEBUG=1` prints every line claude writes to its PTY as
`[claude/pty] <line>` on stderr. Essential for diagnosis.

---

## What's been tried and observed

In order:

### Attempt 1 — `claude` REPL via plain stdin pipe

Spawn claude without `-p`, write `/clear`, `/model X`, then the
agent-prefixed prompt to stdin. **Did not work.** Symptom: claude
process alive but produces zero output; bridge subprocess never
spawns; 30s `WaitBridgeReady` timeout. Cause: claude's REPL requires
a TTY; with a piped (non-tty) stdin it sits idle and never
initializes its MCP servers.

### Attempt 2 — `claude -p <bootstrap>` + bridge

Spawn `claude -p "begin" --output-format stream-json --verbose
--system-prompt "..."`. The system prompt tells the model to loop on
`await_message`/`reply`. **User rejected** this approach: "the only
thing that we could not use is 'clade -p', the whole point of the
bridge is to avoid that". REVERTED.

### Attempt 3 — PTY (creack/pty) with `--system-prompt`

`pty.Start(cmd)` gives claude a real TTY. Bootstrap `"begin\r"`
written to the PTY master 500ms after spawn. `configurePTYCancel`
helper replaces `configureProcessGroup` for the PTY path (Setpgid +
Setsid combo on the same cmd is rejected as EPERM at fork/exec —
`pty.Start` already sets `Setsid: true` which gives the same
session-leader semantics).

**Current state.** PTY allocation succeeds; claude paints its full
v2.1.145 REPL banner. Bridge ready fires at ~830ms (confirmed by
`waitBridgeReady` returning nil). First step starts, sends prompt via
`rt.SendMessage`, then within ~150ms the run aborts with `wait done:
context canceled`. Nothing visible in the `[claude/pty]` stream
between banner and abort.

### Latest changes (uncommitted, may or may not help)

- **`waitBridgeReady` now waits for `RuntimeEventAwaitPending`** (not
  just `BridgeReady` channel close). Rationale: the model only enters
  the `await_message` park after processing the bootstrap turn; the
  first `SendMessage` must wait for that, otherwise the prompt may
  queue at the bridge and race against the verifier's `BeginStep`.
- **`interactiveCore.FeedHook` filters bootstrap `"begin"` from the
  verifier.** Rationale: `"begin"` arrives as a `UserPromptSubmit`
  hook; if that hook races `BeginStep`, the verifier sees `"begin"`
  instead of the expected agent prefix and fires a spurious violation
  (which cancels `runCtx` and produces the `context canceled` symptom).

Neither has been tested yet (the user asked me to write this resume
doc instead).

---

## The most likely root cause (hypothesis)

The verifier `OnViolation` callback is firing somewhere we don't see
(stderr-buried) and cancels `runCtx`. Evidence:

- The symptom is `context canceled` exactly when `WaitStepDone`'s
  `ctx.Done()` channel fires.
- The only thing that cancels `runCtx` from within the run is
  `verifier.OnViolation` (via `core.runCancel`) — `rt.SetStopFn` is
  also bound to it but nothing in non-web mode calls `RequestStop`.
- The bootstrap `"begin"` produces a `UserPromptSubmit` hook with
  payload `{"prompt":"begin"}`. Without filtering, the verifier sees
  this and matches it against the active step's expected prefix
  (which is `/apex-agent-pm --autonomous -- apex-create-prd
--autonomous`). No match → violation.
- Stderr in the user's terminal got interleaved with PTY noise and
  the `❌ assertion:step-contract` line may have been buried.

**First diagnostic to run after rebuild:** capture stderr to a file
and `grep -E "assertion:step-contract|violation"` it.

```bash
APE_INTERACTIVE_DEBUG=1 /home/diegos/_dev/ape-web-sandbox/.bin/ape pipeline design --no-tui 2> /tmp/ape.stderr
grep -E "assertion|violation|context canceled|bridge" /tmp/ape.stderr | head
```

If the violation message appears, the bootstrap filter from the
latest edit should fix it. If not, the cause is elsewhere (and we
need to actually look at what cancels `runCtx`).

---

## Where the code lives

| Path                                          | What                                                                                                                                  |
| --------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/pipeline/interactive.go`            | `runStageInteractive` — PTY spawn, bootstrap, chain loop, `WaitStepDone` wait                                                         |
| `internal/pipeline/proc_unix.go`              | `configureProcessGroup` (Setpgid path for programmatic) + `configurePTYCancel` (Setsid path for PTY)                                  |
| `internal/apecmd/pipeline_interactive.go`     | `interactiveCore`, `waitBridgeReady` (waits for `AwaitPending`), `isBootstrapPrompt` filter, `runWithInteractive` (no-UI variant)     |
| `internal/apecmd/pipeline_interactive_tui.go` | `runWithInteractiveTUI` — wires the Bubble Tea panels                                                                                 |
| `internal/apecmd/pipeline_web.go`             | `runWithWeb(..., interactive bool)` — composes `Hub` + `interactiveCore` when `interactive=true`                                      |
| `internal/apecmd/chat.go`                     | `runChat` — `ape chat`, same PTY shape as pipeline interactive                                                                        |
| `internal/bridge/orchestrator/runtime.go`     | `BridgeRuntime` (IPC accept, stop, event channel, dispatch). `RuntimeEventAwaitPending` / `Resolved` emitted on `await_message` calls |
| `internal/bridge/orchestrator/contract.go`    | `ContractVerifier` — simplified to agent-prefix verification only                                                                     |
| `internal/tui/interactive.go`                 | `InteractiveModel` — pipeline TUI with hooks panel + await modal                                                                      |
| `internal/tui/chat.go`                        | `ChatModel` — `ape chat` TUI                                                                                                          |
| `internal/bridge/config/settings.go`          | `BuildSettings` — `InjectHooks: true` opts non-web modes into the hooks block (PLAN-6 / Phase E addition)                             |

---

## Invariants — do not regress

1. **`--print` byte-equivalent with PLAN-5.** Eval consumer at
   `/home/diegos/_dev/exoar/apex_process_framework_eval` reads it.
   `runPlain` is the only path it takes; bridges/PTYs/system-prompts
   are all forbidden in this branch.
2. **`--web -P` matches PLAN-5 web behaviour verbatim.** The `interactive`
   parameter to `runWithWeb` defaults to false; when false, the function
   behaves exactly as PLAN-5.
3. **No `claude -p` in interactive paths.** Hard rule from the user.
4. **Broker (HTTP/SSE) is web-only.** TUI and no-UI must not start an
   HTTP listener.
5. **No `Co-Authored-By: Claude` trailer on commits** (project memory
   `feedback_no_claude_attribution.md`).
6. **Prettier-format every markdown edit:**
   `npx prettier --write "<file>" --log-level silent`.

---

## What to actually do first

The user explicitly said "you need to test it". Concrete steps:

1. **Rebuild** with the latest uncommitted changes:

   ```bash
   cd /home/diegos/_dev/github/diegosz/apex_process_ape
   go build -o /home/diegos/_dev/ape-web-sandbox/.bin/ape ./cmd/ape
   ```

2. **Run the failing case with stderr captured:**

   ```bash
   pkill -f "claude" 2>/dev/null
   cd /home/diegos/_dev/ape-web-sandbox/greeter
   git reset --hard 3676580
   APE_INTERACTIVE_DEBUG=1 /home/diegos/_dev/ape-web-sandbox/.bin/ape pipeline design --no-tui > /tmp/ape.stdout 2> /tmp/ape.stderr
   echo "exit=$?"
   ```

3. **Inspect stderr for the verifier violation:**

   ```bash
   grep -E "assertion:step-contract|violation|context canceled|bridge not ready" /tmp/ape.stderr
   ```

4. **If the violation message appears:** the bootstrap filter (latest
   edit) should be the fix; but the user's latest run was BEFORE that
   edit landed in a build. Confirm by re-running. If it appears again
   after the rebuild, the filter isn't matching — debug the JSON
   payload shape.

5. **If no violation message:** the cancel source is something other
   than `OnViolation`. Add `fmt.Fprintf(os.Stderr, "runCancel called
from <site>\n")` at each call site of `runCancel` /
   `core.runCancel` and re-run to identify the actual canceller.

6. **Sandbox smoke matrix** (once interactive works for `--no-tui`):
   - `ape pipeline design` (TUI variant)
   - `ape pipeline design --web` (web + interactive)
   - `ape pipeline design --web -P` (PLAN-5 regression — must still work)
   - `ape pipeline design --print` (locked path — must be byte-equivalent)
   - `ape chat` (single bridged session)

---

## Open questions for the user (only if blocked)

- Is there a known-good claude argv shape from PLAN-5 ape chat that
  we could pull from the OLD commit history? PLAN-5 session.go was
  deleted in this branch's Phase G; `git show <pre-PLAN-6-tip>
internal/bridge/orchestrator/session.go` would surface it.
- Did the PLAN-5 ape chat actually work end-to-end in real use, or
  was it cargo-cult code that nobody tested? The sandbox PID-93535
  leftover from before our build was suggestive but not conclusive.

---

## Context references

| Path                                                                   | What                                                    |
| ---------------------------------------------------------------------- | ------------------------------------------------------- |
| `development/planning/plan-6_interactive-exec-and-orthogonal-modes.md` | PLAN-6 plan (status: approved, 2026-05-19)              |
| `development/research/resume-plan-6-kickoff.md`                        | The pre-implementation kickoff doc (now superseded)     |
| `development/research/resume-plan-5-post-launch.md`                    | PLAN-5 resume doc — sandbox layout reference            |
| `development/research/claude-mcp-bridge.md`                            | Bridge architecture                                     |
| `docs/explanation/exec-modes.md`                                       | PLAN-6's explanation of why interactive vs programmatic |
| `docs/reference/invocation-matrix.md`                                  | The full UI × Exec table                                |
| `docs/reference/step-contract.md`                                      | The simplified contract (agent-prefix only)             |
| `/home/diegos/_dev/ape-web-sandbox/greeter/`                           | Sandbox; clean state `git reset --hard 3676580`         |
| `/home/diegos/_dev/exoar/apex_process_framework_eval/`                 | Eval consumer; `--print` byte-equivalence must hold     |
| Project memory `feedback_no_claude_attribution.md`                     | No `Co-Authored-By: Claude` trailer on commits          |
