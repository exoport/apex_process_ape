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
// guard. Carries over the PLAN-6 tmux-era test: drive a bash session with
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

// TestCapturePaneNoANSI verifies that CapturePane returns the
// rendered VT grid as plain text — no ANSI escape bytes, no
// CSI sequences. The session prints text with explicit color escapes
// and we assert (a) the visible text appears and (b) no ESC (0x1B)
// byte made it into the output.
func TestCapturePaneNoANSI(t *testing.T) {
	requireBash(t)
	requireUnix(t)
	name := "ape-repl-test-noansi"
	_ = KillSession(t.Context(), name)

	// `printf` with explicit color escape so we don't depend on a
	// host config that ships colored ls/grep. A trailing sleep keeps
	// bash alive long enough for the pump to drain.
	if err := NewSession(t.Context(), name, "/tmp", []string{
		"bash", "--noprofile", "--norc", "-c",
		"printf '\\033[31mhello-red\\033[0m\\n'; printf '\\033[1;34mhello-bold-blue\\033[0m\\n'; sleep 1",
	}); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = KillSession(t.Context(), name) })

	// Give the pump time to drain bash's stdout.
	time.Sleep(400 * time.Millisecond)

	pane, err := CapturePane(t.Context(), name)
	if err != nil {
		t.Fatalf("CapturePane: %v", err)
	}
	if !strings.Contains(pane, "hello-red") {
		t.Fatalf("expected 'hello-red' in pane, got:\n%s", pane)
	}
	if !strings.Contains(pane, "hello-bold-blue") {
		t.Fatalf("expected 'hello-bold-blue' in pane, got:\n%s", pane)
	}
	if strings.ContainsRune(pane, 0x1B) {
		t.Fatalf("pane contains ESC byte (0x1B) — VT emulator did not strip ANSI escapes:\n%q", pane)
	}
	// CSI parameters (digit;digit) are fine on their own; what we
	// really care about is that the leading ESC[ is gone.
	if strings.Contains(pane, "\x1b[") {
		t.Fatalf("pane contains a CSI introducer (ESC['), VT emulator failure:\n%q", pane)
	}
}

// TestKillSession_ReapsGrandchildren is the FE regression guard: a
// session whose child has backgrounded a long-running grandchild
// (sleep 60) must have that grandchild cleaned up when KillSession
// is called. Today's claude REPL doesn't actually spawn long-lived
// grandchildren in practice — this test exists so a future change
// can't silently lose the cleanup behaviour.
func TestKillSession_ReapsGrandchildren(t *testing.T) {
	requireBash(t)
	requireUnix(t)
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep not on PATH; skipping")
	}

	name := "ape-repl-test-grandchild"
	_ = KillSession(t.Context(), name)

	// Unique marker so pgrep can find our specific sleep among any
	// other sleep processes on the system. Embedded in the sleep
	// argv as a no-op env-export prefix so it appears in pgrep -f.
	marker := "APE_REPL_GRANDCHILD_MARKER_" + t.Name()
	script := "export " + marker + "=1; sleep 60 & wait $!"
	if err := NewSession(t.Context(), name, "/tmp", []string{
		"bash", "--noprofile", "--norc", "-c", script,
	}); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = KillSession(t.Context(), name) })

	// Give bash time to fork the sleep.
	time.Sleep(500 * time.Millisecond)

	pre, _ := exec.Command("pgrep", "-f", marker).Output()
	if strings.TrimSpace(string(pre)) == "" {
		t.Skipf("could not find spawned grandchild via pgrep -f %s; not exercising reaper", marker)
	}

	if err := KillSession(t.Context(), name); err != nil {
		t.Fatalf("KillSession: %v", err)
	}

	// Wait past the SIGTERM-to-SIGKILL grace so the escalator has
	// definitely fired before we look.
	time.Sleep(procGroupKillGrace + 500*time.Millisecond)

	post, _ := exec.Command("pgrep", "-f", marker).Output()
	if strings.TrimSpace(string(post)) != "" {
		// Try to kill it ourselves so a failing test doesn't leak.
		_ = exec.Command("pkill", "-9", "-f", marker).Run()
		t.Fatalf("grandchild still alive after KillSession; pgrep -f %s:\n%s", marker, post)
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
