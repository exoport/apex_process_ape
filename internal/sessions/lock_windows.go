//go:build windows

package sessions

import (
	"os"
)

// Windows: skip flock. The registry is best-effort and two ape
// invocations racing for the same file is rare enough that we accept
// the residual collision risk on Windows. Future plan can swap to
// LockFileEx via golang.org/x/sys/windows if it ever becomes a real
// problem in practice.
func lockExclusive(_ *os.File) error { return nil }
func unlock(_ *os.File)              {}

// pidAlive uses os.FindProcess; on Windows FindProcess never fails
// for a pid that doesn't exist (it returns a handle that errors on
// later operations). We probe via Signal(syscall.Signal(0)) which is
// safe on Windows: it returns an error for dead PIDs.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(nil) == nil
}
