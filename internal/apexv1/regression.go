package apexv1

import (
	"context"
	"fmt"
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
// recorded for that same command. A milestone tier that differs from the
// change tier is baselined once per milestone, by the first of its
// changes to start (W2 Part 1, State.MilestoneRegression), so the gate
// reports red then and red now as pre-existing rather than failing on it.

// KindRegression is the regression command's kind in a run of checks.
const KindRegression = "regression"

// regressionRef is the regression command's ref (and log name).
const regressionRef = "regression"

// milestoneRegressionRef is the milestone tier's baseline as pre_existing_red
// lists it (W2 Part 1).
const milestoneRegressionRef = "milestone-regression"

// RegressionBaseline is the regression command's run at a change's start:
// recorded once, and kept across re-starts unless the command changed.
type RegressionBaseline struct {
	Cmd          string `json:"cmd"`
	Exit         int    `json:"exit"`
	OutputSHA256 string `json:"output_sha256"`
	SHA          string `json:"sha"`
	At           string `json:"at"`
	// Secs is how long the run took (W2: regression.slow); 0 for a
	// baseline recorded before v1.1.
	Secs float64 `json:"secs,omitempty"`
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
	// Secs is how long the run took (W2: regression.slow).
	Secs float64 `json:"secs,omitempty"`
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
	// ShellNote: exit 127 on a bash/zsh builtin (RN-1).
	ShellNote string `json:"shell_note,omitempty"`
	// Skipped: verify did not run the command (Q87) — "docs-only", every
	// path the change touched is docs or the development folder. Paths
	// are the docs paths it touched (the development folder not listed;
	// the first regressionPathsShown, paths_total counts them); exit 0,
	// failed false.
	Skipped    string   `json:"skipped,omitempty"`
	Paths      []string `json:"paths,omitempty"`
	PathsTotal int      `json:"paths_total,omitempty"`
	// NotSkipped: the change touched only docs, yet verify ran the
	// command — why (docs.tested, an unaccepted docs config, a command
	// naming a touched path).
	NotSkipped string `json:"not_skipped,omitempty"`
}

// skippedRegression is the verdict of a regression command cmd verify
// skipped for a docs-only change that touched paths, beside the
// change's baseline base.
func skippedRegression(cmd string, base *RegressionBaseline, paths []string) *RegressionVerdict {
	v := &RegressionVerdict{Cmd: cmd, Skipped: RegressionSkippedDocsOnly, Paths: paths}
	if base != nil && base.Cmd == cmd {
		exit := base.Exit
		v.BaselineExit, v.BaselineSHA = &exit, base.SHA
	}
	if v.PathsTotal = len(paths); v.PathsTotal > regressionPathsShown {
		v.Paths = paths[:regressionPathsShown]
	}
	return v
}

// skip is v when it is a skipped command's verdict, else nil: what
// state.json's verify records.
func (v *RegressionVerdict) skip() *RegressionVerdict {
	if v == nil || v.Skipped == "" {
		return nil
	}
	return v
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
	v := &RegressionVerdict{Cmd: r.Cmd, Exit: r.Exit, OutputSHA256: r.OutputSHA256, Log: r.Log, Secs: r.Secs, Reason: r.Reason, Detail: r.Detail, ShellNote: r.ShellNote}
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
	b := &RegressionBaseline{Cmd: r.Cmd, Exit: r.Exit, OutputSHA256: r.OutputSHA256, SHA: sha, At: Stamp(), Secs: r.Secs}
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

// rebaselineMilestoneRegression records the milestone tier's baseline
// (W2 Part 1) at a milestone change's start, when the tiers differ and no
// change of the milestone, active or archived, has one for that command
// yet: one full run per milestone and command. A tweak (milestone: none)
// never records one (D40). Returns the run when there was one.
func (p *Project) rebaselineMilestoneRegression(ctx context.Context, ci ChangeInfo, st *State, head string, opt RunOptions) *CheckResult {
	if ci.Doc == nil || ci.Doc.IsTweak() {
		return nil
	}
	cmd := p.milestoneRegressionCmd()
	if cmd == "" || cmd == p.regressionCmd() || (st.MilestoneRegression != nil && st.MilestoneRegression.Cmd == cmd) {
		return nil
	}
	if other, b := p.milestoneRegressionBaseline(ctx, ci.Doc.Meta.Milestone, cmd); b != nil && other != ci.ID {
		return nil
	}
	r := p.runRegression(ctx, cmd, p.LogDir(ci.ID), "baseline-milestone-", opt)
	st.MilestoneRegression = p.regressionBaseline(&r, head)
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
		for _, b := range []*RegressionBaseline{c.State.Regression, c.State.MilestoneRegression} {
			if b == nil || b.Cmd != cmd {
				continue
			}
			if best == nil || b.At < best.At || (b.At == best.At && c.ID < change) {
				change, best = c.ID, b
			}
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
		if r := st.MilestoneRegression; r != nil && r.Exit != 0 {
			out = append(out, PreExistingRed{Change: c.ID, Kind: KindRegression, Ref: milestoneRegressionRef, Cmd: r.Cmd, Exit: r.Exit, SHA: r.SHA})
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

// regressionSlowNotices is project check's regression.slow notice (W2 Part
// 1): a single-command regression: — one command for both tiers, so every
// change's start and verify pays for the whole suite — whose last recorded
// run took longer than checks.slow_regression. The remedy is the tiers.
func (p *Project) regressionSlowNotices(ctx context.Context) []Diagnostic {
	cfg, err := LoadConfig(p.Root)
	if err != nil || cfg.Regression.Change == "" || cfg.Regression.MilestoneCmd() != cfg.Regression.Change {
		return nil
	}
	cmd := cfg.Regression.Change
	secs, where := p.lastRegressionRun(ctx, cmd)
	limit := cfg.Checks.SlowRegressionAt()
	if secs <= limit.Seconds() {
		return nil
	}
	took := roundSecs(secs)
	return []Diagnostic{notice(Diagnostic{Code: CodeRegressionSlow, File: configYAML, Line: configKeyLine(p.Root, "regression"), Message: fmt.Sprintf(
		"regression: `%s` took %s at its last run (%s), over checks.slow_regression (%s), and as the single command it runs at every change's start and verify as well as at the gate"+
			" — split it into tiers, regression: {change: <a fast subset>, milestone: %s}, or raise checks.slow_regression", cmd, took, where, limit, cmd)})}
}

// lastRegressionRun is how long cmd took at its newest recorded run, and
// where that was: an active change's baseline or verify, or a milestone's
// last gate. 0 when none is recorded with a duration.
func (p *Project) lastRegressionRun(ctx context.Context, cmd string) (secs float64, where string) {
	var at string
	seen := func(when string, s float64, w string) {
		if s > 0 && when > at {
			at, secs, where = when, s, w
		}
	}
	for _, c := range p.changes(ctx) {
		st := c.State
		if st == nil {
			continue
		}
		if b := st.Regression; b != nil && b.Cmd == cmd {
			seen(b.At, b.Secs, c.ID+"'s baseline")
		}
		if v := st.Verify; v != nil {
			for i := range v.Results {
				if r := &v.Results[i]; r.Kind == KindRegression && r.Cmd == cmd {
					seen(v.At, r.Secs, c.ID+"'s verify")
				}
			}
		}
	}
	if rm := p.roadmap(); rm != nil {
		for i := range rm.Milestones {
			id := rm.Milestones[i].ID
			if run := p.LastGateRun(id); run != nil && run.Regression != nil && run.Regression.Cmd == cmd {
				seen(run.At, run.Regression.Secs, id+"'s gate")
			}
		}
	}
	return secs, where
}
