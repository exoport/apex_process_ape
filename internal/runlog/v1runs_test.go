package runlog

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// APEX v1 trial D16: in a v1 project (development/.apex exists) every run
// record lives under development/.apex/runs, never in the output folder
// models clear out, and the directory keeps itself out of git.
func TestApeRoot_V1ProjectKeepsRunsInApexDir(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "development", ".apex"), 0o755))
	runs := filepath.Join(root, "development", ".apex", "runs")

	require.Equal(t, runs, ApeRoot(root))
	require.Equal(t, filepath.Join(runs, "tasks"), TasksRoot(root))
	require.Equal(t, filepath.Join(runs, "tasks", "apex-apply", "r1"), TaskRunDir(root, "apex-apply", "r1"))
	require.Equal(t, filepath.Join(runs, "detached"), DetachedRoot(root))
	require.Equal(t, filepath.Join(runs, "timestamp.state"), TimestampStatePath(root))
	for _, rr := range RunRoots(root) {
		require.True(t, strings.HasPrefix(rr.Path, runs+string(filepath.Separator)), rr.Path)
	}
	ignore, err := os.ReadFile(filepath.Join(runs, ".gitignore"))
	require.NoError(t, err)
	require.Contains(t, string(ignore), "\n*\n")
	require.NoDirExists(t, filepath.Join(root, "_output"))
}

func TestApeRoot_V1ProjectHonoursDevelopmentFolder(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "_apex", "config.yaml"),
		[]byte("project_name: x\ndevelopment_folder: docs/dev\n"), 0o644))
	require.Equal(t, filepath.Join(root, "_output", "ape"), ApeRoot(root), "no docs/dev/.apex yet: not v1")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "docs", "dev", ".apex"), 0o755))
	require.Equal(t, filepath.Join(root, "docs", "dev", ".apex", "runs"), ApeRoot(root))
}

// A bare development/ folder (this repository has one) is not v1.
func TestApeRoot_NonV1ProjectIsUnchanged(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "development", "planning"), 0o755))
	require.Equal(t, filepath.Join(root, "_output", "ape"), ApeRoot(root))
	require.NoDirExists(t, filepath.Join(root, "development", ".apex"))
}

// The self-ignoring directory keeps a run from dirtying the tree, with no
// change to any tracked .gitignore.
func TestApeRoot_V1RunsAreNeverTracked(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "development", ".apex"), 0o755))
	git := exec.Command("git", "init", "-q")
	git.Dir = root
	require.NoError(t, git.Run())
	dir := TaskRunDir(root, "apex-apply", "20261015-120000-abcd")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte("run_id: x\n"), 0o644))
	st := exec.Command("git", "status", "--porcelain", "--untracked-files=all")
	st.Dir = root
	out, err := st.Output()
	require.NoError(t, err)
	require.Empty(t, strings.TrimSpace(string(out)))
}

// The framework's apex-plan commits development/ wholesale (`git add
// development/`); the run records must not ride along, in a project
// initialised before `runs/` was listed in .apex/.gitignore too.
func TestApeRoot_V1RunsStayOutOfAWholesaleAdd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	apexDir := filepath.Join(root, "development", ".apex")
	require.NoError(t, os.MkdirAll(apexDir, 0o755))
	// The pre-D16 ignore list: no runs/ entry.
	require.NoError(t, os.WriteFile(filepath.Join(apexDir, ".gitignore"), []byte("logs/\nstop-gate/\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "development", "project.md"), []byte("# x\n"), 0o644))
	gitIn := func(args ...string) string {
		c := exec.Command("git", args...)
		c.Dir = root
		out, err := c.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
		return string(out)
	}
	gitIn("init", "-q")
	dir := TaskRunDir(root, "apex-plan", "r1")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte("run_id: r1\n"), 0o644))
	gitIn("add", "development/")
	staged := gitIn("diff", "--cached", "--name-only")
	require.Contains(t, staged, "development/project.md")
	require.NotContains(t, staged, "runs")
}
