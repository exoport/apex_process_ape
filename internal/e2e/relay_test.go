//go:build !windows

package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// fakeClaudeLoop stands in for claude under the loop relay (OI-11). A loop
// session (any line but apex-plan's) writes a transcript whose turn sent
// the next of FAKE_LOOP_TOKENS (space-separated, one per loop session) on
// FAKE_MODEL, reports it through UserPromptSubmit, saves `ape status
// --json` as status-<n>.json, launches one child —
// `ape task apex-plan`, as the loop does — recording its exit and output,
// then ends its turn. A child session (apex-plan's line) ends its turn at
// once.
const fakeClaudeLoop = `#!/bin/bash
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
case "$line" in
  */apex-plan*) kind=child ;;
  *) kind=loop ;;
esac
echo x >> "$FAKE_DIR/$kind-sessions"
n=$(wc -l < "$FAKE_DIR/$kind-sessions" | tr -d ' ')
tokens=10
if [ "$kind" = loop ]; then tokens=$(echo $FAKE_LOOP_TOKENS | cut -d' ' -f"$n"); fi
tr="$FAKE_DIR/$kind-$n.jsonl"
printf '{"type":"assistant","sessionId":"%s-%s","timestamp":"2026-10-19T12:00:00.000Z","message":{"id":"msg_%s_%s","model":"%s","stop_reason":"tool_use","usage":{"input_tokens":2,"output_tokens":1,"cache_read_input_tokens":%s,"cache_creation_input_tokens":0}}}\n' "$kind" "$n" "$kind" "$n" "${FAKE_MODEL:-claude-sonnet-4-5}" "$tokens" > "$tr"
printf '{"session_id":"%s-%s","hook_event_name":"UserPromptSubmit","cwd":"%s","transcript_path":"%s","prompt":"x"}' "$kind" "$n" "$PWD" "$tr" | sh -c "$ups"
if [ "$kind" = loop ]; then
  "$APE_BIN" status --json --cwd "$PWD" > "$FAKE_DIR/status-$n.json" 2>/dev/null
  "$APE_BIN" task apex-plan --args M1 --idle-timeout 60s --cwd "$PWD" > "$FAKE_DIR/child-of-$n.out" 2>&1
  echo $? > "$FAKE_DIR/child-of-$n.code"
fi
printf '{"session_id":"%s-%s","hook_event_name":"Stop","cwd":"%s","transcript_path":"%s","background_tasks":[]}' "$kind" "$n" "$PWD" "$tr" | sh -c "$stop"
printf '\n❯ '
while IFS= read -r more; do :; done
`

// loopProject is a v1 project with an approved M1, the loop's stand-in
// claude on PATH, and limits (appended to _apex/config.yaml, committed).
func loopProject(t *testing.T, limits string) (ape, project, fake string, env []string) {
	t.Helper()
	for _, bin := range []string{"bash", "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	ape = buildApe(t)
	project, fake = t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fake, "claude"), []byte(fakeClaudeLoop), 0o755))
	env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "APE_SESSION=", "APE_DEPTH=", "CLAUDECODE=", "APE_SESSION_MODEL=", "APE_SESSION_EFFORT=",
		"PATH="+fake+":"+os.Getenv("PATH"), "APE_CLAUDE_PROBE=off", "FAKE_DIR="+fake, "APE_BIN="+ape)
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
	for _, skill := range []string{apexv1.OrchestratorSkill, "apex-plan"} {
		require.NoError(t, os.MkdirAll(filepath.Join(project, ".claude", "skills", skill), 0o755))
		require.NoError(t, writeFile(filepath.Join(project, ".claude", "skills", skill, "SKILL.md"), "# "+skill+"\n"))
	}
	require.NoError(t, writeWaveGate(project))
	if limits != "" {
		setConfig(t, project, limits)
	}
	sh("git", "add", "-A")
	sh("git", "commit", "-q", "-m", "setup")
	sh(ape, "milestone", "new", "Skeleton", "--gate", "./gate.sh", "--goal", "waves")
	sh(ape, "milestone", "approve", "M1", "--by", "test")
	return ape, project, fake, env
}

// loopManifests are the loop's run manifests, oldest first.
func loopManifests(t *testing.T, project string) []pipeline.Manifest {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(project, "_apex", "state", "runs", "tasks", apexv1.OrchestratorSkill, "*", "manifest.yaml"))
	require.NoError(t, err)
	sort.Strings(paths)
	var out []pipeline.Manifest
	for _, p := range paths {
		if filepath.Base(filepath.Dir(p)) == "latest" {
			continue
		}
		data, err := os.ReadFile(p)
		require.NoError(t, err)
		var m pipeline.Manifest
		require.NoError(t, yaml.Unmarshal(data, &m))
		out = append(out, m)
	}
	return out
}

func fakeFile(t *testing.T, fake, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fake, name))
	require.NoError(t, err)
	return strings.TrimSpace(string(data))
}

//nolint:tagliatelle // ape's snake_case --json contract
type relayEnvelope struct {
	ExitCode int     `json:"exit_code"`
	Relays   int     `json:"relays"`
	RelayOf  string  `json:"relay_of"`
	Error    *string `json:"error"`
}

// OI-11 end to end: the loop's first session, at 75 % of its 200K window,
// has its child launch refused with session.relay and ends; ape task starts
// a fresh loop session with the same args, recorded relay_of the first run,
// whose child (at 0 %) starts and runs. One run of the loop, one lock.
func TestTask_LoopRelayedAtTheThreshold(t *testing.T) {
	ape, project, fake, env := loopProject(t, "")
	env = append(slices.Clone(env), "FAKE_LOOP_TOKENS=150000 10")
	out, code := runApe(t, ape, project, env, "task", apexv1.OrchestratorSkill, "--args", "M1", "--idle-timeout", "60s", "--output-format", "json", "--cwd", project)
	require.Equal(t, 0, code, out)

	require.Equal(t, "2", fakeFile(t, fake, "child-of-1.code"), "the first session's child launch is refused, exit 2")
	refused := fakeFile(t, fake, "child-of-1.out")
	require.Contains(t, refused, "context is at 75 % (150002 of 200000 tokens, claude-sonnet-4-5), at or over limits.context_relay 60 %")
	require.Contains(t, refused, "(code session.relay, action stop)")
	require.Equal(t, "0", fakeFile(t, fake, "child-of-2.code"), "the fresh session's child runs: %s", fakeFile(t, fake, "child-of-2.out"))
	require.Equal(t, "x", fakeFile(t, fake, "child-sessions"), "one child session: the refused launch spawned none")

	ms := loopManifests(t, project)
	require.Len(t, ms, 2, "two loop sessions")
	require.Empty(t, ms[0].RelayOf)
	require.Zero(t, ms[0].Relay)
	require.Equal(t, ms[0].RunID, ms[1].RelayOf, "the fresh session records the run it relays")
	require.Equal(t, 1, ms[1].Relay)
	require.Equal(t, pipeline.StatusCompleted, ms[1].Status)
	require.NotNil(t, ms[1].TreeCostUSD)
	require.Len(t, ms[1].Children, 1, "the child is the fresh session's")

	var envl relayEnvelope
	require.NoError(t, json.Unmarshal([]byte(out), &envl), out)
	require.Equal(t, 1, envl.Relays)
	require.Equal(t, ms[0].RunID, envl.RelayOf)

	// ape status --json, read by each live loop session: its fill and relays.
	for i, want := range []struct {
		fill    float64
		relays  int
		relayOf string
	}{{0.75001, 0, ""}, {0.00006, 1, ms[0].RunID}} {
		var st struct {
			Sessions []struct {
				RunID string `json:"run_id"` //nolint:tagliatelle // ape's snake_case --json contract
				Relay *struct {
					ContextFill  *float64 `json:"context_fill"`  //nolint:tagliatelle // ape's snake_case --json contract
					ContextRelay float64  `json:"context_relay"` //nolint:tagliatelle // ape's snake_case --json contract
					Relays       int      `json:"relays"`
					RelaysCap    int      `json:"relays_cap"` //nolint:tagliatelle // ape's snake_case --json contract
					RelayOf      string   `json:"relay_of"`   //nolint:tagliatelle // ape's snake_case --json contract
				} `json:"relay"`
			} `json:"sessions"`
		}
		n := strconv.Itoa(i + 1)
		require.NoError(t, json.Unmarshal([]byte(fakeFile(t, fake, "status-"+n+".json")), &st))
		require.Len(t, st.Sessions, 1, "the loop, live, with no child yet")
		require.Equal(t, ms[i].RunID, st.Sessions[0].RunID)
		r := st.Sessions[0].Relay
		require.NotNil(t, r, "session %s", n)
		require.NotNil(t, r.ContextFill)
		require.InDelta(t, want.fill, *r.ContextFill, 1e-4)
		require.InDelta(t, 0.6, r.ContextRelay, 1e-9)
		require.Equal(t, want.relays, r.Relays)
		require.Equal(t, 5, r.RelaysCap)
		require.Equal(t, want.relayOf, r.RelayOf)
	}
	require.NoDirExists(t, filepath.Join(project, "_apex", "state", "runs", "relays", ms[1].RunID+".json"))
	records, _ := filepath.Glob(filepath.Join(project, "_apex", "state", "runs", "relays", "*.json"))
	require.Empty(t, records, "the relay records go with their runs")
	locks, _ := filepath.Glob(filepath.Join(project, "_apex", "state", "runs", "sessions", "*.json"))
	require.Empty(t, locks)
}

// Past limits.relays the loop run stops: the second session's child launch
// is refused with limits.relays (exit 1, person), and ape task exits 1 with
// termination relays and a resume line.
func TestTask_LoopRelaysExhausted(t *testing.T) {
	ape, project, fake, env := loopProject(t, "limits:\n  relays: 1\n")
	env = append(slices.Clone(env), "FAKE_LOOP_TOKENS=150000 150000")
	out, code := runApe(t, ape, project, env, "task", apexv1.OrchestratorSkill, "--args", "M1", "--idle-timeout", "60s", "--output-format", "json", "--cwd", project)
	require.Equal(t, 1, code, out)
	require.Equal(t, "2", fakeFile(t, fake, "child-of-1.code"))
	require.Equal(t, "1", fakeFile(t, fake, "child-of-2.code"))
	require.Contains(t, fakeFile(t, fake, "child-of-2.out"), "(code limits.relays, action person)")
	_, err := os.Stat(filepath.Join(fake, "child-sessions"))
	require.True(t, os.IsNotExist(err), "no child ever spawned")

	ms := loopManifests(t, project)
	require.Len(t, ms, 2, "one relay, then the stop")
	require.NotNil(t, ms[1].Termination)
	require.Equal(t, pipeline.TerminationRelays, ms[1].Termination.Kind)
	var envl relayEnvelope
	require.NoError(t, json.Unmarshal([]byte(out), &envl), out)
	require.Equal(t, 1, envl.ExitCode)
	require.Equal(t, 1, envl.Relays)
	require.NotNil(t, envl.Error)
	require.Contains(t, *envl.Error, "after 1 relay(s), limits.relays 1")
	require.Contains(t, *envl.Error, "resume: `ape status` shows where M1 stopped; then `ape task "+apexv1.OrchestratorSkill+" --args M1`")

	st, code := runApe(t, ape, project, env, "status", "--json")
	require.Equal(t, 0, code)
	var status struct {
		LatestRun struct {
			RunID   string `json:"run_id"` //nolint:tagliatelle // ape's snake_case --json contract
			Stopped string `json:"stopped"`
		} `json:"latest_run"` //nolint:tagliatelle // ape's snake_case --json contract
	}
	require.NoError(t, json.Unmarshal([]byte(st), &status))
	require.Equal(t, ms[1].RunID, status.LatestRun.RunID)
	require.Equal(t, pipeline.TerminationRelays, status.LatestRun.Stopped)
}

// An unknown context window is an unknown fill: no relay, said once.
func TestTask_LoopUnknownWindowNotRelayed(t *testing.T) {
	ape, project, fake, env := loopProject(t, "")
	env = append(slices.Clone(env), "FAKE_LOOP_TOKENS=900000", "FAKE_MODEL=claude-nonesuch-9")
	c := exec.Command(ape, "task", apexv1.OrchestratorSkill, "--args", "M1", "--idle-timeout", "60s", "--cwd", project)
	c.Dir, c.Env = project, env
	var stderr strings.Builder
	c.Stderr = &stderr
	require.Equal(t, 0, exitStatus(c.Run()), stderr.String())
	require.Equal(t, "0", fakeFile(t, fake, "child-of-1.code"), "the child starts")
	require.Contains(t, fakeFile(t, fake, "child-of-1.out"), "ape knows no context window for claude-nonesuch-9")
	require.Contains(t, stderr.String(), "⚠ relay: ape knows no context window for claude-nonesuch-9")
	require.Len(t, loopManifests(t, project), 1, "one session")
}
