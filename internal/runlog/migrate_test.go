package runlog

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// legacyRun materialises a run at the PRE-move path.
func legacyRun(t *testing.T, root, kindDir, group, runID string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(root, "_output", kindDir, group, runID)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for name, body := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
}

func TestMigrate_MovesLegacyRunsUnderApe(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	legacyRun(t, root, "pipelines", "design", "run1", map[string]string{
		"manifest.yaml":     "pipeline: design\n",
		"hook-events.jsonl": "{}\n",
	})
	legacyRun(t, root, "tasks", "apex-foo", "run2", map[string]string{"manifest.yaml": "skill: apex-foo\n"})

	res, err := Migrate(root)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"pipeline/design/run1", "task/apex-foo/run2"}, res.Moved)
	require.Empty(t, res.Conflicts)

	// Contents arrive intact at the new path...
	body, err := os.ReadFile(filepath.Join(PipelineRunDir(root, "design", "run1"), "manifest.yaml"))
	require.NoError(t, err)
	require.Equal(t, "pipeline: design\n", string(body))
	require.FileExists(t, filepath.Join(PipelineRunDir(root, "design", "run1"), "hook-events.jsonl"))
	require.FileExists(t, filepath.Join(TaskRunDir(root, "apex-foo", "run2"), "manifest.yaml"))

	// ...and the emptied legacy trees are gone rather than left as husks.
	require.NoDirExists(t, filepath.Join(root, "_output", "pipelines"))
	require.NoDirExists(t, filepath.Join(root, "_output", "tasks"))
}

// Running it twice must not move anything the second time, and must not
// error. `ape framework update` calls it on every invocation.
func TestMigrate_IsIdempotent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	legacyRun(t, root, "pipelines", "design", "run1", map[string]string{"manifest.yaml": "x\n"})

	first, err := Migrate(root)
	require.NoError(t, err)
	require.Len(t, first.Moved, 1)

	second, err := Migrate(root)
	require.NoError(t, err)
	require.Empty(t, second.Moved)
	require.Empty(t, second.Conflicts)
	require.FileExists(t, filepath.Join(PipelineRunDir(root, "design", "run1"), "manifest.yaml"))
}

// A project that never ran ape, or one already on the new layout, has
// nothing to do and must not fail.
func TestMigrate_NothingToDo(t *testing.T) {
	t.Parallel()
	res, err := Migrate(t.TempDir())
	require.NoError(t, err)
	require.Empty(t, res.Moved)
	require.Empty(t, res.Conflicts)
}

// The destination already existing is the one case where guessing would
// destroy history. Both copies survive and the collision is reported.
func TestMigrate_ConflictLeavesBothIntact(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	legacyRun(t, root, "pipelines", "design", "run1", map[string]string{"manifest.yaml": "OLD\n"})
	dst := PipelineRunDir(root, "design", "run1")
	require.NoError(t, os.MkdirAll(dst, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dst, "manifest.yaml"), []byte("NEW\n"), 0o600))

	res, err := Migrate(root)
	require.NoError(t, err)
	require.Empty(t, res.Moved)
	require.Equal(t, []string{"pipeline/design/run1"}, res.Conflicts)

	// The destination is untouched...
	got, err := os.ReadFile(filepath.Join(dst, "manifest.yaml"))
	require.NoError(t, err)
	require.Equal(t, "NEW\n", string(got))
	// ...and the source is still there to be resolved by hand.
	src, err := os.ReadFile(filepath.Join(root, "_output", "pipelines", "design", "run1", "manifest.yaml"))
	require.NoError(t, err)
	require.Equal(t, "OLD\n", string(src))
}

// A conflict must not take the whole migration down with it: every other
// run still moves.
func TestMigrate_ConflictDoesNotBlockOtherRuns(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	legacyRun(t, root, "pipelines", "design", "run1", map[string]string{"manifest.yaml": "OLD\n"})
	legacyRun(t, root, "pipelines", "design", "run2", map[string]string{"manifest.yaml": "fine\n"})
	require.NoError(t, os.MkdirAll(PipelineRunDir(root, "design", "run1"), 0o755))

	res, err := Migrate(root)
	require.NoError(t, err)
	require.Equal(t, []string{"pipeline/design/run2"}, res.Moved)
	require.Equal(t, []string{"pipeline/design/run1"}, res.Conflicts)
	require.FileExists(t, filepath.Join(PipelineRunDir(root, "design", "run2"), "manifest.yaml"))
	// The group survives because it still holds the unresolved run.
	require.DirExists(t, filepath.Join(root, "_output", "pipelines", "design", "run1"))
}

// `latest` is a symlink into a sibling run dir. It has to arrive pointing
// at the same relative target, not at a path that no longer exists.
func TestMigrate_CarriesTheLatestSymlink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	legacyRun(t, root, "pipelines", "design", "run1", map[string]string{"manifest.yaml": "x\n"})
	link := filepath.Join(root, "_output", "pipelines", "design", "latest")
	require.NoError(t, os.Symlink("run1", link))

	res, err := Migrate(root)
	require.NoError(t, err)
	require.Equal(t, []string{"pipeline/design/run1"}, res.Moved)

	newLink := filepath.Join(PipelinesRoot(root), "design", "latest")
	target, err := os.Readlink(newLink)
	require.NoError(t, err)
	require.Equal(t, "run1", target)
	// And it resolves — the run it names really is beside it now.
	require.FileExists(t, filepath.Join(filepath.Dir(newLink), target, "manifest.yaml"))
}

// frameworkOutputDirs is what the FRAMEWORK writes under the output folder,
// per its own answer on the layout split. None of them is ape's, and ape
// must leave every one untouched — the ownership rule this whole layout
// change exists to establish.
var frameworkOutputDirs = []string{
	"handoffs",
	"governance",
	"functionality",
	"planning",
	"implementation",
	"framework-requests",
	"verify-orchestrator",
}

// The framework owns the output folder. Its directories sit beside ape's
// trees and must be left exactly where they are — every one of them, not
// just the one that was convenient to write a test for.
func TestMigrate_LeavesFrameworkOutputAlone(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	legacyRun(t, root, "pipelines", "design", "run1", map[string]string{"manifest.yaml": "x\n"})

	for _, dir := range frameworkOutputDirs {
		f := filepath.Join(root, "_output", dir, "record.md")
		require.NoError(t, os.MkdirAll(filepath.Dir(f), 0o755))
		require.NoError(t, os.WriteFile(f, []byte("framework-owned: "+dir+"\n"), 0o600))
	}

	_, err := Migrate(root)
	require.NoError(t, err)

	for _, dir := range frameworkOutputDirs {
		body, err := os.ReadFile(filepath.Join(root, "_output", dir, "record.md"))
		require.NoError(t, err, "%s must survive the migration", dir)
		require.Equal(t, "framework-owned: "+dir+"\n", string(body))
	}
	// ape's own legacy tree still went where it belongs.
	require.FileExists(t, filepath.Join(PipelineRunDir(root, "design", "run1"), "manifest.yaml"))
}

func TestPending(t *testing.T) {
	t.Parallel()
	t.Run("false on a clean project", func(t *testing.T) {
		t.Parallel()
		require.False(t, Pending(t.TempDir()))
	})
	t.Run("false once migrated", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		legacyRun(t, root, "tasks", "apex-foo", "run1", map[string]string{"manifest.yaml": "x\n"})
		require.True(t, Pending(root))
		_, err := Migrate(root)
		require.NoError(t, err)
		require.False(t, Pending(root))
	})
	t.Run("true while a legacy run remains", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		legacyRun(t, root, "pipelines", "design", "run1", map[string]string{"manifest.yaml": "x\n"})
		require.True(t, Pending(root))
	})
	// An empty legacy directory is not pending work — there is nothing in
	// it to move, and reporting it would nag forever on a project whose
	// runs were deleted by hand.
	t.Run("false for an empty legacy tree", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, "_output", "pipelines", "design"), 0o755))
		require.False(t, Pending(root))
	})
}
