package apexv1

import (
	"context"
	"os"
	"path/filepath"
	"sort"
)

// Backlog 32, 37: the project's own test suite as a regression command,
// `regression: <cmd>` in _apex/config.yaml. The spec's scenario checks
// cover what changes said; the suite covers everything else the project
// already had. It runs at a change's baseline, at verify, and at the
// milestone gate, and fails only when it is newly red: a suite that was
// already red before the work began is the planner's to roadmap, not the
// change's to fix.
//
// Tiers (H10): `regression: {change: <cmd>, milestone: <cmd>}` runs the
// change tier at start and verify and the milestone tier at the gate. The
// gate judges it by the earliest baseline a change of the milestone
// recorded for that same command, so a milestone tier that differs from
// the change tier has no baseline: red at the gate is red.

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
	// Failing are the failing tests the run's output named (Q47), read
	// when it was red; none for a green run, a run whose runner names
	// nothing ape reads, a run naming more than regressionFailingCap, or
	// a baseline recorded before rc.5.
	Failing []string `json:"failing,omitempty"`
}

// regressionFailingCap is the most failing tests a regression baseline
// stores: state.json is committed on the change's branch. A run naming
// more stores none and is judged by the exit codes — a cut list would
// make a cut name look new.
const regressionFailingCap = 500

// regressionNewFailuresShown is how many new failing tests a verdict
// carries (a gate's is committed in milestones.json);
// new_failures_total counts them all.
const regressionNewFailuresShown = 50

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
	// NewFailures are the failing tests the run names that its baseline
	// did not (Q47): what failed it when the baseline was red too, or
	// every failing test when the baseline was green.
	NewFailures []string `json:"new_failures,omitempty"`
	// NewFailuresTotal counts them when there are any: new_failures holds
	// at most regressionNewFailuresShown.
	NewFailuresTotal int `json:"new_failures_total,omitempty"`
	// Reason and Detail: the run was vacuous or unrun (H11). An unrun
	// command always fails: nothing shows it would pass.
	Reason string `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// regressionCmd is the configured regression command's change tier — what
// a change's baseline and verify run — "" when none.
func (p *Project) regressionCmd() string {
	cfg, err := LoadConfig(p.Root)
	if err != nil {
		return ""
	}
	return cfg.Regression.Change
}

// milestoneRegressionCmd is the tier the milestone gate runs (H10): the
// milestone command, else the change tier's, "" when none.
func (p *Project) milestoneRegressionCmd() string {
	cfg, err := LoadConfig(p.Root)
	if err != nil {
		return ""
	}
	return cfg.Regression.MilestoneCmd()
}

// runRegression runs the regression command like any check: sh -c from
// the project root, its output logged under logDir, strays undone.
func (p *Project) runRegression(ctx context.Context, cmd, logDir, prefix string, opt RunOptions) CheckResult {
	return p.RunChecks(ctx, logDir, prefix, []PlannedCheck{{Ref: regressionRef, Kind: KindRegression, Cmd: cmd}}, opt)[0]
}

// judgeRegression marks r PreExistingRed when it is red and so was base
// (same command), and returns the verdict; out is r's output. Red at both
// points is pre-existing only when nothing new fails: when the failing
// tests were named on both sides, every one failing now must have failed
// at baseline, else it fails with the new names (Q47: a new failure
// behind an already-red suite); when either side named none, the exit
// codes decide, as before rc.5. An unrun command is never pre-existing
// red: it is red for want of a requirement, not by its tests.
func judgeRegression(r *CheckResult, base *RegressionBaseline, out string) *RegressionVerdict {
	v := &RegressionVerdict{Cmd: r.Cmd, Exit: r.Exit, OutputSHA256: r.OutputSHA256, Log: r.Log, Reason: r.Reason, Detail: r.Detail}
	if base != nil && base.Cmd == r.Cmd {
		exit := base.Exit
		v.BaselineExit, v.BaselineSHA = &exit, base.SHA
	}
	if r.Exit != 0 {
		var now []string
		if r.Reason != ReasonUnrun {
			now = failingTests(out)
		}
		switch {
		case r.Reason == ReasonUnrun || v.BaselineExit == nil:
		case *v.BaselineExit == 0:
			v.NewFailures = now
		case len(now) > 0 && len(base.Failing) > 0:
			v.NewFailures = namesNotIn(now, base.Failing)
			v.PreExistingRed = len(v.NewFailures) == 0
		default:
			v.PreExistingRed = true
		}
		v.Failed = !v.PreExistingRed
		if v.NewFailuresTotal = len(v.NewFailures); v.NewFailuresTotal > regressionNewFailuresShown {
			v.NewFailures = v.NewFailures[:regressionNewFailuresShown]
		}
	}
	r.PreExistingRed = v.PreExistingRed
	return v
}

// checkOutput is a check's logged output, "" when it has none.
func (p *Project) checkOutput(r *CheckResult) string {
	if r == nil || r.Log == "" {
		return ""
	}
	data, _ := os.ReadFile(filepath.Join(p.Root, filepath.FromSlash(r.Log)))
	return string(data)
}

// regressionBaseline is r recorded as a regression baseline at sha, with
// the failing tests its output names when it is red (Q47) — none past
// regressionFailingCap.
func (p *Project) regressionBaseline(r *CheckResult, sha string) *RegressionBaseline {
	b := &RegressionBaseline{Cmd: r.Cmd, Exit: r.Exit, OutputSHA256: r.OutputSHA256, SHA: sha, At: Stamp()}
	if r.Exit != 0 && r.Reason != ReasonUnrun {
		if names := failingTests(p.checkOutput(r)); len(names) <= regressionFailingCap {
			b.Failing = names
		}
	}
	return b
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
	st.Regression = p.regressionBaseline(&r, head)
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
func (p *Project) milestoneRegressionBaseline(ctx context.Context, milestone, cmd string) (string, *RegressionBaseline) {
	var change string
	var best *RegressionBaseline
	for _, c := range append(p.changes(ctx), p.archivedChanges()...) {
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
		dir := p.archivedPath(folder)
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
		planned := p.EffectiveSpecChecks(c.Doc)
		for i := range planned {
			pc := planned[i]
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
