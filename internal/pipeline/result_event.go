package pipeline

import (
	"encoding/json"
	"strings"
)

// resultEvent mirrors the terminal `{"type":"result", ...}` payload that
// claude --output-format stream-json emits at the end of every step.
// Field set is the intersection ape consumes; any additional fields in
// the on-wire payload are ignored. JSON tags use snake_case because the
// upstream wire format is snake_case — ape does not control it.
type resultEvent struct {
	Type         string  `json:"type"`
	Subtype      string  `json:"subtype"`
	DurationMS   int64   `json:"duration_ms"`
	NumTurns     int     `json:"num_turns"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	Usage        struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`

	// Interactive-mode extras carried from StepTelemetry (transcript
	// scan). Never present on the stream-json wire — json:"-" keeps
	// parseResultEvent honest about what claude actually emits.
	ModelUsage    map[string]ModelUsage `json:"-"`
	Sessions      []SessionUsage        `json:"-"`
	TelemetryNote string                `json:"-"`
}

// parseResultEvent scans the accumulated step output (concatenated
// stream-json lines, newline-delimited) for the terminal event with
// `"type":"result"` and decodes it. Returns nil when no such event is
// present — that's the degraded path where the manifest still writes
// but per-step metrics are zeroed.
//
// We scan from the end because the result event is by construction the
// last one claude emits; the first candidate that parses with
// type=="result" wins.
func parseResultEvent(output string) *resultEvent {
	if output == "" {
		return nil
	}
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if !strings.Contains(line, `"result"`) {
			continue
		}
		var ev resultEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.Type == "result" {
			return &ev
		}
	}
	return nil
}
