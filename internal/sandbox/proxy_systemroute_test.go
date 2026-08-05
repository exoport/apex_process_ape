package sandbox

import (
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The system route (PLAN-24 D5). It is the one destination a workspace reaches
// that its own allowlist does not name, so each property below is a claim the
// design makes and has to be able to prove.

// systemRouteProxy builds a proxy whose allowlist grants nothing, with one
// system route pointing the sentinel authority at addr.
func systemRouteProxy(t *testing.T, sink AuditSink, target string) *Proxy {
	t.Helper()
	p := NewProxy(ProxyConfig{
		Matcher: NewMatcher(nil), // deny-all: the route must not depend on the allowlist
		JobID:   "ws:test",
		Sink:    sink,
		SystemRoutes: []SystemRoute{{
			Host:   AgentNatsHost,
			Port:   "4222",
			Target: target,
			Reason: SystemRouteReasonAgentNats,
		}},
	})
	require.NoError(t, p.Start("127.0.0.1:0"))
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// The route must survive the DEFAULT port set. AllowedPorts defaults to {"443"}
// and nothing overrides it, so a CONNECT to :4222 dies on the port check unless
// system routes are consulted first — which is exactly why this is a separate
// concept and not a config tweak.
func TestSystemRouteBypassesTheDefaultPortSet(t *testing.T) {
	host, port := echoServer(t)
	sink := &capSink{}
	p := systemRouteProxy(t, sink, host+":"+port)

	conn, status := connectThrough(t, p.Addr(), AgentNatsHost, "4222")
	assert.Contains(t, status, "200", "the system route did not survive the default 443-only port set")

	_, err := conn.Write([]byte("INFO"))
	require.NoError(t, err)
	buf := make([]byte, 4)
	_, err = io.ReadFull(conn, buf)
	require.NoError(t, err)
	assert.Equal(t, "INFO", string(buf), "bytes did not reach the route's target")
	_ = conn.Close()

	require.Eventually(t, func() bool { return len(sink.all()) == 1 }, time.Second, 10*time.Millisecond)
	e := sink.all()[0]
	assert.Equal(t, decisionAllowed, e.Decision)
	// The audit line records what the GUEST asked for, not where the proxy sent
	// it — the trail is a record of guest behaviour.
	assert.Equal(t, AgentNatsHost, e.Host)
	assert.Equal(t, "4222", e.Port)
	// ...and it is labelled, which is the property that makes granting the route
	// reviewable at all.
	assert.Equal(t, SystemRouteReasonAgentNats, e.Reason)
}

// The route is EXACT. A workspace must not be able to reach anything else on the
// sentinel, nor the sentinel on another port.
func TestSystemRouteIsExactMatch(t *testing.T) {
	host, port := echoServer(t)
	sink := &capSink{}
	p := systemRouteProxy(t, sink, host+":"+port)

	for _, tc := range []struct{ host, port string }{
		{AgentNatsHost, "443"},            // right host, wrong port
		{"evil." + AgentNatsHost, "4222"}, // subdomain of the sentinel
		{"aped.internal.evil.com", "4222"},
	} {
		conn, status := connectThrough(t, p.Addr(), tc.host, tc.port)
		_ = conn.Close()
		assert.NotContains(t, status, "200", "%s:%s was tunnelled by an exact-match route", tc.host, tc.port)
	}
}

// A workspace's ordinary allowlist must still be enforced with a route present:
// the route adds one destination, it does not open the proxy.
func TestSystemRouteDoesNotWidenTheAllowlist(t *testing.T) {
	host, port := echoServer(t)
	sink := &capSink{}
	p := systemRouteProxy(t, sink, host+":"+port)

	conn, status := connectThrough(t, p.Addr(), "api.anthropic.com", "443")
	_ = conn.Close()
	assert.Contains(t, status, "403", "a deny-all allowlist stopped denying once a system route existed")
}

// An incomplete route grants nothing rather than everything.
func TestIncompleteSystemRouteIsIgnored(t *testing.T) {
	p := NewProxy(ProxyConfig{
		Matcher: NewMatcher(nil),
		SystemRoutes: []SystemRoute{
			{Host: AgentNatsHost, Port: "4222"}, // no target
			{Host: "", Port: "4222", Target: "127.0.0.1:1"},
		},
	})
	require.NoError(t, p.Start("127.0.0.1:0"))
	defer p.Close()

	conn, status := connectThrough(t, p.Addr(), AgentNatsHost, "4222")
	_ = conn.Close()
	assert.NotContains(t, status, "200", "a route with no target tunnelled anyway")
}

// CONNECT authorities are not case-normalized by clients, so the match must be.
func TestSystemRouteMatchIsCaseInsensitiveOnHost(t *testing.T) {
	host, port := echoServer(t)
	p := systemRouteProxy(t, &capSink{}, host+":"+port)

	conn, status := connectThrough(t, p.Addr(), "APED.Internal", "4222")
	_ = conn.Close()
	assert.Contains(t, status, "200", "an upper-case authority missed the route")
}

// An ordinary allowed tunnel must keep recording an EMPTY reason, so "reason is
// set" stays a reliable filter for node-granted traffic.
func TestOrdinaryTunnelRecordsNoSystemReason(t *testing.T) {
	host, port := echoServer(t)
	sink := &capSink{}
	p := NewProxy(ProxyConfig{
		Matcher:      NewMatcher([]string{host}),
		Sink:         sink,
		AllowedPorts: []string{port},
		SystemRoutes: []SystemRoute{{Host: AgentNatsHost, Port: "4222", Target: "127.0.0.1:1", Reason: SystemRouteReasonAgentNats}},
	})
	require.NoError(t, p.Start("127.0.0.1:0"))
	defer p.Close()

	conn, status := connectThrough(t, p.Addr(), host, port)
	assert.Contains(t, status, "200")
	_ = conn.Close()

	require.Eventually(t, func() bool { return len(sink.all()) == 1 }, time.Second, 10*time.Millisecond)
	assert.Empty(t, sink.all()[0].Reason, "an ordinary allowlisted tunnel was labelled as a system route")
}
