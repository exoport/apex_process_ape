package apexv1

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// trackUpstream configures origin/main as main's upstream without any
// remote: the remote-tracking ref is set to sha by hand, which is all ape
// reads (an uncoordinated project never fetches).
func trackUpstream(t *testing.T, p *Project, sha string) {
	t.Helper()
	run(t, p.Root, "config", "remote.origin.url", "/nonexistent")
	run(t, p.Root, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	run(t, p.Root, "config", "branch.main.remote", "origin")
	run(t, p.Root, "config", "branch.main.merge", "refs/heads/main")
	run(t, p.Root, "update-ref", "refs/remotes/origin/main", sha)
}

// upstreamAhead is a commit on top of main's tip that only origin/main has.
func upstreamAhead(t *testing.T, p *Project) string {
	t.Helper()
	return run(t, p.Root, "commit-tree", "-p", "main", "-m", "someone else's push", "main^{tree}")
}

// D45 / H6: a change is neither started from nor archived onto a base
// branch behind its upstream; status shows the upstream either way.
func TestBehindUpstream_StartArchiveStatus(t *testing.T) {
	p := fixture(t, baseFiles())
	addChange(t, p, "add-wave", changeDoc("add-wave", "greeting", addedWave, "- T1 wave — check: ./wave.sh"))
	commitAll(t, p, "plan: add-wave")

	require.Nil(t, p.Status().BaseUpstream, "no upstream configured: no check, nothing shown")

	trackUpstream(t, p, run(t, p.Root, "rev-parse", "main"))
	u := p.Status().BaseUpstream
	require.Equal(t, &BaseUpstream{Branch: "main", Upstream: "origin/main"}, u)

	trackUpstream(t, p, upstreamAhead(t, p))
	head := HeadSHA(ctx(), p.Root)
	_, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.Equal(t, CodeBehindUpstream, failCode(t, err))
	require.ErrorContains(t, err, "main is 1 commit(s) behind its upstream origin/main")
	require.Equal(t, "main", CurrentBranch(ctx(), p.Root))
	require.Equal(t, head, HeadSHA(ctx(), p.Root))
	require.False(t, p.branchExists(ctx(), "change/add-wave"))
	r := p.Status()
	require.Equal(t, 1, r.BaseUpstream.Behind)
	require.Contains(t, r.Brief(), "Base: main vs origin/main: 0 ahead, 1 behind — pull")

	// Ahead only (local work not pushed yet) is fine.
	trackUpstream(t, p, run(t, p.Root, "rev-parse", "main~1"))
	_, err = p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, "change/add-wave", CurrentBranch(ctx(), p.Root))
	u = p.Status().BaseUpstream
	require.Equal(t, "main", u.Branch, "on a change branch, status measures its base")
	require.Equal(t, 1, u.Ahead)
}

func TestBehindUpstream_ArchiveRefuses(t *testing.T) {
	p := reviewedWave(t)
	_, err := p.Accept(ctx(), "add-wave", "T2", "ann")
	require.NoError(t, err)
	commitAll(t, p, "review: add-wave")
	trackUpstream(t, p, upstreamAhead(t, p))
	head, mainBefore := HeadSHA(ctx(), p.Root), run(t, p.Root, "rev-parse", "main")
	_, err = p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.Equal(t, CodeBehindUpstream, failCode(t, err))
	require.Equal(t, head, HeadSHA(ctx(), p.Root))
	require.Equal(t, mainBefore, run(t, p.Root, "rev-parse", "main"))
	clean(t, p, "a refused archive")
}

// A re-entry that would catch up with the local base refuses too.
func TestBehindUpstream_ReentryCatchUpRefuses(t *testing.T) {
	p := verifiedWaveOnSuite(t)
	moveMain(t, p, map[string]string{"hug.sh": "#!/bin/sh\necho hug\n"})
	run(t, p.Root, "switch", "-q", "main")
	trackUpstream(t, p, upstreamAhead(t, p))
	_, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.Equal(t, CodeBehindUpstream, failCode(t, err))
	require.ErrorContains(t, err, "catching add-wave up")
}

// coordinatedConfig makes a project coordinated (coordination.github).
const coordinatedConfig = "coordination:\n  ensemble: shop\n  home: ../home\n  url: nats://127.0.0.1:4222\n  github:\n    repo: shop/notes\n"

// remoteAhead gives p a real bare origin holding main, then pushes one more
// commit to it that the local origin/main does not know of yet: another
// member's push, visible only to a fetch.
func remoteAhead(t *testing.T, p *Project) (bare, before string) {
	t.Helper()
	bare = t.TempDir()
	run(t, bare, "init", "-q", "--bare")
	run(t, p.Root, "config", "remote.origin.url", bare)
	run(t, p.Root, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	run(t, p.Root, "config", "branch.main.remote", "origin")
	run(t, p.Root, "config", "branch.main.merge", "refs/heads/main")
	run(t, p.Root, "push", "-q", "origin", "main")
	before = run(t, p.Root, "rev-parse", "refs/remotes/origin/main")
	run(t, p.Root, "push", "-q", bare, upstreamAhead(t, p)+":refs/heads/main")
	run(t, p.Root, "update-ref", "refs/remotes/origin/main", before)
	return bare, before
}

// v1.5 section 5.5: a coordinated project fetches its trunk before judging
// it, so another member's push stops a start from the stale main; an
// uncoordinated one never fetches (D45), and starts from what it has.
func TestBehindUpstream_CoordinatedFetches(t *testing.T) {
	files := baseFiles()
	files["_apex/config.yaml"] = coordinatedConfig
	p := fixture(t, files)
	bare := withOrigin(t, p)
	_, _, err := p.PrepareMilestoneBranch(ctx(), "M1")
	require.NoError(t, err)
	run(t, p.Root, "switch", "-q", "milestone/M1")
	addChange(t, p, "add-wave", changeDoc("add-wave", "greeting", addedWave, "- T1 wave — check: ./wave.sh"))
	commitAll(t, p, "plan: add-wave")
	// Another member's change merges into milestone/M1 on the remote.
	before := run(t, p.Root, "rev-parse", "refs/remotes/origin/milestone/M1")
	run(t, p.Root, "push", "-q", bare, run(t, p.Root, "commit-tree", "-p", before, "-m", "someone else's merge", before+"^{tree}")+":refs/heads/milestone/M1")
	require.Equal(t, before, run(t, p.Root, "rev-parse", "refs/remotes/origin/milestone/M1"), "not fetched yet")
	local := run(t, p.Root, "rev-parse", "milestone/M1")

	_, err = p.Start(ctx(), "add-wave", RunOptions{})
	require.Equal(t, CodeBehindUpstream, failCode(t, err), "the fetch saw the member's push")
	require.ErrorContains(t, err, "milestone/M1 is 1 commit(s) behind its upstream origin/milestone/M1")
	require.Equal(t, run(t, bare, "rev-parse", "milestone/M1"), run(t, p.Root, "rev-parse", "refs/remotes/origin/milestone/M1"))
	require.Equal(t, "milestone/M1", CurrentBranch(ctx(), p.Root))
	require.Equal(t, local, run(t, p.Root, "rev-parse", "milestone/M1"), "only the remote-tracking ref moved")

	// A fetch that fails refuses, and writes nothing.
	run(t, p.Root, "config", "remote.origin.url", "/nonexistent")
	_, err = p.Start(ctx(), "add-wave", RunOptions{})
	require.Equal(t, CodeFetchFailed, failCode(t, err))
	require.ErrorContains(t, err, "git fetch origin refs/heads/milestone/M1 failed")
	require.False(t, p.branchExists(ctx(), "change/add-wave"))
}

func TestBehindUpstream_UncoordinatedNeverFetches(t *testing.T) {
	p := fixture(t, baseFiles())
	addChange(t, p, "add-wave", changeDoc("add-wave", "greeting", addedWave, "- T1 wave — check: ./wave.sh"))
	commitAll(t, p, "plan: add-wave")
	_, before := remoteAhead(t, p)

	_, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, before, run(t, p.Root, "rev-parse", "refs/remotes/origin/main"), "no fetch")
	require.Equal(t, "change/add-wave", CurrentBranch(ctx(), p.Root))
}
