package repl

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeMenu models a claude selection dialog well enough to test the walk:
// Down moves the ❯ cursor, and the pane is rendered from wherever it
// currently sits. A frame list cannot stand in for this — the behaviour
// under test is precisely that ape reads the selection back before
// confirming, so the selection has to be able to move.
type fakeMenu struct {
	mu      sync.Mutex
	header  string
	options []string
	sel     int
	enters  int
	downs   int
	// ready is returned once Enter has been pressed on some option.
	ready     string
	confirmed bool
}

func (m *fakeMenu) render() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.confirmed {
		return m.ready
	}
	var b strings.Builder
	b.WriteString(m.header)
	for i, opt := range m.options {
		cursor := "  "
		if i == m.sel {
			cursor = ReadyGlyph + " "
		}
		b.WriteString("\n " + cursor + opt)
	}
	b.WriteString("\n\n Enter to confirm · Esc to cancel")
	return b.String()
}

// chosen reports the option Enter landed on, or "" if none was confirmed.
func (m *fakeMenu) chosen() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.confirmed {
		return ""
	}
	return m.options[m.sel]
}

func installFakeMenu(t *testing.T, m *fakeMenu) {
	t.Helper()
	capturePaneFn = func(context.Context, string) (string, error) { return m.render(), nil }
	sendEnterFn = func(context.Context, string) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.enters++
		m.confirmed = true
		return nil
	}
	sendDownFn = func(context.Context, string) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.downs++
		if m.sel < len(m.options)-1 {
			m.sel++
		}
		return nil
	}
	t.Cleanup(func() {
		capturePaneFn = CapturePane
		sendEnterFn = SendEnter
		sendDownFn = SendDown
	})
}

const trustHeader = ` Accessing workspace:

 /tmp/some-project

 Quick safety check: Is this a project you created or one you trust?

 Claude Code'll be able to read, edit, and execute files here.
`

// The regression. claude 2.1.248 reordered the trust dialog to put
// "No, exit" first AND made it the default, so ape's bare Enter pressed
// the exit button: the session died, the REPL never became ready, and
// every stage burned its idle window for zero turns. 2.1.247 was clean.
func TestTrustModal_WalksPastAPreselectedDecline(t *testing.T) {
	m := &fakeMenu{
		header:  trustHeader,
		options: []string{"No, exit", "Yes, I trust this folder"},
		ready:   readyFooterPane,
	}
	installFakeMenu(t, m)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, WaitForReady(ctx, "s"))

	require.Equal(t, "Yes, I trust this folder", m.chosen(),
		"Enter must land on the option that grants trust, never on the decline")
	require.Equal(t, 1, m.downs, "one move to reach the second option")
	require.Equal(t, 1, m.enters)
}

// The pre-2.1.248 order still works, and costs no keystrokes it does not
// need: a correct fix must not press Down on a menu already sitting on
// the right row.
func TestTrustModal_ConfirmsAPreselectedAccept(t *testing.T) {
	m := &fakeMenu{
		header:  trustHeader,
		options: []string{"Yes, I trust this folder", "No, exit"},
		ready:   readyFooterPane,
	}
	installFakeMenu(t, m)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, WaitForReady(ctx, "s"))

	require.Equal(t, "Yes, I trust this folder", m.chosen())
	require.Zero(t, m.downs, "already selected — no movement needed")
}

// Position is claude's business. Any ordering must work, including one
// nobody has shipped, because the whole point is not to encode a layout.
func TestTrustModal_FindsTheOptionAtAnyPosition(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options []string
	}{
		{"trust last of three", []string{"No, exit", "Show security guide", "Yes, I trust this folder"}},
		{"trust in the middle", []string{"No, exit", "Yes, I trust this folder", "Something else"}},
		{"trust first", []string{"Yes, I trust this folder", "No, exit"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &fakeMenu{header: trustHeader, options: tc.options, ready: readyFooterPane}
			installFakeMenu(t, m)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			require.NoError(t, WaitForReady(ctx, "s"))
			require.Equal(t, "Yes, I trust this folder", m.chosen())
		})
	}
}

// A dialog whose options ape cannot read must fail LOUDLY with the pane,
// not press Enter and hope. Guessing on an unrecognised menu is how the
// 2.1.248 regression turned into a dead session rather than an error.
func TestTrustModal_RefusesToGuessOnAnUnknownMenu(t *testing.T) {
	m := &fakeMenu{
		header:  trustHeader,
		options: []string{"No, exit", "Maybe later", "Show security guide"},
		ready:   readyFooterPane,
	}
	installFakeMenu(t, m)

	// Long enough to reach the give-up path: every move waits for a
	// repaint that never comes, which is the slow case by design.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err := WaitForReady(ctx, "s")

	require.Error(t, err)
	require.Zero(t, m.enters, "never confirm an option that was not understood")
	require.Contains(t, err.Error(), "could not reach a trust-granting option")
	// Bounded: it gives up rather than pressing keys for ever.
	require.LessOrEqual(t, m.downs, maxMenuMoves)
}

// grantsTrust is word-based because the wording is claude's, not ours.
// The decline cases are the ones that matter: a naive `contains("trust")`
// selects "Don't trust this folder".
func TestGrantsTrust(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		option string
		want   bool
	}{
		{"Yes, I trust this folder", true},
		{"1. Yes, I trust this folder", true},
		{"Trust this workspace", true},
		{"Yes, proceed — I trust this directory", true},

		{"No, exit", false},
		{"Don't trust this folder", false},
		{"Do not trust this workspace", false},
		{"No, I don't trust this folder", false},
		{"Never trust this folder", false},
		{"Cancel", false},
		{"Show security guide", false},
		{"", false},
	} {
		t.Run(tc.option, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, grantsTrust(tc.option))
		})
	}
}

func TestSelectedMenuOption(t *testing.T) {
	t.Parallel()

	t.Run("reads the highlighted row", func(t *testing.T) {
		t.Parallel()
		got, ok := selectedMenuOption(" ❯ No, exit\n   Yes, I trust this folder")
		require.True(t, ok)
		require.Equal(t, "No, exit", got)
	})

	// The glyph doubles as the REPL prompt, where it sits alone. Reading
	// that as a menu row would make an empty prompt look like a dialog.
	t.Run("a bare prompt glyph is not a menu row", func(t *testing.T) {
		t.Parallel()
		_, ok := selectedMenuOption("────────\n❯ \n────────")
		require.False(t, ok)
	})

	t.Run("no glyph at all", func(t *testing.T) {
		t.Parallel()
		_, ok := selectedMenuOption("just some output")
		require.False(t, ok)
	})
}

// The match is on words, so it survives the rewordings claude has already
// shipped — and would survive the next one.
func TestTrustModalMatch_AcrossWordings(t *testing.T) {
	t.Parallel()
	match := blockingModals[0].match

	for _, pane := range []string{
		"Do you trust the files in this folder?",                              // pre-2.1.24x
		"Quick safety check: Is this a project you created or one you trust?", // 2.1.240
		" Accessing workspace:\n Is this a project you trust?",                // 2.1.248
		"Do you trust this directory?",                                        // hypothetical
		"Trust this workspace to continue",                                    // hypothetical
	} {
		require.True(t, match(pane), "should match: %q", pane)
	}

	for _, pane := range []string{
		readyFooterPane,
		"Some other modal entirely",
		"trust", // the word alone, with no place to trust
	} {
		require.False(t, match(pane), "should NOT match: %q", pane)
	}
}
