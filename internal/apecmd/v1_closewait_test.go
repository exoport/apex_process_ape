package apecmd

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
)

// FINDING-151-E3-1 (eval): a coordinated close SIGTERM'd while its close PR
// waited in main's merge queue returned the queued result with no error,
// so the close said "done" and recorded exit 0 while its PR never merged
// (v1.5.0 by `kill -TERM`; 1.5.1's `ape run stop M1 --force` printed "it
// ended with exit 0"). A cut wait is run.interrupted now, naming the PR as
// still queued, and the close is not reported closed into main.
func TestAwaitClosePR_ACutQueueWaitIsInterrupted(t *testing.T) {
	root, _, _ := gateRecallRepo(t)
	p, err := apexv1.Open(root)
	require.NoError(t, err)
	queued := func() *apexv1.GateResult {
		return &apexv1.GateResult{Milestone: "M1", Status: apexv1.MilestoneDone, Integration: &apexv1.Integration{
			Kind: apexv1.KindClose, State: apexv1.IntegrationQueued, Attempts: 1,
			PR: apexv1.PRRef{Number: 2, URL: "https://github.com/acme/notes/pull/2"},
		}}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the worker's SIGTERM

	g, err := awaitClosePR(ctx, p, "M1", apexv1.RunOptions{}, queued(), nil)
	require.Error(t, err, "a cut wait is not the close's success")
	var fe *apexv1.FailError
	require.ErrorAs(t, err, &fe)
	require.Equal(t, apexv1.CodeRunInterrupted, fe.Code)
	require.ErrorContains(t, err, "https://github.com/acme/notes/pull/2")
	require.ErrorContains(t, err, "still queued, not merged, and M1 is not closed into main")
	require.Equal(t, apexv1.IntegrationQueued, g.Integration.State, "the result says queued, not merged")

	// The control: a close not queued is returned as it is, with no wait.
	merged := queued()
	merged.Integration.State = apexv1.IntegrationMerged
	_, err = awaitClosePR(ctx, p, "M1", apexv1.RunOptions{}, merged, nil)
	require.NoError(t, err)
}
