package apecmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// 1.4.3 PDFA-4: a pre-tool hook registered without "onFailure": "block" (as
// every init before rc.7q wrote it) is project check's hooks.fail-open
// notice, and doctor's hooks row now says so too: WARN, with init as the
// fix (pdfa's doctor said "24 ok" over it). After init, OK.
func TestDoctorHooks_FailOpenWarns(t *testing.T) {
	root := v1Repo(t)
	settings := filepath.Join(root, ".claude", "settings.json")
	data, err := os.ReadFile(settings)
	require.NoError(t, err)
	stripped := strings.ReplaceAll(string(data), `"onFailure": "block"`, `"x": 0`)
	require.NotEqual(t, string(data), stripped, "the fixture has onFailure to strip")
	require.NoError(t, os.WriteFile(settings, []byte(stripped), 0o644))

	res := checkV1Hooks(context.Background(), doctorEnv{ProjectRoot: root})
	require.Equal(t, StatusWarn, res.Status, res.Message)
	require.Contains(t, res.Message, apexv1.CodeHooksFailOpen)
	require.Equal(t, "ape project init --cwd "+root, res.FixCommand)

	stdout, stderr, err := v1Exec(t, newProjectCmd, "init", "--cwd", root)
	require.NoError(t, err, "stdout: %s\nstderr: %s", stdout, stderr)
	res = checkV1Hooks(context.Background(), doctorEnv{ProjectRoot: root})
	require.Equal(t, StatusOK, res.Status, res.Message)
}
