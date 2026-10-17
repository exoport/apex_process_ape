package apexv1

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// deadPID is the pid of a process that has ended.
func deadPID(t *testing.T) int {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX `true`")
	}
	c := exec.Command("true")
	require.NoError(t, c.Run())
	return c.Process.Pid
}

// D58 2.1: one run per scope. A held lock is returned to the caller (who
// attaches, or refuses another verb with run.busy); a stale one is removed
// and reported as run.interrupted — unless its run recorded a result.
func TestRunLock_AttachBusyStale(t *testing.T) {
	p := fixture(t, baseFiles())
	lock, held, err := p.TakeRunLock(ctx(), "change verify", "add-wave")
	require.NoError(t, err)
	require.Nil(t, held)
	require.Equal(t, os.Getpid(), lock.PID)
	require.Equal(t, "change verify", lock.Verb)
	require.Equal(t, "_apex/state/runs/add-wave.log", lock.Log)
	require.Equal(t, HeadSHA(ctx(), p.Root), lock.SHA)
	require.True(t, AnyRunLive(p.Root), "a live lock is work in progress")

	_, held, err = p.TakeRunLock(ctx(), "change archive", "add-wave")
	require.NoError(t, err)
	require.NotNil(t, held, "a living run holds it")
	require.Equal(t, lock.ID, held.ID)
	busy := RunBusy(held)
	var ce *ConflictError
	require.True(t, errors.As(busy, &ce))
	require.Equal(t, CodeRunBusy, ce.Code)
	require.Equal(t, ActionRetry, ActionFor(ce.Code))
	require.Contains(t, ce.Msg, "`ape change verify add-wave` is running on it since "+lock.Started)

	other, held, err := p.TakeRunLock(ctx(), "milestone gate", "M1")
	require.NoError(t, err, "another scope is free")
	require.Nil(t, held)
	p.ReleaseRunLock(other)

	// Stale: its process is gone, with no result.
	lock.PID = deadPID(t)
	require.True(t, p.UpdateRunLock(lock))
	require.False(t, AnyRunLive(p.Root))
	_, _, err = p.TakeRunLock(ctx(), "change verify", "add-wave")
	requireCode(t, err, CodeRunInterrupted)
	require.Contains(t, err.Error(), "ended without a result")
	require.Nil(t, p.LoadRunLock("add-wave"), "removed")

	// Stale, but its run recorded a result: it ended fine; the scope is free.
	lock2, _, err := p.TakeRunLock(ctx(), "change verify", "add-wave")
	require.NoError(t, err)
	require.NoError(t, p.SaveRunResult(&RunResult{ID: lock2.ID, Verb: lock2.Verb, Scope: lock2.Scope}))
	lock2.PID = deadPID(t)
	require.True(t, p.UpdateRunLock(lock2))
	lock3, held, err := p.TakeRunLock(ctx(), "change verify", "add-wave")
	require.NoError(t, err)
	require.Nil(t, held)
	require.NotEqual(t, lock2.ID, lock3.ID)

	p.ReleaseRunLock(lock2) // not the lock's run any more: a no-op
	require.NotNil(t, p.LoadRunLock("add-wave"))
	p.ReleaseRunLock(lock3)
	require.Nil(t, p.LoadRunLock("add-wave"))
	clean(t, p, "run locks (ignored)")
}

// The lock file is whole the moment it exists, and only ape's fields.
func TestRunLock_FileShape(t *testing.T) {
	p := fixture(t, baseFiles())
	lock, _, err := p.TakeRunLock(ctx(), "milestone close", "M1")
	require.NoError(t, err)
	data, err := os.ReadFile(p.RunLockPath("M1"))
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m))
	for _, k := range []string{"id", "pid", "verb", "scope", "sha", "started", "log"} {
		require.Contains(t, m, k)
	}
	require.Equal(t, "M1", m["scope"])
	p.ReleaseRunLock(lock)
}

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	require.Error(t, err)
	switch e := err.(type) {
	case *FailError:
		require.Equal(t, code, e.Code)
	case *ConflictError:
		require.Equal(t, code, e.Code)
	case *UsageError:
		require.Equal(t, code, e.Code)
	default:
		t.Fatalf("%T %v", err, err)
	}
}
