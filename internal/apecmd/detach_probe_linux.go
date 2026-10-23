package apecmd

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// procRoot is where the process table is read; a test points it elsewhere.
var procRoot = "/proc"

// probeSupervisor reads the supervisor's command line from
// /proc/<pid>/cmdline rather than exec'ing ps (F14): under host load ps
// took longer than its timeout, and a live run was reported lost. A pid
// that is gone (ESRCH) is a definite negative, as is a command line that is
// not this run's supervisor's. A live pid whose /proc entry cannot be read
// (no /proc, a hidepid mount, gone between the two) falls back to ps, whose
// failure is an unknown.
func probeSupervisor(ctx context.Context, h *detachedHandle) supervisorCheck {
	if h.SupervisorPID <= 0 || !pidAlive(h.SupervisorPID) {
		return supervisorNotOurs
	}
	args, err := procCmdline(procRoot, h.SupervisorPID)
	if err != nil {
		return probeSupervisorPS(ctx, h)
	}
	return supervisorArgs(args, h.ID)
}

// procCmdline is pid's argv as ps prints it: /proc/<pid>/cmdline's
// NUL-separated arguments joined by spaces. A zombie's is empty.
func procCmdline(root string, pid int) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(strings.ReplaceAll(string(data), "\x00", " ")), nil
}
