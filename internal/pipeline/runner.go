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
)

const (
	flagAutonomous       = "--autonomous"
	flagOutputFormat     = "--output-format"
	flagVerbose          = "--verbose"
	flagOutputStreamJSON = "stream-json"
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
func Run(ctx context.Context, spec *Spec, opts RunOptions) error { //nolint:gocritic // RunOptions is a small configuration struct passed once per pipeline run; pointer would not change semantics and would split the documented API shape
	if opts.ProjectRoot == "" {
		opts.ProjectRoot = "."
	}
	if opts.ClaudeBin == "" {
		opts.ClaudeBin = "claude"
	}
	if err := Preflight(spec, opts.ProjectRoot); err != nil {
		return err
	}

	mw, err := startManifestWriter(spec, opts)
	if err != nil {
		return err
	}

	runErr := runStages(ctx, spec, opts, mw)
	finalizeManifest(mw, runErr, opts.Observer)
	return runErr
}

// runStages drives the spec's stage/step chain. Separated from Run so
// the manifest finalization path is a single return point.
func runStages(ctx context.Context, spec *Spec, opts RunOptions, mw *manifestWriter) error { //nolint:gocritic // RunOptions mirrors Run's parameter shape; see Run's nolint rationale
	for _, stage := range spec.Stages() {
		stageStart := time.Now()
		var stageIdx int
		if mw != nil {
			stageIdx = mw.BeginStage(stage.Name, stageStart)
		}
		notify(opts.Observer, func(o Observer) { o.OnStageStart(stage.Name) })
		stageStatus := StatusCompleted

		for i, step := range stage.Chain {
			stepStart := time.Now()
			notify(opts.Observer, func(o Observer) { o.OnStepStart(stage.Name, i, step) })

			argv, argvErr := buildArgv(opts.ClaudeBin, step, opts.Prompt)
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

			notify(opts.Observer, func(o Observer) { o.OnStepEnd(stage.Name, i, step, time.Since(stepStart), out, runErr) })
			if runErr != nil {
				stageStatus = StatusFailed
				if mw != nil {
					_ = mw.EndStage(stageIdx, stageStatus, time.Now())
				}
				notify(opts.Observer, func(o Observer) { o.OnStageEnd(stage.Name, time.Since(stageStart), runErr) })
				return fmt.Errorf("stage %q step %d (%s): %w", stage.Name, i, step.Skill, runErr)
			}
		}

		if mw != nil {
			_ = mw.EndStage(stageIdx, stageStatus, time.Now())
		}
		notify(opts.Observer, func(o Observer) { o.OnStageEnd(stage.Name, time.Since(stageStart), nil) })
	}
	return nil
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
func buildArgv(claudeBin string, step Step, prompt string) ([]string, error) { //nolint:gocritic // Step is a small configuration struct; pointer would complicate caller sites without benefit
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
	argv := []string{
		claudeBin, "--dangerously-skip-permissions",
		"-p", promptStr,
		flagOutputFormat, flagOutputStreamJSON,
		flagVerbose,
	}
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
		s.Buffer(make([]byte, 0, 64*1024), 1024*1024) //nolint:mnd // 64KiB initial / 1MiB ceiling on a Scanner buffer is a documented Bubble-Tea-adjacent norm
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
	wg.Add(2) //nolint:mnd // exactly two pipe readers (stdout + stderr); not a magic number worth a named constant
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
func startManifestWriter(spec *Spec, opts RunOptions) (*manifestWriter, error) { //nolint:gocritic // RunOptions mirrors Run's shape; see Run's nolint rationale
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
	step Step, //nolint:gocritic // Step mirrors buildArgv's parameter shape; see buildArgv's existing nolint rationale
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
	return filepath.Join(target, "pipeline-report.md")
}
