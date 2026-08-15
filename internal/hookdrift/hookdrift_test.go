package hookdrift

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// writeRun materialises a fake run dir with a hook-events.jsonl (and an
// optional manifest) under <root>/_output/tasks/<skill>/<runID>/.
func writeRun(t *testing.T, root, skill, runID, hookBody, manifest string) string {
	t.Helper()
	dir := filepath.Join(root, "_output", "tasks", skill, runID)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, "hook-events.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(hookBody), 0o600))
	if manifest != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte(manifest), 0o600))
	}
	return dir
}

func lines(ls ...string) string { return strings.Join(ls, "\n") + "\n" }

const (
	healthyStop = `{"event":"Stop","payload":{"background_tasks":[],"session_id":"s1"}}`
	// The shape a future Claude Code that renamed the field would produce.
	driftedStop  = `{"event":"Stop","payload":{"session_id":"s1"}}`
	agentPost    = `{"event":"PostToolUse","payload":{"tool_name":"Agent","tool_response":{"status":"completed"}}}`
	bashPost     = `{"event":"PostToolUse","payload":{"tool_name":"Bash","tool_response":{"stdout":"hi"}}}`
	subagentStop = `{"event":"SubagentStop","payload":{"agent_id":"a1","agent_transcript_path":"/tmp/a1.jsonl"}}`
)

func TestObserve_HealthyCorpus(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeRun(t, root, "apex-story-batch-dev", "run1",
		lines(agentPost, subagentStop, healthyStop),
		"claude_version: 2.1.232 (Claude Code)\nstatus: completed\n")

	rep, err := Observe(root, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.True(t, rep.Observed())
	require.True(t, rep.OK())
	require.Equal(t, 1, rep.Runs)
	require.Empty(t, rep.Drifted())
	require.Contains(t, rep.Summary(), "background_tasks 1/1")
	require.Contains(t, rep.Summary(), "Claude Code 2.1.232")
	require.NotContains(t, rep.Summary(), "(Claude Code)")
}

// The failure this package exists for: the gate's field is gone, nothing
// errored, and without this check nobody would notice.
func TestObserve_DetectsDrift(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeRun(t, root, "apex-story-batch-dev", "run1",
		lines(agentPost, subagentStop, driftedStop, driftedStop), "")

	rep, err := Observe(root, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.True(t, rep.Observed())
	require.False(t, rep.OK())

	drifted := rep.Drifted()
	require.Len(t, drifted, 1)
	require.Equal(t, FieldBackgroundTasks, drifted[0].Field)
	require.Equal(t, 2, drifted[0].Seen)
	require.Zero(t, drifted[0].Present)
}

// Partial presence is NOT drift: one old runlog in the window alongside
// current ones must not raise an alarm.
func TestObserve_PartialPresenceIsNotDrift(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeRun(t, root, "skill-a", "run1", lines(driftedStop), "")
	writeRun(t, root, "skill-b", "run2", lines(healthyStop), "")

	rep, err := Observe(root, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.True(t, rep.OK())
	require.Equal(t, 2, rep.Runs)
}

// Absence of evidence is not coverage: a project with no runs skips
// rather than passing, exactly as `costs coverage` does in CI.
func TestObserve_NoRunsIsUnobserved(t *testing.T) {
	t.Parallel()
	rep, err := Observe(t.TempDir(), time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.False(t, rep.Observed())
	require.True(t, rep.OK())
	require.Contains(t, rep.Summary(), "not verified")
}

// Runs older than the window say nothing about the Claude Code installed
// today, so they must not be counted.
func TestObserve_WindowExcludesOldRuns(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := writeRun(t, root, "skill-a", "old", lines(healthyStop), "")
	old := time.Now().Add(-72 * time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(dir, "hook-events.jsonl"), old, old))

	rep, err := Observe(root, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.False(t, rep.Observed())
	require.Zero(t, rep.Runs)
}

// Only Agent-tool results carry a spawn status worth gating on; a Bash
// PostToolUse must not be counted as evidence either way.
func TestObserve_OnlyAgentPostToolUseCounts(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeRun(t, root, "skill-a", "run1", lines(bashPost, bashPost, healthyStop), "")

	rep, err := Observe(root, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	for _, o := range rep.Observations {
		if o.Field == FieldToolResponse {
			require.Zero(t, o.Seen, "Bash results must not count toward the Agent contract")
		}
	}
	require.True(t, rep.OK())
}

// A malformed line must never invalidate a sweep.
func TestObserve_ToleratesMalformedLines(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeRun(t, root, "skill-a", "run1", lines(`{not json`, healthyStop, ``), "")

	rep, err := Observe(root, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.True(t, rep.Observed())
	require.True(t, rep.OK())
}

// TestObserve_RealFixtures runs the sweep over the captured payloads in
// testdata, so the detector is validated against real Claude Code output
// rather than only against hand-written rows.
func TestObserve_RealFixtures(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	body, err := os.ReadFile(filepath.Join(
		"..", "..", "testdata", "hookpayloads", "stop-clean-with-contract.jsonl"))
	require.NoError(t, err)
	agent, err := os.ReadFile(filepath.Join(
		"..", "..", "testdata", "hookpayloads", "agent-post-completed.jsonl"))
	require.NoError(t, err)
	subs, err := os.ReadFile(filepath.Join(
		"..", "..", "testdata", "hookpayloads", "subagent-pairs.jsonl"))
	require.NoError(t, err)

	writeRun(t, root, "apex-story-batch-dev", "run1",
		string(body)+string(agent)+string(subs), "")

	rep, err := Observe(root, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.True(t, rep.Observed())
	require.True(t, rep.OK(), "real captured payloads must satisfy the contract: %s", rep.Summary())
}
