package cost

import (
	"path/filepath"
	"testing"
	"time"
)

// TestScanStreamingDedup locks the PLAN-10 D1 H6 dedup: an assistant
// message.id logged twice — a partial snapshot (no stop_reason) then the
// final one (stop_reason set, larger usage) — must count ONCE, and the
// kept turn must be the final snapshot (output 500, not the partial's 5,
// and not the 505 a naive sum would give).
func TestScanStreamingDedup(t *testing.T) {
	res, err := ScanSession(filepath.Join("testdata", "session-streaming.jsonl"))
	if err != nil {
		t.Fatalf("ScanSession: %v", err)
	}
	if res.Totals.NumTurns != 2 {
		t.Fatalf("NumTurns = %d, want 2 (msg_stream deduped + msg_two)", res.Totals.NumTurns)
	}
	if res.Totals.OutputTokens != 500+30 {
		t.Fatalf("OutputTokens = %d, want %d (final snapshot wins, no double-count)", res.Totals.OutputTokens, 530)
	}
	if res.Totals.InputTokens != 100+20 {
		t.Fatalf("InputTokens = %d, want 120", res.Totals.InputTokens)
	}
	if len(res.ByModel) != 1 {
		t.Fatalf("ByModel has %d entries, want 1 (claude-sonnet-5)", len(res.ByModel))
	}
	if _, ok := res.ByModel["claude-sonnet-5"]; !ok {
		t.Fatalf("ByModel missing claude-sonnet-5: %+v", res.ByModel)
	}
}

// TestScanTurnMetadata locks the D1 per-turn dimensions the ape.metrics
// payload needs: chronological first/last turn timestamps (of the kept
// snapshots) and the Claude Code version.
func TestScanTurnMetadata(t *testing.T) {
	res, err := ScanSession(filepath.Join("testdata", "session-streaming.jsonl"))
	if err != nil {
		t.Fatalf("ScanSession: %v", err)
	}
	// msg_stream's kept turn is the FINAL fragment (10:00:04), so that is
	// the earliest surviving turn; msg_two is the latest (10:00:06).
	wantFirst := time.Date(2026, 7, 15, 10, 0, 4, 0, time.UTC)
	wantLast := time.Date(2026, 7, 15, 10, 0, 6, 0, time.UTC)
	if !res.FirstTurnAt.Equal(wantFirst) {
		t.Errorf("FirstTurnAt = %v, want %v", res.FirstTurnAt, wantFirst)
	}
	if !res.LastTurnAt.Equal(wantLast) {
		t.Errorf("LastTurnAt = %v, want %v", res.LastTurnAt, wantLast)
	}
	if res.ClaudeVersion != "2.1.201" {
		t.Errorf("ClaudeVersion = %q, want 2.1.201 (last turn)", res.ClaudeVersion)
	}
	if len(res.Turns) != 2 {
		t.Fatalf("Turns len = %d, want 2", len(res.Turns))
	}
	if res.Turns[0].StopReason != "end_turn" {
		t.Errorf("kept msg_stream turn StopReason = %q, want end_turn (final snapshot)", res.Turns[0].StopReason)
	}
}

// TestStreamingRepriceRoundTrip is the PLAN-17 acceptance in miniature:
// the per-model token counts, repriced with the date-aware table at the
// session date, equal the scanner's own cost_usd (self-consistency).
func TestStreamingRepriceRoundTrip(t *testing.T) {
	res, err := ScanSession(filepath.Join("testdata", "session-streaming.jsonl"))
	if err != nil {
		t.Fatalf("ScanSession: %v", err)
	}
	var repriced float64
	for model, tot := range res.ByModel {
		price, ok := LookupAt(model, res.FirstTurnAt)
		if !ok {
			t.Fatalf("model %q unpriced", model)
		}
		repriced += TurnCost(UsageBlock{
			InputTokens:  tot.InputTokens,
			OutputTokens: tot.OutputTokens,
			CacheRead:    tot.CacheReadTokens,
			CacheCreation: CacheCreation{
				Ephemeral5m: tot.CacheCreation5mTokens,
				Ephemeral1h: tot.CacheCreation1hTokens,
			},
		}, price)
	}
	if !floatEq(repriced, res.Totals.CostUSD) {
		t.Fatalf("repriced %.10f != scanner cost_usd %.10f", repriced, res.Totals.CostUSD)
	}
	// Sonnet 5 on 2026-07-15 is inside the intro window ($2/$10):
	// input 120 × $2/1M + output 530 × $10/1M.
	wantIntro := 120*2.0/1_000_000 + 530*10.0/1_000_000
	if !floatEq(res.Totals.CostUSD, wantIntro) {
		t.Fatalf("cost_usd %.10f != intro-priced %.10f (date-aware D3 not applied?)", res.Totals.CostUSD, wantIntro)
	}
}

func floatEq(a, b float64) bool {
	const eps = 1e-9
	d := a - b
	return d < eps && d > -eps
}
