package apexv1

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// mergeOnRemote merges branch from into branch into on the bare remote
// with a merge commit, as a queue merging a PR does; it returns the merge.
func mergeOnRemote(t *testing.T, bare, into, from string) string {
	t.Helper()
	tmp := t.TempDir()
	run(t, tmp, "clone", "-q", "--branch", into, bare, ".")
	run(t, tmp, "merge", "-q", "--no-ff", "--no-edit", "origin/"+from)
	run(t, tmp, "push", "-q", "origin", into)
	return run(t, tmp, "rev-parse", "HEAD")
}

// v1.5 stage 1a step 8 (section 5.3): a coordinated close runs the gate on
// milestone/<M>, commits the close record there as its last commit, and
// opens the close PR into main; ejected after main moved, it needs main
// taken in, then goes again (an attempt); merged, main fast-forwards to
// the merge, milestone/<M> is deleted here and on the remote, and
// milestone-done names the release point.
func TestClosePR(t *testing.T) {
	files := baseFiles()
	files["_apex/config.yaml"] = coordinatedConfig
	files["gate.sh"] = "#!/bin/sh\nexit 0\n"
	p := fixture(t, files)
	bare := withOrigin(t, p)
	_, _, err := p.PrepareMilestoneBranch(ctx(), "M1")
	require.NoError(t, err)
	f := &fakeIntegrator{states: map[int]PRState{}}
	useIntegrator(t, f)
	mainBefore := run(t, bare, "rev-parse", "main")

	g, err := p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.True(t, g.Run.Green())
	require.NotNil(t, g.Integration)
	require.Equal(t, KindClose, g.Integration.Kind)
	require.Equal(t, []string{"apex/close-M1→main"}, f.opened)
	require.Equal(t, "milestone/M1", CurrentBranch(ctx(), p.Root), "the close ran on the milestone's branch")
	tip := run(t, p.Root, "rev-parse", "milestone/M1")
	require.Equal(t, tip, run(t, bare, "rev-parse", "apex/close-M1"))
	require.Equal(t, "apex: M1 done", run(t, p.Root, "log", "-1", "--format=%s", tip), "the close record is the branch's last commit")
	require.Equal(t, mainBefore, run(t, bare, "rev-parse", "main"), "main moves only through its queue")
	events, _ := os.ReadFile(p.EventsPath())
	require.NotContains(t, string(events), EventMilestoneDone, "milestone-done waits for the merge")

	f.states[7] = PRState{Queue: "AWAITING_CHECKS"}
	_, err = p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.Len(t, f.opened, 1, "queued: nothing redone")

	// Another milestone closed first; this PR was ejected.
	pushFile(t, bare, "main", "notes.txt", "M0 shipped\n")
	f.states[7] = PRState{}
	_, err = p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.Equal(t, CodeTakeInNeeded, failCode(t, err))

	mergeOnRemote(t, bare, "milestone/M1", "main") // the take-in merged
	g, err = p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, 2, g.Integration.Attempts)
	require.Equal(t, "apex: M1 done", run(t, p.Root, "log", "-1", "--format=%s", "milestone/M1"), "the close record replayed on top of the take-in")
	require.Equal(t, g.Integration.Commit, run(t, bare, "rev-parse", "apex/close-M1"), "pushed again")
	run(t, bare, "merge-base", "--is-ancestor", "main", "apex/close-M1")

	f.states[7] = PRState{Queue: "AWAITING_CHECKS"}
	release := mergeOnRemote(t, bare, "main", "apex/close-M1")
	f.states[7] = PRState{Merged: true, MergeCommit: release}
	g, err = p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, release, g.Commit)
	require.Equal(t, IntegrationMerged, g.Integration.State)
	require.Equal(t, "main", CurrentBranch(ctx(), p.Root))
	require.Equal(t, release, run(t, p.Root, "rev-parse", "main"))
	require.False(t, p.branchExists(ctx(), "milestone/M1"))
	_, err = gitRaw(ctx(), bare, "rev-parse", "--verify", "--quiet", "refs/heads/milestone/M1")
	require.Error(t, err, "deleted on the remote")
	rm := p.roadmap()
	require.Equal(t, MilestoneDone, rm.Find("M1").Status, "the close record reached main")
	events, _ = os.ReadFile(p.EventsPath())
	require.Contains(t, string(events), "the release point")
	require.Equal(t, 1, strings.Count(string(events), `"milestone-done"`))

	g, err = p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.NoError(t, err, "closed: a second close is a no-op")
	require.Equal(t, MilestoneDone, g.Status)
}
