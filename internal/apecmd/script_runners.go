package apecmd

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/exoport/apex_process_ape/apescript"
	"github.com/exoport/apex_process_ape/internal/cost"
	"github.com/exoport/apex_process_ape/internal/eventing"
	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/exoport/apex_process_ape/internal/sessiondriver"
)

// resolveMaxDuration mirrors the CLI's --max-duration default for the
// apescript paths, which carry no flag: a zero MaxDuration means "not
// configured" and resolves to the 3h ceiling (PLAN-19 D2). A script that
// truly wants no cap sets a very large value.
func resolveMaxDuration(d time.Duration) time.Duration {
	if d == 0 {
		return sessiondriver.DefaultMaxDuration
	}
	return d
}

// scriptRunner implements the apescript orchestration hooks (RunPipeline /
// RunTask / RunPrompt) by driving the exact same interactive runners the
// `ape pipeline` / `ape task` / `ape prompt` commands use. It carries the
// per-invocation defaults (project root, quiet, NATS config) and accumulates
// the total cost of every run a script launches so `--output-format` can
// report it.
type scriptRunner struct {
	projectRoot string
	quiet       bool
	base        runConfig // nats/eventing knobs shared by every launched run

	// claudeBin overrides the spawned claude executable (test seam only).
	claudeBin string

	mu        sync.Mutex
	costTotal float64
}

// resolveRoot picks the per-call cwd override or the script's project root.
func (r *scriptRunner) resolveRoot(cwd string) string {
	if cwd != "" {
		return cwd
	}
	return r.projectRoot
}

// addCost folds a launched run's cost into the script total.
func (r *scriptRunner) addCost(c float64) {
	r.mu.Lock()
	r.costTotal += c
	r.mu.Unlock()
}

// totalCost returns the summed cost of every run the script launched.
func (r *scriptRunner) totalCost() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.costTotal
}

// runCfg clones the shared eventing knobs and stamps the per-run fields.
func (r *scriptRunner) runCfg(manifestDir string, kind eventing.Kind) runConfig {
	cfg := r.base
	cfg.manifestDir = manifestDir
	cfg.quiet = r.quiet
	cfg.suppressSummary = true // the script, not the runner, owns output
	cfg.progressWriter = os.Stderr
	cfg.kind = kind
	cfg.claudeBin = r.claudeBin
	return cfg
}

func (r *scriptRunner) runTask(ctx context.Context, o apescript.TaskOpts) (apescript.RunResult, error) {
	root := r.resolveRoot(o.Cwd)
	step := buildTaskStep(taskOptions{
		skill: o.Skill, agent: o.Agent, model: o.Model, args: o.Args,
		prompt: o.Prompt, promptFlagName: o.PromptFlag, skillNoCommit: o.NoCommit,
	})
	var taskCommit *pipeline.CommitDirective
	if strings.TrimSpace(o.TaskCommit) != "" {
		taskCommit = &pipeline.CommitDirective{Mode: pipeline.CommitModeExplicit, Message: o.TaskCommit}
	}
	spec := pipeline.NewSingleStepSpec(o.Skill, step, taskCommit)

	manifestDir := filepath.Join(root, "_output", "tasks")
	cfg := r.runCfg(manifestDir, eventing.KindTask)
	cfg.prompt = o.Prompt
	cfg.allowDirty = o.AllowDirty
	cfg.idleTimeout = o.IdleTimeout
	cfg.maxDuration = resolveMaxDuration(o.MaxDuration)

	headBefore := gitHeadFull(ctx, root)
	start := time.Now()
	runErr := runWithInteractive(ctx, spec, root, cfg)
	dur := time.Since(start)

	runDir := pipeline.ResolveLatestRunDir(root, o.Skill, manifestDir)
	res := r.manifestToResult(ctx, root, runDir, headBefore, dur)
	return res, runErr
}

func (r *scriptRunner) runPipeline(ctx context.Context, o apescript.PipelineOpts) (apescript.RunResult, error) {
	root := r.resolveRoot(o.Cwd)
	spec, err := pipeline.LoadSpec(o.Name, root)
	if err != nil {
		return apescript.RunResult{}, err
	}
	cfg := r.runCfg("", eventing.KindPipeline)
	cfg.prompt = o.Prompt
	cfg.fromStage = o.From
	cfg.noCommit = o.NoCommit
	cfg.maxDuration = resolveMaxDuration(o.MaxDuration)

	headBefore := gitHeadFull(ctx, root)
	start := time.Now()
	runErr := runWithInteractive(ctx, spec, root, cfg)
	dur := time.Since(start)

	runDir := pipeline.ResolveLatestRunDir(root, spec.Name, "")
	res := r.manifestToResult(ctx, root, runDir, headBefore, dur)
	return res, runErr
}

func (r *scriptRunner) runPrompt(ctx context.Context, o apescript.PromptOpts) (apescript.RunResult, error) {
	root := r.resolveRoot(o.Cwd)
	res, _, err := runPromptCore(ctx, promptOptions{
		text:        o.Text,
		handoff:     o.Handoff,
		agent:       o.Agent,
		model:       o.Model,
		workflow:    o.Workflow,
		ultracode:   o.Ultracode,
		idleTimeout: o.IdleTimeout,
		maxDuration: resolveMaxDuration(o.MaxDuration),
		projectRoot: root,
		quiet:       r.quiet,
		format:      "human",
	})
	out := apescript.RunResult{
		RunID:      res.PromptID,
		Status:     res.Status,
		CostUSD:    res.CostUSD,
		PerModel:   res.PerModel,
		Duration:   time.Duration(res.DurationSeconds * float64(time.Second)),
		CommitSHAs: []string{},
	}
	r.addCost(res.CostUSD)
	return out, err
}

// manifestToResult reads the finalized run manifest and maps it onto a
// RunResult, folding the run's cost into the script total and collecting the
// commit SHAs the run produced. A missing/unreadable manifest yields a
// zero-value result (best-effort; the run error is the authoritative signal).
func (r *scriptRunner) manifestToResult(ctx context.Context, projectRoot, runDir, headBefore string, dur time.Duration) apescript.RunResult {
	res := apescript.RunResult{
		Duration:   dur,
		CommitSHAs: gitCommitShasSince(ctx, projectRoot, headBefore),
	}
	if runDir == "" {
		return res
	}
	m, err := pipeline.LoadManifest(runDir)
	if err != nil {
		return res
	}
	res.RunID = m.RunID
	res.Status = string(m.Status)
	res.ManifestPath = filepath.Join(runDir, "manifest.yaml")
	res.CostUSD = m.Totals.CostUSD
	res.PerModel = modelUsageRecordsToTotals(m.Totals.ModelUsage)
	r.addCost(m.Totals.CostUSD)
	return res
}

// modelUsageRecordsToTotals converts manifest model_usage records to the
// cost.Totals shape apescript exposes on RunResult.PerModel.
func modelUsageRecordsToTotals(mu map[string]pipeline.ModelUsageRecord) map[string]cost.Totals {
	if len(mu) == 0 {
		return nil
	}
	out := make(map[string]cost.Totals, len(mu))
	for model, u := range mu {
		out[model] = cost.Totals{
			CostUSD:               u.CostUSD,
			InputTokens:           u.TokensInput,
			OutputTokens:          u.TokensOutput,
			CacheReadTokens:       u.TokensCacheRead,
			CacheCreationTokens:   u.TokensCacheCreation,
			CacheCreation5mTokens: u.TokensCacheCreation5m,
			CacheCreation1hTokens: u.TokensCacheCreation1h,
			NumTurns:              u.NumTurns,
		}
	}
	return out
}

// gitCommitShasSince returns the full SHAs of commits made after `before`
// (oldest first). Best-effort; empty on any error or empty `before`.
func gitCommitShasSince(ctx context.Context, projectRoot, before string) []string {
	if before == "" {
		return []string{}
	}
	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", "log", "--reverse", "--format=%H", before+"..HEAD") //nolint:gosec // `before` is a SHA captured from rev-parse at run start, not user input
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
