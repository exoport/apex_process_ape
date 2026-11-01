package apexv1

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeIntegrator is the coordinated repository's API for archive: it
// records each call and answers from its fields.
type fakeIntegrator struct {
	mu       sync.Mutex
	opened   []string // head→base
	enqueued []int
	states   map[int]PRState
	openErr  error
	// numbers: each head's PR number, from 7 up, as GitHub reuses an open
	// PR for its head.
	numbers map[string]int
	// onEnqueue, when set, runs as a PR is enqueued (outside the lock): a
	// test's queue merging it.
	onEnqueue func(head string, n int)
}

func (f *fakeIntegrator) OpenPR(_ context.Context, head, base, _, _ string) (PRRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.openErr != nil {
		return PRRef{}, f.openErr
	}
	f.opened = append(f.opened, head+"→"+base)
	if f.numbers == nil {
		f.numbers = map[string]int{}
	}
	n, ok := f.numbers[head]
	if !ok {
		n = 7 + len(f.numbers)
		f.numbers[head] = n
	}
	return PRRef{Number: n, NodeID: fmt.Sprintf("PR_%d", n), URL: fmt.Sprintf("https://github.example/shop/notes/pull/%d", n)}, nil
}

func (f *fakeIntegrator) Enqueue(_ context.Context, pr PRRef) (string, error) {
	f.mu.Lock()
	f.enqueued = append(f.enqueued, pr.Number)
	head := ""
	for h, n := range f.numbers {
		if n == pr.Number {
			head = h
		}
	}
	hook := f.onEnqueue
	f.mu.Unlock()
	if hook != nil {
		hook(head, pr.Number)
	}
	return "QUEUED", nil
}

func (f *fakeIntegrator) PRState(_ context.Context, n int) (PRState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.states[n], nil
}

func useIntegrator(t *testing.T, f *fakeIntegrator) {
	t.Helper()
	old := NewIntegrator
	NewIntegrator = func(*Project) (Integrator, error) { return f, nil }
	t.Cleanup(func() { NewIntegrator = old })
}

// coordinatedReviewedWave is reviewedWaveFrom in a coordinated project:
// M1 claimed (milestone/M1 cut and pushed), the change planned, started,
// verified, reviewed and its T2 accepted on it.
func coordinatedReviewedWave(t *testing.T) (p *Project, bare string) {
	t.Helper()
	files := baseFiles()
	files["_apex/config.yaml"] = coordinatedConfig
	p = fixture(t, files)
	bare = withOrigin(t, p)
	_, _, err := p.PrepareMilestoneBranch(ctx(), "M1")
	require.NoError(t, err)
	run(t, p.Root, "switch", "-q", "milestone/M1")
	addChange(t, p, "add-wave", waveChange("add-wave"))
	commitAll(t, p, "plan: add-wave")
	_, err = p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	write(t, p, "wave.sh", "#!/bin/sh\necho wave\n")
	commitAll(t, p, "feat: wave")
	_, err = p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	write(t, p, "development/changes/add-wave/review.md", "fine\n")
	_, err = p.Review(ctx(), "add-wave", "pass", "development/changes/add-wave/review.md")
	require.NoError(t, err)
	_, err = p.Accept(ctx(), "add-wave", "T2", "ann")
	require.NoError(t, err)
	return p, bare
}

// v1.5 stage 1a step 6 (section 5.1): a coordinated archive pushes its one
// squash commit to apex/<id>, opens the PR into milestone/<M> and enqueues
// it; milestone/<M> itself is untouched (the change lands through the PR)
// and change/<id> is kept. A re-run while queued redoes nothing.
func TestCoordinatedArchive_PushesOpensEnqueues(t *testing.T) {
	p, bare := coordinatedReviewedWave(t)
	f := &fakeIntegrator{states: map[int]PRState{}}
	useIntegrator(t, f)
	baseBefore := run(t, p.Root, "rev-parse", "milestone/M1")
	remoteBefore := run(t, bare, "rev-parse", "milestone/M1")

	res, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)
	require.NotNil(t, res.Integration)
	in := res.Integration
	require.Equal(t, IntegrationQueued, in.State)
	require.Equal(t, 7, in.PR.Number)
	require.Equal(t, 1, in.Attempts)
	require.Equal(t, []string{"apex/add-wave→milestone/M1"}, f.opened)
	require.Equal(t, []int{7}, f.enqueued)

	require.Equal(t, res.Commit, run(t, bare, "rev-parse", "apex/add-wave"), "the squash was pushed")
	require.Equal(t, baseBefore, run(t, bare, "rev-parse", "apex/add-wave^"), "one commit on the base tip")
	require.Equal(t, "add-wave", run(t, p.Root, "log", "-1", "--format=%(trailers:key=Change,valueonly)", res.Commit))
	require.Equal(t, baseBefore, run(t, p.Root, "rev-parse", "milestone/M1"), "the base is untouched: the change lands through its PR")
	require.Equal(t, remoteBefore, run(t, bare, "rev-parse", "milestone/M1"), "the queue merges it, not ape (the plan commit rides the PR, section 5.4)")
	require.True(t, p.branchExists(ctx(), "change/add-wave"), "kept for an ejection's catch-up")
	require.Equal(t, in.Commit, p.LoadIntegration("add-wave").Commit, "recorded in the checkout's state")

	f.states[7] = PRState{Queue: "AWAITING_CHECKS"}
	again, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)
	require.Equal(t, in.PR, again.Integration.PR)
	require.Len(t, f.opened, 1, "queued: nothing redone")
	require.Equal(t, res.Commit, run(t, bare, "rev-parse", "apex/add-wave"))
}

// A push that fails rolls the squash back; a PR that cannot open leaves
// the pushed branch and the base unchanged, and a second archive retries.
func TestCoordinatedArchive_FailuresWriteNothing(t *testing.T) {
	p, _ := coordinatedReviewedWave(t)
	f := &fakeIntegrator{states: map[int]PRState{}, openErr: errors.New("HTTP 422: Validation Failed")}
	useIntegrator(t, f)
	baseBefore := run(t, p.Root, "rev-parse", "milestone/M1")

	_, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.Equal(t, CodeIntegrationPR, failCode(t, err))
	require.ErrorContains(t, err, "Validation Failed")
	require.Equal(t, baseBefore, run(t, p.Root, "rev-parse", "milestone/M1"))
	require.Nil(t, p.LoadIntegration("add-wave"))

	f.openErr = nil
	res, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)
	require.Equal(t, IntegrationQueued, res.Integration.State)

	q, _ := coordinatedReviewedWave(t)
	useIntegrator(t, &fakeIntegrator{states: map[int]PRState{}})
	qBase := run(t, q.Root, "rev-parse", "milestone/M1")
	run(t, q.Root, "config", "remote.origin.pushurl", "/nonexistent")
	_, err = q.Archive(ctx(), "add-wave", false, RunOptions{})
	require.Equal(t, CodeIntegrationPush, failCode(t, err))
	require.Equal(t, qBase, run(t, q.Root, "rev-parse", "milestone/M1"))
	require.Equal(t, "change/add-wave", CurrentBranch(ctx(), q.Root))
	require.Nil(t, q.LoadIntegration("add-wave"))
}

// queueMerges stands in for the merge queue: it squashes apex/<id>'s tree
// onto the remote's milestone/M1 and returns the merge commit.
func queueMerges(t *testing.T, bare, id string) string {
	t.Helper()
	tip := run(t, bare, "rev-parse", "milestone/M1")
	sha := run(t, bare, "commit-tree", "-p", tip, "-m", "feat: merged by the queue", "apex/"+id+"^{tree}")
	run(t, bare, "update-ref", "refs/heads/milestone/M1", sha)
	return sha
}

// Merged: the local base moves to the merge, the change branch's tip is
// kept and the branch deleted, the checkout ends on the base.
func TestCoordinatedArchive_Merged(t *testing.T) {
	p, bare := coordinatedReviewedWave(t)
	f := &fakeIntegrator{states: map[int]PRState{}}
	useIntegrator(t, f)
	_, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)
	tip := run(t, p.Root, "rev-parse", "change/add-wave")

	merge := queueMerges(t, bare, "add-wave")
	f.states[7] = PRState{Merged: true, MergeCommit: merge}
	res, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)
	require.Equal(t, merge, res.Commit)
	require.Equal(t, IntegrationMerged, p.LoadIntegration("add-wave").State)
	require.Equal(t, merge, run(t, p.Root, "rev-parse", "milestone/M1"), "the base is the merged tip")
	require.Equal(t, "milestone/M1", CurrentBranch(ctx(), p.Root))
	require.False(t, p.branchExists(ctx(), "change/add-wave"))
	require.Equal(t, tip, run(t, p.Root, "rev-parse", "refs/apex/archive/add-wave"))
	require.NoDirExists(t, p.ChangeDir("add-wave"), "its folder moved to the archive in the merged squash")
	require.Len(t, f.enqueued, 1)
}

// Ejected (out of the queue, not merged) after another member's merge:
// archive catches up with the moved base and enqueues again, an attempt;
// the ejection that reaches limits.attempts blocks the change.
func TestCoordinatedArchive_EjectedReenqueuesThenBlocks(t *testing.T) {
	p, bare := coordinatedReviewedWave(t)
	f := &fakeIntegrator{states: map[int]PRState{}}
	useIntegrator(t, f)
	first, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)

	// Another change merged first; this PR was ejected (its queue entry
	// gone, not merged).
	tip := run(t, bare, "rev-parse", "milestone/M1")
	theirs := run(t, bare, "commit-tree", "-p", tip, "-m", "feat: theirs", tip+"^{tree}")
	run(t, bare, "update-ref", "refs/heads/milestone/M1", theirs)
	f.states[7] = PRState{}

	res, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)
	require.NotNil(t, res.Integration)
	require.Equal(t, 2, res.Integration.Attempts)
	require.Equal(t, IntegrationQueued, res.Integration.State)
	require.NotEqual(t, first.Commit, res.Commit, "squashed again on the moved base")
	run(t, p.Root, "merge-base", "--is-ancestor", theirs, res.Commit)
	require.Equal(t, res.Commit, run(t, bare, "rev-parse", "apex/add-wave"), "force-pushed")
	require.Equal(t, []int{7, 7}, f.enqueued)

	in := p.LoadIntegration("add-wave")
	in.Attempts = p.limits().AttemptsCap()
	require.NoError(t, p.saveIntegration(*in))
	_, err = p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.Equal(t, CodeIntegrationEjections, failCode(t, err))
	st, reason := p.ChangeStatus(ctx(), "add-wave")
	require.Equal(t, StatusBlocked, st)
	require.Contains(t, reason, "merge queue")
	require.Len(t, f.enqueued, 2, "blocked: not enqueued again")
}

// The worker's wait: ArchiveUntilMerged enqueues, re-reads the PR at each
// wake or poll while queued, and returns once it merged; with ctx ended
// first, it returns the queued result.
func TestCoordinatedArchive_UntilMerged(t *testing.T) {
	old := QueuePoll
	QueuePoll = 50 * time.Millisecond
	t.Cleanup(func() { QueuePoll = old })
	p, bare := coordinatedReviewedWave(t)
	f := &fakeIntegrator{states: map[int]PRState{7: {Queue: "AWAITING_CHECKS"}}}
	useIntegrator(t, f)

	short, cancel := context.WithTimeout(ctx(), 300*time.Millisecond)
	defer cancel()
	res, err := p.ArchiveUntilMerged(short, "add-wave", false, RunOptions{}, nil)
	require.NoError(t, err)
	require.Equal(t, IntegrationQueued, res.Integration.State, "ctx ended while queued")

	wake := make(chan struct{}, 1)
	merge := queueMerges(t, bare, "add-wave")
	go func() {
		time.Sleep(200 * time.Millisecond)
		f.mu.Lock()
		f.states[7] = PRState{Merged: true, MergeCommit: merge}
		f.mu.Unlock()
		wake <- struct{}{}
	}()
	res, err = p.ArchiveUntilMerged(ctx(), "add-wave", false, RunOptions{}, wake)
	require.NoError(t, err)
	require.Equal(t, IntegrationMerged, res.Integration.State)
	require.Equal(t, res.Commit, run(t, p.Root, "rev-parse", "milestone/M1"))
	require.Len(t, f.enqueued, 1)
}
