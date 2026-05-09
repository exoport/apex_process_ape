package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
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
type Observer interface {
	OnStageStart(stage string)
	OnStageEnd(stage string, dur time.Duration, err error)
	OnStepStart(stage string, idx int, step Step)
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
			out, runErr := runClaude(ctx, argv, opts.ProjectRoot)
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

// runClaude executes argv with cwd = projectRoot, captures combined
// stdout+stderr, and returns it. claude does not flush per-line under
// normal sub-process invocation (per PLAN-7 § Open issues), so this
// function returns the full captured output once the process exits.
func runClaude(ctx context.Context, argv []string, projectRoot string) (string, error) {
	if len(argv) == 0 {
		return "", errors.New("empty argv")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // argv is constructed from embedded pipeline specs and validated step data; intentional subprocess dispatch
	cmd.Dir = projectRoot
	var buf strings.Builder
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return buf.String(), err
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
