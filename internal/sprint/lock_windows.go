//go:build windows

package sprint

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// lockFile is the Windows half of the tracker lock. See lock_unix.go for
// why the lock exists at all.
//
// This is where ape does better than the script it replaces:
// `reconcile-epic-status.py` degrades to a NO-OP wherever `fcntl` is
// unavailable, so on Windows its concurrent-write guarantee simply is not
// there. LockFileEx gives the same exclusive semantics as flock, so the
// guarantee is platform-independent.
func lockFile(path string) (unlock func(), err error) {
	lockPath := LockPath(path)
	f, openErr := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if openErr != nil {
		// Unwritable directory: proceed unlocked and let the tracker read
		// fail with this package's own error contract.
		return func() {}, nil //nolint:nilerr // degrade unlocked; the tracker read reports the real problem
	}
	handle := windows.Handle(f.Fd())
	var overlapped windows.Overlapped
	if err := windows.LockFileEx(
		handle,
		windows.LOCKFILE_EXCLUSIVE_LOCK,
		0, 1, 0,
		&overlapped,
	); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock %s: %w", lockPath, err)
	}
	return func() {
		var releaseOverlapped windows.Overlapped
		_ = windows.UnlockFileEx(handle, 0, 1, 0, &releaseOverlapped)
		_ = f.Close()
	}, nil
}
