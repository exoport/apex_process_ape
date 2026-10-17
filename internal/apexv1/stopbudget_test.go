package apexv1

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// stopBudget sets the stop hook's budgets for one test.
func stopBudget(t *testing.T, perCheck, total time.Duration) {
	t.Helper()
	oc, ot := StopCheckBudget, StopRunBudget
	StopCheckBudget, StopRunBudget = perCheck, total
	t.Cleanup(func() { StopCheckBudget, StopRunBudget = oc, ot })
}

// D58 2.5: the stop hook never re-runs a check whose last recorded run
// was longer than its per-check budget. With a verify at HEAD the check
// is judged from it — red stays red, with its tail; with code moved since,
// the model is told to run the verify.
func TestStopHook_NeverReRunsALongCheck(t *testing.T) {
	stopBudget(t, time.Second, 10*time.Second)
	p := startedWave(t)
	write(t, p, "wave.sh", "#!/bin/sh\nsleep 2\necho still waving wrong\nexit 1\n")
	commitAll(t, p, "feat: slow wave\n\nChange: add-wave")
	res, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.False(t, res.Green)
	inApeSession(t)

	start := time.Now()
	d := StopHook(ctx(), stopPayload(p.Root, "Stop", false), RunOptions{})
	require.Less(t, time.Since(start), 1500*time.Millisecond, "the 2s check was not re-run")
	require.True(t, d.Block)
	require.Contains(t, d.Reason, "2 command check(s) are red", "T1, and the scenario sharing its command")
	require.Contains(t, d.Reason, "T1 `./wave.sh` exit 1")
	require.Contains(t, d.Reason, "from the last verify at HEAD, not re-run: too long for the stop hook")
	require.Contains(t, d.Reason, "still waving wrong", "with its tail")
	require.Contains(t, d.Reason, "ape change verify add-wave --wait 9m")

	// Code moved since that verify: nothing judges the long check.
	write(t, p, "wave2.sh", "#!/bin/sh\necho 2\n")
	commitAll(t, p, "feat: more\n\nChange: add-wave")
	d = StopHook(ctx(), stopPayload(p.Root, "Stop", false), RunOptions{})
	require.True(t, d.Block)
	require.Contains(t, d.Reason, "2 command check(s) could not be judged here")
	require.Contains(t, d.Reason, "Not re-run here, and no verify at HEAD judged them: T1, greeting/Wave/wave")
	require.Contains(t, d.Reason, "Run `ape change verify add-wave --wait 9m` (exit 75 means still running")
}

// Before any verify a check's length is unknown: the hook runs it under
// its per-check budget, and one that outruns it is not red — it is
// unjudged, and the model is told to verify.
func TestStopHook_UnknownLengthRunsUnderTheBudget(t *testing.T) {
	stopBudget(t, time.Second, 10*time.Second)
	p := startedWave(t)
	write(t, p, "wave.sh", "#!/bin/sh\nsleep 20\n")
	commitAll(t, p, "feat: slow wave\n\nChange: add-wave")
	inApeSession(t)
	start := time.Now()
	d := StopHook(ctx(), stopPayload(p.Root, "Stop", false), RunOptions{})
	require.Less(t, time.Since(start), 8*time.Second)
	require.True(t, d.Block)
	require.Contains(t, d.Reason, "could not be judged here")
	require.NotContains(t, d.Reason, "are red")
}

// The cumulative budget: once one hook run has spent it, the rest are
// judged from the last verify at HEAD.
func TestStopHook_CumulativeBudget(t *testing.T) {
	stopBudget(t, 2*time.Second, 1500*time.Millisecond)
	p := fixture(t, baseFiles())
	addChange(t, p, "add-wave", changeDoc("add-wave", "greeting", addedWave,
		"- T1 add wave — check: ./a.sh\n- T2 wave twice — check: ./b.sh\n"))
	commitAll(t, p, "plan: add-wave")
	_, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	write(t, p, "a.sh", "#!/bin/sh\nsleep 1\necho a-red\nexit 1\n")
	write(t, p, "b.sh", "#!/bin/sh\nsleep 1\necho b-red\nexit 1\n")
	commitAll(t, p, "feat: wave\n\nChange: add-wave")
	_, err = p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	inApeSession(t)
	d := StopHook(ctx(), stopPayload(p.Root, "Stop", false), RunOptions{})
	require.True(t, d.Block)
	require.Contains(t, d.Reason, "3 command check(s) are red")
	require.Contains(t, d.Reason, "T1 `./a.sh` exit 1 (full output: _apex/state/logs/add-wave/stop-T1.log)", "T1 was re-run")
	require.Contains(t, d.Reason, "T2 `./b.sh` exit 1 (full output: _apex/state/logs/add-wave/T2.log) — from the last verify at HEAD", "T2 was over what was left")
}
