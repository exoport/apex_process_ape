---
plan_id: PLAN-10
created_at: 2026-07-02
updated_at: 2026-07-12
status: done
shipped_v0_0_36: D5 rollup/Bucket PerModel + the sumTotals NumTurns bug fix + ape costs MODEL column (human + json) + the ape costs run/chat subcommands (were advertised-but-unregistered). Still deferred after v0.0.36 — D1 per-turn TurnRecord + H6 (requestId/stop_reason) dedup (speculative; would perturb v0.0.35-validated telemetry) and D3 date-aware pricing (needs D1's per-turn timestamps; the standard-rate table is the blessed conservative fallback). Update 2026-07-08: the 5m/1h split later shipped in v0.0.37 (PLAN-10 D1), and the D6 discrepancy closeout is RESOLVED (development/research/cost-discrepancy-20260521.md).
implementation_status: D2 (sub-agent discovery) + D4 (transcript copy) + the per-model manifest/envelope fields (`model_usage`, `sessions[]`, `totals.num_turns`) shipped additively under `schema_version: 2` in v0.0.27–v0.0.35 (see PLAN-11 v0.0.35 errata). Corrected 2026-07-03 against the actual eval contract — NO v3 schema bump (the eval's `apex_eval/ape_manifest.py` hard-rejects any `schema_version` outside `[1,2]` while tolerating unknown fields, so additive-under-v2 is the only eval-safe path); `StepRecord.ModelObserved` was dropped from the shipped shape (the 5m/1h cache split was later re-added in v0.0.37). Nothing open — the last loose end, the optional `cost.NewTailer` live-file poller (zero production callers), was deleted on the branch (v0.0.42); `AssistantLine` was preserved (relocated to `internal/cost/line.go`) since `scanTurns` still parses it. Shipped since — D1 per-turn `TurnRecord` + H6 (requestId/stop_reason) dedup, D2 `SessionFiles` extraction, and D3 date-aware pricing (Sonnet-5 intro window + `[1m]` normalization) all landed on the branch (v0.0.42); earlier: D5 rollup `PerModel`, the `sumTotals` NumTurns fix, the `ape costs` MODEL column, and `ape costs run`/`chat` (all v0.0.36) + the 5m/1h split (v0.0.37); D6 discrepancy closeout **DONE** (`development/research/cost-discrepancy-20260521.md`, resolved 2026-07-08: the 5m/1h split + confirmed prices/multipliers + the v0.0.35 double-count fix close it).
tags:
  - cost
  - telemetry
  - transcripts
  - subagents
  - billing-accuracy
summary: Rework `internal/cost` from single-aggregate `Totals` to per-turn records carrying timestamp, model, requestId, stop_reason, and the full cache 5m/1h split; dedupe the way ccusage does (message.id + requestId, prefer stop_reason entries); discover and scan subagent transcripts (`<session-id>/subagents/agent-*.jsonl`); copy transcripts into the run dir instead of symlinking; refresh the price table (Fable 5/Mythos 5, Opus 4.8, date-aware Sonnet 5 intro pricing) and normalize `[1m]` model suffixes; add per-model blocks to the manifest (additively, under `schema_version: 2` — a v3 bump would break the eval reader) and `ape costs`; and wire the dead plumbing (chat cost recording, incremental rollup folds). This is the prerequisite for resolving development/research/cost-discrepancy-20260521.md and for any cost number becoming load-bearing.
origin:
  - development/research/cost-discrepancy-20260521.md — open since 2026-05-21; its "recommended path" step 4 (per-model breakdown) is this plan.
  - 2026-07-02 research: Anthropic's current pricing page confirms ape's Opus 4.7 rates ($5/$25) and cache multipliers (1.25×/2.0×/0.1×) exactly — falsifying discrepancy hypotheses H5 and the multiplier variant of H1. No 1M-context surcharge exists on current models (the `[1m]` premium was legacy Sonnet-4-era). Community parsers (ccusage) dedupe by message.id AND requestId and trust only entries with stop_reason — ape dedupes by message.id only (new hypothesis H6: streaming-fragment double count). Subagents write separate files `~/.claude/projects/<proj>/<session-id>/subagents/agent-<id>.jsonl` (`isSidechain: true`) that ape never scans; 236 such files exist on this machine.
  - 2026-07-02 review found the dead plumbing: `cost.NewTailer`, `cost.ScanLatestSession`, `cost.FoldPipelineRun/FoldChat/SaveRollup`, `runlog.WriteSessionYAML` have zero production callers; `ape chat` records no cost; non-web runs never refresh the rollup.
---

# PLAN-10: Telemetry v2 — per-model cost, timestamps, subagents

## Goal

Every ape-driven claude session produces an accurate, per-model,
per-timestamp usage record that includes subagent turns, survives
`~/.claude/projects` rotation, and reconciles against Anthropic's published
pricing. `ape costs` and the manifest expose the model dimension. The cost
discrepancy investigation gets the data it needs to close.

## Why now

- PLAN-9 removes stream-json `total_cost_usd` — after it lands, transcript
  scanning is the *only* cost source, so it must be right.
- PLAN-13 (blob upload) uploads "the transcripts of a run" — which requires
  first knowing how to enumerate them (main + subagents). Discovery logic
  lands here.
- The user explicitly wants "tokens per model … including subagents,
  including the timestamp, so we are able to measure the cost in Claude Code
  API prices."

## Non-goals

- No OTEL exporter (Claude Code's own OTEL surface exists but requires env
  configuration of the claude process; transcript scanning stays the
  mechanism — revisit if it proves insufficient).
- No billing-dashboard integration; the plan produces the numbers to compare
  against a dashboard manually (discrepancy doc step 3).
- No NATS/blob work (PLAN-13), no new commands.
- **No eval-side reader change.** ape already emits `model_usage` /
  `sessions[]` on the `ape task --output-format json` envelope, but the eval's
  `_synthesize_task_transcript` (`apex_eval/runner.py`) reads only the aggregate
  `usage` + a single `model` and explicitly degrades per-sub-agent attribution
  to the task model. Teaching the eval to consume `sessions[]` for true
  per-sub-agent model attribution is an **eval-repo** change, out of scope here
  — ape emitting the fields is necessary but not sufficient for that.

## Design

### D1: Scan output shape (`internal/cost`)

```go
type TurnRecord struct {
    Timestamp  time.Time // entry `timestamp` (ISO 8601)
    Model      string    // normalized (see D3)
    SessionID  string    // owning session (main or subagent file's session)
    MessageID  string
    RequestID  string
    StopReason string
    Sidechain  bool      // isSidechain, or file came from subagents/
    AgentID    string    // subagent id when applicable
    Usage      UsageBlock // keeps Ephemeral5m / Ephemeral1h split
    CostUSD    float64
}

type ScanResult struct {
    Turns    []TurnRecord
    PerModel map[string]Totals // Totals gains CacheCreation5m/1h fields
    Totals   Totals
}
```

`ScanSessionJSONL` returns `ScanResult`. Old callers use `.Totals`; new
callers get the model/time dimensions. **Dedup (H6 hardening):** group by
`(message.id)`, and within a `requestId` prefer the entry carrying
`stop_reason` (final snapshot); ignore `type != assistant` and `isMeta`
entries as today.

### D2: Session-set discovery

`SessionFiles(mainPath string) []SessionFile` — given the main transcript
`…/<sid>.jsonl`, also glob `…/<sid>/subagents/agent-*.jsonl` (and tolerate the
older inline-`isSidechain` layout, which D1 already handles). Each
`SessionFile` carries its **agent id** (parsed from the `agent-<id>.jsonl`
filename) as its distinct identifier — NOT a session id: a sub-agent
transcript's internal `sessionId` equals its parent's, so agent_id is the
only thing that distinguishes subs. Used by: per-step telemetry (subagent
turns attributed to the step whose session they belong to), end-of-run
transcript copy (D4), PLAN-13 upload, and PLAN-17's standalone `ape metrics`
/ `ape transcript` commands. Note the known upstream gap: subagent files
carry no parent-session pointer (anthropics/claude-code#32175) — our mapping
is by directory containment, which is exactly what the layout gives us.

**Reconciliation with the shipped interactive path (v0.0.35).** The
per-step interactive runner already implements sub-agent discovery, and it
proved directory-globbing alone is the *fallback*, not the primary source:
the `SubagentStop` hook carries the sub's own transcript directly as
`agent_transcript_path` (its `transcript_path` on that envelope is the
PARENT — folding it was the v0.0.34 2×-main double-count bug). So the
shipped order is: (1) capture `agent_transcript_path` from `SubagentStop`,
keyed by `agent_id`, folded once each; (2) a robustness sweep of
`…/<sid>/subagents/agent-*.jsonl` (mtime-scoped to the step) recovers any
sub a dropped hook would have lost; (3) a guard never folds a sub whose
resolved path equals the main transcript. `SessionFiles` here (the batch
discovery for copy/upload/metrics) is the same glob as the sweep and should
share the agent-id-from-filename helper. See PLAN-11's v0.0.35 errata.

### D3: Prices + model normalization

- `NormalizeModel`: strip `[1m]` suffix (confirmed: no surcharge on current
  models — same per-token rate), lowercase, trim.
- Table refresh (`prices.go`, header re-dated): add
  `claude-fable-5`/`claude-mythos-5` ($10/$50), `claude-opus-4-8` ($5/$25),
  `claude-sonnet-5` — **date-aware**: $2/$10 through 2026-08-31, $3/$15
  after. Implement as an optional `EffectiveFrom` on price entries; `Lookup`
  takes the turn timestamp (D1 provides it — this is a concrete payoff of
  per-turn timestamps).
- Keep `~/.ape/prices.yaml` overrides; extend override schema with the same
  optional dating.

### D4: Transcript capture — copy, not just symlink

At run finalize (pipeline, chat, and later command/task), copy every file
from D2's session set into `<run-dir>/transcripts/` (existing symlinks kept
during the run for live tailing; replaced by real files at the end).
Programmatic-mode absence of transcripts disappears with PLAN-9. Copies are
what PLAN-13 uploads and what survives `~/.claude/projects` rotation — the
discrepancy doc already lost one dataset to exactly that rotation.

### D5: Per-model manifest (additive, stays v2) + rollup + `ape costs`

- **Per-model manifest — additive under `schema_version: 2`, NOT a v3 bump
  (shipped v0.0.27–v0.0.35).** `StepRecord` and `ManifestTotals` gained
  `model_usage: map[model]{cost_usd, tokens_input, tokens_output,
  tokens_cache_read, tokens_cache_creation, num_turns}`, `StepRecord.sessions[]`
  (per-claude-session breakdown: main REPL + one entry per sub-agent, keyed by
  agent_id with `parent_session_id`), and `totals.num_turns`. **Do NOT bump the
  schema to 3:** the eval's reader (`apex_eval/ape_manifest.py`,
  `MAX_SCHEMA_VERSION = 2`) raises `ManifestSchemaError` for any version outside
  `[1,2]` but tolerates unknown fields (field-by-field `raw.get`), so a v3 bump
  breaks the eval while added v2 fields do not. **Dropped from the original
  shape:** the 5m/1h cache split (`Totals` collapses `ephemeral_5m +
  ephemeral_1h` into one `CacheCreationTokens` today) and `StepRecord.
  ModelObserved` (per-model attribution lives in the `model_usage` keys
  instead). Reintroduce the 5m/1h split only if D6 needs the tier comparison.
  **IMPLEMENTED v0.0.37 (2026-07-04) — shipped ADDITIVELY under
  `schema_version: 2`, NOT a v3 bump.** `Totals`, `StepRecord`,
  `ManifestTotals`, every `model_usage` entry, and the `ape task` envelope now
  carry `tokens_cache_creation_5m` / `tokens_cache_creation_1h`
  (`cache_creation_5m_input_tokens` / `cache_creation_1h_input_tokens` on the
  envelope) alongside the unchanged summed `tokens_cache_creation`. The scanner
  already parsed + priced the tiers (`formula.go`); this stops collapsing them
  in `Totals.Add`. Golden + manifest round-trip tests lock `5m + 1h == sum`. The
  per-turn `TurnRecord` + H6 (requestId/stop_reason) dedup portion of D1 remains
  deferred. Original decision rationale follows. Keep `tokens_cache_creation` as the
  sum (5m + 1h) and add `tokens_cache_creation_5m` / `tokens_cache_creation_1h`
  alongside it (mirror on the `ape task` envelope's `usage`/`model_usage`/
  `sessions[]` blocks: keep `cache_creation_input_tokens`, add
  `cache_creation_5m_input_tokens` / `cache_creation_1h_input_tokens`). Rationale
  from an eval-side review of the proposed v3 rename (verdict: NO-GO): every eval
  consumer sums cache-creation into a single quantity
  (`input_tokens = tokens_input + tokens_cache_read + tokens_cache_creation`,
  `apex_eval/runner.py`) and would immediately re-add 5m + 1h — the split has
  zero consumer there. A v3 bump would (a) hard-reject at
  `ape_manifest.py:read_manifest` (`MAX_SCHEMA_VERSION = 2`), zeroing all ape
  pipeline-stage telemetry; (b) silently `.get()`-to-zero the renamed field on
  the ape-task path, under-reporting input tokens 3–16% with no assertion
  guarding it; and (c) force a permanent v2/v3 normalization shim plus break
  ~1,310 archived v2 manifests and older pinned binaries. Removing a
  summed-by-every-consumer field is producer-side tidiness every consumer pays
  for — additive keeps ape's granularity at zero eval cost.
- Wire the dead code — **done:** pipeline and `ape task` finalize fold into the
  rollup (`FoldPipelineRun` / `FoldTaskRun`; `RebuildRollup` walks both
  `_output/pipelines` and `_output/tasks`); `ape chat` exit writes `session.yaml`
  (`runlog.WriteSessionYAML`) and folds `FoldChat`. **Done (v0.0.42):**
  `cost.NewTailer` had zero production callers, so it was default-deleted (the TUI
  never wanted live cost — `StepTelemetry`'s rescan is adequate). `AssistantLine`,
  the one symbol `scanTurns` still parses, was relocated to `internal/cost/line.go`.
- **Shipped v0.0.36:** `Rollup`/`Bucket` gained `PerModel`, and the `sumTotals`
  `NumTurns` drop (it had summed `CostUSD` + the four token fields only,
  `rollup.go`) is fixed so rollup totals carry turn counts. Eval-neutral (the eval
  never reads the rollup).
- **Shipped v0.0.36:** `ape costs` gained a MODEL column, and `ape costs run
  <run-id>` (reader over a manifest) + `ape costs chat <chat-id>` (reader over
  `session.yaml`) are now registered (`internal/apecmd/costs.go` —
  `newCostsRunCmd`/`newCostsChatCmd`), so the help no longer lies. Eval-neutral
  (the eval never runs `ape costs`).

### D6: Close out the discrepancy investigation — RESOLVED 2026-07-08

**Done.** `development/research/cost-discrepancy-20260521.md` is closed: the
per-tier 5m/1h split shipped in v0.0.37, ape's prices + cache multipliers were
confirmed correct (2026-07-02), and the v0.0.35 sub-agent double-count fix removed
the ape-side over-count — so ape's transcript-scan cost is trustworthy, and the
residual gap vs stream-json is a legitimate measurement-window difference (H2:
full session tree vs orchestrator-only), not a defect. The optional per-tier
falsification test below was **not run** (moot: the multipliers are confirmed, so
ape's per-tier cost is correct regardless of the 5m/1h mix).

Original plan: with per-tier sums now first-class, run the falsification test from
the research doc — for one paired `--tui` vs historical `-P` step, compare
`sum(ephemeral_1h)` vs `sum(ephemeral_5m)`; record findings in
`development/research/cost-discrepancy-20260521.md` and either close it or promote
the surviving hypothesis with data.

## Scope — steps

1. D1 + D3 in `internal/cost` (pure, heavily unit-tested against fixture
   JSONL including streaming-fragment and sidechain fixtures under
   `testdata/`).
2. D2 discovery + D4 copy — **done in v0.0.35** (SubagentStop
   `agent_transcript_path` keyed by agent_id, `subagents/agent-*.jsonl`
   robustness sweep, double-count guard, transcript copy into the run dir). The
   logic lives in `internal/apecmd/pipeline_interactive.go`, coupled to the
   runner — **not yet extracted** as the reusable `internal/cost.SessionFiles`
   helper the plan wanted for PLAN-13 upload / PLAN-17 metrics. Extract when
   PLAN-13/17 need it.
3. D5 per-model manifest + wiring. **No schema bump** — additive under v2,
   verified against the eval's `ape_manifest.py` (rejects v3, ignores unknown
   fields). Per-model fields, rollup wiring, rollup `PerModel`, the `sumTotals`
   NumTurns fix, the `ape costs` MODEL column, and the `ape costs run`/`chat`
   subcommands all shipped (v0.0.35–36); the 5m/1h split shipped v0.0.37.
   The last item under D5, the `cost.NewTailer` dead-code cleanup, shipped v0.0.42.
4. D6 analysis + doc update — **DONE 2026-07-08**
   (`development/research/cost-discrepancy-20260521.md` resolved): the 5m/1h split
   shipped v0.0.37 and, with prices/multipliers already confirmed correct + the
   v0.0.35 double-count fix, ape's numbers are trustworthy; the formal tier
   comparison is optional (moot given the confirmed multipliers).
5. Docs: `reference/cost-model.md` and `reference/pipeline-run-manifest.md`
   updated (coordinates with PLAN-9 F4).

## Acceptance

- A fixture run with a subagent-dispatching skill shows subagent tokens in the
  step's `model_usage` block and per-session under `sessions[]`, attributed to
  the correct model — **met in v0.0.35** (the `apex-story-batch-dev` sub-agent
  spawner is PLAN-11's ship gate).
- Re-scanning the two archived sessions named in the research doc
  (`0a675bc4…`, `eac5a5c5…`, if still present) produces per-model numbers;
  per-tier (5m/1h) numbers are available since the v0.0.37 split; results would be
  recorded in `development/research/cost-discrepancy-20260521.md`.
- `ape costs --output-format json` includes per-model breakdowns. *(Met — D5
  rollup `PerModel` shipped v0.0.36.)*
- **Per-model manifest fields are additive under `schema_version: 2` and the
  eval reads the manifest unmodified.** (Corrected 2026-07-03: the original
  "manifest v3 is additive: the eval reads it unmodified" was wrong — the eval's
  `ape_manifest.py` sets `MAX_SCHEMA_VERSION = 2` and raises `ManifestSchemaError`
  on `schema_version: 3`, so per-model *must* ship as added v2 fields, which is
  what v0.0.27–v0.0.35 did. What the eval tolerates is unknown fields, not a
  higher version number.)

## Risks

- Transcript format is explicitly unstable upstream (no official spec). The
  scanner already tolerates unknown lines; fixtures pin the shapes we rely
  on, and `version` (Claude Code version) is captured per turn so drift is
  diagnosable.
- **Frozen contract — `hook-events.jsonl`.** The eval reconstructs
  conversations by reading the run dir's `hook-events.jsonl` and following
  its `payload.transcript_path` (`apex_eval/runner.py:_task_conversation`).
  Its event/field shape is additive-only; nothing in this plan touches it,
  and future plans must not either.
- Date-aware pricing adds complexity for one model (Sonnet 5 intro); the
  fallback is charging the post-intro rate (conservative over-estimate) if
  the dated lookup is judged not worth it — decide in review.
