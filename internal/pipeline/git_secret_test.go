package pipeline

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// D155: the v0 pipeline's commit is scanned like every commit ape makes:
// a step's work that adds a secret is refused with commit.secret, nothing
// is committed, and the work stays staged.
func TestGitCommit_RefusesASecret(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available on PATH")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "ape-test")
	t.Setenv("GIT_AUTHOR_EMAIL", "ape-test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "ape-test")
	t.Setenv("GIT_COMMITTER_EMAIL", "ape-test@example.com")
	root := t.TempDir()
	git := func(args ...string) string {
		out, err := exec.CommandContext(context.Background(), "git", append([]string{"-C", root}, args...)...).CombinedOutput()
		require.NoError(t, err, string(out))
		return string(out)
	}
	git("init", "-q", "-b", "main")
	git("commit", "-q", "--allow-empty", "-m", "init")
	head := git("rev-parse", "HEAD")
	token := "gh" + "p_" + "aB3dE5fG7hI9jK1mN3pQ5rS7tU9vW1xY3zA5"
	require.NoError(t, os.WriteFile(filepath.Join(root, "notes.md"), []byte("token "+token+"\n"), 0o644))
	require.NoError(t, gitAddAll(context.Background(), root))

	_, err := gitCommit(context.Background(), root, "docs: step 1")
	require.ErrorContains(t, err, "git commit: commit.secret: nothing was committed: docs: step 1 adds a secret: notes.md:1 rule github-pat: ")
	require.NotContains(t, err.Error(), token)
	require.Equal(t, head, git("rev-parse", "HEAD"))
	require.Equal(t, "notes.md\n", git("diff", "--cached", "--name-only"))

	require.NoError(t, os.WriteFile(filepath.Join(root, "notes.md"), []byte("no token\n"), 0o644))
	require.NoError(t, gitAddAll(context.Background(), root))
	sha, err := gitCommit(context.Background(), root, "docs: step 1")
	require.NoError(t, err)
	require.NotEmpty(t, sha)
}
