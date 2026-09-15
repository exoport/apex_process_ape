package apecmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// A malformed reconcile call is a usage error: exit 2, with the reason on
// stderr, and the tracker untouched. It used to exit 1 — the code a caller
// reads as a failed run — for a missing --epic/--all, and to exit 0 for
// `--epic 1 --all`, having run --all and dropped --epic in silence.
//
// Not parallel: captureStderr swaps the process-global os.Stderr.
func TestSprintReconcile_UsageErrorsExitTwoAndSayWhy(t *testing.T) {
	root := projectNoChdir(t)
	tracker := filepath.Join(root, "development", "implementation", "sprint-status.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(tracker), 0o755))
	body := []byte("development_status:\n  epic-1: backlog\n  1-1_x: done\n")
	require.NoError(t, os.WriteFile(tracker, body, 0o644))

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"neither --epic nor --all", nil, "reconcile needs --epic <N> or --all"},
		{"both at once", []string{"--epic", "1", "--all"}, "--epic and --all are exclusive"},
		{"epic 0 is not an epic", []string{"--epic", "0"}, "--epic must be an epic number of 1 or more, got 0"},
		{"a negative epic", []string{"--epic", "-2"}, "got -2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			stderr := captureStderr(t, func() {
				_, _, err = runReconcile(t, root, tc.args...)
			})
			require.Error(t, err)
			code, silent := ExitCode(err)
			require.Equal(t, ExitUsage, code)
			require.True(t, silent, "reported by the command itself")
			require.Contains(t, stderr, tc.want,
				"the exit code is reported as already printed, so the message must actually be on stderr")

			after, readErr := os.ReadFile(tracker)
			require.NoError(t, readErr)
			require.Equal(t, body, after, "a refused call writes nothing")
		})
	}
}

// Outside any project with no --file there is no tracker to reconcile.
// That was already exit 2 — and printed nothing at all, because the bare
// usageErr is reported by ExitCode as already printed and nobody printed
// it. Measured on the pre-fix binary: `ape sprint reconcile --epic 1` in an
// empty directory, exit 2, empty stderr.
func TestSprintReconcile_NoTrackerIsAUsageErrorThatSaysSo(t *testing.T) {
	var err error
	stderr := captureStderr(t, func() {
		_, _, err = runReconcile(t, t.TempDir(), "--epic", "1")
	})
	code, _ := ExitCode(err)
	require.Equal(t, ExitUsage, code)
	require.Contains(t, stderr, "no sprint-status.yaml resolved; pass --file")
}

// The valid shapes still run: a guard that refused everything would pass
// the tests above.
func TestSprintReconcile_ValidScopesStillRun(t *testing.T) {
	root := projectNoChdir(t)
	tracker := filepath.Join(root, "development", "implementation", "sprint-status.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(tracker), 0o755))
	require.NoError(t, os.WriteFile(tracker,
		[]byte("development_status:\n  epic-1: backlog\n  1-1_x: done\n"), 0o644))

	for _, args := range [][]string{{"--epic", "1", "--check"}, {"--all", "--check"}} {
		stdout, _, err := runReconcile(t, root, args...)
		require.NoError(t, err, "%v", args)
		require.Contains(t, stdout, "would reconcile epic-1", "%v", args)
	}
}
