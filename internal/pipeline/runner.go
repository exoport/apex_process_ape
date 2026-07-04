package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	flagAutonomous       = "--autonomous"
	flagOutputFormat     = "--output-format"
	flagVerbose          = "--verbose"
	flagOutputStreamJSON = "stream-json"
	defaultClaudeBin     = "claude"
)

// RunOptions controls a pipeline invocation.
type RunOptions struct {
	// ProjectRoot is the working directory of the user's project.
	// All claude invocations are spawned with this as cwd.
	ProjectRoot string

	// Prompt is forwarded to steps that reference {{ .Prompt }} in
	// their args_template (currently only apex-create-epics-and-stories).
	// Empty string means no prompt; the template's {{ if .Prompt }}
	// guard will then produce no flag.
	Prompt string

	// ClaudeBin overrides the claude executable path. Defaults to "claude"
	// (resolved via $PATH). Used by tests to swap in a shim.
	ClaudeBin string

	// PrependFlags is inserted into every claude invocation after
	// argv[0] and before --dangerously-skip-permissions. Used by
	// web mode to attach --strict-mcp-config + --mcp-config + --settings
	// without coupling the runner to the orchestrator package.
	// PLAN-5 / C1 + C3 (pipeline web mode).
	PrependFlags []string

	// OnStageStart / OnStageEnd are extra hooks alongside the
	// Observer interface. Web mode wires them to broker.Publish so
	// the SSE schema's stage-start / stage-end events fire. The
	// existing Observer continues to feed the TUI / plain printer.
	// Pass nil to skip web-side publishing.
	OnStageStart func(stage string)
	OnStageEnd   func(stage string, dur time.Duration, err error)

	// RunLog, when non-nil, captures per-run hook / call / checkpoint
	// streams alongside PLAN-3's manifest.yaml. Web mode opens one
	// at the resolved run-dir and passes it through. PLAN-5 / C6.
	RunLog RunLogger

	// OnRunDir fires once the manifest writer has resolved the run
	// directory but before any step runs. Web mode uses this to open
	// runlog.Writer alongside manifest.yaml. PLAN-5 / C6.
	OnRunDir func(dir string)

	// Observer receives lifecycle events. Nil is allowed — events are
	// dropped. The TUI installs an Observer that forwards events to a
	// Bubble Tea program; the plain printer installs one that writes
	// status lines to stdout.
	Observer Observer

	// ManifestDir overrides the root location for per-run manifest
	// artifacts. Defaults to <ProjectRoot>/_output/pipelines when empty.
	// PLAN-3 / M4.
	ManifestDir string

	// DisableManifest skips writing the run manifest entirely.
	// Production code never sets this; reserved for test paths that
	// don't want to litter the temp project tree.
	DisableManifest bool

	// ApeVersion is recorded in the manifest's ape_version field.
	// Callers should pass apecmd.Version; empty string falls back to "dev".
	ApeVersion string

	// NoCommit, when true, suppresses every per-step `git commit`.
	// Equivalent to the user passing `--no-commit` on the CLI. Wins
	// over any per-step `commit:` setting in the pipeline YAML.
	// PLAN-4 / C2.
	NoCommit bool

	// AllowDirty, when true, bypasses the pre-run dirty-tree gate.
	// The first committing step's diff will then include any prior
	// uncommitted changes the project tree had at runner start.
	// Meaningful only when NoCommit is false. PLAN-4 / C5.
	AllowDirty bool

	// FromStage, when non-empty, skips all stages before the named one.
	// The named stage and everything after it run normally. If the name
	// does not match any stage in the spec, Run returns an error before
	// any stage executes. CLI: --from <stage>.
	FromStage string

	// WaitStepDone is called between steps in interactive mode to
	// block until the model has finished responding to the step's
	// prompt. The apecmd wiring layer implements this by subscribing
	// to the bridge's Stop hook. Nil falls back to a fixed grace
	// window controlled by InteractiveStepGrace; meaningful only for
	// smoke tests that don't wire the bridge.
	WaitStepDone func(ctx context.Context, stage string, stepIdx int) error

	// InteractiveStepGrace is the duration the runner waits between
	// steps in interactive mode when WaitStepDone is nil. Default is
	// 2 seconds. Production callers should set WaitStepDone instead
	// of relying on this grace window.
	InteractiveStepGrace time.Duration

	// OnInteractiveStepStart fires before the interactive runner
	// types a step's slash-command prompt into the PTY. The apecmd
	// layer uses this to register a StepContract with the bridge
	// verifier so the next UserPromptSubmit hook can be matched
	// against the expected agent-prefix shape.
	OnInteractiveStepStart func(info InteractiveStepInfo)
	// OnInteractiveStepEnd fires after WaitStepDone returns for a
	// step. Used by apecmd to release the StepContract so a stray
	// late UserPromptSubmit doesn't match the previous step.
	OnInteractiveStepEnd func(info InteractiveStepInfo)

	// StepTelemetryFn, when set, is called after each interactive
	// step's WaitStepDone returns. The apecmd interactive core scans
	// the just-finished step's window of the claude session transcript
	// and returns the derived cost / tokens / num_turns. Programmatic
	// mode (claude -p) ignores this — its stream-json result event
	// already provides the same information directly via the
	// recordStep path. nil = no telemetry to record (manifest fields
	// stay zero).
	StepTelemetryFn func(stage string, stepIdx int) *StepTelemetry
}

// StepTelemetry is the apecmd-side computed telemetry for one
// interactive step, derived from the claude session transcript(s). The
// runner adapts it to the same shape the stream-json `result` event
// produces in programmatic mode, so the manifest writer's recordStep
// API stays uniform across exec modes.
type StepTelemetry struct {
	CostUSD               float64
	TokensInput           int
	TokensOutput          int
	TokensCacheRead       int
	TokensCacheCreation   int
	TokensCacheCreation5m int
	TokensCacheCreation1h int
	NumTurns              int
	// ModelUsage is the per-model breakdown of the aggregate above,
	// keyed by normalized model id. Restores sub-agent model
	// attribution (a consumer picking the non-primary model gets the
	// real sub-agent model, not the step's declared --model).
	ModelUsage map[string]ModelUsage
	// Sessions carries per-claude-session usage records: the step's
	// main session plus any sub-agent (Agent tool) sessions observed
	// via SubagentStart/Stop hooks.
	Sessions []SessionUsage
	// Note is a diagnosability breadcrumb stamped on the manifest when
	// telemetry could not be derived (transcript unavailable, zero
	// assistant turns). A zeroed step must be explainable, never
	// silent.
	Note string
}

// ModelUsage is one model's (or one session's) share of a step's
// usage. Field set mirrors StepTelemetry's aggregate.
// Field order must stay identical to ModelUsageRecord (manifest.go) —
// modelUsageToRecords relies on a direct ModelUsageRecord(u) conversion.
type ModelUsage struct {
	CostUSD               float64
	TokensInput           int
	TokensOutput          int
	TokensCacheRead       int
	TokensCacheCreation   int
	TokensCacheCreation5m int
	TokensCacheCreation1h int
	NumTurns              int
}

// SessionUsage is the usage of one claude session that contributed to
// a step: the main REPL session or a sub-agent session spawned by the
// Agent tool.
type SessionUsage struct {
	SessionID       string
	ParentSessionID string // empty for the step's main session
	Usage           ModelUsage
	ModelUsage      map[string]ModelUsage
}

// InteractiveStepInfo describes a single step in interactive mode for
// the OnInteractiveStepStart / End callbacks. The apecmd wiring layer
// translates this into a bridge orchestrator.StepContract.
type InteractiveStepInfo struct {
	Stage   string
	StepIdx int
	Skill   string
	Agent   string
	// Model is the model the step expects after a `/model` command;
	// empty means no model switch is expected at this step boundary.
	Model   string
	NoClear bool
}

// Observer hooks every state transition the runner emits. Methods are
// called from the runner's goroutine; observers must not block on I/O.
//
// PLAN-1 / I4b: OnStepLine is invoked once per newline-delimited
// chunk the spawned claude subprocess writes to stdout, BEFORE
// OnStepEnd fires. The line is the raw stream-json event text;
// observers are responsible for parsing + rendering. Implementations
// that don't care about live progress can leave OnStepLine empty.
type Observer interface {
	OnStageStart(stage string)
	OnStageEnd(stage string, dur time.Duration, err error)
	OnStepStart(stage string, idx int, step Step)
	OnStepLine(stage string, idx int, line string)
	OnStepEnd(stage string, idx int, step Step, dur time.Duration, output string, err error)
}

// Run executes a pipeline against opts.ProjectRoot. Each stage runs
// sequentially; within a stage, each step in the chain runs sequentially.
// Any non-zero claude exit fails the whole pipeline and aborts further
// stages (per PLAN-7 § Scope — full-fail semantics).
//
// PLAN-3 / M4: each run produces an on-disk manifest under
// opts.ManifestDir (default <ProjectRoot>/_output/pipelines). The
// manifest writer is constructed after preflight and finalized on every
// return path (success, step failure, context cancellation, build-argv
// error). When opts.DisableManifest is set, the writer is skipped and
// the runner's behavior is byte-identical to PLAN-2.
func Run(ctx context.Context, spec *Spec, opts RunOptions) error {
	if opts.ProjectRoot == "" {
		opts.ProjectRoot = "."
	}
	if opts.ClaudeBin == "" {
		opts.ClaudeBin = defaultClaudeBin
	}
	if opts.InteractiveStepGrace == 0 {
		opts.InteractiveStepGrace = 2 * time.Second
	}
	if opts.FromStage != "" {
		if err := validateFromStage(spec, opts.FromStage); err != nil {
			return err
		}
	}
	if err := Preflight(spec, opts.ProjectRoot); err != nil {
		return err
	}
	if err := PreflightSkills(spec, opts.ProjectRoot); err != nil {
		return err
	}

	if err := dirtyTreeGate(ctx, spec, opts); err != nil {
		return err
	}

	mw, err := startManifestWriter(spec, opts)
	if err != nil {
		return err
	}
	if mw != nil {
		mw.manifest.ClaudeVersion = claudeVersion(ctx, opts.ClaudeBin)
		_ = mw.persist()
		if opts.OnRunDir != nil {
			opts.OnRunDir(mw.runDir)
		}
	}

	// ape is PTY-only since v0.0.36 (PLAN-9 F2): every run executes in
	// the interactive per-stage claude REPL. The programmatic `claude -p`
	// path (runStages / buildArgv / runClaude) was removed.
	runErr := runStagesInteractive(ctx, spec, opts, mw)
	finalizeManifest(mw, runErr, opts.Observer)
	runLog(opts.RunLog, "pipeline-end", spec.Name, map[string]any{"error": errMessage(runErr)})
	return runErr
}

// pipelineWantsCommits returns true when at least one stage in the
// spec would emit a commit under the resolved PLAN-6 / C2 plan and
// the global NoCommit kill-switch is unset. Used to gate the dirty-
// tree check — if nothing will commit, prior dirty state is harmless.
func pipelineWantsCommits(spec *Spec, opts RunOptions) bool {
	if opts.NoCommit {
		return false
	}
	return spec.PipelineWantsCommits()
}

// dirtyTreeGate refuses to start a commit-emitting pipeline against a
// project whose working tree has uncommitted changes. The first
// committing step's `git add -A` would otherwise conflate user WIP
// into ape's commit. PLAN-4 / C5.
//
// Bypass: opts.AllowDirty (CLI `--commit-allow-dirty`) suppresses the
// check. opts.NoCommit also short-circuits because the run is going
// to be commit-free anyway.
func dirtyTreeGate(ctx context.Context, spec *Spec, opts RunOptions) error {
	if !pipelineWantsCommits(spec, opts) || opts.AllowDirty {
		return nil
	}
	if err := gitAvailable(ctx, opts.ProjectRoot); err != nil {
		return err
	}
	porcelain, err := gitStatusPorcelain(ctx, opts.ProjectRoot)
	if err != nil {
		return err
	}
	if porcelain == "" {
		return nil
	}
	return fmt.Errorf(
		"working tree has uncommitted changes; commit or stash before running `ape pipeline` "+
			"(commits run by default). Bypass options: --no-commit (leave the entire run uncommitted) or "+
			"--commit-allow-dirty (commit anyway; prior WIP merges into the first step's commit). "+
			"Note: `_output/` should be in your .gitignore — ape's manifest tree lives there.\n\n"+
			"git status --porcelain output:\n%s", porcelain,
	)
}

// validateFromStage returns an error when name does not match any stage
// in spec, listing the available names to guide the caller.
func validateFromStage(spec *Spec, name string) error {
	for _, s := range spec.Stages() {
		if s.Name == name {
			return nil
		}
	}
	stages := spec.Stages()
	names := make([]string, len(stages))
	for i, s := range stages {
		names[i] = s.Name
	}
	return fmt.Errorf("--from: stage %q not found in pipeline %q (available: %s)",
		name, spec.Name, strings.Join(names, ", "))
}

// errMessage returns err.Error() for non-nil err, empty string otherwise.
// Used to fold an error into a runlog payload map without nil-deref.
func errMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// runLog routes to opts.RunLog if non-nil. Helper so call sites stay
// terse and consistent.
func runLog(rl RunLogger, kind, step string, payload any) {
	if rl == nil {
		return
	}
	rl.CheckpointKindStep(kind, step, payload, time.Now().UTC())
}

// notify safely invokes a callback against an optional Observer.
func notify(o Observer, fn func(Observer)) {
	if o == nil {
		return
	}
	fn(o)
}

// startManifestWriter constructs the run's manifest writer. Returns
// (nil, nil) when manifest writing is disabled. Errors propagate to the
// caller — a failed manifest setup aborts the run before any step
// executes, so the failure is surfaced loud.
func startManifestWriter(spec *Spec, opts RunOptions) (*manifestWriter, error) {
	if opts.DisableManifest {
		return nil, nil //nolint:nilnil // intentional: disabled-manifest path is a documented no-op, not an error
	}
	baseDir := opts.ManifestDir
	if baseDir == "" {
		baseDir = filepath.Join(opts.ProjectRoot, "_output", "pipelines")
	}
	apeVersion := opts.ApeVersion
	if apeVersion == "" {
		apeVersion = "dev"
	}
	source := filepath.Join(PipelinesDir(opts.ProjectRoot), spec.Name+".yaml")
	return newManifestWriter(baseDir, spec.Name, opts.ProjectRoot, source, apeVersion, time.Now())
}

// finalizeManifest writes the terminal manifest + report. Picks the
// status from runErr; nil → completed, context.Canceled → cancelled,
// anything else → failed. Emits a single-line report pointer through
// the observer's OnStageEnd channel only via the existing path — this
// helper does not synthesize new events.
func finalizeManifest(mw *manifestWriter, runErr error, _ Observer) {
	if mw == nil {
		return
	}
	status := StatusCompleted
	switch {
	case errors.Is(runErr, context.Canceled), errors.Is(runErr, context.DeadlineExceeded):
		status = StatusCancelled
	case runErr != nil:
		status = StatusFailed
	}
	_, _ = mw.Finalize(status, time.Now())
}

// openStepLog wraps manifestWriter.OpenStepLog with the nil-mw fallback
// callers need at every step site. Returns (nil, "") when writing is
// disabled or the log file cannot be opened — the runner tolerates that
// degradation and proceeds with stream-only output.
func openStepLog(mw *manifestWriter, stageIdx, stepIdx int, stageName, skill string) (writer io.WriteCloser, relPath string) {
	if mw == nil {
		return nil, ""
	}
	w, rel, err := mw.OpenStepLog(stageIdx, stepIdx, stageName, skill)
	if err != nil {
		return nil, ""
	}
	return w, rel
}

func closeStepLog(w io.WriteCloser) {
	if w == nil {
		return
	}
	_ = w.Close()
}

// recordStep appends a StepRecord to the manifest writer, populating
// metrics from the parsed terminal result event when present.
func recordStep(
	mw *manifestWriter,
	stageIdx, stepIdx int,
	step Step,
	prompt string,
	startedAt, endedAt time.Time,
	status RunStatus,
	exitCode int,
	eventsPath string,
	ev *resultEvent,
) {
	if mw == nil {
		return
	}
	rec := StepRecord{
		Index:        stepIdx,
		Skill:        step.Skill,
		Agent:        step.Agent,
		Args:         step.Args,
		Prompt:       prompt,
		Model:        step.Model,
		StartedAt:    startedAt.UTC(),
		EndedAt:      endedAt.UTC(),
		DurationSecs: endedAt.Sub(startedAt).Seconds(),
		Status:       status,
		ExitCode:     exitCode,
		EventsPath:   eventsPath,
	}
	if ev != nil {
		rec.CostUSD = ev.TotalCostUSD
		rec.TokensInput = ev.Usage.InputTokens
		rec.TokensOutput = ev.Usage.OutputTokens
		rec.TokensCacheRead = ev.Usage.CacheReadInputTokens
		rec.TokensCacheCreation = ev.Usage.CacheCreationInputTokens
		rec.TokensCacheCreation5m = ev.Usage.CacheCreation5mInputTokens
		rec.TokensCacheCreation1h = ev.Usage.CacheCreation1hInputTokens
		rec.NumTurns = ev.NumTurns
		rec.TelemetryNote = ev.TelemetryNote
		rec.ModelUsage = modelUsageToRecords(ev.ModelUsage)
		rec.Sessions = sessionUsageToRecords(ev.Sessions)
		if status == StatusCompleted && ev.Subtype != "" && ev.Subtype != "success" {
			rec.Status = StatusFailed
		}
	}
	_ = mw.RecordStep(stageIdx, rec)
}

// modelUsageToRecords converts the in-memory per-model map onto the
// manifest's on-disk shape. Returns nil for empty input so the yaml
// field stays omitted.
func modelUsageToRecords(mu map[string]ModelUsage) map[string]ModelUsageRecord {
	if len(mu) == 0 {
		return nil
	}
	out := make(map[string]ModelUsageRecord, len(mu))
	for model, u := range mu {
		// Field sets are identical by construction; a direct type
		// conversion keeps them lock-stepped at compile time.
		out[model] = ModelUsageRecord(u)
	}
	return out
}

// sessionUsageToRecords converts per-session usage onto the manifest
// shape. Returns nil for empty input.
func sessionUsageToRecords(sessions []SessionUsage) []SessionUsageRecord {
	if len(sessions) == 0 {
		return nil
	}
	out := make([]SessionUsageRecord, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, SessionUsageRecord{
			SessionID:       s.SessionID,
			ParentSessionID: s.ParentSessionID,
			CostUSD:         s.Usage.CostUSD,
			TokensInput:     s.Usage.TokensInput,
			TokensOutput:    s.Usage.TokensOutput,
			NumTurns:        s.Usage.NumTurns,
			ModelUsage:      modelUsageToRecords(s.ModelUsage),
		})
	}
	return out
}

// performStepCommit decides whether to run `git commit` for a step
// just completed and records the outcome on the manifest. Returns a
// non-nil error only when the commit itself was attempted and failed
// (PLAN-4 / C4.4 — that case aborts the pipeline). All "did not
// commit" outcomes — skipped, cancelled, no-op, deferred — are silent.
//
// PLAN-6 / C2 Phase D — the plan parameter dictates whether this step
// commits at its own boundary (step-level opt-in), defers to the
// stage-end commit (stage-level / pipeline-level `commit:`), or skips
// entirely (`commit: false` somewhere, or nothing set anywhere). For
// stage-boundary stages, the last step in the chain (isLastStep=true)
// fires the stage-end commit with the plan's stage directive.
func performStepCommit(
	ctx context.Context,
	opts RunOptions,
	mw *manifestWriter,
	plan StageCommitPlan,
	stageIdx, stepIdx int,
	isLastStep bool,
	stepRunErr error,
) error {
	status, msg, sha, errMsg, commitErr := resolveCommitOutcome(ctx, opts, plan, stepIdx, isLastStep, stepRunErr)
	if mw != nil {
		_ = mw.RecordStepCommit(stageIdx, stepIdx, sha, msg, status, errMsg)
	}
	return commitErr
}

// resolveCommitOutcome implements the PLAN-6 / C2 decision table.
// Skip-state conditions (1–3) fire first because a step that failed
// or was cancelled can't have produced a commitable diff in any
// consistent state.
//
//  1. step cancelled (ctx)             → skipped-cancelled
//  2. step failed                      → skipped-step-failed
//  3. --no-commit                      → skipped-by-flag
//  4. plan suppressed                  → skipped-by-spec
//     (some step in the stage has `commit: false`)
//  5. step has explicit boundary       → attempt commit with step directive
//     (plan.StepDirectives[stepIdx-1] is set)
//  6. plan has stage directive
//     - !isLastStep                    → deferred-to-stage
//     - isLastStep                     → attempt commit with stage directive
//  7. no commit configured             → no commit_* fields recorded
//
// "attempt commit" steps fall through to the dirty-tree check:
// clean → no-op, dirty → git add -A → git commit.
func resolveCommitOutcome(
	ctx context.Context,
	opts RunOptions,
	plan StageCommitPlan,
	stepIdx int,
	isLastStep bool,
	stepRunErr error,
) (status CommitStatus, message, sha, errMsg string, commitErr error) {
	switch {
	case errors.Is(stepRunErr, context.Canceled), errors.Is(stepRunErr, context.DeadlineExceeded):
		return CommitStatusSkippedCancelled, "", "", "", nil
	case stepRunErr != nil:
		return CommitStatusSkippedStepFailed, "", "", "", nil //nolint:nilerr // stepRunErr is the step's error, not a commit error; the commit was intentionally skipped
	case opts.NoCommit:
		return CommitStatusSkippedByFlag, "", "", "", nil
	case plan.Suppressed:
		return CommitStatusSkippedBySpec, "", "", "", nil
	}
	// stepIdx is 1-based in the manifest API; plan.StepDirectives is
	// keyed by zero-based index (the Effective() convention).
	if dir, ok := plan.StepDirectives[stepIdx-1]; ok {
		return attemptCommit(ctx, opts.ProjectRoot, dir.Message)
	}
	if plan.StageDirective != nil {
		if !isLastStep {
			return CommitStatusDeferredToStage, "", "", "", nil
		}
		return attemptCommit(ctx, opts.ProjectRoot, plan.StageDirective.Message)
	}
	// No commit configured at any level — leave commit_* fields empty.
	return "", "", "", "", nil
}

// attemptCommit runs the dirty-tree → add → commit dance against
// projectRoot with the given message, returning the resulting
// CommitStatus / sha / err triple. Used by both the step-boundary
// path and the stage-boundary (last-step) path.
func attemptCommit(ctx context.Context, projectRoot, message string) (status CommitStatus, msg, sha, errMsg string, commitErr error) {
	porcelain, err := gitStatusPorcelain(ctx, projectRoot)
	if err != nil {
		return CommitStatusFailed, message, "", err.Error(), err
	}
	if porcelain == "" {
		return CommitStatusNoOp, message, "", "", nil
	}
	if err := gitAddAll(ctx, projectRoot); err != nil {
		return CommitStatusFailed, message, "", err.Error(), err
	}
	sha, err = gitCommit(ctx, projectRoot, message)
	if err != nil {
		return CommitStatusFailed, message, "", err.Error(), err
	}
	return CommitStatusCommitted, message, sha, "", nil
}

// claudeVersion resolves `<claudeBin> --version` for the manifest's
// claude_version stamp. Best-effort with a short timeout: claude-code
// auto-updates silently and its behavior (trust dialog, transcript
// persistence) shifts across versions, so runs must be attributable.
// Returns "" on any failure (shim binaries in tests, missing claude).
func claudeVersion(ctx context.Context, claudeBin string) string {
	vctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(vctx, claudeBin, "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ReportPathFor returns the pipeline-report.md path that the latest
// finalized run for the given pipeline wrote to, or "" if no manifest
// is available. Used by the CLI to print a stable pointer after a run.
// This wraps the symlink ape maintains at <base>/<pipeline>/latest.
func ReportPathFor(projectRoot, pipelineName, manifestDir string) string {
	dir := latestRunDir(projectRoot, pipelineName, manifestDir)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "pipeline-report.md")
}

// CommitsMadeFor returns the count of `committed` steps in the latest
// run's manifest, or 0 when no manifest is available or the read
// fails. Best-effort: callers should treat a 0 return as "unknown or
// none" — the CLI uses it only to decide whether to print the
// `📌 commits:` summary line.
func CommitsMadeFor(projectRoot, pipelineName, manifestDir string) int {
	dir := latestRunDir(projectRoot, pipelineName, manifestDir)
	if dir == "" {
		return 0
	}
	data, err := os.ReadFile(filepath.Join(dir, "manifest.yaml"))
	if err != nil {
		return 0
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return 0
	}
	return m.Totals.CommitsMade
}

// ResolveLatestRunDir is the exported alias of latestRunDir. The web
// CLI binds runlog.Writer to this dir once the runner has finalised
// its manifest path. PLAN-5 / C6.
func ResolveLatestRunDir(projectRoot, pipelineName, manifestDir string) string {
	return latestRunDir(projectRoot, pipelineName, manifestDir)
}

// latestRunDir resolves the absolute path of the latest run's
// directory for a pipeline via the `latest` symlink ape maintains.
// Returns "" when the symlink is absent (no run finalized yet).
func latestRunDir(projectRoot, pipelineName, manifestDir string) string {
	if manifestDir == "" {
		manifestDir = filepath.Join(projectRoot, "_output", "pipelines")
	}
	link := filepath.Join(manifestDir, pipelineName, "latest")
	target, err := os.Readlink(link)
	if err != nil {
		return ""
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(link), target)
	}
	return target
}
