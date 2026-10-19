//go:build !windows

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/stretchr/testify/require"
)

// fakeClaudeToolLoop is eval Q23's progressing session: after the typed
// line it fires UserPromptSubmit WITHOUT a transcript_path, then once a
// second appends an assistant turn to its transcript and fires
// PostToolUse naming that transcript, and never ends its turn.
const fakeClaudeToolLoop = `#!/bin/bash
if [ "$1" = "--version" ]; then echo "2.1.300 (Claude Code)"; exit 0; fi
settings=""
while [ $# -gt 0 ]; do
  if [ "$1" = "--settings" ]; then settings="$2"; shift; fi
  shift
done
hook() { printf '%s' "$settings" | grep -o "env APE_BRIDGE_PORT=[0-9]* [^\"]* notify --event $1" | head -1; }
ups=$(hook UserPromptSubmit)
post=$(hook PostToolUse)
printf '\n  bypass permissions on (shift+tab to cycle)\n❯ '
IFS= read -r line
tr="$FAKE_DIR/q23.jsonl"
: > "$tr"
printf '{"session_id":"q23","hook_event_name":"UserPromptSubmit","cwd":"%s","prompt":"x"}' "$PWD" | sh -c "$ups"
for i in $(seq 1 30); do
  printf '{"type":"assistant","sessionId":"q23","timestamp":"2026-10-19T12:00:00.000Z","message":{"id":"msg_%s","model":"claude-opus-5","stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":100,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}\n' "$i" >> "$tr"
  printf '{"session_id":"q23","transcript_path":"%s","hook_event_name":"PostToolUse","tool_name":"Bash","cwd":"%s"}' "$tr" "$PWD" | sh -c "$post"
  sleep 1
done
printf '\n❯ '
while IFS= read -r more; do :; done
`

// Eval Q23 end to end: a session progressing only through PostToolUse
// hooks, stopped by an explicit --max-duration, says the hook is its
// latest progress ("hook Ns ago", never "hook none"), and the
// transcript_path only its PostToolUse payloads carry is the step's
// transcript: the stopped step records its cost and turns.
func TestTask_PostToolUseOnlySessionHookColumn(t *testing.T) {
	for _, bin := range []string{"bash", "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	ape := buildApe(t)
	project, fake := t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fake, "claude"), []byte(fakeClaudeToolLoop), 0o755))
	env := append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "APE_SESSION=", "CLAUDECODE=", "APE_SESSION_MODEL=", "APE_SESSION_EFFORT=",
		"PATH="+fake+":"+os.Getenv("PATH"), "APE_CLAUDE_PROBE=off", "FAKE_DIR="+fake)
	sh := func(name string, args ...string) {
		c := exec.Command(name, args...)
		c.Dir, c.Env = project, env
		out, err := c.CombinedOutput()
		require.NoError(t, err, "%s %v: %s", name, args, out)
	}
	sh("git", "init", "-q")
	sh("git", "commit", "-q", "--allow-empty", "-m", "base")
	sh(ape, "project", "init")
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".claude", "skills", "apex-foo"), 0o755))
	require.NoError(t, writeFile(filepath.Join(project, ".claude", "skills", "apex-foo", "SKILL.md"), "# foo\n"))
	sh("git", "add", "-A")
	sh("git", "commit", "-q", "-m", "setup")

	_, code := runApe(t, ape, project, env, "task", "apex-foo", "--idle-timeout", "30s", "--max-duration", "4s", "--output-format", "json", "--cwd", project)
	require.Equal(t, 1, code, "a person's cap stops the run")

	m := v1TaskManifest(t, project)
	require.NotNil(t, m.Termination)
	require.Equal(t, pipeline.TerminationMaxDuration, m.Termination.Kind)
	require.Regexp(t, `\(hook \d+s ago; transcript \d+s ago;`, m.Termination.Message)
	require.NotContains(t, m.Termination.Message, "hook none")
	require.Positive(t, m.Totals.CostUSD, "the PostToolUse-only transcript_path is the step's transcript")
	require.Positive(t, m.Totals.NumTurns)
}
