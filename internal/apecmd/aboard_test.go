package apecmd

import (
	"bytes"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/exoport/aboard/pkg/aboard"
	aboardcli "github.com/exoport/aboard/pkg/aboard/cli"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// Resolved with Find, not Commands: cobra's Commands() sorts the child slice
// IN PLACE, so calling it on the shared rootCmd from a parallel test races
// every other test that walks the same tree (the command-surface checks all
// use Find, which only reads).
func TestAboardIsMountedOnTheRoot(t *testing.T) {
	t.Parallel()
	found, _, err := rootCmd.Find([]string{"aboard"})
	require.NoError(t, err)
	require.NotNil(t, found)
	require.Equal(t, "aboard", found.Name(), "`ape aboard` must be registered on ape's root")
	require.True(t, found.HasSubCommands(), "the whole board tree mounts, not a stub")
}

// The two fields that make this host distinguishable in a board's records.
// Argv0 is what aboard names in the instance record and /health; it is the
// command the user typed, which is not ape's binary name.
func TestAboardOptionsIdentifyTheApeHost(t *testing.T) {
	t.Parallel()
	opts := aboardOptions()
	require.Equal(t, aboard.HostApe, opts.Host)
	require.Equal(t, "ape aboard", opts.Argv0)
}

// runCapabilities executes `capabilities` on a tree built from opts and
// returns what it printed.
func runCapabilities(t *testing.T, opts aboardcli.Options) string {
	t.Helper()
	var out bytes.Buffer
	opts.Stdout = &out
	cmd := aboardcli.NewRootCmd(opts)
	cmd.SetArgs([]string{"capabilities"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	require.NoError(t, cmd.Execute())
	require.NotEmpty(t, out.String())
	return out.String()
}

// The capability manifest describes the BOARD, not the process serving it, so
// an agent reading it must not be able to tell which host it reached. That is
// why aboard.AppName stays "aboard" under both hosts while app/host in
// /health become "ape-aboard".
//
// Asserted as full-output equality rather than on capsHash alone: a change
// that altered a declared command but left the hash stale would pass a hash
// check and still be the drift this guards.
func TestAboardCapabilitiesAreHostIndependent(t *testing.T) {
	t.Parallel()
	hosted := runCapabilities(t, aboardOptions())
	standalone := runCapabilities(t, aboardcli.Options{})
	require.Equal(t, standalone, hosted,
		"the manifest must not reveal the host; if this fails, the change moved capsHash")
}

// ape's ExitCode knows only its own *exitError and otherwise answers 1. The
// mounted tree brings a second table (2 usage, 3 `wait` timed out) that is
// documented and scripted against, so those have to survive the host's
// mapping rather than collapsing into 1.
func TestAboardExitStatusesSurviveApesMapping(t *testing.T) {
	t.Parallel()

	// A real usage error from the mounted tree, not a synthetic one: the
	// point is that whatever aboard actually returns is understood here.
	cmd := newAboardCmd()
	cmd.SetArgs([]string{"export"}) // ExactArgs(1), none given
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	usageErr := cmd.Execute()
	require.Error(t, usageErr)

	code, _ := ExitCode(usageErr)
	require.Equal(t, aboard.ExitUsage, code,
		"a board usage error must exit 2, not ape's generic 1")

	// Exit 3 (`aboard wait` timing out) travels the identical branch — any
	// code aboard reports that is not ExitFailed is preferred — and is
	// verified end to end against a live board, which a unit test cannot
	// construct.

	t.Run("an error ape owns still wins", func(t *testing.T) {
		t.Parallel()
		code, silent := ExitCode(gateErr(7, errors.New("boom")))
		require.Equal(t, 7, code)
		require.True(t, silent, "ape's own errors already reported themselves")
	})

	t.Run("an error neither owns is unchanged", func(t *testing.T) {
		t.Parallel()
		code, silent := ExitCode(errors.New("plain"))
		require.Equal(t, ExitRunFailed, code)
		require.False(t, silent)
	})

	t.Run("nil is still zero", func(t *testing.T) {
		t.Parallel()
		code, silent := ExitCode(nil)
		require.Equal(t, ExitOK, code)
		require.False(t, silent)
	})
}

// Cobra runs the CLOSEST PersistentPreRun walking up from the executed
// command, and aboard's tree defines none of its own — so without the shadow
// in newAboardCmd, every board command would run ape's, whose background
// update check can print "update available: … run 'ape update'" onto stderr
// mid-board-output. Because that check is fired as a goroutine, whether it
// appeared at all would depend on a race with process exit, making a board
// command's stderr nondeterministic.
//
// The control case is the same tree with the shadow removed, so this fails if
// the shadow stops working OR if cobra's resolution order ever changes —
// either way the assertion is about behaviour, not about the field being set.
func TestAboardDoesNotInheritApesPersistentPreRun(t *testing.T) {
	// Not parallel: redirects the process's stdout.
	restore := discardStdout(t)
	defer restore()

	run := func(shadowed bool) bool {
		called := false
		host := &cobra.Command{Use: "ape", SilenceUsage: true, SilenceErrors: true}
		host.PersistentPreRun = func(*cobra.Command, []string) { called = true }
		board := newAboardCmd()
		if !shadowed {
			board.PersistentPreRun = nil
		}
		host.AddCommand(board)
		host.SetArgs([]string{"aboard", "capabilities"})
		host.SetOut(io.Discard)
		host.SetErr(io.Discard)
		require.NoError(t, host.Execute())
		return called
	}

	require.False(t, run(true),
		"a board command must not run ape's root PersistentPreRun")
	require.True(t, run(false),
		"control: without the shadow cobra does walk up to ape's hook, so the test is live")
}

// discardStdout points os.Stdout at a sink for the duration of a test. aboard
// resolves its output writer at call time, so redirecting before Execute is
// enough.
func discardStdout(t *testing.T) func() {
	t.Helper()
	prev := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	done := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, r); close(done) }()
	return func() {
		os.Stdout = prev
		_ = w.Close()
		<-done
		_ = r.Close()
	}
}
