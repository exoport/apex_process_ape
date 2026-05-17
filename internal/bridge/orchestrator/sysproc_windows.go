//go:build windows

package orchestrator

import (
	"os/exec"
	"syscall"
)

// On Windows the bridge does not target sub-process groups; SIGTERM is
// emulated by exec.CommandContext via context cancellation. Returning
// nil here keeps SysProcAttr unset, which is the correct default.
func newSysProcAttr() *syscall.SysProcAttr {
	return nil
}

// terminateGroup kills the process directly on Windows. Process-group
// SIGTERM semantics are not available; we fall back to Process.Kill().
// The bridge grandchild relies on its parent being gone to exit.
func terminateGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
