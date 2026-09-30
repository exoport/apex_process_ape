//go:build windows

package apecmd

import (
	"context"
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

// procIdentity reports whether pid is alive, its executable's base name,
// and when it was created.
func procIdentity(pid int) (alive bool, image string, created time.Time) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid)) //nolint:gosec // a pid ape recorded
	if err != nil {
		return false, "", time.Time{}
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil || code != 259 { // STILL_ACTIVE
		return false, "", time.Time{}
	}
	buf := make([]uint16, windows.MAX_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err == nil {
		image = filepath.Base(windows.UTF16ToString(buf[:size]))
	}
	var c, e, k, u windows.Filetime
	if err := windows.GetProcessTimes(h, &c, &e, &k, &u); err == nil {
		created = time.Unix(0, c.Nanoseconds())
	}
	return true, image, created
}

func pidAlive(pid int) bool {
	alive, _, _ := procIdentity(pid)
	return alive
}

// isOurs: alive, an ape executable, and created no earlier than the handle
// — a reused pid belongs to a process created later than its original, but
// never to one created before the run was even started.
func isOurs(pid int, h *detachedHandle) bool {
	if pid <= 0 {
		return false
	}
	alive, image, created := procIdentity(pid)
	if !alive || !strings.EqualFold(strings.TrimSuffix(image, ".exe"), "ape") {
		return false
	}
	return !created.IsZero() && !created.Before(h.StartedAt.Add(-2*time.Second))
}

func supervisorIsOurs(_ context.Context, h *detachedHandle) bool { return isOurs(h.SupervisorPID, h) }

// stopDetached stops the run itself: Windows has no SIGTERM to forward, and
// killing the supervisor would lose the exit. The supervisor sees the run
// end and records it. The run's pid is checked to still be this run's ape
// process immediately before; anything else is refused.
func stopDetached(_ context.Context, h *detachedHandle) error {
	if !isOurs(h.ChildPID, h) {
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
