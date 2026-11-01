package apexv1

import (
	"os"
	"path/filepath"
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
	require.Equal(t, []string{"milestone/M1→main"}, f.opened, "the close PR's head is the milestone's branch itself")
	require.Equal(t, "milestone/M1", CurrentBranch(ctx(), p.Root), "the close ran on the milestone's branch")
	tip := run(t, p.Root, "rev-parse", "milestone/M1")
	require.Equal(t, tip, run(t, bare, "rev-parse", "milestone/M1"), "the close record landed on the remote's milestone branch")
	require.Equal(t, "apex: M1 done", run(t, p.Root, "log", "-1", "--format=%s", tip), "the close record is the branch's last commit")
	require.Equal(t, mainBefore, run(t, bare, "rev-parse", "main"), "main moves only through its queue")
	events, _ := os.ReadFile(p.EventsPath())
	require.NotContains(t, string(events), EventMilestoneDone, "milestone-done waits for the merge")

	f.states[7] = PRState{Queue: "AWAITING_CHECKS"}
	_, err = p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.Len(t, f.opened, 1, "queued: nothing redone")

	// Another milestone closed first; this PR was ejected. The close takes
	// main in itself (the take-in lands on milestone/M1), then enqueues
	// the same PR again: its head followed the branch.
	pushFile(t, bare, "main", "notes.txt", "M0 shipped\n")
	f.states[7] = PRState{}
	g, err = p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, 2, g.Integration.Attempts)
	require.Equal(t, []int{7, 7}, f.enqueued, "the same PR, enqueued again")
	require.Equal(t, g.Integration.Commit, run(t, bare, "rev-parse", "milestone/M1"))
	run(t, bare, "merge-base", "--is-ancestor", "main", "milestone/M1")
	run(t, bare, "merge-base", "--is-ancestor", tip, "milestone/M1")
	require.Equal(t, "take-in-1", run(t, bare, "log", "-1", "--format=%(trailers:key=Take-in,valueonly)", "milestone/M1"))

	f.states[7] = PRState{Queue: "AWAITING_CHECKS"}
	release := mergeOnRemote(t, bare, "main", "milestone/M1")
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

// A close whose take-in conflicts is refused with the take-in's
// change.conflicted, laid out for apex-apply; no close PR opens.
func TestClosePR_TakeInConflictRefuses(t *testing.T) {
	files := baseFiles()
	files["_apex/config.yaml"] = coordinatedConfig
	files["gate.sh"] = "#!/bin/sh\nexit 0\n"
	p := fixture(t, files)
	bare := withOrigin(t, p)
	_, _, err := p.PrepareMilestoneBranch(ctx(), "M1")
	require.NoError(t, err)
	f := &fakeIntegrator{states: map[int]PRState{}}
	useIntegrator(t, f)
	pushFile(t, bare, "milestone/M1", "greet.sh", "#!/bin/sh\necho Hi $1\n")
	pushFile(t, bare, "main", "greet.sh", "#!/bin/sh\necho Howdy $1\n")

	_, err = p.CloseMilestone(ctx(), "M1", RunOptions{})
	var ce *ConflictError
	require.ErrorAs(t, err, &ce)
	require.Equal(t, CodeConflicted, ce.Code)
	require.Equal(t, "change/take-in-1", CurrentBranch(ctx(), p.Root))
	require.Empty(t, f.opened, "no close PR before main is in")
}

// D708 (no runner in 1a): an ejected close PR goes back into main's queue
// only after the gate ran again on the branch as it now is; red refuses.
func TestClosePR_RedoRegatesAndRefusesRed(t *testing.T) {
	files := baseFiles()
	files["_apex/config.yaml"] = coordinatedConfig
	files["gate.sh"] = "#!/bin/sh\ntest ! -f boom\n"
	p := fixture(t, files)
	bare := withOrigin(t, p)
	_, _, err := p.PrepareMilestoneBranch(ctx(), "M1")
	require.NoError(t, err)
	f := &fakeIntegrator{states: map[int]PRState{}}
	useIntegrator(t, f)
	_, err = p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.Len(t, f.enqueued, 1)

	// Ejected; main brings what turns the gate red.
	pushFile(t, bare, "main", "boom", "x\n")
	f.states[7] = PRState{}
	_, err = p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.Equal(t, "gate.red", failCode(t, err))
	require.ErrorContains(t, err, "not enqueued")
	require.Len(t, f.enqueued, 1, "not enqueued again on red")
}

// D708: main moving between the close's gate and its enqueue is taken in
// and gated again before the PR goes into the queue.
func TestClosePR_MainMovedDuringGate(t *testing.T) {
	clone, mark := t.TempDir(), filepath.Join(t.TempDir(), "moved")
	files := baseFiles()
	files["_apex/config.yaml"] = coordinatedConfig
	files["gate.sh"] = "#!/bin/sh\nif [ ! -f " + mark + " ]; then touch " + mark + "; cd " + clone +
		" && echo moved >> notes.txt && git add notes.txt && git -c user.name=t -c user.email=t@t commit -qm 'elsewhere: main moved' && git push -q origin main; fi\nexit 0\n"
	p := fixture(t, files)
	bare := withOrigin(t, p)
	run(t, clone, "clone", "-q", "--branch", "main", bare, ".")
	_, _, err := p.PrepareMilestoneBranch(ctx(), "M1")
	require.NoError(t, err)
	f := &fakeIntegrator{states: map[int]PRState{}}
	useIntegrator(t, f)

	g, err := p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.FileExists(t, mark, "the gate ran and moved main")
	require.Len(t, f.enqueued, 1)
	run(t, bare, "merge-base", "--is-ancestor", "main", "milestone/M1")
	require.Equal(t, g.Integration.Commit, run(t, bare, "rev-parse", "milestone/M1"), "enqueued on the tree with the moved main")
}
