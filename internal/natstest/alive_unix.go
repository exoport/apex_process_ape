//go:build !windows

package natstest

import "syscall"

// processAlive reports whether a process with pid exists (signal 0), as
// apexv1's does.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
