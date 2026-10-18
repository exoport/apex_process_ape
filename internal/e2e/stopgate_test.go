//go:build !windows

package e2e

import (
	"encoding/json"
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
// project's own Stop hook: it runs every distinct Stop command — `ape hook
// stop` when the project's .claude/settings.json registers it, and the
// copy ape task passes inline in --settings — beside ape's injected Stop
// hook, and when any of them blocks it continues the turn — here by
// writing the missing wave.sh, as a model fixing its red check would, after
// FAKE_CONTINUE_DELAY seconds — and stops again with stop_hook_active true.
// The turn whose Stop no hook blocked is written to $FAKE_DIR/allowed.
// `ape` resolves to the running binary: ape task puts it first on the
// session's PATH.
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
inline=$(printf '%s' "$settings" | grep -o "ape hook stop --inline" | head -1)
fromfile=""
if grep -q '"ape hook stop"' .claude/settings.json 2>/dev/null; then fromfile="ape hook stop"; fi
printf '\n  bypass permissions on (shift+tab to cycle)\n❯ '
IFS= read -r line
line=${line%$'\r'}
printf '{"session_id":"fake-1","hook_event_name":"UserPromptSubmit","cwd":"%s","prompt":"%s"}' "$PWD" "$line" | sh -c "$ups"
active=false
for turn in 1 2 3 4 5; do
  payload=$(printf '{"session_id":"fake-1","hook_event_name":"Stop","cwd":"%s","stop_hook_active":%s,"background_tasks":[]}' "$PWD" "$active")
  : > "$FAKE_DIR/project-hook-$turn.out"
  if [ -n "$fromfile" ]; then printf '%s' "$payload" | $fromfile > "$FAKE_DIR/file-hook-$turn.out" & a=$!; fi
  if [ -n "$inline" ]; then printf '%s' "$payload" | $inline > "$FAKE_DIR/inline-hook-$turn.out" & b=$!; fi
  printf '%s' "$payload" | sh -c "$stop"
  [ -n "$fromfile" ] && wait $a && cat "$FAKE_DIR/file-hook-$turn.out" >> "$FAKE_DIR/project-hook-$turn.out"
  [ -n "$inline" ] && wait $b && cat "$FAKE_DIR/inline-hook-$turn.out" >> "$FAKE_DIR/project-hook-$turn.out"
  if ! grep -q '"decision":"block"' "$FAKE_DIR/project-hook-$turn.out"; then echo "$turn" > "$FAKE_DIR/allowed"; break; fi
  echo "turn $turn blocked" >> "$FAKE_DIR/turns"
  sleep "${FAKE_CONTINUE_DELAY:-0}"
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
//
// Backlog 31: the stop hook reaches the session whether or not the
// project's settings.json still registers it — ape task passes it inline —
// and when both copies run, exactly one decides each Stop.
func TestTask_ProjectStopHookBlockKeepsTheRunAlive(t *testing.T) {
	for _, bin := range []string{"bash", "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	ape := buildApe(t)
	t.Run("settings.json and inline", func(t *testing.T) { stopHookRun(t, ape, false) })
	t.Run("hooks removed from settings.json", func(t *testing.T) { stopHookRun(t, ape, true) })
}

func stopHookRun(t *testing.T, ape string, dropSettingsHooks bool) {
	t.Helper()
	project, gitEnv := redChangeProject(t, ape)
	if dropSettingsHooks {
		require.NoError(t, writeFile(filepath.Join(project, ".claude", "settings.json"), "{}\n"))
	}

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

	// D16, backlog 26: in a v1 project the run records live in
	// _apex/state/runs, which git ignores, and nothing is written to _output/.
	data, err := os.ReadFile(filepath.Join(project, "_apex", "state", "runs", "tasks", "apex-apply", "latest", "manifest.yaml"))
	require.NoError(t, err)
	require.NoDirExists(t, filepath.Join(project, "_output"))
	status, err := exec.Command("git", "-C", project, "status", "--porcelain", "--untracked-files=all").Output()
	require.NoError(t, err)
	require.NotContains(t, string(status), "_apex/state", "run records, logs and hook verdicts are never tracked")
	verdictLogs, err := filepath.Glob(filepath.Join(project, "_apex", "state", "hooks", "*.jsonl"))
	require.NoError(t, err)
	require.NotEmpty(t, verdictLogs, "the stop hook's verdict log is in _apex/state/hooks, where ape task read it")
	var m pipeline.Manifest
	require.NoError(t, yaml.Unmarshal(data, &m))
	require.NotNil(t, m.StopGate, "manifest:\n%s", data)
	require.Equal(t, 1, m.StopGate.Deferrals, "manifest:\n%s\nstderr:\n%s", data, tail(stderr.String(), 20))
	require.Equal(t, 1, m.StopGate.Allowed)
	require.Equal(t, "block", m.StopGate.Events[0].Verdict)
	require.Equal(t, "add-wave", m.StopGate.Events[0].Change)
	if !dropSettingsHooks {
		out, err := os.ReadFile(filepath.Join(fake, "file-hook-1.out"))
		require.NoError(t, err, "the settings.json copy ran")
		require.Empty(t, string(out), "the settings.json copy stands down for the inline one")
	}
	inline, err := os.ReadFile(filepath.Join(fake, "inline-hook-1.out"))
	require.NoError(t, err)
	require.Contains(t, string(inline), `"decision":"block"`)
}

// rc.5 11a: ape prompt honours a blocked Stop as ape task does (D88). The
// project's settings.json copy of the stop hook blocks the first Stop (its
// in-progress change is red); the session takes two seconds to fix the
// check and stop again. Before, ape prompt ended the session on the
// blocked Stop, so the session never reached its second one.
func TestPrompt_ProjectStopHookBlockKeepsTheSessionAlive(t *testing.T) {
	for _, bin := range []string{"bash", "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	ape := buildApe(t)
	project, gitEnv := redChangeProject(t, ape)

	fake := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fake, "claude"), []byte(fakeClaudeV1), 0o755))
	cmd := exec.Command(ape, "prompt", "fix the wave", "--output-format", "json", "--idle-timeout", "60s", "--cwd", project)
	cmd.Env = append(slices.Clone(gitEnv), "PATH="+fake+":"+os.Getenv("PATH"), "APE_CLAUDE_PROBE=off", "FAKE_DIR="+fake,
		"FAKE_CONTINUE_DELAY=2")
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	require.NoError(t, cmd.Run(), "stderr:\n%s", tail(stderr.String(), 30))

	allowed, err := os.ReadFile(filepath.Join(fake, "allowed"))
	require.NoError(t, err, "the session was ended before its allowed Stop\nstderr:\n%s", tail(stderr.String(), 20))
	require.Equal(t, "2\n", string(allowed))
	turns, err := os.ReadFile(filepath.Join(fake, "turns"))
	require.NoError(t, err, "the project hook never blocked — the fixture tests nothing")
	require.Equal(t, "turn 1 blocked\n", string(turns))

	var res struct {
		PromptID string `json:"prompt_id"`
		Status   string `json:"status"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout.String()), &res), stdout.String())
	require.Equal(t, "completed", res.Status)
	data, err := os.ReadFile(filepath.Join(project, "_apex", "state", "runs", "prompts", res.PromptID, "prompt.yaml"))
	require.NoError(t, err)
	var rec struct {
		StopGate *struct {
			Deferrals int `yaml:"deferrals"`
			Allowed   int `yaml:"allowed"`
			Events    []struct {
				Verdict string `yaml:"verdict"`
				Change  string `yaml:"change"`
			} `yaml:"events"`
		} `yaml:"stop_gate"`
	}
	require.NoError(t, yaml.Unmarshal(data, &rec))
	require.NotNil(t, rec.StopGate, "prompt.yaml:\n%s", data)
	require.Equal(t, 1, rec.StopGate.Deferrals)
	require.Equal(t, 1, rec.StopGate.Allowed)
	require.Equal(t, "block", rec.StopGate.Events[0].Verdict)
	require.Equal(t, "add-wave", rec.StopGate.Events[0].Change)
}

// redChangeProject is a v1 project on the branch of its one in-progress
// change, add-wave, whose check (./wave.sh) is red: the stop hook blocks
// every Stop there until wave.sh exists. It returns the project and the
// environment to run ape and git in.
func redChangeProject(t *testing.T, ape string) (string, []string) {
	t.Helper()
	project := t.TempDir()
	gitEnv := append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "APE_SESSION=", "CLAUDECODE=")
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
	require.NoError(t, writeWaveGate(project))
	sh(ape, "milestone", "new", "Skeleton", "--gate", "./gate.sh", "--goal", "waves")
	sh(ape, "milestone", "approve", "M1", "--by", "test")
	sh(ape, "change", "new", "add-wave", "--milestone", "M1", "--touches", "greeting")
	require.NoError(t, writeFile(filepath.Join(project, "development", "changes", "add-wave", "change.md"), v1Change))
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".claude", "skills", "apex-apply"), 0o755))
	require.NoError(t, writeFile(filepath.Join(project, ".claude", "skills", "apex-apply", "SKILL.md"), "# apply\n"))
	sh("git", "add", "-A")
	sh("git", "commit", "-q", "-m", "plan")
	// D38: start checks out change/add-wave; the gate judges the change
	// whose branch is HEAD.
	sh(ape, "change", "start", "add-wave")
	head, err := exec.Command("git", "-C", project, "branch", "--show-current").Output()
	require.NoError(t, err)
	require.Equal(t, "change/add-wave", strings.TrimSpace(string(head)))
	return project, gitEnv
}
