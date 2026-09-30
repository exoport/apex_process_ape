//go:build !windows

package apecmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
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

// supervisorIsOurs reports whether the handle's supervisor pid is still
// THIS run's supervisor, not merely a live process. A pid is reused once
// its process is gone, so a bare liveness check could keep a wait going
// forever on a stranger — and `run stop` would signal one. The supervisor's
// command line names its handle (`ape run supervise --handle
// …/detached/<id>.json`), which no unrelated process has; the file name is
// matched rather than the full path, so a symlinked temp or project path
// (macOS /tmp vs /private/tmp) cannot make ours look foreign.
func supervisorIsOurs(ctx context.Context, h *detachedHandle) bool {
	if h.SupervisorPID <= 0 || !pidAlive(h.SupervisorPID) {
		return false
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "args=", "-p", strconv.Itoa(h.SupervisorPID)).Output() //nolint:gosec // ps, with a pid ape recorded
	if err != nil {
		return false
	}
	args := strings.TrimSpace(string(out))
	return strings.Contains(args, "run supervise --handle ") &&
		strings.HasSuffix(args, filepath.Join("detached", h.ID+".json"))
}

// stopDetached asks the supervisor to stop — it forwards SIGTERM to the run
// and records the exit — after checking, immediately before signalling,
// that the pid is still this run's supervisor. Anything else is refused.
func stopDetached(ctx context.Context, h *detachedHandle) error {
	if !supervisorIsOurs(ctx, h) {
		return fmt.Errorf("pid %d is not run %s's supervisor (it is gone, and the pid may belong to another "+
			"process now); nothing was signalled", h.SupervisorPID, h.ID)
	}
	return syscall.Kill(h.SupervisorPID, syscall.SIGTERM)
}

func forwardTerm(p *os.Process) {
	_ = p.Signal(syscall.SIGTERM)
}
