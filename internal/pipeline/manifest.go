package pipeline

import (
	"time"
)

// ManifestSchemaVersion is the current on-disk schema for run manifests.
// Bumped on any backward-incompatible change. Consumers should reject
// manifests whose schema_version differs from a version they understand.
const ManifestSchemaVersion = 1

// RunStatus enumerates terminal pipeline / stage / step states.
type RunStatus string

const (
	StatusRunning   RunStatus = "running"
	StatusCompleted RunStatus = "completed"
	StatusFailed    RunStatus = "failed"
	StatusCancelled RunStatus = "cancelled"
)

// Manifest is the canonical on-disk record of one ape pipeline run.
// It is written to <project_root>/_output/pipelines/<name>/<run_id>/manifest.yaml.
// The eval reads this artifact (apex_process_framework_eval PLAN-9).
//
// YAML field names are snake_case (not camelCase) by design: the on-disk
// schema is the external contract for the eval consumer and for humans
// reading the file directly, and snake_case matches the project's other
// on-disk YAMLs (config.yaml, pipelines/*.yaml). The .golangci.yaml
// exclusion rule for internal/pipeline/(manifest|result_event).go
// covers tagliatelle on every field below.
type Manifest struct {
	SchemaVersion int            `yaml:"schema_version"`
	ApeVersion    string         `yaml:"ape_version"`
	Pipeline      Ref            `yaml:"pipeline"`
	ProjectRoot   string         `yaml:"project_root"`
	RunID         string         `yaml:"run_id"`
	StartedAt     time.Time      `yaml:"started_at"`
	EndedAt       time.Time      `yaml:"ended_at,omitempty"`
	DurationSecs  float64        `yaml:"duration_seconds"`
	Status        RunStatus      `yaml:"status"`
	Totals        ManifestTotals `yaml:"totals"`
	Stages        []StageRecord  `yaml:"stages"`
}

// Ref identifies the pipeline a run was executed against.
type Ref struct {
	Name   string `yaml:"name"`
	Source string `yaml:"source"`
	Digest string `yaml:"digest"`
}

// ManifestTotals aggregates per-step cost / tokens across the whole run.
type ManifestTotals struct {
	CostUSD             float64 `yaml:"cost_usd"`
	TokensInput         int     `yaml:"tokens_input"`
	TokensOutput        int     `yaml:"tokens_output"`
	TokensCacheRead     int     `yaml:"tokens_cache_read"`
	TokensCacheCreation int     `yaml:"tokens_cache_creation"`
	StepsRun            int     `yaml:"steps_run"`
	StepsFailed         int     `yaml:"steps_failed"`
}

// StageRecord captures one stage's lifecycle.
type StageRecord struct {
	Index        int          `yaml:"index"`
	Name         string       `yaml:"name"`
	StartedAt    time.Time    `yaml:"started_at"`
	EndedAt      time.Time    `yaml:"ended_at,omitempty"`
	DurationSecs float64      `yaml:"duration_seconds"`
	Status       RunStatus    `yaml:"status"`
	Steps        []StepRecord `yaml:"steps"`
}

// StepRecord captures one step's metrics. Numeric fields default to
// zero if the terminal `result` event was missing or unparseable; status
// reflects the exit / parse outcome regardless.
type StepRecord struct {
	Index               int       `yaml:"index"`
	Skill               string    `yaml:"skill"`
	Agent               string    `yaml:"agent,omitempty"`
	Args                string    `yaml:"args,omitempty"`
	Prompt              string    `yaml:"prompt,omitempty"`
	Model               string    `yaml:"model,omitempty"`
	StartedAt           time.Time `yaml:"started_at"`
	EndedAt             time.Time `yaml:"ended_at,omitempty"`
	DurationSecs        float64   `yaml:"duration_seconds"`
	Status              RunStatus `yaml:"status"`
	ExitCode            int       `yaml:"exit_code"`
	CostUSD             float64   `yaml:"cost_usd"`
	TokensInput         int       `yaml:"tokens_input"`
	TokensOutput        int       `yaml:"tokens_output"`
	TokensCacheRead     int       `yaml:"tokens_cache_read"`
	TokensCacheCreation int       `yaml:"tokens_cache_creation"`
	NumTurns            int       `yaml:"num_turns"`
	EventsPath          string    `yaml:"events_path,omitempty"`
}
