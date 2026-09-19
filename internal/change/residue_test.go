package change

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// What a halt actually leaves: a tracked file edited part way and a new
// file git has never seen. The patch carries the first, the byte copy
// carries the second, and neither covers the other.
func TestSave_CarriesTrackedChangesAndUntrackedFiles(t *testing.T) {
	root := gitRepo(t)
	write(t, root, "src/a.go", "package a\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-m", "base")

	write(t, root, "src/a.go", "package a\n\nfunc half() {}\n")
	write(t, root, "src/new file.go", "package a // never seen by git\n")

	dir := filepath.Join(t.TempDir(), "residue")
	res, err := Save(context.Background(), root, dir)
	require.NoError(t, err)

	require.Equal(t, []string{"src/a.go", "src/new file.go"}, res.Paths)
	require.True(t, res.Verified, "the patch passed `git apply --check -R`")
	require.False(t, res.Truncated)
	require.Empty(t, res.Note)

	patch, err := os.ReadFile(res.PatchPath)
	require.NoError(t, err)
	require.Contains(t, string(patch), "func half() {}")
	require.Contains(t, string(patch), "index ", "--full-index writes whole blob ids")

	// A path with a space, kept as its own path under untracked/.
	copied, err := os.ReadFile(filepath.Join(dir, "untracked", "src", "new file.go"))
	require.NoError(t, err)
	require.Equal(t, "package a // never seen by git\n", string(copied),
		"a listed name keeps no content, so the bytes are saved")
}

// Nothing left in the tree means nothing to save, and no empty patch
// file to make it look like there was something.
func TestSave_ACleanTreeLeavesNothing(t *testing.T) {
	root := gitRepo(t)
	write(t, root, "a.txt", "a\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-m", "base")

	dir := filepath.Join(t.TempDir(), "residue")
	res, err := Save(context.Background(), root, dir)
	require.NoError(t, err)
	require.Empty(t, res.Paths)
	require.Empty(t, res.PatchPath)
	require.NoDirExists(t, dir)
}

// The residue is saved after the work is done, and the run's own
// context is already dead when a SIGINT is what ended it. Capturing
// under the caller's cancelled context would take git down with it and
// lose the work at the moment it most needs saving.
func TestSave_SurvivesACancelledContext(t *testing.T) {
	root := gitRepo(t)
	write(t, root, "a.txt", "a\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-m", "base")
	write(t, root, "a.txt", "edited\n")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	dir := filepath.Join(t.TempDir(), "residue")
	res, err := Save(ctx, root, dir)
	require.NoError(t, err)
	require.Equal(t, []string{"a.txt"}, res.Paths)
	require.True(t, res.Verified)
}

// An untracked file that is ignored is not residue: it is the project's
// own housekeeping, and ape's run artifacts live in exactly such a
// folder.
func TestSave_IgnoredFilesAreNotResidue(t *testing.T) {
	root := gitRepo(t)
	write(t, root, ".gitignore", "_output/\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-m", "base")
	write(t, root, "_output/ape/changes/x/request.txt", "a request\n")

	dir := filepath.Join(t.TempDir(), "residue")
	res, err := Save(context.Background(), root, dir)
	require.NoError(t, err)
	require.Empty(t, res.Paths)
}
