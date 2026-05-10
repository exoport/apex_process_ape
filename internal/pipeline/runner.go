package pipeline

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
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
func Run(ctx context.Context, spec *Spec, opts RunOptions) error {
	if opts.ProjectRoot == "" {
		opts.ProjectRoot = "."
	}
	if opts.ClaudeBin == "" {
		opts.ClaudeBin = "claude"
	}
	if err := Preflight(spec, opts.ProjectRoot); err != nil {
		return err
	}
	for _, stage := range spec.Stages() {
		stageStart := time.Now()
		notify(opts.Observer, func(o Observer) { o.OnStageStart(stage.Name) })
		for i, step := range stage.Chain {
			stepStart := time.Now()
			notify(opts.Observer, func(o Observer) { o.OnStepStart(stage.Name, i, step) })
			argv, err := buildArgv(opts.ClaudeBin, step, opts.Prompt)
			if err != nil {
				notify(opts.Observer, func(o Observer) { o.OnStepEnd(stage.Name, i, step, time.Since(stepStart), "", err) })
				notify(opts.Observer, func(o Observer) { o.OnStageEnd(stage.Name, time.Since(stageStart), err) })
				return err
			}
			out, runErr := runClaude(ctx, argv, opts.ProjectRoot, opts.Observer, stage.Name, i)
			notify(opts.Observer, func(o Observer) { o.OnStepEnd(stage.Name, i, step, time.Since(stepStart), out, runErr) })
			if runErr != nil {
				notify(opts.Observer, func(o Observer) { o.OnStageEnd(stage.Name, time.Since(stageStart), runErr) })
				return fmt.Errorf("stage %q step %d (%s): %w", stage.Name, i, step.Skill, runErr)
			}
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
func runClaude(ctx context.Context, argv []string, projectRoot string, observer Observer, stage string, idx int) (string, error) {
	if len(argv) == 0 {
		return "", errors.New("empty argv")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // argv is constructed from embedded pipeline specs and validated step data; intentional subprocess dispatch
	cmd.Dir = projectRoot

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
			mu.Unlock()
			notify(observer, func(o Observer) { o.OnStepLine(stage, idx, line) })
		}
		// Scanner errors here (token too long, etc.) are absorbed
		// into the accumulated output via cmd.Wait()'s exit code.
	}
	wg.Add(2) //nolint:mnd // exactly two pipe readers (stdout + stderr); not a magic number worth a named constant
	go scan(stdout)
	go scan(stderr)
	wg.Wait()

	waitErr := cmd.Wait()
	if waitErr != nil {
		return buf.String(), waitErr
	}
	return buf.String(), nil
}

// notify safely invokes a callback against an optional Observer.
func notify(o Observer, fn func(Observer)) {
	if o == nil {
		return
	}
	fn(o)
}
