//go:build !windows

package repocache

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// lockFile takes an exclusive advisory lock on path, creating it. Two
// projects updating at once share one clone, and a fetch interleaved with
// another run's checkout leaves a half-switched tree or a held index.lock.
func lockFile(path string) (unlock func(), err error) {
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
