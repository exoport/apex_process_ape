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
	// mu guards term. vt.Emulator is not safe for concurrent use, and
	// SafeEmulator does not lock String, which is the read ape makes.
	mu   sync.Mutex
	term *vt.Emulator
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
	_, _ = s.term.Write(p)
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
