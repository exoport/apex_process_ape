//go:build linux

package repl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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

// The recorder through the real thing: a child on a real PTY that never
// shows a ready REPL. The NotReadyError must carry exactly what the child
// wrote — escape sequences and all, the part a pane cannot show.
func TestWaitForReady_RecordsTheRawBytesOfARealSession(t *testing.T) {
	name := fmt.Sprintf("ape-test-recorder-%d", os.Getpid())
	t.Cleanup(func() { _ = KillSession(context.Background(), name) })
	script := `printf '\033[?u\033[c\033[2Jbooting, never ready'; sleep 30`
	require.NoError(t, NewSession(t.Context(), name, "/tmp", []string{"bash", "--noprofile", "--norc", "-c", script}))

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	err := WaitForReady(ctx, name)

	nr, ok := errors.AsType[*NotReadyError](err)
	require.True(t, ok, "%T: %v", err, err)
	require.Contains(t, nr.Pane, "booting, never ready")
	require.True(t, bytes.Contains(nr.Output, []byte("\x1b[?u\x1b[c\x1b[2Jbooting, never ready")),
		"the raw bytes, escapes included: %q", nr.Output)
}

// ProbeClaude through a real PTY, with shell stand-ins for claude: the
// verdict must follow what the screen actually shows. The trust-walk case
// is covered by TestJudgeNotReady and, against the real claude, by the live
// startup_probe subtest.
func TestProbeClaude_OnARealPTY(t *testing.T) {
	dir := t.TempDir()
	stub := func(name, body string) string {
		p := dir + "/" + name
		require.NoError(t, os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755))
		return p
	}
	t.Run("a REPL with both ready signals is verified", func(t *testing.T) {
		bin := stub("ready.sh", `printf '\n❯ \n\n  ⏵⏵ bypass permissions on (shift+tab to cycle)\n'; sleep 30`)
		res := ProbeClaude(t.Context(), bin)
		require.Equal(t, ProbeVerified, res.Verdict, "%s\n%s", res.Detail, res.Pane)
	})
	t.Run("a REPL without the footer is broken, not waved through on the ❯ fallback", func(t *testing.T) {
		bin := stub("nofooter.sh", `printf '\n❯ \n'; sleep 30`)
		res := ProbeClaude(t.Context(), bin)
		require.Equal(t, ProbeBroken, res.Verdict)
		require.Contains(t, res.Detail, "bypass permissions on")
		require.NotEmpty(t, res.Output, "the raw bytes come with a broken verdict")
	})
}
