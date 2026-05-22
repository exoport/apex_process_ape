package repl

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// requireBash skips tests when bash isn't on PATH (e.g. minimal CI
// images, native-Windows CI without Git Bash).
func requireBash(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed; skipping")
	}
}

// requireUnix skips tests that depend on POSIX termios behaviour the
// Windows ConPTY layer doesn't reproduce identically (e.g. ICRNL).
func requireUnix(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX termios behavior; skipping on Windows")
	}
}

// TestSessionLifecycle exercises NewSession/HasSession/KillSession
// against a real bash so the package's actual PTY paths stay honest.
func TestSessionLifecycle(t *testing.T) {
	requireBash(t)
	requireUnix(t)
	name := "ape-repl-test-lifecycle"
	_ = KillSession(t.Context(), name)

	if err := NewSession(t.Context(), name, "/tmp", []string{"bash", "--noprofile", "--norc"}); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = KillSession(t.Context(), name) })

	if !HasSession(t.Context(), name) {
		t.Fatalf("HasSession returned false after NewSession")
	}
	if err := KillSession(t.Context(), name); err != nil {
		t.Fatalf("KillSession: %v", err)
	}
	if HasSession(t.Context(), name) {
		t.Fatalf("HasSession returned true after KillSession")
	}
	// Killing a non-existent session is a no-op error-wise.
	if err := KillSession(t.Context(), name); err != nil {
		t.Errorf("KillSession on missing session should be a no-op, got: %v", err)
	}
}

// TestSendCommandOrderingWithClear is the multi-step /clear regression
// guard. Mirrors the tmux variant's test: drive a bash session with
// PS1='❯ ' as a stand-in for claude (so WaitForReady's glyph matches),
// then send /clear before a slash-style prompt and assert both lines
// appear in capture-pane in order.
func TestSendCommandOrderingWithClear(t *testing.T) {
	requireBash(t)
	requireUnix(t)
	name := "ape-repl-test-clear-order"
	_ = KillSession(t.Context(), name)

	if err := NewSession(t.Context(), name, "/tmp", []string{
		"bash", "--noprofile", "--norc", "-c", "PS1='❯ '; export PS1; exec bash --noprofile --norc",
	}); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = KillSession(t.Context(), name) })

	readyCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := WaitForReady(readyCtx, name); err != nil {
		t.Fatalf("WaitForReady: %v", err)
	}

	if err := SendCommand(t.Context(), name, "/clear"); err != nil {
		t.Fatalf("SendCommand(/clear): %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := SendCommand(t.Context(), name, "/apex-some-skill --autonomous"); err != nil {
		t.Fatalf("SendCommand(skill): %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	pane, err := CapturePane(t.Context(), name)
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

// TestSendTextLiteral verifies that SendText preserves leading slashes
// and special chars verbatim through the PTY.
func TestSendTextLiteral(t *testing.T) {
	requireBash(t)
	requireUnix(t)
	name := "ape-repl-test-literal"
	_ = KillSession(t.Context(), name)

	if err := NewSession(t.Context(), name, "/tmp", []string{
		"bash", "--noprofile", "--norc", "-c", "PS1='❯ '; export PS1; exec bash --noprofile --norc",
	}); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = KillSession(t.Context(), name) })

	readyCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := WaitForReady(readyCtx, name); err != nil {
		t.Fatalf("WaitForReady: %v", err)
	}

	const literal = "/apex-agent-pm --autonomous -- apex-create-prd --autonomous"
	if err := SendText(t.Context(), name, literal); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	pane, err := CapturePane(t.Context(), name)
	if err != nil {
		t.Fatalf("CapturePane: %v", err)
	}
	if !strings.Contains(pane, literal) {
		t.Fatalf("literal text not preserved in pane:\nwant substring: %q\npane:\n%s", literal, pane)
	}
}
