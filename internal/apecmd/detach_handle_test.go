package apecmd

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The supervisor reads the handle as it starts, which can be before --detach
// has recorded the supervisor's pid. It used to write that stale copy back
// with the child's pid, erasing supervisor_pid; CI caught it as
// TestDetach_SurvivesTheStartingShellsTree timing out. Both orders must end
// with both pids recorded.
func TestDetachHandle_BothPIDsSurviveEitherWriteOrder(t *testing.T) {
	t.Parallel()

	for _, order := range []string{"detach first", "supervisor first"} {
		path := filepath.Join(t.TempDir(), "run.json")
		require.NoError(t, writeHandle(path, &detachedHandle{ID: "run", Argv: []string{"task"}}))

		// The supervisor's startup read, taken before either write.
		stale, err := readHandle(path)
		require.NoError(t, err)
		require.Zero(t, stale.SupervisorPID)

		if order == "detach first" {
			require.NoError(t, recordSupervisorPID(path, 4242))
			require.NoError(t, recordChildPID(path, 4242, 5151))
		} else {
			require.NoError(t, recordChildPID(path, 4242, 5151))
			require.NoError(t, recordSupervisorPID(path, 4242))
		}

		got, err := readHandle(path)
		require.NoError(t, err)
		require.Equal(t, 4242, got.SupervisorPID, order)
		require.Equal(t, 5151, got.ChildPID, order)
	}
}

// recordExit writes on a fresh read too: the exit lands without losing
// either pid, even from a copy read before they were recorded.
func TestDetachHandle_RecordExitKeepsThePIDs(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "run.json")
	require.NoError(t, writeHandle(path, &detachedHandle{ID: "run"}))
	stale, err := readHandle(path)
	require.NoError(t, err)
	require.NoError(t, recordSupervisorPID(path, 4242))
	require.NoError(t, recordChildPID(path, 4242, 5151))

	_ = recordExit(path, stale, 3, "boom")

	got, err := readHandle(path)
	require.NoError(t, err)
	require.NotZero(t, got.SupervisorPID)
	require.Equal(t, 5151, got.ChildPID)
	require.NotNil(t, got.ExitCode)
	require.Equal(t, 3, *got.ExitCode)
}
