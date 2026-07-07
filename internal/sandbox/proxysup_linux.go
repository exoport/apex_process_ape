//go:build linux

package sandbox

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Start launches the detached CONNECT-proxy daemon for a workspace and
// returns once it is confirmed listening — or fails closed. It re-execs the
// ape binary as the hidden `sandbox _proxyd` command in its own session
// (setsid) so the proxy outlives `ape sandbox up`; the child reports its
// bound loopback address over an inherited pipe (fd proxyReadyChildFD),
// which the parent dial-checks before wiring HTTPS_PROXY.
func (s *ProxySupervisor) Start(ctx context.Context, o ProxyStartOptions) (ProxyState, error) {
	exe := s.Exe
	if exe == "" {
		e, err := os.Executable()
		if err != nil {
			return ProxyState{}, fmt.Errorf("sandbox: resolve ape binary for egress proxy: %w", err)
		}
		exe = e
	}
	if o.Dir != "" {
		if err := os.MkdirAll(o.Dir, 0o700); err != nil {
			return ProxyState{}, fmt.Errorf("sandbox: proxy state dir: %w", err)
		}
	}

	pr, pw, err := os.Pipe()
	if err != nil {
		return ProxyState{}, fmt.Errorf("sandbox: proxy readiness pipe: %w", err)
	}
	defer pr.Close()

	args := ProxyDaemonArgs(o.Workspace, o.listen(), o.AuditLog, o.Allow, proxyReadyChildFD)
	// The daemon must outlive `ape sandbox up`, so it is deliberately NOT
	// bound to a context that gets cancelled when the command returns.
	cmd := exec.Command(exe, args...) //nolint:noctx // detached daemon: must outlive up's context; binary/args are ape-controlled
	cmd.ExtraFiles = []*os.File{pw}   // → proxyReadyChildFD in the child
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin = nil
	if o.Dir != "" {
		if logf, lerr := os.OpenFile(o.logPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); lerr == nil {
			cmd.Stdout = logf
			cmd.Stderr = logf
			defer logf.Close()
		}
	}

	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		return ProxyState{}, fmt.Errorf("sandbox: start egress proxy daemon: %w", err)
	}
	// Close the parent's write end so a dead child yields EOF on the pipe.
	_ = pw.Close()
	pid := cmd.Process.Pid
	// Detach: we never Wait on the long-lived daemon.
	_ = cmd.Process.Release()

	addr, err := readReady(pr, s.readyTimeout())
	if err != nil {
		_ = stopPID(pid)
		return ProxyState{}, fmt.Errorf("sandbox: egress proxy did not become ready: %w", err)
	}
	if err := dialCheck(ctx, addr, 2*time.Second); err != nil {
		_ = stopPID(pid)
		return ProxyState{}, fmt.Errorf("sandbox: egress proxy unreachable at %s (fail-closed): %w", addr, err)
	}
	return ProxyState{
		Workspace: o.Workspace,
		PID:       pid,
		Addr:      addr,
		AuditLog:  o.AuditLog,
		Allow:     o.Allow,
	}, nil
}

// Stop terminates a supervised proxy by PID (SIGTERM). A zero/absent PID or
// an already-exited process is a no-op.
func (s *ProxySupervisor) Stop(st ProxyState) error {
	return stopPID(st.PID)
}

// readReady reads the daemon's one-line bound address from the readiness
// pipe, bounded by timeout so a stuck daemon fails closed.
func readReady(pr *os.File, timeout time.Duration) (string, error) {
	if err := pr.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return "", err
	}
	line, err := bufio.NewReader(pr).ReadString('\n')
	if addr := strings.TrimSpace(line); addr != "" {
		return addr, nil
	}
	if err != nil {
		return "", err
	}
	return "", errors.New("empty readiness address")
}

// stopPID sends SIGTERM to pid. A non-positive pid or an already-dead
// process is not an error.
func stopPID(pid int) error {
	if pid <= 0 {
		return nil
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := p.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

// dialCheck confirms the proxy is actually accepting connections at addr —
// the fail-closed gate before HTTPS_PROXY is wired.
func dialCheck(ctx context.Context, addr string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	return c.Close()
}
