//go:build linux

package e2e

import (
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

// fakeClaudeGate is a session that starts a detached long run — `ape
// milestone gate M1 --wait 0`, whose worker runs the gate script — and then
// leaves a server of its own, `setsid sleep 600`, before its Stop. The gate
// script double-forks a server too and is still running when the session
// ends (D447): what a worker started is the worker's, not the session's.
const fakeClaudeGate = `#!/bin/bash
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
printf '%s' "$APE_SESSION" > "$FAKE_DIR/session.marker"
ape milestone gate M1 --wait 0 --cwd "$PWD" > "$FAKE_DIR/gate-call.out" 2>&1
echo $? > "$FAKE_DIR/gate-call.exit"
for _ in $(seq 100); do [ -s "$GATE_SERVER_PID" ] && break; sleep 0.1; done
( setsid sleep 600 > /dev/null 2>&1 < /dev/null & echo $! > "$FAKE_SERVER_PID" )
printf '{"session_id":"fake-1","hook_event_name":"Stop","cwd":"%s","background_tasks":[]}' "$PWD" | sh -c "$stop"
printf '\n❯ '
while IFS= read -r more; do :; done
`

// gateServerScript is M1's gate: it double-forks a server — the
// intermediate shell exits, so the server leaves the worker's tree and, by
// setsid, its session — then runs until the test says the session has
// ended (or a minute), and is green only while that server still runs.
const gateServerScript = `#!/bin/sh
printf '%s' "$APE_SESSION" > "$FAKE_DIR/gate.marker"
( setsid sleep 600 > /dev/null 2>&1 < /dev/null & echo $! > "$GATE_SERVER_PID" )
for _ in $(seq 600); do [ -e "$FAKE_DIR/release" ] && break; sleep 0.1; done
kill -0 "$(cat "$GATE_SERVER_PID")"
`

// workerProject is the v1 fixture with M1's gate set to ./gate.sh (body
// given) and one skill, a fake claude dir, and an env with that claude and
// this ape first on PATH, outside every session.
func workerProject(t *testing.T, ape, claude, gate string) (project, fake string, env []string) {
	t.Helper()
	for _, bin := range []string{"bash", "git", "setsid"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	src, err := filepath.Abs(filepath.Join("..", "..", "testdata", "apexv1project"))
	require.NoError(t, err)
	project = filepath.Join(t.TempDir(), "project")
	require.NoError(t, os.CopyFS(project, os.DirFS(src)))
	roadmap := filepath.Join(project, "development", "roadmap.md")
	data, err := os.ReadFile(roadmap)
	require.NoError(t, err)
	require.Contains(t, string(data), "\nGate: true\n")
	require.NoError(t, writeFile(roadmap, strings.Replace(string(data), "\nGate: true\n", "\nGate: ./gate.sh\n", 1)))
	require.NoError(t, os.WriteFile(filepath.Join(project, "gate.sh"), []byte(gate), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".claude", "skills", "apex-foo"), 0o755))
	require.NoError(t, writeFile(filepath.Join(project, ".claude", "skills", "apex-foo", "SKILL.md"), "# foo\n"))

	fake = t.TempDir()
	if claude != "" {
		require.NoError(t, os.WriteFile(filepath.Join(fake, "claude"), []byte(claude), 0o755))
	}
	require.NoError(t, os.Symlink(ape, filepath.Join(fake, "ape")))
	env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "PATH="+fake+":"+os.Getenv("PATH"), "APE_CLAUDE_PROBE=off", "FAKE_DIR="+fake,
		"GATE_SERVER_PID="+filepath.Join(fake, "gate-server.pid"), "FAKE_SERVER_PID="+filepath.Join(fake, "server.pid"),
		"APE_SESSION=", "APE_DEPTH=", "CLAUDECODE=")
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "core.hooksPath=/dev/null", "commit", "-q", "-m", "base"}} {
		c := exec.Command("git", args...)
		c.Dir, c.Env = project, env
		out, err := c.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	// Whatever the test does, nothing it started outlives it: both servers,
	// the worker, and its gate script.
	t.Cleanup(func() {
		for _, f := range []string{"gate-server.pid", "server.pid"} {
			if pid := serverPID(filepath.Join(fake, f)); pid > 0 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
		killUnder(project)
	})
	return project, fake, env
}

// killUnder SIGKILLs every process whose working directory is under root.
func killUnder(root string) {
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		cwd, err := os.Readlink("/proc/" + e.Name() + "/cwd")
		if err != nil || (cwd != root && !strings.HasPrefix(cwd, root+"/")) {
			continue
		}
		if pid, err := strconv.Atoi(e.Name()); err == nil && pid > 0 && pid != os.Getpid() {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

// D447 (F3 limit 1): a session that starts a detached gate and ends while
// the gate runs. The server the gate script double-forked carried the
// session's APE_SESSION, inherited through the worker, and the session's
// sweep killed it — the gate went red for a reason the project did not
// cause. The worker carries its own marker now: its server survives the
// session and the gate is green, while the server the session itself left
// is still killed and recorded, in the same run.
func TestTask_SparesWhatADetachedGateStarted(t *testing.T) {
	ape := buildApe(t)
	project, fake, env := workerProject(t, ape, fakeClaudeGate, gateServerScript)

	cmd := exec.Command(ape, "task", "apex-foo", "--output-format", "json", "--idle-timeout", "60s", "--cwd", project)
	cmd.Env = env
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	require.NoError(t, cmd.Run(), "stderr:\n%s", tail(stderr.String(), 30))

	call, err := os.ReadFile(filepath.Join(fake, "gate-call.exit"))
	require.NoError(t, err, "the fake session never ran the gate")
	callOut, _ := os.ReadFile(filepath.Join(fake, "gate-call.out"))
	require.Equal(t, "75", strings.TrimSpace(string(call)), "the gate call returns at once, its run still going:\n%s", callOut)
	gatePID := serverPID(filepath.Join(fake, "gate-server.pid"))
	ownPID := serverPID(filepath.Join(fake, "server.pid"))
	require.Positive(t, gatePID, "the gate script never started its server — the fixture tests nothing")
	require.Positive(t, ownPID, "the fake session never started its own server — the fixture tests nothing")
	_, err = os.Stat(filepath.Join(project, "_apex", "state", "runs", "M1.json"))
	require.NoError(t, err, "the gate run must still hold its lock when the session ends, or this is not mid-gate")

	// What the session left is killed and recorded; what the gate started
	// is not the session's.
	require.False(t, alive(ownPID), "the session's own server, pid %d, after ape task", ownPID)
	require.True(t, alive(gatePID), "the gate's server, pid %d, was killed by the session's sweep", gatePID)
	require.Contains(t, stderr.String(), "the session left 1 process running after it ended")
	data, err := os.ReadFile(filepath.Join(runlog.TasksRoot(project), "apex-foo", "latest", "manifest.yaml"))
	require.NoError(t, err)
	var m pipeline.Manifest
	require.NoError(t, yaml.Unmarshal(data, &m))
	require.Len(t, m.SessionLeftovers, 1, "manifest:\n%s", data)
	require.Equal(t, ownPID, m.SessionLeftovers[0].PID)
	require.Equal(t, proctree.LeftoverKilled, m.SessionLeftovers[0].Action)

	// The worker's marker still marks an ape session — the session's —
	// and names the worker's run, so it is never the session's exact entry.
	session, err := os.ReadFile(filepath.Join(fake, "session.marker"))
	require.NoError(t, err)
	require.Equal(t, "task/"+m.RunID, string(session))
	gateMarker, err := os.ReadFile(filepath.Join(fake, "gate.marker"))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(gateMarker), "task/"+m.RunID+"/worker/"), "the gate script's APE_SESSION: %q", gateMarker)

	// The gate ends green: its server was there all along.
	require.NoError(t, writeFile(filepath.Join(fake, "release"), ""))
	attach := exec.Command(ape, "milestone", "gate", "M1", "--wait", "60s", "--cwd", project)
	attach.Env = env
	out, err := attach.CombinedOutput()
	require.NoError(t, err, "the gate went red:\n%s", out)
	require.Contains(t, string(out), "GREEN")
}

// gateNestingScript is M1's gate: it runs `ape task` and records what it
// was told, and is green.
const gateNestingScript = `#!/bin/sh
ape task apex-foo --cwd "$PWD" > "$FAKE_DIR/nested.out" 2>&1
echo $? > "$FAKE_DIR/nested.exit"
exit 0
`

// D447: the worker's own marker changes nothing about the nesting rule.
// `ape task` from a gate script a session's worker runs is refused exactly
// as it is from the session itself: same exit, same code, same words.
func TestTask_NestingFromAWorkersScriptIsTheSessions(t *testing.T) {
	ape := buildApe(t)
	project, fake, env := workerProject(t, ape, "", gateNestingScript)
	env = append(slices.Clone(env), "APE_SESSION=task/r-nest", "APE_DEPTH=1")

	direct := exec.Command(ape, "task", "apex-foo", "--cwd", project)
	direct.Env = env
	directOut, err := direct.CombinedOutput()
	require.Equal(t, 2, exitStatus(err), "ape task in the session:\n%s", directOut)
	require.Contains(t, string(directOut), "code session.depth")

	gate := exec.Command(ape, "milestone", "gate", "M1", "--wait", "60s", "--cwd", project)
	gate.Env = env
	gateOut, err := gate.CombinedOutput()
	require.NoError(t, err, "the gate:\n%s", gateOut)
	exit, err := os.ReadFile(filepath.Join(fake, "nested.exit"))
	require.NoError(t, err, "the gate script never ran ape task")
	nested, err := os.ReadFile(filepath.Join(fake, "nested.out"))
	require.NoError(t, err)
	require.Equal(t, "2", strings.TrimSpace(string(exit)), "ape task from the worker's script:\n%s", nested)
	require.Equal(t, string(directOut), string(nested), "the refusal from the worker's script is the session's, word for word")

	// And no worker of the gate is left: it ended with its result.
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(project, "_apex", "state", "runs", "M1.json"))
		return os.IsNotExist(err)
	}, 10*time.Second, 50*time.Millisecond)
}
