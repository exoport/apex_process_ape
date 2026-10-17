package apecmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/cost"
	"github.com/exoport/apex_process_ape/internal/framework"
	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"gopkg.in/yaml.v3"
)

// v1OrchestratorSkill is the skill that runs a milestone unattended.
const v1OrchestratorSkill = "apex-orchestrator"

// v1BudgetWatch stops an `ape task` session of an APEX v1 project when its
// milestone's spend reaches the milestone's budget (backlog 30, only with
// limits.budget: enforce and a Budget: line). The spend is live: what the
// milestone's runs had recorded when the session started, plus the
// session's own cost so far, read from its transcripts — the main session
// and its sub-agents — as the manifest's telemetry would be.
type v1BudgetWatch struct {
	p      *apexv1.Project
	limits *apexv1.TaskLimits

	mu  sync.Mutex
	hit *apexv1.LimitHit
}

// newV1BudgetWatch decides the session's budget limit, or refuses to start
// it (apexv1 FailError milestone.budget-exceeded). nil, nil when no budget
// applies — also for a fix's session (args names a change of `milestone:
// none`, D40), which counts toward no milestone.
func newV1BudgetWatch(ctx context.Context, p *apexv1.Project, skill, args string) (*v1BudgetWatch, error) {
	tl, err := p.TaskLimitsFor(ctx, skill, args)
	if err != nil || tl == nil {
		return nil, err
	}
	return &v1BudgetWatch{p: p, limits: tl}, nil
}

// budgetPolls is when the watch reads the session's cost: soon after the
// start, so a session that is over at once stops at once, then every
// budgetPollSteady. A transcript rescan is a few milliseconds per MB.
var (
	budgetPolls      = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second}
	budgetPollSteady = 30 * time.Second
)

// run polls until ctx ends. On the first poll at or over the budget it
// records the hit and fails the session's step through stop — again every
// second until the run ends, since a fatal posted before the step's wait
// began is drained when that wait starts.
func (w *v1BudgetWatch) run(ctx context.Context, sessionCost func() float64, stop func(error)) {
	for i := 0; ; i++ {
		wait := budgetPollSteady
		if i < len(budgetPolls) {
			wait = budgetPolls[i]
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		hit := w.limits.Check(sessionCost())
		if hit == nil {
			continue
		}
		w.mu.Lock()
		w.hit = hit
		w.mu.Unlock()
		err := &pipeline.LimitStopError{
			Kind: pipeline.TerminationBudget, Message: hit.Message,
			SpentUSD: hit.Stop.SpentUSD, BudgetUSD: hit.Stop.BudgetUSD, SessionCostUSD: hit.Stop.SessionUSD,
		}
		for {
			stop(err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
}

// stopped is the hit that stopped the session, or nil.
func (w *v1BudgetWatch) stopped() *apexv1.LimitHit {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.hit
}

// liveSessionCost is the running step's cost so far: its main transcript
// and every sub-agent transcript of it, hook-captured or found beside it.
func (c *interactiveCore) liveSessionCost() float64 {
	c.transcriptMu.Lock()
	main, since := c.activeTranscript, c.stepStartedAt
	var subs []string
	for _, s := range c.subSessions {
		if s.transcript != "" {
			subs = append(subs, s.transcript)
		}
	}
	c.transcriptMu.Unlock()
	if main == "" {
		return 0
	}
	seen := map[string]bool{}
	var paths []string
	for _, f := range cost.SessionFiles(main, since) {
		seen[filepath.Clean(f.Path)] = true
		paths = append(paths, f.Path)
	}
	for _, s := range subs {
		if !seen[filepath.Clean(s)] {
			seen[filepath.Clean(s)] = true
			paths = append(paths, s)
		}
	}
	return cost.ScanPaths(paths).Totals.CostUSD
}

// recordBudgetStop records a budget stop in _apex/state/milestones.json
// (committed alone), naming the stopped run, so `ape status` shows it and
// the milestone's spend counts the session at its measured cost.
// Best-effort beyond a warning: the run is over and already reported.
func recordBudgetStop(projectRoot, skill, manifestDir string, w *v1BudgetWatch) {
	hit := w.stopped()
	if hit == nil {
		return
	}
	stop := hit.Stop
	if runDir := pipeline.ResolveLatestRunDir(projectRoot, skill, manifestDir); runDir != "" {
		stop.Run = runIDFromDir(runDir)
		if data, err := os.ReadFile(filepath.Join(runDir, "manifest.yaml")); err == nil {
			var m pipeline.Manifest
			if yaml.Unmarshal(data, &m) == nil && m.RunID != "" {
				stop.Run = m.RunID
			}
		}
	}
	if err := w.p.RecordLimitStop(w.limits.Milestone, stop); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ budget stop not recorded in milestones.json: %v\n", err)
	}
}

// v1ConfigError is the parse error of a v1 project's _apex/config.yaml
// (its _apex/state/ exists), which would otherwise make it look like no v1
// project at all: a mistyped limits.budget must not run the session with
// no limit and no word said.
func v1ConfigError(root string) error {
	if !runlog.IsV1Project(root) {
		return nil
	}
	if _, err := apexv1.Open(root); err != nil {
		return fmt.Errorf("%s: %w", apexv1.ConfigPath(root), err)
	}
	return nil
}

// v1ApeFloor refuses a session of a v1 project whose installed
// _apex/ape-commands.yaml declares a min_ape_version above this ape (H1,
// D47 Q1): its skills would call commands and behaviour this ape lacks,
// and fail mid-run instead of up front. It is the floor `ape framework
// update` already refuses an install by (exit 11), judged the same way
// (framework.CompareApeVersion): a dev build or a Go pseudo-version cannot
// be compared, and is warned about on warn and let run, as there; no file,
// or no min_ape_version in it, is no floor. An unparseable file refuses
// (a usage error), as it does there.
func v1ApeFloor(root string, warn io.Writer) error {
	manifest, err := framework.LoadApeCommands(root)
	if err != nil {
		return fmt.Errorf("%w — `ape framework update` reinstalls it", err)
	}
	minimum := ""
	if manifest != nil {
		minimum = manifest.MinApeVersion
	}
	switch framework.CompareApeVersion(Version, minimum) {
	case framework.ApeVersionBelow:
		route := apeUpdateRoute(root)
		return &apexv1.FailError{Code: "ape.below-floor", Msg: fmt.Sprintf(
			"ape %s is older than the installed framework's min_ape_version %s (%s) — update ape first (%s): %s",
			Version, minimum, framework.ProjectApeCommands, route.name, route.command("v"+trimV(minimum)))}
	case framework.ApeVersionUnknown:
		fmt.Fprintf(warn, "warning: ape %s cannot be compared with the installed framework's min_ape_version %s (a dev build or a pseudo-version) — continuing\n",
			Version, minimum)
	case framework.ApeVersionOK, framework.ApeVersionNoMinimum:
	}
	return nil
}

// taskPreflightExit is `ape task`'s exit for a failure before any spawn: 1
// for a v1 limit or floor that refused the start (a check that failed),
// else 2.
func taskPreflightExit(err error) int {
	if fe, ok := errors.AsType[*apexv1.FailError](err); ok {
		switch fe.Code {
		case "milestone.budget-exceeded", "milestone.blocks-exceeded", "ape.below-floor":
			return ExitRunFailed
		}
	}
	return ExitUsage
}
