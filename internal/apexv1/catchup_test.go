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

// H8 (D47 Q3): a rebase that conflicts leaves the conflict on the change's
// branch for apex-apply. The old tip is kept under refs/apex/pre-rebase,
// change/<id> is recreated at the base tip with HEAD attached, the markers
// and the unmerged path are in the tree, the baseline is the base tip's,
// and the state says in-progress with the conflict recorded. verify
// refuses until the markers are resolved and committed; then verify is
// green, the review runs again, and archive lands ONE squash on main.
func TestCatchUp_ConflictLeftOnTheBranch(t *testing.T) {
	p := verifiedWaveOnSuite(t)
	before := state(t, p, "add-wave")
	oldTip := HeadSHA(ctx(), p.Root)
	tip := moveMain(t, p, map[string]string{"wave.sh": "#!/bin/sh\necho another wave\n"})

	_, err := p.Verify(ctx(), "add-wave", RunOptions{})
	var ce *ConflictError
	require.ErrorAs(t, err, &ce)
	require.Equal(t, CodeConflicted, ce.Code)
	require.Equal(t, []string{"wave.sh"}, ce.Conflicts)
	require.Equal(t, ActionApply, ActionFor(ce.Code))

	// HEAD on the branch, recreated at main's tip plus ape's state commit.
	require.Equal(t, "change/add-wave", CurrentBranch(ctx(), p.Root), "HEAD is attached")
	require.Equal(t, tip, run(t, p.Root, "rev-parse", "HEAD~1"))
	require.Equal(t, "apex: add-wave conflicted rebasing onto main", subjects(t, p)[0])
	require.Equal(t, oldTip, run(t, p.Root, "rev-parse", "refs/apex/pre-rebase/add-wave/1"))
	require.NoDirExists(t, filepath.Join(p.Root, ".git", "rebase-merge"))
	require.NoFileExists(t, filepath.Join(p.Root, ".git", "CHERRY_PICK_HEAD"))
	require.NoDirExists(t, filepath.Join(p.Root, ".git", "sequencer"))
	require.Equal(t, []string{"wave.sh"}, p.unmergedPaths(ctx()))
	require.Contains(t, read(t, p, "wave.sh"), "<<<<<<< ")
	require.Equal(t, 1, worktrees(t, p))

	st := state(t, p, "add-wave")
	require.Equal(t, StatusInProgress, st.Status)
	require.Equal(t, tip, st.BaseSHA)
	require.NotEqual(t, before.Baseline, st.Baseline, "re-baselined at the base tip")
	require.True(t, st.Verify.Stale)
	require.Len(t, st.Rebases, 1)
	require.Equal(t, "refs/apex/pre-rebase/add-wave/1", st.Rebases[0].OldTipRef)
	require.Equal(t, []string{"wave.sh"}, st.Rebases[0].Conflicts)

	// verify refuses while unmerged, and while markers remain once added.
	_, err = p.Verify(ctx(), "add-wave", RunOptions{})
	require.Equal(t, CodeUnresolvedConflict, failCode(t, err))
	require.ErrorContains(t, err, "wave.sh (unmerged)")
	run(t, p.Root, "add", "wave.sh")
	_, err = p.Verify(ctx(), "add-wave", RunOptions{})
	require.Equal(t, CodeUnresolvedConflict, failCode(t, err))
	require.ErrorContains(t, err, "wave.sh (conflict markers)")

	// apex-apply resolves (the change's side), adds and commits.
	write(t, p, "wave.sh", "#!/bin/sh\necho wave\n")
	run(t, p.Root, "add", "-A")
	run(t, p.Root, "commit", "-q", "-m", "fix: resolve wave.sh\n\nChange: add-wave")
	clean(t, p, "the resolution")
	v, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.True(t, v.Green)
	require.Nil(t, v.CatchUp, "already on the base tip")
	require.Equal(t, StatusVerified, v.Status)
	write(t, p, "development/changes/add-wave/review.md", "fine\n")
	_, err = p.Review(ctx(), "add-wave", "pass", "development/changes/add-wave/review.md")
	require.NoError(t, err)
	_, err = p.Accept(ctx(), "add-wave", "T2", "ann")
	require.NoError(t, err)
	res, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)
	require.Equal(t, "main", CurrentBranch(ctx(), p.Root))
	require.Equal(t, tip, run(t, p.Root, "rev-parse", "HEAD~1"), "one squash on main")
	require.Equal(t, res.Commit, HeadSHA(ctx(), p.Root))
	require.Equal(t, "#!/bin/sh\necho wave\n", read(t, p, "wave.sh"))
	clean(t, p, "the archive")
}

// A rebase that conflicts commit by commit while the branch's net diff
// applies cleanly (a change made and reverted on the branch): the net diff
// is committed as the rebased code, and the catch-up ends as a clean one.
func TestCatchUp_NetDiffAppliesCleanly(t *testing.T) {
	p := verifiedWaveOnSuite(t)
	write(t, p, "greet.sh", "#!/bin/sh\necho Howdy $1\n")
	commitAll(t, p, "try: howdy")
	write(t, p, "greet.sh", "#!/bin/sh\necho Hello $1\n")
	commitAll(t, p, "revert: hello again")
	tip := moveMain(t, p, map[string]string{"greet.sh": "#!/bin/sh\necho Hello $1 !\n"})

	v, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.True(t, v.Green)
	require.NotNil(t, v.CatchUp)
	require.Empty(t, v.CatchUp.Conflicts)
	require.Equal(t, "refs/apex/pre-rebase/add-wave/1", v.CatchUp.OldTipRef)
	subj := subjects(t, p)
	require.Equal(t, []string{"apex: add-wave verified", "apex: add-wave rebased onto main", "apex: add-wave re-applied onto main as one commit", "apex: add-wave conflicted rebasing onto main"}, subj[:4])
	require.True(t, p.isAncestor(ctx(), tip, "HEAD"))
	require.Equal(t, "#!/bin/sh\necho Hello $1 !\n", read(t, p, "greet.sh"), "main's side kept")
	require.Equal(t, "#!/bin/sh\necho wave\n", read(t, p, "wave.sh"), "the change's code re-applied")
	st := state(t, p, "add-wave")
	require.Empty(t, st.Rebases[0].Conflicts)
	clean(t, p, "a clean net diff")
}

// A second conflicted catch-up keeps its own old tip: n counts up.
func TestKeepPreRebaseTip_Numbers(t *testing.T) {
	p := fixture(t, baseFiles())
	head := HeadSHA(ctx(), p.Root)
	ref, err := p.keepPreRebaseTip(ctx(), "add-wave", head)
	require.NoError(t, err)
	require.Equal(t, "refs/apex/pre-rebase/add-wave/1", ref)
	ref, err = p.keepPreRebaseTip(ctx(), "add-wave", head)
	require.NoError(t, err)
	require.Equal(t, "refs/apex/pre-rebase/add-wave/2", ref)
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

// The baseline at the base tip runs in the project's own checkout, so the
// project's ignored files — here deps/, which the suite needs, as a real
// suite needs node_modules or a build cache — are there. In a fresh
// worktree the suite would be red at the new baseline, and every later
// red would pass as pre-existing.
func TestCatchUp_BaselineSeesIgnoredFiles(t *testing.T) {
	files := suiteFiles()
	files["suite.sh"] = "#!/bin/sh\n[ -f deps/ready ]\n"
	files[".gitignore"] = "deps/\n"
	p := fixture(t, files)
	write(t, p, "deps/ready", "x\n")
	addChange(t, p, "add-wave", waveChange("add-wave"))
	commitAll(t, p, "plan: add-wave")
	_, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, 0, state(t, p, "add-wave").Regression.Exit)
	write(t, p, "wave.sh", "#!/bin/sh\necho wave\n")
	commitAll(t, p, "feat: wave")
	tip := moveMain(t, p, map[string]string{"hug.sh": "#!/bin/sh\necho hug\n"})

	v, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.NotNil(t, v.CatchUp)
	st := state(t, p, "add-wave")
	require.Equal(t, tip, st.Regression.SHA)
	require.Equal(t, 0, st.Regression.Exit, "the ignored deps/ is present at the new baseline")
	require.Equal(t, "change/add-wave", CurrentBranch(ctx(), p.Root), "back on the change branch")
	require.FileExists(t, filepath.Join(p.Root, "deps", "ready"))
	require.Equal(t, 1, worktrees(t, p), "no extra worktree")

	// A suite the base itself breaks is still caught as pre-existing, and
	// one the change breaks after the catch-up is newly red.
	write(t, p, "suite.sh", "#!/bin/sh\nexit 1\n")
	commitAll(t, p, "break the suite")
	v, err = p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.False(t, v.Green, "red against a green baseline: newly red")
}
