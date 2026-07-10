//go:build windows

package service

import (
	"os"
	"os/exec"
)

// configureProcessGroup is a no-op on Windows: ConPTY / the Windows process
// model lacks POSIX process groups, so terminateGroup falls back to a
// direct process kill. If a future plan needs to reap grandchildren under
// Windows, the right API is a Job Object via golang.org/x/sys/windows.
func configureProcessGroup(_ *exec.Cmd) {}

// terminateGroup kills the child process directly on Windows (no group
// semantics). A non-positive pid is a no-op.
func terminateGroup(pid int) {
	if pid <= 0 {
		return
	}
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}
