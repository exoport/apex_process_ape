package apexv1

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// processStart is an opaque identity of when pid started (Q144); on macOS
// the kernel's p_starttime.
func processStart(pid int) (string, bool) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || k == nil || int(k.Proc.P_pid) != pid {
		return "", false
	}
	t := k.Proc.P_starttime
	return fmt.Sprintf("%d.%06d", t.Sec, t.Usec), true
}
