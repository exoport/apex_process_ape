package change

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	for _, args := range [][]string{
		{"init", "--initial-branch=main"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "T"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		require.NoError(t, cmd.Run(), "git %v", args)
	}
	return root
}

func git(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
}

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

// The three properties the invocation is chosen for, checked against
// real git rather than against the flags: the files inside a new
// directory are listed one by one (not collapsed to `dir/`), a path with
// a space comes back unquoted, and both sides of a rename are reported.
func TestChanged_ListsFilesInNewDirsUnquotedWithBothSidesOfARename(t *testing.T) {
	root := gitRepo(t)
	write(t, root, "kept.txt", "one\n")
	write(t, root, "moved.txt", "two\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-m", "base")

	// A new directory holding two files, one of them with a space in its
	// name. The default `git status --porcelain` would report this as a
	// single `evidence/` entry, and the quoting default would wrap the
	// second name in quotes.
	write(t, root, "evidence/gate.txt", "pass\n")
	write(t, root, "evidence/a name.txt", "pass\n")
	git(t, root, "mv", "moved.txt", "renamed.txt")
	write(t, root, "kept.txt", "one\nedited\n")

	changed, err := Changed(context.Background(), root)
	require.NoError(t, err)
	require.Equal(t, []string{
		"evidence/a name.txt",
		"evidence/gate.txt",
		"kept.txt",
		"moved.txt",
		"renamed.txt",
	}, changed)
}

func TestChanged_CleanTreeIsEmpty(t *testing.T) {
	root := gitRepo(t)
	write(t, root, "a.txt", "a\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-m", "base")

	changed, err := Changed(context.Background(), root)
	require.NoError(t, err)
	require.Empty(t, changed)
}

// The rename's second field is the ORIGINAL path, not another record.
// Read as a record it would contribute a two-character status line as a
// path, and the reconciliation would then refuse over a path that is not
// a path.
func TestParseStatusZ_RenameSecondFieldIsNotARecord(t *testing.T) {
	out := []byte("R  new.txt\x00old.txt\x00 M kept.txt\x00")
	require.Equal(t, []string{"kept.txt", "new.txt", "old.txt"}, parseStatusZ(out))
}

func TestDetachedHEAD(t *testing.T) {
	root := gitRepo(t)
	write(t, root, "a.txt", "a\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-m", "base")

	ctx := context.Background()
	detached, err := DetachedHEAD(ctx, root)
	require.NoError(t, err)
	require.False(t, detached)

	// An unborn HEAD is on a branch that does not exist yet, not a
	// detached one: the first commit creates the branch.
	fresh := gitRepo(t)
	detached, err = DetachedHEAD(ctx, fresh)
	require.NoError(t, err)
	require.False(t, detached, "a repository with no commits is not detached")

	git(t, root, "checkout", "--detach", "HEAD")
	detached, err = DetachedHEAD(ctx, root)
	require.NoError(t, err)
	require.True(t, detached)
}
