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
	})
	return root, p
}

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
	id := p.LoadRunLock("add-wave").ID
	require.Eventually(t, func() bool { return p.LoadRunLock("add-wave") == nil }, 30*time.Second, 50*time.Millisecond)
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
