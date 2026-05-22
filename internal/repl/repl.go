// Package repl drives a child program (typically `claude`) through an
// in-process pseudo-terminal so callers can type into it and read its
// rendered output programmatically. The interactive runner ships its
// per-step prompts as PTY keystrokes (Write to the master end + Enter),
// and reads claude's rendered output through a VT-grid emulator. API
// surface: NewSession / KillSession / HasSession / CapturePane /
// SendText / SendEnter / SendCommand / WaitForReady, keyed by session
// name. PLAN-8 (2026-05-22) replaced the prior `internal/tmux` shim
// that shelled out to a `tmux` binary; the keystroke-delivery shape is
// the same (writing bytes to the PTY master is what `tmux send-keys -l`
// did under the hood), only the PTY ownership moved in-process.
//
// The PTY backend is github.com/aymanbagabas/go-pty, which transparently
// uses Unix PTYs on Linux/macOS and ConPTY on Windows — so ape works
// natively under Git Bash on Windows 11 without WSL or a tmux binary
// on PATH.
//
// PTY output is parsed through a github.com/hinshun/vt10x VT100/xterm
// emulator. CapturePane returns the rendered grid as plain text — no
// ANSI escape sequences, no cursor-positioning noise — matching tmux's
// `capture-pane -p` semantics rather than dumping raw bytes.
//
// Known limitations:
//
//   - No external attach: a session lives and dies with the ape
//     process. There is no equivalent of the old `tmux attach -t …`
//     for an in-flight run. Pipeline runs that want live introspection
//     should tail the per-step ndjson event log instead.
//   - Visible-grid scrollback only. vt10x's grid is sized to claude's
//     perceived terminal (200×50), matching the ioctl winsize claude
//     reads. Lines that scroll off the top via `\n` at the bottom are
//     gone — tmux's history buffer (default 2000 lines) isn't
//     reproduced. In practice the manifest's per-step `step-out` field
//     and the debug stderr mirror are the consumers, and both look at
//     the most recent screenful; the authoritative stream of model
//     output flows through the bridge's hook events, not pane capture.
package repl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/aymanbagabas/go-pty"
	"github.com/hinshun/vt10x"
)

// PromptSettle is the wait between typing a command and pressing
// Enter. Long prompts otherwise submit before the REPL has finished
// loading them — confirmed by the community /pmux pattern and
// anthropics/claude-code#40168. 300ms is the well-known safe value.
const PromptSettle = 300 * time.Millisecond

// ReadyPollInterval is how often we capture pane output while waiting
// for the ❯ glyph that signals the claude REPL is accepting input.
const ReadyPollInterval = 250 * time.Millisecond

// ReadyGlyph is the prompt glyph claude renders when ready.
const ReadyGlyph = "❯"

// Pane geometry. Carries forward the PLAN-6 tmux-era `-x 200 -y 50`
// so capture snapshots have the same shape across the migration —
// claude's TUI wraps to this width and expects this height from the
// ioctl winsize.
const (
	paneCols = 200
	paneRows = 50
)

type session struct {
	name string
	ptm  pty.Pty
	cmd  *pty.Cmd

	// term is the VT100/xterm emulator that turns the PTY byte stream
	// into a 200×50 rendered grid. It owns its own mutex; Write and
	// String are both safe to call from independent goroutines.
	term vt10x.Terminal

	done chan struct{}
}

var (
	regMu    sync.Mutex
	registry = map[string]*session{}
)

// NewSession spawns argv attached to a PTY, registers it under name,
// and starts background readers that accumulate pane output for
// CapturePane / WaitForReady. argv[0] is the program; argv[1:] its
// arguments. dir becomes the child's working directory.
func NewSession(_ context.Context, name, dir string, argv []string) error {
	if len(argv) == 0 {
		return errors.New("repl: empty argv")
	}

	regMu.Lock()
	if _, exists := registry[name]; exists {
		regMu.Unlock()
		return fmt.Errorf("repl: session %q already exists", name)
	}
	regMu.Unlock()

	ptm, err := pty.New()
	if err != nil {
		return fmt.Errorf("repl: new pty: %w", err)
	}
	if err := ptm.Resize(paneCols, paneRows); err != nil {
		_ = ptm.Close()
		return fmt.Errorf("repl: resize pty: %w", err)
	}

	cmd := ptm.Command(argv[0], argv[1:]...)
	cmd.Dir = dir

	if err := cmd.Start(); err != nil {
		_ = ptm.Close()
		return fmt.Errorf("repl: start %q: %w", argv[0], err)
	}

	s := &session{
		name: name,
		ptm:  ptm,
		cmd:  cmd,
		term: vt10x.New(vt10x.WithSize(paneCols, paneRows)),
		done: make(chan struct{}),
	}
	regMu.Lock()
	registry[name] = s
	regMu.Unlock()

	go s.pump()
	go s.reap()

	return nil
}

// pump drains PTY output into the VT emulator until the PTY closes
// (child exited, or KillSession closed the master). vt10x.Write
// acquires the terminal's internal lock, so concurrent CapturePane
// reads are safe.
func (s *session) pump() {
	buf := make([]byte, 4096)
	for {
		n, err := s.ptm.Read(buf)
		if n > 0 {
			_, _ = s.term.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// reap waits for the child process and signals done so HasSession
// reports false once the REPL has exited on its own.
func (s *session) reap() {
	_ = s.cmd.Wait()
	close(s.done)
}

// snapshot returns the VT grid as plain text — each row trimmed of
// trailing padding spaces, fully-empty trailing rows removed. vt10x's
// String() acquires its own lock, so no extra synchronization is
// needed here.
func (s *session) snapshot() string {
	raw := s.term.String()
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

func lookup(name string) (*session, bool) {
	regMu.Lock()
	s, ok := registry[name]
	regMu.Unlock()
	return s, ok
}

// KillSession terminates the child and tears down the PTY. Not-found
// is treated as success so callers can use it as a pre-check (`_ =
// KillSession(...)` before `NewSession`).
func KillSession(_ context.Context, name string) error {
	regMu.Lock()
	s, ok := registry[name]
	if ok {
		delete(registry, name)
	}
	regMu.Unlock()
	if !ok {
		return nil
	}
	if s.cmd.Process != nil {
		// On Unix, signal the whole process group so grandchildren
		// (e.g. a future claude-spawned background task) don't
		// orphan. terminateGroup is a no-op on Windows. The direct
		// Process.Kill is the belt-and-suspenders for both
		// platforms — on Unix, SIGTERM to the group has already
		// fired and SIGKILL is scheduled; on Windows, this is the
		// only termination signal.
		terminateGroup(s.cmd.Process.Pid)
		_ = s.cmd.Process.Kill()
	}
	_ = s.ptm.Close()
	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
	}
	return nil
}

// HasSession reports whether the session is alive: registered AND the
// child process hasn't exited yet.
func HasSession(_ context.Context, name string) bool {
	s, ok := lookup(name)
	if !ok {
		return false
	}
	select {
	case <-s.done:
		return false
	default:
		return true
	}
}

// CapturePane returns the rendered VT grid as plain text. ANSI escape
// sequences are interpreted, not included in the output.
func CapturePane(_ context.Context, name string) (string, error) {
	s, ok := lookup(name)
	if !ok {
		return "", fmt.Errorf("repl: no session %q", name)
	}
	return s.snapshot(), nil
}

// SendText writes text literally to the PTY input without submitting
// it. Successor to tmux's `send-keys -l`: every byte goes through
// verbatim so leading slashes and special chars survive.
func SendText(_ context.Context, name, text string) error {
	s, ok := lookup(name)
	if !ok {
		return fmt.Errorf("repl: no session %q", name)
	}
	if _, err := io.WriteString(s.ptm, text); err != nil {
		return fmt.Errorf("repl: send-text %s: %w", name, err)
	}
	return nil
}

// SendEnter writes a carriage return to the PTY. The kernel's ICRNL
// termios setting on the slave converts CR into NL for the child, so
// the spawned program sees a real Enter keypress.
func SendEnter(_ context.Context, name string) error {
	s, ok := lookup(name)
	if !ok {
		return fmt.Errorf("repl: no session %q", name)
	}
	if _, err := s.ptm.Write([]byte{'\r'}); err != nil {
		return fmt.Errorf("repl: send-enter %s: %w", name, err)
	}
	return nil
}

// SendCommand types text, settles for PromptSettle, then presses
// Enter. The canonical "send a slash command" helper.
func SendCommand(ctx context.Context, name, text string) error {
	if err := SendText(ctx, name, text); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(PromptSettle):
	}
	return SendEnter(ctx, name)
}

// WaitForReady polls CapturePane until the ❯ glyph appears (claude
// REPL is up and accepting input) or ctx cancels.
func WaitForReady(ctx context.Context, name string) error {
	ticker := time.NewTicker(ReadyPollInterval)
	defer ticker.Stop()
	for {
		snap, err := CapturePane(ctx, name)
		if err == nil && strings.Contains(snap, ReadyGlyph) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
