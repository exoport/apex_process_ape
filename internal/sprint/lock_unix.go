//go:build !windows

package sprint

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// lockFile takes an exclusive advisory lock on a sidecar `<path>.lock`,
// serialising the tracker's read-modify-write.
//
// The lock is not optional politeness: concurrent per-epic sub-agents
// reconcile the SAME sprint-status.yaml, and without it the last writer
// silently drops a sibling's update. `reconcile-epic-status.py` takes the
// same flock and degrades to a no-op where fcntl is missing; ape locks on
// Windows too (see lock_windows.go), so the guarantee holds on every
// platform rather than most.
//
// A sidecar that cannot be created means the directory is unwritable,
// which means the tracker is unreachable anyway — so we proceed unlocked
// and let the read fail with this package's own error contract rather
// than a confusing permissions message about a file the operator never
// heard of.
func lockFile(path string) (unlock func(), err error) {
	lockPath := LockPath(path)
	f, openErr := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if openErr != nil {
		// Deliberately not propagated — see the doc comment: an unwritable
		// directory means the tracker read is about to fail with a message
		// that names the tracker, which is far more useful than one naming
		// a sidecar the operator has never heard of.
		return func() {}, nil //nolint:nilerr // degrade unlocked; the tracker read reports the real problem
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock %s: %w", lockPath, err)
	}
	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, nil
}
