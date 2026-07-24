package aped

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/sandbox"
	"github.com/exoport/apex_process_ape/internal/workspace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testSupervisor builds a supervisor that binds on loopback (the real bridge
// address is not present in a test environment) with a small port range.
func testSupervisor(t *testing.T, policy *EgressPolicy) *EgressSupervisor {
	t.Helper()
	return NewEgressSupervisor(EgressConfig{
		BindIP:   "127.0.0.1",
		PortLow:  0, // exercise the default range; ports are bound, not assumed
		PortHigh: 0,
		StateDir: t.TempDir(),
		Policy:   policy,
		Node:     "testnode",
		Stderr:   &strings.Builder{},
	})
}

func TestEgressSupervisorNoRequestNoProxy(t *testing.T) {
	s := testSupervisor(t, &EgressPolicy{Enabled: true, AllowedDomains: []string{"github.com"}})
	plan, err := s.Plan("dev", nil)
	require.NoError(t, err)
	assert.Empty(t, plan.Domains)
	assert.Empty(t, plan.ProxyURL, "no request → no proxy is started at all")
	assert.Empty(t, s.Active())
}

func TestEgressSupervisorDeniesWhenPolicyDisabled(t *testing.T) {
	// Nil policy and disabled policy both fail closed.
	for name, pol := range map[string]*EgressPolicy{
		"nil policy":      nil,
		"disabled policy": {Enabled: false, AllowedDomains: []string{"github.com"}},
	} {
		t.Run(name, func(t *testing.T) {
			s := testSupervisor(t, pol)
			_, err := s.Plan("dev", []string{"github.com"})
			require.ErrorIs(t, err, workspace.ErrPolicyDenied)
			assert.Empty(t, s.Active())
		})
	}
}

func TestEgressSupervisorDeniesWhenNothingIsAllowed(t *testing.T) {
	s := testSupervisor(t, &EgressPolicy{Enabled: true, AllowedDomains: []string{"github.com"}})
	_, err := s.Plan("dev", []string{"evil.example.com"})
	require.ErrorIs(t, err, workspace.ErrPolicyDenied)
	assert.Empty(t, s.Active(), "a denied request must not leave a listener behind")
}

func TestEgressSupervisorEnforcesMaxDomains(t *testing.T) {
	s := testSupervisor(t, &EgressPolicy{
		Enabled: true, AllowedDomains: []string{"*.example.com"}, MaxDomains: 1,
	})
	_, err := s.Plan("dev", []string{"a.example.com", "b.example.com"})
	require.ErrorIs(t, err, workspace.ErrPolicyDenied)
}

func TestEgressSupervisorStartsProxyAndNarrowsToPolicy(t *testing.T) {
	s := testSupervisor(t, &EgressPolicy{
		Enabled: true, AllowedDomains: []string{"github.com", "*.githubusercontent.com"},
	})
	defer s.StopAll()

	plan, err := s.Plan("dev", []string{"github.com", "raw.githubusercontent.com", "evil.example.com"})
	require.NoError(t, err)
	// The refused domain is dropped, not fatal: the workspace gets what policy allows.
	assert.Equal(t, []string{"github.com", "raw.githubusercontent.com"}, plan.Domains)

	host, port, err := sandbox.ParseProxyHostPort(plan.ProxyURL)
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", host)
	assert.GreaterOrEqual(t, port, sandbox.DefaultEgressPortLow)
	assert.LessOrEqual(t, port, sandbox.DefaultEgressPortHigh,
		"the port must sit inside the range the host nftables chain accepts")

	// The proxy is really listening.
	conn, err := net.Dial("tcp", net.JoinHostPort(host, plan.ProxyURL[strings.LastIndex(plan.ProxyURL, ":")+1:]))
	require.NoError(t, err)
	_ = conn.Close()

	// Its audit trail exists per workspace.
	assert.FileExists(t, filepath.Join(sandbox.ProxyDirFor(s.cfg.StateDir, "dev"), "egress-audit.jsonl"))
	assert.Len(t, s.Active(), 1)
}

func TestEgressSupervisorReusesProxyForSameAllowlist(t *testing.T) {
	s := testSupervisor(t, &EgressPolicy{Enabled: true, AllowedDomains: []string{"*.example.com"}})
	defer s.StopAll()

	first, err := s.Plan("dev", []string{"a.example.com"})
	require.NoError(t, err)
	again, err := s.Plan("dev", []string{"a.example.com"})
	require.NoError(t, err)
	assert.Equal(t, first.ProxyURL, again.ProxyURL, "same allowlist → same proxy")

	// A CHANGED allowlist replaces the proxy rather than reusing one that enforces
	// the old set. The address may well be identical — the freed port is the lowest
	// available, so it is handed straight back — which is why the reuse decision is
	// made on the allowlist, not on the address.
	changed, err := s.Plan("dev", []string{"a.example.com", "b.example.com"})
	require.NoError(t, err)
	assert.Equal(t, []string{"a.example.com", "b.example.com"}, changed.Domains)
	assert.Len(t, s.Active(), 1, "the superseded proxy is stopped, not leaked")
}

func TestEgressSupervisorStopReleasesPortAndFiles(t *testing.T) {
	s := testSupervisor(t, &EgressPolicy{Enabled: true, AllowedDomains: []string{"github.com"}})
	plan, err := s.Plan("dev", []string{"github.com"})
	require.NoError(t, err)

	s.Stop("dev")
	assert.Empty(t, s.Active())
	s.Stop("dev") // idempotent

	// The listener is gone.
	_, err = net.Dial("tcp", strings.TrimPrefix(plan.ProxyURL, "http://"))
	require.Error(t, err)

	// The freed port is handed to the next workspace.
	next, err := s.Plan("web", []string{"github.com"})
	require.NoError(t, err)
	assert.Equal(t, plan.ProxyURL, next.ProxyURL)
	s.StopAll()
	assert.Empty(t, s.Active())
}

func TestEgressSupervisorPublishesAuditRows(t *testing.T) {
	var subjects []string
	var payloads [][]byte
	s := NewEgressSupervisor(EgressConfig{
		BindIP:   "127.0.0.1",
		StateDir: t.TempDir(),
		Policy:   &EgressPolicy{Enabled: true, AllowedDomains: []string{"github.com"}},
		Publish: func(subject string, data []byte) {
			subjects = append(subjects, subject)
			payloads = append(payloads, data)
		},
		Node:   "testnode",
		Stderr: &strings.Builder{},
	})
	defer s.StopAll()

	plan, err := s.Plan("dev", []string{"github.com"})
	require.NoError(t, err)

	// Drive one DENIED CONNECT through the proxy: a denial is the interesting audit
	// row, and it needs no upstream network to produce.
	addr := strings.TrimPrefix(plan.ProxyURL, "http://")
	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	_, err = conn.Write([]byte("CONNECT blocked.example.com:443 HTTP/1.1\r\nHost: blocked.example.com:443\r\n\r\n"))
	require.NoError(t, err)
	buf := make([]byte, 128)
	_, _ = conn.Read(buf)
	_ = conn.Close()

	require.NotEmpty(t, subjects, "the denial should have been forwarded on NATS")
	assert.Equal(t, "ape.audit.testnode.egress", subjects[0])
	assert.Contains(t, string(payloads[0]), `"workspace":"dev"`)
	assert.Contains(t, string(payloads[0]), `"decision":"denied"`)

	// And the durable per-workspace trail has it too.
	data, err := os.ReadFile(filepath.Join(sandbox.ProxyDirFor(s.cfg.StateDir, "dev"), "egress-audit.jsonl"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "blocked.example.com")
}
