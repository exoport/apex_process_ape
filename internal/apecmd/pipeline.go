package apecmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/diegosz/apex_process_ape/internal/output"
	"github.com/diegosz/apex_process_ape/internal/pipeline"
	"github.com/diegosz/apex_process_ape/internal/tui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

const exitCodePreflightFailed = 2

func newPipelineCmd() *cobra.Command {
	var (
		promptFlag   string
		noTUI        bool
		cwdFlag      string
		outputFormat string
	)
	cmd := &cobra.Command{
		Use:   "pipeline [name]",
		Short: "List or run an APEX pipeline",
		Long:  pipelineLongHelp(),
		Args:  cobra.MaximumNArgs(1),
		ValidArgsFunction: func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			if len(args) > 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			projectRoot := cwdFlag
			if projectRoot == "" {
				wd, err := os.Getwd()
				if err != nil {
					return nil, cobra.ShellCompDirectiveError
				}
				projectRoot = wd
			}
			return pipeline.AvailablePipelines(projectRoot), cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			projectRoot := cwdFlag
			if projectRoot == "" {
				wd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("cannot determine working directory: %w", err)
				}
				projectRoot = wd
			}
			// No positional arg → list mode. With a name → run mode.
			if len(args) == 0 {
				res := pipelineListResult{
					ProjectRoot:  projectRoot,
					PipelinesDir: pipeline.PipelinesDir(projectRoot),
					Names:        pipeline.AvailablePipelines(projectRoot),
				}
				return printPipelineList(res, output.Format(outputFormat))
			}
			name := args[0]
			spec, err := pipeline.LoadSpec(name, projectRoot)
			if err != nil {
				// Root cmd has SilenceErrors=true, so a bare return
				// would swallow the actionable "run ape framework
				// update" hint. Print to stderr explicitly, matching
				// the convention used elsewhere in apecmd.
				fmt.Fprintf(os.Stderr, "Error: %s\n", err.Error())
				return err
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
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format for list mode (no positional arg): human|json|yaml")
	cmd.PersistentFlags().StringVar(&cwdFlag, "cwd", "", "Project root directory (default: current working dir)")
	return cmd
}

func pipelineLongHelp() string {
	return `List or run a named APEX pipeline against the project in the current
working directory.

  ape pipeline                 List installed pipelines (also accepts
                               --output-format human|json|yaml).
  ape pipeline <name>          Run the named pipeline.

Available pipelines are read from <project>/_apex/pipelines/. To
install the canonical set (design, governance, epics) from the
framework repo, run "ape framework update".

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
so embedded quotes/specials survive without shell quoting.`
}

// pipelineListResult is the structured payload for `ape pipeline`
// invoked with no positional arg (list mode).
type pipelineListResult struct {
	ProjectRoot  string   `json:"projectRoot"  yaml:"projectRoot"`
	PipelinesDir string   `json:"pipelinesDir" yaml:"pipelinesDir"`
	Names        []string `json:"names"        yaml:"names"`
}

func printPipelineList(res pipelineListResult, format output.Format) error {
	switch format {
	case output.FormatJSON:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	case output.FormatYAML:
		return output.Print(os.Stdout, output.FormatYAML, res)
	default:
		if len(res.Names) == 0 {
			fmt.Printf("No pipelines installed at %s\n", res.PipelinesDir)
			fmt.Println(`Run "ape framework update" to install the canonical set (design, governance, epics).`)
			return nil
		}
		fmt.Printf("Pipelines installed at %s:\n", res.PipelinesDir)
		for _, n := range res.Names {
			fmt.Printf("  %s\n", n)
		}
		return nil
	}
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
// arrives, or when the user confirms the quit modal (q / Ctrl+C with
// y, or double Ctrl+C), in which case runCancel cancels the runner's
// context — exec.CommandContext then tears down the in-flight claude
// subprocess.
func runWithTUI(ctx context.Context, spec *pipeline.Spec, projectRoot, prompt string) error {
	// Local cancel scoped to this TUI run. Wrapping the caller's ctx
	// gives the modal a dedicated cancellation handle without
	// interfering with the cobra signal-handling on the parent ctx.
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	model := tui.NewPipelineModel(spec, runCancel)
	program := tea.NewProgram(model, tea.WithAltScreen())
	obs := tui.NewPipelineTUIObserver(program)

	runErrCh := make(chan error, 1)
	go func() {
		err := pipeline.Run(runCtx, spec, pipeline.RunOptions{
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
		// os.Exit skips the deferred runCancel; invoke explicitly so
		// no leaked goroutine or subprocess can survive.
		runCancel()
		os.Exit(exitCodePreflightFailed) //nolint:gocritic // explicit runCancel() above neutralizes the defer-skip
	}
	return runErr
}

// plainObserver writes one status line per state transition. Used when
// stdout is non-TTY or --no-tui is set. Per PLAN-1 / I4b, this
// observer also streams parsed stream-json events through the shared
// tui.RenderEvent function so log captures and CI runs see the same
// human-friendly progress feed as the interactive TUI.
type plainObserver struct {
	w            *os.File
	t0           time.Time
	currentStage string
	currentSkill string
}

func newPlainObserver(w *os.File) *plainObserver {
	return &plainObserver{w: w, t0: time.Now()}
}

func (p *plainObserver) OnStageStart(stage string) {
	p.currentStage = stage
	fmt.Fprintf(p.w, "[%s] stage start: %s\n", elapsed(p.t0), stage)
}

func (p *plainObserver) OnStageEnd(stage string, dur time.Duration, err error) {
	if err != nil {
		fmt.Fprintf(p.w, "[%s] stage FAIL: %s (%s) — %v\n", elapsed(p.t0), stage, fmtDuration(dur), err)
		return
	}
	fmt.Fprintf(p.w, "[%s] stage done: %s (%s)\n", elapsed(p.t0), stage, fmtDuration(dur))
}

func (p *plainObserver) OnStepStart(stage string, idx int, step pipeline.Step) { //nolint:gocritic // Step is passed by value to match the Observer interface signature
	p.currentStage = stage
	p.currentSkill = step.Skill
	tag := step.Skill
	if step.Agent != "" {
		tag = step.Agent + " -> " + step.Skill
	}
	fmt.Fprintf(p.w, "[%s]   step %d: %s\n", elapsed(p.t0), idx+1, tag)
}

// OnStepLine renders each stream-json event the spawned claude
// subprocess emits as a single timestamped, prefixed line on stdout.
// Suppressed events (noisy successful tool_results, etc.) are
// dropped. Same renderer that powers the interactive TUI lives in
// internal/tui/event_renderer.go.
func (p *plainObserver) OnStepLine(stage string, _ int, line string) {
	r := tui.RenderEvent(line)
	if !r.IsDisplayable() {
		return
	}
	// Use the runner-supplied stage when we have it (covers race-y
	// cases where OnStageStart hasn't recorded yet); fall back to
	// the observer's tracked stage.
	stageName := stage
	if stageName == "" {
		stageName = p.currentStage
	}
	fmt.Fprintf(p.w, "[%s] %s · %s · %s %s\n",
		elapsed(p.t0), stageName, p.currentSkill, r.Glyph, r.Body)
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
