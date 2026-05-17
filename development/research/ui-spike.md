---
spike_id: ui-spike-1
date: 2026-05-17
status: complete
decision: htmx-only (HTMX 2.x + stdlib html/template + handwritten CSS)
contradicts: claude-mcp-bridge.md §6 (expected GOAT-hybrid)
---

# UI-stack selection spike — verdict

Three runnable variants of the pipeline-progress screen, identical mock
pipeline, compared along LOC, build steps, diff-per-change, bundle weight, and
ergonomics. Code + run instructions live in the standalone sibling repo
`/home/diegos/_dev/github/diegosz/claude_mcp_bridge_spike/`.

## Result

**Pick htmx-only.** Specifically: **HTMX 2.x + stdlib `html/template` +
handwritten CSS**, with inline `onclick` helpers when client-only interactivity
is needed. **Drop Templ and Alpine for now**; revisit if the UI surface grows
past ~3 screens or the component-count makes typed templates pay off.

This contradicts the expected pick in
[`claude-mcp-bridge.md`](claude-mcp-bridge.md) §6, which proposed the GOAT-hybrid
(Templ + HTMX + Tailwind + Alpine). The design doc must be updated accordingly
— see [§update to design doc](#update-to-design-doc) below.

## Rubric

All numbers from the committed spike code. Diff-per-change row applies one
canonical edit ("add a per-stage cost figure in USD next to the duration") to
each variant on top of the spike baseline. Bundle weight is gzipped JS+CSS
shipped to the browser.

| Metric                              | vanilla                                                                                                                                    | **htmx**                                               | hybrid                                        |
| ----------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------ | --------------------------------------------- |
| Hand-written Go (LOC)               | 57                                                                                                                                         | 242                                                    | 193                                           |
| Hand-written templates (LOC)        | —                                                                                                                                          | 102                                                    | 231                                           |
| Hand-written frontend JS (LOC)      | 159                                                                                                                                        | ~10                                                    | ~0                                            |
| Hand-written CSS (LOC)              | 88                                                                                                                                         | 88                                                     | 4 (Tailwind input)                            |
| Generated, committed (LOC + bytes)  | 0                                                                                                                                          | 0                                                      | 645 LOC `_templ.go` + 10.6 KB CSS             |
| Build steps                         | `go build`                                                                                                                                 | `go build`                                             | `templ generate` → `tailwindcss` → `go build` |
| External tool deps                  | 0                                                                                                                                          | 0                                                      | `templ` + `tailwindcss` binaries              |
| Diff per change — hand-written      | +8 / −1                                                                                                                                    | **+5 / −0**                                            | +18 / −0 (incl. ~12 LOC fmt helpers)          |
| Diff per change — regenerated, auto | 0                                                                                                                                          | 0                                                      | +66 / −30 `_templ.go`, ±1 CSS                 |
| Gzipped browser bundle              | **~2.5 KB**                                                                                                                                | ~19.5 KB                                               | ~37 KB                                        |
| Binary size                         | 8.9 MB                                                                                                                                     | 12.3 MB                                                | 9.4 MB                                        |
| SSE reconnect after server bounce   | OK (EventSource native auto-reconnect; `#hooks` accumulates duplicates on reload — easy to fix server-side by clearing on `pipeline-init`) | OK (HTMX SSE ext wraps `EventSource` — same behaviour) | OK (same)                                     |

## Rationale per variant

**vanilla.** Genuinely smallest bundle (~2.5 KB gz), smallest binary, zero
tools. The cost is duplicated state: the server already maintains the
last-20-hooks rolling buffer (verified in the htmx variant), and reproducing
that buffer in JS for the collapsible widget rebuilds work that already
exists. JS state plus typed JSON payloads also crosses a refactor boundary
every time a field is renamed. Tolerable for one screen; expensive when the
chat UI, pipeline dashboard, and cost view share state.

**htmx.** Server is the single source of truth for view state; the wire
ships opaque HTML, so a refactor on the Go side never has to be mirrored on
the JS side. OOB swaps (`hx-swap-oob="beforeend:#hooks"`) handle the
append-only regions cleanly. The two pain points are real but minor: stdlib
`html/template` define-blocks are syntactically clunky, and the inline
`onclick` for the copy button is the kind of glue Alpine would otherwise own
— but five lines of JS is not the moment to take on a framework dependency.
HTMX core at ~17 KB gz is a meaningful one-time cost; everything past that
is markup, not code.

**hybrid.** Type-safe components are real value at scale; Tailwind utility
classes are a real ergonomic win for fast UI iteration. But for `ape pipeline`
/ `ape chat` web mode at PoC-to-MVP stage, the ceremony cost is concrete and
the payoff is speculative:

- two extra binaries on the contributor's path (`templ`, `tailwindcss`),
- a regenerate step before every component edit can compile,
- ~17 KB more gzipped bundle for Alpine alone,
- ~12 LOC of `fmtCost`/`pad2` helpers needed because templ template syntax
  has no `printf` (a real surprise the spike surfaced),
- 645 LOC of committed generated Go that has to be reviewed in PRs and that
  diff-noises on every component touch.

The GOAT-hybrid is the right answer for a frontend with a component library
and a team of UI engineers iterating in parallel. It is the wrong answer for
a CLI tool's web mode whose total screen count is "dashboard + chat" and
whose codebase needs to stay legible to Go contributors who are not frontend
specialists.

## Decision

**Stack:** HTMX 2.x core + HTMX SSE extension + stdlib `net/http` + stdlib
`html/template` + a single handwritten `styles.css`. Vendor HTMX core + SSE
extension under `internal/web/assets/vendor/` (committed). No npm, no
build-time codegen, no JS framework.

**Client-only interactivity:** inline `onclick` handlers calling small helper
functions in a single `<script>` block, or — if the JS grows past ~50 LOC —
extract to a vendored `app.js`. Adopt Alpine only when interactive widgets
multiply past what inline handlers can carry cleanly.

**Migration path if this proves wrong:** swapping `html/template` for Templ
later is a mechanical, fragment-by-fragment refactor (both produce HTML
fragments; the OOB swap markers are identical). Adding Alpine later is also
opt-in per widget. We are not locking ourselves out of the GOAT-hybrid by
starting with htmx-only.

## Out-of-scope confirmations

- The mock SSE schema (`pipeline-init`, `stage-start`, `stage-update`,
  `stage-end`, `hook`, `reply`) survives unchanged into PLAN-5 — it was
  legible to all three variants and to the rolling-buffer hook display.
- The "reset on new connection" rule worked correctly across all three
  variants. EventSource auto-reconnect after a server bounce restarts the
  pipeline from t=0; appending lists (`#hooks`, `#replies`) show duplicates
  on reload but that is trivial to fix server-side by re-emitting a `clear`
  event on connect — defer to PLAN-5.
- The 30 s deterministic timeline is enough to exercise stage transitions,
  hook fan-out, replies, the failure terminal, and the running-duration
  counter. No additional fixture work needed for the screen contract.

## Update to design doc

[`claude-mcp-bridge.md`](claude-mcp-bridge.md) §6 must be updated to reflect:

- Drop "Templ" from the stack.
- Drop "Alpine.js sprinkles" from the stack.
- Drop Tailwind 4 + the `tailwindcss` binary requirement.
- Replace with: HTMX 2.x core + HTMX SSE extension + stdlib `html/template`
  - handwritten `styles.css`.
- Strike the "GOAT-hybrid" label; the working name for the stack is just
  "htmx + html/template".
- Add a one-paragraph reference to this spike file as the rationale for the
  redirect from the doc's original expectation.

[`resume-mcp-bridge-plan.md`](resume-mcp-bridge-plan.md) decisions table must
have its "spike first" caveat replaced with the locked stack above so PLAN-5
can start without re-reading the spike.
