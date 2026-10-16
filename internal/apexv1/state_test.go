package apexv1

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Backlog 26: an older ape kept its state in development/.apex/; this one
// reports the folder with the move to make, and reads nothing in it.
func TestProjectCheck_OldStateFolderIsAFinding(t *testing.T) {
	files := baseFiles()
	files["development/.apex/milestones.json"] = `{"M1": {"result": "green", "since": null}}` + "\n"
	p := fixture(t, files)
	fs := ClassifyFindings(p.Check())
	require.Len(t, fs, 1, findingMessages(fs))
	require.Equal(t, "layout.old-state", fs[0].Code)
	require.Equal(t, "development/.apex", fs[0].File)
	require.Equal(t, "move development/.apex/{milestones,decisions}.json to _apex/state/ and delete development/.apex/", fs[0].Fix)
	recs, err := p.LoadMilestoneRecords()
	require.NoError(t, err)
	require.Empty(t, recs, "the old milestones.json is not read")
}

// A project only an older ape touched is still recognised, so its
// commands run and project check can report the move.
func TestOldStateFolderStillMarksAV1Project(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "development", ".apex"), 0o755))
	sub := filepath.Join(root, "internal")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	require.Equal(t, root, FindRoot(sub))
	p, err := Open(root)
	require.NoError(t, err)
	require.True(t, p.IsV1())
}

// Ape's own records in _apex/state/ are not code: committing them (a limit
// stop, recorded on whatever branch the stopped session was on) does not
// make a verify or a review stale, and an uncommitted one is not
// "uncommitted code".
func TestStateRecordsAreNotCode(t *testing.T) {
	p := verifiedWave(t)
	verified := state(t, p, "add-wave").Verify.SHA
	require.NoError(t, p.RecordLimitStop(ctx(), "M1", LimitStop{Reason: StopBudget, At: Stamp(), Run: "r1", Skill: "apex-apply"}))
	require.Equal(t, "_apex/state/milestones.json", run(t, p.Root, "show", "--name-only", "--format=", "HEAD"))
	moved, files, err := p.codeMovedSince(ctx(), verified)
	require.NoError(t, err)
	require.False(t, moved, "%v", files)
	_, err = p.Review(ctx(), "add-wave", "pass", "development/changes/add-wave/review.md")
	require.NoError(t, err, "the verify is not stale")

	write(t, p, "_apex/state/milestones.json", "{}\n")
	write(t, p, "_apex/state/logs/scratch.log", "x\n")
	dirty, err := p.dirtyCode(ctx())
	require.NoError(t, err)
	require.Empty(t, dirty)
	require.NoDirExists(t, filepath.Join(p.Root, "development", ".apex"), "nothing writes the old place")
}
