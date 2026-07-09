package reporting

import (
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/cost"
	"github.com/exoport/apex_process_ape/internal/eventing"
)

// MetricsPayload is the ape.metrics.<user>.<project>.<sid> payload's
// metric-specific fields (PLAN-17 D3). It carries per-model token counts +
// timestamps so a consumer can reprice against Claude Code API rates at any
// later moment (the "convert to API prices any moment" requirement): the
// per_model tokens times the date-aware price table equal cost_usd.
type MetricsPayload struct {
	DurationSeconds   float64
	NumTurns          int
	PerModel          map[string]eventing.ModelMetrics
	FirstTurnAt       string // RFC3339Nano, "" when unknown
	LastTurnAt        string
	ClaudeCodeVersion string
	RunID             string // set only in --run-id (manifest-totals) mode
}

// BuildMetrics maps a scanned session set onto the metrics payload. Pure
// and deterministic given the scan — the runner and the CLI both call it,
// which is what makes a supervised run and a standalone `ape metrics`
// byte-identical (PLAN-17 exit gate).
func BuildMetrics(scan cost.ScanResult, runID string) MetricsPayload {
	perModel := make(map[string]eventing.ModelMetrics, len(scan.ByModel))
	for model, t := range scan.ByModel {
		perModel[model] = eventing.ModelMetrics{
			CostUSD:              t.CostUSD,
			InputTokens:          t.InputTokens,
			OutputTokens:         t.OutputTokens,
			CacheReadInputTokens: t.CacheReadTokens,
			CacheCreation5m:      t.CacheCreation5mTokens,
			CacheCreation1h:      t.CacheCreation1hTokens,
			Turns:                t.NumTurns,
		}
	}
	var duration float64
	if !scan.FirstTurnAt.IsZero() && !scan.LastTurnAt.IsZero() {
		duration = scan.LastTurnAt.Sub(scan.FirstTurnAt).Seconds()
	}
	return MetricsPayload{
		DurationSeconds:   duration,
		NumTurns:          scan.Totals.NumTurns,
		PerModel:          perModel,
		FirstTurnAt:       rfc3339OrEmpty(scan.FirstTurnAt),
		LastTurnAt:        rfc3339OrEmpty(scan.LastTurnAt),
		ClaudeCodeVersion: scan.ClaudeVersion,
		RunID:             runID,
	}
}

// Metrics publishes a metrics snapshot on ape.metrics.<user>.<project>.<sid>.
func (r *Reporter) Metrics(sessionID string, p MetricsPayload) error {
	subject := strings.Join([]string{rootMetrics, r.userTok, r.project, tok(sessionID)}, ".")
	extra := map[string]any{
		"duration_seconds":    p.DurationSeconds,
		"num_turns":           p.NumTurns,
		"per_model":           p.PerModel,
		"first_turn_at":       p.FirstTurnAt,
		"last_turn_at":        p.LastTurnAt,
		"claude_code_version": p.ClaudeCodeVersion,
	}
	if p.RunID != "" {
		extra["run_id"] = p.RunID
	}
	return r.publish(subject, r.envelope(sessionID, extra))
}

func rfc3339OrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}
