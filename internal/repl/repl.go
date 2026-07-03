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
	"os"
	"regexp"
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

// pumpReadBufSize is the per-read scratch buffer for the PTY pump.
// 4 KiB matches the default pipe buffer in Linux's kernel and keeps
// the read syscall count low without holding more than one page in
// flight.
const pumpReadBufSize = 4096

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

// ScrubClaudeCodeEnv returns env with the parent Claude Code session's
// nesting markers removed, so a spawned claude persists its own
// session transcript (the source ape scans for telemetry):
//
//   - CLAUDECODE — the top-level "running inside Claude Code" flag;
//   - CLAUDE_CODE_* — the whole parent-injected family
//     (CLAUDE_CODE_ENTRYPOINT, CLAUDE_CODE_SESSION_ID,
//     CLAUDE_CODE_CHILD_SESSION, CLAUDE_CODE_SSE_PORT, …). The
//     persistence-suppressing marker is in this set; stripping the
//     family is robust across claude versions;
//   - CLAUDE_EFFORT — so the child's effort comes from ape's flags,
//     not the parent session's inherited effort.
//
// Everything else — ANTHROPIC_* auth included — passes through
// untouched. Exported so non-PTY interactive spawns (`ape chat`) can
// apply the same scrub.
func ScrubClaudeCodeEnv(env []string) []string {
	const (
		envClaudeCode       = "CLAUDECODE"
		envClaudeEffort     = "CLAUDE_EFFORT"
		envClaudeCodePrefix = "CLAUDE_CODE_"
	)
	out := make([]string, 0, len(env))
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		if k == envClaudeCode || k == envClaudeEffort || strings.HasPrefix(k, envClaudeCodePrefix) {
			continue
		}
		out = append(out, e)
	}
	return out
}

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
	// Scrub the parent Claude Code session's nesting markers so the
	// spawned claude registers as its own TOP-LEVEL session. When ape
	// itself runs inside a Claude Code session (ubiquitous in dev),
	// the inherited CLAUDECODE / CLAUDE_CODE_* markers make the child
	// claude treat itself as a nested/child session and suppress
	// session-transcript persistence — ~/.claude/projects/<cwd>/<sid>.jsonl
	// is never written, zeroing every transcript-derived telemetry
	// value (the v0.0.28–32 saga's true root cause; verified: strip →
	// transcript persists, keep → zero).
	cmd.Env = ScrubClaudeCodeEnv(os.Environ())

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
	buf := make([]byte, pumpReadBufSize)
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

// SessionDone returns a channel that is closed when the session's child
// process exits. Returns a nil channel if the session is not registered;
// callers should treat nil as "never closes".
func SessionDone(_ context.Context, name string) <-chan struct{} {
	s, ok := lookup(name)
	if !ok {
		return nil
	}
	return s.done
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

// Test seams for WaitForReady's poll/dismiss loop so it can be driven
// against a scripted pane without a live PTY. Production code never
// reassigns these.
var (
	capturePaneFn = CapturePane
	sendEnterFn   = SendEnter
)

// modalSpec describes a blocking modal claude may render before the
// REPL accepts input (folder-trust prompt, onboarding screens, …).
// match tests a pane snapshot for the modal's signature; accept sends
// the keystrokes that dismiss it.
type modalSpec struct {
	name   string
	match  func(s string) bool
	accept func(ctx context.Context, name string) error
}

// blockingModals is the registry of known pre-REPL modals. Keep every
// modal signature in this one table so a new onboarding screen (theme
// picker, "what's new") is a one-line addition, not a re-debug.
//
// trust-folder: claude-code renders a folder-trust modal on first
// launch in an untrusted directory. Its menu item prints the ❯ glyph,
// which the pre-hardening WaitForReady treated as "ready" — the step
// prompt was then eaten by the modal and the run idled until timeout.
// --dangerously-skip-permissions does not suppress it interactively.
var blockingModals = []modalSpec{
	{
		name: "trust-folder",
		match: func(s string) bool {
			return strings.Contains(s, "trust this folder") ||
				strings.Contains(s, "Is this a project you")
		},
		// Dismiss with a BARE Enter — option 1 ("Yes, I trust this
		// folder") is preselected and the dialog shows "Enter to
		// confirm", so Enter alone accepts it. Do NOT type a "1"
		// selection keystroke: in the interactive runtime that "1"
		// surfaces as a UserPromptSubmit the step-contract verifier
		// could consume as the skill prompt (got "1" → spurious
		// stage failure in any untrusted dir). Bare Enter carries no
		// prompt text, eliminating that leak at the source. (The
		// verifier also skips non-slash UPS events as defense in
		// depth — see orchestrator.ContractVerifier.Consume.)
		accept: func(ctx context.Context, name string) error {
			return sendEnterFn(ctx, name)
		},
	},
}

// dismissBlockingModals dismisses at most one known modal per call and
// reports whether it acted. Callers should re-poll the pane after a
// dismissal before testing readiness.
func dismissBlockingModals(ctx context.Context, name, snap string) (bool, error) {
	for _, m := range blockingModals {
		if !m.match(snap) {
			continue
		}
		if err := m.accept(ctx, name); err != nil {
			return false, fmt.Errorf("repl: dismiss %s modal: %w", m.name, err)
		}
		return true, nil
	}
	return false, nil
}

// emptyPromptRe matches a prompt line with nothing typed: the ❯ glyph
// alone on its line (snapshot() strips trailing spaces). A modal menu
// item renders as `❯ 1. …` and can never match.
var emptyPromptRe = regexp.MustCompile(`(?m)^\s*` + ReadyGlyph + `\s*$`)

// replReady reports whether the pane shows the live REPL input
// affordance rather than a menu item that merely contains the glyph.
// Primary signal: the bypass-permissions footer — ape always launches
// claude with --dangerously-skip-permissions, so the footer is present
// whenever the real REPL is up. Fallback: an empty prompt line, which
// also keeps the bash-based tests (PS1='❯ ') honest.
func replReady(snap string) bool {
	if strings.Contains(snap, "bypass permissions on") {
		return true
	}
	return emptyPromptRe.MatchString(snap)
}

// NotReadyError is returned by WaitForReady when the REPL did not
// become ready before ctx expired. Pane carries the last captured
// snapshot so an unrecognized blocking modal is diagnosable from the
// error text instead of a silent stall (no-silent-caps principle).
type NotReadyError struct {
	Name string
	Pane string
	Err  error
}

func (e *NotReadyError) Error() string {
	return fmt.Sprintf("repl %q not ready before timeout: %v; last pane:\n%s", e.Name, e.Err, e.Pane)
}

func (e *NotReadyError) Unwrap() error { return e.Err }

// WaitForReady polls CapturePane until the claude REPL is genuinely
// accepting input or ctx cancels. Known blocking modals (see
// blockingModals) are dismissed along the way; after a dismissal the
// loop re-polls before testing readiness. On timeout the returned
// *NotReadyError includes the last pane snapshot.
func WaitForReady(ctx context.Context, name string) error {
	ticker := time.NewTicker(ReadyPollInterval)
	defer ticker.Stop()
	var lastSnap string
	for {
		snap, err := capturePaneFn(ctx, name)
		if err == nil {
			lastSnap = snap
			handled, derr := dismissBlockingModals(ctx, name, snap)
			switch {
			case derr != nil:
				return derr
			case handled:
				// Modal dismissed — poll again before testing readiness.
			case replReady(snap):
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return &NotReadyError{Name: name, Pane: lastSnap, Err: ctx.Err()}
		case <-ticker.C:
		}
	}
}
