package repl

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hinshun/vt10x"
	"github.com/stretchr/testify/require"
)

// trustMenuStream is Claude Code 2.1.270's output for the folder-trust dialog,
// captured byte for byte from a PTY sized like ape's pane: the first paint,
// then everything it wrote after one Down arrow. Only the workspace path is
// replaced.
func trustMenuStream(t *testing.T) (initial, down []byte) {
	t.Helper()
	var err error
	initial, err = os.ReadFile(filepath.Join("testdata", "claude-2.1.270-trust-menu-initial.bin"))
	require.NoError(t, err)
	down, err = os.ReadFile(filepath.Join("testdata", "claude-2.1.270-trust-menu-down.bin"))
	require.NoError(t, err)
	return initial, down
}

// render feeds chunks through a fresh ape-sized emulator, filtered or not,
// and returns the pane as CapturePane would.
func render(filtered bool, chunks ...[]byte) string {
	s := &session{term: vt10x.New(vt10x.WithSize(paneCols, paneRows))}
	var f privateCSIFilter
	for _, c := range chunks {
		if filtered {
			c = f.apply(c)
		}
		_, _ = s.term.Write(c)
	}
	return s.snapshot()
}

// The failure make check-hooks caught, reproduced from the real bytes: after
// Down, claude has moved the selection to the trust option. Unfiltered, vt10x
// runs the kitty keyboard sequences as cursor restores and paints that
// repaint over the top border, so the menu ape reads never changes — the
// control case, which is what keeps this test honest about the fixture.
func TestPrivateCSIFilter_TrustMenuRepaintLandsOnTheMenu(t *testing.T) {
	t.Parallel()
	initial, down := trustMenuStream(t)

	before, ok := selectedMenuOption(render(true, initial))
	require.True(t, ok)
	require.Equal(t, "No, exit", before, "claude 2.1.270 preselects the refusal")

	pane := render(true, initial, down)
	sel, ok := selectedMenuOption(pane)
	require.True(t, ok)
	require.Equal(t, "Yes, I trust this folder", sel, "pane:\n%s", pane)
	require.True(t, grantsTrust(sel))

	broken := render(false, initial, down)
	stale, ok := selectedMenuOption(broken)
	require.True(t, ok)
	require.Equal(t, "No, exit", stale,
		"control: without the filter vt10x must misplace the repaint, or this fixture no longer shows the bug; pane:\n%s", broken)
}

// A PTY read can end anywhere, including inside a sequence. Every split of
// the captured stream must render exactly as the whole does.
func TestPrivateCSIFilter_SplitReadsRenderTheSame(t *testing.T) {
	t.Parallel()
	initial, down := trustMenuStream(t)
	stream := append(append([]byte{}, initial...), down...)

	var whole privateCSIFilter
	want := whole.apply(stream)
	for i := range len(stream) + 1 {
		var f privateCSIFilter
		got := append(f.apply(stream[:i]), f.apply(stream[i:])...)
		require.Equal(t, want, got, "split at byte %d", i)
	}
}

// Dropping is the only way the filter changes what vt10x renders, so what it
// drops is pinned exactly — and so is what it must leave alone.
func TestPrivateCSIFilter_DropsOnlyMisreadSequences(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, in, want string
	}{
		{"kitty push", "a\x1b[>5ub", "ab"},
		{"kitty pop", "a\x1b[<ub", "ab"},
		{"kitty query", "a\x1b[?ub", "ab"},
		{"modifyOtherKeys", "a\x1b[>4;2mb", "ab"},
		{"XTVERSION", "a\x1b[>0qb", "ab"},
		{"equals-prefixed", "a\x1b[=1cb", "ab"},
		{"private save", "a\x1b[?1sb", "ab"},
		{"plain restore cursor stays", "a\x1b[ub", "a\x1b[ub"},
		{"plain save cursor stays", "a\x1b[sb", "a\x1b[sb"},
		{"DEC mode set stays", "\x1b[?25h\x1b[?2004h", "\x1b[?25h\x1b[?2004h"},
		{"DEC mode reset stays", "\x1b[?25l", "\x1b[?25l"},
		{"cursor moves stay", "\x1b[4A\x1b[1C\x1b[4G", "\x1b[4A\x1b[1C\x1b[4G"},
		{"truecolour SGR stays", "\x1b[38;2;177;185;249m❯", "\x1b[38;2;177;185;249m❯"},
		{"non-CSI escapes stay", "\x1b7\x1b8\x1b(B\x1b]8;;\a", "\x1b7\x1b8\x1b(B\x1b]8;;\a"},
		{"a lone escape before a private one", "\x1b\x1b[>5u", "\x1b"},
		{"control inside CSI passes whole", "\x1b[>\r5u", "\x1b[>\r5u"},
		{"utf-8 untouched", "─❯─", "─❯─"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var f privateCSIFilter
			require.Equal(t, tc.want, string(f.apply([]byte(tc.in))))
		})
	}
}

// An unterminated sequence is held, not lost, and a runaway one is released
// at vt10x's own bound rather than swallowing the stream behind it.
func TestPrivateCSIFilter_HoldsThenBoundsUnfinishedSequences(t *testing.T) {
	t.Parallel()
	var f privateCSIFilter
	require.Empty(t, f.apply([]byte("\x1b[>5")))
	require.Equal(t, "x", string(f.apply([]byte("ux"))))

	var g privateCSIFilter
	runaway := append([]byte("\x1b["), make([]byte, privateCSIMaxLen)...)
	for i := 2; i < len(runaway); i++ {
		runaway[i] = '1'
	}
	require.Len(t, g.apply(runaway), len(runaway))
}
