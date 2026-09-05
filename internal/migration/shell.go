package migration

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"

	"github.com/exoport/apex_process_ape/internal/selfpath"
)

// ShellRunner runs a line through the platform shell.
//
// A shell is required rather than an argv split: the framework's own
// entries pipe into `jq` and quote embedded JSON, and splitting those on
// whitespace would silently run something else.
//
// Env, when non-empty, replaces the child's environment. It is how the
// self-shadow below reaches the child; an empty Env inherits this
// process's, which is correct for a caller that has not built one.
type ShellRunner struct {
	Env []string
}

func (r ShellRunner) Run(ctx context.Context, dir, line string) (int, error) {
	name, flag := "sh", "-c"
	if runtime.GOOS == "windows" {
		name, flag = "cmd", "/c"
	}
	cmd := exec.CommandContext(ctx, name, flag, line)
	cmd.Dir = dir
	if len(r.Env) > 0 {
		cmd.Env = r.Env
	}
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		// The command ran and said no. That is a verdict, not a failure
		// to obtain one.
		return exitErr.ExitCode(), nil
	}
	return -1, err
}

// refusingRunner is what `--plan --no-check` uses: every check reports
// not-run rather than a verdict nobody obtained.
type refusingRunner struct{}

func (refusingRunner) Run(context.Context, string, string) (int, error) {
	return -1, errors.New("checks disabled")
}

// NoCheckRunner returns a Runner that executes nothing.
func NoCheckRunner() Runner { return refusingRunner{} }

// NewShellRunner builds a runner in whose environment `ape` resolves to
// the RUNNING binary, and returns a cleanup plus a notice when it could
// not arrange that.
//
// A migration's `check:` and `command:` name `ape`, and that can only
// coherently mean the `ape` deciding whether to apply the migration. See
// `internal/selfpath` for the defect this prevents and the two live
// observations of it.
func NewShellRunner() (runner ShellRunner, cleanup func(), notice string) {
	env, cleanup, notice := selfpath.Pin(os.Environ())
	return ShellRunner{Env: env}, cleanup, notice
}
