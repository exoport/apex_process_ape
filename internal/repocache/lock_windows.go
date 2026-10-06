//go:build windows

package repocache

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// lockFile is the Windows half of the clone lock; see lock_unix.go.
func lockFile(path string) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	handle := windows.Handle(f.Fd())
	var overlapped windows.Overlapped
	if err := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &overlapped); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() {
		var release windows.Overlapped
		_ = windows.UnlockFileEx(handle, 0, 1, 0, &release)
		_ = f.Close()
	}, nil
}
