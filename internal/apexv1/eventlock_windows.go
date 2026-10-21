//go:build windows

package apexv1

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// lockFileExclusive is the Windows half of the event log's lock; see
// eventlock_unix.go.
func lockFileExclusive(path string) (unlock func(), err error) {
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
