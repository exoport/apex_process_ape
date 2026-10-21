package apexv1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// W2 Part 1: with tiers that differ, the milestone's first change to start
// records the milestone tier's baseline once; the gate judges that tier
// against it, so a suite red before the milestone's work is pre-existing.
func TestMilestoneRegressionBaseline_TakenOncePerMilestone(t *testing.T) {
	files := suiteFiles()
	files["_apex/config.yaml"] = "project_name: greeter\nregression: {change: ./suite.sh, milestone: ./suite-all.sh}\n"
	files["suite-all.sh"] = "#!/bin/sh\necho all\n[ -z \"$ALL_RED\" ]\n"
	p := fixture(t, files)
	t.Setenv("ALL_RED", "1") // red before the milestone's work

	addChange(t, p, "add-wave", waveChange("add-wave"))
	commitAll(t, p, "plan: add-wave")
	res, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.NotNil(t, res.MilestoneRegression, "the first start records the milestone tier")
	require.Equal(t, "./suite-all.sh", res.MilestoneRegression.Cmd)
	b := state(t, p, "add-wave").MilestoneRegression
	require.NotNil(t, b)
	require.Equal(t, "./suite-all.sh", b.Cmd)
	require.NotZero(t, b.Exit, "recorded red")
	require.Equal(t, "./suite.sh", state(t, p, "add-wave").Regression.Cmd, "the change tier is unchanged")
	reds := p.preExistingRed(p.changes(ctx()))
	require.Contains(t, reds, PreExistingRed{Change: "add-wave", Kind: KindRegression, Ref: milestoneRegressionRef, Cmd: "./suite-all.sh", Exit: b.Exit, SHA: b.SHA},
		"the milestone tier red at baseline is the planner's to roadmap")

	write(t, p, "wave.sh", "#!/bin/sh\necho wave\n")
	commitAll(t, p, "feat: wave")
	run(t, p.Root, "switch", "-q", "main")
	addChange(t, p, "add-bow", changeDoc("add-bow", "greeting",
		"### ADDED [greeting] Requirement: Bow\nThe system SHALL bow.\n#### Scenario: bow\n- GIVEN x WHEN y THEN z\n- check: ./bow.sh\n",
		"- T1 add bow — check: ./bow.sh\n"))
	commitAll(t, p, "plan: add-bow")
	res, err = p.Start(ctx(), "add-bow", RunOptions{})
	require.NoError(t, err)
	require.Nil(t, res.MilestoneRegression, "one full run per milestone and command")
	require.Nil(t, state(t, p, "add-bow").MilestoneRegression)

	run(t, p.Root, "switch", "-q", "main")
	g, err := p.RunGate(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.NotNil(t, g.Run.Regression)
	require.Equal(t, "./suite-all.sh", g.Run.Regression.Cmd)
	require.True(t, g.Run.Regression.PreExistingRed, "red then and red now: reported, not failed")
	require.False(t, g.Run.Regression.Failed)
	require.Equal(t, "add-wave", g.Run.Regression.BaselineChange)
	require.Positive(t, g.Run.Regression.Secs)

	// Green at that baseline and red now is red: the work broke it.
	q := fixture(t, files)
	t.Setenv("ALL_RED", "")
	addChange(t, q, "add-wave", waveChange("add-wave"))
	commitAll(t, q, "plan: add-wave")
	_, err = q.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.Zero(t, state(t, q, "add-wave").MilestoneRegression.Exit)
	run(t, q.Root, "switch", "-q", "main")
	t.Setenv("ALL_RED", "1")
	g, err = q.RunGate(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.True(t, g.Run.Regression.Failed, "green then, red now")
	require.False(t, g.Run.Regression.PreExistingRed)
	require.Contains(t, g.Run.Why(), "green at baseline (add-wave)")
}

// Equal tiers take no second run, and a tweak never records one (D40).
func TestMilestoneRegressionBaseline_NotForEqualTiersOrATweak(t *testing.T) {
	p := fixture(t, suiteFiles())
	addChange(t, p, "add-wave", waveChange("add-wave"))
	commitAll(t, p, "plan: add-wave")
	res, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.Nil(t, res.MilestoneRegression)
	require.Nil(t, state(t, p, "add-wave").MilestoneRegression)

	files := suiteFiles()
	files["_apex/config.yaml"] = "project_name: greeter\nregression: {change: ./suite.sh, milestone: ./suite-all.sh}\n"
	files["suite-all.sh"] = "#!/bin/sh\necho all\n"
	q := fixture(t, files)
	tw := ChangeInfo{ID: "fix-hug", Doc: ParseChange("development/changes/fix-hug/change.md", tweakDoc("fix-hug"))}
	require.True(t, tw.Doc.IsTweak())
	require.Nil(t, q.rebaselineMilestoneRegression(ctx(), tw, &State{ID: "fix-hug"}, "abc", RunOptions{}))
}

// W2 Part 1: regression.slow — a single-command regression: whose last
// recorded run took over checks.slow_regression is a notice; tiers are
// never one.
func TestRegressionSlowNotice(t *testing.T) {
	slow := func(p *Project) []Diagnostic {
		var out []Diagnostic
		for _, d := range p.Notices() {
			if d.Code == CodeRegressionSlow {
				out = append(out, d)
			}
		}
		return out
	}
	lastGate := func(t *testing.T, p *Project, cmd string, secs float64) {
		t.Helper()
		run := GateRun{At: Stamp(), Cmd: "./gate.sh", Regression: &RegressionVerdict{Cmd: cmd, Secs: secs}}
		data, err := json.Marshal(run)
		require.NoError(t, err)
		write(t, p, "_apex/state/logs/gate-M1.json", string(data))
	}

	p := fixture(t, suiteFiles())
	require.Empty(t, slow(p), "no run recorded")
	lastGate(t, p, "./suite.sh", 299)
	require.Empty(t, slow(p), "under the default 5m")
	lastGate(t, p, "./suite.sh", 301)
	ds := slow(p)
	require.Len(t, ds, 1)
	require.Equal(t, SeverityNotice, ds[0].Severity)
	require.Equal(t, "replan", ds[0].Action)
	require.Equal(t, "_apex/config.yaml", ds[0].File)
	require.Equal(t, 2, ds[0].Line)
	require.Contains(t, ds[0].Message, "took 5m1s at its last run (M1's gate)")
	require.Contains(t, ds[0].Fix, "regression: {change: <a fast subset>, milestone: ./suite.sh}")

	write(t, p, "_apex/config.yaml", "project_name: greeter\nregression: ./suite.sh\nchecks: {slow_regression: 10m}\n")
	require.Empty(t, slow(p), "checks.slow_regression raises the threshold")
	write(t, p, "_apex/config.yaml", "project_name: greeter\nregression: {change: ./suite.sh, milestone: ./suite-all.sh}\n")
	lastGate(t, p, "./suite-all.sh", 3600)
	require.Empty(t, slow(p), "tiers: the change tier is the fast one")

	// A change's baseline counts as a run.
	q := fixture(t, suiteFiles())
	write(t, q, "_apex/config.yaml", "project_name: greeter\nregression: ./suite.sh\nchecks: {slow_regression: 1ns}\n")
	addChange(t, q, "add-wave", waveChange("add-wave"))
	commitAll(t, q, "plan: add-wave")
	_, err := q.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	ds = slow(q)
	require.Len(t, ds, 1)
	require.Contains(t, ds[0].Message, "add-wave's baseline")
}
