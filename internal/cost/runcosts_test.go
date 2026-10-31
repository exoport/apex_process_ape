package cost

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/runlog"
)

// v1.4.2 D1: a run's tokens are the four kinds together, read from the
// kinds — so a manifest written before tokens_total existed counts the
// same — and its wall time is the manifest's duration.
func TestRunCosts_TokensAndWall(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(runlog.TasksRoot(root), "apex-apply", "20261031-100000-aaaaaa")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	manifest := "run_id: 20261031-100000-aaaaaa\nstarted_at: 2026-10-31T10:00:00Z\nduration_seconds: 125.5\nstatus: completed\n" +
		"totals:\n  cost_usd: 1.5\n  tokens_input: 100\n  tokens_output: 20\n  tokens_cache_read: 3000\n  tokens_cache_creation: 400\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte(manifest), 0o644))
	runs, err := RunCosts(root)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.Equal(t, 3520, runs[0].TokensTotal, "cache reads included (D656 Q-L1)")
	require.InDelta(t, 125.5, runs[0].DurationSecs, 0.001)
}
