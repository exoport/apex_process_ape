package apexv1

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
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
		run(t, p.Root, "switch", "-q", "main")
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
		run(t, p.Root, "switch", "-q", "main")
		t.Setenv("SUITE_RED", "1")
		g, err := p.RunGate(ctx(), "M1", RunOptions{})
		require.NoError(t, err)
		require.True(t, g.Run.Green(), g.Run.Why())
		require.True(t, g.Run.Regression.PreExistingRed)
	})
	t.Run("the earliest baseline wins, archived changes included", func(t *testing.T) {
		p := suiteWave(t, true)
		run(t, p.Root, "switch", "-q", "main")
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

// namedSuiteWave is suiteWave with a suite that names its failing tests
// as go test does: TestEnv while SUITE_RED is set, TestNew while
// SUITE_NEW is.
func namedSuiteWave(t *testing.T) *Project {
	t.Helper()
	files := suiteFiles()
	files["suite.sh"] = "#!/bin/sh\necho suite\n" +
		"[ -n \"$SUITE_RED\" ] && echo '--- FAIL: TestEnv (0.01s)'\n" +
		"[ -n \"$SUITE_NEW\" ] && echo '--- FAIL: TestNew (0.00s)'\n" +
		"[ -z \"$SUITE_RED$SUITE_NEW\" ]\n"
	p := fixture(t, files)
	addChange(t, p, "add-wave", waveChange("add-wave"))
	commitAll(t, p, "plan: add-wave")
	t.Setenv("SUITE_RED", "1")
	_, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	write(t, p, "wave.sh", "#!/bin/sh\necho wave\n")
	commitAll(t, p, "feat: wave")
	return p
}

// Q47 (item 11b): a suite already red at baseline hid any new failure
// behind it — verify called every red there "pre-existing". The failing
// tests are compared by name when both runs name them.
func TestVerify_RegressionJudgedByFailingNames(t *testing.T) {
	t.Run("a new failure behind an already-red suite fails verify", func(t *testing.T) {
		p := namedSuiteWave(t)
		require.Equal(t, []string{"TestEnv"}, state(t, p, "add-wave").Regression.Failing, "the baseline records the names")
		t.Setenv("SUITE_NEW", "1")
		v, err := p.Verify(ctx(), "add-wave", RunOptions{})
		require.NoError(t, err)
		require.False(t, v.Green)
		require.True(t, v.Regression.Failed)
		require.False(t, v.Regression.PreExistingRed)
		require.Equal(t, 1, *v.Regression.BaselineExit)
		require.Equal(t, []string{"TestNew"}, v.Regression.NewFailures)
		require.Equal(t, "regression", v.Failing()[0].Task)
	})
	t.Run("the same failures: pre-existing", func(t *testing.T) {
		p := namedSuiteWave(t)
		v, err := p.Verify(ctx(), "add-wave", RunOptions{})
		require.NoError(t, err)
		require.True(t, v.Green, "%+v", v.Failing())
		require.True(t, v.Regression.PreExistingRed)
		require.Empty(t, v.Regression.NewFailures)
	})
	t.Run("fewer failures: pre-existing", func(t *testing.T) {
		p := namedSuiteWave(t)
		st := state(t, p, "add-wave")
		st.Regression.Failing = append(st.Regression.Failing, "TestGoneNow")
		require.NoError(t, SaveState(p.StateFile("add-wave"), st))
		commitAll(t, p, "a baseline with one more failure")
		v, err := p.Verify(ctx(), "add-wave", RunOptions{})
		require.NoError(t, err)
		require.True(t, v.Regression.PreExistingRed)
	})
	t.Run("no names at baseline: the exit codes decide, as before", func(t *testing.T) {
		// A baseline recorded before rc.5 has no failing list.
		p := namedSuiteWave(t)
		st := state(t, p, "add-wave")
		st.Regression.Failing = nil
		require.NoError(t, SaveState(p.StateFile("add-wave"), st))
		commitAll(t, p, "an rc.4 baseline")
		t.Setenv("SUITE_NEW", "1")
		v, err := p.Verify(ctx(), "add-wave", RunOptions{})
		require.NoError(t, err)
		require.True(t, v.Green)
		require.True(t, v.Regression.PreExistingRed)
		require.Empty(t, v.Regression.NewFailures)
	})
	t.Run("no names now: the exit codes decide", func(t *testing.T) {
		p := namedSuiteWave(t)
		write(t, p, "suite.sh", "#!/bin/sh\necho 'make: *** [test] Error 1'\nexit 2\n")
		commitAll(t, p, "the suite crashes before naming anything")
		v, err := p.Verify(ctx(), "add-wave", RunOptions{})
		require.NoError(t, err)
		require.True(t, v.Regression.PreExistingRed)
	})
	t.Run("green at baseline: every failing test is new", func(t *testing.T) {
		p := suiteWave(t, false)
		write(t, p, "suite.sh", "#!/bin/sh\necho '--- FAIL: TestNew (0.00s)'\nexit 1\n")
		commitAll(t, p, "a red suite")
		v, err := p.Verify(ctx(), "add-wave", RunOptions{})
		require.NoError(t, err)
		require.True(t, v.Regression.Failed)
		require.Equal(t, []string{"TestNew"}, v.Regression.NewFailures)
	})
}

func TestGate_RegressionJudgedByFailingNames(t *testing.T) {
	p := namedSuiteWave(t)
	run(t, p.Root, "switch", "-q", "main")
	t.Setenv("SUITE_NEW", "1")
	g, err := p.RunGate(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.False(t, g.Run.Green())
	require.True(t, g.Run.Regression.Failed)
	require.Equal(t, []string{"TestNew"}, g.Run.Regression.NewFailures)
	require.Equal(t, "add-wave", g.Run.Regression.BaselineChange)
	require.Contains(t, g.Run.Why(), "regression command exit 1, red at baseline (add-wave) too, but with 1 failing test(s) the baseline did not have: TestNew")

	t.Setenv("SUITE_NEW", "")
	g, err = p.RunGate(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.True(t, g.Run.Green(), g.Run.Why())
	require.True(t, g.Run.Regression.PreExistingRed)
}

// A state.json written before rc.5 has no failing list: it loads, and
// one written now carries it.
func TestRegressionBaseline_OldStateLoads(t *testing.T) {
	dir := t.TempDir()
	file := dir + "/state.json"
	write := func(s string) { require.NoError(t, os.WriteFile(file, []byte(s), 0o644)) }
	write(`{"id":"add-wave","status":"in-progress","regression_baseline":{"cmd":"./suite.sh","exit":1,"output_sha256":"ab","sha":"c0ffee","at":"2026-10-17T00:00:00Z"}}`)
	st, err := LoadState(file)
	require.NoError(t, err)
	require.Equal(t, &RegressionBaseline{Cmd: "./suite.sh", Exit: 1, OutputSHA256: "ab", SHA: "c0ffee", At: "2026-10-17T00:00:00Z"}, st.Regression)
	st.Regression.Failing = []string{"TestEnv"}
	require.NoError(t, SaveState(file, st))
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	require.Contains(t, string(data), `"failing": [`)
}

// state.json is committed on change/<id>: a baseline stores at most
// regressionFailingCap names, and one naming more stores none — a cut
// list would make a cut name look new — so it is judged by the exit
// codes. The verdict carries at most regressionNewFailuresShown new names.
func TestRegressionBaseline_FailingCap(t *testing.T) {
	p := fixture(t, baseFiles())
	names := func(n int, prefix string) string {
		var b strings.Builder
		for i := range n {
			fmt.Fprintf(&b, "--- FAIL: %s%03d (0.00s)\n", prefix, i)
		}
		return b.String()
	}
	logged := func(name, out string) *CheckResult {
		write(t, p, "_apex/state/logs/"+name, out)
		return &CheckResult{Cmd: "./suite.sh", Exit: 1, Log: "_apex/state/logs/" + name}
	}

	at := p.regressionBaseline(logged("cap500.log", names(regressionFailingCap, "TestEnv")), "c0ffee")
	require.Len(t, at.Failing, regressionFailingCap, "500 names are stored")

	base := p.regressionBaseline(logged("cap501.log", names(regressionFailingCap+1, "TestEnv")), "c0ffee")
	require.Nil(t, base.Failing, "501 names: none stored")
	data, err := json.Marshal(base)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"failing"`)

	now := logged("now.log", names(regressionFailingCap+1, "TestEnv")+"--- FAIL: TestNew (0.00s)\n")
	v := judgeRegression(now, base, p.checkOutput(now))
	require.True(t, v.PreExistingRed, "no stored names: the exit-code rule, red at both is pre-existing")
	require.False(t, v.Failed)
	require.Empty(t, v.NewFailures)

	// Against a green baseline, every failing test is new: 51 shown as
	// 50, with the total.
	now = logged("many.log", names(51, "TestNew"))
	v = judgeRegression(now, &RegressionBaseline{Cmd: "./suite.sh", Exit: 0}, p.checkOutput(now))
	require.True(t, v.Failed)
	require.Len(t, v.NewFailures, regressionNewFailuresShown)
	require.Equal(t, 51, v.NewFailuresTotal)
}
