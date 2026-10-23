//go:build linux

package proctree

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// helperEnv makes this test binary a stand-in for an `ape __run` worker
// (TestMain): started as `<test binary> __run …`, in its own session, it
// backgrounds a sleep — re-parented to init once its shell exits, but
// still in the worker's session — writes its pid to the file named by
// helperPIDFile, and waits.
const (
	helperEnv     = "PROCTREE_TEST_HELPER"
	helperPIDFile = "PROCTREE_TEST_PIDFILE"
)

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "worker" {
		sh := exec.Command("sh", "-c", `sleep 600 & echo $! > "$`+helperPIDFile+`"`) //nolint:noctx // test helper
		_ = sh.Run()
		time.Sleep(10 * time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func testMarker(t *testing.T) string {
	t.Helper()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "PROCTREE_TEST_SESSION=task/" + hex.EncodeToString(b)
}

// orphan starts `( setsid sleep 600 & )` with env — the sleep re-parented
// once the shell exits, in a session of its own, as a server a model's Bash
// call backgrounded is — and returns its pid. It is killed when the test
// ends, whatever the test did.
func orphan(t *testing.T, env ...string) int {
	t.Helper()
	pidf := filepath.Join(t.TempDir(), "pid")
	sh := exec.Command("sh", "-c", `( setsid sleep 600 & echo $! > "$PIDF" )`) //nolint:noctx // test
	sh.Env = append(append(os.Environ(), "PIDF="+pidf), env...)
	require.NoError(t, sh.Run())
	pid := readPID(t, pidf)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	return pid
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	var pid int
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
		return err == nil && pid > 0
	}, 5*time.Second, 10*time.Millisecond)
	return pid
}

func pidsOf(list []Leftover) []int {
	out := make([]int, 0, len(list))
	for _, l := range list {
		out = append(out, l.PID)
	}
	return out
}

func gone(pid int) bool {
	p, ok := readProc(pid, time.Time{})
	return !ok || p.state == 'Z'
}

func TestLeftovers_FindsReparentedMarkedProcess(t *testing.T) {
	marker := testMarker(t)
	pid := orphan(t, marker)
	pp, ok := Parent(pid)
	require.True(t, ok)
	require.NotEqual(t, os.Getpid(), pp, "the sleep is no child of this process")

	reported := SessionLeftovers(t.Context(), marker, LeftoversReport, LeftoversGrace)
	require.Equal(t, []int{pid}, pidsOf(reported))
	require.Equal(t, LeftoverKept, reported[0].Action)
	require.Equal(t, "sleep 600", reported[0].Command)
	require.WithinDuration(t, time.Now(), reported[0].Started, time.Minute)
	require.False(t, gone(pid), "report leaves it running")

	killed := SessionLeftovers(t.Context(), marker, LeftoversKill, LeftoversGrace)
	require.Equal(t, []int{pid}, pidsOf(killed))
	require.Equal(t, LeftoverKilled, killed[0].Action)
	require.True(t, gone(pid))

	require.Nil(t, SessionLeftovers(t.Context(), marker, LeftoversKill, LeftoversGrace), "nothing left")
}

func TestLeftovers_SkipsADifferentRunsMarker(t *testing.T) {
	marker := testMarker(t)
	other := orphan(t, marker+"0") // a prefix match is not the entry
	another := orphan(t, strings.Replace(marker, "task/", "prompt/", 1))
	require.Nil(t, SessionLeftovers(t.Context(), marker, LeftoversKill, LeftoversGrace))
	require.False(t, gone(other))
	require.False(t, gone(another))
}

func TestLeftovers_SkipsRunWorkerSession(t *testing.T) {
	marker := testMarker(t)
	pidf := filepath.Join(t.TempDir(), "pid")
	w := exec.Command(os.Args[0], "__run", "r1", "milestone", "gate", "M1") //nolint:noctx,gosec // this test binary as a worker
	w.Env = append(os.Environ(), helperEnv+"=worker", helperPIDFile+"="+pidf, marker)
	w.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	require.NoError(t, w.Start())
	t.Cleanup(func() {
		_ = syscall.Kill(-w.Process.Pid, syscall.SIGKILL)
		_ = w.Wait()
	})
	sleep := readPID(t, pidf)
	t.Cleanup(func() { _ = syscall.Kill(sleep, syscall.SIGKILL) })
	require.Eventually(t, func() bool { pp, ok := Parent(sleep); return ok && pp != w.Process.Pid }, 5*time.Second, 10*time.Millisecond,
		"the worker's sleep is re-parented, out of its tree")

	// And a real leftover beside them, so the scan is seen to run.
	left := orphan(t, marker)
	got := SessionLeftovers(t.Context(), marker, LeftoversKill, LeftoversGrace)
	require.Equal(t, []int{left}, pidsOf(got), "the worker and its session are ape's own")
	require.False(t, gone(w.Process.Pid))
	require.False(t, gone(sleep))
}
