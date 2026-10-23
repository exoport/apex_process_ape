//go:build unix

package apecmd

import (
	"encoding/json"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/stretchr/testify/require"
)

// F1 narrowed (D455), the attach case end to end with a detached worker:
// a call whose --wait runs out exits 75; the run ends before the next
// call, which gets its result once ("recorded": true) without running the
// gate; the call after that runs the gate again.
func TestGateRecall_DetachedAttachAfter75(t *testing.T) {
	root, gateRuns, _ := gateRecallRepo(t)
	require.NoError(t, os.WriteFile(gateSleepFile(root), []byte("2\n"), 0o644))
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

	stdout, _, err := v1Exec(t, newMilestoneCmd, "gate", "M1", "--json", "--wait", "0", "--cwd", root)
	require.Equal(t, exitCodeStillRunning, exitCodeOf(t, err), stdout)
	var running runningJSON
	require.NoError(t, json.Unmarshal([]byte(stdout), &running), stdout)
	require.True(t, running.Running)
	lock := p.LoadRunLock("M1")
	require.NotNil(t, lock)
	require.Eventually(t, func() bool { return p.LoadRunLock("M1") == nil }, 30*time.Second, 50*time.Millisecond)
	awaitExit(t, lock.PID)
	require.Equal(t, 2, gateRuns(), "the worker ran the gate")
	r := p.LoadRunResult("M1")
	require.NotNil(t, r)
	require.Equal(t, lock.ID, r.ID)
	require.False(t, r.Reported, "no call printed it")
	require.NotEmpty(t, r.Tree)

	exit, recorded, stderr := gateJSON(t, root, "--wait", "60s")
	require.Equal(t, ExitRunFailed, exit)
	require.True(t, recorded, "the finished run, to the call that had seen only 75")
	require.Contains(t, stderr, "not a new run")
	require.Equal(t, 2, gateRuns(), "the gate did not run again")
	require.True(t, p.LoadRunResult("M1").Reported)

	require.NoError(t, os.WriteFile(gateSleepFile(root), []byte("0\n"), 0o644))
	exit, recorded, _ = gateJSON(t, root, "--wait", "60s")
	require.Equal(t, ExitRunFailed, exit)
	require.False(t, recorded, "printed once: the next call runs the gate")
	require.Equal(t, 3, gateRuns())
}
