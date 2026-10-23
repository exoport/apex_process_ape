//go:build unix

package apecmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/stretchr/testify/require"
)

// F14 (eval F-LOST1): a detached run is lost only on a definite negative
// seen twice — the supervisor's pid gone, or another program's — never on
// a look that could not tell. Under host load `ps` took longer than its
// 2-second timeout, and `ape run wait` exited 76 and wrote <id>.lost while
// the supervisor was alive and went on to finish the run.

// fastLostConfirm shortens the delay between a loss's two looks.
func fastLostConfirm(t *testing.T) {
	t.Helper()
	old := lostConfirmDelay
	lostConfirmDelay = 20 * time.Millisecond
	t.Cleanup(func() { lostConfirmDelay = old })
}

// withProbe swaps the supervisor probe for the test.
func withProbe(t *testing.T, probe func(context.Context, *detachedHandle) supervisorCheck) {
	t.Helper()
	old := probeSupervisorFn
	probeSupervisorFn = probe
	t.Cleanup(func() { probeSupervisorFn = old })
}

// countingProbe wraps probe, counting its looks.
func countingProbe(t *testing.T, probe func(context.Context, *detachedHandle) supervisorCheck) func() int {
	t.Helper()
	var mu sync.Mutex
	n := 0
	withProbe(t, func(ctx context.Context, h *detachedHandle) supervisorCheck {
		mu.Lock()
		n++
		mu.Unlock()
		return probe(ctx, h)
	})
	return func() int { mu.Lock(); defer mu.Unlock(); return n }
}

// scriptedProbe answers each look with the next of checks, then the last
// one for good.
func scriptedProbe(t *testing.T, checks ...supervisorCheck) func() int {
	t.Helper()
	var mu sync.Mutex
	i := 0
	return countingProbe(t, func(context.Context, *detachedHandle) supervisorCheck {
		mu.Lock()
		defer mu.Unlock()
		c := checks[min(i, len(checks)-1)]
		i++
		return c
	})
}

// fakeSupervisor starts a live process whose command line is what ape's
// check matches — "… run supervise --handle …/detached/<id>.json" — as the
// eval's reproduction does, and returns its pid. It ends when the test does.
func fakeSupervisor(t *testing.T, handle string) int {
	t.Helper()
	cmd := exec.Command("sh", "-c", "read x", "run supervise --handle", handle)
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd.Process.Pid
}

// stranger starts an unrelated live process: a pid the supervisor's could
// have been reused by.
func stranger(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd.Process.Pid
}

// reapedPID is the pid of a process that has ended and been reaped.
func reapedPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	require.NoError(t, cmd.Run())
	return cmd.ProcessState.Pid()
}

// psStub puts a `ps` running script first on PATH.
func psStub(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ps"), []byte("#!/bin/sh\n"+script+"\n"), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// slowPS is the eval's stub: a ps that takes longer than ape's timeout.
func slowPS(t *testing.T) {
	t.Helper()
	old := psTimeout
	psTimeout = 300 * time.Millisecond
	t.Cleanup(func() { psTimeout = old })
	psStub(t, `sleep 3; exec /bin/ps "$@" 2>/dev/null || exec /usr/bin/ps "$@"`)
}

// detachedProject is an APEX v1 project (where a loss is marked and
// logged) with a detached run's handle, id, whose supervisor is pid (0: a
// fake supervisor naming the handle).
func detachedProject(t *testing.T, id string, pid int) (root, path string) {
	t.Helper()
	root = v1Repo(t)
	require.NoError(t, os.MkdirAll(runlog.DetachedRoot(root), 0o755))
	path = handlePath(root, id)
	if pid == 0 {
		pid = fakeSupervisor(t, path)
	}
	require.NoError(t, writeHandle(path, &detachedHandle{
		ID: id, Argv: []string{"task", "apex-loop"}, Dir: root, Root: root, SupervisorPID: pid, ChildPID: pid,
		Log: filepath.Join(runlog.DetachedRoot(root), id+".log"), Stdout: filepath.Join(runlog.DetachedRoot(root), id+".out"),
		StartedAt: time.Now().UTC(), Depth: 1,
	}))
	return root, path
}

// requireNotLost: wait, status and ape status's sweep all see a live run.
func requireNotLost(t *testing.T, root, id string) {
	t.Helper()
	out, code := runRunCmd(t, "wait", id, "--cwd", root, "--wait", "2s", "--json")
	require.Equal(t, exitCodeStillRunning, code, "a live run is waited on, not lost: %s", out)
	require.Equal(t, "running", reportOf(t, out)["state"])
	out, code = runRunCmd(t, "status", id, "--cwd", root, "--json")
	require.Zero(t, code)
	require.Equal(t, "running", reportOf(t, out)["state"])
	require.Equal(t, []string{id}, detachedRuns(context.Background(), root))
	require.NoFileExists(t, lostMarkerPath(root, id))
}

// The eval's reproduction, as a test: a live supervisor and a ps slower
// than the timeout. Neither `ape run wait` nor `ape run status` reports it
// lost, on this platform's probe (Linux: /proc, which never runs ps) and on
// the ps probe macOS and the BSDs use (their slow ps an unknown).
func TestRunWait_ASlowPSIsNeverALoss(t *testing.T) {
	fastLostConfirm(t)
	slowPS(t)
	t.Run("platform probe", func(t *testing.T) {
		root, _ := detachedProject(t, "live", 0)
		requireNotLost(t, root, "live")
	})
	t.Run("ps probe", func(t *testing.T) {
		withProbe(t, func(ctx context.Context, h *detachedHandle) supervisorCheck {
			if !pidAlive(h.SupervisorPID) {
				return supervisorNotOurs
			}
			return probeSupervisorPS(ctx, h)
		})
		root, _ := detachedProject(t, "live", 0)
		requireNotLost(t, root, "live")
	})
}

// ps failing, timing out or missing is an unknown; a ps that answers
// decides.
func TestProbeSupervisorPS(t *testing.T) {
	_, path := detachedProject(t, "live", 0)
	h, err := readHandle(path)
	require.NoError(t, err)
	foreign := &detachedHandle{ID: "live", SupervisorPID: stranger(t)}

	require.Equal(t, supervisorOurs, probeSupervisorPS(context.Background(), h), "the real ps")
	require.Equal(t, supervisorNotOurs, probeSupervisorPS(context.Background(), foreign), "the real ps, a stranger")

	t.Run("slow", func(t *testing.T) {
		slowPS(t)
		start := time.Now()
		require.Equal(t, supervisorUnknown, probeSupervisorPS(context.Background(), h))
		require.Equal(t, supervisorUnknown, probeSupervisorPS(context.Background(), foreign))
		require.Less(t, time.Since(start), 2*time.Second, "bounded by the timeout, not the stub's sleep")
	})
	t.Run("failing", func(t *testing.T) {
		psStub(t, "exit 1")
		require.Equal(t, supervisorUnknown, probeSupervisorPS(context.Background(), h))
	})
	t.Run("missing", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		require.Equal(t, supervisorUnknown, probeSupervisorPS(context.Background(), h))
	})
}

// A loss needs two definite negatives: one, then "ours", is not a loss;
// an unknown is never one; a run an earlier command found lost needs one.
func TestSupervisorLost_TwoDefiniteNegatives(t *testing.T) {
	fastLostConfirm(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		checks []supervisorCheck
		lost   bool
		looks  int
	}{
		{"not ours twice", []supervisorCheck{supervisorNotOurs, supervisorNotOurs}, true, 2},
		{"not ours, then ours", []supervisorCheck{supervisorNotOurs, supervisorOurs}, false, 2},
		{"not ours, then unknown", []supervisorCheck{supervisorNotOurs, supervisorUnknown}, false, 2},
		{"unknown", []supervisorCheck{supervisorUnknown}, false, 1},
		{"ours", []supervisorCheck{supervisorOurs}, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, path := detachedProject(t, "r", 4242)
			looks := scriptedProbe(t, tc.checks...)
			h, err := readHandle(path)
			require.NoError(t, err)
			lost, cur := supervisorLost(ctx, path, h)
			require.Equal(t, tc.lost, lost)
			require.Nil(t, cur.ExitCode)
			require.Equal(t, tc.looks, looks())
			require.NoFileExists(t, lostMarkerPath(root, "r"), "supervisorLost only judges; the callers mark")
		})
	}
	t.Run("found lost before", func(t *testing.T) {
		root, path := detachedProject(t, "r", 4242)
		require.NoError(t, os.WriteFile(lostMarkerPath(root, "r"), nil, 0o644))
		looks := scriptedProbe(t, supervisorNotOurs)
		h, err := readHandle(path)
		require.NoError(t, err)
		lost, _ := supervisorLost(ctx, path, h)
		require.True(t, lost)
		require.Equal(t, 1, looks(), "the marker is the earlier sighting")
	})
	t.Run("exit recorded between the looks", func(t *testing.T) {
		_, path := detachedProject(t, "r", 4242)
		h, err := readHandle(path)
		require.NoError(t, err)
		withProbe(t, func(context.Context, *detachedHandle) supervisorCheck {
			if cur, err := readHandle(path); err == nil && cur.ExitCode == nil {
				zero := 0
				cur.ExitCode = &zero
				require.NoError(t, writeHandle(path, cur))
			}
			return supervisorNotOurs
		})
		lost, cur := supervisorLost(ctx, path, h)
		require.False(t, lost)
		require.NotNil(t, cur.ExitCode, "the recorded exit is the answer")
	})
}

// One definite negative, then the supervisor is seen as ours again (a look
// that misread): `ape run wait` keeps waiting and `ape run status` reports
// it running — no 76, no <id>.lost.
func TestRunWait_OneNegativeIsNotALoss(t *testing.T) {
	fastLostConfirm(t)
	root, _ := detachedProject(t, "r", 4242)
	scriptedProbe(t, supervisorNotOurs, supervisorOurs)
	out, code := runRunCmd(t, "wait", "r", "--cwd", root, "--wait", "1s", "--json")
	require.Equal(t, exitCodeStillRunning, code, out)
	scriptedProbe(t, supervisorNotOurs, supervisorOurs)
	out, code = runRunCmd(t, "status", "r", "--cwd", root, "--json")
	require.Zero(t, code)
	require.Equal(t, "running", reportOf(t, out)["state"])
	scriptedProbe(t, supervisorNotOurs, supervisorUnknown)
	require.Equal(t, []string{"r"}, detachedRuns(context.Background(), root))
	require.NoFileExists(t, lostMarkerPath(root, "r"))
}

// A supervisor that really died is lost on the second look: 76 and
// <id>.lost, with the platform's real probe.
func TestRunWait_ADeadSupervisorIsLostOnTheSecondLook(t *testing.T) {
	fastLostConfirm(t)
	root, _ := detachedProject(t, "dead", reapedPID(t))
	looks := countingProbe(t, probeSupervisor)
	out, code := runRunCmd(t, "wait", "dead", "--cwd", root, "--wait", "5s", "--json")
	require.Equal(t, exitCodeRunLost, code, out)
	require.Equal(t, "lost", reportOf(t, out)["state"])
	require.Equal(t, 2, looks(), "two looks, a delay apart")
	require.FileExists(t, lostMarkerPath(root, "dead"))

	out, code = runRunCmd(t, "status", "dead", "--cwd", root, "--json")
	require.Zero(t, code)
	require.Equal(t, "lost", reportOf(t, out)["state"])
	require.Equal(t, 3, looks(), "found lost before: the marker is the earlier sighting")
}

// A supervisor pid reused by an unrelated program is lost (twice seen),
// and `ape run stop` signals nothing.
func TestRunWait_AReusedPIDIsLost(t *testing.T) {
	fastLostConfirm(t)
	pid := stranger(t)
	root, _ := detachedProject(t, "reused", pid)
	looks := countingProbe(t, probeSupervisor)
	out, code := runRunCmd(t, "status", "reused", "--cwd", root, "--json")
	require.Zero(t, code)
	require.Equal(t, "lost", reportOf(t, out)["state"])
	require.Equal(t, 2, looks())
	require.FileExists(t, lostMarkerPath(root, "reused"))

	_, code = runRunCmd(t, "wait", "reused", "--cwd", root, "--wait", "5s")
	require.Equal(t, exitCodeRunLost, code)
	_, code = runRunCmd(t, "stop", "reused", "--cwd", root, "--timeout", "1s")
	require.Equal(t, exitCodeRunLost, code)
	require.NoError(t, syscall.Kill(pid, 0), "the stranger was not signalled")
}

// `ape run stop` signals only a process it has identified: a check that
// cannot tell signals nothing and exits 75.
func TestRunStop_AnUnknownSignalsNothing(t *testing.T) {
	fastLostConfirm(t)
	pid := stranger(t)
	root, _ := detachedProject(t, "r", pid)
	scriptedProbe(t, supervisorUnknown)
	_, code := runRunCmd(t, "stop", "r", "--cwd", root, "--timeout", "1s")
	require.Equal(t, exitCodeStillRunning, code)
	require.NoError(t, syscall.Kill(pid, 0), "nothing was signalled")
	require.NoFileExists(t, lostMarkerPath(root, "r"))
}

// The change verbs' session waits (review --run, tool, accept --delegate)
// keep waiting through looks that cannot tell, and settle a loss only once
// it is seen twice.
func TestAwaitHandle_UnknownKeepsWaiting(t *testing.T) {
	fastLostConfirm(t)
	_, path := detachedProject(t, "r", 4242)
	looks := scriptedProbe(t, supervisorUnknown)
	_, ended, lost := awaitHandle(context.Background(), path, 700*time.Millisecond)
	require.False(t, ended)
	require.False(t, lost)
	require.Greater(t, looks(), 1, "it kept looking")

	scriptedProbe(t, supervisorNotOurs, supervisorOurs)
	_, ended, lost = awaitHandle(context.Background(), path, 0)
	require.False(t, ended)
	require.False(t, lost, "one negative is not a loss")

	scriptedProbe(t, supervisorNotOurs)
	_, ended, lost = awaitHandle(context.Background(), path, 0)
	require.False(t, ended)
	require.True(t, lost)
}
