package apecmd

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The update notice reached skill-parsed payloads: Claude Code's Bash tool
// merges stderr into the result, so a JSON answer could arrive with
// "update available: …" before or after it. The commands are the real ones,
// resolved from a private tree — `config resolve` is the call the framework
// eval's payload check failed on, and `notify` is a hidden hot-path command.
func TestShouldCheckForUpdates(t *testing.T) {
	// Not parallel: building a tree is not race-free despite newRootCmd's
	// comment — newSandboxCmd binds --node and its siblings to package
	// variables, and TestAboardIsMountedOnTheRoot already builds one in
	// parallel, so a second parallel builder fails -race.
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
