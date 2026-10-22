//go:build unix

package apecmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/stretchr/testify/require"
)

// detachedRepo is v1Repo with add-wave started and its T1 committed as
// script: the long verbs run in a detached worker, this test binary acting
// as ape for it.
func detachedRepo(t *testing.T, script string) (string, *apexv1.Project) {
	t.Helper()
	root := v1Repo(t)
	old := v1DetachDefault
	v1DetachDefault = true
	t.Setenv(asApeBinary, "1")
	t.Cleanup(func() { v1DetachDefault = old })
	_, _, err := v1Exec(t, newChangeCmd, "new", "add-wave", "--milestone", "M1", "--touches", "greeting", "--cwd", root)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, filepath.FromSlash("development/changes/add-wave/change.md")), []byte(v1ChangeMD), 0o644))
	v1Git(t, root, "add", "-A")
	v1Git(t, root, "commit", "-q", "-m", "plan")

	// Detached, and done well within the wait: printed as always.
	stdout, _, err := v1Exec(t, newChangeCmd, "start", "add-wave", "--json", "--wait", "60s", "--cwd", root)
	require.NoError(t, err, stdout)
	var started apexv1.StartResult
	require.NoError(t, json.Unmarshal([]byte(stdout), &started), stdout)
	require.Equal(t, apexv1.StatusInProgress, started.Status)

	require.NoError(t, os.WriteFile(filepath.Join(root, "wave.sh"), []byte("#!/bin/sh\n"+script+"\n"), 0o755))
	v1Git(t, root, "add", "wave.sh")
	v1Git(t, root, "commit", "-q", "-m", "feat: wave", "-m", "Change: add-wave")
	p, err := apexv1.Open(root)
	require.NoError(t, err)
	t.Cleanup(func() {
		// No worker outlives its test.
		if l := p.LoadRunLock("add-wave"); l != nil && l.PID > 0 && l.PID != os.Getpid() {
			_ = syscall.Kill(l.PID, syscall.SIGKILL)
		}
		// Nor one that has released its lock but not exited yet, nor a
		// check of it: a process still in the repo when t.TempDir removes
		// it can write into it (D380: "_apex/state: directory not empty").
		awaitNoProcessUnder(t, root, 10*time.Second)
	})
	return root, p
}

// awaitNoProcessUnder waits up to d for every process whose working
// directory is under root to exit, then SIGKILLs the rest (Linux: it reads
// /proc/*/cwd; elsewhere it does nothing).
func awaitNoProcessUnder(t *testing.T, root string, d time.Duration) {
	t.Helper()
	if runtime.GOOS != "linux" {
		return
	}
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	under := func() []int {
		var pids []int
		entries, _ := os.ReadDir("/proc")
		for _, e := range entries {
			pid, err := strconv.Atoi(e.Name())
			if err != nil || pid == os.Getpid() {
				continue
			}
			cwd, err := os.Readlink("/proc/" + e.Name() + "/cwd")
			if err == nil && (cwd == root || strings.HasPrefix(cwd, root+string(filepath.Separator))) {
				pids = append(pids, pid)
			}
		}
		return pids
	}
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if len(under()) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, pid := range under() {
		t.Logf("pid %d still in %s after %s: killed", pid, root, d)
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// awaitExit waits for the process pid to be gone (reaped).
func awaitExit(t *testing.T, pid int) {
	t.Helper()
	require.Eventually(t, func() bool { return syscall.Kill(pid, 0) == syscall.ESRCH },
		10*time.Second, 20*time.Millisecond, "worker pid %d did not exit", pid)
}

//nolint:tagliatelle // the v1 --json payloads are snake_case by contract
type runningJSON struct {
	Running bool     `json:"running"`
	Verb    string   `json:"verb"`
	Scope   string   `json:"scope"`
	Since   string   `json:"since"`
	Log     string   `json:"log"`
	PID     int      `json:"pid"`
	LogTail []string `json:"log_tail"`
}

// D58 2.3: a call that outlives --wait exits 75 and leaves the worker
// running; the same command again attaches (never a second run) and prints
// the result once the run ends. Another verb on the change meanwhile is
// run.busy.
func TestLongRun_Exit75ThenAttach(t *testing.T) {
	root, p := detachedRepo(t, "sleep 3\necho wave")

	stdout, _, err := v1Exec(t, newChangeCmd, "verify", "add-wave", "--json", "--wait", "0", "--cwd", root)
	require.Equal(t, exitCodeStillRunning, exitCodeOf(t, err), stdout)
	var running runningJSON
	require.NoError(t, json.Unmarshal([]byte(stdout), &running), stdout)
	require.True(t, running.Running)
	require.Equal(t, "change verify", running.Verb)
	require.Equal(t, "add-wave", running.Scope)
	require.NotEmpty(t, running.Since)
	require.Equal(t, "_apex/state/runs/add-wave.log", running.Log)
	lock := p.LoadRunLock("add-wave")
	require.NotNil(t, lock)
	require.NotEqual(t, os.Getpid(), lock.PID, "the checks run in the worker, not in the call")

	stdout, stderr, err := v1Exec(t, newChangeCmd, "verify", "add-wave", "--wait", "0", "--cwd", root)
	require.Equal(t, exitCodeStillRunning, exitCodeOf(t, err))
	require.Contains(t, stdout, "`ape change verify add-wave` is still running since")
	require.Contains(t, stdout, "run the same command again to attach")
	require.Equal(t, lock.ID, p.LoadRunLock("add-wave").ID, "it attached: no second run")
	_ = stderr

	stdout, _, err = v1Exec(t, newChangeCmd, "archive", "add-wave", "--json", "--cwd", root)
	require.Equal(t, exitV1Conflict, exitCodeOf(t, err))
	require.Contains(t, stdout, `"code": "run.busy"`)
	require.Contains(t, stdout, `"action": "retry"`)
	require.Contains(t, stdout, "`ape change verify add-wave` is running on it since "+lock.Started)

	stdout, _, err = v1Exec(t, newChangeCmd, "verify", "add-wave", "--json", "--wait", "60s", "--cwd", root)
	require.NoError(t, err, stdout)
	var res apexv1.VerifyResult
	require.NoError(t, json.Unmarshal([]byte(stdout), &res), stdout)
	require.True(t, res.Green)
	require.Equal(t, apexv1.StatusVerified, res.Status)
	require.Nil(t, p.LoadRunLock("add-wave"), "the lock is released")
	require.Equal(t, "apex: add-wave verified", v1Git(t, root, "log", "-1", "--format=%s"), "the worker recorded the verify")
	awaitExit(t, lock.PID)

	// A --wait past the Bash cap is a usage error.
	_, _, err = v1Exec(t, newChangeCmd, "verify", "add-wave", "--wait", "10m", "--cwd", root)
	require.Equal(t, ExitUsage, exitCodeOf(t, err))
}

// A run that ended after its call stopped waiting is printed by the next
// call of its verb — not run again.
func TestLongRun_UnprintedResultIsDelivered(t *testing.T) {
	root, p := detachedRepo(t, "sleep 1\necho wave")
	_, _, err := v1Exec(t, newChangeCmd, "verify", "add-wave", "--wait", "0", "--cwd", root)
	require.Equal(t, exitCodeStillRunning, exitCodeOf(t, err))
	lock := p.LoadRunLock("add-wave")
	require.NotNil(t, lock)
	id, worker := lock.ID, lock.PID
	require.NotEqual(t, os.Getpid(), worker)
	require.Eventually(t, func() bool { return p.LoadRunLock("add-wave") == nil }, 30*time.Second, 50*time.Millisecond)
	// The lock is released before the worker exits: wait for the exit too.
	awaitExit(t, worker)
	commits := v1Git(t, root, "rev-list", "--count", "HEAD")

	stdout, _, err := v1Exec(t, newChangeCmd, "verify", "add-wave", "--wait", "0", "--cwd", root)
	require.NoError(t, err, stdout)
	require.Contains(t, stdout, "change add-wave at")
	require.Contains(t, stdout, "verified")
	r := p.LoadRunResult("add-wave")
	require.Equal(t, id, r.ID, "the recorded run, not a new one")
	require.True(t, r.Reported)
	require.Equal(t, commits, v1Git(t, root, "rev-list", "--count", "HEAD"))
}

// D58 2.2: SIGTERM to the worker mid-check records nothing — no attempt,
// no state commit — and the next call reports run.interrupted.
func TestLongRun_SIGTERMIsInterruptedNotRed(t *testing.T) {
	root, p := detachedRepo(t, "sleep 30\nexit 1")
	head := v1Git(t, root, "rev-parse", "HEAD")
	_, _, err := v1Exec(t, newChangeCmd, "verify", "add-wave", "--wait", "0", "--cwd", root)
	require.Equal(t, exitCodeStillRunning, exitCodeOf(t, err))
	lock := p.LoadRunLock("add-wave")
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(lock.Log)))
		return err == nil && strings.Contains(string(data), "T1 started")
	}, 30*time.Second, 50*time.Millisecond, "the check is running")
	require.NoError(t, syscall.Kill(p.LoadRunLock("add-wave").PID, syscall.SIGTERM))

	stdout, _, err := v1Exec(t, newChangeCmd, "verify", "add-wave", "--json", "--wait", "30s", "--cwd", root)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err), stdout)
	require.Contains(t, stdout, `"code": "run.interrupted"`)
	require.Contains(t, stdout, `"action": "retry"`)
	require.Equal(t, head, v1Git(t, root, "rev-parse", "HEAD"), "no state commit")
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash("_apex/state/attempts.json")))
	if err == nil {
		require.NotContains(t, string(data), `"since": 1`, "no attempt counted")
	}
	require.Nil(t, p.LoadRunLock("add-wave"))
}

// D58 2.2: the worker SIGKILLed mid-check (nothing of ape's runs) leaves no
// orphaned check, and the next call reports the stale lock as
// run.interrupted — never an attempt.
func TestLongRun_SIGKILLLeavesNoOrphan(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux: Pdeathsig and the watchdog")
	}
	pidFile := filepath.Join(t.TempDir(), "pgid")
	root, p := detachedRepo(t, "echo $$ > "+pidFile+"\nsleep 300 &\nsleep 300")
	_, _, err := v1Exec(t, newChangeCmd, "verify", "add-wave", "--wait", "0", "--cwd", root)
	require.Equal(t, exitCodeStillRunning, exitCodeOf(t, err))
	var pgid int
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		pgid, err = strconv.Atoi(strings.TrimSpace(string(data)))
		return err == nil && pgid > 0
	}, 30*time.Second, 50*time.Millisecond)
	lock := p.LoadRunLock("add-wave")
	require.NoError(t, syscall.Kill(lock.PID, syscall.SIGKILL))
	require.Eventually(t, func() bool { return syscall.Kill(-pgid, 0) == syscall.ESRCH },
		10*time.Second, 50*time.Millisecond, "the check outlived its worker")

	require.Eventually(t, func() bool { return !lock.Alive() }, 10*time.Second, 20*time.Millisecond)
	stdout, _, err := v1Exec(t, newChangeCmd, "verify", "add-wave", "--json", "--wait", "0", "--cwd", root)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err), stdout)
	require.Contains(t, stdout, `"code": "run.interrupted"`)
	require.Contains(t, stdout, "ended without a result")
	require.Nil(t, p.LoadRunLock("add-wave"), "the stale lock is removed")
}

// endedProbe is a worker's stderr (its run log) that records, when the
// worker writes its "ended" line, whether the run's lock and result are
// there.
type endedProbe struct {
	p                         *apexv1.Project
	buf                       strings.Builder
	seen, lockHeld, hasResult bool
}

func (e *endedProbe) Write(b []byte) (int, error) {
	if !e.seen && strings.Contains(string(b), "] ") && strings.Contains(string(b), " ended, exit ") {
		e.seen = true
		e.lockHeld = e.p.LoadRunLock("add-wave") != nil
		e.hasResult = e.p.LoadRunResult("add-wave") != nil
	}
	return e.buf.Write(b)
}

// D380: the worker writes "ended, exit N — result:" to its log before it
// releases the run lock, so a call that sees the run end reads a log that
// already says so — and the result is recorded by then.
func TestLongRun_EndedLineBeforeLockRelease(t *testing.T) {
	root, p := detachedRepo(t, "echo wave")

	// Detached: the log ends with the ended line by the time the call
	// that waited for the run returns.
	_, _, err := v1Exec(t, newChangeCmd, "verify", "add-wave", "--json", "--wait", "60s", "--cwd", root)
	require.NoError(t, err)
	data, err := os.ReadFile(p.RunLogPath("add-wave"))
	require.NoError(t, err)
	require.Contains(t, string(data), " ended, exit 0 — result:")

	// The worker, in this process, on a lock taken as a call takes it:
	// what the log reader sees is the order, not a timing.
	lock, held, err := p.TakeRunLock(t.Context(), "change verify", "add-wave")
	require.NoError(t, err)
	require.Nil(t, held)
	probe := &endedProbe{p: p}
	cmd := newRunWorkerCmd()
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(probe)
	cmd.SetArgs([]string{lock.ID, "change", "verify", "add-wave", "--cwd", root})
	require.NoError(t, cmd.Execute(), probe.buf.String())
	require.True(t, probe.seen, "the worker wrote its ended line: %s", probe.buf.String())
	require.True(t, probe.lockHeld, "the ended line is written while the lock is held")
	require.True(t, probe.hasResult, "and after the result is recorded")
	require.Nil(t, p.LoadRunLock("add-wave"), "then the lock is released")
}
