package apexv1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// caughtUpFiles is suiteFiles with a suite that is red while flaky.txt
// exists — something the base can break on its own.
func caughtUpFiles() map[string]string {
	files := suiteFiles()
	files["suite.sh"] = "#!/bin/sh\necho suite\n[ ! -f flaky.txt ]\n"
	return files
}

// verifiedWaveOnSuite is add-wave started (suite green at its baseline),
// its wave.sh committed and verified green: status verified.
func verifiedWaveOnSuite(t *testing.T) *Project {
	t.Helper()
	p := fixture(t, caughtUpFiles())
	addChange(t, p, "add-wave", waveChange("add-wave"))
	commitAll(t, p, "plan: add-wave")
	_, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	write(t, p, "wave.sh", "#!/bin/sh\necho wave\n")
	commitAll(t, p, "feat: wave")
	v, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.True(t, v.Green)
	require.Equal(t, StatusVerified, v.Status)
	return p
}

// moveMain lands a commit on main and checks change/add-wave out again.
func moveMain(t *testing.T, p *Project, files map[string]string) string {
	t.Helper()
	run(t, p.Root, "switch", "-q", "main")
	for rel, body := range files {
		write(t, p, rel, body)
	}
	commitAll(t, p, "fix: landed on main")
	tip := HeadSHA(ctx(), p.Root)
	run(t, p.Root, "switch", "-q", "change/add-wave")
	return tip
}

func worktrees(t *testing.T, p *Project) int {
	t.Helper()
	return strings.Count(run(t, p.Root, "worktree", "list", "--porcelain"), "worktree ")
}

// D40: the base moved — verify rebases change/<id> onto it, re-takes the
// baseline at the NEW BASE TIP (where the change's own wave.sh does not
// exist, so T1 is red there, and where the base's own flaky.txt makes the
// regression command red), marks the old verify stale, then verifies the
// rebased code: the suite is red at its new baseline, so not a failure.
func TestCatchUp_VerifyRebasesAndRebaselinesAtTheBaseTip(t *testing.T) {
	p := verifiedWaveOnSuite(t)
	oldBase := state(t, p, "add-wave").BaseSHA
	tip := moveMain(t, p, map[string]string{"hug.sh": "#!/bin/sh\necho hug\n", "flaky.txt": "x\n"})

	v, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	cu := v.CatchUp
	require.NotNil(t, cu)
	require.True(t, cu.Rebased)
	require.Equal(t, "main", cu.BaseBranch)
	require.Equal(t, oldBase, cu.From)
	require.Equal(t, tip, cu.To)
	require.True(t, cu.VerifyStale)
	require.Equal(t, StatusInProgress, cu.Status, "a verified change goes back to in-progress")
	require.True(t, p.isAncestor(ctx(), tip, "HEAD"), "change/add-wave sits on main's tip")
	require.Contains(t, subjects(t, p), "apex: add-wave rebased onto main")
	require.Equal(t, 1, worktrees(t, p), "the baseline worktree is removed")

	st := state(t, p, "add-wave")
	require.Equal(t, tip, st.BaseSHA)
	require.Len(t, st.Rebases, 1)
	require.Equal(t, Rebase{At: st.Rebases[0].At, From: oldBase, To: tip, Head: st.Rebases[0].Head, Rebased: true}, st.Rebases[0])
	byTask := map[string]Baseline{}
	for _, b := range st.Baseline {
		byTask[b.Task] = b
		require.Equal(t, tip, b.SHA)
	}
	require.Equal(t, 127, byTask["T1"].Exit, "the change's own new test is red at the base tip — not green, as it would be at the rebased HEAD")
	require.Equal(t, 127, byTask["greeting/Wave/wave"].Exit)
	require.Equal(t, 0, byTask["greeting/Say hello/plain run"].Exit)
	require.Equal(t, 1, st.Regression.Exit, "the regression baseline is the base's: flaky.txt is red there")
	require.Equal(t, tip, st.Regression.SHA)
	require.FileExists(t, filepath.Join(p.LogDir("add-wave"), "baseline-regression.log"))
	require.Equal(t, "_apex/state/logs/add-wave/baseline-regression.log", cu.Regression.Log)

	require.True(t, v.Green, "the suite is red, as it was at the new baseline: pre-existing, not a failure")
	require.True(t, v.Regression.PreExistingRed)
	require.Empty(t, v.GreenAtBaseline, "nothing of the change's own was green at the base tip")
	require.Equal(t, StatusVerified, v.Status)
	require.False(t, st.Verify.Stale, "the new verify replaced the stale one")
	clean(t, p, "catch-up")
}

// Start on a change whose branch exists catches it up too; a verified
// change keeps its old verify, marked stale, and goes back to
// in-progress. Nothing happens when the base did not move.
func TestCatchUp_StartReentryMarksTheVerifyStale(t *testing.T) {
	p := verifiedWaveOnSuite(t)
	run(t, p.Root, "switch", "-q", "main")
	res, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.True(t, res.Resumed)
	require.Nil(t, res.CatchUp, "the base did not move: nothing to catch up")
	require.Equal(t, StatusVerified, state(t, p, "add-wave").Status)
	v, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.Nil(t, v.CatchUp)
	require.Empty(t, state(t, p, "add-wave").Rebases)
	require.NotContains(t, subjects(t, p), "apex: add-wave rebased onto main")

	tip := moveMain(t, p, map[string]string{"hug.sh": "#!/bin/sh\necho hug\n"})
	run(t, p.Root, "switch", "-q", "main")
	res, err = p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.True(t, res.Resumed)
	require.NotNil(t, res.CatchUp)
	require.Equal(t, tip, res.CatchUp.To)
	require.Equal(t, StatusInProgress, res.Status)
	st := state(t, p, "add-wave")
	require.Equal(t, StatusInProgress, st.Status)
	require.True(t, st.Verify.Stale)
	require.Contains(t, st.Verify.StaleReason, "base moved: main")
	require.True(t, st.Verify.Green, "kept for the record")
	_, err = p.Review(ctx(), "add-wave", "pass", "development/changes/add-wave/change.md")
	require.Equal(t, CodeWrongStatus, failCode(t, err), "a stale verify is reviewed by no one")
}

// A rebase that conflicts: aborted, the change blocked naming the files,
// exit 3 — and nothing else written: no baseline, no base_sha, no catch-up
// record. A person who rebases by hand then starts it: ape re-baselines at
// the base tip without rebasing again.
func TestCatchUp_ConflictBlocks(t *testing.T) {
	p := verifiedWaveOnSuite(t)
	before := state(t, p, "add-wave")
	tip := moveMain(t, p, map[string]string{"wave.sh": "#!/bin/sh\necho another wave\n"})

	_, err := p.Verify(ctx(), "add-wave", RunOptions{})
	var ce *ConflictError
	require.ErrorAs(t, err, &ce)
	require.Equal(t, CodeRebaseConflict, ce.Code)
	require.Equal(t, []string{"wave.sh"}, ce.Conflicts)
	require.Equal(t, "change/add-wave", CurrentBranch(ctx(), p.Root))
	require.NoDirExists(t, filepath.Join(p.Root, ".git", "rebase-merge"), "the rebase was aborted")
	require.Equal(t, tip, run(t, p.Root, "rev-parse", "main"))
	st := state(t, p, "add-wave")
	require.Equal(t, StatusBlocked, st.Status)
	require.Contains(t, *st.BlockedReason, "verify: main moved, and rebasing change/add-wave onto it conflicts in wave.sh")
	require.Contains(t, *st.BlockedReason, "a person rebases it")
	require.Equal(t, before.BaseSHA, st.BaseSHA)
	require.Equal(t, before.Baseline, st.Baseline)
	require.Empty(t, st.Rebases)
	require.Equal(t, "apex: add-wave blocked", subjects(t, p)[0])
	require.Equal(t, 1, worktrees(t, p))
	clean(t, p, "a rebase conflict")

	// A person resolves it by hand (the change's side wins).
	run(t, p.Root, "rebase", "-q", "-X", "theirs", "main")
	res, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.NotNil(t, res.CatchUp)
	require.False(t, res.CatchUp.Rebased, "the branch already holds main's commits")
	require.Equal(t, StatusInProgress, res.Status)
	st = state(t, p, "add-wave")
	require.Equal(t, tip, st.BaseSHA)
	require.Len(t, st.Rebases, 1)
	require.False(t, st.Rebases[0].Rebased)
	for _, b := range st.Baseline {
		if b.Task == "T1" {
			require.Equal(t, 0, b.Exit, "main's own wave.sh passes T1 at the base tip")
		}
	}
	v, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.True(t, v.Green)
	require.NotEmpty(t, v.GreenAtBaseline, "judged against the new baseline: T1 was green at the base tip")
}

// A catch-up needs a clean tree: anything uncommitted outside the change's
// own folder refuses before git is touched.
func TestCatchUp_NeedsACleanTree(t *testing.T) {
	p := verifiedWaveOnSuite(t)
	moveMain(t, p, map[string]string{"hug.sh": "#!/bin/sh\necho hug\n"})
	write(t, p, "scratch.txt", "x\n")
	head := HeadSHA(ctx(), p.Root)
	_, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.Equal(t, "tree.dirty", failCode(t, err))
	require.ErrorContains(t, err, "main moved ahead of change/add-wave, and ape rebases the change onto it before verify")
	require.ErrorContains(t, err, "scratch.txt")
	require.Equal(t, head, HeadSHA(ctx(), p.Root))
	require.NoError(t, os.Remove(filepath.Join(p.Root, "scratch.txt")))

	// The change's own folder is committed on its branch first.
	write(t, p, "development/changes/add-wave/review.md", "notes\n")
	v, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.NotNil(t, v.CatchUp)
	require.Contains(t, subjects(t, p), "apex: add-wave notes")
}
