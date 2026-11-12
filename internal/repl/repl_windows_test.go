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

	// Wait for the pump to have drained cmd.exe's output. A fixed 500 ms
	// was not enough on a loaded runner: the pane was still empty at
	// 0.54 s, cmd.exe not yet started (V8-WIN-5, CI-5GREEN-4 on 744f355),
	// so the capture is polled, bounded, and a red shows the last pane.
	var pane string
	deadline := time.Now().Add(20 * time.Second)
	for {
		var err error
		pane, err = CapturePane(t.Context(), name)
		if err != nil {
			t.Fatalf("CapturePane on Windows: %v", err)
		}
		if strings.Contains(pane, sentinel) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected %q in pane within 20 s, got:\n%s", sentinel, pane)
		}
		time.Sleep(100 * time.Millisecond)
	}

	if err := KillSession(t.Context(), name); err != nil {
		t.Fatalf("KillSession on Windows: %v", err)
	}
	if HasSession(t.Context(), name) {
		t.Fatalf("HasSession reported true after KillSession on Windows")
	}
}
