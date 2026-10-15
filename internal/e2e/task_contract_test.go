//go:build !windows

package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeClaude is a stand-in for claude that is just enough of one for a
// whole `ape task` run: it answers --version, draws both ready signals,
// reads the typed line, and reports it through the hooks ape injected
// with --settings — UserPromptSubmit carrying the line, then a clean Stop.
// The typed line is also written to $FAKE_CLAUDE_LINE for the test.
const fakeClaude = `#!/bin/bash
if [ "$1" = "--version" ]; then echo "2.1.300 (Claude Code)"; exit 0; fi
settings=""
while [ $# -gt 0 ]; do
  if [ "$1" = "--settings" ]; then settings="$2"; shift; fi
  shift
done
hook() { printf '%s' "$settings" | grep -o "env APE_BRIDGE_PORT=[0-9]* [^\"]* notify --event $1" | head -1; }
ups=$(hook UserPromptSubmit)
stop=$(hook Stop)
printf '\n  bypass permissions on (shift+tab to cycle)\n❯ '
IFS= read -r line
line=${line%$'\r'}
printf '%s' "$line" > "$FAKE_CLAUDE_LINE"
esc=$(printf '%s' "$line" | sed 's/\\/\\\\/g; s/"/\\"/g')
printf '{"session_id":"fake-1","hook_event_name":"UserPromptSubmit","cwd":"%s","prompt":"%s"}' "$PWD" "$esc" | sh -c "$ups"
printf '{"session_id":"fake-1","hook_event_name":"Stop","cwd":"%s","background_tasks":[]}' "$PWD" | sh -c "$stop"
printf '\n❯ '
while IFS= read -r more; do :; done
`

// The end-to-end form of v1 trial D4: a no-agent `ape task` types
// `/<skill> --autonomous` with no --no-commit, and its own step contract
// accepts that line, so the run completes. Before the contract learned
// D4's line, every such run died in seconds with assertion:step-contract.
func TestTask_NoAgentLinePassesItsStepContract(t *testing.T) {
	for _, bin := range []string{"bash", "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	ape := buildApe(t)
	for name, tc := range map[string]struct {
		flags []string
		line  string
	}{
		"no --no-commit":   {nil, "/apex-foo --autonomous --prompt build it"},
		"with --no-commit": {[]string{"--no-commit"}, "/apex-foo --autonomous --no-commit --prompt build it"},
	} {
		t.Run(name, func(t *testing.T) {
			project := t.TempDir()
			git := func(args ...string) {
				c := exec.Command("git", args...)
				c.Dir = project
				c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
				out, err := c.CombinedOutput()
				require.NoError(t, err, "git %v: %s", args, out)
			}
			git("init", "-q")
			git("commit", "-q", "--allow-empty", "-m", "base")
			require.NoError(t, os.MkdirAll(filepath.Join(project, ".claude", "skills", "apex-foo"), 0o755))
			require.NoError(t, writeFile(filepath.Join(project, ".claude", "skills", "apex-foo", "SKILL.md"), "# foo\n"))

			fake := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(fake, "claude"), []byte(fakeClaude), 0o755))
			lineFile := filepath.Join(t.TempDir(), "line")

			args := append([]string{
				"task", "apex-foo", "--prompt", "build it", "--prompt-flag", "--prompt",
				"--output-format", "json", "--idle-timeout", "20s", "--cwd", project,
			}, tc.flags...)
			cmd := exec.Command(ape, args...)
			cmd.Env = append(os.Environ(), "PATH="+fake+":"+os.Getenv("PATH"), "APE_CLAUDE_PROBE=off",
				"FAKE_CLAUDE_LINE="+lineFile, "APE_SESSION=")
			var stdout, stderr strings.Builder
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()

			typed, readErr := os.ReadFile(lineFile)
			require.NoError(t, readErr, "the fake claude never received a line\nstderr:\n%s", tail(stderr.String(), 30))
			require.Equal(t, tc.line, string(typed))
			require.NotContains(t, stderr.String(), "assertion:step-contract")
			require.NoError(t, err, "stderr:\n%s", tail(stderr.String(), 30))
			var env map[string]any
			require.NoError(t, json.Unmarshal([]byte(stdout.String()), &env), "stdout: %s", stdout.String())
			require.Equal(t, true, env["success"], "envelope: %s", stdout.String())
		})
	}
}
