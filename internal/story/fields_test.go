package story

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// An absent implementation folder and an empty one are the SAME WORLD —
// no stories — and used to get two different answers: an error for the
// first, a well-formed empty projection for the second. Twelve framework
// skills call this, several legitimately before any story exists, and a
// skill may not work around a failing command.
func TestScanHeads_AnAbsentRootIsAnEmptyAnswerNotAnError(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "never-planned")

	scan, err := ScanHeads(absent)
	require.NoError(t, err, "a project that has not planned yet is not a failure")
	require.True(t, scan.RootMissing, "and the caller is told WHY it is empty")
	require.Empty(t, scan.Heads)

	// The empty folder, for comparison: same answer, and RootMissing is
	// what distinguishes the two.
	present := t.TempDir()
	scan, err = ScanHeads(present)
	require.NoError(t, err)
	require.False(t, scan.RootMissing)
	require.Empty(t, scan.Heads)
}

// The projection over an absent folder is well-formed, so a caller
// reading `stories_matched` gets a number rather than an exit code —
// and `implementation_folder_missing` keeps "nowhere to look" distinct
// from "looked and found nothing".
func TestProject_AbsentRootProjectsEmptyAndSaysSo(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "never-planned")

	res, err := Project(absent, []string{"story_id", "status"})
	require.NoError(t, err)
	require.Equal(t, 0, res.Trailer.FilesScanned)
	require.Equal(t, 0, res.Trailer.StoriesMatched)
	require.True(t, res.Trailer.RootMissing)
	require.Equal(t, map[string]int{"story_id": 0, "status": 0}, res.Trailer.PerFieldPresent,
		"every requested field still reports zero rather than being absent")
}

// An unreadable root is NOT the tolerated case: it says the answer
// cannot be trusted, where an absent root says the answer is empty.
func TestScanHeads_AnUnreadableRootIsStillAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory, so there is no unreadable case to make")
	}
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "locked"), 0o755))
	require.NoError(t, os.Chmod(filepath.Join(root, "locked"), 0o000))
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "locked"), 0o755) })

	_, err := ScanHeads(root)
	require.Error(t, err, "a directory ape cannot read is a failed answer, not an empty one")
}
