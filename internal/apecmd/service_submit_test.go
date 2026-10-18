package apecmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/repl"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/exoport/apex_process_ape/internal/service"
	"github.com/stretchr/testify/require"
)

// orchestratorManifest writes a task manifest for apex-orchestrator run orch-run.
func orchestratorManifest(t *testing.T, root, status string) {
	t.Helper()
	dir := filepath.Join(runlog.TasksRoot(root), apexv1.OrchestratorSkill, "orch-run")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte("run_id: orch-run\nstatus: "+status+"\n"), 0o644))
}

// D83: the daemon's check of a depth-1 caller is a direct `ape task`'s rule
// over the stamp. Only a running apex-orchestrator run's job, only an
// allow-listed skill's task, only while its child slot is free; and the
// environment it hands the job's ape makes that ape the orchestrator's
// child, by the same gateSession a direct child goes through.
func TestServiceGate(t *testing.T) {
	outsideEverySession(t)
	root := t.TempDir()
	job := func(kind service.Kind, skill string) service.RunRequest {
		return service.RunRequest{ProjectRoot: root, Skill: skill, Caller: &service.CallerStamp{Depth: 1, RunID: "orch-run"}}
	}

	_, err := serviceGate(service.KindTask, job(service.KindTask, "apex-apply"))
	requireSessionDepth(t, err)
	require.Contains(t, err.Error(), "not a running apex-orchestrator run")

	orchestratorManifest(t, root, "running")
	env, err := serviceGate(service.KindTask, job(service.KindTask, "apex-apply"))
	require.NoError(t, err)
	_, err = serviceGate(service.KindTask, job(service.KindTask, "apex-handoff"))
	requireCode(t, err, apexv1.CodeSessionNotAllowed)
	_, err = serviceGate(service.KindPrompt, job(service.KindPrompt, ""))
	requireCode(t, err, apexv1.CodeSessionNotAllowed)

	// The probe leaves no lock behind; a live child's lock is busy.
	_, statErr := os.Stat(filepath.Join(runlog.SessionLocksRoot(root), "child-orch-run.json"))
	require.ErrorIs(t, statErr, os.ErrNotExist)
	other := &sessionLaunch{depth: 2, skill: "apex-change", parentRun: "orch-run"}
	require.NoError(t, other.acquire(root, "detached-other"))
	_, err = serviceGate(service.KindTask, job(service.KindTask, "apex-apply"))
	requireCode(t, err, apexv1.CodeChildRunning)
	other.release()

	// The job's ape, under that environment, is the orchestrator's child.
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	l, err := gateSession("ape task", "apex-apply", root)
	require.NoError(t, err)
	require.Equal(t, 2, l.depth)
	require.Equal(t, "orch-run", l.parentRun)
	require.NoError(t, l.acquire(root, "pid-job"))
	_, err = serviceGate(service.KindTask, job(service.KindTask, "apex-review"))
	requireCode(t, err, apexv1.CodeChildRunning)
	l.release()

	orchestratorManifest(t, root, "completed")
	_, err = serviceGate(service.KindTask, job(service.KindTask, "apex-apply"))
	requireSessionDepth(t, err)
}

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	require.Error(t, err)
	switch e := err.(type) {
	case *apexv1.UsageError:
		require.Equal(t, code, e.Code, e.Msg)
	case *apexv1.ConflictError:
		require.Equal(t, code, e.Code, e.Msg)
	default:
		t.Fatalf("%T: %v", err, err)
	}
}

// The client stamps where it runs, as `ape task` decides it: nothing at a
// terminal, the session's run at depth 1, and its parent at depth 2.
func TestCallerStamp(t *testing.T) {
	outsideEverySession(t)
	c := callerStamp("")
	require.Equal(t, 0, c.Depth)
	require.Empty(t, c.RunID)
	require.Equal(t, os.Getpid(), c.PID)

	inSession(t, "task/orch-run", 1)
	registerRow(t, orchestratorRow())
	c = callerStamp("")
	require.Equal(t, service.CallerStamp{Depth: 1, RunID: "orch-run", PID: os.Getpid()}, *c)

	outsideEverySession(t)
	inSession(t, "task/child-run", 2)
	t.Setenv(repl.EnvParentRun, "orch-run")
	c = callerStamp("")
	require.Equal(t, service.CallerStamp{Depth: 2, RunID: "child-run", ParentRun: "orch-run", PID: os.Getpid()}, *c)
}
