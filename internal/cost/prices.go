// Package cost wires Claude session JSONL → per-step USD totals →
// project rollup file. PLAN-5 / C7.
//
// Data path varies by mode:
//
//   --print mode    `result` event in stream-json stdout (existing
//                   PLAN-3 path, unchanged).
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
// TODO(PLAN-5 / C7): confirm these per-million-token figures against
// the current Anthropic price list before shipping. The 1.25× / 2.00×
// / 0.10× cache multipliers in the formula come from the design doc
// §11 and are not in this table.
var Prices = map[string]ModelPrice{
	// Claude 4.x family — initial values pending review.
	"claude-opus-4-7":   {BaseInput: 15.00, Output: 75.00},
	"claude-opus-4-6":   {BaseInput: 15.00, Output: 75.00},
	"claude-sonnet-4-6": {BaseInput: 3.00, Output: 15.00},
	"claude-sonnet-4-5": {BaseInput: 3.00, Output: 15.00},
	"claude-haiku-4-5":  {BaseInput: 1.00, Output: 5.00},
}

// Lookup returns the price for model, plus a flag indicating whether
// the model was known. Unknown models return zero price (caller may
// stamp a `cost_note` on the affected step's manifest record).
func Lookup(model string) (ModelPrice, bool) {
	p, ok := Prices[model]
	return p, ok
}
