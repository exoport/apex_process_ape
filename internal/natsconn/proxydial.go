package natsconn

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Dialing NATS through an HTTP CONNECT proxy (PLAN-24 D5).
//
// A sandbox workspace has exactly one route off the box: the per-workspace
// CONNECT proxy on the host bridge. That is fine for HTTPS, which every client
// reaches through HTTPS_PROXY, and useless for NATS, whose client speaks its own
// protocol on a raw TCP conn and knows nothing about proxies.
//
// The bridge is the CONNECT verb itself: it produces a bidirectional byte
// tunnel, and what rides it afterwards is opaque to the proxy. So the dialer
// below does the HTTP handshake and hands NATS back an ordinary net.Conn, over
// which NATS then runs its own INFO/CONNECT exchange. (NATS's protocol also has
// a verb spelled CONNECT. There is no conflict — the HTTP one has completed and
// the proxy is a pipe by the time NATS says a word.)

// proxyDialTimeout bounds the dial + handshake. Short: the proxy is one hop away
// on a host bridge, and a client that cannot reach it is not going to be helped
// by waiting.
const proxyDialTimeout = 10 * time.Second

// ProxyDialer is a nats.CustomDialer that tunnels through an HTTP CONNECT proxy.
type ProxyDialer struct {
	// ProxyAddr is the proxy's host:port (no scheme).
	ProxyAddr string
	// Timeout bounds the TCP dial and the CONNECT handshake together. Zero uses
	// proxyDialTimeout.
	Timeout time.Duration
}

// Dial opens a TCP conn to the proxy, performs the CONNECT handshake for
// address, and returns the tunnelled conn.
//
// network is ignored beyond a sanity check: a CONNECT tunnel is TCP by
// definition, and a caller asking for udp has misconfigured something in a way
// that must not silently downgrade.
func (d *ProxyDialer) Dial(network, address string) (net.Conn, error) {
	if !strings.HasPrefix(network, "tcp") {
		return nil, fmt.Errorf("natsconn: CONNECT tunnels are TCP; got network %q", network)
	}
	if strings.TrimSpace(d.ProxyAddr) == "" {
		return nil, errors.New("natsconn: no proxy address configured for the CONNECT dialer")
	}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = proxyDialTimeout
	}
	deadline := time.Now().Add(timeout)

	// The nats.CustomDialer contract hands us no context, so the timeout is the
	// only bound available — which is fine here: the proxy is one hop away on a
	// host bridge, and the caller's own connect timeout sits above this.
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(context.Background(), "tcp", d.ProxyAddr)
	if err != nil {
		return nil, fmt.Errorf("natsconn: dial proxy %s: %w", d.ProxyAddr, err)
	}
	if err := d.handshake(conn, address, deadline); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// handshake writes the CONNECT request and consumes the response, leaving conn
// positioned at the first tunnelled byte.
//
// The response is read with a bufio.Reader bounded to the response itself:
// http.ReadResponse would otherwise be free to buffer past the header and
// swallow the first bytes the peer sends, which for NATS is the INFO line the
// client needs. Requesting no body (a CONNECT 200 has none) and reading through
// a reader we then discard is safe only because the server cannot send tunnel
// data before we do — NATS servers send INFO on connect, so this matters.
func (d *ProxyDialer) handshake(conn net.Conn, address string, deadline time.Time) error {
	_ = conn.SetDeadline(deadline)
	defer func() { _ = conn.SetDeadline(time.Time{}) }()

	req := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: address},
		Host:   address,
		Header: http.Header{},
	}
	if err := req.Write(conn); err != nil {
		return fmt.Errorf("natsconn: write CONNECT %s: %w", address, err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return fmt.Errorf("natsconn: read CONNECT response for %s: %w", address, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("natsconn: proxy %s refused CONNECT %s: %s", d.ProxyAddr, address, resp.Status)
	}
	if br.Buffered() > 0 {
		// The proxy sent tunnel bytes in the same read as the response header. We
		// cannot hand those to NATS through a bare net.Conn, and dropping them would
		// eat the server's INFO line and hang the client on a connect that looks
		// healthy. Refuse loudly instead — with a deny-by-default proxy that answers
		// the handshake before dialing upstream, this does not happen.
		return fmt.Errorf("natsconn: proxy %s sent %d byte(s) of tunnel data with its CONNECT response",
			d.ProxyAddr, br.Buffered())
	}
	return nil
}

// ProxyAddrFromEnv resolves the CONNECT proxy address from the standard proxy
// environment, which aped already injects into every workspace (HTTPS_PROXY).
// Returns "" when no proxy is configured — the caller then dials directly, which
// is correct off a sandbox.
//
// NO_PROXY is deliberately NOT consulted. aped sets it to
// "localhost,127.0.0.1" so the guest's sshd does not try to proxy its own
// loopback, and the agent's endpoint is a sentinel NAME precisely so it is not
// caught by that — but a future NO_PROXY entry must not be able to silently
// strip the agent's only route home. This dialer is used where the proxy is the
// route, not where it is an optimisation.
func ProxyAddrFromEnv() string {
	for _, key := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if addr := proxyAddr(os.Getenv(key)); addr != "" {
			return addr
		}
	}
	return ""
}

// proxyAddr normalizes a proxy env value ("http://169.254.42.1:3128", or a bare
// "169.254.42.1:3128") to host:port.
func proxyAddr(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	if u.Port() == "" {
		return net.JoinHostPort(u.Hostname(), "80")
	}
	return u.Host
}
