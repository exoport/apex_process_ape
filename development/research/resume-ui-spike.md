# Continuation Prompt — UI Spike PoC

Use this prompt to resume work in a fresh session. Goal: build the **frontend-stack
selection spike** for `ape chat` / `ape pipeline` web mode. Output is a comparison doc
that locks the frontend choice before PLAN-5 implementation starts.

---

## Why this spike exists

`claude-mcp-bridge.md` §6 proposes a **GOAT-hybrid** stack (Templ + HTMX 2.x + Tailwind +
Alpine.js sprinkles) but explicitly requires a spike before locking it. The spike builds
the same screen three ways and compares LOC, diff-per-change ergonomics, and build-step
weight. Expected outcome: confirm the hybrid. Spike is allowed to redirect the choice if
the evidence says otherwise.

Read first:

1. `development/research/claude-mcp-bridge.md` — full design context. §6 (frontend), §9
   (transcript / events), §10 (ports), §11 (cost tracking). Skim everything else for the
   event model.
2. `/home/diegos/_dev/github/diegosz/claude_mcp_bridge_poc/serve.go` and `ui.go` — the
   validated PoC's vanilla-JS frontend. Same SSE broker pattern; the spike reuses it.

---

## What to build

Three sibling directories under `development/spikes/ui/`, each a fully runnable Go
program serving the same screen via different frontend stacks. Each program is
self-contained (own `main.go`, own `go.mod` or shared workspace — your call), bound to
its own port:

```
development/spikes/ui/
├── README.md             # comparison rubric + how to run all three
├── shared/               # mock-data generator (used by all variants)
│   └── pipeline.go       # emits a deterministic 5-stage mock pipeline over ~30 s
├── variant-vanilla/      # vanilla JS + EventSource + fetch + manual DOM
│   ├── main.go
│   ├── assets/index.html
│   └── assets/app.js
├── variant-htmx/         # HTMX 2.x + stdlib html/template, no Templ, no Alpine
│   ├── main.go
│   └── templates/*.tmpl
└── variant-hybrid/       # Templ + HTMX + Tailwind + Alpine sprinkles (GOAT-hybrid)
    ├── main.go
    ├── components/*.templ
    ├── components/*_templ.go    # generated, committed
    ├── styles/input.css         # Tailwind source
    └── assets/styles.css        # Tailwind build output, committed
```

### The screen — identical across all three variants

One page at `GET /`. Three regions:

1. **Stage list (top)** — one card per pipeline stage. Card shows:
   - Stage name (e.g. `apex-create-prd`)
   - Status glyph: ⏳ pending · ▸ running · ✓ done · ✗ failed
   - Duration in seconds (live-updating while running)
   - Last hook event line (truncated to 80 chars)
2. **Hook stream (middle)** — append-only list of `PostToolUse` events. Each line:
   `[timestamp] tool_name — input-summary`. New lines append at the bottom.
3. **Reply log (bottom)** — append-only list of `reply` messages, styled as chat bubbles.

Below the page, two **Alpine widgets** in the hybrid variant (and equivalent manual
implementations in the other two):

- A `<details>`-driven collapsible panel under each stage card that, when open, shows
  the last 20 hook events for that stage. Closed by default.
- A "copy command" button on each stage card that copies `ape pipeline resume <stage>`
  to the clipboard, with a brief "copied!" toast.

### The mock pipeline (shared/pipeline.go)

Deterministic 30-second timeline driven by a single `time.Ticker`:

| t (s) | Event           | Payload                                            |
| ----- | --------------- | -------------------------------------------------- |
| 0     | `pipeline-init` | 5 stages, all pending: prd, ux, arch, epics, story |
| 1     | `stage-start`   | `apex-create-prd`                                  |
| 1–5   | `hook` x 8      | `PostToolUse` Read/Write/Edit/Bash, mixed          |
| 4     | `reply`         | "Drafted PRD outline (4 sections)"                 |
| 5     | `stage-end` (✓) | `apex-create-prd`, duration 4 s                    |
| 6     | `stage-start`   | `apex-create-ux-design`                            |
| 6–11  | `hook` x 10     | various                                            |
| 10    | `reply`         | "UX design draft committed"                        |
| 11    | `stage-end` (✓) | `apex-create-ux-design`                            |
| 12    | `stage-start`   | `apex-create-architecture`                         |
| 12–22 | `hook` x 15     | various                                            |
| 22    | `stage-end` (✓) | `apex-create-architecture`                         |
| 23    | `stage-start`   | `apex-create-epics-and-stories`                    |
| 23–26 | `hook` x 5      | various                                            |
| 26    | `stage-end` (✓) | `apex-create-epics-and-stories`                    |
| 27    | `stage-start`   | `apex-create-story`                                |
| 27–29 | `hook` x 4      | various                                            |
| 29    | `stage-end` (✗) | `apex-create-story`, error: "schema mismatch"      |

The same generator drives all three variants (each `main.go` imports `shared/pipeline.go`).
Reset on each new SSE connection so reloading restarts the timeline.

### SSE event format

Named events (`event: <name>`), JSON payloads (`data: {…}`). The variants differ in
how they render the payload, not in the payload itself:

```
event: pipeline-init
data: {"stages":[{"name":"apex-create-prd","status":"pending"}, …]}

event: stage-start
data: {"name":"apex-create-prd","started_at":"2026-05-17T10:00:01Z"}

event: stage-update
data: {"name":"apex-create-prd","duration_s":3,"last_hook":"PostToolUse Read prd.md"}

event: stage-end
data: {"name":"apex-create-prd","status":"done","duration_s":4}

event: hook
data: {"stage":"apex-create-prd","tool":"Read","summary":"prd.md","ts":"…"}

event: reply
data: {"stage":"apex-create-prd","text":"Drafted PRD outline (4 sections)"}
```

(For the HTMX variant: server emits **pre-rendered HTML fragments** on these same event
names. Same generator, different render. The vanilla and Templ variants emit JSON.)

---

## Comparison rubric (what to measure)

Capture into `development/spikes/ui/README.md` and `development/research/ui-spike.md`
(the final verdict doc) for each variant:

1. **LOC**, separately:
   - Server-side (Go)
   - Frontend templates/components
   - Frontend JS (and any CSS not produced by Tailwind)
   - Generated files (committed): `_templ.go`, Tailwind `styles.css`
2. **Build steps required to ship.** `go install` only, or also `templ generate`,
   `tailwindcss build`, etc.
3. **Diff per change** — make one realistic change in all three and record the patch
   size. The change: "Add a per-stage cost figure (USD) next to the duration."
4. **SSE reconnect behaviour** — kill the server for 2 seconds, restart, observe each
   variant's recovery. Vanilla and HTMX should auto-reconnect via `EventSource`; record
   visible artefacts (gap markers, double-renders).
5. **Bundle weight in the browser** — gzipped sum of all JS + CSS shipped to the client.
6. **Subjective ergonomics** — 2–3 sentences on what was painful in each.

Write the verdict in `development/research/ui-spike.md`:

```yaml
---
spike_id: ui-spike-1
date: 2026-05-17
status: complete
decision: <chosen-variant>
---
```

Body: rubric table, one-paragraph rationale per variant, final pick with the strongest
2–3 reasons. If the verdict disagrees with `claude-mcp-bridge.md` §6's expected hybrid,
update the design doc and call it out in the verdict.

---

## Time budget

2–3 hours total. If a variant blows the budget (e.g. fighting Tailwind config), cap it
and document the failure mode — that itself is a data point.

---

## Concrete starting steps

1. `mkdir -p development/spikes/ui/{shared,variant-vanilla,variant-htmx,variant-hybrid}`
2. Write `shared/pipeline.go` first — the generator is the contract. ~100 LOC.
3. Build `variant-vanilla` second (simplest). It validates the generator end-to-end.
4. Build `variant-htmx` third. Reuse the generator; render Go templates per event.
5. Build `variant-hybrid` last. Install templ + tailwind locally:
   ```bash
   go install github.com/a-h/templ/cmd/templ@latest
   # Tailwind: prefer the standalone CLI to avoid npm
   curl -sLO https://github.com/tailwindlabs/tailwindcss/releases/latest/download/tailwindcss-linux-x64
   chmod +x tailwindcss-linux-x64 && sudo mv tailwindcss-linux-x64 /usr/local/bin/tailwindcss
   ```
   Generated `_templ.go` and `assets/styles.css` are committed.
6. Add a top-level `Makefile` target `make spike` that builds all three and prints their
   URLs. Each variant binds a different port (8801 / 8802 / 8803) so they can run
   concurrently for side-by-side comparison.

---

## Out of scope for this spike

- The real MCP bridge — the spike uses mock data only.
- Authentication, multi-session, port registry (those are PLAN-5).
- Cost tracking UI (PLAN-5+).
- The chat-bridge use case — this spike only exercises the pipeline-progress screen.
- Wiring the spike into the ape CLI — keep it standalone under `development/spikes/`.

---

## After the spike

- Commit the spike code + verdict doc together. Suggested commit message:
  `spike(ui): pipeline-progress screen in 3 frontend stacks (vanilla/htmx/hybrid)`
- Update `development/research/claude-mcp-bridge.md` §6 with the locked decision if it
  differs from the expected hybrid.
- Update `development/research/resume-mcp-bridge-plan.md` decisions table — replace the
  "spike first" caveat with the locked stack — so PLAN-5 can start without re-reading
  the spike.
- The next session writes PLAN-5 from `resume-mcp-bridge-plan.md` (now unblocked).

---

## Context references

| Path                                                                                          | What                                                       |
| --------------------------------------------------------------------------------------------- | ---------------------------------------------------------- |
| `/home/diegos/_dev/github/diegosz/apex_process_ape/`                                          | This repo (spike lives under `development/spikes/ui/`)     |
| `/home/diegos/_dev/github/diegosz/apex_process_ape/development/research/claude-mcp-bridge.md` | Design doc; §6 is the spike's contract                     |
| `/home/diegos/_dev/github/diegosz/claude_mcp_bridge_poc/serve.go`                             | Validated SSE broker — reuse the pattern in each variant   |
| `/home/diegos/_dev/github/diegosz/claude_mcp_bridge_poc/ui.go`                                | Validated vanilla-JS frontend — `variant-vanilla` baseline |
| `https://templ.guide/`                                                                        | Templ syntax + codegen reference                           |
| `https://htmx.org/extensions/sse/`                                                            | HTMX SSE extension (`hx-ext="sse"`, `sse-swap`)            |
| `https://alpinejs.dev/`                                                                       | Alpine.js docs                                             |
| `https://tailwindcss.com/blog/standalone-cli`                                                 | Tailwind standalone CLI (no npm)                           |
