package apexv1

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// B1 (D755 (a)): a run logs to its own file, runs/<scope>.<run-id>.log, so
// a later run of the scope no longer truncates an earlier one's log (1b
// lost M9's take-in-2 fence line that way). RunLogPath is the scope's newest
// log; a log written before B1 (runs/<scope>.log) is still found. Pruning
// keeps the newest RunLogsKept per scope and touches no other scope's.
func TestRunLogs_PerRunPathsAndPruning(t *testing.T) {
	p := fixture(t, baseFiles())
	require.NoError(t, os.MkdirAll(p.RunsDir(), 0o755))
	require.Equal(t, filepath.Join(p.RunsDir(), "M1.20261102-120000-1-abc.log"), p.RunLogPathFor("M1", "20261102-120000-1-abc"))

	legacy := filepath.Join(p.RunsDir(), "M1.log")
	require.NoError(t, os.WriteFile(legacy, []byte("before B1\n"), 0o644))
	require.Equal(t, legacy, p.RunLogPath("M1"), "a pre-B1 log is found while no per-run log exists")

	ids := make([]string, 0, RunLogsKept+2)
	for i := range RunLogsKept + 2 {
		id := "20261102-1200" + string(rune('0'+i/10)) + string(rune('0'+i%10)) + "-1-abc"
		ids = append(ids, id)
		path := p.RunLogPathFor("M1", id)
		require.NoError(t, os.WriteFile(path, []byte(id+"\n"), 0o644))
		at := time.Date(2026, 11, 2, 12, 0, i, 0, time.UTC)
		require.NoError(t, os.Chtimes(path, at, at))
	}
	other := p.RunLogPathFor("take-in-M1", "20261101-000000-1-abc")
	require.NoError(t, os.WriteFile(other, []byte("x\n"), 0o644))
	require.Equal(t, p.RunLogPathFor("M1", ids[len(ids)-1]), p.RunLogPath("M1"), "the newest run's log")

	p.PruneRunLogs("M1")
	for i, id := range ids {
		if i < 2 {
			require.NoFileExists(t, p.RunLogPathFor("M1", id), "older than the newest %d: pruned", RunLogsKept)
		} else {
			require.FileExists(t, p.RunLogPathFor("M1", id))
		}
	}
	require.FileExists(t, other, "control: another scope's log is not pruned")
	require.FileExists(t, legacy, "control: the pre-B1 log is left as it is")
}

// B1: fence lines noted during a run are taken once; the next take starts
// empty.
func TestNoteFence_TakenOnce(t *testing.T) {
	TakeFences()
	NoteFence("a")
	NoteFence("b")
	require.Equal(t, []string{"a", "b"}, TakeFences())
	require.Empty(t, TakeFences(), "control: nothing left for the next run")
}
