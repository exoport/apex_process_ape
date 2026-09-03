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

	// outMu guards lastOutput, the timestamp of the most recent non-empty
	// PTY read. The pump goroutine writes it; LastOutputAt reads it. A
	// lock-guarded timestamp keeps the accessor race-free without
	// touching the vt10x reader path (PLAN-19 D1 optional PTY signal).
	outMu      sync.Mutex
	lastOutput time.Time

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
//   - CLAUDE_CODE_EFFORT_LEVEL (via the CLAUDE_CODE_ prefix) — so the
//     child's reasoning effort comes from ape's resolved config
//     (NewSessionWithEnv re-injects it), not the parent session's
//     inherited level. CLAUDE_EFFORT is stripped too as a harmless legacy
//     alias.
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

// EnvClaudeEffortLevel is the environment variable the spawned claude reads
// to set its reasoning-effort level (low|medium|high|xhigh|max). ape sets it
// from the resolved --effort flag / pipeline `effort:` field (default
// DefaultEffort). Setting it via the env — rather than claude's --effort CLI
// flag — makes it propagate to sub-agents the session spawns (e.g. a batch
// skill's per-item sub-agents) and take precedence over any inherited value.
// ScrubClaudeCodeEnv strips the inherited one first (via the CLAUDE_CODE_
// prefix) so the value re-injected here is authoritative — no duplicate key.
const EnvClaudeEffortLevel = "CLAUDE_CODE_EFFORT_LEVEL"

// DefaultEffort is the reasoning effort ape applies to a spawned claude when
// nothing else sets one — no --effort flag, and (for pipelines) no
// step/stage/pipeline `effort:` field. Exported so every spawn path shares a
// single default: the pipeline/task runner, `ape prompt`, and `ape chat`.
const DefaultEffort = "xhigh"

// EffortEnv returns the CLAUDE_CODE_EFFORT_LEVEL entry for the resolved
// effort, substituting DefaultEffort when effort is empty. Append it to a
// spawned claude's scrubbed environment (NewSessionWithEnv, or ape chat's
// direct exec) so effort propagates to sub-agents and overrides any inherited
// level. Always returns exactly one entry (the default guarantees non-empty).
func EffortEnv(effort string) []string {
	if effort == "" {
		effort = DefaultEffort
	}
	return []string{EnvClaudeEffortLevel + "=" + effort}
}

// NewSession spawns argv attached to a PTY, registers it under name,
// and starts background readers that accumulate pane output for
// CapturePane / WaitForReady. argv[0] is the program; argv[1:] its
// arguments. dir becomes the child's working directory.
func NewSession(ctx context.Context, name, dir string, argv []string) error {
	return NewSessionWithEnv(ctx, name, dir, argv, nil)
}

// NewSessionWithEnv is NewSession with extra "KEY=VALUE" environment entries
// appended to the spawned claude's scrubbed environment. The interactive
// runner uses it to inject CLAUDE_CODE_EFFORT_LEVEL (see EnvClaudeEffortLevel)
// from ape's resolved effort. Because the scrub has already removed any
// inherited value, an appended entry is the sole occurrence of its key.
func NewSessionWithEnv(_ context.Context, name, dir string, argv, extraEnv []string) error {
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
	// transcript persists, keep → zero). extraEnv (e.g. the resolved
	// CLAUDE_CODE_EFFORT_LEVEL) is appended after the scrub so it survives
	// and is authoritative.
	cmd.Env = append(ScrubClaudeCodeEnv(os.Environ()), extraEnv...)

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
			s.outMu.Lock()
			s.lastOutput = time.Now()
			s.outMu.Unlock()
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

// LastOutputAt returns the timestamp of the most recent non-empty PTY
// read for the session. ok is false when the session is unknown or has
// produced no output yet. Lock-guarded and safe to call concurrently
// with the reader goroutine (PLAN-19 D1 optional PTY progress signal).
func LastOutputAt(name string) (time.Time, bool) {
	s, ok := lookup(name)
	if !ok {
		return time.Time{}, false
	}
	s.outMu.Lock()
	defer s.outMu.Unlock()
	if s.lastOutput.IsZero() {
		return time.Time{}, false
	}
	return s.lastOutput, true
}

// SessionPID returns the child process's pid, or (0, false) when the
// session is unknown or never started. Used by the termination
// diagnostic's child-liveness line (PLAN-19 D4).
func SessionPID(name string) (int, bool) {
	s, ok := lookup(name)
	if !ok || s.cmd.Process == nil {
		return 0, false
	}
	return s.cmd.Process.Pid, true
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

// SendDown presses the Down arrow: the ANSI escape a terminal sends for
// it (ESC [ B). Used to move the selection in claude's pre-REPL menus.
//
// An arrow rather than a digit deliberately — see the trust-folder modal's
// accept function for why a typed selection keystroke is not safe here.
func SendDown(ctx context.Context, name string) error {
	return SendText(ctx, name, "\x1b[B")
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
	sendDownFn    = SendDown
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
		// Matched on WORDS, not on either exact sentence it has used.
		// The prompt is claude's copy, not a contract: it has been
		// "Do you trust the files in this folder?", then "Quick safety
		// check: Is this a project you created or one you trust?", and
		// the heading moved from the folder path to "Accessing
		// workspace:". What survives every rewording is that the screen
		// talks about TRUSTING a PLACE, so that is what this asks.
		match: trustModalVisible,
		// Walk the menu to the option that grants trust, THEN confirm.
		//
		// Never a bare Enter. Until claude 2.1.247 the accept option was
		// preselected, so Enter alone worked; 2.1.248 reordered the menu
		// to put "No, exit" first and made it the default. A blind Enter
		// then presses the exit button — the session dies, the REPL never
		// becomes ready, and every stage burns its idle window for zero
		// turns. Reading the selection back before confirming is what
		// makes the order claude's business rather than ours.
		//
		// Arrow keys, never a "1"/"2" selection keystroke: in the
		// interactive runtime a typed digit surfaces as a
		// UserPromptSubmit the step-contract verifier could consume as
		// the skill prompt (got "1" → spurious stage failure in any
		// untrusted dir). An arrow carries no prompt text, so the leak
		// cannot happen at the source. (The verifier also skips
		// non-slash UPS events as defense in depth — see
		// orchestrator.ContractVerifier.Consume.)
		accept: func(ctx context.Context, name string) error {
			// A PAINTED dialog is not an dialog READY FOR INPUT. claude
			// draws the menu before its key handler is live, and a
			// keystroke sent into that gap is echoed as text instead of
			// consumed — visible as a literal ^[[B above the dialog, and
			// worse, left sitting in the input buffer to be submitted with
			// the first real prompt. Waiting for the pane to stop changing
			// is what separates the two states.
			if err := awaitPaneSettled(ctx, name); err != nil {
				return err
			}
			last := ""
			for range maxMenuMoves {
				snap, err := capturePaneFn(ctx, name)
				if err != nil {
					return err
				}
				// The dialog can go away underneath this loop — claude
				// draws it, and a slow first paint means the caller may
				// have matched on a frame that is already stale. Anything
				// typed after that lands in the REPL as text, which is how
				// a first draft of this walked eight ^[[B escapes into a
				// live prompt. Re-checking each pass makes the loop a
				// no-op the moment there is nothing left to dismiss.
				if !trustModalVisible(snap) {
					return nil
				}
				selected, ok := selectedMenuOption(snap)
				if !ok {
					return nil // no menu on screen; nothing to press
				}
				if grantsTrust(selected) {
					return sendEnterFn(ctx, name)
				}
				last = selected
				if err := sendDownFn(ctx, name); err != nil {
					return err
				}
				if err := awaitMenuMove(ctx, name, selected); err != nil {
					return err
				}
			}
			snap, _ := capturePaneFn(ctx, name)
			return fmt.Errorf(
				"repl: could not reach a trust-granting option in %d moves (last selection %q) — "+
					"the dialog's options have changed shape; pane:\n%s", maxMenuMoves, last, snap,
			)
		},
	},
}

// menuMovePoll / menuMoveTimeout bound the wait for a selection to move
// after an arrow key.
//
// Polling rather than sleeping a fixed interval, because the number that
// matters is claude's repaint latency and it is not ours to predict. A
// first draft slept 150ms and concluded the arrows were being ignored —
// they were not, the pane simply had not redrawn yet, and the run then
// typed escape sequences into a live REPL. Measured repaint on this
// machine is comfortably under a second; the timeout is the point at
// which "not moving" becomes the honest description.
const (
	menuMovePoll    = 100 * time.Millisecond
	menuMoveTimeout = 2 * time.Second
	// menuSettleQuiet is how long the pane must stop changing before the
	// dialog is treated as ready for input, and menuSettleTimeout bounds
	// that wait.
	menuSettleQuiet   = 400 * time.Millisecond
	menuSettleTimeout = 5 * time.Second
)

// maxMenuMoves bounds the walk through a selection menu. Generous
// relative to any dialog claude has shipped, and finite so a menu whose
// selection does not move can never spin.
const maxMenuMoves = 6

// selectedMenuOption returns the text of the highlighted line in a
// selection dialog — the one the ❯ cursor sits on — and whether one was
// found.
//
// The glyph doubles as the REPL's prompt, but there it is alone on its
// line (see emptyPromptRe); a menu row always carries the option text
// after it, so requiring text is what separates the two.
func selectedMenuOption(snap string) (string, bool) {
	for line := range strings.SplitSeq(snap, "\n") {
		trimmed := strings.TrimSpace(line)
		rest, found := strings.CutPrefix(trimmed, ReadyGlyph)
		if !found {
			continue
		}
		if rest = strings.TrimSpace(rest); rest != "" {
			return rest, true
		}
	}
	return "", false
}

// trustModalVisible reports whether the pane is showing the folder-trust
// dialog.
//
// Matched on WORDS, not on either exact sentence claude has used. The
// prompt is its copy, not a contract: it has been "Do you trust the files
// in this folder?", then "Quick safety check: Is this a project you
// created or one you trust?", and the heading moved from the folder path
// to "Accessing workspace:". What survives every rewording is that the
// screen talks about TRUSTING a PLACE, so that is what this asks.
func trustModalVisible(s string) bool {
	l := strings.ToLower(s)
	if !strings.Contains(l, "trust") {
		return false
	}
	for _, place := range []string{"folder", "workspace", "directory", "project"} {
		if strings.Contains(l, place) {
			return true
		}
	}
	return false
}

// awaitPaneSettled waits until two consecutive captures agree, i.e. the
// screen has stopped being redrawn. Best effort: on timeout it returns
// nil and lets the caller proceed, because a pane that never settles is
// still worth trying rather than failing outright.
func awaitPaneSettled(ctx context.Context, name string) error {
	deadline := time.Now().Add(menuSettleTimeout)
	prev, _ := capturePaneFn(ctx, name)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(menuSettleQuiet):
		}
		snap, err := capturePaneFn(ctx, name)
		if err == nil && snap == prev {
			return nil
		}
		prev = snap
		if time.Now().After(deadline) {
			return nil
		}
	}
}

// awaitMenuMove waits for the highlighted option to stop being `from`,
// or for menuMoveTimeout. Returning without a change is not an error here
// — the caller compares selections across passes and reports the stall
// with the pane, which is more useful than a bare timeout.
func awaitMenuMove(ctx context.Context, name, from string) error {
	deadline := time.Now().Add(menuMoveTimeout)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(menuMovePoll):
		}
		if snap, err := capturePaneFn(ctx, name); err == nil {
			if sel, ok := selectedMenuOption(snap); ok && sel != from {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return nil
		}
	}
}

// grantsTrust reports whether a menu option is the one that accepts the
// folder.
//
// Word-based rather than an exact phrase, because the wording is not ours
// and has already moved once. It asks two questions: does the option talk
// about trusting, and is it phrased as an acceptance rather than a
// refusal. The second half is what matters — a menu offering "Yes, I
// trust this folder" against "No, exit" is easy, but one offering
// "Don't trust this folder" would satisfy a naive substring match and
// select exactly the wrong row.
func grantsTrust(option string) bool {
	l := strings.ToLower(option)
	if !strings.Contains(l, "trust") {
		return false
	}
	for _, decline := range []string{"no,", "no ", "don't", "do not", "never", "exit", "cancel", "quit"} {
		if strings.Contains(l, decline) {
			return false
		}
	}
	return true
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
