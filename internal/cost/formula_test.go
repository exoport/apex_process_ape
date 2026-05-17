package cost

import (
	"testing"
)

func TestTurnCost_AllTerms(t *testing.T) {
	// Use a unit-priced model so the formula's coefficient values
	// surface clearly in the test.
	p := ModelPrice{BaseInput: 1.0, Output: 2.0}
	u := UsageBlock{
		InputTokens:   1_000_000, // contributes 1.0
		OutputTokens:  500_000,   // contributes 2.0 * 0.5 = 1.0
		CacheRead:     1_000_000, // contributes 1.0 * 0.10 = 0.10
		CacheCreation: CacheCreation{Ephemeral5m: 1_000_000, Ephemeral1h: 1_000_000},
		// 5m contributes 1.0 * 1.25 = 1.25
		// 1h contributes 1.0 * 2.00 = 2.00
	}
	got := TurnCost(u, p)
	want := 1.0 + 1.25 + 2.00 + 0.10 + 1.0
	if got != want {
		t.Errorf("TurnCost = %.4f, want %.4f", got, want)
	}
}

func TestTurnCost_UnknownModelIsZero(t *testing.T) {
	got := TurnCost(UsageBlock{InputTokens: 1_000_000}, ModelPrice{})
	if got != 0 {
		t.Errorf("zero price model should yield 0 cost, got %f", got)
	}
}

func TestTotals_Add(t *testing.T) {
	p := ModelPrice{BaseInput: 1.0, Output: 1.0}
	var totals Totals
	totals.Add(UsageBlock{InputTokens: 500_000, OutputTokens: 500_000}, p)
	totals.Add(UsageBlock{InputTokens: 500_000, OutputTokens: 500_000}, p)
	if totals.InputTokens != 1_000_000 || totals.OutputTokens != 1_000_000 {
		t.Errorf("token aggregation off: %+v", totals)
	}
	// Each turn: 0.5M input × $1 + 0.5M output × $1 = $1.00; × 2 = $2.00.
	if totals.CostUSD < 1.99 || totals.CostUSD > 2.01 {
		t.Errorf("cost aggregation off: %f", totals.CostUSD)
	}
}

func TestLookup_KnownAndUnknown(t *testing.T) {
	if _, ok := Lookup("claude-opus-4-7"); !ok {
		t.Error("expected claude-opus-4-7 in price table")
	}
	if _, ok := Lookup("fictional-model"); ok {
		t.Error("did not expect fictional-model in price table")
	}
}
