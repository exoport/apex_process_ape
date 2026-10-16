package apexv1

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func failCode(t *testing.T, err error) string {
	t.Helper()
	var fe *FailError
	require.ErrorAs(t, err, &fe)
	return fe.Code
}

// D38: start creates and checks out change/<id> from the current branch,
// records it as base_branch with base_sha, and commits the state there.
func TestStart_CreatesTheChangeBranch(t *testing.T) {
	p := fixture(t, baseFiles())
	addChange(t, p, "add-wave", waveChange("add-wave"))
	commitAll(t, p, "plan: add-wave")
	mainSHA := HeadSHA(ctx(), p.Root)

	res, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, "change/add-wave", res.Branch)
	require.Equal(t, "main", res.BaseBranch)
	require.Equal(t, mainSHA, res.BaseSHA)
	require.Equal(t, "change/add-wave", CurrentBranch(ctx(), p.Root))
	st := state(t, p, "add-wave")
	require.Equal(t, "main", st.BaseBranch)
	require.Equal(t, mainSHA, st.BaseSHA)
	require.Equal(t, mainSHA, run(t, p.Root, "rev-parse", "main"), "the status commit is on the change's branch, not main")
	require.Equal(t, "apex: add-wave in-progress", subjects(t, p)[0])

	// Back on main: the change's status is read from its branch.
	run(t, p.Root, "switch", "-q", "main")
	ci, err := p.requireChange(ctx(), "add-wave")
	require.NoError(t, err)
	require.Equal(t, StatusInProgress, ci.State.Status)
	require.Equal(t, "change/add-wave", ci.Branch)
	require.Equal(t, StatusDraft, state(t, p, "add-wave").Status, "main's working tree still holds the draft")

	// Commands that record the change's state refuse off its branch.
	_, err = p.Verify(ctx(), "add-wave", RunOptions{})
	require.Equal(t, CodeWrongBranch, failCode(t, err))
	require.ErrorContains(t, err, "`git switch change/add-wave`")
	_, err = p.Accept(ctx(), "add-wave", "T2", "ann")
	require.Equal(t, CodeWrongBranch, failCode(t, err))
	_, err = p.Block(ctx(), "add-wave", "why")
	require.Equal(t, CodeWrongBranch, failCode(t, err))

	// Start on main again resumes: it only checks the branch out.
	res, err = p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.True(t, res.Resumed)
	require.Equal(t, "change/add-wave", CurrentBranch(ctx(), p.Root))
	require.Equal(t, "main", res.BaseBranch)

	// On the change's branch, records that belong on the base branch are
	// refused (D11).
	_, err = p.NewChange(ctx(), "add-hug", "M1", []string{"greeting"}, nil)
	require.Equal(t, CodeChangeBranch, failCode(t, err))
	require.ErrorContains(t, err, "`git switch main`")
	_, err = p.ApproveMilestone(ctx(), "M1", "ann")
	require.Equal(t, CodeChangeBranch, failCode(t, err))
	_, err = p.NewMilestone(ctx(), "Next", "./gate.sh", nil, "more")
	require.Equal(t, CodeChangeBranch, failCode(t, err))
	_, err = p.RunGate(ctx(), "M1", RunOptions{})
	require.Equal(t, CodeChangeBranch, failCode(t, err))
	_, err = p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.Equal(t, CodeChangeBranch, failCode(t, err))
	_, err = p.AcceptDecision(ctx(), "ADR-0001", "ann")
	require.Equal(t, CodeChangeBranch, failCode(t, err))
	// An ADR the change needs is written with it, on its branch.
	_, err = p.NewDecision(ctx(), "use-sh", "", "")
	require.NoError(t, err)
}

func TestStart_BranchRefusals(t *testing.T) {
	t.Run("on another change's branch", func(t *testing.T) {
		p := startedWave(t)
		run(t, p.Root, "switch", "-q", "main")
		addChange(t, p, "add-hug", strings.ReplaceAll(waveChange("add-hug"), "Wave", "Hug"))
		commitAll(t, p, "plan: add-hug")
		run(t, p.Root, "switch", "-q", "change/add-wave")
		_, err := p.Start(ctx(), "add-hug", RunOptions{})
		require.Equal(t, CodeWrongBranch, failCode(t, err))
		require.ErrorContains(t, err, "`git switch main`, then start add-hug")
	})
	t.Run("detached HEAD", func(t *testing.T) {
		p := fixture(t, baseFiles())
		addChange(t, p, "add-wave", waveChange("add-wave"))
		commitAll(t, p, "plan")
		run(t, p.Root, "switch", "-q", "--detach")
		_, err := p.Start(ctx(), "add-wave", RunOptions{})
		require.Equal(t, "branch.detached", failCode(t, err))
	})
	t.Run("dirty tree: nothing created", func(t *testing.T) {
		p := fixture(t, baseFiles())
		addChange(t, p, "add-wave", waveChange("add-wave"))
		commitAll(t, p, "plan")
		write(t, p, "notes.txt", "mine\n")
		_, err := p.Start(ctx(), "add-wave", RunOptions{})
		require.Equal(t, "tree.dirty", failCode(t, err))
		require.Equal(t, "main", CurrentBranch(ctx(), p.Root))
		require.False(t, p.branchExists(ctx(), "change/add-wave"))
	})
	t.Run("a refused start leaves no branch", func(t *testing.T) {
		files := baseFiles()
		files["development/roadmap.md"] = strings.Replace(roadmapApproved, "approved (by eval 2026-10-15T12:00:00Z)", "proposed", 1)
		p := fixture(t, files)
		addChange(t, p, "add-wave", waveChange("add-wave"))
		commitAll(t, p, "plan")
		_, err := p.Start(ctx(), "add-wave", RunOptions{})
		require.ErrorContains(t, err, "not approved")
		require.Equal(t, "main", CurrentBranch(ctx(), p.Root))
		require.False(t, p.branchExists(ctx(), "change/add-wave"))
	})
}

// Two changes in flight, each on its branch: each one's status is read
// from its own branch, from anywhere, and start's requirement-conflict
// check sees a change started on another branch.
func TestChanges_ReadFromTheirBranches(t *testing.T) {
	p := startedWave(t)
	run(t, p.Root, "switch", "-q", "main")
	addChange(t, p, "add-wave-two", waveChange("add-wave-two"))
	commitAll(t, p, "plan: add-wave-two")
	_, err := p.Start(ctx(), "add-wave-two", RunOptions{})
	var ce *ConflictError
	require.ErrorAs(t, err, &ce, "add-wave, in progress on its branch, claims the same requirement")
	require.Contains(t, ce.Error(), "add-wave")
	require.Equal(t, "main", CurrentBranch(ctx(), p.Root))

	got := map[string]string{}
	for _, c := range p.Changes() {
		got[c.ID] = c.State.Status + "@" + c.Branch
	}
	require.Equal(t, map[string]string{"add-wave": "in-progress@change/add-wave", "add-wave-two": "draft@"}, got)
}

func TestAbandon_AStartedChangeDeletesItsBranch(t *testing.T) {
	p := startedWave(t)
	write(t, p, "wave.sh", "#!/bin/sh\necho wave\n")
	commitAll(t, p, "feat: wave")
	tip := HeadSHA(ctx(), p.Root)
	mainBefore := run(t, p.Root, "rev-parse", "main")

	res, err := p.Abandon(ctx(), "add-wave", "replaced")
	require.NoError(t, err)
	require.Equal(t, tip, res.BranchTip)
	require.Equal(t, "main", CurrentBranch(ctx(), p.Root), "the move is committed on the base branch")
	require.False(t, p.branchExists(ctx(), "change/add-wave"))
	require.Equal(t, mainBefore, run(t, p.Root, "rev-parse", "HEAD~1"))
	require.Equal(t, "apex: add-wave abandoned", subjects(t, p)[0])
	require.NoFileExists(t, filepath.Join(p.Root, "wave.sh"), "no code of the change reaches main")
	st, err := LoadState(filepath.Join(p.Root, res.ArchivedTo, "state.json"))
	require.NoError(t, err)
	require.Equal(t, StatusAbandoned, st.Status)
	require.Equal(t, tip, st.BranchTip)
	require.Equal(t, "main", st.BaseBranch, "the archive holds the state as the branch had it")
	require.Empty(t, run(t, p.Root, "status", "--porcelain"))
}
