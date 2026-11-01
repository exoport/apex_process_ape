package apexv1

import (
	"context"
	"fmt"
	"sync"
	"testing"

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

// D702 (stage 1a step 6, reworked): a coordinated archive lands its one
// squash commit on milestone/<M> itself — v1.4's archive, then one
// fast-forward push — and deletes change/<id> as v1.4 does.
func TestCoordinatedArchive_LandsOnMilestone(t *testing.T) {
	p, bare := coordinatedReviewedWave(t)
	res, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)
	require.Equal(t, "milestone/M1", res.BaseBranch)
	require.Equal(t, res.Commit, run(t, p.Root, "rev-parse", "milestone/M1"))
	require.Equal(t, res.Commit, run(t, bare, "rev-parse", "milestone/M1"), "landed on the remote's milestone branch")
	require.Equal(t, "add-wave", run(t, p.Root, "log", "-1", "--format=%(trailers:key=Change,valueonly)", res.Commit))
	require.False(t, p.branchExists(ctx(), "change/add-wave"))
	require.Equal(t, "milestone/M1", CurrentBranch(ctx(), p.Root))
	require.NoDirExists(t, p.ChangeDir("add-wave"))
}

// The single writer, enforced at the push: a lease the fence finds lost
// refuses before anything is pushed; a branch that moved between the fetch
// and the push (another writer) is refused by the fast-forward push, and
// the squash is rolled back. Either way nothing lands and change/<id> is
// checked out again.
func TestCoordinatedArchive_FenceAndMovedRefuse(t *testing.T) {
	p, bare := coordinatedReviewedWave(t)
	remoteBefore := run(t, bare, "rev-parse", "milestone/M1")
	localBefore := run(t, p.Root, "rev-parse", "milestone/M1")
	old := FenceMilestone
	t.Cleanup(func() { FenceMilestone = old })

	FenceMilestone = func(context.Context, *Project, string) error {
		return &FailError{Code: CodeNotLeaseHolder, Msg: "M1's lease is held by bob/xyz"}
	}
	_, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.Equal(t, CodeNotLeaseHolder, failCode(t, err))
	require.Equal(t, remoteBefore, run(t, bare, "rev-parse", "milestone/M1"), "nothing pushed")
	require.Equal(t, localBefore, run(t, p.Root, "rev-parse", "milestone/M1"), "the squash rolled back")
	require.Equal(t, "change/add-wave", CurrentBranch(ctx(), p.Root))

	// Another writer lands on the remote between ape's fetch and its push.
	var theirs string
	FenceMilestone = func(context.Context, *Project, string) error {
		theirs = pushFile(t, bare, "milestone/M1", "theirs.txt", "x\n")
		return nil
	}
	_, err = p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.Equal(t, CodeMilestoneMoved, failCode(t, err))
	require.Equal(t, theirs, run(t, bare, "rev-parse", "milestone/M1"), "the other writer's commit stands; ours did not land")
	require.Equal(t, localBefore, run(t, p.Root, "rev-parse", "milestone/M1"))
	require.Equal(t, "change/add-wave", CurrentBranch(ctx(), p.Root))

	// Archive again: it fetches, catches up and lands on top.
	FenceMilestone = nil
	res, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)
	require.Equal(t, res.Commit, run(t, bare, "rev-parse", "milestone/M1"))
	run(t, p.Root, "merge-base", "--is-ancestor", theirs, res.Commit)
}

// PLAN-V15-GHAPP 9.3: with GitVia set (the App service's tokens), the
// fetches (read) and the push (push) go to its URL with its environment —
// the trunk's remote is not used at all — and ape moves the
// remote-tracking refs itself.
func TestCoordinatedArchive_GitVia(t *testing.T) {
	p, bare := coordinatedReviewedWave(t)
	old := GitVia
	t.Cleanup(func() { GitVia = old })
	purposes := map[string]int{}
	GitVia = func(_ context.Context, _ *Project, purpose string) (string, []string, error) {
		purposes[purpose]++
		return bare, []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=AUTHORIZATION: basic eDp5"}, nil
	}
	run(t, p.Root, "config", "remote.origin.url", "/nonexistent")
	res, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err, "the remote's URL is broken: everything went through GitVia")
	require.Positive(t, purposes["read"])
	require.Equal(t, 1, purposes["push"])
	require.Equal(t, res.Commit, run(t, bare, "rev-parse", "milestone/M1"))
	require.Equal(t, res.Commit, run(t, p.Root, "rev-parse", "refs/remotes/origin/milestone/M1"), "the tracking ref follows a push by URL")
}

// ape ghapp fetch: branches fetched (through GitVia when set) and the
// local ones that are only behind fast-forwarded; ahead is left alone.
func TestFetchAndSync(t *testing.T) {
	files := baseFiles()
	files["_apex/config.yaml"] = coordinatedConfig
	p := fixture(t, files)
	bare := withOrigin(t, p)
	theirs := pushFile(t, bare, "main", "notes.txt", "merged elsewhere\n")
	lines, err := p.FetchAndSync(ctx(), "main")
	require.NoError(t, err)
	require.Contains(t, lines[0], "fast-forwarded")
	require.Equal(t, theirs, run(t, p.Root, "rev-parse", "main"))
	lines, err = p.FetchAndSync(ctx(), "main")
	require.NoError(t, err)
	require.Contains(t, lines[0], "up to date")
	commitOnTop(t, p, "local.txt", "x\n", "local: not pushed")
	pushFile(t, bare, "main", "more.txt", "y\n")
	lines, err = p.FetchAndSync(ctx(), "main")
	require.NoError(t, err)
	require.Contains(t, lines[0], "left as it is")
}

// The mirror's PR #2: an acceptance landed through main's merge queue
// arrives with a merge commit whose body repeats the PR's title, "apex:
// config accepted by …". The guard takes the commit whose subject is the
// acceptance, so the change it accepted is no finding.
func TestConfigAcceptance_ThroughAMergeCommit(t *testing.T) {
	p := fixture(t, baseFiles())
	run(t, p.Root, "commit", "-q", "--allow-empty", "-m", "apex: project init")
	run(t, p.Root, "switch", "-q", "-c", "apex/coord")
	write(t, p, "_apex/config.yaml", coordinatedConfig)
	commitAll(t, p, "chore(config): coordination")
	run(t, p.Root, "switch", "-q", "main")
	run(t, p.Root, "merge", "-q", "--no-ff", "-m", "Merge pull request #1 from shop/apex/coord\n\nchore(config): coordination", "apex/coord")
	require.Contains(t, findingMessages(p.provenanceFindings(ctx())), "guarded config keys (coordination)")

	run(t, p.Root, "switch", "-q", "-c", "apex/accept")
	_, err := p.AcceptConfig(ctx(), "ann")
	require.NoError(t, err)
	run(t, p.Root, "switch", "-q", "main")
	run(t, p.Root, "merge", "-q", "--no-ff", "-m", "Merge pull request #2 from shop/apex/accept\n\napex: config accepted by ann", "apex/accept")
	require.NotContains(t, findingMessages(p.provenanceFindings(ctx())), "guarded config keys", "the acceptance in the merged branch counts")
}
