package pipeline

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

	// Interactive, when true, runs the pipeline under PLAN-6 exec
	// mode: one `claude` process per stage running inside its own
	// in-process PTY (PLAN-8: `internal/repl/`), with steps fed as
	// real REPL keystrokes via PTY Write (instead of one `claude -p`
	// per step). Steps share the session within a stage and are
	// separated by a `/clear` slash command; stage boundaries are the
	// process spawn. Default is false (today's per-step programmatic
	// mode).
	Interactive bool

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
// interactive step, derived from the claude session transcript. The
// runner adapts it to the same shape the stream-json `result` event
// produces in programmatic mode, so the manifest writer's recordStep
// API stays uniform across exec modes.
type StepTelemetry struct {
	CostUSD             float64
	TokensInput         int
	TokensOutput        int
	TokensCacheRead     int
	TokensCacheCreation int
	NumTurns            int
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
	if mw != nil && opts.OnRunDir != nil {
		opts.OnRunDir(mw.runDir)
	}

	var runErr error
	if opts.Interactive {
		runErr = runStagesInteractive(ctx, spec, opts, mw)
	} else {
		runErr = runStages(ctx, spec, opts, mw)
	}
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

// runStages drives the spec's stage/step chain. Separated from Run so
// the manifest finalization path is a single return point.
func runStages(ctx context.Context, spec *Spec, opts RunOptions, mw *manifestWriter) error {
	for _, stage := range spec.Stages() {
		stageStart := time.Now()
		var stageIdx int
		if mw != nil {
			stageIdx = mw.BeginStage(stage.Name, stageStart)
		}
		notify(opts.Observer, func(o Observer) { o.OnStageStart(stage.Name) })
		if opts.OnStageStart != nil {
			opts.OnStageStart(stage.Name)
		}
		runLog(opts.RunLog, "stage-start", stage.Name, nil)
		stageStatus := StatusCompleted

		plan, planErr := spec.PlanStageCommits(stage.Name)
		if planErr != nil {
			runLog(opts.RunLog, "stage-end", stage.Name, map[string]any{"error": errMessage(planErr)})
			return planErr
		}

		for i, step := range stage.Chain {
			isLastStep := i == len(stage.Chain)-1
			stepStart := time.Now()
			notify(opts.Observer, func(o Observer) { o.OnStepStart(stage.Name, i, step) })

			argv, argvErr := buildArgv(opts.ClaudeBin, step, opts.Prompt, opts.PrependFlags)
			if argvErr != nil {
				recordStep(mw, stageIdx, i+1, step, opts.Prompt, stepStart, time.Now(), StatusFailed, 1, "", nil)
				notify(opts.Observer, func(o Observer) {
					o.OnStepEnd(stage.Name, i, step, time.Since(stepStart), "", argvErr)
				})
				stageStatus = StatusFailed
				if mw != nil {
					_ = mw.EndStage(stageIdx, stageStatus, time.Now())
				}
				notify(opts.Observer, func(o Observer) { o.OnStageEnd(stage.Name, time.Since(stageStart), argvErr) })
				if opts.OnStageEnd != nil {
					opts.OnStageEnd(stage.Name, time.Since(stageStart), argvErr)
				}
				runLog(opts.RunLog, "stage-end", stage.Name, map[string]any{"error": errMessage(argvErr)})
				return argvErr
			}

			eventLog, eventsRel := openStepLog(mw, stageIdx, i+1, stage.Name, step.Skill)
			out, runErr := runClaude(ctx, argv, opts.ProjectRoot, opts.Observer, stage.Name, i, eventLog)
			closeStepLog(eventLog)

			stepStatus := StatusCompleted
			exitCode := 0
			if runErr != nil {
				stepStatus = StatusFailed
				exitCode = exitCodeFromErr(runErr)
			}
			recordStep(mw, stageIdx, i+1, step, opts.Prompt, stepStart, time.Now(), stepStatus, exitCode, eventsRel, parseResultEvent(out))

			// Commit boundary: PLAN-6 / C2. Runs only after the step's
			// run-state is recorded so the manifest reflects both the
			// run outcome and the commit outcome atomically. Failures
			// here abort the pipeline — a dirty unexpected tree is
			// worse to silently extend than to fail loudly.
			commitErr := performStepCommit(ctx, opts, mw, plan, stageIdx, i+1, isLastStep, runErr)
			if commitErr == nil && runErr == nil {
				// Best-effort checkpoint — the manifest also
				// records commit_sha, this is for the
				// streaming SSE / runlog consumer.
				runLog(opts.RunLog, "commit-made", StepLabel(stage.Name, i+1, step.Skill), nil)
			}

			notify(opts.Observer, func(o Observer) { o.OnStepEnd(stage.Name, i, step, time.Since(stepStart), out, runErr) })
			if runErr != nil {
				stageStatus = StatusFailed
				if mw != nil {
					_ = mw.EndStage(stageIdx, stageStatus, time.Now())
				}
				notify(opts.Observer, func(o Observer) { o.OnStageEnd(stage.Name, time.Since(stageStart), runErr) })
				if opts.OnStageEnd != nil {
					opts.OnStageEnd(stage.Name, time.Since(stageStart), runErr)
				}
				runLog(opts.RunLog, "stage-end", stage.Name, map[string]any{"error": errMessage(runErr)})
				return fmt.Errorf("stage %q step %d (%s): %w", stage.Name, i, step.Skill, runErr)
			}
			if commitErr != nil {
				stageStatus = StatusFailed
				if mw != nil {
					_ = mw.EndStage(stageIdx, stageStatus, time.Now())
				}
				notify(opts.Observer, func(o Observer) { o.OnStageEnd(stage.Name, time.Since(stageStart), commitErr) })
				if opts.OnStageEnd != nil {
					opts.OnStageEnd(stage.Name, time.Since(stageStart), commitErr)
				}
				runLog(opts.RunLog, "stage-end", stage.Name, map[string]any{"error": errMessage(commitErr)})
				return fmt.Errorf("stage %q step %d (%s) commit: %w", stage.Name, i, step.Skill, commitErr)
			}
		}

		if mw != nil {
			_ = mw.EndStage(stageIdx, stageStatus, time.Now())
		}
		notify(opts.Observer, func(o Observer) { o.OnStageEnd(stage.Name, time.Since(stageStart), nil) })
		if opts.OnStageEnd != nil {
			opts.OnStageEnd(stage.Name, time.Since(stageStart), nil)
		}
		runLog(opts.RunLog, "stage-end", stage.Name, nil)
	}
	return nil
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

// buildArgv constructs the argv for a single step's claude invocation.
//
// The claude CLI takes the slash command + its arguments as a single
// prompt string via the -p flag, not as discrete argv elements. We
// build that prompt string per PAT-25 conventions, then assemble the
// outer argv around it:
//
//	claude --dangerously-skip-permissions -p <prompt> \
//	    --output-format stream-json --verbose [--model M]
//
// The prompt string itself follows two PAT-25 shapes:
//
//   - With agent:    /{agent} --autonomous -- {skill} --autonomous {step.Args} {step.PromptFlag prompt}
//   - Without agent: /{skill} --autonomous --no-commit {step.Args} {step.PromptFlag prompt}
//
// step.PromptFlag, when set with a non-empty runtime prompt, contributes
// "--prompt <value>" inside the prompt string. The value is appended
// verbatim — argv is never serialized through a shell, so embedded
// quotes/specials in the user's prompt survive intact.
func buildArgv(claudeBin string, step Step, prompt string, prependFlags []string) ([]string, error) {
	if claudeBin == "" {
		return nil, errors.New("empty claude bin")
	}
	if step.Skill == "" {
		return nil, errors.New("step missing skill")
	}
	var promptParts []string
	if step.Agent != "" {
		promptParts = []string{"/" + step.Agent, flagAutonomous, "--", step.Skill, flagAutonomous}
	} else {
		promptParts = []string{"/" + step.Skill, flagAutonomous, "--no-commit"}
	}
	if step.Args != "" {
		promptParts = append(promptParts, strings.Fields(step.Args)...)
	}
	if step.PromptFlag != "" && prompt != "" {
		promptParts = append(promptParts, step.PromptFlag, prompt)
	}
	promptStr := strings.Join(promptParts, " ")
	argv := []string{claudeBin}
	// PLAN-5 / C1 + C3 — web mode prepends --strict-mcp-config,
	// --mcp-config <inline>, --settings <inline> here. The flags
	// must land before --dangerously-skip-permissions for claude's
	// argv parser to attach them to the right options table.
	argv = append(argv, prependFlags...)
	argv = append(
		argv,
		"--dangerously-skip-permissions",
		"-p", promptStr,
		flagOutputFormat, flagOutputStreamJSON,
		flagVerbose,
	)
	if step.Model != "" {
		argv = append(argv, "--model", step.Model)
	}
	return argv, nil
}

// runClaude executes argv with cwd = projectRoot, streams the
// subprocess's stdout + stderr line-by-line to the Observer via
// OnStepLine, accumulates the full output into a buffer, and returns
// it once the process exits.
//
// Per PLAN-1 / I4b, claude --output-format stream-json produces NDJSON
// events on stdout as the model executes; surfacing those as they
// arrive is what makes the TUI feel live. Stderr is interleaved into
// the same line stream (with a sentinel prefix not yet used) so
// failures still surface in the captured output.
func runClaude(ctx context.Context, argv []string, projectRoot string, observer Observer, stage string, idx int, eventLog io.Writer) (string, error) {
	if len(argv) == 0 {
		return "", errors.New("empty argv")
	}
	if os.Getenv("APE_DEBUG_ARGV") != "" {
		// Surface the full argv on stderr for crash diagnosis. The
		// inline --mcp-config / --settings JSON blobs make this verbose
		// but they're <1 KB each so it stays readable.
		fmt.Fprintf(os.Stderr, "[ape-debug] argv:\n")
		for i, a := range argv {
			fmt.Fprintf(os.Stderr, "  [%d] %s\n", i, a)
		}
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // argv is constructed from embedded pipeline specs and validated step data; intentional subprocess dispatch
	cmd.Dir = projectRoot
	// PLAN-2 / F1: rewire context-cancel to SIGTERM the whole process
	// group (not just the direct child) so claude-spawned Task-tool
	// grandchildren can't outlive a confirmed quit. No-op on Windows.
	configureProcessGroup(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start claude: %w", err)
	}

	var (
		buf strings.Builder
		mu  sync.Mutex // guards buf — both reader goroutines append concurrently
		wg  sync.WaitGroup
	)
	scan := func(r io.Reader) {
		defer wg.Done()
		// Default Scanner buffer is 64KiB; stream-json events from
		// claude can include long tool_result bodies, so allocate a
		// larger ceiling (1MiB) to keep Scan() from erroring out.
		s := bufio.NewScanner(r)
		s.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for s.Scan() {
			line := s.Text()
			mu.Lock()
			buf.WriteString(line)
			buf.WriteByte('\n')
			if eventLog != nil {
				_, _ = eventLog.Write([]byte(line))
				_, _ = eventLog.Write([]byte{'\n'})
			}
			mu.Unlock()
			notify(observer, func(o Observer) { o.OnStepLine(stage, idx, line) })
		}
		// Scanner errors here (token too long, etc.) are absorbed
		// into the accumulated output via cmd.Wait()'s exit code.
	}
	wg.Add(2)
	go scan(stdout)
	go scan(stderr)
	// Per the os/exec docs, all reads from StdoutPipe / StderrPipe
	// must complete before Wait. The order here is load-bearing: a
	// SIGTERM-trapping grandchild (PLAN-2 / F1 scenario) keeps the
	// pipe write-end open after the immediate child dies; the
	// SIGKILL-after-grace escalator inside configureProcessGroup's
	// Cancel hook is what eventually closes that pipe and unblocks
	// the scanners, letting wg.Wait return.
	wg.Wait()
	waitErr := cmd.Wait()
	return buf.String(), waitErr
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
		rec.NumTurns = ev.NumTurns
		if status == StatusCompleted && ev.Subtype != "" && ev.Subtype != "success" {
			rec.Status = StatusFailed
		}
	}
	_ = mw.RecordStep(stageIdx, rec)
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

// exitCodeFromErr extracts the OS exit code from an exec error. Falls
// back to 1 when the underlying error is not an *exec.ExitError.
func exitCodeFromErr(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 1
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
