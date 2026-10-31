//go:build !windows

package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeOrchestrator is Claude Code as the orchestrator terminal meets it:
// ready at once, every typed line logged to $FAKE_TYPED and answered with
// an assistant message in the transcript of the session id it was given.
const fakeOrchestrator = `#!/bin/bash
if [ "$1" = "--version" ]; then echo "2.1.312 (Claude Code)"; exit 0; fi
sid=""
while [ $# -gt 0 ]; do [ "$1" = "--session-id" ] && sid="$2"; shift; done
dir="$HOME/.claude/projects/fake"; mkdir -p "$dir"; t="$dir/$sid.jsonl"
echo $$ > "$FAKE_PID"
printf '\n  bypass permissions on (shift+tab to cycle)\n❯ '
while IFS= read -r line; do
  printf '%s\n' "$line" >> "$FAKE_TYPED"
  ts=$(date -u +%Y-%m-%dT%H:%M:%S.000Z)
  printf '{"type":"assistant","timestamp":"%s","message":{"content":[{"type":"text","text":"ack %s"}]}}\n' "$ts" "$(wc -l < "$FAKE_TYPED" | tr -d ' ')" >> "$t"
  printf '\n● ack\n  bypass permissions on (shift+tab to cycle)\n❯ '
done
`

// v1.4.2 A8 + D660.2 end to end on a fake claude: start types
// /apex-orchestrator once ready; send is typed marked on one line and read
// returns the reply; killing the session logs orchestrator-dead, which
// `ape status --wait --until conductor` wakes on.
func TestConductorTerminal(t *testing.T) {
	for _, bin := range []string{"bash", "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	ape := buildApe(t)
	project, fake, home := t.TempDir(), t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fake, "claude"), []byte(fakeOrchestrator), 0o755))
	typed, pidFile := filepath.Join(fake, "typed.log"), filepath.Join(fake, "claude.pid")
	dumpOnFailure(t, project, fake)
	env := append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "APE_SESSION=", "CLAUDECODE=", "PATH="+fake+":"+os.Getenv("PATH"),
		"APE_CLAUDE_PROBE=off", "HOME="+home, "FAKE_TYPED="+typed, "FAKE_PID="+pidFile)
	sh := func(name string, args ...string) {
		c := exec.Command(name, args...)
		c.Dir, c.Env = project, env
		out, err := c.CombinedOutput()
		require.NoError(t, err, "%s %v: %s", name, args, out)
	}
	sh("git", "init", "-q")
	sh("git", "commit", "-q", "--allow-empty", "-m", "base")
	sh(ape, "project", "init")
	t.Cleanup(func() { _, _ = runApe(t, ape, project, env, "conductor", "stop") })

	out, code := runApe(t, ape, project, env, "conductor", "start", "--idle-alert", "1h")
	require.Equal(t, 0, code, out)
	require.Eventually(t, func() bool { b, _ := os.ReadFile(typed); return strings.Contains(string(b), "/apex-orchestrator") },
		30*time.Second, 200*time.Millisecond, "the orchestrator skill typed once ready")

	out, code = runApe(t, ape, project, env, "conductor", "send", "M2's plan is approved:\nstart it")
	require.Equal(t, 0, code, out)
	require.Eventually(t, func() bool {
		b, _ := os.ReadFile(typed)
		return strings.Contains(string(b), `<ape-delivery seq="`) && strings.Contains(string(b), "M2's plan is approved: start it </ape-delivery>")
	}, 30*time.Second, 200*time.Millisecond, "the message typed, marked, on one line")

	require.Eventually(t, func() bool {
		out, _ := runApe(t, ape, project, env, "conductor", "read", "--json")
		var rs []struct {
			N    int    `json:"n"`
			Text string `json:"text"`
		}
		return json.Unmarshal([]byte(out), &rs) == nil && len(rs) == 2 && rs[1].Text == "ack 2"
	}, 30*time.Second, 300*time.Millisecond, "read returns the orchestrator's replies")

	out, code = runApe(t, ape, project, env, "conductor", "start")
	require.Equal(t, 1, code, "one terminal per project: %s", out)

	// The session dies: orchestrator-dead, and the conductor's wait —
	// from the seq it last saw, as a conductor waits — wakes on it.
	seqOut, _ := runApe(t, ape, project, env, "status", "--json")
	var st struct {
		Seq int64 `json:"seq"`
	}
	require.NoError(t, json.Unmarshal([]byte(seqOut), &st), seqOut)
	pidData, err := os.ReadFile(pidFile)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	require.NoError(t, err)
	require.NoError(t, syscall.Kill(pid, syscall.SIGKILL))
	out, code = runApe(t, ape, project, env, "status", "--wait", "30s", "--until", "conductor", "--since", strconv.FormatInt(st.Seq, 10), "--json")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, `"orchestrator-dead"`)
	out, _ = runApe(t, ape, project, env, "conductor", "status", "--json")
	require.Contains(t, out, `"alive": false`)
}
