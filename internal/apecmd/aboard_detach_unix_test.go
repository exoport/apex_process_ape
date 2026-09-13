//go:build unix

package apecmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/exoport/aboard/pkg/aboard"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// asApeBinary turns this test binary into ape for a subprocess, so a detached
// board start — which re-runs os.Executable() — can be tested without a build
// step. The child goes through Execute and ExitCode exactly as cmd/ape does.
const asApeBinary = "APE_APECMD_TEST_AS_BINARY"

func TestMain(m *testing.M) {
	if os.Getenv(asApeBinary) == "1" {
		if err := Execute(); err != nil {
			code, silent := ExitCode(err)
			if !silent {
				_, _ = os.Stderr.WriteString("Error: " + err.Error() + "\n")
			}
			os.Exit(code)
		}
		os.Exit(ExitOK)
	}
	os.Exit(m.Run())
}

// apeProcess runs this binary as `ape <args>` and returns what it printed.
func apeProcess(t *testing.T, args ...string) (out string, code int) {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), asApeBinary+"=1")
	body, err := cmd.CombinedOutput()
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		code = exitErr.ExitCode()
	} else {
		require.NoError(t, err, "running ape %v", args)
	}
	return string(body), code
}

// `serve --detach` (aboard v0.2.0) starts the board by running the command
// AGAIN: os.Executable() with the cobra command path minus the root's name, so
// under ape the child is `ape aboard serve …`. aboard tests that mechanism
// against its own binary and cannot test it against this one — whether ape's
// binary reaches the tree at the path it was mounted at is a property of ape.
// Move the mount, or put anything between main and the argument dispatch, and
// the detached child cannot find `serve`.
//
// So this is run, not inspected: the child must come up as the ape host, in a
// session of its own, and a second start must be refused. The shutdown is
// asserted too, because it is ape's signal context (root.go) — not aboard's —
// that lets a detached ape-hosted board remove its record when it is stopped.
func TestAboardServeDetachReRunsApe(t *testing.T) {
	dir := t.TempDir()
	out, code := apeProcess(t, "aboard", "--cwd="+dir, "init")
	require.Zero(t, code, "init:\n%s", out)

	out, code = apeProcess(t, "aboard", "--cwd="+dir, "serve", "--detach")
	require.Zero(t, code, "serve --detach:\n%s", out)
	require.Contains(t, out, "detached")

	root, err := aboard.FindRoot(dir)
	require.NoError(t, err)
	body, err := os.ReadFile(root.InstanceFile(""))
	require.NoError(t, err, "no instance record after a detached start")
	var inst aboard.Instance
	require.NoError(t, json.Unmarshal(body, &inst))

	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_ = syscall.Kill(inst.PID, syscall.SIGTERM)
		}
	})

	live := aboard.ProbeBoard(context.Background(), inst.Port, inst.Base)
	require.NotNil(t, live, "the detached board does not answer on port %d", inst.Port)
	require.Equal(t, inst.PID, live.PID)
	require.Equal(t, aboard.HostApe, live.App, "the detached child must be ape, not a standalone aboard")
	require.Equal(t, aboardArgv0, live.Argv0)

	sid, err := unix.Getsid(inst.PID)
	require.NoError(t, err)
	require.Equal(t, inst.PID, sid, "the detached board must lead a session of its own")

	again, code := apeProcess(t, "aboard", "--cwd="+dir, "serve", "--detach")
	require.NotZero(t, code, "a second serve --detach in the same project succeeded:\n%s", again)
	require.Contains(t, again, "already running")

	require.NoError(t, syscall.Kill(inst.PID, syscall.SIGTERM))
	stopped = true
	require.Eventually(t, func() bool {
		_, err := os.Stat(root.InstanceFile(""))
		return errors.Is(err, os.ErrNotExist)
	}, 10*time.Second, 50*time.Millisecond,
		"a TERM must shut the detached board down cleanly and remove %s", root.InstanceFile(""))
	require.Nil(t, aboard.ProbeBoard(context.Background(), inst.Port, inst.Base))
}
