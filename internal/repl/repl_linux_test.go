//go:build linux

package repl

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestKillSession_ReapsGrandchildren is the FE regression guard: a
// session whose child has backgrounded a long-running grandchild
// (sleep 60) must have that grandchild cleaned up when KillSession
// is called. Today's claude REPL doesn't actually spawn long-lived
// grandchildren in practice — this test exists so a future change
// can't silently lose the cleanup behaviour.
//
// Linux-only because the test relies on:
//
//   - procGroupKillGrace (defined in proc_unix.go: linux || darwin)
//   - pgrep -f matching against /proc/PID/cmdline contents (Linux
//     semantic; macOS's pgrep matches a different cmdline view)
//   - Setsid + negative-pid SIGTERM-then-SIGKILL working uniformly
//
// The production reaper code (terminateGroup in proc_unix.go) compiles
// and runs on both Linux and macOS; this test just verifies the
// observable behaviour against pgrep's Linux semantics. If you need
// macOS coverage, a darwin-tagged sibling test with sysctl-based
// process inspection would be the right shape.
func TestKillSession_ReapsGrandchildren(t *testing.T) {
	requireBash(t)
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep not on PATH; skipping")
	}

	name := "ape-repl-test-grandchild"
	_ = KillSession(t.Context(), name)

	// Unique marker that pgrep -f finds against the bash session's
	// command line (which includes the script text).
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
