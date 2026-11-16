//go:build unix

package apecmd

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// statusRuns is ape status --json's runs.
func statusRuns(t *testing.T, root string) []apexv1.LiveRun {
	t.Helper()
	stdout, _, err := v1Exec(t, newStatusCmd, "--json", "--cwd", root)
	require.NoError(t, err)
	var st struct {
		Runs []apexv1.LiveRun `json:"runs"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &st), stdout)
	require.NotNil(t, st.Runs, "runs is always present")
	return st.Runs
}

// V15-2D-STOP-1: a detached gate is listed by ape status and ape run
// status, and ape run stop M1 TERMs its worker — the worker's own, checked
// — and exits with what it recorded: run.interrupted, no gate run, no
// commit, the lock gone; the next call of the verb runs again.
func TestRunStop_LongRunGate(t *testing.T) {
	root, gateRuns, _ := gateRecallRepo(t)
	require.NoError(t, os.WriteFile(gateSleepFile(root), []byte("30\n"), 0o644))
	old := v1DetachDefault
	v1DetachDefault = true
	t.Setenv(asApeBinary, "1")
	t.Cleanup(func() { v1DetachDefault = old })
	p, err := apexv1.Open(root)
	require.NoError(t, err)
	t.Cleanup(func() {
		if l := p.LoadRunLock("M1"); l != nil && l.PID > 0 && l.PID != os.Getpid() {
			_ = syscall.Kill(l.PID, syscall.SIGKILL)
		}
		awaitNoProcessUnder(t, root, 10*time.Second)
	})
	lastGate := p.LastGateRun("M1")
	head := v1Git(t, root, "rev-parse", "HEAD")

	_, _, err = v1Exec(t, newMilestoneCmd, "gate", "M1", "--wait", "0", "--cwd", root)
	require.Equal(t, exitCodeStillRunning, exitCodeOf(t, err))
	require.Eventually(t, func() bool { return gateRuns() == 2 }, 30*time.Second, 50*time.Millisecond, "the worker's gate check is running")
	lock := p.LoadRunLock("M1")
	require.NotNil(t, lock)
	require.NotEqual(t, os.Getpid(), lock.PID)

	runs := statusRuns(t, root)
	require.Len(t, runs, 1)
	require.Equal(t, apexv1.LiveRun{Verb: "milestone gate", Scope: "M1", Command: "ape milestone gate M1", PID: lock.PID, Since: lock.Started, Log: lock.Log}, runs[0])
	stdout, _, err := v1Exec(t, newStatusCmd, "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, stdout, "Running long runs")
	require.Contains(t, stdout, "M1 ape milestone gate M1 since "+lock.Started)

	stdout, _, err = v1Exec(t, newRunCmd, "status", "M1", "--json", "--cwd", root)
	require.NoError(t, err)
	var rs longRunReport
	require.NoError(t, json.Unmarshal([]byte(stdout), &rs), stdout)
	require.Equal(t, longRunReport{ID: lock.ID, State: runRunning, Verb: "milestone gate", Scope: "M1", PID: lock.PID, Since: lock.Started, Log: lock.Log, Alive: true}, rs)

	stdout, stderr, err := v1Exec(t, newRunCmd, "stop", "M1", "--timeout", "30s", "--cwd", root)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err), stdout+stderr)
	require.Contains(t, stdout, "the run of `ape milestone gate M1` stopped; it ended with exit 1")
	require.Contains(t, stdout, "interrupted")
	awaitExit(t, lock.PID)
	require.Nil(t, p.LoadRunLock("M1"), "the lock is released")
	r := p.LoadRunResult("M1")
	require.Equal(t, lock.ID, r.ID)
	require.True(t, r.Reported, "printed by the stop: the next gate runs again")
	require.Equal(t, lastGate, p.LastGateRun("M1"), "no gate run recorded")
	require.Equal(t, head, v1Git(t, root, "rev-parse", "HEAD"), "no state commit")
	require.Empty(t, statusRuns(t, root))

	stdout, _, err = v1Exec(t, newRunCmd, "status", "M1", "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, stdout, `"state": "ended, exit 1"`)
}

// writeRunLock writes scope's run lock as a worker would have, its pid
// and pid_start as given.
func writeRunLock(t *testing.T, p *apexv1.Project, l *apexv1.RunLock) {
	t.Helper()
	require.NoError(t, os.MkdirAll(p.RunsDir(), 0o755))
	data, err := json.Marshal(l)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(p.RunLockPath(l.Scope), data, 0o644))
}

// startProcess starts argv (env added) and reaps it when it ends; the
// test's cleanup kills it.
func startProcess(t *testing.T, env []string, argv ...string) *exec.Cmd {
	t.Helper()
	c := exec.Command(argv[0], argv[1:]...)
	c.Env = append(os.Environ(), env...)
	require.NoError(t, c.Start())
	done := make(chan struct{})
	go func() { _ = c.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = c.Process.Kill()
		<-done
	})
	return c
}

// alive reports whether pid has not exited (a zombie the reaper has not
// collected yet counts as exited: its /proc state is Z).
func alive(pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return syscall.Kill(pid, 0) == nil
	}
	f := strings.Fields(string(data))
	return len(f) > 2 && f[2] != "Z"
}

// V15-2D-STOP-1: a lock whose pid is not the run's worker is never
// signalled — another program with that pid (pid_start unrecorded, so only
// its command line tells), or a pid whose start time is not the lock's
// (reused) — and ape status does not list a stale lock.
func TestRunStop_NotTheWorkerIsRefused(t *testing.T) {
	root := v1Repo(t)
	p, err := apexv1.Open(root)
	require.NoError(t, err)
	stranger := startProcess(t, nil, "sleep", "60")
	pid := stranger.Process.Pid

	writeRunLock(t, p, &apexv1.RunLock{ID: "20261115-000000-1-abcdef", PID: pid, Verb: "milestone gate", Scope: "M1", Started: "2026-11-15T00:00:00Z", Log: "_apex/state/runs/M1.x.log"})
	stdout, stderr, err := v1Exec(t, newRunCmd, "stop", "M1", "--timeout", "2s", "--cwd", root)
	require.Equal(t, exitCodeRunLost, exitCodeOf(t, err), stdout+stderr)
	require.Contains(t, stderr, "is not the worker of `ape milestone gate M1`")
	require.Contains(t, stderr, "nothing was signalled")
	time.Sleep(100 * time.Millisecond)
	require.True(t, alive(pid), "the stranger was not signalled")
	require.NotNil(t, p.LoadRunLock("M1"), "a live pid's lock is not removed")

	// The same pid with another start time: the lock's process is gone.
	writeRunLock(t, p, &apexv1.RunLock{ID: "20261115-000000-1-abcdef", PID: pid, PIDStart: "1", Verb: "milestone gate", Scope: "M1", Started: "2026-11-15T00:00:00Z", Log: "_apex/state/runs/M1.x.log"})
	require.Empty(t, statusRuns(t, root), "a reused pid's lock is not a live run")
	stdout, _, err = v1Exec(t, newRunCmd, "status", "M1", "--json", "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, stdout, `"state": "lost"`)
	require.Contains(t, stdout, `"alive": false`)
	_, stderr, err = v1Exec(t, newRunCmd, "stop", "M1", "--timeout", "2s", "--cwd", root)
	require.Equal(t, exitCodeRunLost, exitCodeOf(t, err), stderr)
	require.Contains(t, stderr, "nothing was signalled")
	require.Contains(t, stderr, "ended without a result")
	require.True(t, alive(pid), "the stranger was not signalled")
	require.Nil(t, p.LoadRunLock("M1"), "the stale lock is removed")

	// A process whose command line is the worker's but whose start time
	// is not the lock's: the lock's worker is gone, whatever runs now.
	look := fakeCloseWorker(t, p, "20261115-000000-1-cccccc", "")
	l := p.LoadRunLock("M1")
	l.PIDStart = "1"
	writeRunLock(t, p, l)
	_, stderr, err = v1Exec(t, newRunCmd, "stop", "M1", "--timeout", "2s", "--cwd", root)
	require.Equal(t, exitCodeRunLost, exitCodeOf(t, err), stderr)
	require.Contains(t, stderr, "nothing was signalled")
	require.True(t, alive(look), "a pid_start that differs is not the worker")
	require.Nil(t, p.LoadRunLock("M1"), "the stale lock is removed")

	// No lock: the result its last run left (v1Repo's approve).
	stdout, _, err = v1Exec(t, newRunCmd, "stop", "M1", "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, stdout, "the run of `ape milestone approve M1` had already ended with exit 0")
	// No lock, no result, no handle: a usage error.
	_, _, err = v1Exec(t, newRunCmd, "stop", "M7", "--cwd", root)
	require.Equal(t, ExitUsage, exitCodeOf(t, err))
	_, _, err = v1Exec(t, newRunCmd, "stop", "../M1", "--cwd", root)
	require.Equal(t, ExitUsage, exitCodeOf(t, err))
}

// fakeCloseWorker starts a stand-in for scope M1's `milestone close`
// worker: its command line is a worker's (`… __run <id> milestone close
// M1`), and on SIGTERM it removes its lock and exits without a result.
func fakeCloseWorker(t *testing.T, p *apexv1.Project, id, logLine string) int {
	t.Helper()
	script := filepath.Join(t.TempDir(), "worker.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nsleep 60 &\nkid=$!\ntrap 'kill $kid; rm -f \"$LOCK\"; exit 0' TERM\ntouch \"$READY\"\nwait\n"), 0o755))
	ready := script + ".ready"
	c := startProcess(t, []string{"LOCK=" + p.RunLockPath("M1"), "READY=" + ready}, "sh", script, "__run", id, "milestone", "close", "M1")
	log := "_apex/state/runs/M1." + id + ".log"
	require.NoError(t, os.MkdirAll(p.RunsDir(), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(p.Root, log), []byte("[ape] started\n"+logLine), 0o644))
	writeRunLock(t, p, &apexv1.RunLock{ID: id, PID: c.Process.Pid, Verb: verbMilestoneClose, Scope: "M1", Started: "2026-11-15T00:00:00Z", Log: log})
	// The shell has set its trap.
	require.Eventually(t, func() bool { _, err := os.Stat(ready); return err == nil }, 5*time.Second, 20*time.Millisecond)
	return c.Process.Pid
}

// V15-2D-STOP-1: a close whose log has a fence line is refused without
// --force — nothing signalled — and names what may be left; --force stops
// it. A close before its fence stops without --force.
func TestRunStop_ClosePastFence(t *testing.T) {
	root := v1Repo(t)
	p, err := apexv1.Open(root)
	require.NoError(t, err)

	pid := fakeCloseWorker(t, p, "20261115-000000-1-aaaaaa",
		"2026-11-15T00:00:01Z ape: fence: close PR: enqueueing under close.demo, held by this checkout (token 3), last renewed 2026-11-15T00:00:00Z\n")
	stdout, stderr, err := v1Exec(t, newRunCmd, "stop", "M1", "--timeout", "5s", "--cwd", root)
	require.Equal(t, exitV1Conflict, exitCodeOf(t, err), stdout+stderr)
	require.Contains(t, stderr, "`ape milestone close M1` is past its fence")
	require.Contains(t, stderr, "close PR into main may be open and queued in main's merge queue")
	require.Contains(t, stderr, "--force")
	require.Contains(t, stderr, "Nothing was signalled")
	require.True(t, alive(pid), "not signalled")

	stdout, stderr, err = v1Exec(t, newRunCmd, "stop", "M1", "--force", "--timeout", "5s", "--cwd", root)
	require.Equal(t, exitCodeRunLost, exitCodeOf(t, err), stdout+stderr)
	require.Contains(t, stderr, "ended without a result")
	require.Eventually(t, func() bool { return !alive(pid) }, 5*time.Second, 20*time.Millisecond, "stopped")
	require.Nil(t, p.LoadRunLock("M1"))

	// Before its fence: no --force needed.
	pid = fakeCloseWorker(t, p, "20261115-000000-1-bbbbbb", "")
	stdout, stderr, err = v1Exec(t, newRunCmd, "stop", "M1", "--timeout", "5s", "--cwd", root)
	require.Equal(t, exitCodeRunLost, exitCodeOf(t, err), stdout+stderr)
	require.Eventually(t, func() bool { return !alive(pid) }, 5*time.Second, 20*time.Millisecond, "stopped")
}

// 2D-STOP-R1 (research's probe): the scope's lock was removed as stale
// between longRunScope's look and runStopLong (another stop, or the verb's
// next call), and no result was ever recorded (a worker killed hard).
// runStopLong read the missing result's verb and panicked; it says the run
// is gone with no result, exit 76, and signals nothing.
func TestRunStop_NeitherLockNorResultIsLost(t *testing.T) {
	root, _, _ := gateRecallRepo(t)
	p, err := apexv1.Open(root)
	require.NoError(t, err)
	require.Nil(t, p.LoadRunLock("take-in-M1"))
	require.Nil(t, p.LoadRunResult("take-in-M1"))
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var errOut strings.Builder
	cmd.SetErr(&errOut)
	var perr any
	func() {
		defer func() { perr = recover() }()
		err = runStopLong(cmd, p, "take-in-M1", time.Second, false)
	}()
	require.Nil(t, perr, "ape run stop must not panic")
	require.Equal(t, exitCodeRunLost, exitCodeOf(t, err))
	require.Contains(t, errOut.String(), "gone without recording a result")
}

// 2D-STOP-R2 (research's probe): the close writes its first fence line
// while ape run stop looks at its worker. The fence is read again just
// before the signal, so the stop refuses, exit 3, and signals nothing.
func TestRunStop_AFenceWrittenDuringTheLookIsRefused(t *testing.T) {
	root := v1Repo(t)
	p, err := apexv1.Open(root)
	require.NoError(t, err)
	pid := fakeCloseWorker(t, p, "20261115-000000-1-cccccc", "")
	was := probeWorkerFn
	probeWorkerFn = func(ctx context.Context, l *apexv1.RunLock) supervisorCheck {
		f, err := os.OpenFile(filepath.Join(p.Root, l.Log), os.O_APPEND|os.O_WRONLY, 0o644)
		require.NoError(t, err)
		_, _ = f.WriteString("2026-11-15T00:00:02Z ape: fence: milestone/M1: pushing under milestone.demo.M1, held by this checkout (token 3), last renewed 2026-11-15T00:00:00Z\n")
		_ = f.Close()
		return was(ctx, l)
	}
	t.Cleanup(func() { probeWorkerFn = was })
	stdout, stderr, err := v1Exec(t, newRunCmd, "stop", "M1", "--timeout", "5s", "--cwd", root)
	require.Equal(t, exitV1Conflict, exitCodeOf(t, err), stdout+stderr)
	require.Contains(t, stderr, "is past its fence")
	time.Sleep(200 * time.Millisecond)
	require.True(t, alive(pid), "not signalled")
}
