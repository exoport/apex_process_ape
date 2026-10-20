package repl

import (
	"io"
	"regexp"
	"strings"
	"sync"

	uv "github.com/charmbracelet/ultraviolet"
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

// idlePrompt reports whether a row of the grid is claude's prompt line with
// nothing typed on it: the ❯ glyph first (blank cells before it allowed),
// then only cells that are blank — U+00A0 included — or drawn faint, the
// first of them possibly reverse video.
//
// Faint is how claude draws the placeholder it sometimes puts on an empty
// prompt (2.1.294: `❯` U+00A0, then SGR 2 `Try "refactor <filepath>"` SGR
// 22). The text of that row is not empty, so emptyPromptRe rejects it; the
// cells say what the text cannot — nothing on the line was typed. Read from
// the attributes, never the wording, so a new hint changes nothing.
//
// Reverse video is claude's own cursor. It hides the terminal's and, in some
// terminals (measured under tmux on 2.1.294 and 2.1.304, not in the eval's),
// draws one over the hint's first character: SGR 7 `T` SGR 27, then SGR 2
// `ry …`. So a reverse cell is accepted only where an empty input's cursor
// sits — the first thing after the glyph — and only with faint cells after
// it; a lone reverse character is one typed with the cursor on it.
//
// A typed line is drawn in the normal weight, and so is a menu row (`❯ 1.
// …`): both still fail.
func (s *screen) idlePrompt() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for y := range s.term.Height() {
		if s.idlePromptRow(y) {
			return true
		}
	}
	return false
}

// idlePromptRow is idlePrompt for row y. The caller holds s.mu.
func (s *screen) idlePromptRow(y int) bool {
	blank := func(c *uv.Cell) bool {
		return c == nil || c.Content == "" || c.Content == " " || c.Content == "\u00a0"
	}
	width := s.term.Width()
	x := 0
	for x < width && blank(s.term.CellAt(x, y)) {
		x++
	}
	if x == width || s.term.CellAt(x, y).Content != ReadyGlyph {
		return false
	}
	drawn, cursor, faint := false, false, false
	for x++; x < width; x++ {
		c := s.term.CellAt(x, y)
		switch {
		case blank(c):
			continue
		case c.Style.Attrs&uv.AttrFaint != 0:
			faint = true
		case c.Style.Attrs&uv.AttrReverse != 0 && !drawn:
			cursor = true
		default:
			return false
		}
		drawn = true
	}
	return !cursor || faint
}

// untypedInputBox reports whether the grid shows claude's input line inside
// its input box: a row with the ❯ glyph first, between the two horizontal
// rules claude draws around the box, and not a numbered menu row (`❯ 1. …`).
//
// It does not look at what follows the glyph, so it says nothing about
// whether anything was typed: it is for the startup probe alone, where ape
// has typed nothing and whatever stands on that line is claude's own — the
// placeholder hint. idlePrompt reads the hint from its faint cells, and on a
// monochrome screen (NO_COLOR, TERM=dumb) there are none: the hint is drawn
// in the normal weight, like typed text, and the probe declared a working
// claude broken (atomix on 2.1.305, D269; 2.1.304 does the same with the
// hint shown). The frame is drawn either way.
func (s *screen) untypedInputBox() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for y := range s.term.Height() {
		if s.inputBoxRow(y) {
			return true
		}
	}
	return false
}

// inputBoxRow is untypedInputBox for row y. The caller holds s.mu.
func (s *screen) inputBoxRow(y int) bool {
	text := func(y int) string {
		var b strings.Builder
		for x := range s.term.Width() {
			if c := s.term.CellAt(x, y); c != nil {
				b.WriteString(c.Content)
			}
		}
		return strings.TrimSpace(strings.ReplaceAll(b.String(), "\u00a0", " "))
	}
	rule := func(y int) bool {
		t := text(y)
		return t != "" && strings.Trim(t, "─") == ""
	}
	if y < 1 || y+1 >= s.term.Height() {
		return false
	}
	rest, ok := strings.CutPrefix(text(y), ReadyGlyph)
	return ok && !menuRowRe.MatchString(rest) && rule(y-1) && rule(y+1)
}

// menuRowRe is the text after a menu row's ❯: a numbered option.
var menuRowRe = regexp.MustCompile(`^\s*\d+[.)]`)

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
