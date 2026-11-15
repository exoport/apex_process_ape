package natstest

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// claimDir holds one file per port a natstest process has handed out,
// named for the port and holding that process's pid. go test runs each
// package as its own process, and claimPort's in-process set cannot see
// another's: a stopped node's port was picked by another package's
// cluster while it was down, and Restart could not bind it (CI-5GREEN-4
// on b4a54f8, Windows). A file whose process is gone is stale and is
// claimed again. A test points it at its own directory.
var claimDir = filepath.Join(os.TempDir(), claimDirName(os.Getuid()))

// claimDirName is the claim directory's name for a user: one per user,
// since the directory is 0700 and a shared /tmp would hand a second user
// one whose every create fails (NT-2). Windows has no uid (-1), and its
// TempDir is the user's own.
func claimDirName(uid int) string {
	if uid < 0 {
		return "ape-natstest-ports"
	}
	return "ape-natstest-ports-" + strconv.Itoa(uid)
}

// testAfterStaleRead, when set, runs once a claim has been read as stale
// and before it is reclaimed: a test's way to let another process reclaim
// it first. Nil outside the test.
var testAfterStaleRead func(path string)

// claimAcross claims port p for this process across every natstest
// process on the host: an O_EXCL create, so two processes never both
// win; a file left by a process that has exited is reclaimed and the
// create tried once more. Any other error refuses the port, so the
// picker tries another.
func claimAcross(p int) bool {
	if err := os.MkdirAll(claimDir, 0o700); err != nil {
		return false
	}
	path := filepath.Join(claimDir, strconv.Itoa(p))
	for range 2 {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, werr := f.WriteString(strconv.Itoa(os.Getpid()))
			cerr := f.Close()
			return werr == nil && cerr == nil
		}
		if !errors.Is(err, fs.ErrExist) || !staleClaim(path) {
			return false
		}
		if testAfterStaleRead != nil {
			testAfterStaleRead(path)
		}
		if !reclaim(path) {
			return false
		}
	}
	return false
}

// reclaim removes the stale claim at path, under path+".reclaim" (an
// O_EXCL create, so one process at a time) and only if it is still stale
// there. Two processes that both read the same stale claim would
// otherwise both remove it, the second removing the live claim the first
// had made in its place, and both hold the port (NT-1). A reclaim already
// held refuses the port; one a process left by dying between its create
// and its remove (microseconds) keeps refusing that one port.
func reclaim(path string) bool {
	lock := path + ".reclaim"
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return false
	}
	_ = f.Close()
	defer func() { _ = os.Remove(lock) }()
	if !staleClaim(path) {
		return false
	}
	return os.Remove(path) == nil
}

// staleClaim reports whether the claim at path was made by a process that
// is no longer running. A claim being written (empty yet) is not stale.
func staleClaim(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return false
	}
	return pid != os.Getpid() && !processAlive(pid)
}
