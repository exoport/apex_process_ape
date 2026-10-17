//go:build !windows

package proctree

import (
	"errors"
	"syscall"
)

// Alive reports whether a process with pid exists (signal 0; EPERM is a
// process another user owns, which exists).
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
