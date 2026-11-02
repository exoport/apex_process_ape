package apexv1

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// withOrigin gives p a bare origin holding its main, main tracking it.
func withOrigin(t *testing.T, p *Project) string {
	t.Helper()
	bare := t.TempDir()
	run(t, bare, "init", "-q", "--bare")
	run(t, p.Root, "remote", "add", "origin", bare)
	run(t, p.Root, "push", "-q", "-u", "origin", "main")
	return bare
}

// v1.5 stage 1a step 5 (section 5.1): the claim cuts milestone/<M> from
// the remote's main and pushes it; a second checkout's claim tracks the
// remote's; M's change is planned and started on it. The control: an
// uncoordinated project prepares nothing and plans on main.
func TestMilestoneBranch_ClaimCutsItAndChangesUseIt(t *testing.T) {
	files := baseFiles()
	files["_apex/config.yaml"] = coordinatedConfig
	p := fixture(t, files)
	bare := withOrigin(t, p)

	_, err := p.NewChange(ctx(), "add-wave", "M1", []string{"greeting"}, nil)
	require.Equal(t, CodeMilestoneBranchMissing, failCode(t, err), "not claimed here: no milestone/M1")
	require.Equal(t, "main", CurrentBranch(ctx(), p.Root))

	branch, created, err := p.PrepareMilestoneBranch(ctx(), "M1")
	require.NoError(t, err)
	require.Equal(t, "milestone/M1", branch)
	require.True(t, created)
	require.Equal(t, run(t, bare, "rev-parse", "main"), run(t, bare, "rev-parse", "milestone/M1"), "cut from the remote's main")
	require.Equal(t, "origin/milestone/M1", run(t, p.Root, "rev-parse", "--abbrev-ref", "milestone/M1@{upstream}"))
	require.Equal(t, "main", CurrentBranch(ctx(), p.Root), "the claim switches nothing")
	_, created, err = p.PrepareMilestoneBranch(ctx(), "M1")
	require.NoError(t, err)
	require.False(t, created, "a second claim keeps the remote's")

	_, err = p.NewChange(ctx(), "add-wave", "M1", []string{"greeting"}, nil)
	require.NoError(t, err)
	require.Equal(t, "milestone/M1", CurrentBranch(ctx(), p.Root), "M1's change is planned on milestone/M1")
	addChange(t, p, "add-wave", changeDoc("add-wave", "greeting", addedWave, "- T1 wave — check: ./wave.sh"))
	commitAll(t, p, "plan: add-wave")
	run(t, p.Root, "switch", "-q", "main")
	run(t, p.Root, "switch", "-q", "milestone/M1")
	res, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err, "%+v", res)
	st, err := LoadState(p.StateFile("add-wave"))
	require.NoError(t, err)
	require.Equal(t, "milestone/M1", st.BaseBranch, "its base is the milestone's branch")

	// Another member's push to milestone/M1; a second checkout's claim
	// tracks the remote's branch as it is — M1's claim is recorded on
	// main (D736: a re-claim or a handover adopts).
	run(t, p.Root, "push", "-q", bare, run(t, p.Root, "commit-tree", "-p", "milestone/M1", "-m", "a merged change", "milestone/M1^{tree}")+":refs/heads/milestone/M1")
	run(t, p.Root, "push", "-q", bare, run(t, p.Root, "commit-tree", "-p", "main", "-m", "apex: M1 claimed by dana/abc123", "main^{tree}")+":refs/heads/main")
	q := fixture(t, files)
	run(t, q.Root, "remote", "add", "origin", bare)
	run(t, q.Root, "fetch", "-q", "origin")
	run(t, q.Root, "reset", "-q", "--hard", "origin/main")
	run(t, q.Root, "branch", "-q", "-u", "origin/main")
	_, created, err = q.PrepareMilestoneBranch(ctx(), "M1")
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, run(t, bare, "rev-parse", "milestone/M1"), run(t, q.Root, "rev-parse", "milestone/M1"))

	// A push that fails is the fetch code, and leaves no tracking ref.
	r := fixture(t, files)
	run(t, r.Root, "remote", "add", "origin", t.TempDir()+"/none")
	_, _, err = r.PrepareMilestoneBranch(ctx(), "M2")
	require.Equal(t, CodeFetchFailed, failCode(t, err))
}

func TestMilestoneBranch_UncoordinatedPreparesNothing(t *testing.T) {
	p := fixture(t, baseFiles())
	bare := withOrigin(t, p)
	branch, created, err := p.PrepareMilestoneBranch(ctx(), "M1")
	require.NoError(t, err)
	require.Empty(t, branch)
	require.False(t, created)
	_, err = p.NewChange(ctx(), "add-wave", "M1", []string{"greeting"}, nil)
	require.NoError(t, err)
	require.Equal(t, "main", CurrentBranch(ctx(), p.Root))
	_, err = gitRaw(ctx(), bare, "rev-parse", "--verify", "--quiet", "refs/heads/milestone/M1")
	require.Error(t, err, "nothing pushed")
}

// D736: a claim judges a milestone/<M> the remote already has when no claim
// of M is recorded on main. (a) A leftover with nothing of its own (an
// ancestor of main, such as the mirror's R3 probe milestone/M9) is
// fast-forwarded to main, so M starts from main. (b) One with commits main
// lacks is refused, milestone.branch-exists, and nothing moves. (c)
// --adopt takes it as it is. The control: M1's recorded claim adopts its
// branch as it is (TestMilestoneBranch_ClaimCutsItAndChangesUseIt).
func TestMilestoneBranch_ExistingBranchJudged(t *testing.T) {
	files := baseFiles()
	files["_apex/config.yaml"] = coordinatedConfig
	p := fixture(t, files)
	bare := withOrigin(t, p)
	probe := run(t, bare, "rev-parse", "main")
	run(t, bare, "branch", "milestone/M9", "main") // the leftover, at main's old tip
	pushFile(t, bare, "main", "later.txt", "main moved on\n")
	mainTip := run(t, bare, "rev-parse", "main")

	// (a)
	branch, created, note, err := p.PrepareMilestoneBranchAdopt(ctx(), "M9", false)
	require.NoError(t, err)
	require.Equal(t, "milestone/M9", branch)
	require.False(t, created)
	require.Contains(t, note, "existed on origin at "+short(probe)+" with nothing of its own")
	require.Equal(t, mainTip, run(t, bare, "rev-parse", "milestone/M9"), "fast-forwarded to main on the remote")
	require.Equal(t, mainTip, run(t, p.Root, "rev-parse", "milestone/M9"), "and here")

	// (b)
	run(t, bare, "branch", "milestone/M5", "main")
	own := pushFile(t, bare, "milestone/M5", "old-work.txt", "an earlier milestone's work\n")
	_, created, _, err = p.PrepareMilestoneBranchAdopt(ctx(), "M5", false)
	require.Equal(t, CodeMilestoneBranchExists, failCode(t, err))
	require.False(t, created)
	require.ErrorContains(t, err, "1 commit(s) main lacks")
	require.ErrorContains(t, err, "--adopt")
	require.Equal(t, own, run(t, bare, "rev-parse", "milestone/M5"), "nothing moved")

	// (c)
	_, _, note, err = p.PrepareMilestoneBranchAdopt(ctx(), "M5", true)
	require.NoError(t, err)
	require.Empty(t, note)
	require.Equal(t, own, run(t, p.Root, "rev-parse", "milestone/M5"), "adopted as it is")
}
