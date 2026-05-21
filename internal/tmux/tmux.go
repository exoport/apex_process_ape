// Package tmux is a thin shim over the `tmux` CLI used by ape's
// interactive runners (pipeline + chat). Interactive mode spawns
// claude inside a tmux session so prompts can be delivered as real
// REPL keystrokes (`send-keys -l <text>` + Enter) — claude then
// parses slash commands like `/apex-create-prd --autonomous` and
// `/clear` exactly as if a human had typed them.
//
// We shell out to `tmux` rather than vendoring a Go library: the
// surface we need is small (5 subcommands) and tmux is already
// expected to be present on every machine running ape interactively.
package tmux

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// PromptSettle is the wait between typing a command and pressing
// Enter. Long prompts otherwise submit before the REPL has finished
// loading them — confirmed by the community /pmux pattern and
// anthropics/claude-code#40168. 300ms is the well-known safe value.
const PromptSettle = 300 * time.Millisecond

// ReadyPollInterval is how often we capture-pane while waiting
// for the ❯ glyph that signals claude REPL is accepting input.
const ReadyPollInterval = 250 * time.Millisecond

// ReadyGlyph is the prompt glyph claude renders when ready.
const ReadyGlyph = "❯"

// NewSession creates a detached session running argv in dir. The
// session is given a fixed window size so capture-pane returns a
// deterministic shape regardless of the host terminal.
func NewSession(ctx context.Context, name, dir string, argv []string) error {
	if len(argv) == 0 {
		return errors.New("tmux: empty argv")
	}
	base := []string{
		"new-session", "-d",
		"-s", name,
		"-x", "200", "-y", "50",
		"-c", dir,
		"--",
	}
	out, err := exec.CommandContext(ctx, "tmux", append(base, argv...)...).CombinedOutput() //nolint:gosec // argv built from validated spec + caller-supplied prepend flags
	if err != nil {
		return fmt.Errorf("tmux new-session %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// KillSession stops the session. Not-found is treated as success.
func KillSession(ctx context.Context, name string) error {
	out, err := exec.CommandContext(ctx, "tmux", "kill-session", "-t", name).CombinedOutput()
	if err != nil {
		msg := string(out)
		// "can't find session" — server is up but the named session
		// is gone. "no server running" — killing the last session
		// caused the server to shut down before our call. Both mean
		// the post-condition "session does not exist" is satisfied.
		if strings.Contains(msg, "can't find session") || strings.Contains(msg, "no server running") {
			return nil
		}
		return fmt.Errorf("tmux kill-session %s: %w: %s", name, err, strings.TrimSpace(msg))
	}
	return nil
}

// HasSession reports whether the session exists.
func HasSession(ctx context.Context, name string) bool {
	err := exec.CommandContext(ctx, "tmux", "has-session", "-t", name).Run()
	return err == nil
}

// CapturePane returns the pane contents including scrollback. The
// caller passes the result through a diff to lift just one step's
// output, or polls it for marker glyphs.
func CapturePane(ctx context.Context, name string) (string, error) {
	out, err := exec.CommandContext(ctx, "tmux", "capture-pane", "-p", "-S", "-", "-t", name).Output()
	if err != nil {
		return "", fmt.Errorf("tmux capture-pane %s: %w", name, err)
	}
	return string(out), nil
}

// SendText types text into the pane WITHOUT submitting it. The -l
// flag treats every byte as literal input so leading slashes,
// quotes, and special chars survive verbatim.
func SendText(ctx context.Context, name, text string) error {
	out, err := exec.CommandContext(ctx, "tmux", "send-keys", "-t", name, "-l", text).CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux send-keys -l %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// SendEnter presses Enter in the pane (C-m). Pair with PromptSettle
// after SendText to avoid the long-prompt-submit-before-fully-loaded
// race.
func SendEnter(ctx context.Context, name string) error {
	out, err := exec.CommandContext(ctx, "tmux", "send-keys", "-t", name, "C-m").CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux send-keys C-m %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// SendCommand types text, settles, then presses Enter. The canonical
// "send a slash command" helper.
func SendCommand(ctx context.Context, name, text string) error {
	if err := SendText(ctx, name, text); err != nil {
		return err
	}
	time.Sleep(PromptSettle)
	return SendEnter(ctx, name)
}

// WaitForReady polls capture-pane until the ❯ glyph appears (claude
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
