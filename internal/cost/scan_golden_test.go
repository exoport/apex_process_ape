package cost

import (
	"path/filepath"
	"testing"
)

// TestScanSessionGolden is the permanent eval-regression guard for the
// zeroed-telemetry bug (P0a/P0b, 2026-07-02): a checked-in transcript
// fixture in the exact live claude-code shape (nested cache_creation,
// requestId, duplicate message.id, non-assistant line types,
// [1m]-suffixed model id) must yield non-zero tokens, turns, AND cost.
// If any parser drift re-zeros interactive telemetry, this fails.
func TestScanSessionGolden(t *testing.T) {
	res, err := ScanSession(filepath.Join("testdata", "session-golden.jsonl"))
	if err != nil {
		t.Fatalf("ScanSession: %v", err)
	}

	// Three unique assistant messages (msg_01A duplicated → deduped).
	if res.Totals.NumTurns != 3 {
		t.Fatalf("NumTurns = %d, want 3 (dedup by message.id)", res.Totals.NumTurns)
	}
	if want := 3333 + 12 + 5; res.Totals.InputTokens != want {
		t.Fatalf("InputTokens = %d, want %d", res.Totals.InputTokens, want)
	}
	if want := 1083 + 450 + 60; res.Totals.OutputTokens != want {
		t.Fatalf("OutputTokens = %d, want %d", res.Totals.OutputTokens, want)
	}
	if want := 15254 + 20000 + 1000; res.Totals.CacheReadTokens != want {
		t.Fatalf("CacheReadTokens = %d, want %d", res.Totals.CacheReadTokens, want)
	}
	if want := 7341 + 500; res.Totals.CacheCreationTokens != want {
		t.Fatalf("CacheCreationTokens = %d, want %d", res.Totals.CacheCreationTokens, want)
	}
	// PLAN-10 D1: the ephemeral 5m/1h split is retained additively. The
	// golden fixture has one 1h turn (7341) and one 5m turn (500); the
	// summed CacheCreationTokens must equal 5m + 1h.
	if res.Totals.CacheCreation1hTokens != 7341 {
		t.Fatalf("CacheCreation1hTokens = %d, want 7341", res.Totals.CacheCreation1hTokens)
	}
	if res.Totals.CacheCreation5mTokens != 500 {
		t.Fatalf("CacheCreation5mTokens = %d, want 500", res.Totals.CacheCreation5mTokens)
	}
	if res.Totals.CacheCreation5mTokens+res.Totals.CacheCreation1hTokens != res.Totals.CacheCreationTokens {
		t.Fatalf("5m+1h (%d) != CacheCreationTokens (%d)",
			res.Totals.CacheCreation5mTokens+res.Totals.CacheCreation1hTokens, res.Totals.CacheCreationTokens)
	}
	if res.Totals.CostUSD <= 0 {
		t.Fatalf("CostUSD = %v, want > 0 for priced models (opus-4-8, haiku-4-5)", res.Totals.CostUSD)
	}

	// Per-model attribution: the [1m] suffix normalizes onto the base
	// id, so exactly two models appear and their sums equal the
	// aggregate.
	if len(res.ByModel) != 2 {
		t.Fatalf("ByModel has %d entries, want 2 (opus-4-8 + haiku-4-5): %+v", len(res.ByModel), res.ByModel)
	}
	opus, ok := res.ByModel["claude-opus-4-8"]
	if !ok {
		t.Fatalf("ByModel missing claude-opus-4-8 (is [1m] normalization broken?): %+v", res.ByModel)
	}
	if opus.NumTurns != 2 {
		t.Fatalf("opus turns = %d, want 2", opus.NumTurns)
	}
	haiku, ok := res.ByModel["claude-haiku-4-5"]
	if !ok {
		t.Fatalf("ByModel missing claude-haiku-4-5: %+v", res.ByModel)
	}
	var sum Totals
	sum = sumTotals(sum, opus)
	sum = sumTotals(sum, haiku)
	if sum.InputTokens != res.Totals.InputTokens ||
		sum.OutputTokens != res.Totals.OutputTokens ||
		sum.CacheReadTokens != res.Totals.CacheReadTokens ||
		sum.CacheCreationTokens != res.Totals.CacheCreationTokens ||
		sum.CacheCreation5mTokens != res.Totals.CacheCreation5mTokens ||
		sum.CacheCreation1hTokens != res.Totals.CacheCreation1hTokens ||
		sum.CostUSD != res.Totals.CostUSD {
		t.Fatalf("ByModel sums != aggregate:\nsum: %+v\nagg: %+v", sum, res.Totals)
	}
	if res.LastModel != "claude-haiku-4-5" {
		t.Fatalf("LastModel = %q, want claude-haiku-4-5", res.LastModel)
	}
}

// TestScanSessionUnpricedModel locks in Totals.Add's price
// independence — the exact invariant the zeroed-telemetry bug
// violated. A model absent from Prices must still yield non-zero
// tokens and turns; only CostUSD stays 0.
func TestScanSessionUnpricedModel(t *testing.T) {
	res, err := ScanSession(filepath.Join("testdata", "session-unpriced.jsonl"))
	if err != nil {
		t.Fatalf("ScanSession: %v", err)
	}
	if res.Totals.NumTurns != 2 {
		t.Fatalf("NumTurns = %d, want 2", res.Totals.NumTurns)
	}
	if res.Totals.InputTokens != 110 || res.Totals.OutputTokens != 440 {
		t.Fatalf("tokens = in %d / out %d, want 110 / 440", res.Totals.InputTokens, res.Totals.OutputTokens)
	}
	if res.Totals.CostUSD != 0 {
		t.Fatalf("CostUSD = %v, want 0 for unpriced model", res.Totals.CostUSD)
	}
}

// TestLookupNormalization pins the P0b alias / suffix contract.
func TestLookupNormalization(t *testing.T) {
	cases := []string{
		"claude-opus-4-8",
		"claude-opus-4-8[1m]",
		"opus",
		"opus[1m]",
	}
	want, ok := Lookup("claude-opus-4-8")
	if !ok {
		t.Fatalf("claude-opus-4-8 missing from Prices")
	}
	for _, c := range cases {
		p, ok := Lookup(c)
		if !ok {
			t.Errorf("Lookup(%q) not found", c)
			continue
		}
		if p != want {
			t.Errorf("Lookup(%q) = %+v, want %+v", c, p, want)
		}
	}
	if got := NormalizeModel(" claude-sonnet-5[1m] "); got != "claude-sonnet-5" {
		t.Fatalf("NormalizeModel = %q", got)
	}
}

// TestPricesFreshness guards the table against silently missing
// current-generation models (P0b root cause: opus-4-8 / fable-5 /
// sonnet-5 absent → CostUSD 0 on every live run).
func TestPricesFreshness(t *testing.T) {
	for _, model := range []string{
		"claude-opus-4-8",
		"claude-sonnet-5",
		"claude-fable-5",
		"claude-mythos-5",
		"claude-haiku-4-5",
	} {
		p, ok := Prices[model]
		if !ok {
			t.Errorf("Prices missing %s", model)
			continue
		}
		if p.BaseInput <= 0 || p.Output <= 0 {
			t.Errorf("Prices[%s] has zero rate: %+v", model, p)
		}
	}
}
