package pipeline

// resultEvent is the internal per-step telemetry shape fed through
// recordStep to the manifest writer. It originated as a mirror of the
// terminal `{"type":"result", ...}` payload that `claude -p
// --output-format stream-json` emitted, but ape is PTY-only since
// v0.0.36 (PLAN-9 F2 removed the programmatic exec path), so the only
// producer now is stepTelemetryToResultEvent, which populates it from a
// transcript scan (StepTelemetry). No wire (un)marshalling remains — the
// struct is a plain in-memory adapter keeping recordStep's signature
// stable.
type resultEvent struct {
	Type         string
	Subtype      string
	NumTurns     int
	TotalCostUSD float64
	Usage        struct {
		InputTokens              int
		OutputTokens             int
		CacheReadInputTokens     int
		CacheCreationInputTokens int
	}

	// Interactive-mode extras carried from StepTelemetry (transcript scan).
	ModelUsage    map[string]ModelUsage
	Sessions      []SessionUsage
	TelemetryNote string
}
