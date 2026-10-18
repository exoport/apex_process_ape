package apexv1

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// D155 Q77: init writes hot/.gitignore `*`, commits nothing of it (its own
// `*` ignores it), and keeps an existing one.
func TestInit_HotGitignore(t *testing.T) {
	needSh(t)
	root := t.TempDir()
	gitEnv(t)
	run(t, root, "init", "-q")
	res, err := Init(ctx(), root, false)
	require.NoError(t, err)
	p, err := Open(root)
	require.NoError(t, err)
	require.Contains(t, res.Created, "hot/.gitignore")
	require.Equal(t, "*\n", read(t, p, "hot/.gitignore"))
	require.Equal(t, "apex: project init", subjects(t, p)[0])
	require.NotContains(t, run(t, root, "show", "--name-only", "--format=", "HEAD"), "hot/")
	require.Empty(t, run(t, root, "ls-files", "hot/"))
	clean(t, p, "project init")
	require.NotContains(t, findingMessages(p.Check()), "hot/")

	// An existing one is the project's: kept as it is.
	write(t, p, "hot/.gitignore", "*\n!README\n")
	res, err = Init(ctx(), root, false)
	require.NoError(t, err)
	require.Contains(t, res.Kept, "hot/.gitignore")
	require.Equal(t, "*\n!README\n", read(t, p, "hot/.gitignore"))
}

// D155 Q77: the lift lays hot/.gitignore down with the skeleton, outside
// the lift commit.
func TestLift_HotGitignore(t *testing.T) {
	root := liftFixture(t, true)
	res, err := Lift(ctx(), root, LiftOptions{Exclude: []string{"schema/**"}})
	require.NoError(t, err)
	p, err := Open(root)
	require.NoError(t, err)
	require.Contains(t, res.Init.Created, "hot/.gitignore")
	require.Equal(t, "*\n", read(t, p, "hot/.gitignore"))
	require.NotContains(t, run(t, root, "show", "--name-only", "--format=", "HEAD"), "hot/")
	require.Empty(t, run(t, root, "status", "--porcelain"))
	require.NotContains(t, findingMessages(p.Check()), "hot/")
}

// D155 Q77: hot.not-ignored for a hot/ git tracks a file in or does not
// ignore; none without hot/.
func TestCheck_HotNotIgnored(t *testing.T) {
	p := fixture(t, baseFiles())
	require.Empty(t, p.hotFindings(ctx()), "no hot/: no finding")

	// A hot/ with no .gitignore: a secret there would be committed.
	write(t, p, "hot/token", "s3cret\n")
	fs := p.hotFindings(ctx())
	require.Len(t, fs, 1)
	require.Equal(t, "hot/", fs[0].File)
	require.Contains(t, fs[0].Message, "a file in it is not ignored")
	require.Equal(t, CodeHotNotIgnored, ClassifyFindings(fs)[0].Code)
	require.Contains(t, findingMessages(p.Check()), "hot/: hot/ is not ignored by git")

	// Ignored: clean.
	write(t, p, "hot/.gitignore", HotGitignore)
	require.Empty(t, p.hotFindings(ctx()))

	// Tracked, ignored or not (git add -f): a finding naming the files.
	run(t, p.Root, "add", "-f", "hot/token")
	run(t, p.Root, "commit", "-q", "-m", "oops")
	fs = p.hotFindings(ctx())
	require.Len(t, fs, 1)
	require.Contains(t, fs[0].Message, "git tracks hot/token")
	require.Contains(t, fs[0].Message, "`git rm -r --cached hot/`")
	require.NotContains(t, fs[0].Message, "a file in it is not ignored")

	// Ignored by the root .gitignore instead: as good.
	run(t, p.Root, "rm", "-q", "--cached", "hot/token")
	require.NoError(t, os.Remove(filepath.Join(p.Root, "hot", ".gitignore")))
	write(t, p, ".gitignore", "hot/\n")
	require.Empty(t, p.hotFindings(ctx()))
}
