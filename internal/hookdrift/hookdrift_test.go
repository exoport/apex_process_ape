package hookdrift

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/stretchr/testify/require"
)

// writeRun materialises a fake run dir with a hook-events.jsonl (and an
// optional manifest) under <root>/_output/tasks/<skill>/<runID>/.
func writeRun(t *testing.T, root, skill, runID, hookBody, manifest string) string {
	t.Helper()
	return writeRunUnder(t, root, filepath.Join("_output", "ape", "tasks"), skill, runID, hookBody, manifest)
}

// writeRunUnder is writeRun with the runlog root spelled out, so the tests
// can put a run where each real producer puts one.
func writeRunUnder(t *testing.T, root, runlogRoot, skill, runID, hookBody, manifest string) string {
	t.Helper()
	dir := filepath.Join(root, runlogRoot, skill, runID)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, "hook-events.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(hookBody), 0o600))
	if manifest != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte(manifest), 0o600))
	}
	return dir
}

// touch sets a runlog's mtime, which is how Observe decides which run is
// the most recent and therefore whose Claude Code version is judged.
func touch(t *testing.T, dir string, at time.Time) {
	t.Helper()
	require.NoError(t, os.Chtimes(filepath.Join(dir, "hook-events.jsonl"), at, at))
}

func manifestFor(version string) string {
	return "claude_version: " + version + " (Claude Code)\nstatus: completed\n"
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

// Partial presence WITHIN one harness version is not drift: a field the
// harness omits on some turns and sends on others is still being sent.
// Cross-version masking is a different problem, handled by scoping the
// verdict — see TestObserve_JudgesOnlyTheNewestVersion.
func TestObserve_PartialPresenceIsNotDrift(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeRun(t, root, "skill-a", "run1", lines(driftedStop), "")
	writeRun(t, root, "skill-b", "run2", lines(healthyStop), "")

	rep, err := Observe(root, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.True(t, rep.OK())
	require.Equal(t, 2, rep.Runs)
	require.Equal(t, 2, rep.Scanned, "both runs are unstamped, so both are in the judged bucket")
}

// The masking this scoping exists for. A 30-day window that straddles a
// Claude Code upgrade holds healthy pre-upgrade runs; without scoping,
// their Present > 0 keeps the verdict green while every run on the harness
// actually installed has lost the field.
func TestObserve_JudgesOnlyTheNewestVersion(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	now := time.Now()
	// Three healthy runs on the old harness...
	for i, id := range []string{"old1", "old2", "old3"} {
		d := writeRun(t, root, "skill-a", id, lines(healthyStop), manifestFor("2.1.232"))
		touch(t, d, now.Add(-time.Duration(30+i)*time.Minute))
	}
	// ...and one on the harness in use now, which has dropped the field.
	d := writeRun(t, root, "skill-a", "new1", lines(driftedStop), manifestFor("2.1.240"))
	touch(t, d, now.Add(-time.Minute))

	rep, err := Observe(root, now.Add(-time.Hour))
	require.NoError(t, err)
	require.False(t, rep.OK(), "drift on the installed harness must not be masked by older runs")
	require.Equal(t, "2.1.240", rep.Judged)
	require.Equal(t, 4, rep.Runs)
	require.Equal(t, 1, rep.Scanned)
	require.Equal(t, 3, rep.Ignored)

	drifted := rep.Drifted()
	require.Len(t, drifted, 1)
	require.Equal(t, FieldBackgroundTasks, drifted[0].Field)

	// The runs it did not judge have to be named, or a one-run verdict
	// reads as though it spoke for the whole window.
	require.Contains(t, rep.Summary(), "Claude Code 2.1.240")
	require.Contains(t, rep.Summary(), "3 run(s) from 1 other version(s) not judged")
}

// The mirror of the above: the newest harness is healthy, and drift that
// existed on a version no longer in use is not raised as a live problem.
func TestObserve_OldVersionDriftIsNotJudged(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	now := time.Now()
	old := writeRun(t, root, "skill-a", "old1", lines(driftedStop), manifestFor("2.1.232"))
	touch(t, old, now.Add(-30*time.Minute))
	cur := writeRun(t, root, "skill-a", "new1", lines(healthyStop), manifestFor("2.1.240"))
	touch(t, cur, now.Add(-time.Minute))

	rep, err := Observe(root, now.Add(-time.Hour))
	require.NoError(t, err)
	require.True(t, rep.OK())
	require.Equal(t, "2.1.240", rep.Judged)
	require.Equal(t, 1, rep.Scanned)
	require.Equal(t, []string{"2.1.232", "2.1.240"}, rep.Versions,
		"every version in the window is still reported, even unjudged")
}

// Every producer's runlog root must be swept. Reading only _output/tasks
// made `ape pipeline` — the flagship command — invisible to this check for
// its entire existence, so a project that only runs pipelines reported
// "no interactive runs" for ever.
//
// The roots come from runlog.RunRoots rather than a list written out here:
// a hand-written list is exactly what produced the bug, and one in a test
// would go stale in the same silence. A fifth run kind is covered the day
// it is added.
func TestObserve_SweepsEveryRunlogRoot(t *testing.T) {
	t.Parallel()
	roots := runlog.RunRoots("")
	require.NotEmpty(t, roots)
	for _, r := range roots {
		t.Run(r.Kind, func(t *testing.T) {
			t.Parallel()
			proj := t.TempDir()
			// r.Path is relative to "" — rebuild it under the temp project.
			dir := filepath.Join(proj, r.Path, "run1")
			if r.Grouped {
				dir = filepath.Join(proj, r.Path, "design", "run1")
			}
			require.NoError(t, os.MkdirAll(dir, 0o755))
			require.NoError(t, os.WriteFile(
				filepath.Join(dir, "hook-events.jsonl"), []byte(lines(driftedStop)), 0o600,
			))

			rep, err := Observe(proj, time.Now().Add(-time.Hour))
			require.NoError(t, err)
			require.True(t, rep.Observed(), "a %s runlog under %s must be swept", r.Kind, r.Path)
			require.False(t, rep.OK(), "drift in a %s runlog must be detected", r.Kind)
		})
	}
}

// The framework owns the output folder; ape owns only its `ape/` subtree.
// A hook-events.jsonl that turns up anywhere else under it is not ape's run
// and must not be swept — checked against every directory the framework says
// it writes, so a sweep that widened by accident is caught.
func TestObserve_IgnoresTheFrameworksOutputTree(t *testing.T) {
	t.Parallel()
	// Every directory the framework says it writes under the output folder.
	for _, dir := range []string{
		"handoffs", "governance", "functionality", "planning",
		"implementation", "framework-requests", "retrospective",
		"ux-mockups", "ux-wireframes",
		// Build-repo-only, but the literal path is reachable when ape runs
		// inside the framework repo itself.
		"verify-orchestrator",
	} {
		t.Run(dir, func(t *testing.T) {
			t.Parallel()
			proj := t.TempDir()
			outside := filepath.Join(proj, "_output", dir, "run1")
			require.NoError(t, os.MkdirAll(outside, 0o755))
			require.NoError(t, os.WriteFile(
				filepath.Join(outside, "hook-events.jsonl"), []byte(lines(driftedStop)), 0o600,
			))

			rep, err := Observe(proj, time.Now().Add(-time.Hour))
			require.NoError(t, err)
			require.False(t, rep.Observed(),
				"only the ape/ subtree is ape's to read, not _output/%s", dir)
		})
	}
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
		"..", "..", "testdata", "hookpayloads", "stop-clean-with-contract.jsonl",
	))
	require.NoError(t, err)
	agent, err := os.ReadFile(filepath.Join(
		"..", "..", "testdata", "hookpayloads", "agent-post-completed.jsonl",
	))
	require.NoError(t, err)
	subs, err := os.ReadFile(filepath.Join(
		"..", "..", "testdata", "hookpayloads", "subagent-pairs.jsonl",
	))
	require.NoError(t, err)

	writeRun(t, root, "apex-story-batch-dev", "run1",
		string(body)+string(agent)+string(subs), "")

	rep, err := Observe(root, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.True(t, rep.Observed())
	require.True(t, rep.OK(), "real captured payloads must satisfy the contract: %s", rep.Summary())
}
