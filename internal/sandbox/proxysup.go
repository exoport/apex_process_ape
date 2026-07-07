package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// proxyDaemonVerb is the hidden `ape sandbox` subcommand that runs the
// persistent host-side CONNECT egress proxy for a workspace. `ape sandbox
// up` re-execs itself with this verb to spawn the detached daemon (see
// ProxySupervisor.Start); it is not user-facing.
const proxyDaemonVerb = "_proxyd"

// proxyReadyChildFD is the file descriptor the detached daemon reports its
// bound loopback address on. ProxySupervisor.Start passes the daemon a pipe
// as the first extra file, which the child sees as fd 3 (0/1/2 are stdio).
const proxyReadyChildFD = 3

// defaultProxyListen is where the egress proxy binds when none is given: a
// loopback ephemeral port, so the kernel picks a free one per workspace.
const defaultProxyListen = "127.0.0.1:0"

// defaultProxyReadyTimeout bounds how long Start waits for the daemon to
// report it is listening before failing closed.
const defaultProxyReadyTimeout = 5 * time.Second

// EgressMode is how `ape sandbox up` wires a workspace's public egress,
// decided by PlanEgress from the --proxy flag and the profile allowlist.
type EgressMode int

const (
	// EgressOpen: no allowlist and no --proxy — the workspace uses the
	// default container network (unrestricted public egress). Declaring
	// network.authorized_domains switches it to EgressManaged.
	EgressOpen EgressMode = iota
	// EgressExplicit: --proxy host:port was given — wire it as HTTPS_PROXY;
	// the caller owns that proxy's lifecycle, ape does not supervise it.
	EgressExplicit
	// EgressManaged: the profile declares an allowlist — ape supervises a
	// detached CONNECT proxy for the workspace's lifetime (fail-closed: `up`
	// aborts if the proxy can't start).
	EgressManaged
)

// PlanEgress decides how egress is wired for `ape sandbox up`. It is pure —
// the command acts on the result. An explicit --proxy always wins; an
// allowlist with no --proxy means a supervised proxy; neither means the
// default (open) network.
func PlanEgress(explicitProxy string, authorizedDomains []string) EgressMode {
	if strings.TrimSpace(explicitProxy) != "" {
		return EgressExplicit
	}
	if len(authorizedDomains) > 0 {
		return EgressManaged
	}
	return EgressOpen
}

// ProxyState is the durable record of a supervised egress proxy for one
// workspace: enough for `up` to wire HTTPS_PROXY and for `down` to stop it
// later (persisted into the workspace registry).
type ProxyState struct {
	Workspace string
	PID       int
	Addr      string   // loopback host:port the proxy listens on
	AuditLog  string   // path to the egress-audit.jsonl trail
	Allow     []string // the allowlist the daemon enforces
}

// ProxyURL returns the HTTPS_PROXY value pointing at this proxy, or "".
func (s ProxyState) ProxyURL() string {
	if strings.TrimSpace(s.Addr) == "" {
		return ""
	}
	return "http://" + s.Addr
}

// ProxyDirFor returns the per-workspace host-side directory for egress
// artifacts (the audit trail and the daemon log): <stateDir>/proxies/<name>.
func ProxyDirFor(stateDir, name string) string {
	return filepath.Join(stateDir, "proxies", name)
}

// ProxyAuditLogFor returns the per-workspace egress-audit.jsonl path.
func ProxyAuditLogFor(stateDir, name string) string {
	return filepath.Join(ProxyDirFor(stateDir, name), "egress-audit.jsonl")
}

// ProxyDaemonArgs builds the argv (after the ape binary) that launches the
// detached egress-proxy daemon for a workspace. Kept pure so the exact
// command ProxySupervisor.Start spawns is unit-tested without spawning
// anything.
func ProxyDaemonArgs(workspace, listen, auditLog string, allow []string, readyFD int) []string {
	args := []string{"sandbox", proxyDaemonVerb, "--workspace", workspace}
	if strings.TrimSpace(listen) != "" {
		args = append(args, "--listen", listen)
	}
	if strings.TrimSpace(auditLog) != "" {
		args = append(args, "--audit", auditLog)
	}
	if readyFD > 0 {
		args = append(args, "--ready-fd", strconv.Itoa(readyFD))
	}
	for _, a := range allow {
		args = append(args, "--allow", a)
	}
	return args
}

// ProxySupervisor spawns and stops the detached egress-proxy daemon. The
// spawn (setsid + fd hand-off) is Linux-only — see proxysup_linux.go, with
// a portable stub in proxysup_other.go so the cross-platform CLI wiring
// compiles on the Windows CI leg.
type ProxySupervisor struct {
	// Exe is the ape binary to re-exec; "" resolves os.Executable().
	Exe string
	// ReadyTimeout bounds the wait for the daemon to report ready; 0 →
	// defaultProxyReadyTimeout.
	ReadyTimeout time.Duration
}

func (s *ProxySupervisor) readyTimeout() time.Duration {
	if s.ReadyTimeout > 0 {
		return s.ReadyTimeout
	}
	return defaultProxyReadyTimeout
}

// ProxyStartOptions describes the proxy to supervise for one workspace.
type ProxyStartOptions struct {
	Workspace string
	Dir       string   // per-workspace state dir for the daemon log (ProxyDirFor)
	Listen    string   // loopback bind; "" → defaultProxyListen
	AuditLog  string   // egress-audit.jsonl path
	Allow     []string // authorized domains
}

func (o ProxyStartOptions) listen() string {
	if strings.TrimSpace(o.Listen) == "" {
		return defaultProxyListen
	}
	return o.Listen
}

func (o ProxyStartOptions) logPath() string { return filepath.Join(o.Dir, "proxyd.log") }

// DaemonOptions configures RunProxyDaemon — the body of the hidden
// `ape sandbox _proxyd` command.
type DaemonOptions struct {
	Workspace string
	Listen    string
	AuditLog  string
	Allow     []string
	ReadyFD   int // fd to report the bound address on; 0 → don't report
}

// RunProxyDaemon runs the detached host-side CONNECT proxy for one
// workspace until it is signalled to stop. It builds the deny-by-default
// proxy from the workspace allowlist, appends every egress decision to the
// audit log, reports the bound loopback address on the readiness fd (so the
// parent learns the ephemeral port), and serves until SIGTERM/SIGINT or ctx
// cancellation. It is the process body ProxySupervisor.Start re-execs.
func RunProxyDaemon(ctx context.Context, o DaemonOptions) error {
	var sink AuditSink
	if strings.TrimSpace(o.AuditLog) != "" {
		if err := os.MkdirAll(filepath.Dir(o.AuditLog), 0o700); err != nil {
			return fmt.Errorf("sandbox: proxy audit dir: %w", err)
		}
		f, err := os.OpenFile(o.AuditLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return fmt.Errorf("sandbox: open proxy audit log: %w", err)
		}
		defer f.Close()
		sink = NewJSONLSink(f)
	}

	listen := o.Listen
	if strings.TrimSpace(listen) == "" {
		listen = defaultProxyListen
	}
	p := NewProxy(ProxyConfig{
		Matcher: NewMatcher(o.Allow),
		JobID:   "ws:" + o.Workspace,
		Sink:    sink,
	})
	// The proxy is a long-lived background listener; it owns its own
	// lifetime and is torn down by signal/ctx below, not by p.Start's caller.
	if err := p.Start(listen); err != nil { //nolint:contextcheck // Proxy.Start manages its own listener context
		return err
	}
	defer p.Close()

	// Report the bound address so the parent can wire HTTPS_PROXY. Best
	// effort: if the fd is absent, the parent's read times out and fails
	// closed.
	if o.ReadyFD > 0 {
		if rf := os.NewFile(uintptr(o.ReadyFD), "proxy-ready"); rf != nil {
			_, _ = rf.WriteString(p.Addr() + "\n")
			_ = rf.Close()
		}
	}

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigc)
	select {
	case <-ctx.Done():
	case <-sigc:
	}
	return nil
}
