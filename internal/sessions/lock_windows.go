//go:build windows

package sessions

import (
	"os"

	"github.com/exoport/apex_process_ape/internal/proctree"
)

// Windows: skip flock. The registry is best-effort and two ape
// invocations racing for the same file is rare enough that we accept
// the residual collision risk on Windows. Future plan can swap to
// LockFileEx via golang.org/x/sys/windows if it ever becomes a real
// problem in practice.
func lockExclusive(_ *os.File) error { return nil }
func unlock(_ *os.File)              {}

// pidAlive asks Windows whether the process runs (proctree.Alive):
// os.Process.Signal supports only Kill there, so a signal-0 probe would
// call every process dead.
func pidAlive(pid int) bool { return proctree.Alive(pid) }
