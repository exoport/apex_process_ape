package aped

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/exoport/apex_process_ape/internal/sandbox"
	"github.com/exoport/apex_process_ape/internal/workspace"
)

// egressAuditMode is the per-workspace egress trail's permission: readable by the
// operator group (`ape`), written by the front. It records hostnames, ports,
// decisions and byte counts — no secrets — and it exists to be read.
const egressAuditMode = 0o640

// EgressSupervisor runs the per-workspace CONNECT proxies IN-PROCESS in the
// de-privileged aped front (PLAN-21 D2).
//
// Why the front and not the executor: the proxy needs AF_INET (it dials
// upstream), and the root executor is AF_UNIX-only with IPAddressDeny=any by
// design. The front already holds AF_INET on the host↔guest bridge, is not root,
// and holds no capability — so it is the correct home for a network-facing
// listener. It needs one drop-in to dial OUT (IPAddressAllow=any); the allowlist
// is by DOMAIN, which a cgroup IP filter cannot express, so enforcement lives in
// the proxy itself: deny-by-default, never decrypting TLS, every decision audited.
//
// Each workspace gets its own proxy, its own port inside the host-nft-permitted
// range, and its own audit trail, so one workspace's allowlist can never serve
// another's traffic.
type EgressSupervisor struct {
	cfg EgressConfig

	mu      sync.Mutex
	running map[string]*egressProxy // workspace → live proxy
	ports   map[string]int          // workspace → bound port (allocator input)
}

// EgressConfig configures the supervisor.
type EgressConfig struct {
	// BindIP is the bridge host address proxies listen on — the address guests
	// reach them at. "" → the host part of sandbox.DefaultEgressHostCIDR.
	BindIP string
	// PortLow/PortHigh bound the listen port. They MUST match the host nft chain's
	// accepted range (deploy/dev-host.sh writes both from one source). 0 → the
	// sandbox defaults.
	PortLow, PortHigh int
	// StateDir is where per-workspace egress artifacts live
	// (<StateDir>/proxies/<name>/egress-audit.jsonl).
	StateDir string
	// Policy is the node's egress policy — the outer bound every request is
	// intersected with. Nil denies all egress (fail-closed).
	Policy *EgressPolicy
	// Publish forwards each egress decision on ape.audit.<node>.egress. Nil keeps
	// the audit trail file-only.
	Publish func(subject string, data []byte)
	// Node is the slugged node token for the audit subject.
	Node string
	// Stderr receives operational notes (proxy started/stopped); nil → os.Stderr.
	Stderr io.Writer
}

// egressProxy is one workspace's live proxy plus what it took to start it.
type egressProxy struct {
	proxy    *sandbox.Proxy
	addr     string
	port     int
	domains  []string
	auditLog string
	file     *os.File
}

// NewEgressSupervisor builds a supervisor. It starts no listener until Plan is
// called for a workspace.
func NewEgressSupervisor(cfg EgressConfig) *EgressSupervisor {
	return &EgressSupervisor{
		cfg:     cfg,
		running: map[string]*egressProxy{},
		ports:   map[string]int{},
	}
}

func (s *EgressSupervisor) bindIP() string {
	if s.cfg.BindIP != "" {
		return s.cfg.BindIP
	}
	ip, _, err := net.ParseCIDR(sandbox.DefaultEgressHostCIDR)
	if err != nil {
		return "127.0.0.1"
	}
	return ip.String()
}

func (s *EgressSupervisor) stderr() io.Writer {
	if s.cfg.Stderr != nil {
		return s.cfg.Stderr
	}
	return os.Stderr
}

// EgressPlan is the result of planning egress for one workspace: the granted
// allowlist and the proxy URL to inject into the guest. A zero plan (no domains,
// no proxy) means the workspace stays networkless.
type EgressPlan struct {
	Domains  []string
	ProxyURL string
}

// Plan intersects the requested domains with policy, starts (or reuses) the
// workspace's proxy, and returns the plan the resolver folds into the spec.
//
// Fail-closed in three ways: no policy or egress disabled → no egress; every
// requested domain refused → an explicit ErrPolicyDenied (a project asking for
// something policy forbids is told so, rather than silently getting a networkless
// workspace); the proxy failing to bind → an error, never a spec that promises
// egress the guest cannot reach.
func (s *EgressSupervisor) Plan(name string, requested []string) (EgressPlan, error) {
	req := sandbox.SortedDomains(requested)
	if len(req) == 0 {
		return EgressPlan{}, nil
	}
	if s.cfg.Policy == nil || !s.cfg.Policy.Enabled {
		return EgressPlan{}, fmt.Errorf("%w: network egress is disabled on this node (policy egress.enabled)",
			workspace.ErrPolicyDenied)
	}
	granted, refused := sandbox.IntersectDomains(req, s.cfg.Policy.AllowedDomains)
	if len(granted) == 0 {
		return EgressPlan{}, fmt.Errorf("%w: none of the requested egress domains are allowed by policy (%v refused)",
			workspace.ErrPolicyDenied, refused)
	}
	if limit := s.cfg.Policy.MaxDomains; limit > 0 && len(granted) > limit {
		return EgressPlan{}, fmt.Errorf("%w: %d egress domains exceeds the ceiling of %d",
			workspace.ErrPolicyDenied, len(granted), limit)
	}
	if len(refused) > 0 {
		fmt.Fprintf(s.stderr(), "! aped egress %s: narrowed to policy — refused %v\n", name, refused)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Reuse a live proxy only when it enforces exactly the same allowlist; a
	// changed allowlist must not be served by a proxy carrying the old one.
	if p, ok := s.running[name]; ok {
		if equalDomains(p.domains, granted) {
			return EgressPlan{Domains: granted, ProxyURL: "http://" + p.addr}, nil
		}
		s.stopLocked(name)
	}
	p, err := s.startLocked(name, granted)
	if err != nil {
		return EgressPlan{}, err
	}
	return EgressPlan{Domains: granted, ProxyURL: "http://" + p.addr}, nil
}

// startLocked binds and starts one workspace's proxy. Caller holds s.mu.
func (s *EgressSupervisor) startLocked(name string, domains []string) (*egressProxy, error) {
	port, err := sandbox.AllocatePort(s.cfg.PortLow, s.cfg.PortHigh, s.ports)
	if err != nil {
		return nil, err
	}

	auditLog := sandbox.ProxyAuditLogFor(s.cfg.StateDir, name)
	if err := os.MkdirAll(filepath.Dir(auditLog), 0o700); err != nil {
		return nil, fmt.Errorf("aped: egress audit dir for %s: %w", name, err)
	}
	f, err := os.OpenFile(auditLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, egressAuditMode)
	if err != nil {
		return nil, fmt.Errorf("aped: open egress audit log for %s: %w", name, err)
	}
	// Chmod explicitly: the unit sets UMask=0077, which would strip the group bit off
	// the mode passed to OpenFile and leave the trail readable only by the front's own
	// user. An audit trail the operator cannot read is not much of an audit trail —
	// and group `ape` is already the priv-socket gate, so its members are trusted at a
	// strictly higher level than "may read hostnames this workspace connected to".
	if err := f.Chmod(egressAuditMode); err != nil {
		fmt.Fprintf(s.stderr(), "! aped egress %s: could not relax audit log mode: %v\n", name, err)
	}

	proxy := sandbox.NewProxy(sandbox.ProxyConfig{
		Matcher: sandbox.NewMatcher(domains),
		JobID:   "ws:" + name,
		Sink:    s.sinkFor(name, f),
	})
	listen := net.JoinHostPort(s.bindIP(), strconv.Itoa(port))
	if err := proxy.Start(listen); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("aped: start egress proxy for %s on %s: %w "+
			"(is the host↔guest bridge up? systemctl status aped-netbr)", name, listen, err)
	}

	p := &egressProxy{proxy: proxy, addr: proxy.Addr(), port: port, domains: domains, auditLog: auditLog, file: f}
	s.running[name] = p
	s.ports[name] = port
	fmt.Fprintf(s.stderr(), "▶ aped egress %s: proxy on %s, %d domain(s), audit %s\n", name, p.addr, len(domains), auditLog)
	return p, nil
}

// Stop tears down a workspace's proxy (called on Destroy). Unknown names are a
// no-op so teardown is idempotent.
func (s *EgressSupervisor) Stop(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked(name)
}

func (s *EgressSupervisor) stopLocked(name string) {
	p, ok := s.running[name]
	if !ok {
		return
	}
	_ = p.proxy.Close()
	if p.file != nil {
		_ = p.file.Close()
	}
	delete(s.running, name)
	delete(s.ports, name)
	fmt.Fprintf(s.stderr(), "⇣ aped egress %s: proxy stopped\n", name)
}

// StopAll tears down every proxy (front shutdown).
func (s *EgressSupervisor) StopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name := range s.running {
		s.stopLocked(name)
	}
}

// Active reports the live proxies as workspace→address (diagnostics/tests).
func (s *EgressSupervisor) Active() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.running))
	for name, p := range s.running {
		out[name] = p.addr
	}
	return out
}

// sinkFor returns the audit sink for one workspace: always the per-workspace
// JSONL file, plus a NATS publish when the front has a connection. The file is
// the durable trail; the subject is what a live consumer watches.
func (s *EgressSupervisor) sinkFor(name string, f *os.File) sandbox.AuditSink {
	file := sandbox.NewJSONLSink(f)
	if s.cfg.Publish == nil {
		return file
	}
	subject := auditSubject(s.cfg.Node, "Egress")
	return &egressAuditFanout{file: file, publish: s.cfg.Publish, subject: subject, workspace: name}
}

// egressAuditFanout writes each egress decision to the per-workspace JSONL file
// and republishes it on ape.audit.<node>.egress. Publishing is best-effort —
// telemetry must never break the tunnel it observes (the same rule the JSONL sink
// follows).
type egressAuditFanout struct {
	file      sandbox.AuditSink
	publish   func(subject string, data []byte)
	subject   string
	workspace string
}

func (f *egressAuditFanout) Record(e sandbox.EgressAudit) {
	f.file.Record(e)
	data, err := json.Marshal(struct {
		Workspace string `json:"workspace"`
		sandbox.EgressAudit
	}{Workspace: f.workspace, EgressAudit: e})
	if err != nil {
		return
	}
	f.publish(f.subject, data)
}

// equalDomains compares two already-sorted allowlists.
func equalDomains(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
