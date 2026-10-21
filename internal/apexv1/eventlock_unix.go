//go:build !windows

package apexv1

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// lockFileExclusive takes an exclusive advisory lock on path, creating it:
// the event log's seq is read and written under it (events.go), so two
// ape processes never log the same seq.
func lockFileExclusive(path string) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, nil
}
