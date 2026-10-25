package apecmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/stretchr/testify/require"
)

// v1.3: `ape project init` registers pre-tool for Monitor too, and doctor's
// hooks row accepts it.
func TestProjectInit_PreToolMatcherCoversMonitor(t *testing.T) {
	root := v1Repo(t)
	data, err := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	require.NoError(t, err)
	require.Contains(t, string(data), `"matcher": "Edit|Write|MultiEdit|NotebookEdit|Bash|Monitor"`)
	res := checkV1Hooks(context.Background(), doctorEnv{ProjectRoot: root})
	require.Equal(t, StatusOK, res.Status, res.Message)
}

// v1.3: a project registered before v1.3 (the fixture is one: its pre-tool
// matcher has no Monitor) — project check stays green and lists the
// notice, doctor's hooks row warns with init as the fix, and init adds
// Monitor in place.
func TestPreMonitorMatcher_NoticeWarnAndInit(t *testing.T) {
	root := seedV1Fixture(t)
	stdout, stderr, err := v1Exec(t, newProjectCmd, "check", "--json", "--cwd", root)
	require.NoError(t, err, "a notice never fails the check\nstdout: %s\nstderr: %s", stdout, stderr)
	require.Contains(t, stdout+stderr, apexv1.CodeHooksMonitorUnguarded)

	res := checkV1Hooks(context.Background(), doctorEnv{ProjectRoot: root})
	require.Equal(t, StatusWarn, res.Status, res.Message)
	require.Contains(t, res.Message, apexv1.CodeHooksMonitorUnguarded)
	require.Equal(t, "ape project init --cwd "+root, res.FixCommand)

	stdout, stderr, err = v1Exec(t, newProjectCmd, "init", "--cwd", root)
	require.NoError(t, err, "stdout: %s\nstderr: %s", stdout, stderr)
	data, err := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	require.NoError(t, err)
	require.Contains(t, string(data), `"matcher": "`+apexv1.PreToolMatcher+`"`)
	res = checkV1Hooks(context.Background(), doctorEnv{ProjectRoot: root})
	require.Equal(t, StatusOK, res.Status, res.Message)
	stdout, stderr, err = v1Exec(t, newProjectCmd, "check", "--json", "--cwd", root)
	require.NoError(t, err)
	require.NotContains(t, stdout+stderr, apexv1.CodeHooksMonitorUnguarded)
}
