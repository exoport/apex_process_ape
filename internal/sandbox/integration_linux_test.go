//go:build linux

package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tier-2 Kata integration tests (PLAN-16 "Testing tiers"). They provision a
// REAL Kata microVM and therefore need KVM + containerd + Kata + nerdctl +
// the ape-sandbox image on the host. GitHub-hosted runners have no nested
// virt, so these never run in CI — they are opt-in for a local / self-hosted
// box:
//
//	APE_SANDBOX_IT=1 go test -tags '' -run TestIT ./internal/sandbox/
//
// Env knobs:
//
//	APE_SANDBOX_IT        must be "1" to run these at all
//	APE_SANDBOX_IT_IMAGE  workspace image (default sandbox.DefaultImage)
//	APE_SANDBOX_IT_CMD    container command override, space-split (e.g.
//	                      "sleep infinity") for a bare test image that has no
//	                      long-running entrypoint; default: the image default
//	APE_SANDBOX_IT_HOST   address the guest uses to reach the host (the
//	                      container-bridge gateway IP, e.g. 10.4.0.1) — needed
//	                      only by the egress test; it skips without it
//	APE_SANDBOX_IT_PROXY_BIND host address the in-process egress proxy binds
//	                      (default 0.0.0.0:0 so the bridge gateway can reach it)
//	APE_SANDBOX_IT_ALLOW  an allowlisted HTTPS host for the egress test
//	                      (default example.com)

// requireKataIT skips unless the caller opted in AND the host has the bits a
// live Kata workspace needs.
func requireKataIT(t *testing.T) {
	t.Helper()
	if os.Getenv("APE_SANDBOX_IT") != "1" {
		t.Skip("Tier-2: set APE_SANDBOX_IT=1 (needs KVM + containerd + Kata) to run Kata integration tests")
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("no /dev/kvm; Kata microVMs need KVM (nested virt)")
	}
	if _, err := exec.LookPath("nerdctl"); err != nil {
		t.Skip("nerdctl not on PATH; ape drives Kata via nerdctl")
	}
}

func itImage() string {
	if v := strings.TrimSpace(os.Getenv("APE_SANDBOX_IT_IMAGE")); v != "" {
		return v
	}
	return DefaultImage
}

func itCommand() []string {
	if v := strings.TrimSpace(os.Getenv("APE_SANDBOX_IT_CMD")); v != "" {
		return strings.Fields(v)
	}
	return nil // image default (the ape-sandbox entrypoint stays foreground)
}

// itName derives a containerd-safe, per-test workspace name.
func itName(t *testing.T) string {
	t.Helper()
	repl := strings.NewReplacer("/", "-", " ", "-", "_", "-")
	return "it-" + strings.ToLower(repl.Replace(t.Name()))
}

// provisionIT brings up a real workspace for the test and registers teardown
// (container + any named volume). It composes an api-key (mode B) home so no
// host credential file is bound — that lets the isolation test prove the real
// ~/.claude never reaches the guest. mount is a parameter (not hardcoded) so
// volume/ephemeral coverage can be added without reworking the harness.
//
//nolint:unparam // shared harness: callers for volume/ephemeral modes land later
func provisionIT(t *testing.T, mount MountMode, httpsProxy string, allow []string) WorkspaceSpec {
	t.Helper()
	stateDir := t.TempDir()
	name := itName(t)
	staging := StagingDirFor(stateDir, name)
	require.NoError(t, os.MkdirAll(staging, 0o700))

	t.Setenv("APE_SANDBOX_IT_KEY", "sk-it-dummy")
	prof := &Profile{
		Name:         name,
		Backend:      BackendKata,
		VMM:          VMMCloudHypervisor,
		Mount:        mount,
		Credentials:  CredentialAPIKey,
		APIKeySource: "env:APE_SANDBOX_IT_KEY",
		Network:      NetworkPolicy{AuthorizedDomains: allow},
	}
	require.NoError(t, prof.Validate())

	comp, err := Compose(ComposeOptions{Profile: prof, StagingDir: staging})
	require.NoError(t, err)

	spec := WorkspaceSpec{
		Name:       name,
		Image:      itImage(),
		VMM:        VMMCloudHypervisor,
		Mount:      mount,
		Comp:       comp,
		HTTPSProxy: httpsProxy,
		Command:    itCommand(),
	}
	switch mount {
	case MountHostFS:
		spec.ProjectRoot = t.TempDir()
	case MountVolume:
		spec.Volume = ContainerName(name) + "-workspace"
	case MountEphemeral:
		// nothing from the host
	}

	runner := &Runner{}
	require.NoError(t, runner.Provision(context.Background(), spec), "provision workspace")
	t.Cleanup(func() {
		_ = runner.Down(context.Background(), spec.Container())
		if spec.Volume != "" {
			_ = exec.Command("nerdctl", "volume", "rm", "-f", spec.Volume).Run()
		}
	})
	return spec
}

// execCapture runs a non-interactive command in the workspace and returns its
// combined output (on success) and the error (non-nil on a non-zero exit).
func execCapture(t *testing.T, container string, cmd ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	r := &Runner{Stdout: &buf}
	err := r.Exec(context.Background(), container, false, cmd)
	return buf.String(), err
}

// TestIT_HostFSMountAndHomeIsolation asserts the two core boundary
// guarantees: the host-fs project mount is writable both ways, and the real
// host ~/.claude never reaches the guest (mode B binds no credential file).
func TestIT_HostFSMountAndHomeIsolation(t *testing.T) {
	requireKataIT(t)
	spec := provisionIT(t, MountHostFS, "", nil)

	// A file written in-guest at the project mount appears on the host.
	_, err := execCapture(t, spec.Container(), "sh", "-c", "echo hello-from-guest > "+DefaultProjectDest+"/it.txt")
	require.NoError(t, err)
	onHost, err := os.ReadFile(spec.ProjectRoot + "/it.txt")
	require.NoError(t, err)
	assert.Equal(t, "hello-from-guest\n", string(onHost))

	// And a file created on the host is visible in-guest (bidirectional).
	require.NoError(t, os.WriteFile(spec.ProjectRoot+"/from-host.txt", []byte("hi"), 0o600))
	out, err := execCapture(t, spec.Container(), "cat", DefaultProjectDest+"/from-host.txt")
	require.NoError(t, err)
	assert.Equal(t, "hi", strings.TrimSpace(out))

	// Mode B binds no credential file, so the guest has no .credentials.json
	// and no window onto the real host home.
	_, err = execCapture(t, spec.Container(), "test", "!", "-e", DefaultGuestHome+"/.claude/.credentials.json")
	require.NoError(t, err, "guest must not see a real credentials file in mode B")
	// The composed onboarding marker IS present (proves the staged home, not
	// the host home, is mounted).
	_, err = execCapture(t, spec.Container(), "test", "-f", DefaultGuestHome+"/.claude.json")
	require.NoError(t, err, "composed ~/.claude.json must be present in the guest home")
}

// TestIT_ExecRuns confirms the exec path reaches a live guest. (attach is an
// interactive login shell — Tier-3 manual.)
func TestIT_ExecRuns(t *testing.T) {
	requireKataIT(t)
	spec := provisionIT(t, MountHostFS, "", nil)

	out, err := execCapture(t, spec.Container(), "echo", "ok")
	require.NoError(t, err)
	assert.Equal(t, "ok", strings.TrimSpace(out))

	_, err = execCapture(t, spec.Container(), "true")
	require.NoError(t, err)
	_, err = execCapture(t, spec.Container(), "false")
	require.Error(t, err, "a non-zero exit must surface as an error")
}

// TestIT_FreezeUnfreezePreservesState cgroup-freezes and thaws the guest and
// asserts in-guest (tmpfs) state survives the round-trip.
func TestIT_FreezeUnfreezePreservesState(t *testing.T) {
	requireKataIT(t)
	spec := provisionIT(t, MountHostFS, "", nil)
	runner := &Runner{}

	_, err := execCapture(t, spec.Container(), "sh", "-c", "echo persisted > /tmp/it-state")
	require.NoError(t, err)

	require.NoError(t, runner.Freeze(context.Background(), spec.Container()))
	require.NoError(t, runner.Unfreeze(context.Background(), spec.Container()))

	out, err := execCapture(t, spec.Container(), "cat", "/tmp/it-state")
	require.NoError(t, err)
	assert.Equal(t, "persisted", strings.TrimSpace(out))
}

// TestIT_DownLeavesNoContainer tears the workspace down and asserts the
// container is actually gone (no leaked container). Staging/registry cleanup
// is the CLI's responsibility and is unit-tested separately.
func TestIT_DownLeavesNoContainer(t *testing.T) {
	requireKataIT(t)
	spec := provisionIT(t, MountHostFS, "", nil)
	runner := &Runner{}

	require.NoError(t, runner.Down(context.Background(), spec.Container()))

	// `nerdctl inspect` on a removed container must fail.
	err := exec.Command("nerdctl", "inspect", spec.Container()).Run()
	assert.Error(t, err, "container must not exist after down")
}

// TestIT_EgressAllowlistedAndAudited proves the CONNECT proxy enforces the
// allowlist from inside the guest and audits both decisions. The proxy runs
// in-process (host side); the guest reaches it over the bridge gateway
// (APE_SANDBOX_IT_HOST). Requires curl in the image.
func TestIT_EgressAllowlistedAndAudited(t *testing.T) {
	requireKataIT(t)
	hostAddr := strings.TrimSpace(os.Getenv("APE_SANDBOX_IT_HOST"))
	if hostAddr == "" {
		t.Skip("set APE_SANDBOX_IT_HOST to the guest-reachable host IP (bridge gateway) to run the egress test")
	}
	allowHost := strings.TrimSpace(os.Getenv("APE_SANDBOX_IT_ALLOW"))
	if allowHost == "" {
		allowHost = "example.com"
	}
	bind := strings.TrimSpace(os.Getenv("APE_SANDBOX_IT_PROXY_BIND"))
	if bind == "" {
		bind = "0.0.0.0:0"
	}

	auditLog := t.TempDir() + "/egress-audit.jsonl"
	pr, pw, err := os.Pipe()
	require.NoError(t, err)
	defer pr.Close()

	// t.Context() is cancelled at test cleanup, stopping the daemon goroutine.
	go func() {
		_ = RunProxyDaemon(t.Context(), DaemonOptions{
			Workspace: "it-egress",
			Listen:    bind,
			AuditLog:  auditLog,
			Allow:     []string{allowHost},
			ReadyFD:   int(pw.Fd()),
		})
	}()

	require.NoError(t, pr.SetReadDeadline(time.Now().Add(3*time.Second)))
	line, err := bufio.NewReader(pr).ReadString('\n')
	require.NoError(t, err)
	bound := strings.TrimSpace(line) // e.g. 0.0.0.0:PORT
	_, port, ok := strings.Cut(bound, ":")
	require.True(t, ok, "readiness addr %q has no port", bound)

	proxyURL := "http://" + hostAddr + ":" + port
	spec := provisionIT(t, MountHostFS, proxyURL, []string{allowHost})

	// Allowlisted host: the tunnel is established (curl may return any HTTP
	// status; the point is the proxy allowed the CONNECT).
	_, _ = execCapture(t, spec.Container(), "curl", "-sS", "-o", "/dev/null", "https://"+allowHost)

	// Non-allowlisted host: the proxy refuses the CONNECT, so curl fails.
	_, denyErr := execCapture(t, spec.Container(), "curl", "-sS", "-o", "/dev/null", "https://denied.invalid")
	require.Error(t, denyErr, "non-allowlisted egress must be refused")

	// The audit trail records both decisions.
	require.Eventually(t, func() bool {
		var allowed, denied bool
		for _, e := range readAudit(t, auditLog) {
			if e.Decision == decisionAllowed && e.Host == allowHost {
				allowed = true
			}
			if e.Decision == decisionDenied && strings.Contains(e.Host, "denied.invalid") {
				denied = true
			}
		}
		return allowed && denied
	}, 10*time.Second, 100*time.Millisecond, "audit must record an allow for %s and a deny for denied.invalid", allowHost)
}

// readAudit parses the egress-audit.jsonl rows written so far.
func readAudit(t *testing.T, path string) []EgressAudit {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []EgressAudit
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var e EgressAudit
		if json.Unmarshal([]byte(line), &e) == nil {
			out = append(out, e)
		}
	}
	return out
}
