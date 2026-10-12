//go:build windows

package apecmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// execPinned runs the pinned binary and waits: Windows has no exec, so the
// command's exit status is the child's, forwarded as already reported.
func execPinned(bin string, args, env []string) error {
	cmd := exec.Command(bin, args...) //nolint:gosec // the project's own pinned ape, located by bingo's naming rule
	cmd.Stdin, cmd.Stdout, cmd.Stderr, cmd.Env = os.Stdin, os.Stdout, os.Stderr, env
	err := cmd.Run()
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return reportedErr(exitErr.ExitCode(), err)
	}
	if err != nil {
		return usageErrExit(exitCodeApePinUnusable, fmt.Errorf("run the pinned ape %s: %w", bin, err))
	}
	return reportedErr(0, nil)
}
