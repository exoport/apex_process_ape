//go:build !windows

package orchestrator

import (
	"os/exec"
	"syscall"
)

// newSysProcAttr puts the spawned `claude` into its own process group so
// SIGTERM can target the whole group (claude + the bridge grandchild)
// on Stop. PLAN-5 / C3 subprocess-lifecycle.
func newSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// terminateGroup SIGTERMs the entire process group rooted at cmd. Used
// by /api/stop so the bridge grandchild tears down cleanly.
func terminateGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	// Negative PID targets the whole process group.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
}
