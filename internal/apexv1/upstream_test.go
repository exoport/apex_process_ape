package apexv1

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// trackUpstream configures origin/main as main's upstream without any
// remote: the remote-tracking ref is set to sha by hand, which is all ape
// reads (it never fetches).
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
