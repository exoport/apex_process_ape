package apecmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
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

// A mistyped flag is a USAGE error, not a verdict.
//
// Every gate in this binary uses exit 1 to mean "I looked and found
// something", and cobra reported a flag it did not recognise as an
// ordinary error, which the exit table then mapped to the same 1. So
// `ape doc verify --doc epics` exited 1 having read no document, and a
// caller whose help text calls it "a GATE … the caller relies on the
// non-zero exit to stop" concluded the document had duplicates. The
// explanation went to stderr, where a caller reading stdout never saw
// it.
func TestFlagErrors_ExitUsageNotTheVerdictCode(t *testing.T) {
	for _, args := range [][]string{
		{"doc", "verify", "--doc", "epics"},
		{"doc", "verify", "--file", "development/planning/epics.md"},
		{"sprint", "verify", "--nonsense"},
		{"story", "fields", "--nonsense"},
		{"--nonsense"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			root := newRootCmd()
			root.SetArgs(args)
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&bytes.Buffer{})

			code, _ := ExitCode(root.Execute())
			require.Equal(t, ExitUsage, code,
				"a flag ape does not have is exit 2, never the code a gate uses for a finding")
		})
	}
}

// And the other half of the same statement: a real verdict must NOT
// move. A gate that started answering 2 for a finding would be the same
// collision in reverse.
func TestFlagErrors_ContentVerdictsAreUnchanged(t *testing.T) {
	dir := t.TempDir()
	dup := filepath.Join(dir, "dup.md")
	require.NoError(t, os.WriteFile(dup, []byte("# t\n\n## a\n\n## a\n"), 0o644))
	clean := filepath.Join(dir, "clean.md")
	require.NoError(t, os.WriteFile(clean, []byte("# t\n\n## a\n\n## b\n"), 0o644))

	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"duplicates are still 1", []string{"doc", "verify", dup, "--level", "2"}, ExitRunFailed},
		{"a clean document is still 0", []string{"doc", "verify", clean, "--level", "2"}, ExitOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := newRootCmd()
			root.SetArgs(tc.args)
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&bytes.Buffer{})

			code, _ := ExitCode(root.Execute())
			require.Equal(t, tc.want, code)
		})
	}
}

// The same collision as the flag path, one step later: cobra reports a
// REJECTED ARGUMENT as an ordinary error, which the exit table mapped to
// 1 — the code every gate here uses for a finding. `ape doctor zzbogus`
// exited 1 refusing an argument it cannot mean, which reads as a doctor
// run that found a problem.
//
// Nothing in this path reads an error's text. The validator's verdict is
// ape-side; only the code it travels under moves.
func TestArgErrors_ExitUsageNotTheVerdictCode(t *testing.T) {
	for _, args := range [][]string{
		{"doctor", "zzbogus"},
		{"metrics", "zzbogus"},
		{"planning", "zzbogus"},
		{"version", "zzbogus"},
		{"task"}, // a required positional, absent
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			root := newRootCmd()
			root.SetArgs(args)
			root.SetOut(&bytes.Buffer{})
			root.SetErr(&bytes.Buffer{})

			code, _ := ExitCode(root.Execute())
			require.Equal(t, ExitUsage, code,
				"an argument a command cannot mean is exit 2, never a verdict")
		})
	}
}

// `ape version zzbogus` printed the version and exited 0 — a caller that
// typo'd a verb got a plausible answer to a question it never asked. It
// was the only silent survivor of a 23-group sweep, and read-only, which
// is why it survived: nothing it did was wrong except agreeing.
// Exit codes only, deliberately: `ape version` writes to os.Stdout
// rather than to the command's writer, so a buffer set here captures
// nothing. The first draft asserted the bogus call printed no version
// string against that empty buffer — an assertion that passes whatever
// the command does, which is the shape of test this repo keeps finding
// in other people's code.
func TestVersion_RefusesAnArgumentItCannotMean(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"version", "zzbogus"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	code, _ := ExitCode(root.Execute())
	require.Equal(t, ExitUsage, code)

	// And the command itself still answers.
	clean := newRootCmd()
	clean.SetArgs([]string{"version"})
	clean.SetOut(&bytes.Buffer{})
	clean.SetErr(&bytes.Buffer{})
	code, _ = ExitCode(clean.Execute())
	require.Equal(t, ExitOK, code)
}

// A group's own guard already carries a code; wrapping must not restate
// it, or the two would disagree about who decided.
func TestArgGuard_LeavesAnAlreadyCodedErrorAlone(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"sprint", "zzbogus"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})

	err := root.Execute()
	code, reported := ExitCode(err)
	require.Equal(t, ExitUsage, code)
	require.True(t, reported, "the group guard prints its own message and says so")
}
