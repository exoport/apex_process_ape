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
	_ "embed"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// pricesYAML is the built-in price table. Kept as data rather than Go
// literals so the same bytes can be published as a release asset and fed
// back through `ape costs update --from`, letting a price correction ship
// without a new ape binary.
//
//go:embed prices.yaml
var pricesYAML []byte

// ModelPrice is the per-million-tokens USD cost for one model. The
// formula in formula.go consumes these values directly.
type ModelPrice struct {
	// BaseInput is the input price per 1M tokens, USD.
	BaseInput float64
	// Output is the output price per 1M tokens, USD.
	Output float64
}

// PriceSource records how a model's price was resolved. It exists because
// the original failure mode of this package was silence: LookupAt already
// returned an `ok` flag and every caller dropped it, so "model we have no
// price for" and "model that costs nothing" produced the same $0.00. Any
// code that turns tokens into dollars must carry the source alongside the
// number so an unpriced or estimated total can say so.
type PriceSource string

const (
	// PriceOverride: matched ~/.ape/prices.yaml (operator-supplied).
	PriceOverride PriceSource = "override"
	// PriceDated: matched a built-in promotional window for the turn's date.
	PriceDated PriceSource = "dated"
	// PriceExact: matched the built-in table by exact model id.
	PriceExact PriceSource = "exact"
	// PriceFamily: no exact row — priced from the model's family tier.
	// Approximate by construction; always reported as an estimate.
	PriceFamily PriceSource = "family"
	// PriceNone: nothing matched. Cost is zero and is NOT a real zero.
	PriceNone PriceSource = "none"
)

// Priced reports whether the source yielded a usable price at all.
func (s PriceSource) Priced() bool { return s != PriceNone && s != "" }

// Exact reports whether the price came from a specific rate for this exact
// model id (an override, a dated window, or the built-in table) rather than
// from a family estimate.
func (s PriceSource) Exact() bool {
	return s == PriceOverride || s == PriceDated || s == PriceExact
}

// Estimated reports whether the price is a family-tier approximation.
func (s PriceSource) Estimated() bool { return s == PriceFamily }

// Prices is the exact-match table keyed by the `model` field on each
// assistant-line `usage` block in the session JSONL, loaded from the
// embedded prices.yaml at init. Per-million-tokens, USD. The 1.25x / 2.00x
// / 0.10x cache multipliers live in formula.go and apply on top of the
// BaseInput rate here.
//
// There is no API that returns Anthropic's prices, so this table is
// hand-curated and will go stale when a model ships. That is expected and
// handled: familyTiers keeps a new id in the right order of magnitude,
// PriceSource makes the approximation visible, and `ape costs coverage`
// (plus `make check-prices` at release time) reports any locally-observed
// model the table does not cover exactly.
var Prices map[string]ModelPrice

// datedPrices carries the built-in promotional windows, loaded from
// prices.yaml. Entries are checked in file order; the first whose Until is
// at/after the turn timestamp wins.
var datedPrices map[string][]datedPrice

// modelAliases maps claude's short spawn-time aliases to the full model id
// the CLI currently resolves them to, loaded from prices.yaml. A stale
// alias no longer prices at zero — NormalizeModel leaves the bare word in
// place and the family tier catches it.
var modelAliases map[string]string

// familyTiers is the ordered family-estimate table, loaded from
// prices.yaml. Consulted only after an exact match fails.
var familyTiers []familyTier

// SonnetIntroEnd is the last instant Claude Sonnet 5 bills at its
// promotional intro rate. Derived from the claude-sonnet-5 window in
// prices.yaml — the YAML is the source of truth; this var is the named
// handle callers and tests use.
var SonnetIntroEnd time.Time

// PriceTableUpdated is the `updated:` stamp from prices.yaml, surfaced by
// `ape costs coverage` and `ape doctor` so an operator can see how old the
// built-in table is without reading the source.
var PriceTableUpdated string

// datedPrice overrides the standard Prices entry for a model when a
// turn's timestamp falls at/before Until — a promotional/intro window.
// After Until, resolution falls through to Prices.
type datedPrice struct {
	Until time.Time
	Price ModelPrice
}

// familyTier is one family's fallback rate.
type familyTier struct {
	Family string
	Price  ModelPrice
}

// matches reports whether a normalized model id belongs to this family:
// the bare family word (a spawn alias we failed to resolve, e.g. "opus"),
// or the conventional `claude-<family>-<generation>` id shape. Deliberately
// narrow — an id we cannot attribute to a known family must stay unpriced
// rather than be guessed at.
func (f familyTier) matches(model string) bool {
	if model == f.Family || strings.HasPrefix(model, "claude-"+f.Family+"-") {
		return true
	}
	// Legacy id ordering put the generation first (`claude-3-5-sonnet`).
	// Accept the family word anywhere inside a `claude-` id so those still
	// attribute, while a non-Claude id matches nothing.
	if !strings.HasPrefix(model, "claude-") {
		return false
	}
	return strings.HasSuffix(model, "-"+f.Family) || strings.Contains(model, "-"+f.Family+"-")
}

// priceTableFile is the on-disk (and embedded) schema of prices.yaml.
//
// Its `prices:` block is deliberately the same shape `ape costs update
// --from` accepts, so a corrected rate can be lifted straight out of this
// file into a local override with no rebuild. The other blocks — `aliases:`,
// `families:`, `dated_prices:` — are read only from the embedded copy;
// `--from` ignores them, so changing which generation a bare `sonnet`
// resolves to does require a new binary.
//
//nolint:tagliatelle // snake_case matches the on-disk / wire contract
type priceTableFile struct {
	Version     int                        `yaml:"version"`
	Updated     string                     `yaml:"updated"`
	Prices      map[string]priceRow        `yaml:"prices"`
	DatedPrices map[string][]datedPriceRow `yaml:"dated_prices"`
	Aliases     map[string]string          `yaml:"aliases"`
	Families    []familyRow                `yaml:"families"`
}

//nolint:tagliatelle // snake_case matches the on-disk / wire contract
type datedPriceRow struct {
	Until     time.Time `yaml:"until"`
	BaseInput float64   `yaml:"base_input"`
	Output    float64   `yaml:"output"`
}

//nolint:tagliatelle // snake_case matches the on-disk / wire contract
type familyRow struct {
	Family    string  `yaml:"family"`
	BaseInput float64 `yaml:"base_input"`
	Output    float64 `yaml:"output"`
}

func init() {
	tbl, err := parsePriceTable(pricesYAML)
	if err != nil {
		// The asset is embedded at compile time and covered by
		// TestEmbeddedPriceTableLoads — an error here means the binary was
		// built from a broken tree, which must not ship silently.
		panic("cost: embedded prices.yaml is invalid: " + err.Error())
	}
	applyPriceTable(tbl)
}

// parsePriceTable decodes and validates a price-table document.
func parsePriceTable(bs []byte) (priceTableFile, error) {
	var tbl priceTableFile
	if err := yaml.Unmarshal(bs, &tbl); err != nil {
		return tbl, fmt.Errorf("parse: %w", err)
	}
	if len(tbl.Prices) == 0 {
		return tbl, errors.New("no `prices:` map")
	}
	for model, row := range tbl.Prices {
		if err := validatePriceRow(model, row.BaseInput, row.Output); err != nil {
			return tbl, err
		}
	}
	for _, f := range tbl.Families {
		if f.Family == "" {
			return tbl, errors.New("families: entry with empty `family`")
		}
	}
	return tbl, nil
}

// validatePriceRow rejects a price row that cannot be a real rate.
//
// A misspelled key (`base_imput:`) unmarshals to zero and would price those
// tokens at $0 — the exact silent-zero failure this package exists to
// prevent, reintroduced through a typo in the table itself. So a rate of
// zero is only accepted for a sentinel id (`<synthetic>`, Claude Code's
// marker for a locally-generated turn whose usage really is all zeros).
func validatePriceRow(model string, baseInput, output float64) error {
	if baseInput < 0 || output < 0 {
		return fmt.Errorf("model %q has a negative price", model)
	}
	if strings.HasPrefix(model, "<") {
		return nil // sentinel; a genuine zero
	}
	if baseInput == 0 || output == 0 {
		return fmt.Errorf(
			"model %q has a zero price (base_input=%v output=%v) — check for a misspelled key; "+
				"a zero rate would silently report those tokens as free",
			model, baseInput, output)
	}
	return nil
}

// applyPriceTable installs a parsed document into the package-level tables.
func applyPriceTable(tbl priceTableFile) {
	PriceTableUpdated = tbl.Updated

	Prices = make(map[string]ModelPrice, len(tbl.Prices))
	for model, row := range tbl.Prices {
		Prices[model] = ModelPrice{BaseInput: row.BaseInput, Output: row.Output}
	}

	datedPrices = make(map[string][]datedPrice, len(tbl.DatedPrices))
	for model, rows := range tbl.DatedPrices {
		windows := make([]datedPrice, 0, len(rows))
		for _, r := range rows {
			windows = append(windows, datedPrice{
				Until: r.Until.UTC(),
				Price: ModelPrice{BaseInput: r.BaseInput, Output: r.Output},
			})
		}
		datedPrices[model] = windows
	}
	if w := datedPrices["claude-sonnet-5"]; len(w) > 0 {
		SonnetIntroEnd = w[0].Until
	}

	modelAliases = make(map[string]string, len(tbl.Aliases))
	maps.Copy(modelAliases, tbl.Aliases)

	familyTiers = make([]familyTier, 0, len(tbl.Families))
	for _, f := range tbl.Families {
		familyTiers = append(familyTiers, familyTier{
			Family: f.Family,
			Price:  ModelPrice{BaseInput: f.BaseInput, Output: f.Output},
		})
	}
}

// KnownModels returns every exact-match model id in the built-in table,
// sorted. Used by `ape costs coverage` and the release gate to report what
// the binary knows about.
func KnownModels() []string {
	out := make([]string, 0, len(Prices))
	for model := range Prices {
		out = append(out, model)
	}
	sort.Strings(out)
	return out
}

// NormalizeModel canonicalizes a model identifier for price lookup
// and per-model attribution:
//
//   - strips a `[...]` context-window suffix — the spawn-time forms
//     `opus[1m]` / `claude-opus-4-8[1m]` bill at the base model's
//     rate (no 1M-context surcharge on current models);
//   - folds case and separator punctuation, so a hand-written
//     `Claude_Sonnet_4.6` attributes to the same bucket as the
//     transcript's `claude-sonnet-4-6` (no-op on transcript-recorded ids,
//     which are already canonical);
//   - resolves claude's short spawn aliases (`opus`, `sonnet`, …) to
//     the model id the current CLI resolves them to. The transcript
//     records the full resolved id, so the alias hop only matters for
//     callers that log the alias form (e.g. a spec's `model:` field).
//     An alias missing from the table is returned unchanged and is then
//     caught by the family tier rather than falling through to unpriced.
//
// NormalizeModel produces an ATTRIBUTION key — a concrete id to price and
// group by. For canonicalizing a model the caller is about to hand to
// Claude Code, use CanonicalModelArg, which preserves bare aliases so
// Claude resolves its own generation.
func NormalizeModel(model string) string {
	base, _ := splitContextSuffix(model)
	base = foldModelPunctuation(base)
	if full, ok := modelAliases[base]; ok {
		return full
	}
	// A dated snapshot bills at its base model's rate, so
	// `claude-haiku-4-5-20251001` must land on `claude-haiku-4-5`. Only
	// strip when the base id is one we actually know — an unrecognized id
	// keeps its full form so the coverage report names what Claude Code
	// really emitted rather than a truncation of it.
	if stripped := stripDateSuffix(base); stripped != base {
		if _, ok := Prices[stripped]; ok {
			return stripped
		}
		if full, ok := modelAliases[stripped]; ok {
			return full
		}
	}
	return base
}

// Lookup returns the price for model, plus a flag indicating whether the
// model has an exact rate. The model id is normalized first (context-window
// suffix stripped, spawn aliases resolved) so `opus[1m]` and
// `claude-opus-4-8` resolve to the same entry.
//
// Lookup and LookupAt are the EXACT-MATCH forms: they never fall back to a
// family estimate, and ok=false means "this binary has no rate for this
// model". Callers that want the estimate — the transcript scanner does —
// use LookupSourceAt and carry the returned PriceSource with the number.
//
// Lookup consults ~/.ape/prices.yaml first (PLAN-5 / C7 — `ape costs
// update --from <file>` persists overrides there); the built-in Prices map
// is the fallback. Overrides are cached after the first Lookup of a
// process; SaveOverrides drops the cache.
//
// Lookup is the dateless form: it returns the standard (post-intro) rate
// for date-windowed models — the conservative fallback. Callers with a turn
// timestamp should use LookupAt so promotional windows price correctly
// (PLAN-10 D3).
func Lookup(model string) (ModelPrice, bool) {
	return LookupAt(model, time.Time{})
}

// LookupAt is the date-aware exact-match price lookup. Resolution order:
//
//  1. ~/.ape/prices.yaml overrides (an override with no effective_from
//     wins unconditionally; one with effective_from applies only when ts
//     is at/after it — a zero ts never activates a dated override, so the
//     dateless Lookup stays conservative);
//  2. a built-in promotional window (datedPrices) matching ts;
//  3. the standard Prices table.
//
// Unknown models return the zero price + false. Family estimates are
// deliberately excluded here so this function keeps answering the narrow
// question "does this binary have a rate for this exact model".
func LookupAt(model string, ts time.Time) (ModelPrice, bool) {
	p, src := LookupSourceAt(model, ts)
	if !src.Exact() {
		return ModelPrice{}, false
	}
	return p, true
}

// LookupSourceAt is the full date-aware resolution, including the family
// fallback, and reports how the price was reached. This is the form every
// cost-producing path should use: the PriceSource is what lets a report say
// "estimated" or "unpriced" instead of quietly printing $0.00.
//
// Resolution order is override → dated window → exact table → family tier
// → none.
func LookupSourceAt(model string, ts time.Time) (ModelPrice, PriceSource) {
	model = NormalizeModel(model)
	if ov, ok := loadOverridesOnce()[model]; ok {
		if ov.From.IsZero() || (!ts.IsZero() && !ts.Before(ov.From)) {
			return ov.Price, PriceOverride
		}
	}
	if !ts.IsZero() {
		for _, dp := range datedPrices[model] {
			if !ts.After(dp.Until) {
				return dp.Price, PriceDated
			}
		}
	}
	if p, ok := Prices[model]; ok {
		return p, PriceExact
	}
	for _, f := range familyTiers {
		if f.matches(model) {
			return f.Price, PriceFamily
		}
	}
	return ModelPrice{}, PriceNone
}
