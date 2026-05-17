//go:build !windows

package sessions

import (
	"os"
	"syscall"
)

func lockExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

func unlock(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// Signal 0 probes process existence without delivering anything.
	// errno.ESRCH = no such process; errno.EPERM = exists but
	// belongs to a different uid — treat that as alive too (the
	// registry is informational; we don't want to silently drop
	// rows just because the running user changed).
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
