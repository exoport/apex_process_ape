# Pipeline TUI keybinding reference

The `ape pipeline <name>` Bubble Tea TUI has three regions and a small set of keys for navigation, mode switching, and quit. This page is the authoritative reference. For prose orientation, see the [README's Pipeline TUI section](../../README.md#pipeline-tui).

The TUI (`--tui`, the default) renders the live output of the per-stage interactive `claude` REPL. Since v0.0.36 the PTY-hosted REPL is the only exec mode (the programmatic `claude -p` path was removed — see [why-pty-only.md](../explanation/why-pty-only.md)), so every `--tui` run behaves identically, including the await-message modal.

## Layout

```
┌─ apex-create-architecture · step-04 ─────────── stages ─────────────┐
│ 🔧 Read  development/planning/prd/index.md       │ ✓ create-prd 45s │
│ ✎  Drafting ADR table: 4 candidates              │ ✓ shard-prd   8s │
│ 🔧 Write _output/architecture-validation.yaml    │ ▸ create-arch 34 │
│ ↳  validation: 12/12 rules pass                  │   shard-arch   … │
│ ✓  apex-create-architecture · 3.5/5.0 pass       │                  │
├─ status ─────────────────────────────────────────┴──────────────────┤
│ create-arch · ▸ step 4/8 (apex-create-architecture) · 0:34 elapsed  │
└─────────────────────────────────────────────────────────────────────┘
 [mode: live] [style: human] [↑↓ stage] [Enter pin] [L live] [PgUp/PgDn scroll] [r style] [q quit]
```

- **Top-left (~70% width)** — live event feed for the cursor's stage. In Live mode auto-tails the latest event; any `PgUp` / `Home` suspends auto-tail until you page back to the bottom or press `End`. In Pinned mode renders the selected stage's full event log without auto-tail.
- **Top-right (~30% width)** — ordered stage list. Status glyph, stage name, elapsed time, cursor row marker `>`.
- **Bottom row** — cursor stage's running-step + elapsed + verdict.
- **Footer** — current mode + render style + keybind hint.

### Narrow-terminal layout (v0.0.8)

Below width 90, the right-side stage column drops and the stages collapse to a single horizontal stepper row above the event panel. The event panel takes the full width, and the cursor stage is wrapped in `[ ]` for visibility:

```
✓ design   ✓ governance   [▸ epics]   · final
┌─ apex-create-epics-and-stories ──────────────────────────────────┐
│ 🔧 Read  development/planning/architecture/index.md              │
│ ✎  Drafting epic 1 of 4                                          │
│ ...                                                              │
└──────────────────────────────────────────────────────────────────┘
```

Cursor / scroll / mode / quit semantics are unchanged — only the rendering layout differs. The layout swaps live on `SIGWINCH` (terminal resize).

## Modes

| Mode   | Default             | Behavior                                                                                                                                                                                                                   |
| ------ | ------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Live   | yes (while running) | Cursor auto-follows the active stage; new stages move the cursor automatically. Event panel auto-tails the latest event unless the user has manually scrolled (see _auto-tail suspension_ below).                          |
| Pinned | —                   | Cursor is pinned to the stage the user selected via `Enter`. Event panel renders that stage's full log starting at the tail. New events on the _active_ stage update its row in the right panel but don't move the cursor. |

`L` or `Esc` returns to Live mode (cursor snaps back to the running stage; auto-tail re-engages).

### Auto-tail suspension (v0.0.8)

Pre-v0.0.8 the `PgUp` / `PgDn` keys only worked in Pinned mode. As of v0.0.8 they work in either mode. The first `PgUp` (or `Home`) on a running stage seeds the scroll offset to the current tail window and suspends auto-tail — new events arrive silently in the background while the user reads older history. Auto-tail re-engages on either:

- `End` — explicit "rejoin the tail" key.
- `PgDn` paging back past the tail offset.
- `L` / `Esc` — returning to Live mode (also clears Pinned).

While auto-tail is suspended, the cursor and stage list still update normally; only the event panel viewport is held.

## Keys

### Stage navigation

| Key        | Action                                                                                                                                                             |
| ---------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `↑`, `k`   | Move cursor up the stage list. Clears `userScrolled` (rejoin the new stage's tail).                                                                                |
| `↓`, `j`   | Move cursor down the stage list. After completion, also reaches the synthetic `📊 final report` row at the bottom of the list (see _Completion phase_).            |
| `Enter`    | Pin the event panel to the cursor's stage (enter Pinned mode); seeds the scroll offset to the tail of that stage's events so the panel opens on the latest output. |
| `L`, `Esc` | Return to Live mode — cursor snaps to the running stage; auto-tail re-enables.                                                                                     |

Cursor wraps at neither end — `↑` at the top and `↓` at the bottom are no-ops.

### Event-panel scrolling

| Key    | Action                                                                                          |
| ------ | ----------------------------------------------------------------------------------------------- |
| `PgUp` | Scroll up one page (10 events). Suspends auto-tail in Live mode.                                |
| `PgDn` | Scroll down one page (10 events). Paging back to the tail re-engages auto-tail.                 |
| `Home` | Jump to the top of the cursor stage's event log. Suspends auto-tail.                            |
| `End`  | Jump to the tail of the cursor stage's event log. Re-engages auto-tail (clears `userScrolled`). |

### Render style

| Key      | Action                                                                                                                                          |
| -------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| `r`, `R` | Cycle event-row render style: `human` → `raw` → `both` → `human`. Active style appears in the footer (e.g. `[style: raw]`). Affects all stages. |

The three styles render a single event differently:

- **`human`** (default) — parsed glyph + summary: `🔧 Read development/planning/prd/index.md`.
- **`raw`** — original NDJSON line: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read",...}]}}`.
- **`both`** — human row, then a dim raw row beneath it (two-line entry per event). Useful when you're trying to correlate the renderer's parse against the wire format.

### Quit

| Key                        | Action                                                                                                                       |
| -------------------------- | ---------------------------------------------------------------------------------------------------------------------------- |
| `q`, `Ctrl+C`              | Open the quit-confirmation modal.                                                                                            |
| `y`, `Y` (in modal)        | Confirm: cancel the runner context, SIGTERM the `claude` process group then SIGKILL after a 500ms grace, exit (PLAN-2 / F1). |
| `n`, `N`, `Esc` (in modal) | Dismiss the modal.                                                                                                           |
| `Ctrl+C` × 2 within 1s     | Force-quit (bypass the modal). Emergency escape for when the modal itself stalls.                                            |

In the completion phase (see below) `q` and `Ctrl+C` quit directly — there's no confirmation modal, because nothing is left to cancel.

### Await-message reply modal

Some skills (`apex-create-story`, parts of `lift-project`) park an `await_message` MCP tool call mid-step to ask the user a clarifying question. The bridge surfaces this as a modal overlay with a text input.

| Key      | Action                                                                                                                |
| -------- | --------------------------------------------------------------------------------------------------------------------- |
| `Enter`  | Submit the reply. The bridge round-trips the content back to the parked tool call; the modal closes.                  |
| `Esc`    | Clear the input but keep the modal open (the user can either type something else or wait for the bridge to time out). |
| `Ctrl+C` | Falls through to the quit-confirmation modal — double-Ctrl+C still force-quits even when the await modal is up.       |

The modal only opens when a skill actually parks an `await_message` call; runs whose skills never ask for input never surface it.

## Completion phase (v0.0.8)

When the pipeline finishes (last stage's `OnStageEnd` fires), the TUI no longer auto-quits. Instead:

- The keybind-hint footer is replaced by a completion banner: `✓ pipeline complete: N/N stages OK` or `✗ pipeline failed: M/N FAILED`.
- A synthetic `📊 final report` row appends to the stage list. The cursor moves to it automatically; selecting it populates the event panel with a per-stage summary table (status glyph · name · duration · event count · last error if any).
- `↑↓` still navigates among stages + the report row. `PgUp` / `PgDn` / `Enter` / `r` all keep working so the user can inspect per-stage history before quitting.
- `q` (or `Ctrl+C`) exits with the pipeline's exit code.

## Event glyphs

The event-feed lines come from the [stream-json event renderer](../../internal/tui/event_renderer.go). Each prefix maps to a single category:

| Glyph | Kind              | Meaning                                                                                                                                                  |
| ----- | ----------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `✎`   | assistant text    | The model's prose ("Drafting the ADR table"). First line, truncated 80c.                                                                                 |
| `🔧`  | tool use          | `Read`, `Edit`, `Write`, `Bash`, `Grep`, `Glob`, `Task`, `WebFetch`, `WebSearch`, or an unknown MCP tool. Each variant has its own short summary format. |
| `↳`   | tool result       | A non-trivial successful tool output (Bash stdout head, WebFetch summary). Trivial successes (`File created successfully…`) are suppressed.              |
| `↳ ⚠` | tool result error | `is_error=true` on a tool_result block. Red.                                                                                                             |
| `✓`   | skill complete    | The top-level `result` event with success status, with turn count.                                                                                       |
| `✗`   | skill failed      | Top-level `result` event with `is_error=true` or `subtype=error`.                                                                                        |
| `·`   | system event      | `system.init` (session start) and other system pings. Dim.                                                                                               |
| `?`   | unknown           | Schema-drift fallback: the line parsed as JSON but didn't match any known shape, OR didn't parse at all. The raw text is preserved as the body.          |

## `--no-tui` mode

`ape pipeline <name> --no-tui` (auto-enabled on non-TTY) emits the same human-friendly events to stdout, one line per event, prefixed with timestamp + stage + skill:

```
[20:08:42] design · apex-create-architecture · 🔧 Read development/planning/prd/index.md
[20:08:43] design · apex-create-architecture · ✎ Drafting ADR table: 4 candidates
[20:08:51] design · apex-create-architecture · 🔧 Edit development/planning/architecture.md
[20:09:18] design · apex-create-architecture · ✓ skill complete (3 turns)
```

Suppressed event types (trivial tool_results, etc.) are dropped in `--no-tui` too — output stays scannable.

### `--quiet` (v0.0.8)

`ape pipeline <name> --no-tui --quiet` suppresses the per-event stream entirely. Only stage / step start+end markers and failure summaries print, matching the pre-PLAN-1 / I4b shape — useful in CI runs where thousands of event lines would overflow log scrollback. Combining `--quiet` with the interactive TUI is refused with an actionable error (the TUI panels aren't affected by the flag).

## Related

- [How to run a pipeline](../how-to/framework-setup.md) — covers the first-install flow that precedes any `ape pipeline` run.
- [Pipeline spec reference](pipeline-spec.md) — the YAML shape that produces the stages shown in the right panel.
- [Why streaming events](../explanation/why-streaming-events.md) — design rationale.
