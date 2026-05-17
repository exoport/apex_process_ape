# Continuation Prompt — Plan PLAN-5 (`ape chat` + `ape pipeline` web mode)

Use this prompt to resume work in a fresh session. The MCP-bridge design research is
**done**; this hand-off feeds a planning session that produces
`development/planning/plan-5_ape-chat-and-pipeline-web.md`.

---

## Where we are

- **MCP bridge PoC** (`/home/diegos/_dev/github/diegosz/claude_mcp_bridge_poc/`,
  commit `4e542d0`) is built and validated. Bidirectional Web UI ↔ Claude via two MCP
  tools (`await_message`, `reply`). Works on any plan (no channels, no special flags).
- **Design doc** `claude-mcp-bridge.md` carries firm decisions for every previously
  open question. **Read it first** — it is the source of truth.
- The earlier "in-Claude conductor skill" framing is **abandoned**. See the Context
  section of the design doc for the two reasons (sub-agent nesting limit, dissolved
  shared-context benefit under the 5-minute prompt-cache TTL). ape continues to spawn
  one `claude` invocation per pipeline step.
- Plans 1–4 are done. Next plan ID is **PLAN-5**.

---

## Decisions already made (do not re-litigate in PLAN-5)

| Topic               | Decision                                                                                                                                                                                                                                                                                   |
| ------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Orchestration model | ape stays the orchestrator. Per-step `claude` invocations (current model). No in-Claude conductor skill.                                                                                                                                                                                   |
| CLI surface         | `ape chat` (interactive bridge session) + `ape pipeline` (web bridge, **new default**) + `ape pipeline --tui` (today's TUI, opt-in) + `ape pipeline --print` (plain stdout).                                                                                                               |
| MCP config delivery | `--mcp-config '<inline-json>'` — no `.mcp.json` written to disk.                                                                                                                                                                                                                           |
| Settings delivery   | `--settings '<inline-json>'` — no `.claude/settings.json` written to disk.                                                                                                                                                                                                                 |
| Hooks merge policy  | **Default:** ape hooks + project hooks both fire (additive). **`--ignore-project-settings`** flag translates to `--setting-sources user --settings <inline>`.                                                                                                                              |
| Init mechanism      | `--system-prompt` + stdin `io.Pipe` bootstrap (PoC pattern) for `ape chat`. No bootstrap for `ape pipeline` steps (single skill per session).                                                                                                                                              |
| Timeout policy      | `await_message` default 240 s (under 5-minute prompt-cache TTL). No configurable knob.                                                                                                                                                                                                     |
| Bridge presence     | Env var `APE_BRIDGE_PORT` is the cheap signal; `tools/list` is the structural check. Skills degrade to stdout when absent.                                                                                                                                                                 |
| Web frontend        | **HTMX 2.x + stdlib `html/template` + handwritten `styles.css`.** No Templ, no Tailwind, no Alpine. Vendor HTMX core + SSE extension under `internal/web/assets/vendor/` (committed). `go install` is the whole build — no codegen, no JS toolchain. Locked by [ui-spike.md](ui-spike.md). |
| IPC transport       | Keep TCP + NDJSON. Defer NATS-embedded (+15 MB) and stdlib WebSocket until fan-out/remote is a real need.                                                                                                                                                                                  |
| Multi-project       | Per-project random port allocation. `~/.ape/registry.json` tracks live sessions cross-project. `ape sessions` subcommand inspects and prunes.                                                                                                                                              |
| Transcript          | `~/.claude/projects/<dir-hash>/<session-id>.jsonl` is authoritative. Per-step transcripts symlinked into `_output/ape/runs/<run-id>/transcripts/`.                                                                                                                                         |
| Run artefacts       | `_output/ape/runs/<run-id>/` (not `~/.ape/runs/`). User-level state (port registry, prices, plugin cache) stays in `~/.ape/`.                                                                                                                                                              |
| Cost tracking       | Parse per-message `usage` block from session JSONL. Roll up per stage / per run / per project. `ape costs` CLI + `/dashboard` page in the bridge UI.                                                                                                                                       |

Responsibility split:

- **Hooks** = passive observability (every tool call, sub-agent lifecycle).
- **`reply`** = semantic checkpoints (opt-in, skill-driven).
- **`await_message`** = decision gates (opt-in, skill-driven).
- **ape** = stage events, boundary commits, run-log assembly, cost rollups.

Do not collapse these.

---

## Your task

Write **`development/planning/plan-5_ape-chat-and-pipeline-web.md`** following the format
of `plan-4_per-step-boundary-commits.md` (YAML frontmatter with `plan_id`, `created_at`,
`status: draft`, `tags`, `summary`, `origin`; then `Goal`, `Why now`, `Scope — IN`,
`Scope — OUT`).

Before drafting, read in order:

1. `development/research/claude-mcp-bridge.md` — the bridge design with all conclusions.
   Pay special attention to §5 (pipeline integration — ape stays orchestrator), §6
   (frontend stack + spike requirement), §8 (hooks wiring via inline `--settings`),
   §10 (per-project ports), §11 (cost tracking).
2. `development/planning/plan-4_per-step-boundary-commits.md` — closest format reference.
3. `development/planning/plan-3_pipeline-run-manifest.md` — the manifest schema PLAN-5
   reuses (cost rollup attaches alongside it).
4. `internal/apecmd/pipeline.go` — current `ape pipeline` source. PLAN-5 adds new CLI
   modes and a web surface; the per-step execution loop largely stays.
5. `internal/tui/` — confirm the TUI lives behind a clean boundary so flipping it from
   default-on to opt-in (`--tui`) is a small change.

### Plan scope (must cover)

1. **CLI surface and default flip.**
   - Introduce `ape chat`. Single bridged interactive session, no pipeline.
   - `ape pipeline <name>` defaults to web-bridge mode.
   - `--tui` opt-in for the existing Bubble Tea TUI (today's default — **breaking UX
     change**, call it out explicitly in the plan).
   - `--print` opt-in for plain stdout (current `--no-tui` behaviour). Decide whether
     `--no-tui` is kept as an alias or removed.
   - `--ignore-project-settings` global flag → `--setting-sources user`.
2. **Inline config plumbing.**
   - Build the MCP server JSON and the hooks-settings JSON in memory; pass via
     `--mcp-config '<json>'` and `--settings '<json>'`. No file writes in cwd or tmp.
   - Code path: `internal/bridge/config.go` (or similar) holds the builders. Test that
     the JSON is `claude --help`-shaped (run a smoke test against real claude binary).
3. **Bridge runtime.**
   - Port the validated PoC (`/home/diegos/_dev/github/diegosz/claude_mcp_bridge_poc/`)
     into `internal/bridge/`. Sub-packages: `broker` (SSE), `mcp` (the `ape mcp-bridge`
     subcommand), `ipc` (TCP + NDJSON).
   - The 3 validated bugfixes from PoC commit `4e542d0` (SSE flush, stdin bootstrap,
     inline `--mcp-config`) must carry over.
4. **Hooks observability.**
   - `ape notify` subcommand: reads hook JSON on stdin, writes to bridge IPC port
     pulled from `APE_BRIDGE_PORT` env. Drops silently when env absent.
   - Bridge-side handlers route hook events to SSE.
   - Hook block delivered inline via `--settings`. Use `async: true`. Use the new
     `hookSpecificOutput.permissionDecision` schema for any `PreToolUse` gating.
5. **Multi-project port allocation.**
   - Random free-port allocation; URL printed to stdout (and optionally `xdg-open`'d).
   - `~/.ape/registry.json` write on start, best-effort cleanup on exit.
   - `ape sessions` subcommand: list / prune / open.
6. **Run artefacts under `_output/ape/runs/<id>/`.**
   - Layout per design doc §9. Per-step transcripts symlinked from
     `~/.claude/projects/<hash>/`. Hook events, bridge calls, checkpoints captured to
     JSONL. Manifest reused from PLAN-3 schema.
   - Decide whether `.gitignore` for `_output/` is auto-added by ape (with user
     confirmation) or documented as the user's responsibility.
7. **Cost tracking.**
   - Price table (`internal/cost/prices.go`) keyed by model name. Hand-curated; refresh
     via `ape costs update --from <file>` (no live API call).
   - Run-time tailing of per-step JSONL → `_output/ape/runs/<id>/cost.json`.
   - Project-level rollup → `_output/ape/cost-rollup.json`.
   - `ape costs` subcommand for terminal users.
8. **Web UI** (stack locked by [ui-spike.md](ui-spike.md); reference
   `/home/diegos/_dev/github/diegosz/claude_mcp_bridge_spike/variant-htmx/`
   for the working pattern).
   - `internal/web/` package: HTTP mux, SSE handler, per-connection view state
     (rolling per-stage hook buffer, last-seen counters, current-stage pointer).
   - Stdlib `html/template` fragments under `internal/web/templates/`: `page.tmpl`
     (one-time layout) + `fragments.tmpl` with per-event `define` blocks (`stage-card`,
     `hook`, `reply`, plus future `cost-ticker`, `decision-gate`).
   - HTMX 2.x core + SSE extension vendored under `internal/web/assets/vendor/`,
     committed. Handwritten `internal/web/assets/styles.css`, no Tailwind build step.
   - Client-only widgets via inline `onclick` calling helpers in a single `<script>`
     block at the page foot. Adopt Alpine only if widget interactivity grows past the
     point where inline handlers stay readable — defer that decision past PLAN-5.
   - SSE event names (`pipeline-init`, `stage-start`, `stage-update`, `stage-end`,
     `hook`, `reply`) are spike-validated and reusable verbatim; spec the wire schema
     in the plan and lock it.

### Plan scope (explicit OUT)

- The original in-Claude `apex-run-pipeline` conductor skill. Abandoned; document the
  reasoning briefly in `origin:`.
- Remote bridge operation (run `ape` here, view from elsewhere).
- NATS-embedded / WebSocket IPC migration.
- Channels protocol.
- Multiple bridge sessions within the same project (cross-project only).

### Style notes

- Match the existing plan tone: terse, decision-led, "Why now" before "How".
- Cite the PoC commit (`4e542d0`) where claims about validated bridge behaviour are
  load-bearing.
- Call out the **TUI default flip** as a breaking UX change explicitly. Mark it in
  the plan's frontmatter `tags:` with `breaking-default`.
- For every contract the skill / pipeline exposes (env vars, tool signatures, file
  paths), write the **exact contract**. Don't make Phase-2-of-Phase-2 interpret prose.
- Mark the plan `status: draft`; PLAN-5 will be reviewed before implementation.

---

## Context references

| Path                                                                                          | What                                                      |
| --------------------------------------------------------------------------------------------- | --------------------------------------------------------- |
| `/home/diegos/_dev/github/diegosz/apex_process_ape/`                                          | Production ape repo (this is where the plan goes)         |
| `/home/diegos/_dev/github/diegosz/apex_process_ape/development/planning/`                     | Existing plans 1–4 (done) — match their format            |
| `/home/diegos/_dev/github/diegosz/apex_process_ape/development/research/claude-mcp-bridge.md` | Bridge design — read in full                              |
| `/home/diegos/_dev/github/diegosz/claude_mcp_bridge_poc/` (commit `4e542d0`)                  | Validated PoC source — the bridge runtime port-target     |
| `https://code.claude.com/docs/en/hooks`                                                       | Hooks reference (canonical event list + JSON schema)      |
| `https://code.claude.com/docs/en/settings`                                                    | Settings precedence + `--setting-sources` semantics       |
| `https://docs.claude.com/en/docs/build-with-claude/prompt-caching`                            | Prompt-cache pricing + TTL behaviour                      |
| `https://htmx.org/extensions/sse/`                                                            | HTMX SSE extension docs                                   |
| `/home/diegos/_dev/github/diegosz/apex_process_ape/development/research/ui-spike.md`          | Frontend-stack verdict (htmx + stdlib template, no Templ) |
| `/home/diegos/_dev/github/diegosz/claude_mcp_bridge_spike/variant-htmx/`                      | Working reference implementation of the locked stack      |

---

## After PLAN-5 is written

- Update `development/planning/index.md` with the PLAN-5 row.
- Do **not** start implementation; PLAN-5 is a draft that needs a separate go-ahead.
- The **UI spike** is complete ([ui-spike.md](ui-spike.md)); the stack is locked, no
  further spike work blocks PLAN-5.
- If you discover the bridge design needs revision while drafting PLAN-5, edit
  `claude-mcp-bridge.md` and call it out at the top of PLAN-5's `origin:` block — do
  not silently diverge.
