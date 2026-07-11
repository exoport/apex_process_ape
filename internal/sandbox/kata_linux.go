//go:build linux

package sandbox

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Provision creates and starts the detached, long-lived Kata workspace
// container described by spec. The workspace outlives this call — teardown
// is a separate Down.
func (r *Runner) Provision(ctx context.Context, spec WorkspaceSpec) error {
	args, err := spec.RunArgs()
	if err != nil {
		return err
	}
	return r.run(ctx, false, args)
}

// Exec runs a one-shot command inside a running workspace.
func (r *Runner) Exec(ctx context.Context, container string, tty bool, cmd []string) error {
	return r.run(ctx, tty, ExecArgs(container, tty, cmd))
}

// Attach opens an interactive login shell inside a running workspace.
func (r *Runner) Attach(ctx context.Context, container, shell string) error {
	return r.run(ctx, true, AttachArgs(container, shell))
}

// Freeze cgroup-freezes a running workspace's guest (nerdctl pause): the guest
// stops but its RAM stays fully resident. This is a freeze, NOT a VM suspend
// (which would save RAM to disk — unsupported on the Kata backend; see PLAN-18
// D7 and the `ape sandbox suspend` stub).
func (r *Runner) Freeze(ctx context.Context, container string) error {
	return r.run(ctx, false, PauseArgs(container))
}

// Unfreeze thaws a cgroup-frozen workspace (nerdctl unpause).
func (r *Runner) Unfreeze(ctx context.Context, container string) error {
	return r.run(ctx, false, ResumeArgs(container))
}

// Start (re)starts a stopped workspace container.
func (r *Runner) Start(ctx context.Context, container string) error {
	return r.run(ctx, false, StartArgs(container))
}

// Stop gracefully stops a running workspace container.
func (r *Runner) Stop(ctx context.Context, container string) error {
	return r.run(ctx, false, StopArgs(container))
}

// Down tears a workspace down (force-remove the container). Removing the
// registry entry, staging home, and any named volume is the caller's job
// (per the mount-mode persistence policy).
func (r *Runner) Down(ctx context.Context, container string) error {
	return r.run(ctx, false, DownArgs(container))
}

// run shells out to the driver binary. Interactive calls (exec/attach) wire
// the caller's terminal straight through; non-interactive calls capture
// combined output so a nerdctl failure surfaces its own diagnostics in the
// returned error rather than vanishing.
func (r *Runner) run(ctx context.Context, interactive bool, args []string) error {
	// Global flags (e.g. --data-root) precede the subcommand; args[0] stays the
	// subcommand name for diagnostics. globalArgs returns a fresh slice, so this
	// never aliases a shared backing array.
	argv := append(r.globalArgs(), args...)
	if r.runFunc != nil {
		return r.runFunc(ctx, interactive, argv)
	}
	cmd := exec.CommandContext(ctx, r.bin(), argv...) //nolint:gosec // binary + args are ape-controlled

	if interactive {
		cmd.Stdin = orReader(r.Stdin, os.Stdin)
		cmd.Stdout = orWriter(r.Stdout, os.Stdout)
		cmd.Stderr = orWriter(r.Stderr, os.Stderr)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("sandbox: %s %s: %w", r.bin(), args[0], err)
		}
		return nil
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("sandbox: %s %s: %w\n%s", r.bin(), args[0], err, strings.TrimSpace(string(out)))
	}
	if r.Stdout != nil && len(out) > 0 {
		_, _ = r.Stdout.Write(out)
	}
	return nil
}

func orReader(r, def io.Reader) io.Reader {
	if r != nil {
		return r
	}
	return def
}

func orWriter(w, def io.Writer) io.Writer {
	if w != nil {
		return w
	}
	return def
}
