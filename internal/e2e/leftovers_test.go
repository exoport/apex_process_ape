//go:build linux

package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/exoport/apex_process_ape/internal/proctree"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// fakeClaudeServer is fakeClaude with a session that starts a server and
// leaves it running, as the eval's apex-plan, apex-review and apex-apply
// sessions left taskforge servers (O-1): before its Stop it backgrounds
// `setsid sleep 600` from a subshell — re-parented to init the moment the
// subshell exits, so no descendant of claude when ape stops the session —
// and writes the sleep's pid to $FAKE_SERVER_PID.
const fakeClaudeServer = `#!/bin/bash
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
esc=$(printf '%s' "$line" | sed 's/\\/\\\\/g; s/"/\\"/g')
printf '{"session_id":"fake-1","hook_event_name":"UserPromptSubmit","cwd":"%s","prompt":"%s"}' "$PWD" "$esc" | sh -c "$ups"
( setsid sleep 600 > /dev/null 2>&1 < /dev/null & echo $! > "$FAKE_SERVER_PID" )
printf '{"session_id":"fake-1","hook_event_name":"Stop","cwd":"%s","background_tasks":[]}' "$PWD" | sh -c "$stop"
printf '\n❯ '
while IFS= read -r more; do :; done
`

// leftoverProject is a git project with one skill and an _apex/config.yaml
// (ape prompt needs one), and a fake claude dir; the env runs ape with
// that claude first on PATH and no session of its own.
func leftoverProject(t *testing.T) (project string, env []string, pidFile string) {
	t.Helper()
	for _, bin := range []string{"bash", "git", "setsid"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	project = t.TempDir()
	gitEnv := append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	git := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir, c.Env = project, gitEnv
		out, err := c.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".claude", "skills", "apex-foo"), 0o755))
	require.NoError(t, writeFile(filepath.Join(project, ".claude", "skills", "apex-foo", "SKILL.md"), "# foo\n"))
	require.NoError(t, os.MkdirAll(filepath.Join(project, "_apex"), 0o755))
	require.NoError(t, writeFile(filepath.Join(project, "_apex", "config.yaml"), "project_name: leftovers\n"))
	require.NoError(t, writeFile(filepath.Join(project, ".gitignore"), "_output/\n"))
	git("init", "-q")
	git("add", "-A")
	git("commit", "-q", "-m", "base")

	fake := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fake, "claude"), []byte(fakeClaudeServer), 0o755))
	pidFile = filepath.Join(t.TempDir(), "server.pid")
	env = append(slices.Clone(gitEnv), "PATH="+fake+":"+os.Getenv("PATH"), "APE_CLAUDE_PROBE=off", "FAKE_SERVER_PID="+pidFile,
		"APE_SESSION=", "APE_DEPTH=", "CLAUDECODE=")
	// Whatever the test does, no sleep it started outlives it.
	t.Cleanup(func() {
		if pid := serverPID(pidFile); pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return project, env, pidFile
}

func serverPID(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return pid
}

// alive: the pid runs, and is not a zombie waiting for init to reap it.
func alive(pid int) bool {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	_, rest, _ := strings.Cut(string(stat), ") ")
	return !strings.HasPrefix(rest, "Z")
}

func TestTask_KillsAServerTheSessionLeftRunning(t *testing.T) {
	ape := buildApe(t)
	for name, tc := range map[string]struct {
		flags  []string
		action string
	}{
		"default kill":       {nil, proctree.LeftoverKilled},
		"--leftovers report": {[]string{"--leftovers", "report"}, proctree.LeftoverKept},
	} {
		t.Run(name, func(t *testing.T) {
			project, env, pidFile := leftoverProject(t)
			args := append([]string{"task", "apex-foo", "--output-format", "json", "--idle-timeout", "30s", "--cwd", project}, tc.flags...)
			cmd := exec.Command(ape, args...)
			cmd.Env = env
			var stdout, stderr strings.Builder
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			require.NoError(t, cmd.Run(), "stderr:\n%s", tail(stderr.String(), 30))

			pid := serverPID(pidFile)
			require.Positive(t, pid, "the fake session never started its server — the fixture tests nothing")
			require.Equal(t, tc.action == proctree.LeftoverKept, alive(pid), "server pid %d after ape task", pid)
			require.Contains(t, stderr.String(), "the session left 1 process running after it ended")

			data, err := os.ReadFile(filepath.Join(runlog.TasksRoot(project), "apex-foo", "latest", "manifest.yaml"))
			require.NoError(t, err)
			var m pipeline.Manifest
			require.NoError(t, yaml.Unmarshal(data, &m))
			require.Len(t, m.SessionLeftovers, 1, "manifest:\n%s", data)
			l := m.SessionLeftovers[0]
			require.Equal(t, pid, l.PID)
			require.Equal(t, tc.action, l.Action)
			require.Equal(t, "sleep 600", l.Command)
			require.WithinDuration(t, time.Now(), l.Started, 5*time.Minute)
		})
	}
}

func TestPrompt_KillsAServerTheSessionLeftRunning(t *testing.T) {
	ape := buildApe(t)
	project, env, pidFile := leftoverProject(t)
	cmd := exec.Command(ape, "prompt", "start the server", "--output-format", "json", "--idle-timeout", "30s", "--cwd", project)
	cmd.Env = env
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	require.NoError(t, cmd.Run(), "stderr:\n%s", tail(stderr.String(), 30))

	pid := serverPID(pidFile)
	require.Positive(t, pid, "the fake session never started its server — the fixture tests nothing")
	require.False(t, alive(pid), "server pid %d after ape prompt", pid)

	//nolint:tagliatelle // ape's snake_case --json contract
	var res struct {
		PromptID string `json:"prompt_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout.String()), &res), stdout.String())
	data, err := os.ReadFile(filepath.Join(runlog.PromptRunDir(project, res.PromptID), "prompt.yaml"))
	require.NoError(t, err)
	var meta runlog.PromptMeta
	require.NoError(t, yaml.Unmarshal(data, &meta))
	require.Len(t, meta.SessionLeftovers, 1, "prompt.yaml:\n%s", data)
	require.Equal(t, pid, meta.SessionLeftovers[0].PID)
	require.Equal(t, proctree.LeftoverKilled, meta.SessionLeftovers[0].Action)
}
