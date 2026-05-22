// Package repl drives a child program (typically `claude`) through an
// in-process pseudo-terminal so callers can type into it and read its
// rendered output programmatically. Replaces the previous tmux shim
// used by ape's interactive runners — the API surface is intentionally
// the same (NewSession/KillSession/HasSession/CapturePane/SendText/
// SendEnter/SendCommand/WaitForReady, keyed by session name) so
// consumers compile unchanged.
//
// The PTY backend is github.com/aymanbagabas/go-pty, which transparently
// uses Unix PTYs on Linux/macOS and ConPTY on Windows — so apepty works
// natively under Git Bash on Windows 11 without WSL or a tmux binary
// on PATH.
//
// Trade-offs vs the tmux variant:
//
//   - Pane "capture" is the raw PTY output stream, not a rendered VT
//     grid. The accumulated bytes include ANSI control sequences and
//     any redraws the child emits; the diff-snapshot logic in the
//     pipeline runner finds new text by anchoring on the previous
//     snapshot's tail, which is robust to that noise.
//   - No external attach: a session lives and dies with the apepty
//     process. There is no `tmux attach -t …` equivalent for an
//     in-flight run. Pipeline runs that want live introspection should
//     tail the per-step ndjson event log instead.
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

// Pane geometry. Matches the tmux variant so capture snapshots have
// the same shape — claude's TUI wraps to this width.
const (
	paneCols = 200
	paneRows = 50
)

// scrollbackCap bounds per-session scrollback retention. 1 MiB is far
// more than any single stage of slash-command output emits, and the
// trim is from the head so the tail (where the latest output lives)
// is always present.
const scrollbackCap = 1 << 20

type session struct {
	name string
	ptm  pty.Pty
	cmd  *pty.Cmd

	mu  sync.Mutex
	buf []byte

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
		done: make(chan struct{}),
	}
	regMu.Lock()
	registry[name] = s
	regMu.Unlock()

	go s.pump()
	go s.reap()

	return nil
}

// pump drains PTY output into the scrollback buffer until the PTY
// closes (child exited, or KillSession closed the master).
func (s *session) pump() {
	buf := make([]byte, 4096)
	for {
		n, err := s.ptm.Read(buf)
		if n > 0 {
			s.append(buf[:n])
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

func (s *session) append(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf = append(s.buf, p...)
	if len(s.buf) > scrollbackCap {
		drop := len(s.buf) - scrollbackCap
		s.buf = s.buf[drop:]
	}
}

func (s *session) snapshot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.buf)
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

// CapturePane returns the accumulated PTY output as a string,
// including any ANSI control sequences. The pipeline runner's diff
// helper anchors on the previous snapshot's tail to lift just the
// new bytes, so escape noise is harmless for the manifest path.
func CapturePane(_ context.Context, name string) (string, error) {
	s, ok := lookup(name)
	if !ok {
		return "", fmt.Errorf("repl: no session %q", name)
	}
	return s.snapshot(), nil
}

// SendText writes text literally to the PTY input without submitting
// it. Counterpart of tmux's `send-keys -l`: every byte goes through
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
