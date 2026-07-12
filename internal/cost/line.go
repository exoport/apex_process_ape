package cost

// AssistantLine is the minimal shape scanTurns extracts from each JSONL
// row. Lines that don't have `type:"assistant"` are skipped.
//
// Message.ID is the dedupe key. Claude logs the same assistant message
// multiple times under distinct top-level `uuid` values when a tool
// turn re-renders or the conversation tree branches (each duplicate
// shares the same `message.id` but has a different `uuid`).
// Cost / token totals must count each ID once or the result is 2–4×
// over-counted; scanTurns filters by ID.
type AssistantLine struct {
	Type string `json:"type"`
	// PLAN-10 D1 per-turn fields. Timestamp/Version stay strings so a
	// malformed value can never fail the whole line's unmarshal and drop
	// its usage (the v0.0.28–32 zeroed-telemetry class of bug); the caller
	// parses Timestamp leniently. requestId + message.stop_reason drive the
	// H6 dedup (prefer the final, stop_reason-bearing snapshot of a
	// message.id). isMeta/isSidechain match the transcript's own flags.
	IsMeta      bool   `json:"isMeta"`
	IsSidechain bool   `json:"isSidechain"`
	RequestID   string `json:"requestId"`
	Timestamp   string `json:"timestamp"`
	Version     string `json:"version"`
	SessionID   string `json:"sessionId"`
	Message     struct {
		ID         string     `json:"id"`
		Model      string     `json:"model"`
		StopReason string     `json:"stop_reason"`
		Usage      UsageBlock `json:"usage"`
	} `json:"message"`
}
