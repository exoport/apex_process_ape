package apecmd

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// childOpened returns a wait that reports whether a process opened path
// within timeout (RK-1, Q143). A detached `ape run wait` child opens its
// run's handle only from its command, after ape's SIGTERM handler is in
// (root.go's signal.NotifyContext): the readiness a stop test needs. A
// fixed sleep was not it: under the full suite the race-built child had
// not reached that handler in 300 ms, died of the forwarded SIGTERM, and
// recorded exit 1 for 75. SigCgt cannot tell either: Go's runtime catches
// SIGTERM from its start.
func childOpened(t *testing.T, path string) func(time.Duration) bool {
	t.Helper()
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	require.NoError(t, err)
	t.Cleanup(func() { _ = unix.Close(fd) })
	_, err = unix.InotifyAddWatch(fd, path, unix.IN_OPEN)
	require.NoError(t, err)
	return func(timeout time.Duration) bool {
		deadline := time.Now().Add(timeout)
		buf := make([]byte, 4096)
		for time.Now().Before(deadline) {
			fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			if n, err := unix.Poll(fds, int(time.Until(deadline)/time.Millisecond)+1); err != nil || n == 0 {
				continue
			}
			if n, err := unix.Read(fd, buf); err == nil && n > 0 {
				return true
			}
		}
		return false
	}
}
