package sandbox

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlanEgress(t *testing.T) {
	// Explicit --proxy always wins, even alongside an allowlist.
	assert.Equal(t, EgressExplicit, PlanEgress("127.0.0.1:8080", nil))
	assert.Equal(t, EgressExplicit, PlanEgress("127.0.0.1:8080", []string{"api.anthropic.com"}))
	// Allowlist with no --proxy → supervised proxy.
	assert.Equal(t, EgressManaged, PlanEgress("", []string{"api.anthropic.com"}))
	assert.Equal(t, EgressManaged, PlanEgress("   ", []string{"api.anthropic.com"}))
	// Neither → open (default) egress.
	assert.Equal(t, EgressOpen, PlanEgress("", nil))
	assert.Equal(t, EgressOpen, PlanEgress("", []string{}))
}

func TestProxyDaemonArgs(t *testing.T) {
	args := ProxyDaemonArgs("dev", "127.0.0.1:0", "/state/proxies/dev/egress-audit.jsonl",
		[]string{"api.anthropic.com", "*.githubusercontent.com"}, 3)
	assert.Equal(t, []string{
		"sandbox", "_proxyd",
		"--workspace", "dev",
		"--listen", "127.0.0.1:0",
		"--audit", "/state/proxies/dev/egress-audit.jsonl",
		"--ready-fd", "3",
		"--allow", "api.anthropic.com",
		"--allow", "*.githubusercontent.com",
	}, args)

	// Optional pieces are omitted when empty/zero.
	assert.Equal(t,
		[]string{"sandbox", "_proxyd", "--workspace", "dev"},
		ProxyDaemonArgs("dev", "", "", nil, 0))
}

func TestProxyStateProxyURL(t *testing.T) {
	assert.Equal(t, "http://127.0.0.1:5555", ProxyState{Addr: "127.0.0.1:5555"}.ProxyURL())
	assert.Empty(t, ProxyState{}.ProxyURL())
	assert.Empty(t, ProxyState{Addr: "  "}.ProxyURL())
}

func TestProxyDirHelpers(t *testing.T) {
	assert.Equal(t, filepath.FromSlash("/state/proxies/dev"), ProxyDirFor("/state", "dev"))
	assert.Equal(t,
		filepath.FromSlash("/state/proxies/dev/egress-audit.jsonl"),
		ProxyAuditLogFor("/state", "dev"))
}

func TestProxyStartOptionsDefaults(t *testing.T) {
	o := ProxyStartOptions{Dir: "/state/proxies/dev"}
	assert.Equal(t, defaultProxyListen, o.listen())
	assert.Equal(t, "127.0.0.1:1234", ProxyStartOptions{Listen: "127.0.0.1:1234"}.listen())
	assert.Equal(t, filepath.FromSlash("/state/proxies/dev/proxyd.log"), o.logPath())
}

func TestProxySupervisorReadyTimeout(t *testing.T) {
	assert.Equal(t, defaultProxyReadyTimeout, (&ProxySupervisor{}).readyTimeout())
	assert.Equal(t, 2*time.Second, (&ProxySupervisor{ReadyTimeout: 2 * time.Second}).readyTimeout())
}

// TestWorkspaceProxyFieldsRoundTrip locks that the proxy supervisor record
// survives a registry save/load so `down` can find and stop the daemon.
func TestWorkspaceProxyFieldsRoundTrip(t *testing.T) {
	reg := OpenRegistry(t.TempDir())
	require.NoError(t, reg.Put(Workspace{
		Name: "dev", Container: "ape-ws-dev",
		ProxyPID: 4242, ProxyAddr: "127.0.0.1:5555",
		ProxyAuditLog: "/state/proxies/dev/egress-audit.jsonl",
	}))
	got, ok, err := reg.Get("dev")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, 4242, got.ProxyPID)
	assert.Equal(t, "127.0.0.1:5555", got.ProxyAddr)
	assert.Equal(t, "/state/proxies/dev/egress-audit.jsonl", got.ProxyAuditLog)
}

// TestRunProxyDaemon exercises the daemon body in-process (no re-exec): it
// reports its bound address on the readiness fd, denies a non-authorized
// CONNECT, and appends the decision to the audit log. The allowed-tunnel
// path is covered by the proxy tests (TestProxyAllowsAuthorizedHost).
func TestRunProxyDaemon(t *testing.T) {
	if runtime.GOOS == "windows" {
		// The egress proxy daemon is Linux-only, and this harness sets a
		// read deadline on an os.Pipe, which Windows does not support.
		t.Skip("Linux-only: egress proxy daemon + os.Pipe read deadlines")
	}
	auditLog := filepath.Join(t.TempDir(), "egress-audit.jsonl")
	pr, pw, err := os.Pipe()
	require.NoError(t, err)
	defer pr.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- RunProxyDaemon(ctx, DaemonOptions{
			Workspace: "dev",
			Listen:    "127.0.0.1:0",
			AuditLog:  auditLog,
			Allow:     []string{"api.anthropic.com"},
			ReadyFD:   int(pw.Fd()),
		})
	}()

	// The daemon reports its bound loopback address on the readiness fd.
	require.NoError(t, pr.SetReadDeadline(time.Now().Add(3*time.Second)))
	line, err := bufio.NewReader(pr).ReadString('\n')
	require.NoError(t, err)
	addr := strings.TrimSpace(line)
	require.NotEmpty(t, addr)

	// A non-authorized domain is refused (deny-by-default).
	conn, status := connectThrough(t, addr, "evil.example.com", "443")
	_ = conn.Close()
	assert.Contains(t, status, "403")

	// The denial is recorded to the audit log.
	require.Eventually(t, func() bool {
		data, rerr := os.ReadFile(auditLog)
		return rerr == nil &&
			strings.Contains(string(data), decisionDenied) &&
			strings.Contains(string(data), "evil.example.com")
	}, 2*time.Second, 20*time.Millisecond)

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("RunProxyDaemon did not exit after context cancel")
	}
}
