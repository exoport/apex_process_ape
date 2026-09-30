//go:build windows

package apecmd

import (
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

// detachedProcAttr starts the supervisor with no console and in its own
// process group, the Windows counterpart of a new session.
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS}
}

func pidAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid)) //nolint:gosec // a pid ape recorded
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	const stillActive = 259
	return code == stillActive
}

// forwardTerm has no SIGTERM to send on Windows; the child is stopped.
func forwardTerm(p *os.Process) {
	_ = p.Kill()
}

// stopDetached stops the run itself: Windows has no SIGTERM to forward, and
// killing the supervisor would lose the exit. The supervisor sees the run
// end and records it.
func stopDetached(h *detachedHandle) {
	if h.ChildPID == 0 {
		return
	}
	if p, err := os.FindProcess(h.ChildPID); err == nil {
		_ = p.Kill()
	}
}
