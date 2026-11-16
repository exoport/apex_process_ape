package cost

import (
	"math"
	"testing"
)

// ALIAS-R1 (research): the arithmetic for Claude Fable 5.2, not just its row.
// The pricing page (read 2026-11-16) gives $8 input and $40 output per MTok,
// cache hits at $0.20 (0.025x); Fable 5.1's $10 / $50 / $0.25 would be red.
func TestFable52TurnCost(t *testing.T) {
	t.Parallel()

	p, ok := Lookup("claude-fable-5-2")
	if !ok {
		t.Fatal("claude-fable-5-2 missing")
	}
	for _, tc := range []struct {
		name string
		u    UsageBlock
		want float64
	}{
		{"1M in + 1M out", UsageBlock{InputTokens: 1_000_000, OutputTokens: 1_000_000}, 48.00},
		{"1M cache read", UsageBlock{CacheRead: 1_000_000}, 0.20},
	} {
		if got := TurnCost(tc.u, p); math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("Fable 5.2 %s = %v, want %v", tc.name, got, tc.want)
		}
	}
	if got, ok := FamilyAliases()["fable"]; !ok || got != "claude-fable-5-2" {
		t.Errorf("fable resolves to %q, want claude-fable-5-2", got)
	}
}
