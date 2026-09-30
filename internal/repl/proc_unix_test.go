//go:build linux || darwin

package repl

import (
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

// A command claude's Bash tool starts runs outside claude's process group.
// This stands in for it: a child in its own session, under a parent in
// another. Stopping the parent must stop the grandchild too.
func TestTerminateGroup_StopsDescendantsOutsideTheGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	cmd := exec.Command("sh", "-c", "setsid sh -c 'echo $$ > "+pidFile+"; exec sleep 300' & wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	var grandchild int
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		grandchild, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil
	}, 5*time.Second, 20*time.Millisecond)
	t.Cleanup(func() { _ = syscall.Kill(grandchild, syscall.SIGKILL) })

	gpgid, err := syscall.Getpgid(grandchild)
	require.NoError(t, err)
	require.NotEqual(t, cmd.Process.Pid, gpgid, "the fixture's grandchild must be outside the group, or this proves nothing")

	terminateGroup(t.Context(), cmd.Process.Pid)
	_, _ = cmd.Process.Wait()
	require.Eventually(t, func() bool {
		return syscall.Kill(grandchild, 0) != nil
	}, 2*time.Second, 20*time.Millisecond, "the grandchild outside the group outlived terminateGroup")
}
