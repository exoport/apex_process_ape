package apecmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
		promptFlag         string
		noTUI              bool
		quietFlag          bool
		cwdFlag            string
		outputFormat       string
		manifestDirFlag    string
		noCommitFlag       bool
		allowDirtyFlag     bool
		tuiFlag            bool
		printFlag          bool
		webFlag            bool
		openFlag           bool
		ignoreProjSettings bool
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
			mode, optOutTUI, err := resolvePipelineMode(tuiFlag, printFlag, noTUI, webFlag, os.Stderr)
			if err != nil {
				fmt.Fprintln(os.Stderr, "Error: "+err.Error())
				os.Exit(exitCodePreflightFailed) //nolint:gocritic // explicit exit; no defers up to this point
			}
			useTUI := !optOutTUI && term.IsTerminal(int(os.Stdout.Fd())) && mode != PipelineModeWeb
			if quietFlag && useTUI {
				// PLAN-2 / F5: --quiet only suppresses the live
				// event stream that plainObserver emits. The TUI's
				// panels render whether --quiet is set or not, so
				// combining the flags is almost certainly a
				// misconception — refuse loudly rather than
				// silently no-op.
				return errors.New("--quiet is only meaningful with --no-tui (the TUI panels aren't affected by the flag)")
			}
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			runOpts := runConfig{
				prompt:                promptFlag,
				manifestDir:           manifestDirFlag,
				noCommit:              noCommitFlag,
				allowDirty:            allowDirtyFlag,
				ignoreProjectSettings: ignoreProjSettings,
				openOnStart:           openFlag,
			}
			if mode == PipelineModeWeb {
				return runWithWeb(ctx, spec, projectRoot, runOpts)
			}
			if useTUI {
				return runWithTUI(ctx, spec, projectRoot, runOpts)
			}
			return runPlain(ctx, spec, projectRoot, quietFlag, runOpts)
		},
	}
	cmd.Flags().StringVar(&promptFlag, "prompt", "", "Optional prompt forwarded to skills that accept it (currently: epics)")
	cmd.Flags().BoolVar(&webFlag, "web", false, "Bridged web UI (now the default). Explicit form for scripts.")
	cmd.Flags().BoolVar(&tuiFlag, "tui", false, "Bubble Tea TUI (pre-PLAN-5 default; now opt-in).")
	cmd.Flags().BoolVar(&printFlag, "print", false, "Plain stdout (eval / CI capture path).")
	cmd.Flags().BoolVar(&noTUI, "no-tui", false, "Deprecated alias for --print. Prints a stderr warning when used.")
	cmd.Flags().BoolVar(&openFlag, "open", false, "With --web (or default): xdg-open the broker URL on start.")
	cmd.Flags().BoolVar(&ignoreProjSettings, "ignore-project-settings", false, "Tell the spawned claude to skip project + local .claude/settings*.json. Honoured in --web mode.")
	cmd.Flags().BoolVar(&quietFlag, "quiet", false, "With --no-tui: suppress per-event stream; print only stage/step start/end markers")
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format for list mode (no positional arg): human|json|yaml")
	cmd.Flags().StringVar(&manifestDirFlag, "manifest-dir", "", "Override the directory for run manifest artifacts (default: <project>/_output/pipelines)")
	cmd.Flags().BoolVar(&noCommitFlag, "no-commit", false, "Do not commit anything during the run; leave the working tree dirty. Overrides any `commit:` field in the pipeline YAML.")
	cmd.Flags().BoolVar(&allowDirtyFlag, "commit-allow-dirty", false, "Bypass the dirty-tree pre-run gate. The first committing step's diff will include any pre-existing uncommitted changes.")
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
// when --no-tui is set or stdout is not a terminal. When quiet is true
// (PLAN-2 / F5) the per-event stream is suppressed; only stage/step
// start/end markers are emitted, matching the pre-PLAN-1 / I4b shape.
// runConfig bundles CLI-derived knobs passed to runPlain / runWithTUI.
// Grouped to keep call-sites stable as new flags land.
type runConfig struct {
	prompt                string
	manifestDir           string
	noCommit              bool
	allowDirty            bool
	ignoreProjectSettings bool
	openOnStart           bool
}

func runPlain(ctx context.Context, spec *pipeline.Spec, projectRoot string, quiet bool, cfg runConfig) error {
	obs := newPlainObserver(os.Stdout, projectRoot, quiet)
	err := pipeline.Run(ctx, spec, pipeline.RunOptions{
		ProjectRoot: projectRoot,
		Prompt:      cfg.prompt,
		Observer:    obs,
		ApeVersion:  Version,
		ManifestDir: cfg.manifestDir,
		NoCommit:    cfg.noCommit,
		AllowDirty:  cfg.allowDirty,
	})
	if err != nil {
		var pfe *pipeline.PreflightError
		if errors.As(err, &pfe) {
			fmt.Fprintf(os.Stderr, "%s\n", pfe.Error())
			os.Exit(exitCodePreflightFailed)
		}
		return err
	}
	printEndOfRunSummary(spec.Name, projectRoot, cfg)
	return nil
}

// runWithTUI runs the pipeline alongside a Bubble Tea program. The
// pipeline runs in a goroutine; observer events become tea.Msgs that
// drive the two-panel display. The TUI exits once pipelineDoneMsg
// arrives, or when the user confirms the quit modal (q / Ctrl+C with
// y, or double Ctrl+C), in which case runCancel cancels the runner's
// context — exec.CommandContext then tears down the in-flight claude
// subprocess.
func runWithTUI(ctx context.Context, spec *pipeline.Spec, projectRoot string, cfg runConfig) error {
	// Local cancel scoped to this TUI run. Wrapping the caller's ctx
	// gives the modal a dedicated cancellation handle without
	// interfering with the cobra signal-handling on the parent ctx.
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	model := tui.NewPipelineModel(spec, runCancel, projectRoot)
	program := tea.NewProgram(model, tea.WithAltScreen())
	obs := tui.NewPipelineTUIObserver(program)

	runErrCh := make(chan error, 1)
	go func() {
		err := pipeline.Run(runCtx, spec, pipeline.RunOptions{
			ProjectRoot: projectRoot,
			Prompt:      cfg.prompt,
			Observer:    obs,
			ApeVersion:  Version,
			ManifestDir: cfg.manifestDir,
			NoCommit:    cfg.noCommit,
			AllowDirty:  cfg.allowDirty,
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
	if runErr == nil {
		printEndOfRunSummary(spec.Name, projectRoot, cfg)
	}
	return runErr
}

// printEndOfRunSummary emits the post-run pointer lines:
//   - 📊 report: <path>   (PLAN-3 / M6 — always when manifest written)
//   - 📌 commits: N (...) (PLAN-4 / C8 — only when commits were made)
//
// Path is rendered relative to the project root when possible.
func printEndOfRunSummary(pipelineName, projectRoot string, cfg runConfig) {
	reportPath := pipeline.ReportPathFor(projectRoot, pipelineName, cfg.manifestDir)
	if reportPath == "" {
		return
	}
	displayPath := reportPath
	if rel, err := filepath.Rel(projectRoot, reportPath); err == nil {
		displayPath = rel
	}
	fmt.Fprintf(os.Stdout, "📊 report: %s\n", displayPath)
	if !cfg.noCommit {
		if n := pipeline.CommitsMadeFor(projectRoot, pipelineName, cfg.manifestDir); n > 0 {
			fmt.Fprintf(os.Stdout, "📌 commits: %d (run `git log --oneline --grep '^ape:%s/'` to inspect)\n", n, pipelineName)
		}
	}
}

// plainObserver writes one status line per state transition. Used when
// stdout is non-TTY or --no-tui is set. Per PLAN-1 / I4b, this
// observer also streams parsed stream-json events through the shared
// tui.RenderEvent function so log captures and CI runs see the same
// human-friendly progress feed as the interactive TUI.
type plainObserver struct {
	w            io.Writer
	t0           time.Time
	currentStage string
	currentSkill string
	// projectRoot is forwarded to the event renderer so tool-call
	// file paths display relative to the project (PLAN-2 / F6).
	projectRoot string
	// quiet suppresses the per-event stream that OnStepLine emits
	// (PLAN-2 / F5). Stage / step start+end markers and per-stage
	// summaries still print so failures and timings remain visible.
	quiet bool
}

func newPlainObserver(w io.Writer, projectRoot string, quiet bool) *plainObserver {
	return &plainObserver{w: w, t0: time.Now(), projectRoot: projectRoot, quiet: quiet}
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
	if p.quiet {
		// PLAN-2 / F5: --quiet suppresses the per-event stream. The
		// stage/step markers in OnStepStart/OnStepEnd still print,
		// so failure summaries (with captured stdout) remain visible.
		return
	}
	r := tui.RenderEventWithRoot(line, p.projectRoot)
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
