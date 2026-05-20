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
func NewSession(name, dir string, argv []string) error {
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
	out, err := exec.Command("tmux", append(base, argv...)...).CombinedOutput() //nolint:gosec // argv built from validated spec + caller-supplied prepend flags
	if err != nil {
		return fmt.Errorf("tmux new-session %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// KillSession stops the session. Not-found is treated as success.
func KillSession(name string) error {
	out, err := exec.Command("tmux", "kill-session", "-t", name).CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "can't find session") {
			return nil
		}
		return fmt.Errorf("tmux kill-session %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// HasSession reports whether the session exists.
func HasSession(name string) bool {
	err := exec.Command("tmux", "has-session", "-t", name).Run()
	return err == nil
}

// CapturePane returns the pane contents including scrollback. The
// caller passes the result through a diff to lift just one step's
// output, or polls it for marker glyphs.
func CapturePane(name string) (string, error) {
	out, err := exec.Command("tmux", "capture-pane", "-p", "-S", "-", "-t", name).Output()
	if err != nil {
		return "", fmt.Errorf("tmux capture-pane %s: %w", name, err)
	}
	return string(out), nil
}

// SendText types text into the pane WITHOUT submitting it. The -l
// flag treats every byte as literal input so leading slashes,
// quotes, and special chars survive verbatim.
func SendText(name, text string) error {
	out, err := exec.Command("tmux", "send-keys", "-t", name, "-l", text).CombinedOutput() //nolint:gosec // name + text from controlled call sites; tmux is the trusted CLI
	if err != nil {
		return fmt.Errorf("tmux send-keys -l %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// SendEnter presses Enter in the pane (C-m). Pair with PromptSettle
// after SendText to avoid the long-prompt-submit-before-fully-loaded
// race.
func SendEnter(name string) error {
	out, err := exec.Command("tmux", "send-keys", "-t", name, "C-m").CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux send-keys C-m %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// SendCommand types text, settles, then presses Enter. The canonical
// "send a slash command" helper.
func SendCommand(name, text string) error {
	if err := SendText(name, text); err != nil {
		return err
	}
	time.Sleep(PromptSettle)
	return SendEnter(name)
}

// WaitForReady polls capture-pane until the ❯ glyph appears (claude
// REPL is up and accepting input) or ctx cancels.
func WaitForReady(ctx context.Context, name string) error {
	ticker := time.NewTicker(ReadyPollInterval)
	defer ticker.Stop()
	for {
		snap, err := CapturePane(name)
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
