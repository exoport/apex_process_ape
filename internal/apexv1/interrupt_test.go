package apexv1

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// cancelAfter is a context a signal cancels d from now, as Execute's
// signal context is cancelled by SIGTERM.
func cancelAfter(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	c, cancel := context.WithCancel(context.Background())
	tm := time.AfterFunc(d, cancel)
	t.Cleanup(func() { tm.Stop(); cancel() })
	return c
}

func requireInterrupted(t *testing.T, err error) {
	t.Helper()
	fe, ok := err.(*FailError)
	require.True(t, ok, "%v", err)
	require.Equal(t, CodeRunInterrupted, fe.Code)
	require.Equal(t, ActionRetry, ActionFor(fe.Code))
	require.Contains(t, fe.Msg, "nothing was recorded and no attempt counted")
}

// D58 2.2: a verify interrupted mid-check is not a red attempt — it
// records nothing: no attempt, no last verify, no state commit.
func TestVerify_InterruptedIsNotRed(t *testing.T) {
	p := startedWave(t)
	write(t, p, "wave.sh", "#!/bin/sh\nsleep 30\n")
	commitAll(t, p, "feat: slow wave\n\nChange: add-wave")
	before := subjects(t, p)
	stBefore := read(t, p, "development/changes/add-wave/state.json")

	start := time.Now()
	res, err := p.Verify(cancelAfter(t, 500*time.Millisecond), "add-wave", RunOptions{})
	requireInterrupted(t, err)
	require.Nil(t, res)
	require.Less(t, time.Since(start), 10*time.Second, "the check was stopped, not waited for")
	require.Zero(t, p.attempts("add-wave").Since, "no attempt counted")
	require.Zero(t, p.attempts("add-wave").Total)
	require.Nil(t, p.lastVerify("add-wave"), "no last verify recorded")
	require.Equal(t, before, subjects(t, p), "nothing committed")
	require.Equal(t, stBefore, read(t, p, "development/changes/add-wave/state.json"))
	clean(t, p, "an interrupted verify")
}

// An interrupted start leaves no branch and HEAD where it began, the
// change a draft.
func TestStart_InterruptedWritesNothing(t *testing.T) {
	p := fixture(t, baseFiles())
	addChange(t, p, "add-wave", waveChange("add-wave"))
	write(t, p, "wave.sh", "#!/bin/sh\nsleep 30\n")
	commitAll(t, p, "plan: add-wave")
	base := CurrentBranch(ctx(), p.Root)
	before := subjects(t, p)

	_, err := p.Start(cancelAfter(t, 500*time.Millisecond), "add-wave", RunOptions{})
	requireInterrupted(t, err)
	require.Equal(t, base, CurrentBranch(ctx(), p.Root))
	require.False(t, p.branchExists(ctx(), ChangeBranch("add-wave")), "no branch left")
	require.Equal(t, StatusDraft, state(t, p, "add-wave").Status)
	require.Equal(t, before, subjects(t, p))
	clean(t, p, "an interrupted start")
}
