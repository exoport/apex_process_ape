package repl

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/selfpath"
	"github.com/stretchr/testify/require"
)

// goosWindows names the GOOS these POSIX-PTY tests skip on.
const goosWindows = "windows"

// TestScrubClaudeCodeEnv pins the v0.0.33 nesting-marker scrub: the
// parent Claude Code session's markers are removed (they make a
// spawned claude suppress session-transcript persistence — the true
// root cause of the v0.0.28–32 zero-telemetry saga), auth and
// unrelated vars pass through.
func TestScrubClaudeCodeEnv(t *testing.T) {
	in := []string{
		"CLAUDECODE=1",
		"CLAUDE_CODE_CHILD_SESSION=abc",
		"CLAUDE_CODE_ENTRYPOINT=cli",
		"CLAUDE_CODE_SESSION_ID=x",
		"CLAUDE_CODE_SSE_PORT=123",
		"CLAUDE_EFFORT=high",
		"ANTHROPIC_API_KEY=secret",
		"HOME=/home/u",
		"PATH=/usr/bin",
		"CLAUDE=unrelated",       // not in the family — kept
		"CLAUDECODEX=unrelated",  // prefix-lookalike key — kept
		"MY_CLAUDE_CODE_THING=1", // not a prefix match — kept
	}
	out := ScrubClaudeCodeEnv(in)

	for _, e := range out {
		k, _, _ := strings.Cut(e, "=")
		if k == "CLAUDECODE" || k == "CLAUDE_EFFORT" || strings.HasPrefix(k, "CLAUDE_CODE_") {
			t.Fatalf("scrub left nesting marker %q in env: %v", e, out)
		}
	}
	joined := strings.Join(out, "\n")
	for _, kept := range []string{
		"ANTHROPIC_API_KEY=secret", "HOME=/home/u", "PATH=/usr/bin",
		"CLAUDE=unrelated", "CLAUDECODEX=unrelated", "MY_CLAUDE_CODE_THING=1",
	} {
		if !strings.Contains(joined, kept) {
			t.Fatalf("scrub dropped %q:\n%s", kept, joined)
		}
	}
}

// TestNewSessionScrubsNestedClaudeEnv proves the leak cannot reach the
// child regardless of how ape was launched: with CLAUDECODE +
// CLAUDE_CODE_* set on the test process (simulating ape running inside
// a Claude Code session), the spawned session's command env must
// contain none of the stripped keys. CI never runs under CLAUDECODE —
// which is exactly why three green suites shipped alongside failing
// live runs; this guard reproduces the nested context explicitly.
func TestNewSessionScrubsNestedClaudeEnv(t *testing.T) {
	if runtime.GOOS == goosWindows {
		t.Skip("POSIX PTY test; skipping on Windows")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_CODE_CHILD_SESSION", "parent-session")
	t.Setenv("CLAUDE_EFFORT", "high")
	t.Setenv("ANTHROPIC_API_KEY", "keep-me")

	name := "ape-repl-test-envscrub"
	_ = KillSession(t.Context(), name)
	if err := NewSession(t.Context(), name, "/tmp", []string{"bash", "--noprofile", "--norc", "-c", "sleep 2"}); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = KillSession(t.Context(), name) })

	s, ok := lookup(name)
	if !ok {
		t.Fatalf("session not registered")
	}
	if s.cmd.Env == nil {
		t.Fatalf("cmd.Env is nil — child inherits the full parent env, nesting markers included")
	}
	for _, e := range s.cmd.Env {
		k, _, _ := strings.Cut(e, "=")
		if k == "CLAUDECODE" || k == "CLAUDE_EFFORT" || strings.HasPrefix(k, "CLAUDE_CODE_") {
			t.Fatalf("child env contains %q — nested-session markers leaked", e)
		}
	}
	if !strings.Contains(strings.Join(s.cmd.Env, "\n"), "ANTHROPIC_API_KEY=keep-me") {
		t.Fatalf("child env lost ANTHROPIC_API_KEY (auth must pass through)")
	}
}

// TestEffortEnv covers the default substitution (empty → DefaultEffort) and
// explicit pass-through, and that the entry is keyed on EnvClaudeEffortLevel.
func TestEffortEnv(t *testing.T) {
	if got := EffortEnv(""); len(got) != 1 || got[0] != EnvClaudeEffortLevel+"="+DefaultEffort {
		t.Fatalf("EffortEnv(%q) = %v, want [%s=%s]", "", got, EnvClaudeEffortLevel, DefaultEffort)
	}
	if got := EffortEnv("low"); len(got) != 1 || got[0] != EnvClaudeEffortLevel+"=low" {
		t.Fatalf("EffortEnv(%q) = %v, want [%s=low]", "low", got, EnvClaudeEffortLevel)
	}
}

// TestNewSessionWithEnvInjectsEffort proves the effort injection path: an
// inherited CLAUDE_CODE_EFFORT_LEVEL is scrubbed (via the CLAUDE_CODE_
// prefix) and the value ape passes via extraEnv is the sole surviving
// occurrence — so ape's resolved effort is authoritative and not shadowed
// by whatever the parent Claude Code session had set.
func TestNewSessionWithEnvInjectsEffort(t *testing.T) {
	if runtime.GOOS == goosWindows {
		t.Skip("POSIX PTY test; skipping on Windows")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	t.Setenv(EnvClaudeEffortLevel, "inherited-medium") // parent session's level

	name := "ape-repl-test-effort"
	_ = KillSession(t.Context(), name)
	if err := NewSessionWithEnv(
		t.Context(), name, "/tmp",
		[]string{"bash", "--noprofile", "--norc", "-c", "sleep 2"},
		[]string{EnvClaudeEffortLevel + "=xhigh"},
	); err != nil {
		t.Fatalf("NewSessionWithEnv: %v", err)
	}
	t.Cleanup(func() { _ = KillSession(t.Context(), name) })

	s, ok := lookup(name)
	if !ok {
		t.Fatalf("session not registered")
	}
	var got []string
	for _, e := range s.cmd.Env {
		if k, _, _ := strings.Cut(e, "="); k == EnvClaudeEffortLevel {
			got = append(got, e)
		}
	}
	if len(got) != 1 {
		t.Fatalf("want exactly one %s entry, got %v", EnvClaudeEffortLevel, got)
	}
	if got[0] != EnvClaudeEffortLevel+"=xhigh" {
		t.Fatalf("effort entry = %q, want %s=xhigh (inherited value must be scrubbed)", got[0], EnvClaudeEffortLevel)
	}
}

// --- tmux terminal-identity scrub (PTY spawn path only) ----------------

// TestScrubTmuxEnv_RemovesTerminalIdentity is the fix for the leak: a
// child on ape's PTY inherited a pane address belonging to ape, Claude
// Code recorded it in its session registry without checking it was its
// own controlling terminal, and driving that address put literal text in
// the operator's pane while the PTY child saw nothing.
func TestScrubTmuxEnv_RemovesTerminalIdentity(t *testing.T) {
	got := scrubTmuxEnv([]string{
		"PATH=/usr/bin",
		"TMUX=/tmp/tmux-1000/default,2345,0",
		"HOME=/home/x",
		"TMUX_PANE=%0",
		"ANTHROPIC_API_KEY=sk-test",
	})
	want := []string{"PATH=/usr/bin", "HOME=/home/x", "ANTHROPIC_API_KEY=sk-test"}
	if len(got) != len(want) {
		t.Fatalf("scrubTmuxEnv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestScrubTmuxEnv_LeavesEverythingElseAlone — including keys that merely
// start with the same letters. The filter matches whole names, so a
// variable like TMUXP_SESSION or TMUX_TMPDIR belonging to someone else's
// tooling is not collateral.
func TestScrubTmuxEnv_LeavesEverythingElseAlone(t *testing.T) {
	in := []string{
		"TMUXP_SESSION=work",
		"TMUX_TMPDIR=/tmp",
		"TERM=tmux-256color",
		"NOTTMUX=1",
	}
	got := scrubTmuxEnv(in)
	if len(got) != len(in) {
		t.Fatalf("scrubTmuxEnv dropped entries: %v, want %v", got, in)
	}
	for i := range in {
		if got[i] != in[i] {
			t.Errorf("entry %d = %q, want %q", i, got[i], in[i])
		}
	}
}

// TestScrubTmuxEnv_NoTmuxIsAByteIdenticalPassThrough is the Windows /
// ConPTY case: neither variable is normally set there, so the filter must
// be a plain no-op rather than anything conditional on GOOS.
func TestScrubTmuxEnv_NoTmuxIsAByteIdenticalPassThrough(t *testing.T) {
	in := []string{"PATH=C:\\Windows", "USERPROFILE=C:\\Users\\x", "TERM=xterm"}
	got := scrubTmuxEnv(in)
	if len(got) != len(in) {
		t.Fatalf("scrubTmuxEnv = %v, want %v", got, in)
	}
	for i := range in {
		if got[i] != in[i] {
			t.Errorf("entry %d = %q, want %q", i, got[i], in[i])
		}
	}
}

// TestScrubClaudeCodeEnv_KeepsTmux locks the asymmetry the fix depends
// on. `ape chat` calls the SHARED scrubber and direct-execs claude onto
// the user's real terminal, where the inherited pane address is correct.
// If someone later folds the tmux strip into ScrubClaudeCodeEnv to
// "tidy" the two paths, this fails and says why.
func TestScrubClaudeCodeEnv_KeepsTmux(t *testing.T) {
	got := ScrubClaudeCodeEnv([]string{
		"TMUX=/tmp/tmux-1000/default,2345,0",
		"TMUX_PANE=%0",
		"CLAUDECODE=1",
	})
	var sawTmux, sawPane bool
	for _, e := range got {
		switch {
		case strings.HasPrefix(e, "TMUX="):
			sawTmux = true
		case strings.HasPrefix(e, "TMUX_PANE="):
			sawPane = true
		}
	}
	if !sawTmux || !sawPane {
		t.Errorf("ScrubClaudeCodeEnv must NOT strip tmux vars — `ape chat` "+
			"hands claude the user's real terminal, where the inherited pane "+
			"address is correct. got %v", got)
	}
}

// TestNewSessionScrubsTmuxEnv is the end-to-end counterpart: with TMUX
// and TMUX_PANE set on the test process (simulating ape launched from
// inside tmux, which is how the leak was found), a real spawned session's
// command env must carry neither. Asserted on the actual spawn rather
// than on scrubTmuxEnv alone, because the defect was never in the filter
// — it was that no filter was wired into this path at all.
func TestNewSessionScrubsTmuxEnv(t *testing.T) {
	if runtime.GOOS == goosWindows {
		t.Skip("POSIX PTY test; skipping on Windows")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	t.Setenv("TMUX", "/tmp/tmux-1000/default,2345,0")
	t.Setenv("TMUX_PANE", "%0")
	t.Setenv("ANTHROPIC_API_KEY", "keep-me")

	name := "ape-repl-test-tmuxscrub"
	_ = KillSession(t.Context(), name)
	if err := NewSession(t.Context(), name, "/tmp",
		[]string{"bash", "--noprofile", "--norc", "-c", "sleep 2"}); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = KillSession(t.Context(), name) })

	s, ok := lookup(name)
	if !ok {
		t.Fatalf("session not registered")
	}
	for _, e := range s.cmd.Env {
		k, _, _ := strings.Cut(e, "=")
		if k == "TMUX" || k == "TMUX_PANE" {
			t.Fatalf("child env contains %q — the child would record ape's pane "+
				"as its own terminal, and driving that address puts literal text "+
				"in the operator's pane while this child sees nothing", e)
		}
	}
	if !strings.Contains(strings.Join(s.cmd.Env, "\n"), "ANTHROPIC_API_KEY=keep-me") {
		t.Fatalf("child env lost ANTHROPIC_API_KEY (auth must pass through)")
	}
}

// TestSpawnEnv_PinsThisBinaryAsApe covers the composition at the spawn
// site: the scrubbers run, and then `ape` is pinned to this binary.
//
// The defect it guards, observed live rather than reasoned about: ape
// 0.0.67 spawned a session and `ape version` inside it reported 0.0.56,
// because 69 framework skill files run `ape …` lines that resolve through
// the operator's PATH. The framework declares a version FLOOR, and a
// skill running a pre-floor binary inside a dispatch by the post-floor
// one makes the floor unenforceable from the inside.
//
// Asserted on the composition rather than on a live session, because a
// live one costs a real Claude Code run; the end-to-end observation is
// recorded in the commit message and was re-made by hand after the fix.
func TestSpawnEnv_PinsThisBinaryAsApe(t *testing.T) {
	base := scrubTmuxEnv(ScrubClaudeCodeEnv([]string{
		"PATH=/stale/bin:/usr/bin",
		"TMUX=/tmp/tmux-1000/default,123,0",
		"CLAUDECODE=1",
		"HOME=/h",
	}))
	env, unpin, notice := selfpath.Pin(base)
	defer unpin()
	require.Empty(t, notice)

	var pathValue string
	for _, kv := range env {
		if after, ok := strings.CutPrefix(kv, "PATH="); ok {
			pathValue = after
		}
	}
	require.NotEmpty(t, pathValue)

	first, rest, _ := strings.Cut(pathValue, string(os.PathListSeparator))
	require.Equal(t, "/stale/bin:/usr/bin", rest, "the operator's PATH is preserved behind the pin")

	pinned := filepath.Join(first, selfpath.FileName())
	require.FileExists(t, pinned, "`ape` resolves to the pin before /stale/bin")

	// The scrubbers still did their jobs — the pin composes with them
	// rather than replacing the env.
	joined := strings.Join(env, "\n")
	require.NotContains(t, joined, "TMUX=")
	require.NotContains(t, joined, "CLAUDECODE=")
	require.Contains(t, joined, "HOME=/h")

	unpin()
	require.NoFileExists(t, pinned, "the shadow is removed when the session is reaped")
}
