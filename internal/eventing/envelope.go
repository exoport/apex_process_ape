package eventing

// SchemaVersion is the payload envelope version. Bump only for a breaking
// payload change (and document it in docs/reference/events.md); subject
// segments and new fields are additive and never bump this.
const SchemaVersion = 1

// User is the identity block embedded in every payload for traceability
// independent of the subject. Both fields come from the NATS credential's
// decoded JWT (natsconn.Identity).
type User struct {
	Name      string `json:"name,omitempty"`
	PublicKey string `json:"public_key,omitempty"`
}

// ModelMetrics is one model's usage share. Field names match the
// ape.metrics per_model shape so a consumer reprices step-end and metrics
// payloads with one code path.
type ModelMetrics struct {
	CostUSD              float64 `json:"cost_usd"`
	InputTokens          int     `json:"input_tokens"`
	OutputTokens         int     `json:"output_tokens"`
	CacheReadInputTokens int     `json:"cache_read_input_tokens"`
	CacheCreation5m      int     `json:"cache_creation_5m"`
	CacheCreation1h      int     `json:"cache_creation_1h"`
	Turns                int     `json:"turns"`
}

// StepMetrics is a step's transcript-derived telemetry (PLAN-10), carried
// on step-end.
type StepMetrics struct {
	CostUSD             float64                 `json:"cost_usd"`
	TokensInput         int                     `json:"tokens_input"`
	TokensOutput        int                     `json:"tokens_output"`
	TokensCacheRead     int                     `json:"tokens_cache_read"`
	TokensCacheCreation int                     `json:"tokens_cache_creation"`
	NumTurns            int                     `json:"num_turns"`
	PerModel            map[string]ModelMetrics `json:"per_model,omitempty"`
}

// RunTotals is the manifest totals block carried on run-end.
type RunTotals struct {
	CostUSD             float64 `json:"cost_usd"`
	TokensInput         int     `json:"tokens_input"`
	TokensOutput        int     `json:"tokens_output"`
	TokensCacheRead     int     `json:"tokens_cache_read"`
	TokensCacheCreation int     `json:"tokens_cache_creation"`
	NumTurns            int     `json:"num_turns"`
	StepsRun            int     `json:"steps_run"`
	StepsFailed         int     `json:"steps_failed"`
	CommitsMade         int     `json:"commits_made"`
}

// TranscriptBlob is one uploaded transcript's content-addressed reference,
// carried in the run-end transcript_blobs map (keyed by file base name).
type TranscriptBlob struct {
	SessionID string `json:"session_id,omitempty"`
	Digest    string `json:"digest"`
	URI       string `json:"uri,omitempty"`
	Bytes     int64  `json:"bytes,omitempty"`
}
