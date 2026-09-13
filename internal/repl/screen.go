package repl

import (
	"io"
	"strings"
	"sync"

	"github.com/charmbracelet/x/vt"
)

// screen is the grid a session's PTY output is rendered into. Every pane ape
// reads — the trust walk, replReady, the step-out field — is read from here,
// so a desynchronised grid misleads all of them at once, and it does so
// looking like the program changed shape.
//
// The emulator is charmbracelet/x/vt. It replaced hinshun/vt10x, whose
// last commit is from 2022 and which misread the private CSI sequences a
// current TUI sends: claude 2.1.269's kitty keyboard query (CSI ? u) ran as
// "restore cursor", and every repaint after it landed at the top of the
// screen. That needed a hand-written filter in front of vt10x, and a split
// multi-byte rune needed a carry that filter never had, each fixing one
// misreading once someone had lost a day to it. x/vt parses both correctly
// as a matter of course, with nothing in front of it — see
// screen_sequences_test.go for what that was measured against. vt10x lives
// on in test code as a second opinion on the same bytes — see vt10xOracle.
type screen struct {
	// mu guards term and strings. vt.Emulator is not safe for concurrent
	// use, and SafeEmulator does not lock String, which is the read ape
	// makes.
	mu   sync.Mutex
	term *vt.Emulator
	// strings removes string controls before the emulator sees them.
	strings stringControlFilter
	// replies is the emulator's answer pipe, closed with the session.
	replies io.Closer
}

func newScreen() *screen {
	term := vt.NewEmulator(paneCols, paneRows)
	s := &screen{term: term}
	if c, ok := term.InputPipe().(io.Closer); ok {
		s.replies = c
	}
	// A terminal answers queries — device attributes, cursor position,
	// the kitty keyboard flags — by writing to its input, and x/vt writes
	// those answers into a SYNCHRONOUS pipe. Nobody reading it would block
	// the first Write that meets a query, inside the lock, and with it every
	// pane read: `\x1b[c` from claude would freeze the session.
	//
	// The answers are read and DISCARDED, never forwarded to the child.
	// vt10x never answered, claude runs correctly without answers, and
	// answering could change what claude does — confirm kitty keyboard
	// support and it may encode keys in a protocol ape's arrow and Enter
	// bytes do not speak. What claude sees of this terminal stays what it
	// always was.
	go func() { _, _ = io.Copy(io.Discard, term) }()
	return s
}

// write feeds the emulator. Safe to call concurrently with text.
func (s *screen) write(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if out := s.strings.filter(p); len(out) > 0 {
		_, _ = s.term.Write(out)
	}
}

// text returns the grid as plain text — each row trimmed of trailing
// padding, fully-empty trailing rows removed.
func (s *screen) text() string {
	s.mu.Lock()
	raw := s.term.String()
	s.mu.Unlock()
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	end := len(lines)
	for end > 0 && lines[end-1] == "" {
		end--
	}
	return strings.Join(lines[:end], "\n")
}

// close releases the goroutine draining the emulator's answers. The grid
// stays readable afterwards; a write after close still renders, and any
// answer it provokes fails on the closed pipe instead of blocking.
func (s *screen) close() {
	if s.replies != nil {
		_ = s.replies.Close()
	}
}

// stringControlFilter removes the string controls — OSC, DCS, SOS, PM, APC:
// ESC ] … ESC P … ESC X … ESC ^ … ESC _ …, each ended by BEL or ESC \ —
// from the byte stream before the emulator parses it.
//
// ape renders a screen as text, and a string control never puts text on a
// screen: it sets a window title, carries a hyperlink target, writes the
// clipboard, draws an image. Dropping them changes nothing ape reads. A
// hyperlink's visible text sits OUTSIDE its OSC 8 controls and still renders.
//
// Keeping them is what did change something. x/vt's parser (charmbracelet/
// x/ansi) ends a string at byte 0x9C — the 8-bit form of ST — even when that
// byte is the middle of a UTF-8 character in the payload. claude sets its
// title to "✳ Claude Code", and ✳ is E2 9C B3: the parser ended the title at
// the 9C and PRINTED " Claude Code" onto the grid where the cursor stood. The
// whole spinner set claude cycles through its title while it works — ✳ ✶ ✻
// ✽ ✢ — is E2 9C xx, so every title update could write text over whatever row
// the cursor was on. It showed first as a garbled logo row, found by
// comparing x/vt's pane of a real session with the vt10x oracle's.
//
// UTF-8-safe by construction: inside a string no byte but BEL, ESC, CAN or
// SUB is looked at, so no continuation byte can end one. Stateful across
// writes, because a 4 KiB read can split a title anywhere.
type stringControlFilter struct {
	state stringFilterState
}

type stringFilterState int

const (
	sfGround    stringFilterState = iota
	sfEscape                      // an ESC was seen and held
	sfString                      // inside a string control: everything dropped
	sfStringEsc                   // an ESC inside a string: ST, or a new sequence
)

const (
	bel = 0x07
	can = 0x18
	sub = 0x1a
	esc = 0x1b
)

func (f *stringControlFilter) filter(p []byte) []byte {
	out := make([]byte, 0, len(p)+1)
	for _, b := range p {
		out = f.step(out, b)
	}
	return out
}

func (f *stringControlFilter) step(out []byte, b byte) []byte {
	switch f.state {
	case sfEscape:
		switch b {
		case ']', 'P', 'X', '^', '_':
			f.state = sfString // the held ESC and the introducer are both dropped
			return out
		case esc:
			return append(out, esc) // the earlier ESC stands alone; hold this one
		default:
			f.state = sfGround
			return append(out, esc, b)
		}
	case sfString:
		switch b {
		case bel, can, sub:
			f.state = sfGround
		case esc:
			f.state = sfStringEsc
		}
		return out
	case sfStringEsc:
		if b == '\\' {
			f.state = sfGround // ST: the string is over
			return out
		}
		// An ESC that is not ST aborts the string and begins a sequence of
		// its own, exactly as a terminal reads it.
		f.state = sfEscape
		return f.step(out, b)
	default:
		if b == esc {
			f.state = sfEscape
			return out
		}
		return append(out, b)
	}
}
