package apexv1

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// FINDING-V15-1B-3 (B4, D756): the boundary take-in is ape's, not the
// loop's choice. In 1b M9's loop took main in at its archive boundary and
// M10's never did. A coordinated archive now takes main in once it landed,
// when main moved: the T1, main moved, so milestone/M1 ends on take-in-1, the
// merge of the archive's commit and main's tip, landed on the remote. The
// control, main not moved: the archive's commit is the tip, nothing made.
func TestCoordinatedArchive_TakesMainInWhenItMoved(t *testing.T) {
	t.Run("main-moved", func(t *testing.T) {
		p, bare := coordinatedReviewedWave(t)
		useIntegrator(t, &fakeIntegrator{})
		mainTip := pushFile(t, bare, "main", "notes.txt", "from M0\n")
		res, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
		require.NoError(t, err)
		require.NotNil(t, res.TakeIn, "the archive took main in")
		require.Equal(t, "take-in-1", res.TakeIn.ID)
		tip := run(t, bare, "rev-parse", "milestone/M1")
		require.Equal(t, res.TakeIn.Commit, tip, "landed on the remote")
		parents := strings.Fields(run(t, bare, "rev-list", "--parents", "-n", "1", "milestone/M1"))
		require.Equal(t, []string{tip, res.Commit, mainTip}, parents, "the archive's commit merged with main, never rebased")
		require.Equal(t, "milestone/M1", CurrentBranch(ctx(), p.Root))
		require.Nil(t, p.LoadArchiveJournal())
	})
	t.Run("control-main-not-moved", func(t *testing.T) {
		p, bare := coordinatedReviewedWave(t)
		useIntegrator(t, &fakeIntegrator{})
		res, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
		require.NoError(t, err)
		require.Equal(t, res.Commit, run(t, bare, "rev-parse", "milestone/M1"), "the archive's commit is the tip")
		if res.TakeIn != nil {
			require.True(t, res.TakeIn.UpToDate)
			require.Empty(t, res.TakeIn.ID, "nothing made")
		}
	})
}

// A take-in that conflicts after the archive landed is reported as today,
// change.conflicted with take-in-<n> (the loop's "apex-apply take-in-<n>,
// then the same command again"), and the archive stands: it landed, and no
// journal is left that a later command would roll it back by. Archive again
// lands apex-apply's resolution. The control: an uncoordinated archive of an
// archived change is refused (change.archived), as before.
func TestCoordinatedArchive_TakeInConflictThenArchiveAgain(t *testing.T) {
	p, bare := coordinatedReviewedWave(t)
	useIntegrator(t, &fakeIntegrator{})
	pushFile(t, bare, "main", "wave.sh", "#!/bin/sh\necho tide\n")

	res, err := p.Archive(ctx(), "add-wave", false, RunOptions{})
	var ce *ConflictError
	require.ErrorAs(t, err, &ce)
	require.Equal(t, CodeConflicted, ce.Code)
	require.Equal(t, "take-in-1", ce.Change)
	require.Equal(t, "take-in", ce.Kind)
	require.NotNil(t, res)
	require.Equal(t, res.Commit, run(t, bare, "rev-parse", "milestone/M1"), "the archive landed")
	require.NoDirExists(t, p.ChangeDir("add-wave"))
	require.Nil(t, p.LoadArchiveJournal(), "no journal to roll the landed archive back by")
	require.Equal(t, "change/take-in-1", CurrentBranch(ctx(), p.Root))

	write(t, p, "wave.sh", "#!/bin/sh\necho wave and tide\n")
	run(t, p.Root, "add", "wave.sh")
	run(t, p.Root, "commit", "-q", "--no-edit")
	resolved := run(t, p.Root, "rev-parse", "HEAD")
	res, err = p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err, "archive again lands the take-in")
	require.NotNil(t, res.TakeIn)
	require.Equal(t, "take-in-1", res.TakeIn.ID)
	require.Equal(t, resolved, res.TakeIn.Commit)
	require.Equal(t, resolved, run(t, bare, "rev-parse", "milestone/M1"))
	require.Equal(t, "milestone/M1", CurrentBranch(ctx(), p.Root))

	res, err = p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err, "and once more: up to date")
	require.True(t, res.TakeIn.UpToDate)

	u := reviewedWave(t)
	_, err = u.Accept(ctx(), "add-wave", "T2", "ann")
	require.NoError(t, err)
	_, err = u.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)
	_, err = u.Archive(ctx(), "add-wave", false, RunOptions{})
	require.Equal(t, "change.archived", failCode(t, err), "control: uncoordinated, archived is archived")
}
