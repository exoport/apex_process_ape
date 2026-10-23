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

// psTimeout bounds one `ps` look; a test shortens it.
var psTimeout = 2 * time.Second

// supervisorArgs judges a supervisor's command line, its argv joined by
// spaces. A pid is reused once its process is gone, so a bare liveness
// check could keep a wait going forever on a stranger — and `run stop`
// would signal one. The supervisor's command line names its handle (`ape
// run supervise --handle …/detached/<id>.json`), which no unrelated
// process has; the file name is matched rather than the full path, so a
// symlinked temp or project path (macOS /tmp vs /private/tmp) cannot make
// ours look foreign. An empty command line (a zombie, a kernel thread) is
// not ours: a supervisor always has one.
func supervisorArgs(args, id string) supervisorCheck {
	args = strings.TrimSpace(args)
	if strings.Contains(args, "run supervise --handle ") &&
		strings.HasSuffix(args, filepath.Join("detached", id+".json")) {
		return supervisorOurs
	}
	return supervisorNotOurs
}

// probeSupervisorPS asks ps for the supervisor's command line. ps failing
// or timing out — slow under host load, F14 — is an unknown, never "not
// ours": the pid's liveness was already checked, and the next look
// decides.
func probeSupervisorPS(ctx context.Context, h *detachedHandle) supervisorCheck {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), psTimeout)
	defer cancel()
	ps := exec.CommandContext(ctx, "ps", "-o", "args=", "-p", strconv.Itoa(h.SupervisorPID)) //nolint:gosec // ps, with a pid ape recorded
	// A ps wrapper's child holding stdout open cannot outlast the timeout.
	ps.WaitDelay = psTimeout
	out, err := ps.Output()
	if err != nil {
		return supervisorUnknown
	}
	return supervisorArgs(string(out), h.ID)
}

// stopDetached asks the supervisor to stop — it forwards SIGTERM to the run
// and records the exit — after checking, immediately before signalling,
// that the pid is still this run's supervisor. Anything else is refused: a
// definite "not ours" (seen twice) with an error, a check that cannot tell
// with errIdentityUnknown.
func stopDetached(ctx context.Context, h *detachedHandle) error {
	switch lookTwice(ctx, func() supervisorCheck { return probeSupervisorFn(ctx, h) }) {
	case supervisorOurs:
		return syscall.Kill(h.SupervisorPID, syscall.SIGTERM)
	case supervisorUnknown:
		return fmt.Errorf("pid %d: %w (its command line could not be read); nothing was signalled — run it again",
			h.SupervisorPID, errIdentityUnknown)
	case supervisorNotOurs:
	}
	return fmt.Errorf("pid %d is not run %s's supervisor (it is gone, and the pid may belong to another "+
		"process now); nothing was signalled", h.SupervisorPID, h.ID)
}

func forwardTerm(p *os.Process) {
	_ = p.Signal(syscall.SIGTERM)
}
