package pipeline

import (
	"time"
)

// ManifestSchemaVersion is the current on-disk schema for run manifests.
// Bumped on any backward-incompatible change. Consumers should reject
// manifests whose schema_version differs from a version they understand.
//
// History:
//   - v1 (ape v0.0.9) — initial PLAN-3 manifest with per-step metrics.
//   - v2 (ape v0.0.10) — PLAN-4 commit fields on StepRecord
//     (commit_sha, commit_message, commit_status, commit_error) plus
//     totals.commits_made.
//
// Later additions stay ADDITIVE under v2 (new fields, no version bump):
// v0.0.27–v0.0.35 added num_turns, model_usage, sessions[]; v0.0.37 added
// the ephemeral cache-write split (tokens_cache_creation_5m/_1h alongside
// the unchanged tokens_cache_creation sum). The eval reader
// (apex_process_framework_eval) hard-rejects any schema_version outside
// [1,2] but tolerates unknown fields, so additive-under-v2 is the only
// eval-safe path — see PLAN-10 D5.
const ManifestSchemaVersion = 2

// RunStatus enumerates terminal pipeline / stage / step states.
type RunStatus string

const (
	StatusRunning   RunStatus = "running"
	StatusCompleted RunStatus = "completed"
	StatusFailed    RunStatus = "failed"
	StatusCancelled RunStatus = "cancelled"
)

// CommitStatus enumerates per-step commit outcomes recorded in the
// manifest. The set is closed; v2 readers should treat any unknown
// value as opaque (forward-compatible with future ape additions).
type CommitStatus string

const (
	// CommitStatusCommitted — git commit succeeded; commit_sha is set.
	CommitStatusCommitted CommitStatus = "committed"
	// CommitStatusNoOp — would have committed but `git status --porcelain`
	// was empty (step produced no diff).
	CommitStatusNoOp CommitStatus = "no-op"
	// CommitStatusSkippedByFlag — pipeline-level `--no-commit` was set.
	CommitStatusSkippedByFlag CommitStatus = "skipped-by-flag"
	// CommitStatusSkippedBySpec — pipeline YAML had `commit: false` for this step.
	CommitStatusSkippedBySpec CommitStatus = "skipped-by-spec"
	// CommitStatusSkippedStepFailed — the underlying step exited non-zero;
	// no commit attempted.
	CommitStatusSkippedStepFailed CommitStatus = "skipped-step-failed"
	// CommitStatusSkippedCancelled — context was cancelled before / during the step.
	CommitStatusSkippedCancelled CommitStatus = "skipped-cancelled"
	// CommitStatusFailed — git commit invocation returned non-zero; commit_error
	// carries the captured stderr.
	CommitStatusFailed CommitStatus = "failed"
	// CommitStatusDeferredToStage — step ran inside a stage-boundary stage
	// (PLAN-6 / C2 stage-level `commit:`). Its diff is folded into the
	// stage-end commit attributed to the last step in the chain. Recorded
	// on every step except the last; the last step gets the actual
	// outcome (committed / no-op / failed) after the stage-end commit runs.
	CommitStatusDeferredToStage CommitStatus = "deferred-to-stage"
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
	SchemaVersion int    `yaml:"schema_version"`
	ApeVersion    string `yaml:"ape_version"`
	// ClaudeVersion is the resolved `claude --version` output at run
	// start (best-effort; empty when unavailable). claude-code
	// auto-updates silently and its trust-dialog / transcript behavior
	// shifts across versions — telemetry and repro must be
	// attributable to the exact version that ran.
	ClaudeVersion string         `yaml:"claude_version,omitempty"`
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
	// TokensCacheCreation5m / 1h are the ephemeral cache-write split
	// (PLAN-10 D1). Additive fields — schema stays v2; TokensCacheCreation
	// remains the sum of the two, so v2 readers are unaffected.
	TokensCacheCreation5m int `yaml:"tokens_cache_creation_5m"`
	TokensCacheCreation1h int `yaml:"tokens_cache_creation_1h"`
	NumTurns              int `yaml:"num_turns"`
	StepsRun              int `yaml:"steps_run"`
	StepsFailed           int `yaml:"steps_failed"`
	CommitsMade           int `yaml:"commits_made"`
	// ModelUsage is the run-level per-model breakdown, summed across
	// steps. Additive field (schema stays v2 — v2 readers ignore it).
	ModelUsage map[string]ModelUsageRecord `yaml:"model_usage,omitempty"`
}

// ModelUsageRecord is the on-disk shape of one model's (or one
// session's) usage share. Interactive runs derive it from the
// transcript scan; programmatic runs don't populate it.
// Field order must stay identical to pipeline.ModelUsage — runner.go's
// modelUsageToRecords relies on a direct ModelUsageRecord(u) conversion.
type ModelUsageRecord struct {
	CostUSD               float64 `yaml:"cost_usd"`
	TokensInput           int     `yaml:"tokens_input"`
	TokensOutput          int     `yaml:"tokens_output"`
	TokensCacheRead       int     `yaml:"tokens_cache_read"`
	TokensCacheCreation   int     `yaml:"tokens_cache_creation"`
	TokensCacheCreation5m int     `yaml:"tokens_cache_creation_5m"`
	TokensCacheCreation1h int     `yaml:"tokens_cache_creation_1h"`
	NumTurns              int     `yaml:"num_turns"`
}

// SessionUsageRecord is one claude session's usage within a step: the
// step's main REPL session or a sub-agent (Agent tool) session.
type SessionUsageRecord struct {
	SessionID       string                      `yaml:"session_id"`
	ParentSessionID string                      `yaml:"parent_session_id,omitempty"`
	CostUSD         float64                     `yaml:"cost_usd"`
	TokensInput     int                         `yaml:"tokens_input"`
	TokensOutput    int                         `yaml:"tokens_output"`
	NumTurns        int                         `yaml:"num_turns"`
	ModelUsage      map[string]ModelUsageRecord `yaml:"model_usage,omitempty"`
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
	Index                 int          `yaml:"index"`
	Skill                 string       `yaml:"skill"`
	Agent                 string       `yaml:"agent,omitempty"`
	Args                  string       `yaml:"args,omitempty"`
	Prompt                string       `yaml:"prompt,omitempty"`
	Model                 string       `yaml:"model,omitempty"`
	StartedAt             time.Time    `yaml:"started_at"`
	EndedAt               time.Time    `yaml:"ended_at,omitempty"`
	DurationSecs          float64      `yaml:"duration_seconds"`
	Status                RunStatus    `yaml:"status"`
	ExitCode              int          `yaml:"exit_code"`
	CostUSD               float64      `yaml:"cost_usd"`
	TokensInput           int          `yaml:"tokens_input"`
	TokensOutput          int          `yaml:"tokens_output"`
	TokensCacheRead       int          `yaml:"tokens_cache_read"`
	TokensCacheCreation   int          `yaml:"tokens_cache_creation"`
	TokensCacheCreation5m int          `yaml:"tokens_cache_creation_5m"`
	TokensCacheCreation1h int          `yaml:"tokens_cache_creation_1h"`
	NumTurns              int          `yaml:"num_turns"`
	EventsPath            string       `yaml:"events_path,omitempty"`
	CommitSHA             string       `yaml:"commit_sha,omitempty"`
	CommitMessage         string       `yaml:"commit_message,omitempty"`
	CommitStatus          CommitStatus `yaml:"commit_status,omitempty"`
	CommitError           string       `yaml:"commit_error,omitempty"`
	// ModelUsage breaks the step's aggregate down per model
	// (interactive runs; transcript-derived).
	ModelUsage map[string]ModelUsageRecord `yaml:"model_usage,omitempty"`
	// Sessions carries per-claude-session usage: the step's main
	// session plus sub-agent sessions observed via SubagentStart/Stop.
	Sessions []SessionUsageRecord `yaml:"sessions,omitempty"`
	// TelemetryNote is a diagnosability breadcrumb explaining why the
	// numeric fields above are zero (transcript unavailable at scan
	// time, zero assistant turns, …). Empty on healthy steps.
	TelemetryNote string `yaml:"telemetry_note,omitempty"`
}
