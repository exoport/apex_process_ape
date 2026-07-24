//go:build linux

package netd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recorderScript is a stand-in for ip/bridge/nft: it appends its own argv to
// $APE_NETD_LOG and exits 0. It lets the whole request path be exercised —
// socket, peer gate, allocation, command ORDER — without touching the host
// network, which a unit test must never do.
const recorderScript = `#!/bin/sh
printf '%s' "$(basename "$0")" >> "$APE_NETD_LOG"
for a in "$@"; do printf ' %s' "$a" >> "$APE_NETD_LOG"; done
printf '\n' >> "$APE_NETD_LOG"
exit 0
`

// startRecorderServer runs a helper whose tools are recorder scripts and returns
// the client plus the path of the argv log.
func startRecorderServer(t *testing.T) (client *Client, logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "argv.log")
	t.Setenv("APE_NETD_LOG", logPath)

	bins := map[string]string{}
	for _, name := range []string{"ip", "bridge", "nft"} {
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte(recorderScript), 0o700))
		bins[name] = p
	}

	// AF_UNIX paths are capped near 108 bytes; a per-test temp dir stays well inside
	// that, so the socket lives in its own dir rather than beside the argv log.
	sock := filepath.Join(t.TempDir(), "netd.sock")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	errc := make(chan error, 1)
	go func() {
		errc <- Serve(ctx, ServerConfig{
			Socket:    sock,
			LeaseFile: filepath.Join(dir, "leases.json"),
			// The test process is not root, so allow its own uid through the peer gate
			// (the production default is {0}, the executor).
			AllowedUIDs: []uint32{uint32(os.Getuid())},
			IPBin:       bins["ip"], BridgeBin: bins["bridge"], NftBin: bins["nft"],
			Stderr: &strings.Builder{},
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-errc:
		case <-time.After(3 * time.Second):
			t.Log("netd server did not stop within 3s")
		}
	})

	// Wait for the socket to appear before the first dial.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return &Client{Socket: sock, Timeout: 3 * time.Second}, logPath
}

func TestServeEnsureWiresLinkAndReturnsNetnsPath(t *testing.T) {
	client, logPath := startRecorderServer(t)
	ctx := context.Background()

	require.NoError(t, client.Ping(ctx))

	path, err := client.EnsureNetns(ctx, "dev", 3200, false)
	require.NoError(t, err)
	assert.Equal(t, "/run/netns/ape-dev", path)

	logged, err := os.ReadFile(logPath)
	require.NoError(t, err)
	out := string(logged)

	// The wire-up ran in order, on the real bridge name, with port isolation and the
	// in-netns ruleset.
	assert.Contains(t, out, "ip netns add ape-dev")
	assert.Contains(t, out, "type veth peer name")
	assert.Contains(t, out, "master apebr0")
	assert.Contains(t, out, "isolated on")
	assert.Contains(t, out, "ip -n ape-dev addr add 169.254.42.2/24")
	assert.Contains(t, out, "netns exec ape-dev")
	assert.Regexp(t, `nft -f -`, out)
}

func TestServeEnsureAllocatesDistinctAddressesAndReusesLeases(t *testing.T) {
	client, _ := startRecorderServer(t)
	ctx := context.Background()

	first, err := client.Do(ctx, Request{Op: OpEnsure, Workspace: "dev", ProxyPort: 3200})
	require.NoError(t, err)
	second, err := client.Do(ctx, Request{Op: OpEnsure, Workspace: "web", ProxyPort: 3201})
	require.NoError(t, err)
	assert.Equal(t, "169.254.42.2", first.GuestIP)
	assert.Equal(t, "169.254.42.3", second.GuestIP, "a second workspace must not share an address")

	// Re-ensuring keeps the same address, so a guest is never renumbered.
	again, err := client.Do(ctx, Request{Op: OpEnsure, Workspace: "dev", ProxyPort: 3200})
	require.NoError(t, err)
	assert.Equal(t, "169.254.42.2", again.GuestIP)
	assert.Equal(t, "http://169.254.42.1:3200", again.ProxyURL)

	// After delete the address is free for the next workspace.
	require.NoError(t, client.DeleteNetns(ctx, "dev"))
	next, err := client.Do(ctx, Request{Op: OpEnsure, Workspace: "other", ProxyPort: 3202})
	require.NoError(t, err)
	assert.Equal(t, "169.254.42.2", next.GuestIP)
}

func TestServeRejectsInvalidRequests(t *testing.T) {
	client, _ := startRecorderServer(t)
	ctx := context.Background()

	// Client-side validation catches these before they are sent...
	_, err := client.Do(ctx, Request{Op: OpEnsure, Workspace: "bad name", ProxyPort: 3200})
	require.Error(t, err)
	// ...and a port outside the host-permitted range is refused by the plan, so the
	// wall can never be opened for a port the host chain would drop.
	_, err = client.Do(ctx, Request{Op: OpEnsure, Workspace: "dev", ProxyPort: 9999})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside the host-allowed range")
}

func TestServeDeleteIsIdempotent(t *testing.T) {
	client, _ := startRecorderServer(t)
	ctx := context.Background()
	require.NoError(t, client.DeleteNetns(ctx, "never-created"))
	require.NoError(t, client.DeleteNetns(ctx, "never-created"))
}

func TestServeSocketIsRootOnlyMode(t *testing.T) {
	client, _ := startRecorderServer(t)
	info, err := os.Stat(client.Socket)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"only the root executor may command the helper")
}
