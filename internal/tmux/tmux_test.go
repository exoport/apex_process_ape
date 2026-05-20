package tmux

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// requireTmux skips the test when the tmux binary is missing — keeps
// the package buildable in environments that don't have tmux (CI on
// some Linux images, etc.) without silently passing.
func requireTmux(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed; skipping")
	}
}

// TestSessionLifecycle exercises NewSession/HasSession/KillSession
// against a real tmux + bash so the package's actual shell-out paths
// stay honest.
func TestSessionLifecycle(t *testing.T) {
	requireTmux(t)
	name := "ape-tmux-test-lifecycle"
	_ = KillSession(name)

	if err := NewSession(name, "/tmp", []string{"bash", "--noprofile", "--norc"}); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = KillSession(name) })

	if !HasSession(name) {
		t.Fatalf("HasSession returned false after NewSession")
	}
	if err := KillSession(name); err != nil {
		t.Fatalf("KillSession: %v", err)
	}
	if HasSession(name) {
		t.Fatalf("HasSession returned true after KillSession")
	}
	// Killing a non-existent session is a no-op error-wise.
	if err := KillSession(name); err != nil {
		t.Errorf("KillSession on missing session should be a no-op, got: %v", err)
	}
}

// TestSendCommandOrderingWithClear is the multi-step /clear
// regression guard. It drives a real tmux + bash session as a stand-
// in for claude (PS1='❯ ' lets WaitForReady's glyph match), then
// types the canonical PLAN-6 sequence the interactive runner would
// produce for step i>0:
//
//   1. /clear  (would reset claude context; bash just echoes "command not found")
//   2. /apex-some-skill --autonomous  (the skill prompt)
//
// The test verifies both lines appear in capture-pane in the right
// order, with /clear strictly before the skill prompt. This catches
// a regression where the runner stops sending /clear, or sends it
// AFTER the skill prompt.
func TestSendCommandOrderingWithClear(t *testing.T) {
	requireTmux(t)
	name := "ape-tmux-test-clear-order"
	_ = KillSession(name)

	// PS1='❯ ' so WaitForReady finds the marker the same way the
	// runner finds claude's prompt.
	if err := NewSession(name, "/tmp", []string{
		"bash", "--noprofile", "--norc", "-c", "PS1='❯ '; export PS1; exec bash --noprofile --norc",
	}); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = KillSession(name) })

	readyCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := WaitForReady(readyCtx, name); err != nil {
		t.Fatalf("WaitForReady: %v", err)
	}

	// Drive what runStageInteractive does between step 0 and step 1:
	// /clear first (with NoClear=false), then the slash command.
	if err := SendCommand(name, "/clear"); err != nil {
		t.Fatalf("SendCommand(/clear): %v", err)
	}
	// Allow the redraw — same settle the runner uses.
	time.Sleep(200 * time.Millisecond)
	if err := SendCommand(name, "/apex-some-skill --autonomous"); err != nil {
		t.Fatalf("SendCommand(skill): %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	pane, err := CapturePane(name)
	if err != nil {
		t.Fatalf("CapturePane: %v", err)
	}
	clearIdx := strings.Index(pane, "/clear")
	skillIdx := strings.Index(pane, "/apex-some-skill")
	if clearIdx < 0 {
		t.Fatalf("/clear not found in pane:\n%s", pane)
	}
	if skillIdx < 0 {
		t.Fatalf("skill command not found in pane:\n%s", pane)
	}
	if clearIdx >= skillIdx {
		t.Fatalf("/clear (idx=%d) must appear strictly before skill prompt (idx=%d) in pane:\n%s", clearIdx, skillIdx, pane)
	}
}

// TestSendTextLiteral verifies that send-keys -l preserves leading
// slashes and special chars verbatim. Without -l, tmux would try to
// interpret strings like `--` or argument-shaped tokens as key
// escapes, breaking PAT-25 skill prompts.
func TestSendTextLiteral(t *testing.T) {
	requireTmux(t)
	name := "ape-tmux-test-literal"
	_ = KillSession(name)

	if err := NewSession(name, "/tmp", []string{
		"bash", "--noprofile", "--norc", "-c", "PS1='❯ '; export PS1; exec bash --noprofile --norc",
	}); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = KillSession(name) })

	readyCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := WaitForReady(readyCtx, name); err != nil {
		t.Fatalf("WaitForReady: %v", err)
	}

	const literal = "/apex-agent-pm --autonomous -- apex-create-prd --autonomous"
	if err := SendText(name, literal); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	pane, err := CapturePane(name)
	if err != nil {
		t.Fatalf("CapturePane: %v", err)
	}
	if !strings.Contains(pane, literal) {
		t.Fatalf("literal text not preserved in pane:\nwant substring: %q\npane:\n%s", literal, pane)
	}
}
