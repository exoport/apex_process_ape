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
// the unchanged tokens_cache_creation sum); v0.0.60 added the per-step
// terminal-contract verdict (`contract`). The eval reader
// (apex_process_framework_eval) hard-rejects any schema_version outside
// [1,2] but tolerates unknown fields, so additive-under-v2 is the only
// eval-safe path — see PLAN-10 D5.
//
// Verified for the v0.0.60 addition, so nobody bumps defensively: the eval
// builds its StepRecord from explicit key lookups on a plain dict and
// drops what it does not name, and nothing in ape decodes a manifest with
// yaml.Decoder.KnownFields(true). A new field is invisible to both. A
// version BUMP, by contrast, would fail the eval's [1,2] range outright —
// it is the change to avoid, not the field.
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
// It is written to <project_root>/_output/ape/pipelines/<name>/<run_id>/manifest.yaml.
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
	ClaudeVersion string    `yaml:"claude_version,omitempty"`
	Pipeline      Ref       `yaml:"pipeline"`
	ProjectRoot   string    `yaml:"project_root"`
	RunID         string    `yaml:"run_id"`
	StartedAt     time.Time `yaml:"started_at"`
	// Timestamp is the framework's `timestamp` variable for this run —
	// local wall-clock `YYYYMMDDHHMMSS`, issued by internal/stamp and
	// therefore monotonic across processes.
	//
	// It is not a redundant StartedAt. StartedAt is an RFC-3339 instant
	// for telemetry and may move backwards on a skewed clock; this is the
	// value the framework's record fields are written from, and it never
	// does. Empty when the run had no project to issue against.
	Timestamp    string         `yaml:"timestamp,omitempty"`
	EndedAt      time.Time      `yaml:"ended_at,omitempty"`
	DurationSecs float64        `yaml:"duration_seconds"`
	Status       RunStatus      `yaml:"status"`
	Totals       ManifestTotals `yaml:"totals"`
	Stages       []StageRecord  `yaml:"stages"`

	// TranscriptBlobs maps each uploaded transcript's file base name to its
	// content-addressed reference (PLAN-13 D3). Empty/absent when transcript
	// upload was off or failed. Additive under schema_version 2 — a consumer
	// that doesn't know the field ignores it.
	TranscriptBlobs map[string]TranscriptBlob `yaml:"transcript_blobs,omitempty"`
	// UploadStatus records the outcome of the transcript upload:
	// "ok" | "partial" | "failed" (empty when upload was not attempted).
	UploadStatus string `yaml:"upload_status,omitempty"`

	// CommitContract is the per-dispatch commit-ownership verdict, for
	// the run kinds that assert one (`ape task`).
	//
	// It lives here as well as on the JSON envelope because the envelope
	// is ephemeral: a consumer that parses it, sees failure and discards
	// stdout leaves nothing on disk saying WHY the dispatch was judged to
	// have failed. That is not hypothetical — it is how an 89-minute
	// capture was discarded with the cause unrecoverable from the
	// artifacts. The manifest is the durable record, so the verdict
	// belongs in it.
	//
	// Additive under schema_version 2; absent on runs that assert no
	// contract.
	CommitContract *CommitContractRecord `yaml:"commit_contract,omitempty"`
}

// CommitContractRecord is the manifest's copy of the commit-ownership
// verdict. A local shape rather than internal/commitowners' own, so the
// on-disk schema does not move whenever that package's Go types do —
// the manifest is an external contract read by the eval.
type CommitContractRecord struct {
	Skill string `yaml:"skill"`
	// Declared reports whether the project's declaration lists this
	// skill, i.e. which of the two assertions ran.
	Declared bool `yaml:"declared"`
	// Asserted is false when neither assertion could run. A skipped
	// check is never a pass, so a reader must be able to tell them apart.
	Asserted bool `yaml:"asserted"`
	// SkipReason says why, when Asserted is false.
	SkipReason string `yaml:"skip_reason,omitempty"`
	// OK is the verdict when Asserted.
	OK bool `yaml:"ok"`
	// Violations are the failed assertions, `<check>: <message>` each.
	Violations []string `yaml:"violations,omitempty"`
	// Subjects are the commit subjects observed across the dispatch.
	Subjects []string `yaml:"subjects,omitempty"`
}

// TranscriptBlob is one uploaded transcript's content-addressed reference.
type TranscriptBlob struct {
	SessionID string `yaml:"session_id,omitempty"`
	Digest    string `yaml:"digest"`
	URI       string `yaml:"uri,omitempty"`
	Bytes     int64  `yaml:"bytes,omitempty"`
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
	// CommitsMade counts APE'S OWN boundary commits — per-step commits
	// and `ape task --task-commit`. It does NOT count commits the
	// dispatched skill made itself.
	//
	// So `commits_made: 0` on a run whose skill committed six times is
	// correct and expected, not a smoking gun. Spelled out because it has
	// already cost one person a detour while diagnosing a dispatch that
	// exited non-zero: the skill's own commits are in `git log`, and the
	// verdict on them is `commit_contract` below.
	CommitsMade int `yaml:"commits_made"`
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
	// ContextWindow is this model's usable context in tokens, from ape's
	// maintained table — NOT a value Claude Code reported. Omitted when the
	// table has no window for the model, which means UNKNOWN and must not be
	// read as a default.
	//
	// Resolved from the RAW model spelling the transcript recorded, before
	// normalization folds a model and its context variant into one pricing
	// bucket — so a `[1m]` step reports 1M here, not the base model's
	// window. Keyed by the normalized (pricing) id all the same: the fold
	// is correct for cost and wrong for context, and ape now keeps both.
	//
	// One bucket CAN hold turns of two different sizes — a step's
	// sub-agents need not run the spelling the step was spawned with — and
	// no single number is right for it then. That case records nothing
	// (unknown) rather than picking the side that is wrong by 5x.
	ContextWindow int `yaml:"context_window,omitempty"`
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
	Index  int    `yaml:"index"`
	Skill  string `yaml:"skill"`
	Agent  string `yaml:"agent,omitempty"`
	Args   string `yaml:"args,omitempty"`
	Prompt string `yaml:"prompt,omitempty"`
	// Model is what the step's session actually ran on — its stage's
	// launch model. ModelDeclared appears only when the spec asked for
	// a different one, which a running session cannot switch to; see
	// Spec.StageModelConflicts.
	Model                 string       `yaml:"model,omitempty"`
	ModelDeclared         string       `yaml:"model_declared,omitempty"`
	Effort                string       `yaml:"effort,omitempty"`
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
	// Contract is the PLAN-25 Gate C verdict for this step:
	// `present` | `missing` | `no-transcript`. Absent when the step's
	// skill declares no terminal contract in the framework's
	// `_apex/terminal-contracts.csv`, and absent on every manifest
	// written before ape v0.0.60.
	//
	// HOW TO READ IT — this is telemetry about TEXT, not about work.
	//
	// The check matches the closing assistant message of the step's MAIN
	// claude session against the framework's per-skill pattern. That
	// message is a RELAY whenever anything below it did the work, and for
	// the population this table enrols — batch skills — something below it
	// usually did:
	//
	//   - under `--agent` the runner types
	//     `/<agent> --autonomous -- <skill> …`, so the agent skill closes
	//     the session and the sub-skill's block reaches the transcript
	//     only if the agent relayed it verbatim;
	//   - with no agent at all, a batch skill that fans out to Agent-tool
	//     sub-agents has the same shape — the subs' own transcripts are
	//     separate files (see Sessions above), and the main session's
	//     closing message summarises them.
	//
	// So `missing` means "the closing text did not match", NOT "the skill
	// failed to emit its contract". Treated as the latter it conflates a
	// real framework-side quality problem with a relay artifact — which is
	// precisely what the warn-only release exists to avoid deciding on.
	// Any exit-code decision taken from this field has to separate the two
	// first.
	//
	// Two further limits on the denominator:
	//
	//   - only COMPLETED steps have a record at all. A step whose
	//     WaitStepDone failed (idle timeout, detached agent, dead session)
	//     never reaches recordStep, so it carries no verdict either way;
	//   - a `no_clear: true` step shares the previous step's transcript.
	//     If it added no assistant turn of its own, the closing message
	//     read is the PREVIOUS step's — which can record a `present` that
	//     belongs to its predecessor.
	Contract string `yaml:"contract,omitempty"`
	// ContextWindow is the usable context, in tokens, of the model this
	// step was SPAWNED with — the denominator for an occupancy ratio.
	// Omitted when ape's table has no window for that model, which means
	// UNKNOWN: render "could not look", never a default.
	//
	// It is a MAINTAINED value, not a measurement. Claude Code reports the
	// real per-model window only on the stream-json result event's
	// `modelUsage[].contextWindow`; ape is PTY-only by design and does not
	// use that surface, so the reported window cannot reach ape on any path
	// ape drives. Do not label this as reported.
	//
	// Resolved from the step's effective `--model`, so it honours a `[1m]`
	// suffix. As of v0.0.60 the per-model entries do too — their window is
	// read from the raw transcript spelling before normalization — so the
	// two agree on an ordinary step and neither is a trap.
	//
	// This one is still the better divisor for a STEP-level ratio, for a
	// reason that has nothing to do with suffixes: a step whose sub-agents
	// ran other models has several per-model entries and no single one of
	// them is the step's window. Prefer a per-model entry only when you are
	// computing a ratio for that model's share.
	ContextWindow int `yaml:"context_window,omitempty"`
}
