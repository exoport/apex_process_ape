package repl

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
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
