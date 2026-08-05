package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// EgressAudit is one row of the per-job egress-audit.jsonl trail. It is
// per-CONNECT (per TCP tunnel), not per HTTP request — with keep-alive
// many API requests ride one tunnel, so bytes_up/bytes_down/duration_ms
// are per-connection totals. Hostname metadata only; nothing is decrypted.
// snake_case is the on-disk / on-wire contract.
//
//nolint:tagliatelle // stable jsonl/NATS field names
type EgressAudit struct {
	TS         string `json:"ts"`
	JobID      string `json:"job_id"`
	Host       string `json:"host"`
	Port       string `json:"port"`
	Decision   string `json:"decision"` // "allowed" | "denied"
	Reason     string `json:"reason,omitempty"`
	DurationMs int64  `json:"duration_ms"`
	BytesUp    int64  `json:"bytes_up"`
	BytesDown  int64  `json:"bytes_down"`
}

const (
	decisionAllowed = "allowed"
	decisionDenied  = "denied"
)

// AuditSink records egress decisions. The JSONL file sink is the default;
// a NATS-publishing sink is a PLAN-13 follow-up that can wrap this one.
type AuditSink interface {
	Record(e EgressAudit)
}

// JSONLSink writes one JSON object per line to an io.Writer, serialised
// by a mutex so concurrent tunnels don't interleave.
type JSONLSink struct {
	mu sync.Mutex
	w  io.Writer
}

// NewJSONLSink returns a sink writing to w (typically the job runlog's
// egress-audit.jsonl file).
func NewJSONLSink(w io.Writer) *JSONLSink { return &JSONLSink{w: w} }

// Record appends one entry. Write errors are swallowed — the audit trail
// is best-effort and must never break the tunnel it observes.
func (s *JSONLSink) Record(e EgressAudit) {
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.w.Write(append(data, '\n'))
}

// SystemRoute is one exact host:port the NODE grants every workspace through
// its proxy, independent of that workspace's own allowlist (PLAN-24 D5).
//
// It exists because the guest→host path the in-VM agent needs cannot be
// expressed as a domain grant. The alternative — widening ProxyConfig.AllowedPorts
// to admit 4222 — would let the guest reach ANY allowlisted domain on that port,
// which is a real widening; and putting the endpoint in the domain allowlist
// would put it under `egress set`, where the next allowlist rewrite would delete
// the agent's only route home. So it is a separate concept: an exact pair,
// checked before both allowlists, constructed by the daemon from its own
// configuration, with no user-facing surface that can remove it.
//
// It is NOT an audit exemption. A system-route tunnel writes an ordinary
// decision line carrying Reason, so it is visible and filterable in
// egress-audit.jsonl and on ape.audit.<node>.egress like everything else.
type SystemRoute struct {
	// Host is the exact CONNECT authority host. Use a SENTINEL NAME
	// (aped.internal), never a loopback literal: inside the guest "127.0.0.1"
	// means the guest's own loopback, so anything that dials it directly instead
	// of through the proxy fails silently.
	Host string
	// Port is the exact CONNECT authority port.
	Port string
	// Target is the host:port the proxy actually dials, resolved in the PROXY's
	// network namespace. For the agent route this is the front's own loopback:
	// the same process holds the NATS listener, so the far end is a dial it makes
	// to itself.
	Target string
	// Reason is the audit reason recorded for tunnels on this route. It must
	// DISTINGUISH the route ("system route: agent nats"), because being able to
	// tell node-granted traffic from workspace-granted traffic in the trail is
	// the whole basis for accepting the grant.
	Reason string
}

// key is the exact-match lookup key for a route (host:port, host lowercased —
// CONNECT authorities are not case-normalized by clients).
func (r SystemRoute) key() string { return strings.ToLower(r.Host) + ":" + r.Port }

// Proxy is a deny-by-default HTTP CONNECT proxy that runs on the host,
// outside the sandbox. The guest is pointed at it via HTTPS_PROXY. It
// authorises each CONNECT against the domain allowlist, tunnels the
// allowed ones, and records every decision (allowed and denied) to the
// audit sink. It never decrypts — TLS stays end-to-end.
type Proxy struct {
	matcher      *Matcher
	jobID        string
	sink         AuditSink
	allowedPorts map[string]struct{}
	systemRoutes map[string]SystemRoute
	dialTimeout  time.Duration

	srv *http.Server
	ln  net.Listener
	now func() time.Time // injectable clock for tests
}

// ProxyConfig configures a Proxy.
type ProxyConfig struct {
	Matcher      *Matcher
	JobID        string
	Sink         AuditSink
	AllowedPorts []string // default: {"443"}
	// SystemRoutes are node-granted exact destinations checked BEFORE both
	// allowlists (see SystemRoute). Empty is the normal case.
	SystemRoutes []SystemRoute
	DialTimeout  time.Duration
}

// NewProxy builds a Proxy. It does not listen until Start.
func NewProxy(cfg ProxyConfig) *Proxy {
	ports := cfg.AllowedPorts
	if len(ports) == 0 {
		ports = []string{"443"}
	}
	pset := make(map[string]struct{}, len(ports))
	for _, p := range ports {
		pset[p] = struct{}{}
	}
	routes := make(map[string]SystemRoute, len(cfg.SystemRoutes))
	for _, r := range cfg.SystemRoutes {
		if r.Host == "" || r.Port == "" || r.Target == "" {
			continue // an incomplete route grants nothing rather than everything
		}
		routes[r.key()] = r
	}
	dt := cfg.DialTimeout
	if dt == 0 {
		dt = 15 * time.Second
	}
	matcher := cfg.Matcher
	if matcher == nil {
		matcher = NewMatcher(nil) // deny-all
	}
	return &Proxy{
		matcher:      matcher,
		jobID:        cfg.JobID,
		sink:         cfg.Sink,
		allowedPorts: pset,
		systemRoutes: routes,
		dialTimeout:  dt,
		now:          time.Now,
	}
}

// Start binds a listener (addr, e.g. "127.0.0.1:0") and serves in a
// goroutine. The bound address is available via Addr.
func (p *Proxy) Start(addr string) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return fmt.Errorf("proxy listen %s: %w", addr, err)
	}
	p.ln = ln
	p.srv = &http.Server{
		Handler:           http.HandlerFunc(p.handle),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = p.srv.Serve(ln) }()
	return nil
}

// Addr returns the listener address (host:port), or "" before Start.
func (p *Proxy) Addr() string {
	if p.ln == nil {
		return ""
	}
	return p.ln.Addr().String()
}

// ProxyURL returns the HTTPS_PROXY value the guest should use.
func (p *Proxy) ProxyURL() string {
	if p.ln == nil {
		return ""
	}
	return "http://" + p.ln.Addr().String()
}

// Close stops serving and closes the listener.
//
// The listener is closed DIRECTLY as well as through the server: http.Server
// only closes listeners its Serve goroutine has already registered, so a Close
// that races a just-started proxy would return with the socket still accepting
// until that goroutine caught up. Callers that stop a proxy and immediately
// rebind its port (the per-workspace egress supervisor does exactly this) need
// the close to be complete when Close returns. Both closes are idempotent; an
// already-closed listener is not an error.
func (p *Proxy) Close() error {
	var err error
	if p.srv != nil {
		err = p.srv.Close()
	}
	if p.ln != nil {
		if cerr := p.ln.Close(); cerr != nil && err == nil && !errors.Is(cerr, net.ErrClosed) {
			err = cerr
		}
	}
	return err
}

// handle processes one proxied request. Only CONNECT is supported; the
// guest's HTTPS_PROXY makes every https:// request arrive as a CONNECT.
func (p *Proxy) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		http.Error(w, "only CONNECT is supported", http.StatusMethodNotAllowed)
		return
	}
	host, port := splitHostPort(r.Host)

	// System routes are checked FIRST, before the port set and the domain
	// matcher, because they exist precisely to reach a destination neither can
	// express: AllowedPorts defaults to {"443"} and nothing overrides it, so a
	// CONNECT to the agent endpoint would die on the port check below before the
	// matcher was ever consulted.
	if route, ok := p.systemRoutes[strings.ToLower(host)+":"+port]; ok {
		p.tunnel(r.Context(), w, host, port, route.Target, route.Reason)
		return
	}

	if _, ok := p.allowedPorts[port]; !ok {
		p.record(EgressAudit{Host: host, Port: port, Decision: decisionDenied, Reason: "port not allowed"})
		http.Error(w, "port not allowed", http.StatusForbidden)
		return
	}
	if !p.matcher.Allowed(host) {
		p.record(EgressAudit{Host: host, Port: port, Decision: decisionDenied, Reason: "domain not authorized"})
		http.Error(w, "domain not authorized", http.StatusForbidden)
		return
	}
	p.tunnel(r.Context(), w, host, port, net.JoinHostPort(host, port), "")
}

// tunnel dials dialAddr, hijacks the client conn, and copies bytes both ways,
// recording an allowed entry with per-connection byte totals when the tunnel
// closes.
//
// dialAddr is separate from host/port because a system route's far end is not
// the authority the guest asked for: the guest CONNECTs to a sentinel name the
// proxy resolves to a destination of the node's choosing. host/port stay the
// REQUESTED authority so the audit trail records what the guest asked for, and
// reason records why it was granted.
func (p *Proxy) tunnel(ctx context.Context, w http.ResponseWriter, host, port, dialAddr, reason string) {
	start := p.now()
	dialer := net.Dialer{Timeout: p.dialTimeout}
	// Detach the dial from the REQUEST's cancellation, keeping only the dial timeout.
	//
	// CONNECT is a handshake: the client sends its request line and then legitimately
	// sends nothing until it sees "200 Connection Established". Clients that pipe a
	// fixed request in (a shell `printf … | nc`, some agents) half-close their write
	// side at that point, and net/http reacts to the FIN by cancelling the request
	// context — which aborted the upstream dial mid-flight and turned a perfectly good
	// tunnel into a 502 ("lookup …: operation was canceled", observed live
	// 2026-07-24). The client can still RECEIVE, so its half-close says nothing about
	// whether it wants the tunnel.
	//
	// The dial stays bounded by dialer.Timeout, so a genuinely abandoned request costs
	// at most that, not an unbounded goroutine.
	upstream, err := dialer.DialContext(context.WithoutCancel(ctx), "tcp", dialAddr)
	if err != nil {
		p.record(EgressAudit{Host: host, Port: port, Decision: decisionDenied, Reason: "dial failed: " + err.Error()})
		http.Error(w, "upstream dial failed", http.StatusBadGateway)
		return
	}
	defer upstream.Close()

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	client, _, err := hj.Hijack()
	if err != nil {
		return
	}
	defer client.Close()

	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}

	var up, down int64
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); up = copyClose(upstream, client) }()   // client → upstream
	go func() { defer wg.Done(); down = copyClose(client, upstream) }() // upstream → client
	wg.Wait()

	p.record(EgressAudit{
		Host:       host,
		Port:       port,
		Decision:   decisionAllowed,
		Reason:     reason,
		DurationMs: p.now().Sub(start).Milliseconds(),
		BytesUp:    up,
		BytesDown:  down,
	})
}

// copyClose copies src→dst, then half-closes dst if it supports it so the
// peer sees EOF and the paired copy can finish.
func copyClose(dst, src net.Conn) int64 {
	n, _ := io.Copy(dst, src)
	if cw, ok := dst.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
	return n
}

// record stamps the entry with the clock + job id and forwards it to the
// sink (if any).
func (p *Proxy) record(e EgressAudit) {
	if p.sink == nil {
		return
	}
	e.JobID = p.jobID
	if e.TS == "" {
		e.TS = p.now().UTC().Format(time.RFC3339Nano)
	}
	p.sink.Record(e)
}

// splitHostPort splits an authority into host and port, defaulting the
// port to 443 (CONNECT authorities usually carry it, but be defensive).
func splitHostPort(authority string) (host, port string) {
	h, pt, err := net.SplitHostPort(authority)
	if err != nil {
		return authority, "443"
	}
	return h, pt
}
