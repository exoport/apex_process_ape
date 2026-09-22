package cost

import (
	"math"
	"testing"
)

// The cache-read multiple is NOT one number. Most models bill a read at
// 0.10x base input; Claude Opus 5.5 bills 0.05x and Claude Fable 5.1 /
// Mythos 5.1 bill 0.025x. ape applied a single global 0.10 to everything,
// which priced Opus 5.5 cache reads 2x high and Fable 5.1's 4x high.
//
// That is not a rounding error: cache reads are usually the largest token
// category in an agentic run, so the overstatement lands on the biggest
// term in the bill.
func TestCacheReadMultiplierIsPerModel(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		model string
		want  float64
	}{
		{"claude-opus-5-5", 0.05},
		{"claude-fable-5-1", 0.025},
		{"claude-opus-5", DefaultCacheReadMul},
		{"claude-sonnet-5", DefaultCacheReadMul},
		{"claude-haiku-4-5", DefaultCacheReadMul},
	} {
		p, ok := Lookup(tc.model)
		if !ok {
			t.Errorf("%s has no price row", tc.model)
			continue
		}
		if got := p.CacheReadMultiplier(); got != tc.want {
			t.Errorf("%s cache-read multiple = %v, want %v", tc.model, got, tc.want)
		}
	}
}

// The multiple must reach the arithmetic, not just the table. A per-model
// field nothing multiplies by is the same defect wearing a fix.
func TestTurnCostUsesThePerModelCacheRead(t *testing.T) {
	t.Parallel()

	const reads = 1_000_000
	u := UsageBlock{CacheRead: reads}

	opus55, ok := Lookup("claude-opus-5-5")
	if !ok {
		t.Fatal("claude-opus-5-5 missing")
	}
	// 1M cache-read tokens at 4.00 base × 0.05 = $0.20, the published
	// per-MTok cache-read price for this model.
	if got := TurnCost(u, opus55); math.Abs(got-0.20) > 1e-9 {
		t.Errorf("Opus 5.5 cache-read cost = %v, want 0.20", got)
	}

	fable, ok := Lookup("claude-fable-5-1")
	if !ok {
		t.Fatal("claude-fable-5-1 missing")
	}
	// 10.00 × 0.025 = $0.25/MTok.
	if got := TurnCost(u, fable); math.Abs(got-0.25) > 1e-9 {
		t.Errorf("Fable 5.1 cache-read cost = %v, want 0.25", got)
	}

	// A standard model is unchanged: 5.00 × 0.10 = $0.50/MTok.
	opus5, ok := Lookup("claude-opus-5")
	if !ok {
		t.Fatal("claude-opus-5 missing")
	}
	if got := TurnCost(u, opus5); math.Abs(got-0.50) > 1e-9 {
		t.Errorf("Opus 5 cache-read cost = %v, want 0.50", got)
	}

	// The guard must reject the old behaviour. Under the previous single
	// global 0.10, Opus 5.5 would have priced these reads at 0.40 — double.
	if math.Abs(opus55.BaseInput*DefaultCacheReadMul-0.40) > 1e-9 {
		t.Fatal("sanity: the pre-fix global multiple no longer reproduces the 2x overstatement")
	}
}

// The zero value must never read as "cache reads are free". Every
// construction site leaves CacheReadMul unset for a standard model, so a
// naive read of the field would bill those reads at 0%.
func TestUnsetCacheReadMultipleResolvesToTheStandardRate(t *testing.T) {
	t.Parallel()

	var unset ModelPrice
	if got := unset.CacheReadMultiplier(); got != DefaultCacheReadMul {
		t.Errorf("unset multiple = %v, want %v", got, DefaultCacheReadMul)
	}

	p := ModelPrice{BaseInput: 10.0, Output: 50.0}
	if got := TurnCost(UsageBlock{CacheRead: 1_000_000}, p); math.Abs(got-1.00) > 1e-9 {
		t.Errorf("unset-multiple cache-read cost = %v, want 1.00 (10.0 × 0.10)", got)
	}
}
