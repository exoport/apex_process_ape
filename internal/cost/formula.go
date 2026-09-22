package cost

// UsageBlock mirrors the `usage` field on an assistant message line
// in the Claude session JSONL. Fields that may be absent in older
// session captures default to zero — that is the same shape PLAN-3
// already records via the stream-json `result` path.
type UsageBlock struct {
	InputTokens   int           `json:"input_tokens"`
	OutputTokens  int           `json:"output_tokens"`
	CacheRead     int           `json:"cache_read_input_tokens"`
	CacheCreation CacheCreation `json:"cache_creation"`
}

type CacheCreation struct {
	Ephemeral5m int `json:"ephemeral_5m_input_tokens"`
	Ephemeral1h int `json:"ephemeral_1h_input_tokens"`
}

// Per-million scaling factor — Anthropic publishes prices per 1M tokens,
// so the formula divides token counts by perMillion before multiplying.
const perMillion = 1_000_000.0

// Cache-multiplier constants from design doc §11. Cache-creation
// pricing is a fixed multiple of base input; cache-read is also a
// multiple of base input (the "read while cached" path is cheaper
// than fresh input).
const (
	CacheCreationEphemeral5mMul = 1.25
	CacheCreationEphemeral1hMul = 2.00
	// DefaultCacheReadMul is the cache-read multiple for every model that
	// does not publish its own. It is NOT universal: Claude Opus 5.5 reads
	// at 0.05x and Claude Fable 5.1 / Mythos 5.1 at 0.025x, carried per
	// model on ModelPrice.CacheReadMul. This was a single global constant
	// applied to everything, which priced Opus 5.5 cache reads 2x high and
	// Fable 5.1's 4x.
	DefaultCacheReadMul = 0.10
)

// TurnCost returns USD for one assistant turn given its usage block
// and the model's price record. PLAN-5 / C7 formula:
//
//	turn_cost = BaseInput × input_tokens
//	          + BaseInput × 1.25 × cache_creation.ephemeral_5m_input_tokens
//	          + BaseInput × 2.00 × cache_creation.ephemeral_1h_input_tokens
//	          + BaseInput × cacheReadMul × cache_read_input_tokens
//	          + Output    × output_tokens
//
// cacheReadMul is the MODEL's, not a constant: 0.10 for most, 0.05 on
// Claude Opus 5.5, 0.025 on Claude Fable 5.1 / Mythos 5.1.
//
// All terms divided by 1M so the per-million-token price table can be
// used directly. Unknown models (zero ModelPrice) yield $0.00 with no
// error — Tracker decides whether to stamp a `cost_note`.
func TurnCost(u UsageBlock, p ModelPrice) float64 {
	return p.BaseInput*float64(u.InputTokens)/perMillion +
		p.BaseInput*CacheCreationEphemeral5mMul*float64(u.CacheCreation.Ephemeral5m)/perMillion +
		p.BaseInput*CacheCreationEphemeral1hMul*float64(u.CacheCreation.Ephemeral1h)/perMillion +
		p.BaseInput*p.CacheReadMultiplier()*float64(u.CacheRead)/perMillion +
		p.Output*float64(u.OutputTokens)/perMillion
}

// Totals aggregates multiple TurnCosts plus their token counts so the
// caller can populate PLAN-3's v2 manifest fields (cost_usd, tokens_*).
// NumTurns is incremented once per Add call so callers can fill the
// num_turns manifest field from transcript scans (the stream-json
// terminal `result` event has the same count for one-shot `claude -p`
// spawns; transcript scans of an interactive session need the explicit
// counter).
type Totals struct {
	CostUSD             float64 `json:"cost_usd"`
	InputTokens         int     `json:"input_tokens"`
	OutputTokens        int     `json:"output_tokens"`
	CacheReadTokens     int     `json:"cache_read_tokens"`
	CacheCreationTokens int     `json:"cache_creation_tokens"`
	// CacheCreation5mTokens / CacheCreation1hTokens are the ephemeral
	// cache-write split (PLAN-10 D1). CacheCreationTokens stays the sum
	// of the two, so existing consumers are unaffected; the granular
	// fields are additive. The 5m/1h tiers price differently (1.25× vs
	// 2.00× base input — see the multiplier constants).
	CacheCreation5mTokens int `json:"cache_creation_5m_tokens"`
	CacheCreation1hTokens int `json:"cache_creation_1h_tokens"`
	NumTurns              int `json:"num_turns"`
}

// Add folds one turn's usage + cost into the running totals.
func (t *Totals) Add(u UsageBlock, p ModelPrice) {
	t.CostUSD += TurnCost(u, p)
	t.InputTokens += u.InputTokens
	t.OutputTokens += u.OutputTokens
	t.CacheReadTokens += u.CacheRead
	t.CacheCreationTokens += u.CacheCreation.Ephemeral5m + u.CacheCreation.Ephemeral1h
	t.CacheCreation5mTokens += u.CacheCreation.Ephemeral5m
	t.CacheCreation1hTokens += u.CacheCreation.Ephemeral1h
	t.NumTurns++
}
