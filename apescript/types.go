package apescript

import (
	"time"

	"github.com/exoport/apex_process_ape/internal/blobstore"
	"github.com/exoport/apex_process_ape/internal/cost"
	"github.com/exoport/apex_process_ape/internal/pipeline"
)

// Totals is one aggregate cost/token bucket. It aliases the internal cost
// shape so scripts read the same documented fields the manifest carries.
type Totals = cost.Totals

// ScanResult is the outcome of scanning one or more session transcripts —
// aggregate totals, per-model breakdown, and the per-turn records. It aliases
// the internal cost shape (PLAN-10).
type ScanResult = cost.ScanResult

// Manifest is the on-disk record of one ape run. It aliases the internal
// pipeline shape so scripts read the run's totals, stages, steps, sessions,
// and commit trail directly (PLAN-3/PLAN-4).
type Manifest = pipeline.Manifest

// Digest is a content-addressed blob reference ("algo:hex"). It aliases the
// internal blobstore shape (PLAN-13).
type Digest = blobstore.Digest

// RunResult is the outcome of a RunPipeline / RunTask / RunPrompt call. It is
// derived from the run's manifest (or prompt record) after the PTY session
// finishes.
type RunResult struct {
	// RunID is the run's identifier (the manifest run_id, or the prompt id).
	RunID string
	// ManifestPath is the absolute path to the run's manifest.yaml, or "" for
	// a prompt run (which writes prompt.yaml instead).
	ManifestPath string
	// Status is the terminal run status: "completed" | "failed" | "cancelled".
	Status string
	// CostUSD is the run's total attributed cost.
	CostUSD float64
	// PerModel is the per-model cost/token breakdown, keyed by normalized
	// model id. Nil when the run produced no telemetry.
	PerModel map[string]Totals
	// CommitSHAs are the full SHAs of commits the run produced (oldest first).
	CommitSHAs []string
	// Duration is the wall-clock time the run took.
	Duration time.Duration
}

// PipelineOpts configures a RunPipeline call. It mirrors `ape pipeline`.
type PipelineOpts struct {
	// Name is the pipeline to run (resolved from <project>/_apex/pipelines/).
	Name string
	// Prompt is forwarded to the pipeline's prompt_flag step, if any.
	Prompt string
	// From resumes the run at the named stage (empty runs from the start).
	From string
	// NoCommit tells the pipeline not to make boundary commits.
	NoCommit bool
	// Cwd overrides the project root. Empty uses the script's project root.
	Cwd string
}

// TaskOpts configures a RunTask call. It mirrors `ape task` (PLAN-11).
type TaskOpts struct {
	// Skill is the framework skill to run (required).
	Skill string
	// Agent is the framework agent fronting the skill (PAT-25 passthrough).
	Agent string
	// Model is the Claude model for the session (e.g. "opus[1m]").
	Model string
	// Args are verbatim skill args appended to the invocation.
	Args string
	// Prompt is forwarded via PromptFlag (same semantics as `ape task`).
	Prompt string
	// PromptFlag is the skill flag the Prompt value is forwarded through.
	PromptFlag string
	// NoCommit is the skill-layer no-commit (adds --no-commit on the agent path).
	NoCommit bool
	// TaskCommit, when non-empty, commits the whole task at the end with this
	// message (the task layer). Empty leaves the tree untouched by ape.
	TaskCommit string
	// AllowDirty bypasses the dirty-tree gate (relevant only with TaskCommit).
	AllowDirty bool
	// IdleTimeout overrides the idle-without-Stop backstop.
	IdleTimeout time.Duration
	// Cwd overrides the project root. Empty uses the script's project root.
	Cwd string
}

// PromptOpts configures a RunPrompt call. It mirrors `ape prompt` (PLAN-12).
type PromptOpts struct {
	// Text is the initial prompt (mutually exclusive with Handoff).
	Text string
	// Handoff seeds the session from a handoff document (mutually exclusive
	// with Text).
	Handoff string
	// Agent is the framework agent fronting the session.
	Agent string
	// Model is the Claude model for the session.
	Model string
	// Workflow appends a "run via a workflow" directive to the prompt.
	Workflow bool
	// Ultracode prepends the ultracode keyword to the prompt.
	Ultracode bool
	// IdleTimeout overrides the idle-without-Stop backstop.
	IdleTimeout time.Duration
	// Cwd overrides the project root. Empty uses the script's project root.
	Cwd string
}

// SkillInfo describes one resolved framework skill/agent available to a run.
type SkillInfo struct {
	// Name is the skill directory name (e.g. "apex-create-prd").
	Name string
	// Scope is "project" or "user" — which tree the skill resolved from.
	Scope string
	// Path is the absolute path to the skill's SKILL.md.
	Path string
	// Framework reports whether ape's framework manages the skill (apex-*).
	Framework bool
}
