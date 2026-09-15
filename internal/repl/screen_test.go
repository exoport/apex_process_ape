package repl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hinshun/vt10x"
	"github.com/stretchr/testify/require"
)

// trustDialogCapture is the raw PTY output of Claude Code 2.1.270 spawned
// with --dangerously-skip-permissions in a fresh directory: the first frame
// of its trust dialog, then what it wrote after each of three Down presses.
// Captured from a live session, not written by hand — the defect it pins
// was in how those exact bytes were rendered, so a hand-written frame of the
// same shape would not contain it.
func trustDialogCapture(t *testing.T) (initial []byte, downs [][]byte) {
	t.Helper()
	dir := filepath.Join("testdata", "trust-dialog-2.1.270")
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		return b
	}
	initial = read("0-initial.bin")
	for _, n := range []string{"1-down.bin", "2-down.bin", "3-down.bin"} {
		downs = append(downs, read(n))
	}
	return initial, downs
}

// paneEmulator is what ape asks of an emulator: bytes in, the pane out.
type paneEmulator interface {
	write(p []byte)
	text() string
}

// emulators are the two renders every screen property is asserted against:
// production's x/vt, and vt10x behind the filter it needed (the oracle, R4a).
// A property that holds for one and not the other is a sequence one of them
// misreads — found here, from a test, rather than from a stalled run.
func emulators() map[string]func() paneEmulator {
	return map[string]func() paneEmulator{
		"x/vt":         func() paneEmulator { return newScreen() },
		"vt10x+filter": func() paneEmulator { return newVT10xOracle() },
	}
}

// paneRow returns row i of a pane, or "" past its end.
func paneRow(pane string, i int) string {
	rows := strings.Split(pane, "\n")
	if i >= len(rows) {
		return ""
	}
	return rows[i]
}

// The regression, through the real bytes. claude parks the cursor on the
// highlighted row and then sends the kitty keyboard query CSI ? u, which
// bare vt10x runs as "restore cursor"; every repaint after it landed at the
// top of the grid. Each Down must instead move the ❯ between the two rows
// next to the footer, and nothing may appear above the dialog's rule — in
// production's emulator and in the oracle alike.
func TestScreen_TrustDialogTracksTheSelection(t *testing.T) {
	t.Parallel()
	initial, downs := trustDialogCapture(t)

	for name, newEmu := range emulators() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := newEmu()
			s.write(initial)
			sel, ok := selectedMenuOption(s.text())
			require.True(t, ok)
			require.Equal(t, "No, exit", sel, "claude 2.1.270 preselects the decline")

			for i, want := range []string{"Yes, I trust this folder", "No, exit", "Yes, I trust this folder"} {
				s.write(downs[i])
				pane := s.text()
				sel, ok := selectedMenuOption(pane)
				require.True(t, ok)
				require.Equal(t, want, sel, "after Down %d; pane:\n%s", i+1, pane)
				require.Empty(t, paneRow(pane, 0),
					"after Down %d a repaint landed above the dialog — the grid has lost claude's cursor; pane:\n%s", i+1, pane)
				require.Equal(t, 1, strings.Count(pane, "Yes, I trust this folder"),
					"after Down %d the menu appears twice; pane:\n%s", i+1, pane)
			}
		})
	}
}

// The control that says why the oracle's filter exists. Bare vt10x, same
// bytes, reproduces the "second copy" the 2.1.269 failure showed. If this
// starts failing after a vt10x bump, the emulator has changed how it reads
// private CSI sequences: re-read dropPrivateCSI's reasons before deleting
// anything.
func TestScreen_BareVT10xLosesTheCursor(t *testing.T) {
	t.Parallel()
	initial, downs := trustDialogCapture(t)

	term := vt10x.New(vt10x.WithSize(paneCols, paneRows))
	_, _ = term.Write(initial)
	_, _ = term.Write(downs[0])
	top, _, _ := strings.Cut(term.String(), "\n")
	require.Contains(t, top, "No, exit",
		"bare vt10x no longer misplaces claude's repaint at row 0 — the premise of the oracle's CSI filter has changed")
}

// The walk end to end on the real grid: WaitForReady must press Down once
// and confirm on the trust row. Before the fix it read "No, exit" off rows
// the repaints no longer reached, burned every move, and reported that the
// dialog's options had changed shape.
func TestTrustModal_WalksTheCapturedDialog(t *testing.T) {
	initial, downs := trustDialogCapture(t)

	var mu sync.Mutex
	s := newScreen()
	s.write(initial)
	confirmed, pressed := false, 0
	var chosen string
	capturePaneFn = func(context.Context, string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if confirmed {
			return readyFooterPane, nil
		}
		return s.text(), nil
	}
	sendDownFn = func(context.Context, string) error {
		mu.Lock()
		defer mu.Unlock()
		if pressed < len(downs) {
			s.write(downs[pressed])
		}
		pressed++
		return nil
	}
	sendEnterFn = func(context.Context, string) error {
		mu.Lock()
		defer mu.Unlock()
		chosen, _ = selectedMenuOption(s.text())
		confirmed = true
		return nil
	}
	t.Cleanup(func() {
		capturePaneFn = CapturePane
		sendEnterFn = SendEnter
		sendDownFn = SendDown
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, WaitForReady(ctx, "s"))
	require.Equal(t, "Yes, I trust this folder", chosen)
	require.Equal(t, 1, pressed)
}

func TestDropPrivateCSI(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		seq  string
		drop bool
		why  string
	}{
		{"\x1b[?u", true, "kitty keyboard query — vt10x: restore cursor"},
		{"\x1b[>1u", true, "kitty push flags"},
		{"\x1b[<u", true, "kitty pop flags"},
		{"\x1b[=1;1u", true, "kitty set flags"},
		{"\x1b[?1049s", true, "XTSAVE — vt10x: save cursor"},
		{"\x1b[>0q", true, "XTVERSION"},
		{"\x1b[>4;2m", true, "XTMODKEYS — vt10x: SGR reset"},
		{"\x1b[?1;1;0S", true, "XTSMGRAPHICS — vt10x: scroll up"},

		{"\x1b[?2026h", false, "synchronized output: a mode vt10x handles"},
		{"\x1b[?25l", false, "cursor visibility"},
		{"\x1b[?1049h", false, "alternate screen"},
		{"\x1b[?2J", false, "selective erase: ED with no protected cells"},
		{"\x1b[?K", false, "selective erase line"},
		{"\x1b[u", false, "plain ANSI.SYS restore cursor means what it says"},
		{"\x1b[s", false, "plain ANSI.SYS save cursor"},
		{"\x1b[4A", false, "cursor up"},
		{"\x1b[c", false, "DA1"},
	} {
		t.Run(tc.why, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.drop, dropPrivateCSI([]byte(tc.seq)), "%q", tc.seq)
		})
	}
}

// A 4 KiB read can end anywhere. The render must not depend on where: CSI
// ? u split at every byte leaves the cursor where it was.
func TestScreen_PrivateCSISplitAcrossReads(t *testing.T) {
	t.Parallel()
	stream := "\x1b[1;1H\x1b7\x1b[6;11H\x1b[?uX"
	for name, newEmu := range emulators() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for cut := 1; cut < len(stream); cut++ {
				s := newEmu()
				s.write([]byte(stream[:cut]))
				s.write([]byte(stream[cut:]))
				require.Equal(t, strings.Repeat(" ", 10)+"X", paneRow(s.text(), 5), "cut at byte %d", cut)
			}
		})
	}
}

// A C0 control met inside a CSI is executed and the sequence carries on —
// and a private sequence that is ignored must not take the control with it.
func TestScreen_ControlsInsideAPrivateCSIStillRun(t *testing.T) {
	t.Parallel()
	for name, newEmu := range emulators() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := newEmu()
			s.write([]byte("\x1b[1;11H\x1b[?\ru" + "X"))
			require.Equal(t, "X", paneRow(s.text(), 0), "the CR inside the sequence was lost")
		})
	}
}

// A read boundary inside a multi-byte rune must not lose the glyph.
func TestScreen_RuneSplitAcrossReads(t *testing.T) {
	t.Parallel()
	glyph := []byte("a" + ReadyGlyph + "b")
	for name, newEmu := range emulators() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for cut := 2; cut <= 3; cut++ { // inside the three bytes of ❯
				s := newEmu()
				s.write(glyph[:cut])
				s.write(glyph[cut:])
				require.Equal(t, "a"+ReadyGlyph+"b", s.text(), "cut at byte %d", cut)
			}
		})
	}

	// Control: bare vt10x's Write returns short and discards a trailing
	// partial rune, and the pump cannot resend it — the reason for the
	// oracle's carry.
	term := vt10x.New(vt10x.WithSize(paneCols, paneRows))
	_, _ = term.Write(glyph[:2])
	_, _ = term.Write(glyph[2:])
	require.NotContains(t, term.String(), ReadyGlyph,
		"bare vt10x now keeps a split rune — the oracle's carry may no longer be needed")
}

// A terminal answers queries by writing to its input, and x/vt writes those
// answers into a synchronous pipe. Unread, the first query blocks Write
// inside the screen's lock — and every pane read with it. claude sends
// device-attribute and kitty queries at startup, so this is the difference
// between a session and a hang.
func TestScreen_QueriesNeverBlockTheWrite(t *testing.T) {
	t.Parallel()
	s := newScreen()
	defer s.close()
	queries := "\x1b[c\x1b[>0q\x1b[?u\x1b[6n\x1b[5n\x1b[?2026$p"
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 200 {
			s.write([]byte(queries + "x"))
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a terminal query blocked the write: the emulator's answers are not being drained")
	}
	require.Contains(t, s.text(), "xxxx", "and the writes around the queries rendered")
}

// After the session ends the grid is still readable, and a late write — the
// pump can deliver one after reap — renders rather than blocking on the
// closed answer pipe.
func TestScreen_ReadableAndWritableAfterClose(t *testing.T) {
	t.Parallel()
	s := newScreen()
	s.write([]byte("before"))
	s.close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.write([]byte("\x1b[c after"))
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a query after close blocked the write")
	}
	require.Equal(t, "before after", s.text())
}

// TestEmulators_AgreeOnTheCapturedBytes is R4a on the corpus: every captured
// byte fixture, replayed segment by segment through both emulators, must
// render the same pane. A disagreement names a sequence one of them misreads.
func TestEmulators_AgreeOnTheCapturedBytes(t *testing.T) {
	t.Parallel()
	initial, downs := trustDialogCapture(t)
	segments := append([][]byte{initial}, downs...)

	prod, oracle := newScreen(), newVT10xOracle()
	defer prod.close()
	for i, seg := range segments {
		prod.write(seg)
		oracle.write(seg)
		require.Equal(t, oracle.text(), prod.text(), "segment %d: x/vt and vt10x+filter render differently", i)
		require.Equal(t, paneReadsOf(oracle.text()), paneReadsOf(prod.text()), "segment %d", i)
	}
}

// A failed walk reports every pass. One pane at failure cannot separate
// keys that never reach the dialog from a read looking at the wrong rows.
func TestDescribeMenuWalk(t *testing.T) {
	t.Parallel()
	got := describeMenuWalk([]menuMove{
		{selected: "No, exit", moved: false, pane: "pane A"},
		{selected: "No, exit", moved: false, pane: "pane A"},
		{selected: "No, exit", moved: true, pane: "pane B"},
	}, "pane C")

	require.Contains(t, got, `move 1: read "No, exit", pressed Down, the read did not change within 2s`)
	require.Contains(t, got, "(identical to move 1's)")
	require.Contains(t, got, `move 3: read "No, exit", pressed Down, the read changed`)
	require.Contains(t, got, "pane B")
	require.True(t, strings.HasSuffix(got, "pane at failure:\npane C"))
	require.Equal(t, 1, strings.Count(got, "pane A"), "an unchanged pane is named, not repeated")
}
