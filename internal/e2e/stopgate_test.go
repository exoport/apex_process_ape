//go:build !windows

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// fakeClaudeV1 is fakeClaude plus what Claude Code does with a v1
// project's own Stop hook: it runs `ape hook stop` (from the project's
// .claude/settings.json) beside ape's injected Stop hook, and when the
// project hook blocks it continues the turn — here by writing the missing
// wave.sh, as a model fixing its red check would — and stops again with
// stop_hook_active true. `ape` resolves to the running binary: ape task
// puts it first on the session's PATH.
const fakeClaudeV1 = `#!/bin/bash
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
printf '{"session_id":"fake-1","hook_event_name":"UserPromptSubmit","cwd":"%s","prompt":"%s"}' "$PWD" "$line" | sh -c "$ups"
active=false
for turn in 1 2 3 4 5; do
  payload=$(printf '{"session_id":"fake-1","hook_event_name":"Stop","cwd":"%s","stop_hook_active":%s,"background_tasks":[]}' "$PWD" "$active")
  printf '%s' "$payload" | ape hook stop > "$FAKE_DIR/project-hook-$turn.out" &
  project=$!
  printf '%s' "$payload" | sh -c "$stop"
  wait $project
  if ! grep -q '"decision":"block"' "$FAKE_DIR/project-hook-$turn.out"; then break; fi
  echo "turn $turn blocked" >> "$FAKE_DIR/turns"
  printf '#!/bin/sh\necho wave\n' > wave.sh; chmod +x wave.sh
  active=true
done
printf '\n❯ '
while IFS= read -r more; do :; done
`

const v1Change = `---
id: add-wave
milestone: M1
touches: [greeting]
---
# Add wave
## Why
To wave.
## Out of scope
- farewells
## Delta
### ADDED [greeting] Requirement: Wave
The system SHALL wave.
#### Scenario: wave
- check: ./wave.sh
## Tasks
- T1 wave — check: ./wave.sh
`

// v1 trial D1 end to end: in a project whose stop hook blocks the first
// Stop (its one in-progress change is red), ape task keeps the session
// instead of ending the run on that Stop, the session fixes the check,
// the next Stop is allowed, and the run completes — with both verdicts
// recorded under stop_gate in the run manifest.
func TestTask_ProjectStopHookBlockKeepsTheRunAlive(t *testing.T) {
	for _, bin := range []string{"bash", "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	ape := buildApe(t)
	project := t.TempDir()
	gitEnv := append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "APE_SESSION=")
	sh := func(name string, args ...string) {
		c := exec.Command(name, args...)
		c.Dir = project
		c.Env = gitEnv
		out, err := c.CombinedOutput()
		require.NoError(t, err, "%s %v: %s", name, args, out)
	}
	sh("git", "init", "-q")
	sh("git", "commit", "-q", "--allow-empty", "-m", "base")
	sh(ape, "project", "init")
	require.NoError(t, writeFile(filepath.Join(project, "development", "project.md"),
		"# G\n## Vision\nv\n## Users\nu\n## Scope\ns\n## Out of scope\no\n## Constraints\nc\n"))
	sh(ape, "milestone", "new", "Skeleton", "--gate", "./wave.sh", "--goal", "waves")
	sh(ape, "milestone", "approve", "M1", "--by", "test")
	sh(ape, "change", "new", "add-wave", "--milestone", "M1", "--touches", "greeting")
	require.NoError(t, writeFile(filepath.Join(project, "development", "changes", "add-wave", "change.md"), v1Change))
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".claude", "skills", "apex-apply"), 0o755))
	require.NoError(t, writeFile(filepath.Join(project, ".claude", "skills", "apex-apply", "SKILL.md"), "# apply\n"))
	sh("git", "add", "-A")
	sh("git", "commit", "-q", "-m", "plan")
	sh(ape, "change", "start", "add-wave")

	fake := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fake, "claude"), []byte(fakeClaudeV1), 0o755))
	cmd := exec.Command(ape, "task", "apex-apply", "--output-format", "json", "--idle-timeout", "60s", "--cwd", project)
	cmd.Env = append(slices.Clone(gitEnv), "PATH="+fake+":"+os.Getenv("PATH"), "APE_CLAUDE_PROBE=off", "FAKE_DIR="+fake)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	require.NoError(t, err, "stderr:\n%s", tail(stderr.String(), 30))

	turns, err := os.ReadFile(filepath.Join(fake, "turns"))
	require.NoError(t, err, "the project hook never blocked — the fixture tests nothing")
	require.Equal(t, "turn 1 blocked\n", string(turns))

	// D16: in a v1 project the run records live in development/.apex/runs,
	// which ignores itself, and nothing is written to _output/.
	data, err := os.ReadFile(filepath.Join(project, "development", ".apex", "runs", "tasks", "apex-apply", "latest", "manifest.yaml"))
	require.NoError(t, err)
	require.NoDirExists(t, filepath.Join(project, "_output"))
	status, err := exec.Command("git", "-C", project, "status", "--porcelain", "--untracked-files=all").Output()
	require.NoError(t, err)
	require.NotContains(t, string(status), ".apex/runs", "run records are never tracked")
	var m pipeline.Manifest
	require.NoError(t, yaml.Unmarshal(data, &m))
	require.NotNil(t, m.StopGate, "manifest:\n%s", data)
	require.Equal(t, 1, m.StopGate.Deferrals, "manifest:\n%s\nstderr:\n%s", data, tail(stderr.String(), 20))
	require.Equal(t, 1, m.StopGate.Allowed)
	require.Equal(t, "block", m.StopGate.Events[0].Verdict)
	require.Equal(t, "add-wave", m.StopGate.Events[0].Change)
}
