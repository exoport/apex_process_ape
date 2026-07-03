---
plan_id: PLAN-10
created_at: 2026-07-02
status: proposed
tags:
  - cost
  - telemetry
  - transcripts
  - subagents
  - billing-accuracy
summary: Rework `internal/cost` from single-aggregate `Totals` to per-turn records carrying timestamp, model, requestId, stop_reason, and the full cache 5m/1h split; dedupe the way ccusage does (message.id + requestId, prefer stop_reason entries); discover and scan subagent transcripts (`<session-id>/subagents/agent-*.jsonl`); copy transcripts into the run dir instead of symlinking; refresh the price table (Fable 5/Mythos 5, Opus 4.8, date-aware Sonnet 5 intro pricing) and normalize `[1m]` model suffixes; add per-model blocks to manifest v3 and `ape costs`; and wire the dead plumbing (chat cost recording, incremental rollup folds). This is the prerequisite for resolving development/pending/cost-discrepancy-20260521.md and for any cost number becoming load-bearing.
origin:
  - development/pending/cost-discrepancy-20260521.md — open since 2026-05-21; its "recommended path" step 4 (per-model breakdown) is this plan.
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

### D5: Manifest v3 + rollup + `ape costs`

- `ManifestSchemaVersion = 3`: `StepRecord` and `ManifestTotals` gain
  `per_model: map[model]{cost_usd, in, out, cache_read, cache_5m, cache_1h,
  turns}`; `StepRecord.ModelObserved` alongside the spec-declared `Model`;
  totals gain `num_turns`.
- Wire the dead code: pipeline finalize calls `FoldPipelineRun` (incremental)
  for *all* modes, not just web-exit rebuild; `ape chat` exit scans via the
  bridge-known transcript path (fall back `ScanLatestSession`), writes
  `session.yaml` (`runlog.WriteSessionYAML`), folds `FoldChat`. Delete
  `cost.NewTailer` unless the TUI wants live cost (decide in review; default
  delete — `StepTelemetry`'s rescan is adequate).
- `Rollup`/`Bucket` gain `PerModel`; `sumTotals` fixed to sum `NumTurns`.
- `ape costs` grows a MODEL column; implement `ape costs run <run-id>`
  (reader over a manifest — restoring the command PLAN-9 removed from help).

### D6: Close out the discrepancy investigation

With per-tier sums now first-class, run the falsification test from the
pending doc: for one paired `--tui` vs historical `-P` step, compare
`sum(ephemeral_1h)` vs `sum(ephemeral_5m)`. Record findings in
`development/pending/cost-discrepancy-20260521.md` and either close it or
promote the surviving hypothesis with data.

## Scope — steps

1. D1 + D3 in `internal/cost` (pure, heavily unit-tested against fixture
   JSONL including streaming-fragment and sidechain fixtures under
   `testdata/`).
2. D2 discovery + D4 copy (fs-level tests with fake `~/.claude` trees).
3. D5 manifest v3 + wiring (bump schema; eval-repo compatibility check —
   its `ape_manifest.py` reader must tolerate added fields; additive-only so
   yaml unmarshal is safe).
4. D6 analysis + pending-doc update.
5. Docs: `reference/cost-model.md` and `reference/pipeline-run-manifest.md`
   updated (coordinates with PLAN-9 F4).

## Acceptance

- A fixture run with a subagent-dispatching skill shows subagent tokens in
  the step's `per_model` block, attributed to the correct model.
- Re-scanning the two archived sessions named in the pending doc
  (`0a675bc4…`, `eac5a5c5…`, if still present) with v2 produces per-model,
  per-tier numbers; results recorded in the pending doc.
- `ape costs --output-format json` includes per-model breakdowns.
- Manifest v3 is additive: the eval harness reads it unmodified.

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
