//go:build linux

package sandbox

import (
	"context"
	"fmt"
	"io"
	"syscall"
	"time"

	client "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/errdefs"

	"github.com/exoport/apex_process_ape/internal/workspace"
)

var _ InteractiveBackend = (*containerdDriver)(nil)

// OpenExec starts a streamed one-shot command in the workspace and returns a
// workspace.Process the executor relays (PLAN-18 D2). Unlike Exec (NullIO, exit
// code only), the process stdio is wired to pipes so the aped front can bridge it
// to the NATS session subjects.
func (d *containerdDriver) OpenExec(ctx context.Context, id string, req workspace.ExecRequest) (workspace.Process, error) {
	return d.openProcess(ctx, id, req.Cmd, req.TTY, req.Env)
}

// OpenAttach starts the interactive login shell (or AttachRequest.Shell) with a
// PTY and returns it as a streamed workspace.Process.
func (d *containerdDriver) OpenAttach(ctx context.Context, id string, req workspace.AttachRequest) (workspace.Process, error) {
	shell := req.Shell
	if shell == "" {
		shell = "/bin/sh"
	}
	// Attach always allocates a PTY — it is an interactive shell by definition.
	return d.openProcess(ctx, id, []string{shell}, true, nil)
}

// openProcess execs args in the workspace's running task with its stdio wired to
// in-memory pipes (a PTY when tty). The image process spec supplies user/cwd/env;
// args/env/terminal are overridden per request. The returned containerdProcess
// owns cleanup via Wait.
func (d *containerdDriver) openProcess(ctx context.Context, id string, args []string, tty bool, env []string) (workspace.Process, error) {
	ctx = d.nsctx(ctx)
	container, err := d.cli.LoadContainer(ctx, ContainerName(id))
	if err != nil {
		return nil, mapContainerdErr(err)
	}
	task, err := container.Task(ctx, nil)
	if err != nil {
		return nil, mapContainerdErr(err)
	}
	spec, err := container.Spec(ctx)
	if err != nil {
		return nil, fmt.Errorf("containerd driver: load spec %s: %w", id, err)
	}
	pspec := *spec.Process
	if len(args) > 0 {
		pspec.Args = args
	}
	pspec.Terminal = tty
	if len(env) > 0 {
		pspec.Env = append(append([]string(nil), pspec.Env...), env...)
	}

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	cp := &containerdProcess{stdinW: inW, stdoutR: outR, stdoutW: outW, tty: tty}
	var creator cio.Creator
	if tty {
		// A PTY merges stderr into stdout; Stderr() is an immediately-EOF reader.
		creator = cio.NewCreator(cio.WithStreams(inR, outW, nil), cio.WithTerminal)
		cp.stderrR = eofReader{}
	} else {
		errR, errW := io.Pipe()
		creator = cio.NewCreator(cio.WithStreams(inR, outW, errW))
		cp.stderrR, cp.stderrW = errR, errW
	}

	execID := fmt.Sprintf("ape-exec-%d", time.Now().UnixNano())
	process, err := task.Exec(ctx, execID, &pspec, creator)
	if err != nil {
		cp.closePipes()
		return nil, fmt.Errorf("containerd driver: exec %s: %w", id, err)
	}
	statusC, err := process.Wait(ctx)
	if err != nil {
		_, _ = process.Delete(ctx)
		cp.closePipes()
		return nil, fmt.Errorf("containerd driver: wait exec %s: %w", id, err)
	}
	if err := process.Start(ctx); err != nil {
		_, _ = process.Delete(ctx)
		cp.closePipes()
		return nil, fmt.Errorf("containerd driver: start exec %s: %w", id, err)
	}
	cp.proc = process
	cp.statusC = statusC
	return cp, nil
}

// containerdProcess adapts a containerd task exec to workspace.Process. The
// stdio pipes are wired into the exec's cio; the front relays them over the priv
// socket. Wait blocks on the process exit, drains the cio (so all output reaches
// the pipe writers), closes those writers so the reader ends see EOF, and deletes
// the exec.
type containerdProcess struct {
	proc    client.Process
	stdinW  *io.PipeWriter
	stdoutR *io.PipeReader
	stdoutW *io.PipeWriter
	stderrR io.Reader
	stderrW *io.PipeWriter // nil under a PTY (stderr merged into stdout)
	statusC <-chan client.ExitStatus
	tty     bool
}

var _ workspace.Process = (*containerdProcess)(nil)

func (p *containerdProcess) Stdin() io.WriteCloser { return p.stdinW }
func (p *containerdProcess) Stdout() io.Reader     { return p.stdoutR }
func (p *containerdProcess) Stderr() io.Reader     { return p.stderrR }

func (p *containerdProcess) Resize(cols, rows uint16) error {
	if !p.tty {
		return nil // no PTY to resize
	}
	return p.proc.Resize(context.Background(), uint32(cols), uint32(rows))
}

// Kill SIGKILLs the exec so the relay can reap it when the priv conn drops
// before a clean exit (an abandoned interactive client). The signal makes the
// exec exit, so the pending Wait returns via statusC and does the normal
// drain + Delete. A not-found means the exec already exited — treat as done.
func (p *containerdProcess) Kill(ctx context.Context) error {
	if err := p.proc.Kill(ctx, syscall.SIGKILL); err != nil && !errdefs.IsNotFound(err) {
		return err
	}
	return nil
}

// Wait blocks on the exec exit, then drains + tears down the cio so the relay's
// readers see EOF and the exec is reaped.
func (p *containerdProcess) Wait(ctx context.Context) (int, error) {
	var status client.ExitStatus
	select {
	case status = <-p.statusC:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	// Ensure all process output has been copied to our pipe writers before we
	// close them (io.Pipe writes block until read, so this returns once the relay
	// has drained everything the process produced).
	if pio := p.proc.IO(); pio != nil {
		pio.Wait()
	}
	_ = p.stdoutW.Close()
	if p.stderrW != nil {
		_ = p.stderrW.Close()
	}
	// Reap the exec on a fresh context: the caller's ctx may already be cancelled
	// (that is often why the process exited), but the exec must still be deleted.
	_, _ = p.proc.Delete(context.Background()) //nolint:contextcheck // cleanup must survive caller cancellation
	code, _, err := status.Result()
	return int(code), err
}

func (p *containerdProcess) closePipes() {
	_ = p.stdinW.Close()
	_ = p.stdoutW.Close()
	if p.stderrW != nil {
		_ = p.stderrW.Close()
	}
}

// eofReader is an always-EOF io.Reader — the Stderr() of a PTY exec (stderr is
// merged into stdout), so the relay's stderr pump ends immediately.
type eofReader struct{}

func (eofReader) Read([]byte) (int, error) { return 0, io.EOF }
