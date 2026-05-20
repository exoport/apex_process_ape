// Package cost wires Claude session JSONL → per-step USD totals →
// project rollup file. PLAN-5 / C7.
//
// Data path varies by mode:
//
//   --eval mode     `result` event in stream-json stdout (existing
//                   PLAN-3 path, unchanged; renamed from --print).
//   web / --tui     Per-assistant-message `usage` blocks in
//                   ~/.claude/projects/<hash>/<sid>.jsonl. This package
//                   tails the symlink that runlog drops under
//                   <run-dir>/transcripts/.
//
// `ape costs` exposes today / this week / total rollups; the per-run
// detail comes from the existing PLAN-3 manifest.yaml.
package cost

// ModelPrice is the per-million-tokens USD cost for one model. The
// formula in formula.go consumes these values directly.
//
// IMPORTANT: these defaults are starting points and need confirmation
// before the cost-tracking PR merges. See PLAN-5 "When to stop and
// ask" — "Cost-table values." The plan deliberately deferred them
// from the plan body to the implementation PR. Surface this table to
// the user for review before shipping.
type ModelPrice struct {
	// BaseInput is the input price per 1M tokens, USD.
	BaseInput float64
	// Output is the output price per 1M tokens, USD.
	Output float64
}

// Prices is the hand-curated table keyed by the `model` field on
// each assistant-line `usage` block in the session JSONL. Unknown
// models cost $0 with the manifest carrying a `cost_note` (see
// Tracker for that wiring). PLAN-5 / C7.
//
// Source: https://platform.claude.com/docs/en/docs/about-claude/pricing
// fetched 2026-05-17. Per-million-tokens, USD. The 1.25× / 2.00× /
// 0.10× cache multipliers live in formula.go and apply on top of the
// BaseInput rate here.
//
// NOTE: Opus 4.5+ uses **half** the input rate and **one-third** the
// output rate of Opus 4 / 4.1. A future model bump must update this
// table — there is no API to fetch live prices. `ape costs update
// --from <yaml>` (reserved subcommand) is the planned override path.
var Prices = map[string]ModelPrice{
	// Claude Opus 4.5+ — new pricing tier ($5 in / $25 out).
	"claude-opus-4-7": {BaseInput: 5.00, Output: 25.00},
	"claude-opus-4-6": {BaseInput: 5.00, Output: 25.00},
	"claude-opus-4-5": {BaseInput: 5.00, Output: 25.00},
	// Claude Opus 4 / 4.1 — legacy pricing ($15 in / $75 out).
	"claude-opus-4-1": {BaseInput: 15.00, Output: 75.00},
	"claude-opus-4":   {BaseInput: 15.00, Output: 75.00},
	// Claude Sonnet 4.5+ ($3 in / $15 out).
	"claude-sonnet-4-6": {BaseInput: 3.00, Output: 15.00},
	"claude-sonnet-4-5": {BaseInput: 3.00, Output: 15.00},
	"claude-sonnet-4":   {BaseInput: 3.00, Output: 15.00},
	// Claude Haiku 4.5 ($1 in / $5 out).
	"claude-haiku-4-5": {BaseInput: 1.00, Output: 5.00},
	// Claude Haiku 3.5 — retired on first-party API but still
	// reachable via Bedrock / Vertex; keep for those captures.
	"claude-haiku-3-5": {BaseInput: 0.80, Output: 4.00},
}

// Lookup returns the price for model, plus a flag indicating whether
// the model was known. Unknown models return zero price (caller may
// stamp a `cost_note` on the affected step's manifest record).
//
// Lookup consults ~/.ape/prices.yaml first (PLAN-5 / C7 — `ape costs
// update --from <file>` persists overrides there); the built-in
// Prices map is the fallback. Overrides are cached after the first
// Lookup of a process; SaveOverrides drops the cache.
func Lookup(model string) (ModelPrice, bool) {
	if overrides := loadOverridesOnce(); overrides != nil {
		if p, ok := overrides[model]; ok {
			return p, true
		}
	}
	p, ok := Prices[model]
	return p, ok
}
