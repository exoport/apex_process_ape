package repl

// privateCSIFilter removes, from the PTY stream on its way into vt10x, the
// private control sequences vt10x executes as something they are not.
//
// vt10x recognises one private marker, `?`, and honours it only for SM/RM
// (`h`/`l`) and DECSTBM (`r`). A sequence opening with `<`, `=` or `>` fails
// its argument parse and runs as the UNPREFIXED command with default
// arguments, and `u`/`s` ignore the marker entirely. What a real terminal
// treats as a query or a mode switch that draws nothing, vt10x turns into a
// cursor move:
//
//	CSI > flags u   kitty keyboard push     → DECRC, restore cursor
//	CSI < u         kitty keyboard pop      → DECRC, restore cursor
//	CSI ? u         kitty keyboard query    → DECRC, restore cursor
//	CSI > 4;2 m     xterm modifyOtherKeys   → SGR reset
//	CSI > 0 q       XTVERSION               → (unknown, ignored)
//
// Claude Code 2.1.270 sends the first three around every keypress. With the
// cursor last saved at the origin, each repaint that follows lands on the top
// rows of the grid instead of where the dialog is — so an arrow key moved the
// folder-trust selection in claude while the menu in ape's pane never
// changed, and the trust walk gave up with "No, exit" still selected. That
// failure is what make check-hooks caught.
//
// The rule is narrow on purpose. Dropping a sequence is the only thing here
// that can change what vt10x renders, so it is reserved for sequences no
// real terminal draws with: every `<`/`=`/`>`-prefixed CSI (vt10x implements
// none of them, so each one reaching it is a default-argument command nobody
// sent), and the `?`-prefixed cursor save/restore pair. Every other byte,
// including every `?` mode set that vt10x does understand, passes through
// unchanged and in order.
//
// It is a streaming filter: a sequence can straddle two PTY reads, so an
// unfinished one is held until its final byte arrives.
type privateCSIFilter struct {
	pending []byte // an ESC, or ESC [ and what has followed it so far
}

// privateCSIMaxLen matches vt10x's own bound: a CSI that has not ended after
// this many bytes is flushed through rather than held indefinitely.
const privateCSIMaxLen = 256

const (
	asciiESC = 0x1b
	csiOpen  = '['
	// A byte below csiControlEnd is a C0 control; csiFinalLo..csiFinalHi is
	// the range that ends a CSI (ECMA-48 §5.4).
	csiControlEnd = 0x20
	csiFinalLo    = 0x40
	csiFinalHi    = 0x7e
)

// apply returns, as a new slice, the bytes of p that vt10x should see. Bytes
// of an unfinished sequence are held back and emitted by a later call.
func (f *privateCSIFilter) apply(p []byte) []byte {
	out := make([]byte, 0, len(p)+len(f.pending))
	for _, b := range p {
		switch {
		case len(f.pending) == 0:
			if b == asciiESC {
				f.pending = append(f.pending, b)
				continue
			}
			out = append(out, b)

		case len(f.pending) == 1: // ESC seen
			if b == csiOpen {
				f.pending = append(f.pending, b)
				continue
			}
			out = append(out, f.pending...)
			f.pending = f.pending[:0]
			if b == asciiESC {
				f.pending = append(f.pending, b)
				continue
			}
			out = append(out, b)

		default: // inside ESC [
			// A control byte interrupts nothing in vt10x — it runs it and
			// keeps parsing — so handing everything over as-is keeps its view
			// identical. Only a sequence this filter sees whole is ever dropped.
			if b < csiControlEnd {
				out = append(out, f.pending...)
				out = append(out, b)
				f.pending = f.pending[:0]
				continue
			}
			f.pending = append(f.pending, b)
			if b >= csiFinalLo && b <= csiFinalHi {
				if !misreadPrivateCSI(f.pending) {
					out = append(out, f.pending...)
				}
				f.pending = f.pending[:0]
				continue
			}
			if len(f.pending) >= privateCSIMaxLen {
				out = append(out, f.pending...)
				f.pending = f.pending[:0]
			}
		}
	}
	return out
}

// misreadPrivateCSI reports whether a complete `ESC [ … final` sequence is one
// vt10x would execute as a different command (see privateCSIFilter).
func misreadPrivateCSI(seq []byte) bool {
	if len(seq) < 3 {
		return false
	}
	marker, final := seq[2], seq[len(seq)-1]
	switch marker {
	case '<', '=', '>':
		return true
	case '?':
		return final == 'u' || final == 's'
	}
	return false
}
