package repl

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/hinshun/vt10x"
)

// vt10xOracle is the emulator ape rendered panes with until charmbracelet/x/vt
// replaced it: hinshun/vt10x, behind the filter written for claude 2.1.269.
// It is kept, in test code only, as a SECOND OPINION on the same bytes
// (resilience remedy R4a): every captured byte fixture is replayed through
// both, and the live Claude Code gate asks both what ape would read off a
// real session's screen. One emulator misreading a sequence the other
// handles is the class that cost the 2.1.269 incident thirty-odd hours, and
// two emulators disagreeing is how it shows up before anyone is misled.
//
// The filter below is the one written for claude 2.1.269 (8061dcc), unchanged; its
// comments are why each piece exists.
type vt10xOracle struct {
	term vt10x.Terminal

	// held is the tail of the last write not yet forwarded: an unfinished
	// escape sequence, or the leading bytes of a multi-byte rune the read
	// split.
	held []byte
}

func newVT10xOracle() *vt10xOracle {
	return &vt10xOracle{term: vt10x.New(vt10x.WithSize(paneCols, paneRows))}
}

// paneReads is everything ape decides from a pane. Two emulators may differ
// in ways that change none of it — a stray style, an off-screen row — and a
// gate that failed on those would teach its reader to ignore it. They may
// not differ in these.
type paneReads struct {
	Selected    string
	HasMenu     bool
	TrustDialog bool
	Ready       bool
}

func paneReadsOf(pane string) paneReads {
	sel, ok := selectedMenuOption(pane)
	return paneReads{Selected: sel, HasMenu: ok, TrustDialog: trustModalVisible(pane), Ready: replReady(pane)}
}

// write forwards p to the emulator, minus the sequences dropPrivateCSI
// rejects, holding back whatever cannot be judged until more bytes arrive.
func (s *vt10xOracle) write(p []byte) {
	data := slices.Concat(s.held, p)
	s.held = nil
	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); {
		if data[i] != esc {
			out = append(out, data[i])
			i++
			continue
		}
		n, complete := csiLen(data[i:])
		switch {
		case !complete:
			// An escape at the end of this read. Holding it is the only
			// way to judge the sequence: forwarding the prefix would
			// commit vt10x to it before the final byte says what it is.
			s.held = append(s.held, data[i:]...)
			i = len(data)
		case n == 0:
			// ESC not followed by '[' — not a CSI; vt10x's to handle.
			out = append(out, data[i])
			i++
		default:
			seq := data[i : i+n]
			if !dropPrivateCSI(seq) {
				out = append(out, seq...)
			} else {
				// vt10x would still have executed any C0 control carried
				// inside the sequence; only the command itself is dropped.
				for _, c := range seq[2:] {
					if isC0(c) {
						out = append(out, c)
					}
				}
			}
			i += n
		}
	}
	// vt10x's Write returns short on a trailing partial rune and discards
	// it: a caller that ignores the count — as the pump must, it has
	// nowhere to resend from — loses the character outright. A 4 KiB read
	// boundary lands inside ❯ or ─ (three bytes each) often enough to
	// matter, so the partial rune waits for the next read instead.
	if s.held == nil {
		if tail := partialRuneTail(out); tail > 0 {
			s.held = append(s.held, out[len(out)-tail:]...)
			out = out[:len(out)-tail]
		}
	}
	_, _ = s.term.Write(out)
}

// text returns the grid as plain text — each row trimmed of trailing
// padding, fully-empty trailing rows removed.
func (s *vt10xOracle) text() string {
	lines := strings.Split(s.term.String(), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	end := len(lines)
	for end > 0 && lines[end-1] == "" {
		end--
	}
	return strings.Join(lines[:end], "\n")
}

const (
	// csiFinalMin and csiFinalMax bound the byte that ends a CSI sequence.
	csiFinalMin = 0x40
	csiFinalMax = 0x7e
)

// isC0 reports whether c is a control code as vt10x classifies one.
func isC0(c byte) bool { return c < 0x20 || c == 0x7f }

// maxCSILen mirrors vt10x's own bound: it parses a CSI once its buffer
// reaches 256 bytes whatever the last byte is, so a sequence that long is
// forwarded rather than held for ever.
const maxCSILen = 256

// csiLen reports the length of the CSI sequence at the start of b (which
// begins with ESC). n is 0 when b is not a CSI; complete is false when b
// ends before the sequence can be classified.
//
// Delimited the way vt10x delimits it — ESC '[' then bytes until one in
// 0x40–0x7E — so the filter never splits the stream at a different point
// from the emulator it feeds. C0 controls inside the sequence are carried
// along: vt10x executes them without leaving the sequence, and forwarding
// them in their original position preserves that.
func csiLen(b []byte) (n int, complete bool) {
	if len(b) < 2 {
		return 0, false
	}
	if b[1] != '[' {
		return 0, true
	}
	for i := 2; i < len(b); i++ {
		if b[i] == esc || i >= maxCSILen {
			// A new ESC aborts the sequence in vt10x, and an overlong one
			// is parsed as-is; either way what came before is forwarded
			// unjudged, exactly as vt10x would have consumed it.
			return i, true
		}
		if b[i] >= csiFinalMin && b[i] <= csiFinalMax {
			return i + 1, true
		}
	}
	return 0, false
}

// dropPrivateCSI reports whether a complete CSI sequence is one vt10x
// would execute as a DIFFERENT command than the one sent.
//
// vt10x recognises '?' as the only private marker and honours it only for
// set/reset mode; for '<', '=' and '>' it fails to parse the arguments and
// runs the public command with defaults. So the private forms modern TUIs
// send are executed as whatever the final byte means unmarked:
//
//   - CSI ? u  (kitty keyboard query), CSI > u (push), CSI < u (pop),
//     CSI = u (set)  →  ANSI.SYS restore cursor
//   - CSI ? s  (XTSAVE), CSI > s (XTSHIFTESCAPE)  →  save cursor
//   - CSI ? S  (XTSMGRAPHICS), CSI > T (XTRMTITLE)  →  scroll the grid
//
// The first is not hypothetical. Claude Code 2.1.269 sends CSI ? u at
// startup, right after parking the cursor on the trust dialog's highlighted
// row, and vt10x jumped the cursor to the top of the screen. Every
// relative-move repaint after that landed on rows 0–1: the grid showed a
// "second copy" of the menu above the prose, and the real rows next to the
// footer were never updated again, so the selection read back as
// "No, exit" whatever the arrows did.
//
// A terminal that does not implement a sequence ignores it, so dropping is
// the faithful render. The allow-list is the private forms vt10x does
// handle: modes (?h/?l — alternate screen, cursor visibility, synchronized
// output, bracketed paste), and selective erase (?J/?K), which with no
// protected cells erases exactly what ED/EL do.
func dropPrivateCSI(seq []byte) bool {
	if len(seq) < len("\x1b[?u") { // ESC [ marker final
		return false
	}
	switch seq[2] {
	case '<', '=', '>', '?':
	default:
		return false
	}
	final := seq[len(seq)-1]
	if seq[2] == '?' && strings.IndexByte("hlJK", final) >= 0 {
		return false
	}
	return true
}

// partialRuneTail returns how many bytes at the end of b are the start of
// a UTF-8 rune the buffer does not finish, or 0.
func partialRuneTail(b []byte) int {
	for back := 1; back <= utf8.UTFMax-1 && back <= len(b); back++ {
		c := b[len(b)-back]
		if c < utf8.RuneSelf {
			return 0 // ASCII: nothing pending
		}
		if utf8.RuneStart(c) {
			if utf8.FullRune(b[len(b)-back:]) {
				return 0
			}
			return back
		}
	}
	return 0
}
