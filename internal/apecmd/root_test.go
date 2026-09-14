package apecmd

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// newRootCmd promises a private tree, which tests rely on to build trees in
// parallel. A constructor that writes shared state — newSandboxCmd binding its
// connection flags to package variables did — breaks that silently, and only
// the race detector sees it, so this only proves anything under -race (which
// `make test` and CI use). Several builders, not two, so a write the detector
// happens to miss once is unlikely to be missed every time.
func TestNewRootCmd_TreesBuildConcurrently(t *testing.T) {
	t.Parallel()

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() { newRootCmd() })
	}
	wg.Wait()
}

// The update notice reached skill-parsed payloads: Claude Code's Bash tool
// merges stderr into the result, so a JSON answer could arrive with
// "update available: …" before or after it. The commands are the real ones,
// resolved from a private tree — `config resolve` is the call the framework
// eval's payload check failed on, and `notify` is a hidden hot-path command.
func TestShouldCheckForUpdates(t *testing.T) {
	t.Parallel()

	root := newRootCmd()
	resolve, _, err := root.Find([]string{"config", "resolve"})
	require.NoError(t, err)
	require.False(t, resolve.Hidden, "fixture: config resolve must be a visible command")
	notify, _, err := root.Find([]string{"notify"})
	require.NoError(t, err)
	require.True(t, notify.Hidden, "fixture: notify must be a hidden command")

	require.True(t, shouldCheckForUpdates(resolve, true),
		"a visible command at a terminal checks, so a person still sees the notice")
	require.False(t, shouldCheckForUpdates(resolve, false),
		"stderr that is not a terminal is a pipe, a redirect or a tool reading the output — no check")
	require.False(t, shouldCheckForUpdates(notify, true),
		"a hidden command never checks, even at a terminal")
}
