//go:build !windows

package apexv1

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts a check in its own process group so a timeout kills
// everything it started, not only the shell.
func setProcessGroup(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessGroup(c *exec.Cmd) error {
	if c.Process == nil {
		return nil
	}
	return syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
}
