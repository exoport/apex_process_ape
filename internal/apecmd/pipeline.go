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
		fromStageFlag      string
		noCommitFlag       bool
		allowDirtyFlag     bool
		tuiFlag            bool
		evalFlag           bool
		webFlag            bool
		openFlag           bool
		ignoreProjSettings bool
		interactiveFlag    bool
		programmaticFlag   bool
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
			// PLAN-9 F2: the programmatic exec axis was removed in
			// v0.0.36 — interactive PTY is the only exec mode. The old
			// flags error with a pointer rather than silently no-op.
			if programmaticFlag || interactiveFlag || evalFlag {
				fmt.Fprintln(os.Stderr, "Error: "+removedExecFlagMessage())
				os.Exit(exitCodePreflightFailed)
			}
			mode, optOutTUI, err := resolvePipelineMode(PipelineFlags{
				TUI:   tuiFlag,
				Web:   webFlag,
				NoTUI: noTUI,
			}, os.Stderr)
			if err != nil {
				fmt.Fprintln(os.Stderr, "Error: "+err.Error())
				os.Exit(exitCodePreflightFailed)
			}
			useTUI := !optOutTUI && term.IsTerminal(int(os.Stdout.Fd())) && !mode.IsWeb()
			// PLAN-9 F3: surface the resolved rendering surface on start.
			if !quietFlag {
				fmt.Fprintf(os.Stderr, "▸ mode: %s\n", describeMode(mode))
			}
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
				fromStage:             fromStageFlag,
				noCommit:              noCommitFlag,
				allowDirty:            allowDirtyFlag,
				ignoreProjectSettings: ignoreProjSettings,
				openOnStart:           openFlag,
			}
			// PLAN-9 F2 dispatch: interactive PTY is the only exec mode,
			// so the switch is purely over the UI surface. Web routes to
			// the bridged web runner; TUI (when stdout is a terminal) to
			// the Bubble Tea runner; everything else (--no-tui, or a
			// non-terminal stdout) to the plain interactive runner.
			switch {
			case mode.IsWeb():
				return runWithWeb(ctx, spec, projectRoot, runOpts)
			case mode.IsTUI() && useTUI:
				return runWithInteractiveTUI(ctx, spec, projectRoot, runOpts)
			default:
				return runWithInteractive(ctx, spec, projectRoot, runOpts)
			}
		},
	}
	cmd.Flags().StringVar(&promptFlag, "prompt", "", "Optional prompt forwarded to skills that accept it (currently: epics)")
	cmd.Flags().BoolVar(&webFlag, "web", false, "Bridged web UI. Explicit form for scripts.")
	cmd.Flags().BoolVar(&tuiFlag, "tui", false, "Bubble Tea TUI (the default; explicit form for scripts).")
	cmd.Flags().BoolVar(&noTUI, "no-tui", false, "No UI surface: plain stdout progress lines. Exec is still the interactive per-stage claude REPL in an in-process PTY.")
	// PLAN-9 F2: the programmatic exec axis was removed in v0.0.36.
	// These flags remain registered (hidden) only so an old invocation
	// gets the actionable removal message instead of cobra's terse
	// "unknown flag". Remove after one release.
	cmd.Flags().BoolVarP(&interactiveFlag, "interactive", "I", false, "removed in v0.0.36 (interactive PTY is the only exec mode)")
	cmd.Flags().BoolVarP(&programmaticFlag, "programmatic", "P", false, "removed in v0.0.36 (interactive PTY is the only exec mode)")
	cmd.Flags().BoolVar(&evalFlag, "eval", false, "removed in v0.0.36 (interactive PTY is the only exec mode)")
	_ = cmd.Flags().MarkHidden("interactive")
	_ = cmd.Flags().MarkHidden("programmatic")
	_ = cmd.Flags().MarkHidden("eval")
	cmd.Flags().BoolVar(&openFlag, "open", false, "With --web (or default): xdg-open the broker URL on start.")
	cmd.Flags().BoolVar(&ignoreProjSettings, "ignore-project-settings", false, "Tell the spawned claude to skip project + local .claude/settings*.json. Honoured in --web mode.")
	cmd.Flags().BoolVar(&quietFlag, "quiet", false, "With --no-tui: suppress per-event stream; print only stage/step start/end markers")
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format for list mode (no positional arg): human|json|yaml")
	cmd.Flags().StringVar(&manifestDirFlag, "manifest-dir", "", "Override the directory for run manifest artifacts (default: <project>/_output/pipelines)")
	cmd.Flags().StringVar(&fromStageFlag, "from", "", "Skip stages before the named one and start execution there")
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
invocations. ape runs one interactive "claude" REPL per stage inside an
in-process PTY (never "claude -p"): steps are typed as real REPL
keystrokes following PAT-25 passthrough conventions —

    /<agent> --autonomous -- <skill> --autonomous <args>

Skills without an agent skip the passthrough hop:

    /<skill> --autonomous --no-commit <args>

Rendering surface: --tui (default) shows the Bubble Tea panels, --web
serves the bridged web UI, --no-tui prints plain stdout progress lines.

The --prompt flag is forwarded only to skills whose pipeline definition
declares prompt_flag (currently apex-create-epics-and-stories in the
"epics" pipeline). The prompt value passes through as REPL keystrokes
directly, so embedded quotes/specials survive without shell quoting.`
}

// removedExecFlagMessage is the actionable error shown when a caller
// passes one of the exec-axis flags removed in v0.0.36 (PLAN-9 F2).
func removedExecFlagMessage() string {
	return "programmatic mode was removed in v0.0.36; interactive PTY is the only exec mode. " +
		"Drop -P/--programmatic, -I/--interactive, and --eval — the run is interactive by default. " +
		"See docs/explanation/why-pty-only.md."
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

// runConfig bundles CLI-derived knobs passed to the interactive
// runners (runWithInteractive / runWithInteractiveTUI / runWithWeb) and
// to `ape task`. Grouped to keep call-sites stable as new flags land.
type runConfig struct {
	prompt                string
	manifestDir           string
	fromStage             string
	noCommit              bool
	allowDirty            bool
	ignoreProjectSettings bool
	openOnStart           bool

	// progressWriter redirects the plain observer's progress stream.
	// nil keeps the default (os.Stdout). `ape task --output-format
	// json` routes progress to stderr so stdout carries only the
	// result envelope. PLAN-11.
	progressWriter io.Writer
	// quiet suppresses the per-event stream in the plain observer
	// (same semantics as `ape pipeline --quiet` under --no-tui).
	quiet bool
	// suppressSummary skips printEndOfRunSummary — `ape task` prints
	// its own result envelope / summary instead. PLAN-11.
	suppressSummary bool
	// idleTimeout overrides the interactive step idle-without-Stop
	// backstop (default interactiveStepIdleTimeout). PLAN-11:
	// `ape task --idle-timeout`.
	idleTimeout time.Duration
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

func (p *plainObserver) OnStepStart(stage string, idx int, step pipeline.Step) {
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

func (p *plainObserver) OnStepEnd(_ string, idx int, step pipeline.Step, dur time.Duration, stepOutput string, err error) {
	if err != nil {
		fmt.Fprintf(p.w, "[%s]   step %d FAIL: %s (%s)\n", elapsed(p.t0), idx+1, step.Skill, fmtDuration(dur))
		if stepOutput != "" {
			fmt.Fprintf(p.w, "%s\n", stepOutput)
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
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}
