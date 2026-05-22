//go:build windows

package repl

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestSessionLifecycle_Windows exercises the ConPTY code path the
// POSIX repl_test.go tests skip via requireUnix. The PS1='❯ '-bash
// stand-in those tests use can't be reproduced verbatim on ConPTY
// (no ICRNL, no POSIX termios), so the Windows test drives `cmd.exe`
// with a single fire-and-exit echo command and verifies that the
// rendered PTY grid contains the echoed text.
//
// Goal: prove pty.New + Resize + Read + Write + Kill all round-trip
// on ConPTY in CI. If the production path (driving real claude on
// Windows) needs richer assertions later, add them here.
func TestSessionLifecycle_Windows(t *testing.T) {
	if _, err := exec.LookPath("cmd.exe"); err != nil {
		t.Skip("cmd.exe not on PATH; skipping")
	}
	name := "ape-repl-test-windows-lifecycle"
	_ = KillSession(t.Context(), name)

	const sentinel = "hello-from-windows-pty"
	if err := NewSession(t.Context(), name, "", []string{"cmd.exe", "/c", "echo " + sentinel}); err != nil {
		t.Fatalf("NewSession on Windows: %v", err)
	}
	t.Cleanup(func() { _ = KillSession(t.Context(), name) })

	// Give the pump time to drain cmd.exe's stdout. cmd.exe's banner
	// + the echoed line render quickly but ConPTY has some buffering;
	// 500ms is generous.
	time.Sleep(500 * time.Millisecond)

	pane, err := CapturePane(t.Context(), name)
	if err != nil {
		t.Fatalf("CapturePane on Windows: %v", err)
	}
	if !strings.Contains(pane, sentinel) {
		t.Fatalf("expected %q in pane, got:\n%s", sentinel, pane)
	}

	if err := KillSession(t.Context(), name); err != nil {
		t.Fatalf("KillSession on Windows: %v", err)
	}
	if HasSession(t.Context(), name) {
		t.Fatalf("HasSession reported true after KillSession on Windows")
	}
}
