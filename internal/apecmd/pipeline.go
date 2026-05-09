package apecmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/diegosz/apex_process_ape/internal/pipeline"
	"github.com/diegosz/apex_process_ape/internal/tui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

const exitCodePreflightFailed = 2

func newPipelineCmd() *cobra.Command {
	var (
		promptFlag string
		noTUI      bool
		cwdFlag    string
	)
	cmd := &cobra.Command{
		Use:       "pipeline [name]",
		Short:     "Run a named APEX pipeline",
		Long:      pipelineLongHelp(),
		Args:      cobra.ExactArgs(1),
		ValidArgs: pipeline.AvailablePipelines(),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			spec, err := pipeline.LoadSpec(name)
			if err != nil {
				return err
			}
			projectRoot := cwdFlag
			if projectRoot == "" {
				wd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("cannot determine working directory: %w", err)
				}
				projectRoot = wd
			}
			useTUI := !noTUI && term.IsTerminal(int(os.Stdout.Fd()))
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			if useTUI {
				return runWithTUI(ctx, spec, projectRoot, promptFlag)
			}
			return runPlain(ctx, spec, projectRoot, promptFlag)
		},
	}
	cmd.Flags().StringVar(&promptFlag, "prompt", "", "Optional prompt forwarded to skills that accept it (currently: epics)")
	cmd.Flags().BoolVar(&noTUI, "no-tui", false, "Disable the interactive TUI; print plain status lines instead")
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", "Project root directory (default: current working dir)")
	return cmd
}

func pipelineLongHelp() string {
	names := pipeline.AvailablePipelines()
	return fmt.Sprintf(`Run a named APEX pipeline against the project in the current working
directory.

Available pipelines:
  %s

Each pipeline is a sequence of stages; each stage is a chain of skill
invocations dispatched to the local "claude" CLI. Skill invocations
follow PAT-25 passthrough conventions, with the slash command + args
sent to claude as a single prompt string via -p:

    claude --dangerously-skip-permissions \
        -p "/<agent> --autonomous -- <skill> --autonomous <args>" \
        --output-format stream-json --verbose [--model M]

Skills without an agent skip the passthrough hop:

    claude --dangerously-skip-permissions \
        -p "/<skill> --autonomous --no-commit <args>" \
        ...

The --prompt flag is forwarded only to skills whose pipeline definition
declares prompt_flag (currently apex-create-epics-and-stories in the
"epics" pipeline). The prompt value passes through Go's argv directly,
so embedded quotes/specials survive without shell quoting.`,
		strings.Join(names, ", "))
}

// runPlain runs the pipeline with stdout status lines (no TUI). Used
// when --no-tui is set or stdout is not a terminal.
func runPlain(ctx context.Context, spec *pipeline.Spec, projectRoot, prompt string) error {
	obs := newPlainObserver(os.Stdout)
	err := pipeline.Run(ctx, spec, pipeline.RunOptions{
		ProjectRoot: projectRoot,
		Prompt:      prompt,
		Observer:    obs,
	})
	if err != nil {
		var pfe *pipeline.PreflightError
		if errors.As(err, &pfe) {
			fmt.Fprintf(os.Stderr, "%s\n", pfe.Error())
			os.Exit(exitCodePreflightFailed)
		}
		return err
	}
	return nil
}

// runWithTUI runs the pipeline alongside a Bubble Tea program. The
// pipeline runs in a goroutine; observer events become tea.Msgs that
// drive the two-panel display. The TUI exits once pipelineDoneMsg
// arrives (or the user presses q / ctrl+c, in which case the runner's
// context cancellation tears down the in-flight claude process).
func runWithTUI(ctx context.Context, spec *pipeline.Spec, projectRoot, prompt string) error {
	model := tui.NewPipelineModel(spec)
	program := tea.NewProgram(model, tea.WithAltScreen())
	obs := tui.NewPipelineTUIObserver(program)

	runErrCh := make(chan error, 1)
	go func() {
		err := pipeline.Run(ctx, spec, pipeline.RunOptions{
			ProjectRoot: projectRoot,
			Prompt:      prompt,
			Observer:    obs,
		})
		obs.Done(err)
		runErrCh <- err
	}()

	if _, err := program.Run(); err != nil {
		return fmt.Errorf("TUI: %w", err)
	}
	runErr := <-runErrCh
	var pfe *pipeline.PreflightError
	if errors.As(runErr, &pfe) {
		fmt.Fprintf(os.Stderr, "%s\n", pfe.Error())
		os.Exit(exitCodePreflightFailed)
	}
	return runErr
}

// plainObserver writes one status line per state transition. Used when
// stdout is non-TTY or --no-tui is set.
type plainObserver struct {
	w  *os.File
	t0 time.Time
}

func newPlainObserver(w *os.File) *plainObserver {
	return &plainObserver{w: w, t0: time.Now()}
}

func (p *plainObserver) OnStageStart(stage string) {
	fmt.Fprintf(p.w, "[%s] stage start: %s\n", elapsed(p.t0), stage)
}

func (p *plainObserver) OnStageEnd(stage string, dur time.Duration, err error) {
	if err != nil {
		fmt.Fprintf(p.w, "[%s] stage FAIL: %s (%s) — %v\n", elapsed(p.t0), stage, fmtDuration(dur), err)
		return
	}
	fmt.Fprintf(p.w, "[%s] stage done: %s (%s)\n", elapsed(p.t0), stage, fmtDuration(dur))
}

func (p *plainObserver) OnStepStart(_ string, idx int, step pipeline.Step) { //nolint:gocritic // Step is passed by value to match the Observer interface signature
	tag := step.Skill
	if step.Agent != "" {
		tag = step.Agent + " -> " + step.Skill
	}
	fmt.Fprintf(p.w, "[%s]   step %d: %s\n", elapsed(p.t0), idx+1, tag)
}

func (p *plainObserver) OnStepEnd(_ string, idx int, step pipeline.Step, dur time.Duration, output string, err error) { //nolint:gocritic // Step is passed by value to match the Observer interface signature
	if err != nil {
		fmt.Fprintf(p.w, "[%s]   step %d FAIL: %s (%s)\n", elapsed(p.t0), idx+1, step.Skill, fmtDuration(dur))
		if output != "" {
			fmt.Fprintf(p.w, "%s\n", output)
		}
		return
	}
	fmt.Fprintf(p.w, "[%s]   step %d done: %s (%s)\n", elapsed(p.t0), idx+1, step.Skill, fmtDuration(dur))
}

func elapsed(t0 time.Time) string {
	return fmtDuration(time.Since(t0))
}

func fmtDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60) //nolint:mnd // 60 is seconds-per-minute, a well-known constant
}
