package repl

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The kitty keyboard and xterm private sequences claude sends, and what the
// production screen does with each.
//
// Before x/vt, vt10x ran several of these as cursor moves, and a filter
// (privateCSIFilter, b96d831) dropped them in front of it. x/vt keys its CSI
// handlers by prefix, intermediate and final byte, and has none for any of
// these forms, so it ignores them structurally. That filter was measured in
// front of x/vt before it was removed: identical render and cursor on every
// segment of both 2.1.270 captures below, and one input it made WORSE — a
// lone ESC before a private sequence, where it handed x/vt `ESC b` and the
// `b` was swallowed. These tests are what the filter's table was, asserted
// on the emulator ape actually reads, so an x/vt upgrade that starts
// misreading one fails here.

// seqPre parks the cursor mid-grid with a style on, so a sequence that moves
// the cursor, resets attributes or draws shows up against the same bytes
// without it.
const seqPre = "\x1b[3;5H\x1b[1mA"

// TestScreen_PrivateSequencesDrawNothing: each sequence leaves the grid, its
// styles and the cursor exactly as the same bytes without it.
func TestScreen_PrivateSequencesDrawNothing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, seq string }{
		{"kitty push, flags 5", "\x1b[>5u"},
		{"kitty push, flags 1", "\x1b[>1u"},
		{"kitty pop", "\x1b[<u"},
		{"kitty query", "\x1b[?u"},
		{"kitty set", "\x1b[=1;1u"},
		{"XTMODKEYS modifyOtherKeys", "\x1b[>4;2m"},
		{"XTVERSION", "\x1b[>0q"},
		{"equals-prefixed (DA3)", "\x1b[=1c"},
		{"XTSAVE", "\x1b[?1s"},
		{"XTSAVE alternate screen", "\x1b[?1049s"},
		{"XTSMGRAPHICS", "\x1b[?1;1;0S"},
		// A terminal reads two ESCs in a row as a cancelled escape then a new
		// one, so the push is ignored and `b` still draws. The filter kept
		// the lone ESC and dropped the push, which made `ESC b` of it. The
		// vt10x oracle loses the `b` the same way, so this case is asserted
		// on production only; a live stream carrying it would show up as an
		// emulators_agree disagreement, not a silent misread.
		{"lone ESC before a kitty push", "\x1b\x1b[>5u"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			with, without := newScreen(), newScreen()
			defer with.close()
			defer without.close()
			with.write([]byte(seqPre + tc.seq + "b"))
			without.write([]byte(seqPre + "b"))

			require.Equal(t, without.text(), with.text(), "the grid changed")
			require.Equal(t, without.term.Render(), with.term.Render(), "a style changed")
			require.Equal(t, without.term.CursorPosition(), with.term.CursorPosition(), "the cursor moved")
		})
	}
}

// TestScreen_SequencesThatMustTakeEffect is the other half of the filter's
// table: what must NOT be ignored. A fix that dropped too much would fail
// here in either emulator.
func TestScreen_SequencesThatMustTakeEffect(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, in string
		row      int
		want     string
	}{
		{"DEC mode set: autowrap off", "\x1b[1;1H\x1b[?7l" + strings.Repeat("w", paneCols+10) + "Q", 1, ""},
		{"cursor moves", "\x1b[5;1H\x1b[4A\x1b[3CX", 0, "   X"},
		{"truecolour SGR before a glyph", "\x1b[1;1H\x1b[38;2;177;185;249m" + ReadyGlyph, 0, ReadyGlyph},
		{"DECSC / DECRC (ESC 7 / ESC 8)", "\x1b[3;5H\x1b7\x1b[10;20H\x1b8X", 2, "    X"},
		{"a control inside a private CSI still runs", "\x1b[1;11H\x1b[>\ru" + "X", 0, "X"},
		{"UTF-8 box drawing", "\x1b[1;1H─" + ReadyGlyph + "─", 0, "─" + ReadyGlyph + "─"},
	} {
		for name, newEmu := range emulators() {
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				t.Parallel()
				s := newEmu()
				s.write([]byte(tc.in))
				require.Equal(t, tc.want, paneRow(s.text(), tc.row), "pane:\n%s", s.text())
			})
		}
	}
}

// TestScreen_UnfinishedSequences: a sequence split across two reads is held
// until its final byte, and one that never ends within any buffer is ended by
// the first final byte rather than swallowing what follows.
func TestScreen_UnfinishedSequences(t *testing.T) {
	t.Parallel()

	t.Run("held across reads", func(t *testing.T) {
		t.Parallel()
		for name, newEmu := range emulators() {
			split, whole := newEmu(), newEmu()
			split.write([]byte(seqPre + "\x1b[>5"))
			split.write([]byte("ux"))
			whole.write([]byte(seqPre + "x"))
			require.Equal(t, whole.text(), split.text(), name)
		}
	})

	// Production only. vt10x parses a CSI once its buffer holds 256 bytes
	// whatever the last byte is, so it prints the parameter bytes that remain;
	// x/vt keeps ignoring them until the final byte, as a terminal does.
	t.Run("runaway parameters end at the final byte", func(t *testing.T) {
		t.Parallel()
		s := newScreen()
		defer s.close()
		s.write([]byte("\x1b[1;1H\x1b[" + strings.Repeat("1", 300) + "xyZ"))
		require.Equal(t, "yZ", paneRow(s.text(), 0),
			"the runaway CSI ends at `x`; the text after it must render")
	})
}

// TestScreen_PlainRestoreCursorIsIgnored pins a gap rather than a fix. x/vt
// saves the cursor on a plain `CSI s` (SCOSC) but has no handler for a plain
// `CSI u` (SCORC), so a TUI restoring that way would draw at the wrong place
// in ape's pane while vt10x, and a real terminal, restore.
//
// No shim: claude saves and restores with ESC 7 / ESC 8 (DECSC / DECRC),
// which both emulators honour, and neither capture contains a plain SCOSC or
// SCORC (TestTrustCaptures_UseDECSCNotSCORC). A shim would be a filter in
// front of x/vt again — the layer this package just removed — for a sequence
// nothing ape drives sends. If claude ever switches, the capture tripwire and
// the live gate's whole-pane comparison against vt10x are where it shows.
func TestScreen_PlainRestoreCursorIsIgnored(t *testing.T) {
	t.Parallel()

	prod := newScreen()
	defer prod.close()
	prod.write([]byte("\x1b[3;5H\x1b[s\x1b[10;20H\x1b[uX"))
	require.Equal(t, strings.Repeat(" ", 19)+"X", paneRow(prod.text(), 9),
		"x/vt now implements SCORC — update this test and the note above")

	saved := newScreen()
	defer saved.close()
	saved.write([]byte("\x1b[3;5H\x1b[s\x1b[10;20H\x1b8X"))
	require.Equal(t, "    X", paneRow(saved.text(), 2), "plain CSI s still saves, into the DECSC slot")

	oracle := newVT10xOracle()
	oracle.write([]byte("\x1b[3;5H\x1b[s\x1b[10;20H\x1b[uX"))
	require.Equal(t, "    X", paneRow(oracle.text(), 2), "vt10x restores on SCORC; the two disagree here")
}

// trustCapture is one captured byte stream of claude's folder-trust dialog:
// the first frame, then what it wrote after each Down press.
type trustCapture struct {
	name     string
	segments [][]byte
	// selections is the highlighted option after each segment.
	selections []string
}

// trustCaptures are both 2.1.270 captures, and they are two different
// streams. claude picks its keyboard protocol and synchronized output from
// the terminal its environment names (TERM_PROGRAM and friends). In main's,
// claude pushed kitty keyboard flags (`CSI >5u`, `CSI >4;2m`, popped with
// `CSI <u`) and used no synchronized output — a tmux-like terminal. In the
// graph branch's, captured under VS Code, it only queries (`CSI ?u`) and
// wraps each frame in `?2026h/l`. ape passes TERM_PROGRAM through, so
// production meets both; the live gate drives claude in each mode.
func trustCaptures(t *testing.T) []trustCapture {
	t.Helper()
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		require.NoError(t, err)
		return b
	}
	initial, downs := trustDialogCapture(t)
	return []trustCapture{
		{
			name: "kitty push (b96d831)",
			segments: [][]byte{
				read("claude-2.1.270-trust-menu-initial.bin"),
				read("claude-2.1.270-trust-menu-down.bin"),
			},
			selections: []string{"No, exit", "Yes, I trust this folder"},
		},
		{
			name:       "query only, synchronized output (8061dcc)",
			segments:   append([][]byte{initial}, downs...),
			selections: []string{"No, exit", "Yes, I trust this folder", "No, exit", "Yes, I trust this folder"},
		},
	}
}

// TestTrustCaptures_AreTheTwoModes keeps the claim above true of the files:
// if a fixture is re-captured in the other mode, the two stop covering both
// streams, and this says so.
func TestTrustCaptures_AreTheTwoModes(t *testing.T) {
	t.Parallel()
	caps := trustCaptures(t)
	push, query := bytes.Join(caps[0].segments, nil), bytes.Join(caps[1].segments, nil)

	require.Contains(t, string(push), "\x1b[>5u", "the kitty capture must push flags")
	require.NotContains(t, string(push), "\x1b[?2026h")
	require.NotContains(t, string(query), "\x1b[>5u", "the query capture must not push flags")
	require.Contains(t, string(query), "\x1b[?u")
	require.Contains(t, string(query), "\x1b[?2026h")
}

// TestTrustCaptures_UseDECSCNotSCORC is the tripwire for
// TestScreen_PlainRestoreCursorIsIgnored: claude restores the cursor with
// ESC 8, which x/vt honours, and never with a plain CSI u, which it ignores.
func TestTrustCaptures_UseDECSCNotSCORC(t *testing.T) {
	t.Parallel()
	for _, c := range trustCaptures(t) {
		stream := bytes.Join(c.segments, nil)
		require.Contains(t, string(stream), "\x1b8", c.name)
		require.NotContains(t, string(stream), "\x1b[u",
			"%s: claude sent a plain SCORC, which x/vt does not implement — see TestScreen_PlainRestoreCursorIsIgnored", c.name)
		require.NotContains(t, string(stream), "\x1b[s", c.name)
	}
}

// TestTrustCaptures_EverySplitInBothEmulators replays each capture through
// production and the oracle, cut at every byte of the whole stream: a PTY read
// can end anywhere, including inside a sequence or a rune. The pane at the end
// must be the one the uncut stream draws, and so must what ape reads off it at
// every segment — in both emulators, which must also agree with each other.
func TestTrustCaptures_EverySplitInBothEmulators(t *testing.T) {
	t.Parallel()
	for _, c := range trustCaptures(t) {
		stream := bytes.Join(c.segments, nil)
		last := c.selections[len(c.selections)-1]

		t.Run(c.name+"/segments agree", func(t *testing.T) {
			t.Parallel()
			prod, oracle := newScreen(), newVT10xOracle()
			defer prod.close()
			for i, seg := range c.segments {
				prod.write(seg)
				oracle.write(seg)
				require.Equal(t, paneReadsOf(oracle.text()), paneReadsOf(prod.text()), "segment %d", i)
				sel, ok := selectedMenuOption(prod.text())
				require.True(t, ok, "segment %d", i)
				require.Equal(t, c.selections[i], sel, "segment %d; pane:\n%s", i, prod.text())
			}
		})

		for name, newEmu := range emulators() {
			t.Run(c.name+"/every split/"+name, func(t *testing.T) {
				t.Parallel()
				whole := newEmu()
				whole.write(stream)
				want := whole.text()
				if sc, ok := whole.(*screen); ok {
					sc.close()
				}
				for cut := 1; cut < len(stream); cut++ {
					s := newEmu()
					s.write(stream[:cut])
					s.write(stream[cut:])
					got := s.text()
					if sc, ok := s.(*screen); ok {
						sc.close()
					}
					if got != want {
						require.Equal(t, want, got, "cut at byte %d of %d", cut, len(stream))
					}
				}
				sel, _ := selectedMenuOption(want)
				require.Equal(t, last, sel)
			})
		}
	}
}
