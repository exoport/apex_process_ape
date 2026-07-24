package apecmd

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/exoport/apex_process_ape/internal/bridge/config"
	"github.com/exoport/apex_process_ape/internal/bridge/orchestrator"
	"github.com/exoport/apex_process_ape/internal/cost"
	"github.com/exoport/apex_process_ape/internal/framework"
	"github.com/exoport/apex_process_ape/internal/output"
	"github.com/exoport/apex_process_ape/internal/repl"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/exoport/apex_process_ape/internal/sessiondriver"
)

// workflowDirective is the sentence `--workflow` appends to the
// delivered prompt: Claude Code's documented user-side opt-in for
// running a task through a multi-agent workflow. The observable session
// behavior (a workflow is invoked) is the flag's contract, not this
// exact wording.
const workflowDirective = "Run this task using a Claude Code workflow."

// ultracodeKeyword is the token `--ultracode` prepends to the delivered
// prompt: Claude Code's session-level opt-in that makes the session
// author and run workflows by default for substantive work.
const ultracodeKeyword = "ultracode"

// promptReadyTimeout bounds the wait for claude's REPL to come up inside
// the PTY. Matches the pipeline runner's interactiveReadyTimeout.
const promptReadyTimeout = 30 * time.Second

// errClaudeDied marks the "claude process exited before the Stop hook"
// cause so runPrompt can map it to ExitClaudeDied (4).
var errClaudeDied = errors.New("claude process exited before Stop hook")

// Prompt session statuses recorded on prompt.yaml and the result
// envelope.
const (
	promptStatusCompleted   = "completed"
	promptStatusFailed      = "failed"
	promptStatusIdleTimeout = "idle_timeout"
	promptStatusMaxDuration = "max_duration"
	promptStatusClaudeDied  = "claude_died"
)

// promptOptions bundles the resolved `ape prompt` invocation parameters.
type promptOptions struct {
	text                  string // positional initial prompt
	handoff               string // handoff file (mutually exclusive with text)
	agent                 string
	model                 string
	effort                string
	workflow              bool
	ultracode             bool
	idleTimeout           time.Duration
	maxDuration           time.Duration
	projectRoot           string
	quiet                 bool
	ignoreProjectSettings bool
	format                output.Format
}

func newPromptCmd() *cobra.Command {
	var (
		handoffFlag        string
		agentFlag          string
		modelFlag          string
		effortFlag         string
		workflowFlag       bool
		ultracodeFlag      bool
		idleTimeoutFlag    time.Duration
		maxDurationFlag    time.Duration
		cwdFlag            string
		quietFlag          bool
		ignoreProjSettings bool
		outputFormat       string
	)
	cmd := &cobra.Command{
		Use:   "prompt [text]",
		Short: "Drive an unattended Claude session from a prompt or a handoff file",
		Long: `Run one unattended Claude Code session end-to-end: spawn claude in
an in-process PTY, deliver a prompt (or seed the session from a handoff
document), let it work under the ape bridge's hook supervision, detect
completion via the Stop hook, capture the transcript + per-model
telemetry, and exit with a meaningful status.

Exactly one of the positional <text> or --handoff <file> must be given.

  ape prompt "add a CHANGELOG entry for the latest release"
  ape prompt --handoff development/handoffs/2026-07-13-resume.md
  ape prompt "refactor the parser" --agent apex-agent-dev --workflow
  ape prompt "big refactor" --ultracode --model "opus[1m]"

Prompt assembly:
  --agent A        the delivered line is "/A --autonomous -- <prompt>"
                   (no agent: the prompt is sent as a plain message).
  --handoff F      the prompt becomes "Read the handoff document at
                   <abs F> and continue the work it describes."
  --ultracode      prepends the "ultracode" keyword to the prompt
                   (session runs workflows by default).
  --workflow       appends an explicit "run this via a workflow"
                   directive. Independent of --ultracode; both compose.

Records land under <project>/_output/ape/prompts/<prompt-id>/ (runlog
streams + copied transcript + prompt.yaml session record) and fold into
the project cost rollup's Prompts bucket.

ape prompt must run from a project root (a directory with
_apex/config.yaml). It makes no commits of its own.

Exit codes: 0 session completed (Stop hook) · 1 idle-timeout or session
failed · 2 usage or preflight error (no _apex/config.yaml, unresolved
--agent, missing --handoff file) · 3 the claude REPL never became ready
· 4 claude exited before the Stop hook.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			format := output.Format(outputFormat)
			if format != output.FormatHuman && format != output.FormatJSON && format != output.FormatYAML {
				fmt.Fprintf(os.Stderr, "Error: --output-format must be human, json, or yaml, got %q\n", outputFormat)
				os.Exit(ExitUsage)
			}

			var text string
			if len(args) == 1 {
				text = args[0]
			}
			// Exactly one of <text> / --handoff.
			switch {
			case text == "" && handoffFlag == "":
				fmt.Fprintln(os.Stderr, "Error: provide an initial prompt as the positional argument or --handoff <file>")
				os.Exit(ExitUsage)
			case text != "" && handoffFlag != "":
				fmt.Fprintln(os.Stderr, "Error: <text> and --handoff are mutually exclusive")
				os.Exit(ExitUsage)
			}

			projectRoot := cwdFlag
			if projectRoot == "" {
				wd, err := os.Getwd()
				if err != nil {
					fmt.Fprintf(os.Stderr, "Error: cannot determine working directory: %s\n", err)
					os.Exit(ExitUsage)
				}
				projectRoot = wd
			}
			cfgPath := filepath.Join(projectRoot, "_apex", "config.yaml")
			if _, err := os.Stat(cfgPath); err != nil {
				fmt.Fprintf(os.Stderr, "Error: ape prompt requires a project root with _apex/config.yaml; not found at %s\n", cfgPath)
				os.Exit(ExitUsage)
			}

			return runPrompt(cmd.Context(), promptOptions{
				text:                  text,
				handoff:               handoffFlag,
				agent:                 agentFlag,
				model:                 modelFlag,
				effort:                effortFlag,
				workflow:              workflowFlag,
				ultracode:             ultracodeFlag,
				idleTimeout:           idleTimeoutFlag,
				maxDuration:           maxDurationFlag,
				projectRoot:           projectRoot,
				quiet:                 quietFlag,
				ignoreProjectSettings: ignoreProjSettings,
				format:                format,
			})
		},
	}
	cmd.Flags().StringVar(&handoffFlag, "handoff", "", "Handoff document to seed the session with (mutually exclusive with the positional prompt)")
	cmd.Flags().StringVar(&agentFlag, "agent", "", "Framework agent fronting the session: /<agent> --autonomous -- <prompt>")
	cmd.Flags().StringVar(&modelFlag, "model", "", "Claude model for the session (e.g. \"opus[1m]\")")
	cmd.Flags().StringVar(&effortFlag, "effort", "", "Reasoning effort for the session and its sub-agents (low|medium|high|xhigh|max). Default xhigh when unset.")
	cmd.Flags().BoolVar(&workflowFlag, "workflow", false, "Append a directive to run the task through a Claude Code workflow")
	cmd.Flags().BoolVar(&ultracodeFlag, "ultracode", false, "Prepend the ultracode keyword (session runs workflows by default)")
	cmd.Flags().DurationVar(&idleTimeoutFlag, "idle-timeout", 0, "Idle backstop: end the session only after this long with no progress across hooks, transcript growth, or PTY output (e.g. 15m); default matches the pipeline (60m)")
	cmd.Flags().DurationVar(&maxDurationFlag, "max-duration", sessiondriver.DefaultMaxDuration, "Hard wall-clock ceiling regardless of progress (e.g. 3h); the clock resets on each sub-agent boundary, so a batch of sub-agents is bounded per item, not overall. 0 disables the cap.")
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", "Project root directory (default: current working dir)")
	cmd.Flags().BoolVar(&quietFlag, "quiet", false, "Suppress the progress stream on stderr")
	cmd.Flags().BoolVar(&ignoreProjSettings, "ignore-project-settings", false, "Tell the spawned claude to skip project + local .claude/settings*.json")
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json|yaml (json/yaml = result envelope on stdout, progress on stderr)")
	return cmd
}

// resolveDeliveredPrompt derives the base prompt body from the options
// (positional text or a handoff envelope) and applies the --ultracode /
// --workflow decorations. Returns a preflight error when --handoff
// names a file that does not exist.
func resolveDeliveredPrompt(o promptOptions) (string, error) {
	base := o.text
	if o.handoff != "" {
		abs, err := filepath.Abs(o.handoff)
		if err != nil {
			return "", fmt.Errorf("resolve --handoff path: %w", err)
		}
		if _, err := os.Stat(abs); err != nil {
			return "", fmt.Errorf("--handoff file not found: %s", abs)
		}
		base = fmt.Sprintf("Read the handoff document at %s and continue the work it describes.", abs)
	}
	if o.ultracode {
		base = ultracodeKeyword + " " + base
	}
	if o.workflow {
		base = strings.TrimSpace(base) + " " + workflowDirective
	}
	return base, nil
}

// assemblePromptLine wraps the delivered prompt in the framework agent's
// PAT-25 slash prefix when --agent is set, or returns it verbatim (a
// plain user message) otherwise.
func assemblePromptLine(agent, delivered string) string {
	if agent != "" {
		return fmt.Sprintf("/%s --autonomous -- %s", agent, delivered)
	}
	return delivered
}

// promptResult is the `--output-format json|yaml` envelope printed on
// stdout. snake_case is the wire contract.
//
//nolint:tagliatelle // envelope field names are the wire contract
type promptResult struct {
	PromptID        string                 `json:"prompt_id"           yaml:"prompt_id"`
	Status          string                 `json:"status"              yaml:"status"`
	DurationSeconds float64                `json:"duration"            yaml:"duration"`
	CostUSD         float64                `json:"cost_usd"            yaml:"cost_usd"`
	PerModel        map[string]cost.Totals `json:"per_model,omitempty" yaml:"per_model,omitempty"`
	TranscriptPaths []string               `json:"transcript_paths"    yaml:"transcript_paths"`
	SessionID       string                 `json:"session_id"          yaml:"session_id"`

	// Unexported carry-fields for the CLI summary printer; never serialized.
	telemetry *sessiondriver.Telemetry
	runDir    string
	waitErr   error
}

// runPrompt is the `ape prompt` CLI entrypoint: it runs the session core,
// prints the human summary or the structured envelope, and exits with the
// PLAN-12 status code. All the session machinery lives in runPromptCore so
// `apescript.RunPrompt` can drive the identical path without os.Exit or CLI
// printing.
func runPrompt(ctx context.Context, o promptOptions) error {
	res, exitCode, outcomeErr := runPromptCore(ctx, o)

	if exitCode == ExitUsage || exitCode == ExitREPLNotReady {
		// Preflight / never-ready: no result envelope, just the error + exit.
		if outcomeErr != nil {
			fmt.Fprintf(os.Stderr, "Error: %s\n", outcomeErr.Error())
		}
		os.Exit(exitCode)
	}
	if res.PromptID == "" && outcomeErr != nil {
		// Setup error before a session id existed (e.g. spawn failure).
		return outcomeErr
	}

	if o.format == output.FormatHuman {
		printPromptSummary(res, res.telemetry, res.runDir, o.projectRoot, res.waitErr)
	} else if err := output.Print(os.Stdout, o.format, res); err != nil {
		return err
	}

	if exitCode != ExitOK {
		if res.waitErr != nil {
			fmt.Fprintf(os.Stderr, "Error: %s\n", res.waitErr.Error())
		}
		os.Exit(exitCode)
	}
	return nil
}

// runPromptCore scaffolds the bridge + runlog like `ape chat`, spawns claude
// in the in-process PTY, delivers the assembled prompt, waits on the Stop
// hook (with idle-timeout and process-death backstops), scans telemetry,
// writes the session record, folds the cost rollup, and returns the result
// plus a PLAN-12 exit code. It never calls os.Exit and never prints a summary
// — the caller (CLI or apescript facade) owns those. The returned exit code is
// ExitUsage (preflight), ExitREPLNotReady (REPL never ready), or the mapping
// of the wait outcome via promptStatus.
func runPromptCore(ctx context.Context, o promptOptions) (promptResult, int, error) {
	// Preflight (exit 2): agent must resolve; handoff/prompt derivation
	// must succeed. Detected before any claude process spawns.
	if o.agent != "" {
		if _, _, found := framework.ResolveSkill(o.agent, o.projectRoot); !found {
			return promptResult{}, ExitUsage, fmt.Errorf("--agent %q did not resolve under .claude/skills (project or user)", o.agent)
		}
	}
	delivered, err := resolveDeliveredPrompt(o)
	if err != nil {
		return promptResult{}, ExitUsage, err
	}
	promptLine := assemblePromptLine(o.agent, delivered)

	apeBin, err := os.Executable()
	if err != nil {
		return promptResult{}, ExitRunFailed, fmt.Errorf("ape prompt: locate self: %w", err)
	}

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()

	start := time.Now()
	promptID := runlog.NewChatID(start, o.projectRoot, os.Getpid())
	runDir := filepath.Join(o.projectRoot, "_output", "ape", "prompts", promptID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return promptResult{}, ExitRunFailed, fmt.Errorf("ape prompt: create record dir: %w", err)
	}

	var (
		runLogMu sync.Mutex
		rl       *runlog.Writer
	)
	if w, openErr := runlog.New(runDir); openErr == nil {
		rl = w
	}
	getRunLog := func() *runlog.Writer {
		runLogMu.Lock()
		defer runLogMu.Unlock()
		return rl
	}

	driver := sessiondriver.NewDriver(getRunLog, o.idleTimeout)
	driver.SetMaxDuration(o.maxDuration)

	rt := orchestrator.NewBridgeRuntime(orchestrator.BridgeRuntimeOptions{
		OnHook:  driver.FeedHook,
		OnCall:  driver.FeedCall,
		OnReply: driver.FeedReply,
	})
	if err := rt.Listen(runCtx); err != nil {
		return promptResult{}, ExitRunFailed, fmt.Errorf("ape prompt: runtime listen: %w", err)
	}
	rt.SetStopFn(runCancel)
	rtErrCh := make(chan error, 1)
	go func() { rtErrCh <- rt.Serve(runCtx) }()
	defer func() {
		runCancel()
		<-rtErrCh
		runLogMu.Lock()
		if rl != nil {
			_ = rl.Close()
			rl = nil
		}
		runLogMu.Unlock()
	}()

	prepend, err := buildInteractivePrepend(apeBin, rt.IPCPort(), config.ModeTUI, o.ignoreProjectSettings)
	if err != nil {
		return promptResult{}, ExitRunFailed, err
	}
	args := append([]string{}, prepend...)
	args = append(args, "--dangerously-skip-permissions")
	if o.model != "" {
		args = append(args, "--model", o.model)
	}
	argv := append([]string{"claude"}, args...)

	progressf := func(format string, a ...any) {
		if !o.quiet {
			fmt.Fprintf(os.Stderr, format, a...)
		}
	}
	progressf("ape prompt: bridged claude (id %s)\n  record: %s\n", promptID, runDir)

	sessionName := fmt.Sprintf("ape-prompt-%d", os.Getpid())
	_ = repl.KillSession(runCtx, sessionName)
	// Inject the resolved reasoning effort (default xhigh) so it reaches the
	// session and any sub-agents it spawns; repl.EffortEnv applies the default.
	if err := repl.NewSessionWithEnv(runCtx, sessionName, o.projectRoot, argv, repl.EffortEnv(o.effort)); err != nil {
		return promptResult{}, ExitRunFailed, fmt.Errorf("ape prompt: spawn claude: %w", err)
	}
	defer func() { _ = repl.KillSession(context.Background(), sessionName) }() //nolint:contextcheck // cleanup-on-exit

	// PLAN-19 D1/D4: feed the Driver the PTY-output progress signal and the
	// child-liveness probe now that the session exists. The transcript-growth
	// anchor binds itself from the UserPromptSubmit hook (driver.FeedHook).
	driver.SetPTYProbe(func() (time.Time, bool) { return repl.LastOutputAt(sessionName) })
	driver.SetChildAliveProbe(func() (int, bool) { //nolint:contextcheck // liveness snapshot; the probe takes no ctx
		pid, _ := repl.SessionPID(sessionName)
		return pid, repl.HasSession(context.Background(), sessionName)
	})

	driver.Begin()

	readyCtx, cancelReady := context.WithTimeout(runCtx, promptReadyTimeout)
	readyErr := repl.WaitForReady(readyCtx, sessionName)
	cancelReady()
	if readyErr != nil {
		// The claude REPL never became ready (exit 3). The NotReadyError
		// carries the last pane snapshot for diagnosis.
		return promptResult{}, ExitREPLNotReady, readyErr
	}

	// If claude exits before the Stop hook, cancel the wait immediately
	// (exit 4) instead of idling for the full idle-timeout window.
	sessionCtx, cancelSession := context.WithCancelCause(runCtx)
	defer cancelSession(nil)
	if sessionDone := repl.SessionDone(runCtx, sessionName); sessionDone != nil {
		go func() {
			select {
			case <-sessionDone:
				cancelSession(errClaudeDied)
			case <-sessionCtx.Done():
			}
		}()
	}

	progressf("ape prompt: delivering prompt…\n")
	if err := repl.SendCommand(runCtx, sessionName, promptLine); err != nil {
		return promptResult{}, ExitRunFailed, fmt.Errorf("ape prompt: deliver prompt: %w", err)
	}

	waitErr := driver.WaitStepDone(sessionCtx)
	if errors.Is(waitErr, context.Canceled) {
		if cause := context.Cause(sessionCtx); cause != nil && !errors.Is(cause, context.Canceled) {
			waitErr = cause
		}
	}

	tele := driver.Telemetry()
	duration := time.Since(start)
	status, exitCode := promptStatus(waitErr)

	// Persist the session record + fold the rollup. Best-effort: a
	// record write failure must not change the run's exit code.
	perModel := perModelTotals(tele)
	writePromptRecord(runDir, promptID, o, driver.SessionID(), status, start, tele, perModel)
	if _, rerr := cost.RebuildRollup(o.projectRoot); rerr != nil {
		progressf("ape prompt: rebuild cost rollup: %v\n", rerr)
	}

	res := promptResult{
		PromptID:        promptID,
		Status:          status,
		DurationSeconds: duration.Seconds(),
		CostUSD:         tele.Totals.CostUSD,
		PerModel:        perModel,
		TranscriptPaths: collectTranscriptPaths(o.projectRoot, runDir),
		SessionID:       driver.SessionID(),
		telemetry:       tele,
		runDir:          runDir,
		waitErr:         waitErr,
	}
	return res, exitCode, waitErr
}

// promptStatus maps the wait outcome onto a (status, exit-code) pair.
// The idle vs max-duration split (PLAN-19 D2) surfaces on the record +
// envelope so an operator can tell a stall from a hit ceiling.
func promptStatus(waitErr error) (status string, code int) {
	var ite *sessiondriver.IdleTimeoutError
	var mde *sessiondriver.MaxDurationError
	switch {
	case waitErr == nil:
		return promptStatusCompleted, ExitOK
	case errors.Is(waitErr, errClaudeDied):
		return promptStatusClaudeDied, ExitClaudeDied
	case errors.As(waitErr, &mde):
		return promptStatusMaxDuration, ExitRunFailed
	case errors.As(waitErr, &ite):
		return promptStatusIdleTimeout, ExitRunFailed
	default:
		return promptStatusFailed, ExitRunFailed
	}
}

// perModelTotals converts a telemetry per-model map to the envelope /
// record shape (cost.Totals per normalized model id). nil in → nil out.
func perModelTotals(tele *sessiondriver.Telemetry) map[string]cost.Totals {
	if len(tele.ByModel) == 0 {
		return nil
	}
	out := make(map[string]cost.Totals, len(tele.ByModel))
	maps.Copy(out, tele.ByModel)
	return out
}

// writePromptRecord persists prompt.yaml. Best-effort.
func writePromptRecord(runDir, promptID string, o promptOptions, sessionID, status string, start time.Time, tele *sessiondriver.Telemetry, perModel map[string]cost.Totals) {
	meta := runlog.PromptMeta{
		PromptID:  promptID,
		StartedAt: start,
		EndedAt:   time.Now(),
		Status:    status,
		Agent:     o.agent,
		Model:     o.model,
		SessionID: sessionID,
		CostUSD:   tele.Totals.CostUSD,
		TokensIn:  tele.Totals.InputTokens,
		TokensOut: tele.Totals.OutputTokens,
		NumTurns:  tele.Totals.NumTurns,
	}
	if len(perModel) > 0 {
		meta.PerModel = map[string]runlog.PromptModelUsage{}
		for model, t := range perModel {
			meta.PerModel[model] = runlog.PromptModelUsage{
				CostUSD:             t.CostUSD,
				TokensInput:         t.InputTokens,
				TokensOutput:        t.OutputTokens,
				TokensCacheRead:     t.CacheReadTokens,
				TokensCacheCreation: t.CacheCreationTokens,
				NumTurns:            t.NumTurns,
			}
		}
	}
	_ = runlog.WritePromptYAML(runDir, meta)
}

// collectTranscriptPaths lists the copied transcripts under
// <runDir>/transcripts/, project-relative when possible, sorted.
func collectTranscriptPaths(projectRoot, runDir string) []string {
	dir := filepath.Join(runDir, "transcripts")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{}
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if rel, relErr := filepath.Rel(projectRoot, p); relErr == nil {
			p = rel
		}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// printPromptSummary emits the human-mode post-run lines.
func printPromptSummary(res promptResult, tele *sessiondriver.Telemetry, runDir, projectRoot string, waitErr error) {
	glyph := "✅"
	if waitErr != nil {
		glyph = "❌"
	}
	fmt.Fprintf(os.Stdout, "%s prompt %s — %s in %.1fs — $%.2f, %d turn(s)\n",
		glyph, res.PromptID, res.Status, res.DurationSeconds, res.CostUSD, tele.Totals.NumTurns)
	display := runDir
	if rel, err := filepath.Rel(projectRoot, runDir); err == nil {
		display = rel
	}
	fmt.Fprintf(os.Stdout, "📁 record: %s\n", display)
	if tele.Note != "" {
		fmt.Fprintf(os.Stdout, "⚠ telemetry: %s\n", tele.Note)
	}
}
