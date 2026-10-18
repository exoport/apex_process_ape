//go:build unix

package apecmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Q4: a session an orchestrator started dies with it. The supervisor of a
// detached child watches the parent's ape process; when it is gone it stops
// the child as `ape run stop` does, marks the handle stop_reason parent_gone
// and the child's manifest termination parent_gone, and releases the
// child's session lock. The child here is this test binary as ape, waiting
// on a run that never ends, so it lasts until it is stopped.
func TestSupervise_StopsTheChildWhenItsParentIsGone(t *testing.T) {
	outsideEverySession(t)
	t.Setenv(asApeBinary, "1")
	old := parentPollInterval
	parentPollInterval = 50 * time.Millisecond
	t.Cleanup(func() { parentPollInterval = old })

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(runlog.DetachedRoot(root), 0o755))
	require.NoError(t, writeHandle(handlePath(root, "forever"), &detachedHandle{ID: "forever"}))

	parent := exec.Command("sleep", "60")
	require.NoError(t, parent.Start())
	t.Cleanup(func() { _ = parent.Process.Kill(); _, _ = parent.Process.Wait() })

	// The child's own record, as noteDetachedRun leaves it.
	runDir := filepath.Join(runlog.TasksRoot(root), "apex-apply", "20261017-120000-abc")
	require.NoError(t, os.MkdirAll(runDir, 0o755))
	require.NoError(t, os.WriteFile(pipeline.ManifestPath(runDir), []byte("run_id: 20261017-120000-abc\nstatus: running\n"), 0o644))

	launch := &sessionLaunch{depth: 2, skill: "apex-apply", parentRun: "orch-run", parentPID: parent.Process.Pid}
	require.NoError(t, launch.acquire(root, "child-1"))
	h := &detachedHandle{
		ID: "child-1", Argv: []string{"run", "wait", "forever", "--cwd", root, "--wait", "60s"}, Dir: root,
		Log: filepath.Join(runlog.DetachedRoot(root), "child-1.log"), Stdout: filepath.Join(runlog.DetachedRoot(root), "child-1.out"),
		StartedAt: time.Now().UTC(), Depth: 2, ParentRun: "orch-run", ParentPID: parent.Process.Pid, Locks: launch.locks, RunDir: runDir,
	}
	path := handlePath(root, "child-1")
	require.NoError(t, writeHandle(path, h))

	done := make(chan error, 1)
	go func() { done <- supervise(context.Background(), path) }()
	require.Eventually(t, func() bool {
		cur, err := readHandle(path)
		return err == nil && cur.ChildPID != 0
	}, 20*time.Second, 20*time.Millisecond, "the child never started")
	time.Sleep(300 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("the child ended while its parent lived: %v", err)
	default:
	}

	require.NoError(t, parent.Process.Kill())
	_, _ = parent.Process.Wait()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the child outlived its parent")
	}

	got, err := readHandle(path)
	require.NoError(t, err)
	require.Equal(t, stopParentGone, got.StopReason)
	require.NotNil(t, got.ExitCode)
	require.Equal(t, exitCodeStillRunning, *got.ExitCode, "stopped as ape run stop does: its own shutdown decides the code")

	m, err := pipeline.LoadManifest(runDir)
	require.NoError(t, err)
	require.NotNil(t, m.Termination)
	require.Equal(t, pipeline.TerminationParentGone, m.Termination.Kind)
	require.Equal(t, pipeline.StatusCancelled, m.Status)
	require.Contains(t, m.Termination.Message, "orch-run")

	require.NoFileExists(t, launch.locks[0], "the supervisor released the child's lock")
	require.NoError(t, (&sessionLaunch{depth: 2, skill: "apex-plan", parentRun: "orch-run"}).acquire(root, "next"),
		"the orchestrator's next child may start")

	// ape run wait --json reports it.
	var out bytes.Buffer
	cmd := newRunWaitCmd()
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"child-1", "--cwd", root, "--json"})
	err = cmd.Execute()
	require.Equal(t, exitCodeStillRunning, exitCodeOf(t, err), "the run's own exit code")
	var rep map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &rep), out.String())
	require.Equal(t, "ended", rep["state"])
	require.Equal(t, "parent_gone", rep["stop_reason"])
	require.Equal(t, "orch-run", rep["parent_run"])
	require.EqualValues(t, 2, rep["depth"])
	require.NotContains(t, rep, "result", "no transcript: no result, and no error")
}

// The children of an orchestrator run roll up under it, in its manifest
// and in `ape costs run`, while totals stay its own (eval Q4: the eval sums
// every manifest).
func TestRecordLineage_RollsChildrenUpUnderTheOrchestrator(t *testing.T) {
	root := t.TempDir()
	write := func(skill, run, body string) string {
		dir := filepath.Join(runlog.TasksRoot(root), skill, run)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(pipeline.ManifestPath(dir), []byte(body), 0o644))
		return dir
	}
	orch := write(OrchestratorSkillForTest, "20261017-100000-aaa", "run_id: 20261017-100000-aaa\nstatus: completed\ndepth: 1\ntotals:\n  cost_usd: 1.5\n")
	require.NoError(t, os.Symlink(filepath.Base(orch), filepath.Join(filepath.Dir(orch), "latest")))
	write("apex-change", "20261017-100100-bbb", "run_id: 20261017-100100-bbb\nstatus: completed\ndepth: 2\nparent_run: 20261017-100000-aaa\ntotals:\n  cost_usd: 2\n")
	write("apex-apply", "20261017-100200-ccc", "run_id: 20261017-100200-ccc\nstatus: failed\ndepth: 2\nparent_run: 20261017-100000-aaa\ntotals:\n  cost_usd: 0.25\n")
	write("apex-apply", "20261017-090000-zzz", "run_id: 20261017-090000-zzz\nstatus: completed\ndepth: 1\ntotals:\n  cost_usd: 9\n")

	recordLineage(context.Background(), root, OrchestratorSkillForTest, runlog.TasksRoot(root), &sessionLaunch{depth: 1})
	data, err := os.ReadFile(pipeline.ManifestPath(orch))
	require.NoError(t, err)
	var m pipeline.Manifest
	require.NoError(t, yaml.Unmarshal(data, &m))
	require.InDelta(t, 1.5, m.Totals.CostUSD, 1e-9, "totals stay the orchestrator's own")
	require.NotNil(t, m.TreeCostUSD)
	require.InDelta(t, 3.75, *m.TreeCostUSD, 1e-9)
	require.Equal(t, []pipeline.ChildRunRecord{
		{Run: "20261017-100100-bbb", Skill: "apex-change", CostUSD: 2, Status: "completed"},
		{Run: "20261017-100200-ccc", Skill: "apex-apply", CostUSD: 0.25, Status: "failed"},
	}, m.Children)

	// A child's own cost entry names its parent.
	t.Chdir(root)
	out := captureStdout(t, func() {
		cmd := newCostsRunCmd()
		cmd.SetArgs([]string{"20261017-100000-aaa", "--output-format", "json"})
		require.NoError(t, cmd.Execute())
	})
	require.Contains(t, out, `"tree_cost_usd": 3.75`)
	require.Contains(t, out, `"run": "20261017-100200-ccc"`)
	out = captureStdout(t, func() {
		cmd := newCostsRunCmd()
		cmd.SetArgs([]string{"20261017-100100-bbb", "--output-format", "json"})
		require.NoError(t, cmd.Execute())
	})
	require.Contains(t, out, `"parent_run": "20261017-100000-aaa"`)
	require.NotContains(t, out, "tree_cost_usd")
}

// An orchestrator tree that cost $0 says so (eval rc.3 probe: omitempty
// dropped a computed 0.0, which read as "not computed"); a childless
// orchestrator's tree is its own cost; a child records none.
func TestRecordLineage_TreeCostZeroIsPresent(t *testing.T) {
	root := t.TempDir()
	write := func(skill, run, body string) string {
		dir := filepath.Join(runlog.TasksRoot(root), skill, run)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(pipeline.ManifestPath(dir), []byte(body), 0o644))
		require.NoError(t, os.Symlink(filepath.Base(dir), filepath.Join(filepath.Dir(dir), "latest")))
		return dir
	}
	orch := write(OrchestratorSkillForTest, "20261017-100000-aaa", "run_id: 20261017-100000-aaa\nstatus: completed\ndepth: 1\ntotals:\n  cost_usd: 0\n")
	write("apex-change", "20261017-100100-bbb", "run_id: 20261017-100100-bbb\nstatus: completed\ndepth: 2\nparent_run: 20261017-100000-aaa\ntotals:\n  cost_usd: 0\n")

	recordLineage(context.Background(), root, OrchestratorSkillForTest, runlog.TasksRoot(root), &sessionLaunch{depth: 1})
	data, err := os.ReadFile(pipeline.ManifestPath(orch))
	require.NoError(t, err)
	require.Contains(t, string(data), "tree_cost_usd: 0\n")
	t.Chdir(root)
	out := captureStdout(t, func() {
		cmd := newCostsRunCmd()
		cmd.SetArgs([]string{"20261017-100000-aaa", "--output-format", "json"})
		require.NoError(t, cmd.Execute())
	})
	require.Contains(t, out, `"tree_cost_usd": 0`)

	// No children: the tree is the orchestrator's own cost, still written.
	root2 := t.TempDir()
	dir := filepath.Join(runlog.TasksRoot(root2), OrchestratorSkillForTest, "20261017-110000-ddd")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(pipeline.ManifestPath(dir), []byte("run_id: 20261017-110000-ddd\nstatus: completed\ntotals:\n  cost_usd: 0.5\n"), 0o644))
	require.NoError(t, os.Symlink(filepath.Base(dir), filepath.Join(filepath.Dir(dir), "latest")))
	recordLineage(context.Background(), root2, OrchestratorSkillForTest, runlog.TasksRoot(root2), &sessionLaunch{depth: 1})
	var m pipeline.Manifest
	data, err = os.ReadFile(pipeline.ManifestPath(dir))
	require.NoError(t, err)
	require.NoError(t, yaml.Unmarshal(data, &m))
	require.NotNil(t, m.TreeCostUSD)
	require.InDelta(t, 0.5, *m.TreeCostUSD, 1e-9)
	require.Empty(t, m.Children)
}

// OrchestratorSkillForTest keeps the test readable.
const OrchestratorSkillForTest = "apex-orchestrator"
