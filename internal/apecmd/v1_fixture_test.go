package apecmd

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// seedV1Fixture copies testdata/apexv1project the way the live gates seed
// it: plus the CLAUDE.md init writes, committed as `apex: project init`.
func seedV1Fixture(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the v1 fixtures are POSIX shell scripts run by path (./x.sh), not yet run on Windows; checks themselves run under sh -c on every OS")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	for _, kv := range [][2]string{{"GIT_AUTHOR_NAME", "t"}, {"GIT_AUTHOR_EMAIL", "t@t"}, {"GIT_COMMITTER_NAME", "t"}, {"GIT_COMMITTER_EMAIL", "t@t"}} {
		t.Setenv(kv[0], kv[1])
	}
	root := filepath.Join(t.TempDir(), "project")
	require.NoError(t, os.CopyFS(root, os.DirFS(filepath.Join("..", "..", "testdata", "apexv1project"))))
	require.NoError(t, os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("@AGENTS.md\n"), 0o644))
	v1Git(t, root, "init", "-q")
	v1Git(t, root, "add", "-A")
	v1Git(t, root, "commit", "-q", "-m", "apex: project init")
	return root
}

// The fixture the live gates run sessions against must stay a valid v1
// project: a gate seeded from a broken one would judge the breakage.
func TestFixture_ApexV1ProjectIsGreen(t *testing.T) {
	root := seedV1Fixture(t)
	stdout, stderr, err := v1Exec(t, newProjectCmd, "check", "--cwd", root)
	require.NoError(t, err, "stdout: %s\nstderr: %s", stdout, stderr)
	require.Contains(t, stdout, "green")
}
