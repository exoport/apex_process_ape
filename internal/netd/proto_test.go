package netd

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestValidate(t *testing.T) {
	require.NoError(t, Request{Op: OpPing}.Validate())
	require.NoError(t, Request{Op: OpEnsure, Workspace: "dev", ProxyPort: 3128}.Validate())
	require.NoError(t, Request{Op: OpDelete, Workspace: "dev"}.Validate())

	// Default-deny on the verb, and the workspace name is bounded because it becomes
	// part of a netns/interface name and an argv token.
	require.Error(t, Request{Op: "wat", Workspace: "dev"}.Validate())
	require.Error(t, Request{Op: OpEnsure, Workspace: "../etc", ProxyPort: 3128}.Validate())
	require.Error(t, Request{Op: OpEnsure, Workspace: "dev; rm -rf /", ProxyPort: 3128}.Validate())
	require.Error(t, Request{Op: OpEnsure, Workspace: "dev"}.Validate(), "ensure needs a proxy port")
}

func TestRequestResponseRoundTrip(t *testing.T) {
	frame, err := EncodeRequest(Request{Op: OpEnsure, Workspace: "dev", ProxyPort: 3200, Reuse: true})
	require.NoError(t, err)
	assert.Equal(t, byte('\n'), frame[len(frame)-1], "frames are newline-terminated")

	got, err := DecodeRequest(frame)
	require.NoError(t, err)
	assert.Equal(t, Request{Op: OpEnsure, Workspace: "dev", ProxyPort: 3200, Reuse: true}, got)

	rframe, err := EncodeResponse(Response{NetnsPath: "/run/netns/ape-dev", GuestIP: "169.254.42.2"})
	require.NoError(t, err)
	resp, err := DecodeResponse(rframe)
	require.NoError(t, err)
	assert.Equal(t, "/run/netns/ape-dev", resp.NetnsPath)
	require.NoError(t, resp.Err())

	failed, err := DecodeResponse([]byte(`{"error":"nope"}` + "\n"))
	require.NoError(t, err)
	require.Error(t, failed.Err())
}

func TestLeasesPersistAcrossOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leases.json")

	l, err := OpenLeases(path)
	require.NoError(t, err)
	assert.Empty(t, l.All(), "a missing file reads as an empty set")

	require.NoError(t, l.Put("dev", "169.254.42.2"))
	require.NoError(t, l.Put("web", "169.254.42.3"))

	reopened, err := OpenLeases(path)
	require.NoError(t, err)
	ip, ok := reopened.Get("dev")
	require.True(t, ok)
	assert.Equal(t, "169.254.42.2", ip)
	assert.Equal(t, []string{"dev", "web"}, reopened.Names())

	require.NoError(t, reopened.Remove("dev"))
	require.NoError(t, reopened.Remove("dev"), "removing an absent lease is a no-op")
	_, ok = reopened.Get("dev")
	assert.False(t, ok)

	// The lease file holds no secrets but does describe live topology: keep it 0600.
	// Windows has no POSIX mode bits (Go reports 0666), and netd is Linux-only anyway.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
}
