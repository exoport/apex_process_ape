package runlog

import (
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// PromptModelUsage is one model's share of a prompt session, written to
// the per_model block of prompt.yaml. Field yaml tags match the cost
// package's modelUsageRecord so the rollup walker reads them directly.
//
//nolint:tagliatelle // snake_case matches the manifest/rollup on-disk contract
type PromptModelUsage struct {
	CostUSD             float64 `yaml:"cost_usd"`
	TokensInput         int     `yaml:"tokens_input"`
	TokensOutput        int     `yaml:"tokens_output"`
	TokensCacheRead     int     `yaml:"tokens_cache_read"`
	TokensCacheCreation int     `yaml:"tokens_cache_creation"`
	NumTurns            int     `yaml:"num_turns"`
}

// PromptMeta is the session record written to prompt.yaml when an
// `ape prompt` run ends (PLAN-12). It is the prompt analogue of
// SessionMeta (chats) — prompt sessions are not pipelines, so no PLAN-3
// manifest equivalent — but carries a status and a per-model breakdown
// so `ape costs` can attribute them.
//
//nolint:tagliatelle // snake_case matches the session-record on-disk contract
type PromptMeta struct {
	PromptID  string                      `yaml:"prompt_id"`
	StartedAt time.Time                   `yaml:"started_at"`
	EndedAt   time.Time                   `yaml:"ended_at"`
	Status    string                      `yaml:"status"`
	Agent     string                      `yaml:"agent,omitempty"`
	Model     string                      `yaml:"model,omitempty"`
	SessionID string                      `yaml:"session_id,omitempty"`
	CostUSD   float64                     `yaml:"cost_usd"`
	TokensIn  int                         `yaml:"tokens_input"`
	TokensOut int                         `yaml:"tokens_output"`
	NumTurns  int                         `yaml:"num_turns"`
	PerModel  map[string]PromptModelUsage `yaml:"per_model,omitempty"`
}

// WritePromptYAML emits prompt.yaml at <dir>/prompt.yaml.
func WritePromptYAML(dir string, m PromptMeta) error {
	// Normalize the timestamps to UTC RFC3339 for a stable, comparable
	// on-disk shape (matches WriteSessionYAML).
	m.StartedAt = m.StartedAt.UTC().Truncate(time.Second)
	m.EndedAt = m.EndedAt.UTC().Truncate(time.Second)
	bs, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "prompt.yaml"), bs, 0o644) //nolint:gosec // user-visible runlog metadata; world-readable is intentional
}
