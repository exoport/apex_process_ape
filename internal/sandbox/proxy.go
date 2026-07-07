package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
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

// Close stops serving.
func (p *Proxy) Close() error {
	if p.srv == nil {
		return nil
	}
	return p.srv.Close()
}

// handle processes one proxied request. Only CONNECT is supported; the
// guest's HTTPS_PROXY makes every https:// request arrive as a CONNECT.
func (p *Proxy) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		http.Error(w, "only CONNECT is supported", http.StatusMethodNotAllowed)
		return
	}
	host, port := splitHostPort(r.Host)

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
	p.tunnel(r.Context(), w, host, port)
}

// tunnel dials the target, hijacks the client conn, and copies bytes both
// ways, recording an allowed entry with per-connection byte totals when
// the tunnel closes.
func (p *Proxy) tunnel(ctx context.Context, w http.ResponseWriter, host, port string) {
	start := p.now()
	dialer := net.Dialer{Timeout: p.dialTimeout}
	upstream, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
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
