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
//nolint:tagliatelle // snake_case is the record's contract, in YAML and JSON alike
type PromptModelUsage struct {
	CostUSD             float64 `json:"cost_usd"              yaml:"cost_usd"`
	TokensInput         int     `json:"tokens_input"          yaml:"tokens_input"`
	TokensOutput        int     `json:"tokens_output"         yaml:"tokens_output"`
	TokensCacheRead     int     `json:"tokens_cache_read"     yaml:"tokens_cache_read"`
	TokensCacheCreation int     `json:"tokens_cache_creation" yaml:"tokens_cache_creation"`
	NumTurns            int     `json:"num_turns"             yaml:"num_turns"`
}

// PromptMeta is the session record written to prompt.yaml when an
// `ape prompt` run ends (PLAN-12). Prompt sessions are not pipelines, so
// there is no PLAN-3 manifest equivalent, but the record carries a status
// and a per-model breakdown so `ape costs` can attribute them. The
// harness version is not here: it is stamped for every run kind alike in
// harness.yaml, written by the Writer itself.
//
//nolint:tagliatelle // snake_case matches the session-record on-disk contract
//nolint:tagliatelle // snake_case is the record's contract, in YAML and JSON alike
type PromptMeta struct {
	PromptID  string    `json:"prompt_id"            yaml:"prompt_id"`
	StartedAt time.Time `json:"started_at"           yaml:"started_at"`
	EndedAt   time.Time `json:"ended_at"             yaml:"ended_at"`
	Status    string    `json:"status"               yaml:"status"`
	Agent     string    `json:"agent,omitempty"      yaml:"agent,omitempty"`
	Model     string    `json:"model,omitempty"      yaml:"model,omitempty"`
	SessionID string    `json:"session_id,omitempty" yaml:"session_id,omitempty"`
	// Effort and EffortSource are the session's effort and where it came
	// from (internal/effort: flag, table or legacy-default). Under the
	// table with no --model, Effort is the row of the model the main
	// session ran on, not the fallback assumed at launch.
	Effort       string `json:"effort,omitempty"        yaml:"effort,omitempty"`
	EffortSource string `json:"effort_source,omitempty" yaml:"effort_source,omitempty"`
	// TranscriptPath is the session's own transcript, as its hooks named it.
	TranscriptPath string                      `json:"transcript_path,omitempty" yaml:"transcript_path,omitempty"`
	CostUSD        float64                     `json:"cost_usd"                  yaml:"cost_usd"`
	TokensIn       int                         `json:"tokens_input"              yaml:"tokens_input"`
	TokensOut      int                         `json:"tokens_output"             yaml:"tokens_output"`
	NumTurns       int                         `json:"num_turns"                 yaml:"num_turns"`
	PerModel       map[string]PromptModelUsage `json:"per_model,omitempty"       yaml:"per_model,omitempty"`
	// ArgsFile is set when the delivered text was too long to type and
	// went to a file in the run directory instead (PromptArgsFile); the
	// typed line named that file.
	ArgsFile string `json:"args_file,omitempty" yaml:"args_file,omitempty"`
}

// WritePromptYAML emits prompt.yaml at <dir>/prompt.yaml.
func WritePromptYAML(dir string, m PromptMeta) error {
	// Normalize the timestamps to UTC RFC3339 for a stable, comparable
	// on-disk shape.
	m.StartedAt = m.StartedAt.UTC().Truncate(time.Second)
	m.EndedAt = m.EndedAt.UTC().Truncate(time.Second)
	bs, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "prompt.yaml"), bs, 0o644) //nolint:gosec // user-visible runlog metadata; world-readable is intentional
}
