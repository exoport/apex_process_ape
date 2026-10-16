package apexv1

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Backlog 32, 37: the project's own test suite as a regression command,
// `regression: <cmd>` in _apex/config.yaml. The spec's scenario checks
// cover what changes said; the suite covers everything else the project
// already had. It runs at a change's baseline, at verify, and at the
// milestone gate, and fails only when it is newly red: a suite that was
// already red before the work began is the planner's to roadmap, not the
// change's to fix.

// KindRegression is the regression command's kind in a run of checks.
const KindRegression = "regression"

// regressionRef is the regression command's ref (and log name).
const regressionRef = "regression"

// RegressionBaseline is the regression command's run at a change's start:
// recorded once, and kept across re-starts unless the command changed.
type RegressionBaseline struct {
	Cmd          string `json:"cmd"`
	Exit         int    `json:"exit"`
	OutputSHA256 string `json:"output_sha256"`
	SHA          string `json:"sha"`
	At           string `json:"at"`
}

// RegressionVerdict judges one run of the regression command against a
// baseline. Failed: red now and not red at baseline — newly red, or red
// with no baseline for this command to say otherwise.
type RegressionVerdict struct {
	Cmd          string `json:"cmd"`
	Exit         int    `json:"exit"`
	OutputSHA256 string `json:"output_sha256"`
	Log          string `json:"log,omitempty"`
	// BaselineExit is the baseline's exit; null when there is no baseline
	// for this command.
	BaselineExit *int   `json:"baseline_exit"`
	BaselineSHA  string `json:"baseline_sha,omitempty"`
	// BaselineChange names the change whose baseline a milestone gate
	// judged by.
	BaselineChange string `json:"baseline_change,omitempty"`
	// PreExistingRed: red now and red at baseline — reported, not failed.
	PreExistingRed bool `json:"pre_existing_red"`
	Failed         bool `json:"failed"`
}

// regressionCmd is the configured regression command, "" when none.
func (p *Project) regressionCmd() string {
	cfg, err := LoadConfig(p.Root)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cfg.Regression)
}

// runRegression runs the regression command like any check: sh -c from
// the project root, its output logged under logDir, strays undone.
func (p *Project) runRegression(ctx context.Context, cmd, logDir, prefix string, opt RunOptions) CheckResult {
	return p.RunChecks(ctx, logDir, prefix, []PlannedCheck{{Ref: regressionRef, Kind: KindRegression, Cmd: cmd}}, opt)[0]
}

// judgeRegression marks r PreExistingRed when it is red and so was base
// (same command), and returns the verdict.
func judgeRegression(r *CheckResult, base *RegressionBaseline) *RegressionVerdict {
	v := &RegressionVerdict{Cmd: r.Cmd, Exit: r.Exit, OutputSHA256: r.OutputSHA256, Log: r.Log}
	if base != nil && base.Cmd == r.Cmd {
		exit := base.Exit
		v.BaselineExit, v.BaselineSHA = &exit, base.SHA
	}
	if r.Exit != 0 {
		v.PreExistingRed = v.BaselineExit != nil && *v.BaselineExit != 0
		v.Failed = !v.PreExistingRed
	}
	r.PreExistingRed = v.PreExistingRed
	return v
}

// rebaselineRegression records the regression baseline on a first start,
// or when the command changed since the recorded one; otherwise the
// original stays. Returns the run when there was one.
func (p *Project) rebaselineRegression(ctx context.Context, id string, st *State, head string, opt RunOptions) *CheckResult {
	cmd := p.regressionCmd()
	if cmd == "" || (st.Regression != nil && st.Regression.Cmd == cmd) {
		return nil
	}
	r := p.runRegression(ctx, cmd, p.LogDir(id), "baseline-", opt)
	st.Regression = &RegressionBaseline{Cmd: cmd, Exit: r.Exit, OutputSHA256: r.OutputSHA256, SHA: head, At: Stamp()}
	return &r
}

// regressionStale reports a started change whose regression baseline is
// missing or was taken for another command: a reason to re-start.
func (p *Project) regressionStale(st *State) bool {
	cmd := p.regressionCmd()
	return cmd != "" && (st.Regression == nil || st.Regression.Cmd != cmd)
}

// milestoneRegressionBaseline is the baseline a milestone gate judges the
// regression command by: the earliest one, for this command, recorded by
// any change of the milestone, active or in the archive. nil when none.
func (p *Project) milestoneRegressionBaseline(milestone, cmd string) (string, *RegressionBaseline) {
	var change string
	var best *RegressionBaseline
	for _, c := range append(p.Changes(), p.archivedChanges()...) {
		if c.Doc == nil || c.State == nil || c.Doc.Meta.Milestone != milestone {
			continue
		}
		b := c.State.Regression
		if b == nil || b.Cmd != cmd {
			continue
		}
		if best == nil || b.At < best.At || (b.At == best.At && c.ID < change) {
			change, best = c.ID, b
		}
	}
	return change, best
}

// archivedChanges loads every change in the archive (archived and
// abandoned).
func (p *Project) archivedChanges() []ChangeInfo {
	ids := p.ArchivedIDs()
	out := make([]ChangeInfo, 0, len(ids))
	for id, folder := range ids {
		dir := filepath.Join(p.ArchiveDir(), folder)
		ci := ChangeInfo{ID: id}
		ci.State, ci.Err = LoadState(filepath.Join(dir, "state.json"))
		if doc, err := os.ReadFile(filepath.Join(dir, "change.md")); err == nil {
			ci.Doc = ParseChange(p.Rel(filepath.Join(dir, "change.md")), string(doc))
		}
		out = append(out, ci)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// PreExistingRed is one check that was red at a change's baseline and is
// not the change's own to turn green: the regression command, or a
// scenario of the spec the change leaves untouched. The planner roadmaps
// these; a change's own task and delta checks red at baseline are normal
// test-first and never listed.
type PreExistingRed struct {
	Change string `json:"change"`
	Kind   string `json:"kind"`
	Ref    string `json:"ref"`
	Cmd    string `json:"cmd"`
	Exit   int    `json:"exit"`
	SHA    string `json:"sha"`
}

// preExistingRed lists, for every change not archived, what was red at
// its baseline that is not its own.
func (p *Project) preExistingRed(changes []ChangeInfo) []PreExistingRed {
	out := []PreExistingRed{}
	for _, c := range changes {
		st := c.State
		if st == nil {
			continue
		}
		if r := st.Regression; r != nil && r.Exit != 0 {
			out = append(out, PreExistingRed{Change: c.ID, Kind: KindRegression, Ref: regressionRef, Cmd: r.Cmd, Exit: r.Exit, SHA: r.SHA})
		}
		if c.Doc == nil || len(st.Baseline) == 0 {
			continue
		}
		regressionScenarios := map[string]PlannedCheck{}
		for _, pc := range p.EffectiveSpecChecks(c.Doc) {
			if pc.Kind == KindScenario && !pc.Delta {
				regressionScenarios[pc.Ref] = pc
			}
		}
		for _, b := range st.Baseline {
			pc, ok := regressionScenarios[b.Task]
			if !ok || b.Exit == 0 || (b.Cmd != "" && b.Cmd != pc.Cmd) {
				continue
			}
			out = append(out, PreExistingRed{Change: c.ID, Kind: KindScenario, Ref: b.Task, Cmd: pc.Cmd, Exit: b.Exit, SHA: b.SHA})
		}
	}
	return out
}
