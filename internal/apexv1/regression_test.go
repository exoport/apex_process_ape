package apexv1

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// suiteFiles is baseFiles with a regression command configured: the
// suite is red while SUITE_RED is set, and the gate passes.
func suiteFiles() map[string]string {
	files := baseFiles()
	files["_apex/config.yaml"] = "project_name: greeter\nregression: ./suite.sh\n"
	files["suite.sh"] = "#!/bin/sh\necho suite\n[ -z \"$SUITE_RED\" ]\n"
	files["gate.sh"] = "#!/bin/sh\nexit 0\n"
	return files
}

// suiteWave is a started add-wave whose regression baseline was taken
// with the suite red or green.
func suiteWave(t *testing.T, redAtBaseline bool) *Project {
	t.Helper()
	p := fixture(t, suiteFiles())
	addChange(t, p, "add-wave", waveChange("add-wave"))
	commitAll(t, p, "plan: add-wave")
	if redAtBaseline {
		t.Setenv("SUITE_RED", "1")
	}
	res, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err, "a red regression baseline does not block start")
	require.NotNil(t, res.Regression)
	t.Setenv("SUITE_RED", "")
	write(t, p, "wave.sh", "#!/bin/sh\necho wave\n")
	commitAll(t, p, "feat: wave")
	return p
}

func TestStart_RecordsTheRegressionBaseline(t *testing.T) {
	p := suiteWave(t, true)
	st := state(t, p, "add-wave")
	require.NotNil(t, st.Regression)
	require.Equal(t, "./suite.sh", st.Regression.Cmd)
	require.Equal(t, 1, st.Regression.Exit)
	require.Equal(t, st.BaseSHA, st.Regression.SHA)
	require.Len(t, st.Regression.OutputSHA256, 64)
	require.FileExists(t, p.LogDir("add-wave")+"/baseline-regression.log")
	clean(t, p, "start")
}

func TestVerify_RegressionFailsOnlyWhenNewlyRed(t *testing.T) {
	t.Run("green at baseline, red now: verify fails", func(t *testing.T) {
		p := suiteWave(t, false)
		t.Setenv("SUITE_RED", "1")
		v, err := p.Verify(ctx(), "add-wave", RunOptions{})
		require.NoError(t, err)
		require.False(t, v.Green)
		require.True(t, v.Regression.Failed)
		require.False(t, v.Regression.PreExistingRed)
		require.Equal(t, 0, *v.Regression.BaselineExit)
		require.Equal(t, "regression", v.Failing()[0].Task)
		require.Equal(t, KindRegression, v.Failing()[0].Kind)
	})
	t.Run("red at baseline, still red: reported, not failed", func(t *testing.T) {
		p := suiteWave(t, true)
		t.Setenv("SUITE_RED", "1")
		v, err := p.Verify(ctx(), "add-wave", RunOptions{})
		require.NoError(t, err)
		require.True(t, v.Green, "%+v", v.Failing())
		require.Equal(t, &RegressionVerdict{
			Cmd: "./suite.sh", Exit: 1, OutputSHA256: v.Regression.OutputSHA256, Log: "_apex/state/logs/add-wave/regression.log",
			BaselineExit: v.Regression.BaselineExit, BaselineSHA: state(t, p, "add-wave").Regression.SHA, PreExistingRed: true,
		}, v.Regression)
		require.Equal(t, 1, *v.Regression.BaselineExit)
		rec := state(t, p, "add-wave").Verify.Results
		last := rec[len(rec)-1]
		require.Equal(t, KindRegression, last.Kind, "recorded in the verify results like a task check")
		require.Equal(t, 1, last.Exit)
		require.True(t, last.PreExistingRed)
		require.Equal(t, StatusVerified, v.Status)
	})
	t.Run("green now: green", func(t *testing.T) {
		p := suiteWave(t, true)
		v, err := p.Verify(ctx(), "add-wave", RunOptions{})
		require.NoError(t, err)
		require.True(t, v.Green)
		require.False(t, v.Regression.Failed)
		require.False(t, v.Regression.PreExistingRed)
	})
}

// A regression command configured (or changed) after start has no
// baseline: red fails verify, and re-start records one — keeping the
// original otherwise.
func TestRegression_ReStartBaselinesOnlyANewCommand(t *testing.T) {
	p := startedWave(t)
	write(t, p, "wave.sh", "#!/bin/sh\necho wave\n")
	write(t, p, "suite.sh", "#!/bin/sh\nexit 3\n")
	write(t, p, "_apex/config.yaml", "regression: ./suite.sh\n")
	commitAll(t, p, "feat: wave, and a suite")
	taskBaseline := state(t, p, "add-wave").Baseline

	v, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.False(t, v.Green, "no baseline: nothing shows the red was there before")
	require.True(t, v.Regression.Failed)
	require.Nil(t, v.Regression.BaselineExit)

	res, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err, "a regression command without a baseline is a reason to re-start")
	require.Equal(t, 3, res.Regression.Exit)
	st := state(t, p, "add-wave")
	require.Equal(t, taskBaseline, st.Baseline, "the task and scenario baselines are kept")
	require.Empty(t, st.Replans, "the plan did not change")
	first := *st.Regression

	_, err = p.Start(ctx(), "add-wave", RunOptions{})
	require.ErrorContains(t, err, "nothing to re-baseline")
	v, err = p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.True(t, v.Green)
	require.True(t, v.Regression.PreExistingRed)

	write(t, p, "_apex/config.yaml", "regression: ./suite.sh --all\n")
	commitAll(t, p, "chore: the suite takes --all")
	res, err = p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.NotNil(t, res.Regression, "a changed command is baselined again")
	require.Equal(t, "./suite.sh --all", state(t, p, "add-wave").Regression.Cmd)
	require.NotEqual(t, first.Cmd, state(t, p, "add-wave").Regression.Cmd)
}

func TestGate_RegressionJudgedByTheMilestonesEarliestBaseline(t *testing.T) {
	t.Run("green at baseline, red now: the gate is red", func(t *testing.T) {
		p := suiteWave(t, false)
		t.Setenv("SUITE_RED", "1")
		g, err := p.RunGate(ctx(), "M1", RunOptions{})
		require.NoError(t, err)
		require.False(t, g.Run.Green())
		require.True(t, g.Run.Regression.Failed)
		require.Equal(t, "add-wave", g.Run.Regression.BaselineChange)
		require.Contains(t, g.Run.Why(), "regression command exit 1, green at baseline (add-wave)")
	})
	t.Run("red at baseline, still red: green, reported", func(t *testing.T) {
		p := suiteWave(t, true)
		t.Setenv("SUITE_RED", "1")
		g, err := p.RunGate(ctx(), "M1", RunOptions{})
		require.NoError(t, err)
		require.True(t, g.Run.Green(), g.Run.Why())
		require.True(t, g.Run.Regression.PreExistingRed)
	})
	t.Run("the earliest baseline wins, archived changes included", func(t *testing.T) {
		p := suiteWave(t, true)
		write(t, p, "development/archive/2026-01-01-add-hug/change.md", "---\nid: add-hug\nmilestone: M1\ntouches: [greeting]\n---\n# Add hug\n")
		require.NoError(t, SaveState(p.ArchiveDir()+"/2026-01-01-add-hug/state.json", &State{
			ID: "add-hug", Status: StatusArchived,
			Regression: &RegressionBaseline{Cmd: "./suite.sh", Exit: 0, SHA: "abc", At: "2000-01-01T00:00:00Z"},
		}))
		commitAll(t, p, "an archived change with an older, green baseline")
		t.Setenv("SUITE_RED", "1")
		g, err := p.RunGate(ctx(), "M1", RunOptions{})
		require.NoError(t, err)
		require.False(t, g.Run.Green())
		require.Equal(t, "add-hug", g.Run.Regression.BaselineChange)
	})
	t.Run("no baseline in the milestone: red fails the gate", func(t *testing.T) {
		p := fixture(t, suiteFiles())
		t.Setenv("SUITE_RED", "1")
		g, err := p.CloseMilestone(ctx(), "M1", RunOptions{})
		require.ErrorContains(t, err, "no change of the milestone has a baseline for it")
		require.Nil(t, g.Run.Regression.BaselineExit)
		t.Setenv("SUITE_RED", "")
		_, err = p.CloseMilestone(ctx(), "M1", RunOptions{})
		require.NoError(t, err, "green with no baseline is green")
	})
}

// Backlog 37: status lists what was red at baseline and is no change's own
// — the regression command, regression scenarios — never the change's own
// test-first checks.
func TestStatus_PreExistingRed(t *testing.T) {
	files := suiteFiles()
	files["greet.sh"] = "#!/bin/sh\n[ -z \"$GREET_RED\" ] && echo Hello $1\n"
	p := fixture(t, files)
	addChange(t, p, "add-wave", waveChange("add-wave"))
	commitAll(t, p, "plan: add-wave")
	t.Setenv("SUITE_RED", "1")
	t.Setenv("GREET_RED", "1")
	_, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)

	r := p.Status()
	sha := state(t, p, "add-wave").BaseSHA
	require.Equal(t, []PreExistingRed{
		{Change: "add-wave", Kind: KindRegression, Ref: "regression", Cmd: "./suite.sh", Exit: 1, SHA: sha},
		{Change: "add-wave", Kind: KindScenario, Ref: "greeting/Say hello/plain run", Cmd: "./greet.sh | grep -q Hello", Exit: 1, SHA: sha},
		{Change: "add-wave", Kind: KindScenario, Ref: "greeting/Say name/named", Cmd: "./greet.sh Ann | grep -q Ann", Exit: 1, SHA: sha},
	}, r.PreExistingRed, "T1 and the delta's wave scenario were red too, and are not listed")
	require.Contains(t, r.Brief(), "Red at baseline, not any change's own (roadmap it): regression `./suite.sh`, greeting/Say hello/plain run")

	require.Empty(t, fixture(t, baseFiles()).Status().PreExistingRed)
	require.NotNil(t, fixture(t, baseFiles()).Status().PreExistingRed, "always a list in JSON")
}
