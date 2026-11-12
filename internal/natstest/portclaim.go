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
var claimDir = filepath.Join(os.TempDir(), "ape-natstest-ports")

// claimAcross claims port p for this process across every natstest
// process on the host: an O_EXCL create, so two processes never both
// win; a file left by a process that has exited is removed and the
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
		_ = os.Remove(path)
	}
	return false
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
