//go:build darwin

package procstart

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// osStart is the kernel's p_starttime for this process.
func osStart() (time.Time, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", os.Getpid())
	if err != nil {
		return time.Time{}, false
	}
	tv := kp.Proc.P_starttime
	if tv.Sec == 0 {
		return time.Time{}, false
	}
	return time.Unix(tv.Sec, int64(tv.Usec)*1000), true
}
