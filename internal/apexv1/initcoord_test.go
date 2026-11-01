package apexv1

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// configuredRepo is a git repository on main whose config is cfg, committed.
func configuredRepo(t *testing.T, cfg string) string {
	t.Helper()
	needSh(t)
	root := t.TempDir()
	gitEnv(t)
	run(t, root, "init", "-q", "-b", "main")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "_apex", "config.yaml"), []byte(cfg), 0o644))
	run(t, root, "add", "-A")
	run(t, root, "commit", "-q", "-m", "base")
	return root
}

// FINDING-V15-1A-5: in a coordinated project main changes only through
// the merge queue, so `ape project init` on main commits on a branch of
// its own, stays on it and names the route; main does not move. The
// control: the same init in a project that is not coordinated commits on
// main, as before.
func TestInit_CoordinatedMainCommitsOnABranch(t *testing.T) {
	root := configuredRepo(t, coordinatedConfig)
	mainBefore := run(t, root, "rev-parse", "main")
	res, err := Init(ctx(), root, true)
	require.NoError(t, err)
	require.NotEmpty(t, res.Commit)
	require.Equal(t, "apex/project-init", res.Branch)
	require.Equal(t, "ape ghapp land apex/project-init", res.Land)
	require.Equal(t, mainBefore, run(t, root, "rev-parse", "main"), "main does not move")
	require.Equal(t, "apex/project-init", run(t, root, "rev-parse", "--abbrev-ref", "HEAD"), "init stays on its branch")
	require.Equal(t, res.Commit, run(t, root, "rev-parse", "apex/project-init"))
	p, err := Open(root)
	require.NoError(t, err)
	require.Equal(t, CoordinatedScope, p.ScopeLine())

	// A second init on main, the first branch still there: a new name.
	run(t, root, "switch", "-q", "main")
	res, err = Init(ctx(), root, true)
	require.NoError(t, err)
	require.Equal(t, "apex/project-init-2", res.Branch)
	require.Equal(t, mainBefore, run(t, root, "rev-parse", "main"))

	// On any other branch init commits where it is.
	run(t, root, "switch", "-q", "-c", "topic", "main")
	res, err = Init(ctx(), root, true)
	require.NoError(t, err)
	require.Empty(t, res.Branch)
	require.Equal(t, res.Commit, run(t, root, "rev-parse", "topic"))
}

func TestInit_NotCoordinatedCommitsOnMain(t *testing.T) {
	root := configuredRepo(t, "project_name: notes\n")
	res, err := Init(ctx(), root, true)
	require.NoError(t, err)
	require.Empty(t, res.Branch)
	require.Empty(t, res.Land)
	require.Equal(t, res.Commit, run(t, root, "rev-parse", "main"), "the commit is on main, as before")
	p, err := Open(root)
	require.NoError(t, err)
	require.Equal(t, Scope, p.ScopeLine())
}
