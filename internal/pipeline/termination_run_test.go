//go:build !windows

// The end-to-end half of the termination tests. Split out because it
// drives the real runner through claudeREPLShim, which lives in a
// !windows file — without this constraint the whole package stops
// compiling on Windows, and `make test-portable` (the Windows CI gate)
// fails on a test that was never meant to run there. The unit tests in
// termination_test.go stay portable and still cover classification and
// rendering on every platform.

package pipeline //nolint:testpackage // white-box: reads manifestWriter side effects

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/exoport/apex_process_ape/internal/sessiondriver"
)

// The wiring test. The unit cases above prove classification; this proves
// the record actually reaches manifest.yaml through the real runner —
// which is the half that was missing in production, not the classifying.
func TestRun_IdleTerminationReachesTheManifest(t *testing.T) {
	root := t.TempDir()
	pipelinesDir := filepath.Join(root, "_apex", "pipelines")
	require.NoError(t, os.MkdirAll(pipelinesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pipelinesDir, "stalled.yaml"),
		[]byte("name: stalled\nstages:\n  only:\n    chain:\n      - skill: apex-fake\n"), 0o644))

	spec, err := LoadSpec("stalled", root)
	require.NoError(t, err)
	stubSpecSkills(t, root, spec)

	idle := &sessiondriver.IdleTimeoutError{
		Label:      "interactive step",
		Idle:       60 * time.Minute,
		Window:     60 * time.Minute,
		LastSource: "hook",
		Diagnostic: "last progress hook 1h0m0s ago (hook 1h0m0s ago; transcript none for 3h44m; pty n/a); child pid 4242 alive",
	}
	runErr := Run(context.Background(), spec, RunOptions{
		ProjectRoot:  root,
		ClaudeBin:    claudeREPLShim(t),
		ApeVersion:   "0.0.71-test",
		NoCommit:     true,
		WaitStepDone: func(_ context.Context, _ string, _ int) error { return idle },
	})
	require.Error(t, runErr)

	entries, err := os.ReadDir(filepath.Join(root, "_output", "ape", "pipelines", "stalled"))
	require.NoError(t, err)
	var runID string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "20") {
			runID = e.Name()
			break
		}
	}
	require.NotEmpty(t, runID, "no run dir written")

	runDir := filepath.Join(root, "_output", "ape", "pipelines", "stalled", runID)
	data, err := os.ReadFile(filepath.Join(runDir, "manifest.yaml"))
	require.NoError(t, err)

	var m Manifest
	require.NoError(t, yaml.Unmarshal(data, &m))
	require.Equal(t, StatusFailed, m.Status)
	require.NotNil(t, m.Termination, "the run was terminated by the idle backstop and manifest.yaml does not say so — "+
		"this is the exact artifact a 3h44m eval capture left behind, indistinguishable from a step that did nothing")
	require.Equal(t, TerminationIdle, m.Termination.Kind)
	require.Contains(t, m.Termination.Diagnostic, "pty n/a")
	require.InDelta(t, 3600.0, m.Termination.WindowSecs, 0.001)

	// The zeros are still there — that is correct, they count COMPLETED
	// steps — but the report now says why they are zeros.
	require.Empty(t, m.Stages[0].Steps, "precondition: a step aborted mid-flight records no step")
	report, err := os.ReadFile(filepath.Join(runDir, "pipeline-report.md"))
	require.NoError(t, err)
	require.Contains(t, string(report), "## Why this run ended")
	require.Contains(t, string(report), "idle_timeout")
}
