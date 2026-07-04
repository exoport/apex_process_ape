package apecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/diegosz/apex_process_ape/internal/pipeline"
	"github.com/diegosz/apex_process_ape/internal/repl"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// `ape task` uses the shared exit-code table in exitcodes.go
// (ExitOK / ExitRunFailed / ExitUsage / ExitREPLNotReady).

// taskCommitDerivedSentinel is the NoOptDefVal for a bare
// `--task-commit` (no message). Contains a control byte so it cannot
// collide with a user-typed message.
const taskCommitDerivedSentinel = "\x01derived"

func newTaskCmd() *cobra.Command {
	var (
		agentFlag          string
		modelFlag          string
		argsFlag           string
		promptFlag         string
		promptFlagName     string
		noCommitFlag       bool
		taskCommitFlag     string
		allowDirtyFlag     bool
		idleTimeoutFlag    time.Duration
		outputFormat       string
		jsonAlias          bool
		cwdFlag            string
		quietFlag          bool
		manifestDirFlag    string
		ignoreProjSettings bool
	)
	cmd := &cobra.Command{
		Use:   "task <skill>",
		Short: "Run a single framework skill through the interactive PTY runner",
		Long: `Run one framework skill as a single-step interactive run — everything a
pipeline step gets (agent prefix, preflight, bridge hooks, manifest,
telemetry) with all parameters passed as flags instead of a pipeline
YAML file. Execution is PTY-interactive only: claude runs as a REPL,
the prompt is typed as keystrokes, and completion is detected via the
bridge Stop hook.

Commit control is two-layered:
  --no-commit     skill layer — tells the skill/framework not to commit
                  (the no-agent invocation shape already carries it).
  --task-commit   task layer — opt-in git commit of the complete task at
                  the end of the run. Off by default. A bare flag derives
                  the message "ape:task/<skill>".

Run artifacts land under <project>/_output/tasks/<skill>/<run-id>/
(manifest.yaml, per-step ndjson, runlog streams).

Exit codes: 0 success · 1 run failed or idle timeout · 2 usage or
preflight error · 3 REPL never became ready (last pane on stderr).`,
		Example: `  ape task apex-shard-doc --args "--doc prd"
  ape task apex-create-prd --agent apex-agent-pm --model "opus[1m]" --prompt "a greeter CLI" --prompt-flag --prompt
  ape task apex-shard-doc --task-commit "chore: shard prd"
  ape task apex-create-prd --agent apex-agent-pm --output-format json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonMode := jsonAlias || outputFormat == "json"
			if !jsonMode && outputFormat != "human" {
				fmt.Fprintf(os.Stderr, "Error: --output-format must be human or json, got %q\n", outputFormat)
				os.Exit(ExitUsage)
			}
			projectRoot := cwdFlag
			if projectRoot == "" {
				wd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("cannot determine working directory: %w", err)
				}
				projectRoot = wd
			}
			var taskCommit *pipeline.CommitDirective
			if cmd.Flags().Changed("task-commit") {
				msg := taskCommitFlag
				if msg == taskCommitDerivedSentinel {
					msg = pipeline.DerivedTaskCommitMessage(args[0])
				}
				if strings.TrimSpace(msg) == "" {
					fmt.Fprintln(os.Stderr, "Error: --task-commit message cannot be empty")
					os.Exit(ExitUsage)
				}
				taskCommit = &pipeline.CommitDirective{Mode: pipeline.CommitModeExplicit, Message: msg}
			}
			opts := taskOptions{
				skill:                 args[0],
				agent:                 agentFlag,
				model:                 modelFlag,
				args:                  argsFlag,
				prompt:                promptFlag,
				promptFlagName:        promptFlagName,
				skillNoCommit:         noCommitFlag,
				taskCommit:            taskCommit,
				allowDirty:            allowDirtyFlag,
				idleTimeout:           idleTimeoutFlag,
				jsonMode:              jsonMode,
				quiet:                 quietFlag,
				projectRoot:           projectRoot,
				manifestDir:           manifestDirFlag,
				ignoreProjectSettings: ignoreProjSettings,
			}
			return runTask(cmd.Context(), opts)
		},
	}
	cmd.Flags().StringVar(&agentFlag, "agent", "", "Framework agent (slash-command) fronting the skill: /<agent> --autonomous -- <skill> ...")
	cmd.Flags().StringVar(&modelFlag, "model", "", "Claude model for the session (e.g. \"opus[1m]\")")
	cmd.Flags().StringVar(&argsFlag, "args", "", "Verbatim skill args appended to the invocation (whitespace-separated)")
	cmd.Flags().StringVar(&promptFlag, "prompt", "", "Run prompt forwarded via --prompt-flag (same semantics as pipeline --prompt)")
	cmd.Flags().StringVar(&promptFlagName, "prompt-flag", "", "Skill flag name the --prompt value is forwarded through (spec prompt_flag equivalent)")
	cmd.Flags().BoolVar(&noCommitFlag, "no-commit", false, "Skill layer: tell the skill/framework not to commit (adds skill-level --no-commit on the agent path)")
	cmd.Flags().StringVar(&taskCommitFlag, "task-commit", "", "Task layer: commit the complete task at the end; bare flag derives \"ape:task/<skill>\"")
	cmd.Flags().Lookup("task-commit").NoOptDefVal = taskCommitDerivedSentinel
	cmd.Flags().BoolVar(&allowDirtyFlag, "commit-allow-dirty", false, "Bypass the dirty-tree gate (relevant only with --task-commit)")
	cmd.Flags().DurationVar(&idleTimeoutFlag, "idle-timeout", 0, "Idle-without-Stop backstop (e.g. 15m); default matches pipeline (60m)")
	cmd.Flags().StringVar(&outputFormat, "output-format", "human", "Output format: human|json (json = result envelope on stdout, progress on stderr)")
	cmd.Flags().BoolVar(&jsonAlias, "json", false, "Alias for --output-format json")
	cmd.Flags().BoolVar(&quietFlag, "quiet", false, "Suppress the per-event progress stream")
	cmd.Flags().StringVar(&manifestDirFlag, "manifest-dir", "", "Override the run-artifact base dir (default: <project>/_output/tasks)")
	cmd.Flags().BoolVar(&ignoreProjSettings, "ignore-project-settings", false, "Tell the spawned claude to skip project + local .claude/settings*.json")
	_ = cmd.Flags().MarkHidden("json")
	cmd.Flags().StringVar(&cwdFlag, "cwd", "", "Project root directory (default: current working dir)")
	return cmd
}

// taskOptions bundles the resolved `ape task` invocation parameters.
type taskOptions struct {
	skill                 string
	agent                 string
	model                 string
	args                  string
	prompt                string
	promptFlagName        string
	skillNoCommit         bool
	taskCommit            *pipeline.CommitDirective
	allowDirty            bool
	idleTimeout           time.Duration
	jsonMode              bool
	quiet                 bool
	projectRoot           string
	manifestDir           string
	ignoreProjectSettings bool
}

// buildTaskStep maps taskOptions onto a pipeline.Step. The skill-layer
// --no-commit is injected by prefixing step.Args on the agent path
// only — assembleInteractivePromptLine already adds it on the no-agent
// path by PAT-25 convention, so injecting there would duplicate it.
func buildTaskStep(o taskOptions) pipeline.Step {
	step := pipeline.Step{
		Skill:      o.skill,
		Agent:      o.agent,
		Model:      o.model,
		Args:       o.args,
		PromptFlag: o.promptFlagName,
	}
	if o.skillNoCommit && o.agent != "" {
		step.Args = strings.TrimSpace("--no-commit " + step.Args)
	}
	return step
}

// taskUsage mirrors the stream-json result-event usage block shape so
// the envelope drops into consumers that previously read `claude -p`
// output (the eval harness). snake_case is the wire contract.
//
//nolint:tagliatelle // envelope mirrors the stream-json result-event field names
type taskUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	// Ephemeral cache-write split (PLAN-10 D1). Additive — the summed
	// cache_creation_input_tokens above is unchanged; these break it into
	// the 5m/1h tiers for consumers that price them differently.
	CacheCreation5mInputTokens int `json:"cache_creation_5m_input_tokens"`
	CacheCreation1hInputTokens int `json:"cache_creation_1h_input_tokens"`
	NumTurns                   int `json:"num_turns"`
}

// taskModelUsage is one model's (or one session's) usage share on the
// envelope. snake_case is the wire contract.
//
//nolint:tagliatelle // envelope mirrors the stream-json result-event field names
type taskModelUsage struct {
	CostUSD                    float64 `json:"cost_usd"`
	InputTokens                int     `json:"input_tokens"`
	OutputTokens               int     `json:"output_tokens"`
	CacheReadInputTokens       int     `json:"cache_read_input_tokens"`
	CacheCreationInputTokens   int     `json:"cache_creation_input_tokens"`
	CacheCreation5mInputTokens int     `json:"cache_creation_5m_input_tokens"`
	CacheCreation1hInputTokens int     `json:"cache_creation_1h_input_tokens"`
	NumTurns                   int     `json:"num_turns"`
}

// taskSessionUsage is one claude session's usage within the run: the
// main REPL session or a sub-agent (Agent tool) session.
//
//nolint:tagliatelle // envelope mirrors the manifest's snake_case contract
type taskSessionUsage struct {
	SessionID       string                    `json:"session_id"`
	ParentSessionID string                    `json:"parent_session_id,omitempty"`
	CostUSD         float64                   `json:"cost_usd"`
	InputTokens     int                       `json:"input_tokens"`
	OutputTokens    int                       `json:"output_tokens"`
	NumTurns        int                       `json:"num_turns"`
	ModelUsage      map[string]taskModelUsage `json:"model_usage,omitempty"`
}

// taskEnvelope is the `--output-format json` result printed on stdout.
// snake_case is the wire contract (see taskUsage).
//
//nolint:tagliatelle // envelope mirrors the stream-json result-event field names
type taskEnvelope struct {
	Skill           string                    `json:"skill"`
	Agent           string                    `json:"agent,omitempty"`
	Model           string                    `json:"model,omitempty"`
	Success         bool                      `json:"success"`
	ExitCode        int                       `json:"exit_code"`
	DurationSeconds float64                   `json:"duration_seconds"`
	CostUSD         float64                   `json:"cost_usd"`
	Usage           taskUsage                 `json:"usage"`
	ModelUsage      map[string]taskModelUsage `json:"model_usage,omitempty"`
	Sessions        []taskSessionUsage        `json:"sessions,omitempty"`
	Commits         []string                  `json:"commits"`
	ManifestPath    string                    `json:"manifest_path,omitempty"`
	TelemetryNote   string                    `json:"telemetry_note,omitempty"`
	Error           *string                   `json:"error"`
}

// taskExitCode maps a run error onto the PLAN-11 exit-code table.
func taskExitCode(runErr error) int {
	if runErr == nil {
		return ExitOK
	}
	var nre *repl.NotReadyError
	if errors.As(runErr, &nre) {
		return ExitREPLNotReady
	}
	return ExitRunFailed
}

func runTask(ctx context.Context, o taskOptions) error {
	step := buildTaskStep(o)
	spec := pipeline.NewSingleStepSpec(o.skill, step, o.taskCommit)

	manifestDir := o.manifestDir
	if manifestDir == "" {
		manifestDir = filepath.Join(o.projectRoot, "_output", "tasks")
	}

	headBefore := gitHeadFull(ctx, o.projectRoot)

	cfg := runConfig{
		prompt:                o.prompt,
		manifestDir:           manifestDir,
		allowDirty:            o.allowDirty,
		ignoreProjectSettings: o.ignoreProjectSettings,
		quiet:                 o.quiet,
		suppressSummary:       o.jsonMode,
		idleTimeout:           o.idleTimeout,
	}
	if o.jsonMode {
		// stdout carries only the envelope; progress goes to stderr.
		cfg.progressWriter = os.Stderr
	}

	start := time.Now()
	runErr := runWithInteractive(ctx, spec, o.projectRoot, cfg)
	duration := time.Since(start)

	exitCode := taskExitCode(runErr)
	env := taskEnvelope{
		Skill:           o.skill,
		Agent:           o.agent,
		Model:           o.model,
		Success:         runErr == nil,
		ExitCode:        exitCode,
		DurationSeconds: duration.Seconds(),
		Commits:         gitCommitSubjectsSince(ctx, o.projectRoot, headBefore),
	}
	if runErr != nil {
		msg := runErr.Error()
		env.Error = &msg
	}
	fillEnvelopeFromManifest(&env, o.projectRoot, o.skill, manifestDir)

	if o.jsonMode {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(env); err != nil {
			return err
		}
	} else {
		printTaskSummary(env, runErr)
	}
	if exitCode != ExitOK {
		// exitCode != 0 implies runErr != nil (see taskExitCode). A
		// NotReadyError's text carries the last pane snapshot, so an
		// unknown blocking modal is diagnosable straight from stderr.
		fmt.Fprintf(os.Stderr, "Error: %s\n", runErr.Error())
		os.Exit(exitCode)
	}
	return nil
}

// fillEnvelopeFromManifest lifts cost / usage / turns from the run's
// manifest.yaml (via the `latest` symlink). Best-effort: a missing or
// unreadable manifest leaves the telemetry fields zero.
func fillEnvelopeFromManifest(env *taskEnvelope, projectRoot, skill, manifestDir string) {
	runDir := pipeline.ResolveLatestRunDir(projectRoot, skill, manifestDir)
	if runDir == "" {
		return
	}
	manifestPath := filepath.Join(runDir, "manifest.yaml")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return
	}
	var m pipeline.Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return
	}
	if rel, relErr := filepath.Rel(projectRoot, manifestPath); relErr == nil {
		env.ManifestPath = rel
	} else {
		env.ManifestPath = manifestPath
	}
	env.CostUSD = m.Totals.CostUSD
	env.Usage.InputTokens = m.Totals.TokensInput
	env.Usage.OutputTokens = m.Totals.TokensOutput
	env.Usage.CacheReadInputTokens = m.Totals.TokensCacheRead
	env.Usage.CacheCreationInputTokens = m.Totals.TokensCacheCreation
	env.Usage.CacheCreation5mInputTokens = m.Totals.TokensCacheCreation5m
	env.Usage.CacheCreation1hInputTokens = m.Totals.TokensCacheCreation1h
	env.ModelUsage = modelUsageRecordsToEnvelope(m.Totals.ModelUsage)
	for i := range m.Stages {
		for j := range m.Stages[i].Steps {
			step := &m.Stages[i].Steps[j]
			env.Usage.NumTurns += step.NumTurns
			if step.TelemetryNote != "" && env.TelemetryNote == "" {
				env.TelemetryNote = step.TelemetryNote
			}
			for _, s := range step.Sessions {
				env.Sessions = append(env.Sessions, taskSessionUsage{
					SessionID:       s.SessionID,
					ParentSessionID: s.ParentSessionID,
					CostUSD:         s.CostUSD,
					InputTokens:     s.TokensInput,
					OutputTokens:    s.TokensOutput,
					NumTurns:        s.NumTurns,
					ModelUsage:      modelUsageRecordsToEnvelope(s.ModelUsage),
				})
			}
		}
	}
}

// modelUsageRecordsToEnvelope converts manifest model_usage records to
// the envelope shape. nil in → nil out (field omitted).
func modelUsageRecordsToEnvelope(mu map[string]pipeline.ModelUsageRecord) map[string]taskModelUsage {
	if len(mu) == 0 {
		return nil
	}
	out := make(map[string]taskModelUsage, len(mu))
	for model, u := range mu {
		out[model] = taskModelUsage{
			CostUSD:                    u.CostUSD,
			InputTokens:                u.TokensInput,
			OutputTokens:               u.TokensOutput,
			CacheReadInputTokens:       u.TokensCacheRead,
			CacheCreationInputTokens:   u.TokensCacheCreation,
			CacheCreation5mInputTokens: u.TokensCacheCreation5m,
			CacheCreation1hInputTokens: u.TokensCacheCreation1h,
			NumTurns:                   u.NumTurns,
		}
	}
	return out
}

// printTaskSummary emits the human-mode post-run lines.
func printTaskSummary(env taskEnvelope, runErr error) {
	if runErr == nil {
		fmt.Fprintf(os.Stdout, "✅ task %s done in %.1fs — $%.2f, %d turn(s)\n",
			env.Skill, env.DurationSeconds, env.CostUSD, env.Usage.NumTurns)
	}
	if env.ManifestPath != "" {
		fmt.Fprintf(os.Stdout, "📊 manifest: %s\n", env.ManifestPath)
	}
	if len(env.Commits) > 0 {
		fmt.Fprintf(os.Stdout, "📌 commits: %d\n", len(env.Commits))
		for _, c := range env.Commits {
			fmt.Fprintf(os.Stdout, "   %s\n", c)
		}
	}
}

// gitHeadFull returns the full HEAD SHA of projectRoot, or "" when
// unavailable (not a repo, no commits yet, git missing).
func gitHeadFull(ctx context.Context, projectRoot string) string {
	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir = projectRoot
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return ""
	}
	return strings.TrimSpace(stdout.String())
}

// gitCommitSubjectsSince returns the subjects of commits made after
// `before` (oldest first) — the run's complete commit trail, framework
// commits included, not just ape's boundary commit. Best-effort.
func gitCommitSubjectsSince(ctx context.Context, projectRoot, before string) []string {
	if before == "" {
		return []string{}
	}
	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "log", "--reverse", "--format=%s", before+"..HEAD") //nolint:gosec // `before` is a SHA captured from rev-parse at run start, not user input
	cmd.Dir = projectRoot
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return []string{}
	}
	out := strings.TrimSpace(stdout.String())
	if out == "" {
		return []string{}
	}
	return strings.Split(out, "\n")
}
