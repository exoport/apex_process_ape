---
created_at: 2026-05-21
status: open
tags:
  - cost
  - telemetry
  - interactive-exec
  - billing-accuracy
summary: After fixing the message.id dedup bug in cost.ScanSessionJSONL (commit 143644c), ape's transcript-derived cost for interactive cells (`--tui` / `--no-tui` / `--web` interactive) remains roughly 50–60% higher than claude's stream-json `total_cost_usd` for the same skill on the same fixture. The dedup was real and necessary, but a second discrepancy is exposed underneath it. Either ape's `cost.formula.go` rates / multipliers are wrong, or the stream-json `result` event is under-reporting, or the two are measuring different things. Worth resolving before the cost data becomes load-bearing for any decision (CI gates, dashboards, comparison runs, eval scoring).
---

# Cost discrepancy: transcript-scan vs stream-json `total_cost_usd`

## TL;DR

For one well-understood pipeline (`design`, identical fixture across all
cells, sandbox at `33b1793b`):

| Cell                                  | Source of cost number        | `apex-create-architecture` step cost |
| ------------------------------------- | ---------------------------- | ------------------------------------ |
| `--tui -P`                            | stream-json `total_cost_usd` | $4.68 (107 turns)                    |
| `--no-tui -P`                         | stream-json `total_cost_usd` | $4.44 (105 turns)                    |
| `--eval`                              | stream-json `total_cost_usd` | $2.71 ( 55 turns)                    |
| `--web -P`                            | stream-json `total_cost_usd` | $4.68 (107 turns)                    |
| `--tui` (transcript scan, post-dedup) | `cost.ScanSessionJSONL`      | **$7.28 (92 turns)**                 |

After the message.id dedup fix (commit `143644c`), the transcript-scan
result for the architecture step dropped from $13.65 to $7.28 on a
fresh run. The stream-json baseline from same-fixture `-P` runs is
roughly $4.50 ± $0.5 across runs. The remaining ~$2.50 gap is
structural — not run-to-run variance.

Same observation at the pipeline-total level: post-dedup interactive
total was $9.43; programmatic baseline is $4.97. ~1.9× ratio.

## Background

- ape/`internal/cost/formula.go` implements per-turn cost computation
  from the assistant message's `usage` block (per-million pricing,
  with multipliers for cache_read 0.10×, cache_creation_5m 1.25×,
  cache_creation_1h 2.00×).
- Stream-json mode (`claude -p ...`) ends each step with a
  `type:"result"` event carrying `total_cost_usd` and `modelUsage`
  per model. The pipeline runner records this on the manifest in
  programmatic cells.
- Interactive cells have no stream-json on stdout (claude REPL inside
  tmux). PLAN-6 round-3 (commit `dc651af`) added transcript-scan
  telemetry via `cost.ScanSessionJSONL` on the per-session JSONL at
  `~/.claude/projects/<hash>/<sid>.jsonl`.
- The dedup bug fixed in commit `143644c` was real: claude logs the
  same assistant message multiple times under distinct top-level
  `uuid` values. That fix is correct and validated.

## What we have

Per-turn data from one architect session
(`~/.claude/projects/.../0a675bc4-9fe2-41e3-9efb-3c5412b935db.jsonl`,
the original $13.65 / 80-line run):

| Metric                            | After dedup (`--tui`) | Stream-json (`--tui -P`)    |
| --------------------------------- | --------------------- | --------------------------- |
| Turn count (unique by message.id) | 26                    | 107                         |
| `input_tokens` (sum)              | 15,031                | 12,653 (orchestrator only?) |
| `output_tokens` (sum)             | 30,682                | 41,554                      |
| `cache_read_input_tokens` (sum)   | 1,703,661             | 333,677                     |
| `cache_creation` (sum, 1h)        | 195,953               | 16,078 (no 1h/5m breakdown) |
| Total cost                        | $3.65 (ape recompute) | $4.68 (claude reported)     |
| Model                             | `claude-opus-4-7`     | `claude-opus-4-7[1m]`       |

Several things to note from this table:

- The two scans see different turn counts (26 vs 107). The
  interactive transcript ended with 26 unique messages; the
  programmatic stream-json reported 107. Different runs, but the
  ratio is suspicious — possibly an indication of what the
  programmatic mode does differently (more sub-agent dispatch?
  different `Task` tool usage? different model recursion?).
- The model strings differ: stream-json reports
  `claude-opus-4-7[1m]` (1M-context variant). The transcript stores
  `claude-opus-4-7` (no suffix). ape's pricing table has only the
  base name, so the lookup falls back gracefully — but the 1M-context
  variant may have a different price upstream that ape isn't aware
  of. See `internal/cost/prices.go` lines 47–48.
- The cache numbers are wildly different. Programmatic reports
  333k cache_read; interactive reports 1.7M. The interactive runs
  do appear to do more cache reading (more turns reading the
  context), but the ratio is ~5× which is larger than the
  turn-count ratio.

## Hypotheses

In rough priority order — the first two are most likely.

### H1: The 2.00× multiplier on `cache_creation_1h` is wrong or out of date

`internal/cost/formula.go` says cache_creation_1h is `BaseInput × 2.00`.
Anthropic's published pricing as of 2026-05-17 (when `prices.go`
header was last refreshed):

> Cache creation (1h): 2.0× base input rate
> Cache creation (5m): 1.25× base input rate
> Cache read: 0.10× base input rate

If those multipliers shifted (or if 1h is not actually 2.0× — could
be a different ratio), the per-turn cost is wrong. With ~196k
cache_creation_1h tokens at $5/M base, the difference between 1.5×
and 2.0× is ~$0.50 on the architecture step alone, and the gap
scales linearly with cache_creation_1h volume.

**To verify:** Anthropic's pricing page, current as of the run
date. If the multipliers shifted (or if there's an extended-context
surcharge for the `[1m]` variant), update `internal/cost/prices.go`
and `formula.go`.

### H2: Stream-json `result.usage` reports the orchestrator agent only, not sub-agent / Task-tool turns

The stream-json `result` event's top-level `usage` field had
`input_tokens: 702` on an apex-create-prd run; the transcript scan
(post-dedup) summed `input_tokens: 14` across all assistant turns
in the same session. The orders of magnitude differ in opposite
directions for different fields, but the through-line is that
**the two surfaces are measuring different windows**.

If the stream-json `result.usage` is the orchestrating agent's
direct usage and the per-model `modelUsage` block aggregates
sub-agent (Task tool) turns, then `total_cost_usd` may be
under-counting. The transcript JSONL captures everything claude
writes to its session log — which includes sub-agent calls.

But: a quick check of the architecture session via
`tool_use.name == "Task"` showed **zero Task tool calls** in that
transcript. So the "sub-agent invocations" hypothesis doesn't
explain the architecture step's gap. It might still explain other
skills.

**To verify:** instrument a known-leaf skill (no Task tool use,
single agent) in both modes; compare directly. If gap persists,
this hypothesis is wrong.

### H3: Claude's `total_cost_usd` does its own dedup at the Anthropic side

If Anthropic only bills once per unique `message.id` (which is what
they should do — otherwise duplicate logging would charge customers
twice), the transcript scan post-dedup should agree with the bill.
But `total_cost_usd` in the result event might already be billing-
accurate, computed by Anthropic with their own pricing tables, and
ape's recomputation has its own rate-table drift.

This is the inverse of H1: instead of ape over-counting, claude
might be under-reporting, OR ape's calculation is wrong but
in the direction the user prefers.

**To verify:** if anyone has access to an Anthropic billing dashboard
for a known run, compare the three numbers: dashboard total vs
stream-json total vs ape transcript-scan total. The dashboard is
canonical.

### H4: `cache_read_input_tokens` is being double-counted across the conversation tree

claude's session JSONL has `parentUuid` chains; assistant messages
can appear in multiple chain branches (different `uuid`, same
`message.id`). The dedup we shipped collapses identical messages,
but if `cache_read_input_tokens` accumulates across a branch — i.e.,
each "echo" of a message in a new branch has the same usage block
even though the actual API call to Anthropic happened once — then
ape's per-turn cost (which already integrates cache_read at 0.10×)
might still be over-counted in some shape.

This needs a deeper look at how Anthropic's API meters
`cache_read_input_tokens` vs what shows up on each assistant turn.

**To verify:** stream-json's `usage.cache_read_input_tokens` on the
terminal `result` event vs the sum of `cache_read_input_tokens`
across deduped assistant turns in the same session. If the result-
event number is significantly lower, ape is multiplying.

### H5: ape's price for `claude-opus-4-7` is too high

`prices.go` has:

```go
"claude-opus-4-7": {BaseInput: 5.00, Output: 25.00},
```

These rates are from 2026-05-17. If the actual posted rate is
$3.75 / $18.75 (the rumored "Opus 4.7 launch promo" rate), all
ape-computed costs are systematically 25–33% too high. Easy to
falsify against Anthropic's current pricing page.

## Why this matters

Today the cost numbers are advisory — nobody is gating on them.
But the moment any of these consumers come online, the
discrepancy becomes load-bearing:

- The eval at `apex_process_framework_eval` already records
  cost_usd on every run's manifest. If a regression check ever
  compares cost across runs or modes, an interactive-vs-
  programmatic delta will look like a regression even when no skill
  changed.
- `internal/cost/rollup.go` aggregates these numbers for the
  `ape costs` dashboard. Stale or inflated rollups confuse
  budgeting.
- Future eval features (cost-per-token-quality, cost-per-defect)
  need a single source of truth.

## Recommended path

1. **Pricing audit first** — easy and high-leverage. Fetch
   Anthropic's current published rates and verify
   `internal/cost/prices.go` line-by-line. Same for the cache
   multipliers in `internal/cost/formula.go`. If anything shifted,
   update + bump the "fetched 2026-05-17" header in `prices.go`.
2. **Run a controlled comparison cell.** Smallest possible skill
   (e.g., `apex-shard-doc` on an already-sharded doc — no work to
   do, but the session still runs). Run it in `--tui` and `--web -P`
   back-to-back. Compute:
   - Anthropic-reported `total_cost_usd` from the `-P` run.
   - ape's `cost.ScanSessionJSONL` on the same session's JSONL
     (the `-P` run also writes one).
   - ape's `cost.ScanSessionJSONL` on the `--tui` run's JSONL.

   If (1) and (2) agree but (3) differs, the bug is in interactive
   capture or in some session-state contamination. If (2) and (3)
   agree but (1) differs, the bug is in ape's formula or rates.

3. **Billing dashboard cross-check.** If access exists, pull the
   dashboard's number for a single run and compare against the
   computed numbers. This is the canonical resolution.
4. **Per-model token check.** Add per-model breakdown to ape's
   manifest output (currently a single `cost_usd` aggregate).
   Stream-json already provides `modelUsage` per model in its
   `result` event. The transcript scan can produce the same shape
   by grouping by `Message.Model`. Then comparing apples to
   apples is easier — and we'd catch silent haiku-title-generation
   drift.

## Pointers

- Dedup fix (the part already shipped): commit `143644c`
- Round-3 telemetry plumbing: commit `dc651af`
- Path-reset for multi-step stages: commit `c0750e2`
- Cost formula + rates: `internal/cost/formula.go`,
  `internal/cost/prices.go`
- Transcript scan: `internal/cost/scan.go`,
  `internal/cost/jsonltail.go`
- Manifest field plumbing: `pipeline.StepTelemetry` in
  `internal/pipeline/runner.go`,
  `interactiveCore.StepTelemetry` in
  `internal/apecmd/pipeline_interactive.go`
- Stream-json result-event consumer (the comparison reference):
  `parseResultEvent` in `internal/pipeline/result_event.go`

## Update 2026-05-21 (afternoon): head-to-head governance run

A back-to-back `governance --tui` and `governance --tui -P` run from
the same baseline (`b2843e2`) gives the first apples-to-apples
comparison. Aggregate:

| Mode                      | Total cost | Tokens in | Tokens out | Turns |
| ------------------------- | ---------- | --------- | ---------- | ----- |
| `--tui` (transcript scan) | $17.66     | 47,526    | 122,395    | 311   |
| `--tui -P` (stream-json)  | $14.56     | 52,981    | 127,273    | 444   |
| Ratio                     | 1.21×      | 0.90×     | 0.96×      | 0.70× |

Per-step costs are **not uniformly higher in transcript mode**.
Sample paired observations (same skill, same step index):

| Step                        | `--tui` $/turns | `--tui -P` $/turns | ratio     |
| --------------------------- | --------------- | ------------------ | --------- |
| apex-pattern-reconciliation | $0.55 / 8       | $0.73 / 22         | 0.75×     |
| apex-adr-survey             | $0.96 / 16      | $1.27 / 32         | 0.76×     |
| apex-adr-adoption [step 3]  | $1.41 / 23      | $0.77 / 21         | 1.83×     |
| apex-capability-survey      | $1.01 / 16      | $1.47 / 39         | 0.69×     |
| **apex-capability-update**  | **$1.71 / 40**  | **$0.58 / 25**     | **2.97×** |
| **apex-feature-survey**     | **$1.65 / 37**  | **$0.71 / 28**     | **2.34×** |
| apex-feature-create         | $2.16 / 35      | $2.13 / 48         | 1.01×     |

### Observations that promote H1 to the leading hypothesis

1. **Tokens agree within 10%** across the two modes (in: 47k vs 53k;
   out: 122k vs 127k). The cost gap is NOT explained by token volume.
2. **Turn counts diverge by 43%** — programmatic mode reports 444
   turns vs 311 in transcript mode for the SAME work. Cost-per-turn
   is dramatically lower in `-P`.
3. **Six of 18 steps had `-P` cost > `--tui` cost**, opposite the
   design-pipeline pattern. The aggregate gap is a signed sum of
   opposing per-step discrepancies — not a uniform rate inflation.

This pattern is most consistent with **cache-tier mix differences**
between exec modes:

- In interactive REPL mode, the claude session stays alive
  between turns. Caches likely land in the 1h tier
  (`cache_creation_1h × 2.00×` = 2.0× base input rate = $10/M).
- In programmatic per-step mode, each `claude -p` is a fresh
  process. Caches likely land in the 5m tier
  (`cache_creation_5m × 1.25×` = 1.25× base input rate = $6.25/M).
- Stream-json's `result.usage.cache_creation_input_tokens` is a
  single aggregated number — no 1h/5m breakdown. claude's own
  `total_cost_usd` must use SOME blended rate, but ape can't see
  what.
- Transcript-scan reads `cache_creation.ephemeral_1h_input_tokens`
  and `ephemeral_5m_input_tokens` separately. If the bulk of
  interactive's cache creation lands in 1h, the 2.0× multiplier
  bites hard.

### Falsification test for H1

The fastest way to settle this:

1. Pick one step from this run (e.g., apex-capability-update,
   the 2.97× outlier).
2. Scan its session transcript and dump
   `sum(ephemeral_1h_input_tokens)` vs
   `sum(ephemeral_5m_input_tokens)` for the `--tui` run.
3. Do the same for the `-P` run's session transcript (claude
   writes one for `-p` invocations too, at the same path).
4. If interactive shows mostly 1h and programmatic shows mostly
   5m, H1 is confirmed.

If H1 is confirmed, the next question is whether 2.0× / 1.25× is
the ACTUAL pricing or whether Anthropic uses something blended —
which needs the billing dashboard cross-check (H3 territory).

## Data captured for future replay

Sandbox runs from 2026-05-20 / 2026-05-21 with per-cell artifacts
under `/home/diegos/_dev/ape-web-sandbox/greeter/_output/pipelines/`
were wiped by repeated `33b1793b` resets, but session JSONLs persist
under `~/.claude/projects/-home-diegos--dev-ape-web-sandbox-greeter/`
and can be rescanned. Notable sessions:

- `0a675bc4-...jsonl` — `apex-create-architecture` from
  `design --tui` run `20260521-103057-b76f59e` (pre-path-reset
  fix; 80 lines, 26 unique).
- `eac5a5c5-...jsonl` — `apex-create-prd` from same run (19 lines,
  10 unique).

If those rotate out of `~/.claude/projects/` before this is picked
up, re-running `design --tui` against the sandbox produces a fresh
comparable dataset.
