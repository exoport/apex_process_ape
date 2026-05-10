# Pipeline TUI keybinding reference

The `ape pipeline <name>` Bubble Tea TUI has three regions and a small set of keys for navigation, mode switching, and quit. This page is the authoritative reference. For prose orientation, see the [README's Pipeline TUI section](../../README.md#pipeline-tui).

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
 [mode: live] [↑↓ stage] [Enter pin] [L live] [PgUp/PgDn scroll] [q quit]
```

- **Top-left (~70% width)** — live event feed for the cursor's stage. In Live mode auto-follows the active stage and auto-scrolls; in Pinned mode renders the selected stage's full event log with PgUp/PgDn.
- **Top-right (~30% width)** — ordered stage list. Status glyph, stage name, elapsed time, cursor row marker `>`.
- **Bottom row** — cursor stage's running-step + elapsed + verdict.
- **Footer** — current mode + keybind hint.

Minimum sane terminal: 90 cols × 16 rows. Below that the layout degrades gracefully but text may wrap.

## Modes

| Mode   | Default             | Behavior                                                                                                                                                                                                                                                      |
| ------ | ------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Live   | yes (while running) | Cursor auto-follows the active stage. Event panel auto-scrolls so the latest event is always visible. New stages move the cursor automatically.                                                                                                               |
| Pinned | —                   | Cursor is pinned to whatever stage the user selected via `Enter`. Event panel renders that stage's full log; `PgUp` / `PgDn` / `Home` / `End` scroll within it. New events on the _active_ stage update its row in the right panel but don't move the cursor. |

`L` or `Esc` returns to Live mode (cursor snaps back to the running stage).

## Keys

### Stage navigation

| Key        | Action                                                                           |
| ---------- | -------------------------------------------------------------------------------- |
| `↑`, `k`   | Move cursor up the stage list.                                                   |
| `↓`, `j`   | Move cursor down the stage list.                                                 |
| `Enter`    | Pin the event panel to the cursor's stage (enter Pinned mode).                   |
| `L`, `Esc` | Return to Live mode — cursor snaps to the running stage; auto-scroll re-enables. |

Cursor wraps at neither end — `↑` at the top and `↓` at the bottom are no-ops.

### Event-panel scrolling (Pinned mode only)

| Key    | Action                                        |
| ------ | --------------------------------------------- |
| `PgUp` | Scroll up 5 events.                           |
| `PgDn` | Scroll down 5 events.                         |
| `Home` | Jump to the first event of the pinned stage.  |
| `End`  | Jump to the latest event of the pinned stage. |

In Live mode all four are no-ops — auto-scroll keeps the latest visible.

### Quit

| Key                        | Action                                                                               |
| -------------------------- | ------------------------------------------------------------------------------------ |
| `q`, `Ctrl+C`              | Open the quit-confirmation modal.                                                    |
| `y`, `Y` (in modal)        | Confirm: cancel the runner context, SIGKILL the in-flight `claude` subprocess, exit. |
| `n`, `N`, `Esc` (in modal) | Dismiss the modal.                                                                   |
| `Ctrl+C` × 2 within 1s     | Force-quit (bypass the modal). Emergency escape for when the modal itself stalls.    |

When the pipeline has already finished, any key dismisses the screen without showing the modal — there's nothing to cancel.

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

## Related

- [How to run a pipeline](../how-to/framework-setup.md) — covers the first-install flow that precedes any `ape pipeline` run.
- [Pipeline spec reference](pipeline-spec.md) — the YAML shape that produces the stages shown in the right panel.
- [Why streaming events](../explanation/why-streaming-events.md) — design rationale.
