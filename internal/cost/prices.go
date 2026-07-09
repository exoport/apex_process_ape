// Package cost wires Claude session JSONL → per-step USD totals →
// project rollup file. PLAN-5 / C7.
//
// Data path varies by mode:
//
//	--eval mode     `result` event in stream-json stdout (existing
//	                PLAN-3 path, unchanged; renamed from --print).
//	web / --tui     Per-assistant-message `usage` blocks in
//	                ~/.claude/projects/<hash>/<sid>.jsonl. This package
//	                tails the symlink that runlog drops under
//	                <run-dir>/transcripts/.
//
// `ape costs` exposes today / this week / total rollups; the per-run
// detail comes from the existing PLAN-3 manifest.yaml.
package cost

import (
	"strings"
	"time"
)

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
// Source: https://platform.claude.com/docs/en/about-claude/pricing
// fetched 2026-07-02. Per-million-tokens, USD. The 1.25× / 2.00× /
// 0.10× cache multipliers live in formula.go and apply on top of the
// BaseInput rate here.
//
// NOTE: Opus 4.5+ uses **half** the input rate and **one-third** the
// output rate of Opus 4 / 4.1. A future model bump must update this
// table — there is no API to fetch live prices. `ape costs update
// --from <yaml>` persists overrides to ~/.ape/prices.yaml.
var Prices = map[string]ModelPrice{
	// Claude 5 family ($10 in / $50 out).
	"claude-fable-5":  {BaseInput: 10.00, Output: 50.00},
	"claude-mythos-5": {BaseInput: 10.00, Output: 50.00},
	// Claude Sonnet 5 — the STANDARD (post-intro) rate ($3 in / $15 out).
	// The $2/$10 intro window through 2026-08-31 is applied date-aware by
	// LookupAt (datedPrices); this entry is the conservative fallback a
	// dateless Lookup returns (PLAN-10 D3).
	"claude-sonnet-5": {BaseInput: 3.00, Output: 15.00},
	// Claude Opus 4.5+ — current pricing tier ($5 in / $25 out).
	"claude-opus-4-8": {BaseInput: 5.00, Output: 25.00},
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

// SonnetIntroEnd is the last instant Claude Sonnet 5 bills at its
// promotional intro rate ($2 in / $10 out). Turns timestamped at or
// before this fall in the intro window; after it, the standard
// Prices["claude-sonnet-5"] rate ($3/$15) applies. Confirmed against
// Anthropic's pricing page 2026-07-02 (PLAN-10 D3).
var SonnetIntroEnd = time.Date(2026, 8, 31, 23, 59, 59, 0, time.UTC)

// datedPrice overrides the standard Prices entry for a model when a
// turn's timestamp falls at/before Until — a promotional/intro window.
// After Until, LookupAt falls through to Prices. A dateless Lookup
// (zero ts) never matches a window, so it returns the conservative
// standard rate (PLAN-10 D3: "the standard-rate table is the blessed
// conservative fallback").
type datedPrice struct {
	Until time.Time
	Price ModelPrice
}

// datedPrices carries the built-in promotional windows. Entries are
// checked oldest-window-first; the first whose Until is at/after the
// turn timestamp wins. Extend here on future intro pricing — like
// Prices, there is no API to fetch this.
var datedPrices = map[string][]datedPrice{
	"claude-sonnet-5": {
		{Until: SonnetIntroEnd, Price: ModelPrice{BaseInput: 2.00, Output: 10.00}},
	},
}

// NormalizeModel canonicalizes a model identifier for price lookup
// and per-model attribution:
//
//   - strips a `[...]` context-window suffix — the spawn-time forms
//     `opus[1m]` / `claude-opus-4-8[1m]` bill at the base model's
//     rate (no 1M-context surcharge on current models);
//   - resolves claude's short spawn aliases (`opus`, `sonnet`, …) to
//     the model id the current CLI resolves them to. The transcript
//     records the full resolved id, so the alias hop only matters for
//     callers that log the alias form (e.g. a spec's `model:` field).
func NormalizeModel(model string) string {
	if i := strings.IndexByte(model, '['); i > 0 {
		model = model[:i]
	}
	model = strings.TrimSpace(model)
	if full, ok := modelAliases[model]; ok {
		return full
	}
	return model
}

// modelAliases maps claude's short spawn-time aliases to the full
// model id the CLI currently resolves them to. Revisit on model bumps
// — like Prices, there is no API for this; the authoritative id is
// whatever the transcript's `message.model` records.
var modelAliases = map[string]string{
	"opus":   "claude-opus-4-8",
	"sonnet": "claude-sonnet-5",
	"haiku":  "claude-haiku-4-5",
	"fable":  "claude-fable-5",
	"mythos": "claude-mythos-5",
}

// Lookup returns the price for model, plus a flag indicating whether
// the model was known. The model id is normalized first (context-
// window suffix stripped, spawn aliases resolved) so `opus[1m]` and
// `claude-opus-4-8` resolve to the same entry. Unknown models return
// zero price (caller may stamp a note on the affected step's manifest
// record).
//
// Lookup consults ~/.ape/prices.yaml first (PLAN-5 / C7 — `ape costs
// update --from <file>` persists overrides there); the built-in
// Prices map is the fallback. Overrides are cached after the first
// Lookup of a process; SaveOverrides drops the cache.
//
// Lookup is the dateless form: it returns the standard (post-intro)
// rate for date-windowed models — the conservative fallback. Callers
// with a turn timestamp (the transcript scanner) should use LookupAt
// so promotional windows (Sonnet 5 intro) price correctly (PLAN-10 D3).
func Lookup(model string) (ModelPrice, bool) {
	return LookupAt(model, time.Time{})
}

// LookupAt is the date-aware price lookup. Resolution order:
//
//  1. ~/.ape/prices.yaml overrides (an override with no effective_from
//     wins unconditionally; one with effective_from applies only when ts
//     is at/after it — a zero ts never activates a dated override, so the
//     dateless Lookup stays conservative);
//  2. a built-in promotional window (datedPrices) matching ts;
//  3. the standard Prices table.
//
// The model id is normalized first so `opus[1m]` and `claude-opus-4-8`
// resolve identically. Unknown models return the zero price + false.
func LookupAt(model string, ts time.Time) (ModelPrice, bool) {
	model = NormalizeModel(model)
	if ov, ok := loadOverridesOnce()[model]; ok {
		if ov.From.IsZero() || (!ts.IsZero() && !ts.Before(ov.From)) {
			return ov.Price, true
		}
	}
	if !ts.IsZero() {
		for _, dp := range datedPrices[model] {
			if !ts.After(dp.Until) {
				return dp.Price, true
			}
		}
	}
	p, ok := Prices[model]
	return p, ok
}
