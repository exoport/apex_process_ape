//go:build windows

package apecmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// detachedProcAttr starts the supervisor with no console and in its own
// process group, the Windows counterpart of a new session.
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS}
}

// probeOurs looks at pid: this run's (alive, an ape executable, created no
// earlier than the handle — a reused pid belongs to a process created later
// than its original, but never to one created before the run was even
// started), definitely not (no such process, ended, another program, or
// created before the run), or unknown when a query that should answer
// fails (F14: a look that cannot tell is never a loss).
func probeOurs(pid int, h *detachedHandle) supervisorCheck {
	if pid <= 0 {
		return supervisorNotOurs
	}
	ph, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid)) //nolint:gosec // a pid ape recorded
	if err != nil {
		// No such process, or one this user may not query — never the
		// run's, which this user started.
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) || errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return supervisorNotOurs
		}
		return supervisorUnknown
	}
	defer func() { _ = windows.CloseHandle(ph) }()
	var code uint32
	if err := windows.GetExitCodeProcess(ph, &code); err != nil {
		return supervisorUnknown
	}
	if code != 259 { // STILL_ACTIVE
		return supervisorNotOurs
	}
	var buf [windows.MAX_PATH]uint16
	size := uint32(windows.MAX_PATH)
	if err := windows.QueryFullProcessImageName(ph, 0, &buf[0], &size); err != nil {
		return supervisorUnknown
	}
	if image := filepath.Base(windows.UTF16ToString(buf[:size])); !strings.EqualFold(strings.TrimSuffix(image, ".exe"), "ape") {
		return supervisorNotOurs
	}
	var c, e, k, u windows.Filetime
	if err := windows.GetProcessTimes(ph, &c, &e, &k, &u); err != nil {
		return supervisorUnknown
	}
	if time.Unix(0, c.Nanoseconds()).Before(h.StartedAt.Add(-2 * time.Second)) {
		return supervisorNotOurs
	}
	return supervisorOurs
}

func probeSupervisor(_ context.Context, h *detachedHandle) supervisorCheck {
	return probeOurs(h.SupervisorPID, h)
}

// stopDetached ends the run's own process: Windows has no SIGTERM to
// forward, and killing the supervisor would end it before it recorded the
// exit (and orphan the run). The supervisor's wait then returns, it records
// the exit, and it ends. This is a HARD stop (TerminateProcess), not a
// Ctrl-C: ape change saves no residue and the skill's commands are not
// stopped — documented as such. The run's pid is checked to still be this
// run's ape process immediately before; anything else is refused: a
// definite "not ours" (seen twice) with an error, a check that cannot tell
// with errIdentityUnknown.
func stopDetached(ctx context.Context, h *detachedHandle) error {
	switch lookTwice(ctx, func() supervisorCheck { return probeOurs(h.ChildPID, h) }) {
	case supervisorOurs:
	case supervisorUnknown:
		return fmt.Errorf("pid %d: %w; nothing was stopped — run it again", h.ChildPID, errIdentityUnknown)
	case supervisorNotOurs:
		return fmt.Errorf("pid %d is not run %s's process (it is gone, and the pid may belong to another "+
			"process now); nothing was stopped", h.ChildPID, h.ID)
	}
	p, err := os.FindProcess(h.ChildPID)
	if err != nil {
		return err
	}
	return p.Kill()
}

// forwardTerm has no SIGTERM to send on Windows; the child is stopped.
func forwardTerm(p *os.Process) {
	_ = p.Kill()
}
