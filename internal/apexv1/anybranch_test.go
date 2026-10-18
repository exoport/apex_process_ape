package apexv1

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// fixOnMainOnly is add-wave started (HEAD on change/add-wave) and, planned
// on main after that branch was cut, the fix fix-hug: its records exist
// only on main.
func fixOnMainOnly(t *testing.T) *Project {
	t.Helper()
	p := startedWave(t)
	run(t, p.Root, "switch", "-q", "main")
	addFix(t, p, "fix-hug")
	run(t, p.Root, "switch", "-q", ChangeBranch("add-wave"))
	require.NoDirExists(t, p.ChangeDir("fix-hug"))
	return p
}

// onlyMainMoved asserts a command left this checkout as it was — HEAD,
// change/add-wave's tip, a clean tree, no fix folder written here — and
// that main got exactly one commit, subject, touching only paths under
// the prefixes given. It returns main's new tip.
func onlyMainMoved(t *testing.T, p *Project, mainBefore, subject string, prefixes ...string) string {
	t.Helper()
	require.Equal(t, ChangeBranch("add-wave"), CurrentBranch(ctx(), p.Root))
	require.Empty(t, run(t, p.Root, "status", "--porcelain"), "the working tree is untouched")
	require.NoDirExists(t, p.ChangeDir("fix-hug"), "nothing written in change/add-wave's tree")
	require.Equal(t, mainBefore, run(t, p.Root, "rev-parse", "main~1"), "main moved by one commit")
	require.Equal(t, subject, run(t, p.Root, "log", "-1", "--format=%s", "main"))
	for f := range strings.SplitSeq(run(t, p.Root, "show", "--name-only", "--format=", "main"), "\n") {
		require.True(t, slices.ContainsFunc(prefixes, func(pre string) bool { return strings.HasPrefix(f, pre) }), "main's commit touches %s", f)
	}
	return run(t, p.Root, "rev-parse", "main")
}

// D51 Q17: with HEAD on another change's branch, a fix whose records exist
// only on the base branch is listed by status and found, checked,
// approved, blocked, answered and abandoned there — each a commit on main,
// the working tree and HEAD of change/add-wave untouched.
func TestAnyBranch_FixOnTheBaseOnly(t *testing.T) {
	p := fixOnMainOnly(t)
	head := HeadSHA(ctx(), p.Root)
	mainTip := run(t, p.Root, "rev-parse", "main")
	changes := p.Changes()
	ids := make([]string, 0, len(changes))
	for _, c := range changes {
		ids = append(ids, c.ID)
	}
	require.Equal(t, []string{"add-wave", "fix-hug"}, ids, "status lists the base's fix")

	require.Empty(t, findingMessages(p.CheckChangeAnywhere(ctx(), "fix-hug")))
	pv, err := p.PreviewDeltaAnywhere(ctx(), "fix-hug")
	require.NoError(t, err)
	require.Len(t, pv.Specs, 1)
	require.Contains(t, pv.Specs[0].Diff, "+### Requirement: Hug")

	res, err := p.ApproveFix(ctx(), "fix-hug", "ann")
	require.NoError(t, err)
	mainTip = onlyMainMoved(t, p, mainTip, "apex: fix-hug approved", "development/changes/fix-hug/state.json")
	require.Equal(t, mainTip, res.Commit)
	require.Equal(t, []string{"fix-hug"}, p.Status().Fixes)

	_, err = p.Block(ctx(), "fix-hug", "needs a person")
	require.NoError(t, err)
	mainTip = onlyMainMoved(t, p, mainTip, "apex: fix-hug blocked", "development/changes/fix-hug/state.json")
	ci, err := p.requireChange(ctx(), "fix-hug")
	require.NoError(t, err)
	require.Equal(t, "main", ci.Branch)
	require.Equal(t, StatusBlocked, ci.State.Status)
	require.Equal(t, "needs a person", *ci.State.BlockedReason)
	require.Equal(t, 0, p.consecutiveBlocks(ctx(), "M1"), "a fix counts toward no milestone")

	_, err = p.AnswerChange(ctx(), "fix-hug", "hug gently", "ann")
	require.NoError(t, err)
	mainTip = onlyMainMoved(t, p, mainTip, "apex: fix-hug answered", "development/changes/fix-hug/state.json")

	ab, err := p.Abandon(ctx(), "fix-hug", "not needed")
	require.NoError(t, err)
	onlyMainMoved(t, p, mainTip, "apex: fix-hug abandoned", "development/changes/fix-hug/", "development/archive/")
	require.Equal(t, "development/archive/changes/"+Today()+"-fix-hug", ab.ArchivedTo)
	require.Empty(t, ab.BranchTip)
	require.Empty(t, p.filesAt(ctx(), "main", "development/changes/fix-hug"), "moved, not copied")
	st, err := parseState("", []byte(run(t, p.Root, "show", "main:"+ab.ArchivedTo+"/state.json")))
	require.NoError(t, err)
	require.Equal(t, StatusAbandoned, st.Status)
	require.Equal(t, "not needed", st.AbandonedReason)
	require.Equal(t, "hug gently", st.Answer.Text)
	require.Equal(t, fixDoc("fix-hug"), run(t, p.Root, "show", "main:"+ab.ArchivedTo+"/change.md")+"\n")
	require.Equal(t, head, HeadSHA(ctx(), p.Root))

	_, err = p.requireChange(ctx(), "fix-hug")
	require.Equal(t, "change.archived", failCode(t, err))
	require.NotContains(t, p.Status().Fixes, "fix-hug")
}

// D51 Q17: a fix this branch was cut with, approved on main since, is read
// as main has it — this branch's copy is stale.
func TestAnyBranch_StaleCopyOnTheChangeBranch(t *testing.T) {
	p := fixture(t, baseFiles())
	addChange(t, p, "add-wave", waveChange("add-wave"))
	addFix(t, p, "fix-hug")
	_, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.FileExists(t, p.StateFile("fix-hug"), "cut with the draft")
	run(t, p.Root, "switch", "-q", "main")
	_, err = p.ApproveFix(ctx(), "fix-hug", "ann")
	require.NoError(t, err)
	run(t, p.Root, "switch", "-q", ChangeBranch("add-wave"))

	require.Nil(t, state(t, p, "fix-hug").Approved, "this branch's copy")
	require.Equal(t, []string{"fix-hug"}, p.Status().Fixes, "status reads main's")
	res, err := p.ApproveFix(ctx(), "fix-hug", "bob")
	require.NoError(t, err)
	require.True(t, res.Already)
	require.Equal(t, "ann", res.Approved.By)
}

// D51 Q17: a started change's records are written on its branch from
// another branch, without a checkout: block from main.
func TestAnyBranch_BlockAStartedChangeFromTheBase(t *testing.T) {
	p := startedWave(t)
	run(t, p.Root, "switch", "-q", "main")
	mainTip := run(t, p.Root, "rev-parse", "main")
	branchTip := run(t, p.Root, "rev-parse", ChangeBranch("add-wave"))
	st, err := p.Block(ctx(), "add-wave", "parked")
	require.NoError(t, err)
	require.Equal(t, StatusBlocked, st.Status)
	require.Equal(t, "main", CurrentBranch(ctx(), p.Root))
	clean(t, p, "block from main")
	require.Equal(t, branchTip, run(t, p.Root, "rev-parse", ChangeBranch("add-wave")+"~1"))
	require.Equal(t, "apex: add-wave blocked", run(t, p.Root, "log", "-1", "--format=%s", ChangeBranch("add-wave")))
	require.Equal(t, mainTip, run(t, p.Root, "rev-parse", "main~1"), "the milestone's count, on main")
	require.Equal(t, "apex: M1 consecutive blocks 1 (add-wave)", run(t, p.Root, "log", "-1", "--format=%s", "main"))
	require.Equal(t, StatusDraft, state(t, p, "add-wave").Status, "main's copy is the draft it was started from")
	ci, err := p.requireChange(ctx(), "add-wave")
	require.NoError(t, err)
	require.Equal(t, StatusBlocked, ci.State.Status)

	// A human-review task of an in-flight (not reviewed) change, accepted
	// from main, is recorded on its branch.
	_, err = p.Accept(ctx(), "add-wave", "T2", "ann")
	require.NoError(t, err)
	require.Equal(t, "apex: add-wave blocked", run(t, p.Root, "log", "-1", "--format=%s", ChangeBranch("add-wave")), "the status line names the status")
	ci, err = p.requireChange(ctx(), "add-wave")
	require.NoError(t, err)
	require.Equal(t, "T2", ci.State.Accepted[0].Task)
	require.Equal(t, "main", CurrentBranch(ctx(), p.Root))
	clean(t, p, "accept from main")
}

// D51 Q17: a started change is abandoned from another change's branch:
// its folder, as its own branch has it, archived on main; its branch
// deleted, the tip kept.
func TestAnyBranch_AbandonAStartedChangeFromAnotherChangeBranch(t *testing.T) {
	p := startedWave(t)
	write(t, p, "development/changes/add-wave/design.md", "# design\n")
	commitAll(t, p, "design")
	tip := HeadSHA(ctx(), p.Root)
	run(t, p.Root, "switch", "-q", "main")
	addFix(t, p, "fix-hug")
	_, err := p.ApproveFix(ctx(), "fix-hug", "ann")
	require.NoError(t, err)
	_, err = p.Start(ctx(), "fix-hug", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, ChangeBranch("fix-hug"), CurrentBranch(ctx(), p.Root))
	head := HeadSHA(ctx(), p.Root)
	mainTip := run(t, p.Root, "rev-parse", "main")

	res, err := p.Abandon(ctx(), "add-wave", "superseded")
	require.NoError(t, err)
	require.Equal(t, tip, res.BranchTip)
	require.Equal(t, head, HeadSHA(ctx(), p.Root))
	clean(t, p, "abandon from another change's branch")
	require.False(t, p.branchExists(ctx(), ChangeBranch("add-wave")))
	require.Equal(t, tip, run(t, p.Root, "rev-parse", abandonedRef("add-wave")))
	require.Equal(t, mainTip, run(t, p.Root, "rev-parse", "main~1"))
	require.Equal(t, "apex: add-wave abandoned", run(t, p.Root, "log", "-1", "--format=%s", "main"))
	require.Equal(t, "# design", run(t, p.Root, "show", "main:"+res.ArchivedTo+"/design.md"), "as its branch had it")
	require.Empty(t, p.filesAt(ctx(), "main", "development/changes/add-wave"))
	require.NoDirExists(t, filepath.Join(p.Root, res.ArchivedTo), "nothing written in change/fix-hug's tree")
}
