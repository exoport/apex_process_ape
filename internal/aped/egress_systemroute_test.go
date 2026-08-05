package aped

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/sandbox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The claim PLAN-24 D5 makes about the system route, tested at the level where
// it could actually break: the SUPERVISOR, which is what `egress set` drives.

// routeEchoTarget starts a TCP echo server and returns its address — a stand-in
// for the front's own loopback NATS listener.
func routeEchoTarget(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()
	return ln.Addr().String()
}

// routedSupervisor is testSupervisor plus one system route at target, on a
// private port window. Each test gets its own window because the supervisor
// allocates the LOWEST free port in the range and binds it for the test's
// lifetime — sharing the default range makes tests collide on 3128 depending on
// which one tore down first.
func routedSupervisor(t *testing.T, policy *EgressPolicy, target string, portLow int) *EgressSupervisor {
	t.Helper()
	s := NewEgressSupervisor(EgressConfig{
		BindIP:   "127.0.0.1",
		PortLow:  portLow,
		PortHigh: portLow + 15,
		StateDir: t.TempDir(),
		Policy:   policy,
		SystemRoutes: []sandbox.SystemRoute{{
			Host:   sandbox.AgentNatsHost,
			Port:   "4222",
			Target: target,
			Reason: sandbox.SystemRouteReasonAgentNats,
		}},
		Node:   "testnode",
		Stderr: &strings.Builder{},
	})
	t.Cleanup(s.StopAll)
	return s
}

// reachesAgentEndpoint reports whether a CONNECT to the sentinel through
// proxyURL is tunnelled end to end.
func reachesAgentEndpoint(t *testing.T, proxyURL string) bool {
	t.Helper()
	addr := strings.TrimPrefix(proxyURL, "http://")
	c, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer c.Close()
	fmt.Fprintf(c, "CONNECT %s:4222 HTTP/1.1\r\nHost: %s:4222\r\n\r\n",
		sandbox.AgentNatsHost, sandbox.AgentNatsHost)
	br := bufio.NewReader(c)
	status, err := br.ReadString('\n')
	require.NoError(t, err)
	if !strings.Contains(status, "200") {
		return false
	}
	for {
		line, rerr := br.ReadString('\n')
		require.NoError(t, rerr)
		if strings.TrimSpace(line) == "" {
			break
		}
	}
	// Prove it is a real tunnel to the target, not just a 200.
	if _, werr := c.Write([]byte("INFO")); werr != nil {
		return false
	}
	buf := make([]byte, 4)
	if _, rerr := io.ReadFull(br, buf); rerr != nil {
		return false
	}
	return string(buf) == "INFO"
}

// The property the plan asks for by name: an `egress set` that rewrites EVERY
// domain must not take the agent's route with it. The set verb replaces the
// workspace's proxy wholesale (a live proxy carrying the old allowlist must not
// serve the new one), so the route has to be re-applied by construction rather
// than carried across — this is the test that it is.
func TestSystemRouteSurvivesAnEgressSetThatRewritesEveryDomain(t *testing.T) {
	target := routeEchoTarget(t)
	s := routedSupervisor(t, &EgressPolicy{
		Enabled:        true,
		AllowedDomains: []string{"github.com", "api.anthropic.com"},
	}, target, 3600)

	first, err := s.Plan("dev", []string{"github.com"})
	require.NoError(t, err)
	require.True(t, reachesAgentEndpoint(t, first.ProxyURL), "the route was absent on the first proxy")

	// Exactly what `egress set` does: a completely different allowlist, which
	// forces the supervisor to stop the live proxy and start a new one.
	second, err := s.Plan("dev", []string{"api.anthropic.com"})
	require.NoError(t, err)
	assert.Equal(t, []string{"api.anthropic.com"}, second.Domains, "the allowlist did not actually change")
	assert.True(t, reachesAgentEndpoint(t, second.ProxyURL),
		"an egress set that rewrote every domain deleted the agent's route home")
}

// A front restart rebuilds proxies from the recorded per-workspace state, which
// records domains and a port and nothing else. The route must come back anyway —
// it is the successor supervisor's configuration, not the record's.
func TestSystemRouteComesBackAfterAFrontRestart(t *testing.T) {
	target := routeEchoTarget(t)
	state := t.TempDir()
	policy := &EgressPolicy{Enabled: true, AllowedDomains: []string{"github.com"}}

	build := func() *EgressSupervisor {
		return NewEgressSupervisor(EgressConfig{
			BindIP:   "127.0.0.1",
			PortLow:  3620,
			PortHigh: 3635,
			StateDir: state,
			Policy:   policy,
			SystemRoutes: []sandbox.SystemRoute{{
				Host: sandbox.AgentNatsHost, Port: "4222",
				Target: target, Reason: sandbox.SystemRouteReasonAgentNats,
			}},
			Node:   "testnode",
			Stderr: &strings.Builder{},
		})
	}

	first := build()
	plan, err := first.Plan("dev", []string{"github.com"})
	require.NoError(t, err)
	require.True(t, reachesAgentEndpoint(t, plan.ProxyURL))
	first.StopAll() // the front shutting down: proxies closed, state REMEMBERED

	successor := build()
	successor.RestoreAll()
	defer successor.StopAll()

	active := successor.Active()
	addr, ok := active["dev"]
	require.True(t, ok, "the restored proxy is missing")
	assert.True(t, reachesAgentEndpoint(t, "http://"+addr),
		"the agent's route did not survive a front restart")
}

// Without a configured route, nothing changes: a workspace still cannot reach
// the sentinel. The grant comes from node configuration, never from the guest
// naming an address.
func TestNoSystemRouteConfiguredMeansNoAgentEndpoint(t *testing.T) {
	s := NewEgressSupervisor(EgressConfig{
		BindIP:   "127.0.0.1",
		PortLow:  3640,
		PortHigh: 3655,
		StateDir: t.TempDir(),
		Policy:   &EgressPolicy{Enabled: true, AllowedDomains: []string{"github.com"}},
		Node:     "testnode",
		Stderr:   &strings.Builder{},
	})
	t.Cleanup(s.StopAll)
	plan, err := s.Plan("dev", []string{"github.com"})
	require.NoError(t, err)
	assert.False(t, reachesAgentEndpoint(t, plan.ProxyURL),
		"a node with no agent endpoint configured tunnelled to the sentinel anyway")
}
