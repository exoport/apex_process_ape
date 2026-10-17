package apexv1

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// H10: timeouts: and the regression tiers parse, validate, and refuse
// what ape cannot use.
func TestConfig_TimeoutsAndRegressionTiers(t *testing.T) {
	load := func(t *testing.T, yaml string) (Config, error) {
		t.Helper()
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(root+"/_apex", 0o755))
		require.NoError(t, os.WriteFile(ConfigPath(root), []byte(yaml), 0o644))
		return LoadConfig(root)
	}
	cfg, err := load(t, "regression: make test\ntimeouts: {check: 20m, regression: 45m, gate: 1h30m}\n")
	require.NoError(t, err)
	require.Equal(t, RegressionConfig{Change: "make test"}, cfg.Regression)
	require.Equal(t, "make test", cfg.Regression.MilestoneCmd(), "one command is both tiers")
	require.Equal(t, Timeouts{Check: 20 * time.Minute, Regression: 45 * time.Minute, Gate: 90 * time.Minute}, cfg.Timeouts)

	cfg, err = load(t, "regression:\n  change: make test\n  milestone: make test-all\n")
	require.NoError(t, err)
	require.Equal(t, "make test", cfg.Regression.Change)
	require.Equal(t, "make test-all", cfg.Regression.MilestoneCmd())

	cfg, err = load(t, "project_name: x\n")
	require.NoError(t, err)
	require.Equal(t, DefaultCheckTimeout, cfg.Timeouts.For(KindTask, 0))
	require.Equal(t, DefaultCheckTimeout, cfg.Timeouts.For(KindScenario, 0))
	require.Equal(t, DefaultRegressionTimeout, cfg.Timeouts.For(KindRegression, 0))
	require.Equal(t, DefaultGateTimeout, cfg.Timeouts.For(KindGate, 0))
	require.Equal(t, time.Minute, cfg.Timeouts.For(KindGate, time.Minute), "the flag wins")

	for yaml, want := range map[string]string{
		"timeouts: {check: 20}\n":                  "timeouts.check",
		"timeouts: {check: -5m}\n":                 "timeouts.check",
		"timeouts: {check: soon}\n":                "timeouts.check",
		"timeouts: {chek: 5m}\n":                   "timeouts.chek: not a key",
		"timeouts: 5m\n":                           "timeouts: is not a mapping",
		"regression: {change: a, milstone: b}\n":   "regression.milstone: not a key",
		"regression: {milestone: make test-all}\n": "without regression.change",
	} {
		_, err := load(t, yaml)
		require.ErrorContains(t, err, want, yaml)
	}
}

// H10: a check runs under timeouts.check; the --timeout flag overrides it.
func TestRunChecks_ConfiguredTimeout(t *testing.T) {
	p := fixture(t, baseFiles())
	write(t, p, "_apex/config.yaml", "timeouts: {check: 1s}\n")
	start := time.Now()
	r := p.RunChecks(ctx(), p.LogDir("x"), "", []PlannedCheck{{Ref: "T1", Kind: KindTask, Cmd: "sleep 20"}}, RunOptions{})[0]
	require.Equal(t, ExitTimeout, r.Exit)
	require.Less(t, time.Since(start), 15*time.Second)
	r = p.RunChecks(ctx(), p.LogDir("x"), "", []PlannedCheck{{Ref: "T1", Kind: KindTask, Cmd: "sleep 2; true"}}, RunOptions{Timeout: 10 * time.Second})[0]
	require.Equal(t, 0, r.Exit, "the flag overrides the config")
}

// twoCapFiles is baseFiles plus a second capability, farewell, that the
// wave change does not touch: one scenario shares the wave task's
// command, the other has its own.
func twoCapFiles() map[string]string {
	files := baseFiles()
	files["development/specs/farewell/spec.md"] = `# Farewell
Purpose: Say goodbye.

### Requirement: Say bye
The system SHALL print Bye.
#### Scenario: bye
- GIVEN nothing WHEN bye runs THEN it prints Bye
- check: ./bye.sh

### Requirement: Wave goodbye
The system SHALL wave when leaving.
#### Scenario: leaving wave
- GIVEN nothing WHEN leaving THEN it waves
- check: ./wave.sh
`
	files["bye.sh"] = "#!/bin/sh\necho Bye\n"
	files["gate.sh"] = "#!/bin/sh\nexit 0\n"
	return files
}

// H10: verify runs the task checks, the effective spec of the touched
// capabilities, and an untouched scenario only when it shares a task's
// command; the milestone gate runs the whole spec.
func TestVerify_RunsTheTouchedCapabilities(t *testing.T) {
	p := fixture(t, twoCapFiles())
	write(t, p, "bye.sh", "#!/bin/sh\nexit 1\n") // broken, but untouched by the change
	addChange(t, p, "add-wave", waveChange("add-wave"))
	commitAll(t, p, "plan: add-wave")
	_, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	write(t, p, "wave.sh", "#!/bin/sh\necho wave\n")
	commitAll(t, p, "feat: wave")

	v, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.True(t, v.Green, "%+v", v.Results)
	var refs []string
	for i := range v.Results {
		refs = append(refs, v.Results[i].Task)
	}
	require.ElementsMatch(t, []string{
		"T1",
		"greeting/Say hello/plain run", "greeting/Say name/named", "greeting/Wave/wave",
		"farewell/Wave goodbye/leaving wave", // shares ./wave.sh with T1
	}, refs, "farewell/Say bye/bye is the gate's to run")
	for _, b := range state(t, p, "add-wave").Baseline {
		require.NotEqual(t, "farewell/Say bye/bye", b.Task, "the baseline is what verify runs")
	}

	run(t, p.Root, "switch", "-q", "main")
	g, err := p.RunGate(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.False(t, g.Run.Green(), "the gate runs the whole spec")
	var failed []string
	for i := range g.Run.SpecFailed {
		failed = append(failed, g.Run.SpecFailed[i].Task)
	}
	require.Contains(t, failed, "farewell/Say bye/bye")
}

// H10: the gate runs the milestone tier of the regression command; start
// and verify run the change tier.
func TestRegressionTiers(t *testing.T) {
	files := suiteFiles()
	files["_apex/config.yaml"] = "project_name: greeter\nregression: {change: ./suite.sh, milestone: ./suite-all.sh}\n"
	files["suite-all.sh"] = "#!/bin/sh\necho all\n[ -z \"$ALL_RED\" ]\n"
	p := fixture(t, files)
	addChange(t, p, "add-wave", waveChange("add-wave"))
	commitAll(t, p, "plan: add-wave")
	res, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, "./suite.sh", res.Regression.Cmd)
	write(t, p, "wave.sh", "#!/bin/sh\necho wave\n")
	commitAll(t, p, "feat: wave")
	t.Setenv("ALL_RED", "1") // only the milestone tier is red
	v, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.True(t, v.Green)
	require.Equal(t, "./suite.sh", v.Regression.Cmd)

	run(t, p.Root, "switch", "-q", "main")
	g, err := p.RunGate(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, "./suite-all.sh", g.Run.Regression.Cmd)
	require.True(t, g.Run.Regression.Failed, "no change baselines the milestone tier: red is red")
	t.Setenv("ALL_RED", "")
	g, err = p.RunGate(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.True(t, g.Run.Green())
}

// H10: the stop hook re-runs only the checks red at the last verify of
// the current plan; before any verify, the task checks.
func TestStopHook_RerunsTheLastVerifysRedChecks(t *testing.T) {
	t.Setenv(envApeSession, "1")
	files := baseFiles()
	p := fixture(t, files)
	addChange(t, p, "two-tasks", changeDoc("two-tasks", "greeting", addedWave,
		"- T1 add wave — check: ./wave.sh\n- T2 count waves — check: ./count.sh\n"))
	commitAll(t, p, "plan: two-tasks")
	_, err := p.Start(ctx(), "two-tasks", RunOptions{})
	require.NoError(t, err)

	// No verify yet: the task checks run, both red.
	d := StopHook(ctx(), stopPayload(p.Root, "Stop", false), RunOptions{})
	require.True(t, d.Block)
	require.Contains(t, d.Reason, "2 command check(s) are red")

	// T1 green, T2 red at the verify: only T2 is re-run.
	write(t, p, "wave.sh", "#!/bin/sh\necho wave\n")
	commitAll(t, p, "feat: wave")
	v, err := p.Verify(ctx(), "two-tasks", RunOptions{})
	require.NoError(t, err)
	require.False(t, v.Green)
	lv := p.lastVerify("two-tasks")
	require.NotNil(t, lv)
	require.Equal(t, map[string]string{"T2": "./count.sh"}, lv.Red)
	write(t, p, "wave.sh", "#!/bin/sh\nexit 3\n") // broken again, but green at the verify
	d = StopHook(ctx(), stopPayload(p.Root, "Stop", false), RunOptions{})
	require.True(t, d.Block)
	require.Contains(t, d.Reason, "1 command check(s) are red")
	require.Contains(t, d.Reason, "T2 `./count.sh`")
	require.NotContains(t, d.Reason, "T1")

	write(t, p, "count.sh", "#!/bin/sh\necho 1\n")
	d = StopHook(ctx(), stopPayload(p.Root, "Stop", false), RunOptions{})
	require.False(t, d.Block)
	require.Equal(t, "all command checks green", d.Record.Reason)

	// A verify with nothing red: the hook has nothing to run.
	write(t, p, "wave.sh", "#!/bin/sh\necho wave\n")
	commitAll(t, p, "feat: count")
	v, err = p.Verify(ctx(), "two-tasks", RunOptions{})
	require.NoError(t, err)
	require.True(t, v.Green)
	// Back to in-progress (a review block) with the code broken since.
	write(t, p, "development/changes/two-tasks/review.md", "no\n")
	_, err = p.Review(ctx(), "two-tasks", "block", "development/changes/two-tasks/review.md")
	require.NoError(t, err)
	write(t, p, "wave.sh", "#!/bin/sh\nexit 3\n")
	d = StopHook(ctx(), stopPayload(p.Root, "Stop", false), RunOptions{})
	require.False(t, d.Block)
	require.Equal(t, "no check was red at the last verify", d.Record.Reason)
}
