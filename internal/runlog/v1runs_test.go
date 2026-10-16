package runlog

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// APEX v1 trial D16, backlog 26: in a v1 project (_apex/state exists)
// every run record lives under _apex/state/runs, never in the output
// folder models clear out, and the directory keeps itself out of git.
func TestApeRoot_V1ProjectKeepsRunsInStateDir(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex", "state"), 0o755))
	runs := filepath.Join(root, "_apex", "state", "runs")

	require.Equal(t, runs, ApeRoot(root))
	require.Equal(t, filepath.Join(runs, "tasks"), TasksRoot(root))
	require.Equal(t, filepath.Join(runs, "tasks", "apex-apply", "r1"), TaskRunDir(root, "apex-apply", "r1"))
	require.Equal(t, filepath.Join(runs, "detached"), DetachedRoot(root))
	require.Equal(t, filepath.Join(runs, "timestamp.state"), TimestampStatePath(root))
	for _, rr := range RunRoots(root) {
		require.True(t, strings.HasPrefix(rr.Path, runs+string(filepath.Separator)), rr.Path)
	}
	whitelist, err := os.ReadFile(filepath.Join(root, "_apex", "state", ".gitignore"))
	require.NoError(t, err)
	require.Equal(t, V1StateGitignore, string(whitelist), "a state dir without its whitelist gets one")
	require.NoDirExists(t, filepath.Join(root, "_output"))
}

// An older ape's development/.apex/ is not a marker: project check
// reports it, and nothing reads it silently.
func TestApeRoot_OldStateFolderIsNotAMarker(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "development", ".apex"), 0o755))
	require.Equal(t, filepath.Join(root, "_output", "ape"), ApeRoot(root))
	require.NoDirExists(t, filepath.Join(root, "_apex", "state"))
}

// A bare development/ folder (this repository has one) is not v1.
func TestApeRoot_NonV1ProjectIsUnchanged(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "development", "planning"), 0o755))
	require.Equal(t, filepath.Join(root, "_output", "ape"), ApeRoot(root))
	require.NoDirExists(t, filepath.Join(root, "_apex", "state"))
}

// The whitelist keeps a run from dirtying the tree, and from riding along
// in a wholesale `git add -A`, while the two tracked records still can.
func TestApeRoot_V1RunsAreNeverTracked(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	EnsureV1State(root)
	gitIn := func(args ...string) string {
		c := exec.Command("git", args...)
		c.Dir = root
		c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
		out, err := c.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
		return string(out)
	}
	gitIn("init", "-q")
	dir := TaskRunDir(root, "apex-apply", "20261015-120000-abcd")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte("run_id: x\n"), 0o644))
	state := V1StateDir(root)
	for _, f := range []string{"logs/x/T1.log", "hooks/s.json", "project.json", "milestones.json", "decisions.json"} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(state, f)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(state, f), []byte("{}\n"), 0o644))
	}
	gitIn("add", "-A")
	staged := strings.Fields(gitIn("diff", "--cached", "--name-only"))
	require.ElementsMatch(t, []string{"_apex/state/.gitignore", "_apex/state/decisions.json", "_apex/state/milestones.json"}, staged)
}
