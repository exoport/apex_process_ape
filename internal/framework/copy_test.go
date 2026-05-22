package framework_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/diegosz/apex_process_ape/internal/framework"
	"github.com/stretchr/testify/require"
)

// requirePOSIXMode skips tests that rely on POSIX file-mode bits
// surviving a write/read round-trip. Windows reports 0o666 for any
// user-readable file regardless of what Chmod was asked for, so the
// 0o600 assertion below would always fail there.
func requirePOSIXMode(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file-mode round-trip not applicable on Windows")
	}
}

func TestCopyFile_PreservesMode(t *testing.T) {
	requirePOSIXMode(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")
	require.NoError(t, os.WriteFile(src, []byte("hello"), 0o600))

	require.NoError(t, framework.CopyFile(src, dst))

	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	require.Equal(t, "hello", string(got))

	info, err := os.Stat(dst)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestCopyFile_OverwritesExistingDst(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")
	require.NoError(t, os.WriteFile(src, []byte("new"), 0o644))
	require.NoError(t, os.WriteFile(dst, []byte("old-and-longer"), 0o644))

	require.NoError(t, framework.CopyFile(src, dst))

	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	require.Equal(t, "new", string(got))
}

func TestCopyFile_RejectsNonRegular(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "subdir")
	require.NoError(t, os.Mkdir(src, 0o755))

	err := framework.CopyFile(src, filepath.Join(dir, "dst.txt"))
	require.ErrorContains(t, err, "not a regular file")
}

func TestCopyTree_RecursesAndCounts(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")

	// Build a tree: src/a.txt, src/sub/b.txt, src/sub/inner/c.txt
	require.NoError(t, os.MkdirAll(filepath.Join(src, "sub", "inner"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "a.txt"), []byte("A"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(src, "sub", "b.txt"), []byte("B"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(src, "sub", "inner", "c.txt"), []byte("C"), 0o644))

	count, err := framework.CopyTree(src, dst)
	require.NoError(t, err)
	require.Equal(t, 3, count)

	for _, rel := range []string{"a.txt", "sub/b.txt", "sub/inner/c.txt"} {
		_, err := os.Stat(filepath.Join(dst, rel))
		require.NoError(t, err, "missing copied file %s", rel)
	}
}

func TestCopyTree_ErrorsOnNonDirectorySrc(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "f.txt")
	require.NoError(t, os.WriteFile(src, []byte("x"), 0o644))

	_, err := framework.CopyTree(src, filepath.Join(dir, "dst"))
	require.ErrorContains(t, err, "not a directory")
}

func TestAtomicWriteFile_WritesAndCleansUpTemp(t *testing.T) {
	requirePOSIXMode(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "out.txt")

	require.NoError(t, framework.AtomicWriteFile(target, []byte("payload"), 0o600))

	got, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "payload", string(got))

	info, err := os.Stat(target)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// No leftover temp files.
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "out.txt", entries[0].Name())
}

func TestAtomicWriteFile_OverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.txt")
	require.NoError(t, os.WriteFile(target, []byte("old"), 0o644))

	require.NoError(t, framework.AtomicWriteFile(target, []byte("new"), 0o644))

	got, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "new", string(got))
}

func TestAtomicWriteFile_CreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "missing", "nested", "out.txt")

	require.NoError(t, framework.AtomicWriteFile(target, []byte("x"), 0o644))

	got, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "x", string(got))
}
