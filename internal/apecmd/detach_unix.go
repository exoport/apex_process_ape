//go:build !windows

package apecmd

import (
	"errors"
	"os"
	"syscall"
)

// detachedProcAttr starts the supervisor in a new session: no controlling
// terminal, no process group shared with the shell that started it.
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func forwardTerm(p *os.Process) {
	_ = p.Signal(syscall.SIGTERM)
}

// stopDetached asks the supervisor to stop; it forwards SIGTERM to the
// run and records the exit.
func stopDetached(h *detachedHandle) {
	_ = syscall.Kill(h.SupervisorPID, syscall.SIGTERM)
}
