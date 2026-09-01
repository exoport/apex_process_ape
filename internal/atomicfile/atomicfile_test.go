package atomicfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWrite_CreatesAndOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.md")

	require.NoError(t, Write(path, []byte("first\n")))
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "first\n", string(body))

	require.NoError(t, Write(path, []byte("second\n")))
	body, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "second\n", string(body), "an overwrite replaces the whole file, not part of it")
}

// TestWrite_LeavesNoTempFileBehind is the one a reader of the directory
// notices. The temp file is created beside the target so the rename stays
// on one filesystem, which means a leak lands in the corpus itself — a
// `.ape-adr-0001.md.123` sitting in a governance folder reads as a record.
func TestWrite_LeavesNoTempFileBehind(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, Write(filepath.Join(dir, "doc.md"), []byte("x")))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "doc.md", entries[0].Name())
}

// TestWrite_UsesTheDeclaredMode: a record written 0600 is unreadable to
// every other user of a shared checkout, and the failure surfaces far from
// here.
func TestWrite_UsesTheDeclaredMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.md")
	require.NoError(t, Write(path, []byte("x")))

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, Mode, info.Mode().Perm())
}

// TestWrite_PreservesModeOnOverwrite guards the property os.CreateTemp
// makes easy to lose: the temp file is born 0600, so a rename without the
// chmod would silently tighten permissions on an existing document.
func TestWrite_PreservesModeOnOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.md")
	require.NoError(t, os.WriteFile(path, []byte("old"), 0o644))

	require.NoError(t, Write(path, []byte("new")))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, Mode, info.Mode().Perm())
}

func TestWrite_CreatesMissingParents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a", "b", "doc.md")

	require.NoError(t, Write(path, []byte("x")))
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "x", string(body))
}

// TestWrite_OriginalSurvivesAFailedWrite pins the two halves of a failed
// write: the original is intact, and the failure is returned rather than
// swallowed.
//
// An unwritable directory is the failure that can be provoked portably —
// not the interrupted-write case the package is really for, which a unit
// test cannot stage. It is worth knowing what it does and does not prove.
// os.WriteFile does NOT fail here (measured: opening an existing file for
// write needs the file's permission, not the directory's, so it succeeds
// and replaces the content); atomicfile fails because it must create a
// temp file first. So this is not a demonstration that the alternative
// would have corrupted anything. What it does assert is that when this
// package cannot complete, it has not already destroyed the target and it
// says so — which is the contract every caller here depends on to roll a
// story or a record back.
func TestWrite_OriginalSurvivesAFailedWrite(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.md")
	require.NoError(t, os.WriteFile(path, []byte("precious\n"), 0o644))

	require.NoError(t, os.Chmod(dir, 0o500)) // r-x: no new temp file
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := Write(path, []byte("replacement\n"))
	require.Error(t, err, "an unwritable directory must fail loudly, not silently")

	require.NoError(t, os.Chmod(dir, 0o700))
	body, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.Equal(t, "precious\n", string(body), "the original must survive a failed write intact")
}

// TestWrite_EmptyContentIsAValidDocument: writing zero bytes is a real
// outcome, and it must not be confused with the truncated file this
// package exists to prevent.
func TestWrite_EmptyContentIsAValidDocument(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.md")
	require.NoError(t, Write(path, nil))

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Empty(t, body)
}

func TestWrite_HandlesLargeContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.md")
	big := strings.Repeat("line of a very long record\n", 50_000)

	require.NoError(t, Write(path, []byte(big)))
	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Len(t, body, len(big))
}
