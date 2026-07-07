package sandbox

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capSink captures audit entries for assertions.
type capSink struct {
	mu      sync.Mutex
	entries []EgressAudit
}

func (c *capSink) Record(e EgressAudit) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, e)
}

func (c *capSink) all() []EgressAudit {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]EgressAudit(nil), c.entries...)
}

// echoServer starts a TCP server that echoes back whatever it reads.
// Returns its host and port.
func echoServer(t *testing.T) (host, port string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()
	h, p, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)
	return h, p
}

// connectThrough dials the proxy, issues a CONNECT for host:port, and
// returns the tunneled connection (after the 200 line) plus the status.
func connectThrough(t *testing.T, proxyAddr, host, port string) (conn net.Conn, status string) {
	t.Helper()
	c, err := net.Dial("tcp", proxyAddr)
	require.NoError(t, err)
	fmt.Fprintf(c, "CONNECT %s:%s HTTP/1.1\r\nHost: %s:%s\r\n\r\n", host, port, host, port)
	br := bufio.NewReader(c)
	status, err = br.ReadString('\n')
	require.NoError(t, err)
	// Drain the rest of the response headers.
	for {
		line, err := br.ReadString('\n')
		require.NoError(t, err)
		if strings.TrimSpace(line) == "" {
			break
		}
	}
	return c, strings.TrimSpace(status)
}

func TestProxyAllowsAuthorizedHost(t *testing.T) {
	host, port := echoServer(t)
	sink := &capSink{}
	p := NewProxy(ProxyConfig{
		Matcher:      NewMatcher([]string{host}),
		JobID:        "job-1",
		Sink:         sink,
		AllowedPorts: []string{port}, // allow the ephemeral echo port for the test
	})
	require.NoError(t, p.Start("127.0.0.1:0"))
	defer p.Close()

	conn, status := connectThrough(t, p.Addr(), host, port)
	assert.Contains(t, status, "200")

	_, err := conn.Write([]byte("ping"))
	require.NoError(t, err)
	buf := make([]byte, 4)
	_, err = io.ReadFull(conn, buf)
	require.NoError(t, err)
	assert.Equal(t, "ping", string(buf))
	_ = conn.Close()

	// Wait for the tunnel to finish and record.
	require.Eventually(t, func() bool { return len(sink.all()) == 1 }, time.Second, 10*time.Millisecond)
	e := sink.all()[0]
	assert.Equal(t, decisionAllowed, e.Decision)
	assert.Equal(t, "job-1", e.JobID)
	assert.Equal(t, host, e.Host)
	assert.NotEmpty(t, e.TS)
	assert.GreaterOrEqual(t, e.BytesUp, int64(4))
	assert.GreaterOrEqual(t, e.BytesDown, int64(4))
}

func TestProxyDeniesUnauthorizedDomain(t *testing.T) {
	sink := &capSink{}
	p := NewProxy(ProxyConfig{
		Matcher:      NewMatcher([]string{"api.anthropic.com"}),
		Sink:         sink,
		AllowedPorts: []string{"443"},
	})
	require.NoError(t, p.Start("127.0.0.1:0"))
	defer p.Close()

	conn, status := connectThrough(t, p.Addr(), "evil.example.com", "443")
	_ = conn.Close()
	assert.Contains(t, status, "403")

	require.Len(t, sink.all(), 1)
	e := sink.all()[0]
	assert.Equal(t, decisionDenied, e.Decision)
	assert.Equal(t, "evil.example.com", e.Host)
	assert.Contains(t, e.Reason, "not authorized")
}

func TestProxyDeniesDisallowedPort(t *testing.T) {
	sink := &capSink{}
	p := NewProxy(ProxyConfig{
		Matcher:      NewMatcher([]string{"api.anthropic.com"}),
		Sink:         sink,
		AllowedPorts: []string{"443"},
	})
	require.NoError(t, p.Start("127.0.0.1:0"))
	defer p.Close()

	conn, status := connectThrough(t, p.Addr(), "api.anthropic.com", "22")
	_ = conn.Close()
	assert.Contains(t, status, "403")
	require.Len(t, sink.all(), 1)
	assert.Equal(t, decisionDenied, sink.all()[0].Decision)
	assert.Contains(t, sink.all()[0].Reason, "port")
}

func TestProxyRejectsNonConnect(t *testing.T) {
	p := NewProxy(ProxyConfig{Matcher: NewMatcher([]string{"x.com"})})
	require.NoError(t, p.Start("127.0.0.1:0"))
	defer p.Close()

	resp, err := http.Get("http://" + p.Addr() + "/")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}

func TestJSONLSink(t *testing.T) {
	var buf bytes.Buffer
	s := NewJSONLSink(&buf)
	s.Record(EgressAudit{Host: "a.com", Decision: decisionAllowed})
	s.Record(EgressAudit{Host: "b.com", Decision: decisionDenied})

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 2)
	var e0 EgressAudit
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &e0))
	assert.Equal(t, "a.com", e0.Host)
	assert.Equal(t, decisionAllowed, e0.Decision)
}
