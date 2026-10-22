package pipeline //nolint:testpackage // white-box: shares stubSpecSkills with sibling _test.go files

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// W4: a BeforeSpawn error ends the run before any claude spawns, and the
// run's manifest records it — a usage-guard stop as termination
// usage_guard, with the window's reset.
func TestRun_BeforeSpawnStopReachesTheManifest(t *testing.T) {
	root := t.TempDir()
	spec := NewSingleStepSpec("apex-fake", Step{Skill: "apex-fake"}, nil)
	stubSpecSkills(t, root, spec)
	resets := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	var dir string
	spawned := false
	runErr := Run(context.Background(), spec, RunOptions{
		ProjectRoot: root,
		ClaudeBin:   filepath.Join(root, "no-such-claude"),
		ApeVersion:  "1.1.0-test",
		NoCommit:    true,
		OnRunDir:    func(d string) { dir = d },
		BeforeSpawn: func(context.Context) error {
			return &LimitStopError{Kind: TerminationUsageGuard, Message: "usage guard: the 7-day window is at 0.86", ResetsAt: resets}
		},
		OnSessionSpawn: func(int) { spawned = true },
	})
	require.Error(t, runErr)
	require.False(t, spawned)
	require.NotEmpty(t, dir)
	data, err := os.ReadFile(filepath.Join(dir, "manifest.yaml"))
	require.NoError(t, err)
	var m Manifest
	require.NoError(t, yaml.Unmarshal(data, &m))
	require.Equal(t, StatusFailed, m.Status)
	require.NotNil(t, m.Termination)
	require.Equal(t, TerminationUsageGuard, m.Termination.Kind)
	require.True(t, resets.Equal(*m.Termination.ResetsAt))
	require.Contains(t, ResumeLine(TerminationUsageGuard, "M1", "ape task apex-loop --args M1"), "then, after the reset,")
}
