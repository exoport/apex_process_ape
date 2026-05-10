package framework_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/diegosz/apex_process_ape/internal/framework"
	"github.com/stretchr/testify/require"
)

// initRepo creates a fresh git repo at dir with one initial commit on
// branch main, identity configured, and returns the head SHA.
func initRepo(t *testing.T, dir string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git not on PATH: %v", err)
	}
	ctx := context.Background()
	mustRun := func(args ...string) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	mustRun("init", "-b", "main")
	mustRun("config", "user.email", "test@example.invalid")
	mustRun("config", "user.name", "Test")
	mustRun("config", "commit.gpgsign", "false")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644))
	mustRun("add", ".")
	mustRun("commit", "-m", "init")
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	require.NoError(t, err)
	return string(out[:len(out)-1])
}

func TestIsGitRepo(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	require.False(t, framework.IsGitRepo(ctx, repo))
	initRepo(t, repo)
	require.True(t, framework.IsGitRepo(ctx, repo))
}

func TestCurrentBranch_Main(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	initRepo(t, repo)
	branch, err := framework.CurrentBranch(ctx, repo)
	require.NoError(t, err)
	require.Equal(t, "main", branch)
}

func TestHeadSHAAndExactTag(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	sha := initRepo(t, repo)

	got, err := framework.HeadSHA(ctx, repo)
	require.NoError(t, err)
	require.Equal(t, sha, got)

	tag, err := framework.ExactTag(ctx, repo)
	require.NoError(t, err)
	require.Empty(t, tag)

	cmd := exec.CommandContext(ctx, "git", "tag", "v0.0.1")
	cmd.Dir = repo
	require.NoError(t, cmd.Run())

	tag, err = framework.ExactTag(ctx, repo)
	require.NoError(t, err)
	require.Equal(t, "v0.0.1", tag)
}

func TestIsClean(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	initRepo(t, repo)

	clean, err := framework.IsClean(ctx, repo)
	require.NoError(t, err)
	require.True(t, clean)

	require.NoError(t, os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("x"), 0o644))

	clean, err = framework.IsClean(ctx, repo)
	require.NoError(t, err)
	require.False(t, clean)
}

func TestParsePorcelain(t *testing.T) {
	out := "?? new.txt\n M tracked.txt\nA  staged.txt"
	entries := framework.ParsePorcelain(out)
	require.Len(t, entries, 3)
	require.Equal(t, "??", entries[0].Status)
	require.Equal(t, "new.txt", entries[0].Path)
	require.True(t, entries[0].IsUntracked())
	require.Equal(t, " M", entries[1].Status)
	require.False(t, entries[1].IsUntracked())
	require.Equal(t, "A ", entries[2].Status)
}

func TestParsePorcelain_Empty(t *testing.T) {
	require.Nil(t, framework.ParsePorcelain(""))
	require.Nil(t, framework.ParsePorcelain("\n  \n"))
}

func TestSkillsPorcelain_DistinguishesUntrackedFromModified(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	initRepo(t, repo)

	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".claude", "skills", "apex-untracked"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".claude", "skills", "apex-untracked", "SKILL.md"), []byte("u"), 0o644))

	tracked := filepath.Join(repo, ".claude", "skills", "apex-tracked", "SKILL.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(tracked), 0o755))
	require.NoError(t, os.WriteFile(tracked, []byte("v1"), 0o644))
	cmd := exec.CommandContext(ctx, "git", "add", ".")
	cmd.Dir = repo
	require.NoError(t, cmd.Run())
	cmd = exec.CommandContext(ctx, "git", "commit", "-m", "add tracked")
	cmd.Dir = repo
	require.NoError(t, cmd.Run())
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".claude", "skills", "apex-fresh"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".claude", "skills", "apex-fresh", "SKILL.md"), []byte("u"), 0o644))
	require.NoError(t, os.WriteFile(tracked, []byte("v2"), 0o644))

	entries, err := framework.SkillsPorcelain(ctx, repo)
	require.NoError(t, err)

	var untracked, modified int
	for _, e := range entries {
		if e.IsUntracked() {
			untracked++
		} else {
			modified++
		}
	}
	require.GreaterOrEqual(t, untracked, 1, "expected at least one untracked apex-* path")
	require.GreaterOrEqual(t, modified, 1, "expected at least one modified apex-* path")
}
