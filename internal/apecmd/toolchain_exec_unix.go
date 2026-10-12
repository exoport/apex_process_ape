//go:build !windows

package apecmd

import (
	"fmt"
	"syscall"
)

// execPinned replaces this process with the pinned binary: same argv, same
// stdio, and its exit status is the command's, with nothing in between.
func execPinned(bin string, args, env []string) error {
	argv := append([]string{bin}, args...)
	if err := syscall.Exec(bin, argv, env); err != nil { //nolint:gosec // the project's own pinned ape, located by bingo's naming rule
		return usageErrExit(exitCodeApePinUnusable, fmt.Errorf("run the pinned ape %s: %w", bin, err))
	}
	return nil // unreachable: Exec does not return on success
}
