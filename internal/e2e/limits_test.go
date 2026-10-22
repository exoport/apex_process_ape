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

// fakeClaudeSpender is fakeClaude for a session that spends: after the
// typed line it writes a transcript holding one claude-opus-5 turn of
// 200k output tokens ($5.00 at list price) and reports it through
// UserPromptSubmit. With FAKE_STOP_AFTER set it ends the turn that many
// seconds later; without it the turn never ends, as a session looping on
// its own would not.
const fakeClaudeSpender = `#!/bin/bash
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
echo spawned >> "$FAKE_DIR/spawns"
tr="$FAKE_DIR/fake-1.jsonl"
printf '{"type":"assistant","sessionId":"fake-1","timestamp":"2026-10-16T12:00:00.000Z","message":{"id":"msg_1","model":"claude-opus-5","stop_reason":"end_turn","usage":{"input_tokens":0,"output_tokens":200000,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}\n' > "$tr"
printf '{"session_id":"fake-1","hook_event_name":"UserPromptSubmit","cwd":"%s","transcript_path":"%s","prompt":"x"}' "$PWD" "$tr" | sh -c "$ups"
if [ -n "$FAKE_STOP_AFTER" ]; then
  sleep "$FAKE_STOP_AFTER"
  printf '{"session_id":"fake-1","hook_event_name":"Stop","cwd":"%s","transcript_path":"%s","background_tasks":[]}' "$PWD" "$tr" | sh -c "$stop"
fi
printf '\n❯ '
while IFS= read -r more; do :; done
`

// budgetProject is a v1 project whose approved M1 has Budget: 2, with the
// spending fake claude on PATH.
func budgetProject(t *testing.T, ape string) (project, fake string, env []string) {
	t.Helper()
	project, fake = t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fake, "claude"), []byte(fakeClaudeSpender), 0o755))
	env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "APE_SESSION=", "CLAUDECODE=", "APE_SESSION_MODEL=", "APE_SESSION_EFFORT=",
		"PATH="+fake+":"+os.Getenv("PATH"), "APE_CLAUDE_PROBE=off", "FAKE_DIR="+fake)
	sh := func(name string, args ...string) {
		c := exec.Command(name, args...)
		c.Dir = project
		c.Env = env
		out, err := c.CombinedOutput()
		require.NoError(t, err, "%s %v: %s", name, args, out)
	}
	sh("git", "init", "-q")
	sh("git", "commit", "-q", "--allow-empty", "-m", "base")
	sh(ape, "project", "init")
	require.NoError(t, writeFile(filepath.Join(project, "development", "project.md"),
		"# G\n## Vision\nv\n## Users\nu\n## Scope\ns\n## Out of scope\no\n## Constraints\nc\n"))
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".claude", "skills", "apex-foo"), 0o755))
	require.NoError(t, writeFile(filepath.Join(project, ".claude", "skills", "apex-foo", "SKILL.md"), "# foo\n"))
	require.NoError(t, writeWaveGate(project))
	sh("git", "add", "-A")
	sh("git", "commit", "-q", "-m", "setup")
	sh(ape, "milestone", "new", "Skeleton", "--gate", "./gate.sh", "--goal", "waves", "--budget", "2")
	sh(ape, "milestone", "approve", "M1", "--by", "test")
	return project, fake, env
}

func gitOut(t *testing.T, project string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", project}, args...)...).Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

func v1TaskManifest(t *testing.T, project string) pipeline.Manifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(project, "_apex", "state", "runs", "tasks", "apex-foo", "latest", "manifest.yaml"))
	require.NoError(t, err)
	var m pipeline.Manifest
	require.NoError(t, yaml.Unmarshal(data, &m))
	return m
}

// Backlog 30 (a) end to end: with limits.budget: enforce, ape task stops a
// session whose live cost takes the milestone over its Budget:, records
// why in the manifest and in the ignored limit-stops.json (no commit), shows it in
// ape status, and then refuses to start another session.
func TestTask_V1BudgetStopsTheSession(t *testing.T) {
	for _, bin := range []string{"bash", "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	ape := buildApe(t)
	project, fake, env := budgetProject(t, ape)
	setConfig(t, project, "limits:\n  budget: enforce\n")
	// The project's env carries the git identity: a runner has none of its own.
	commit := exec.Command("git", "-C", project, "commit", "-qam", "enforce the budget")
	commit.Env = env
	commitOut, err := commit.CombinedOutput()
	require.NoError(t, err, "%s", commitOut)

	args := []string{"task", "apex-foo", "--idle-timeout", "60s", "--output-format", "json", "--cwd", project}
	_, code := runApe(t, ape, project, env, args...)
	require.Equal(t, 1, code, "the budget stop is a failed run")

	m := v1TaskManifest(t, project)
	require.NotNil(t, m.Termination)
	require.Equal(t, pipeline.TerminationBudget, m.Termination.Kind)
	require.InDelta(t, 5.0, m.Termination.SessionCostUSD, 0.001)
	require.InDelta(t, 5.0, m.Termination.SpentUSD, 0.001)
	require.InDelta(t, 2.0, m.Termination.BudgetUSD, 0.001)
	require.Contains(t, m.Termination.Message, "M1 budget reached")

	require.NotContains(t, gitOut(t, project, "log", "--format=%s"), "stopped", "a stop commits nothing (D38)")
	require.Empty(t, gitOut(t, project, "status", "--porcelain"))
	var recs map[string][]map[string]any
	data, err := os.ReadFile(filepath.Join(project, "_apex", "state", "limit-stops.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &recs))
	require.Len(t, recs["M1"], 1)
	stop := recs["M1"][0]
	require.Equal(t, "budget", stop["reason"])
	require.Equal(t, m.RunID, stop["run"])
	require.Equal(t, "apex-foo", stop["skill"])
	require.InDelta(t, 5.0, stop["session_usd"], 0.001)

	out, code := runApe(t, ape, project, env, "status", "--json")
	require.Equal(t, 0, code)
	//nolint:tagliatelle // ape's snake_case --json contract
	var st struct {
		Milestone struct {
			SpentUSD       float64          `json:"spent_usd"`
			BudgetEnforced bool             `json:"budget_enforced"`
			Stops          []map[string]any `json:"stops"`
		} `json:"milestone"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &st))
	require.InDelta(t, 5.0, st.Milestone.SpentUSD, 0.001, "the stopped session counts at its measured cost")
	require.True(t, st.Milestone.BudgetEnforced)
	require.Len(t, st.Milestone.Stops, 1)
	out, _ = runApe(t, ape, project, env, "status", "--brief")
	require.Contains(t, out, "stopped at budget")

	// At the budget, no session starts.
	c := exec.Command(ape, args...)
	c.Dir, c.Env = project, env
	var stderr strings.Builder
	c.Stderr = &stderr
	require.Equal(t, 1, exitStatus(c.Run()))
	require.Contains(t, stderr.String(), "M1 has spent $5.00 of its $2.00 budget")
	spawns, err := os.ReadFile(filepath.Join(fake, "spawns"))
	require.NoError(t, err)
	require.Equal(t, "spawned\n", string(spawns), "the refused session never spawned")
}

// Off by default: the same over-budget session runs to its Stop.
func TestTask_V1BudgetOffByDefault(t *testing.T) {
	for _, bin := range []string{"bash", "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	ape := buildApe(t)
	project, _, env := budgetProject(t, ape)
	env = append(slices.Clone(env), "FAKE_STOP_AFTER=4")
	_, code := runApe(t, ape, project, env, "task", "apex-foo", "--idle-timeout", "60s", "--output-format", "json", "--cwd", project)
	require.Equal(t, 0, code)
	m := v1TaskManifest(t, project)
	require.Nil(t, m.Termination)
	require.InDelta(t, 5.0, m.Totals.CostUSD, 0.001)
	require.Equal(t, "apex: M1 approved", gitOut(t, project, "log", "-1", "--format=%s"))

	// A mistyped limit refuses the session rather than run it unlimited.
	setConfig(t, project, "limits:\n  budget: enforced\n")
	c := exec.Command(ape, "task", "apex-foo", "--cwd", project)
	c.Dir, c.Env = project, env
	var stderr strings.Builder
	c.Stderr = &stderr
	require.Equal(t, 2, exitStatus(c.Run()))
	require.Contains(t, stderr.String(), `limits.budget: "enforced" is not enforce or off`)
}
