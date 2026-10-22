package migration

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"

	"github.com/exoport/apex_process_ape/internal/selfpath"
)

// ShellRunner runs a line through `sh -c`, on every OS: the framework's
// `check:` and `command:` lines are POSIX shell, as are the project's own
// checks (apexv1's runner). On Windows that is Git Bash's sh; under `cmd`
// the shipped checks were noise, so an update on Windows was refused or
// skipped the lift (Q163). With no `sh` on PATH the spawn fails, which
// reads as cannot-tell, never as a verdict.
//
// A shell is required rather than an argv split: the framework's own
// entries pipe into `jq` and quote embedded JSON, and splitting those on
// whitespace would silently run something else.
//
// Env, when non-empty, replaces the child's environment. It is how the
// self-shadow below reaches the child; an empty Env inherits this
// process's, which is correct for a caller that has not built one.
//
// Stderr, when set, receives the child's standard error. `ape framework
// update` sets it for the commands it applies, so a migration that refuses
// (the lift naming its missing tag, say) is read where it ran; checks keep
// it unset, because a check's stderr is noise around a verdict.
type ShellRunner struct {
	Env    []string
	Stderr io.Writer
}

func (r ShellRunner) Run(ctx context.Context, dir, line string) (int, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", line)
	cmd.Dir = dir
	if len(r.Env) > 0 {
		cmd.Env = r.Env
	}
	if r.Stderr != nil {
		cmd.Stderr = r.Stderr
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
