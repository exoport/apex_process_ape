// Package e2e holds end-to-end gates that drive the real ape binary against
// the installed Claude Code, the way a framework install uses them.
package e2e

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/exoport/apex_process_ape/internal/repl"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// probeSkill is the stand-in for a framework skill that fans work out to
// sub-agents: it dispatches two in ONE message with run_in_background:false,
// as the framework's batch skills do, and then acts on their results.
const probeSkill = "apex-probe-subagents"

// subSleep is how long each sub-agent's command runs. Two of them in
// parallel finish in about subSleep plus a Haiku turn; in series, in twice
// that. The concurrency assertion sits between the two.
const subSleep = 20 * time.Second

const probeSkillBody = `---
name: apex-probe-subagents
description: ape's end-to-end probe. Dispatches two foreground sub-agents in parallel and records what they saw.
---

# Probe: foreground sub-agents under ape task

Follow these steps exactly. Do not skip any, and do not do a sub-agent's work yourself.

1. In ONE assistant message, call the Agent tool TWICE, in parallel. Each call's input MUST include
   ` + "`run_in_background`" + ` set to the boolean false (explicitly, never omitted), ` + "`subagent_type`" + `
   "general-purpose" and ` + "`model`" + ` "haiku".
   - First call: description "probe a". Prompt: "Run this exact Bash command and then reply with only its
     output: ` + "`{ printenv APE_SESSION; printenv CLAUDE_CODE_FORK_SUBAGENT; ape version | head -1; sleep %d; echo a-done; } > _output/probe/a.txt; cat _output/probe/a.txt`" + `"
   - Second call: description "probe b". Same, with b in place of a: "Run this exact Bash command and then
     reply with only its output: ` + "`{ printenv APE_SESSION; printenv CLAUDE_CODE_FORK_SUBAGENT; ape version | head -1; sleep %d; echo b-done; } > _output/probe/b.txt; cat _output/probe/b.txt`" + `"
2. When BOTH results have arrived, write their full text, a first then b, into ` + "`_output/probe/summary.txt`" + `
   using the Write tool. Write only what the two tool results said.
3. Reply with the single line: PROBE COMPLETE
`

// TestLive_TaskSubagents runs a framework-shaped skill through `ape task`
// and checks every layer a sub-agent-dispatching skill depends on:
//
//   - the run completes (exit 0, manifest completed) through the Stop hook;
//   - each foreground Agent call returns its result inline (`completed`,
//     not `async_launched`), and the parent acts on both results before
//     finishing: summary.txt holds what the sub-agents wrote;
//   - the two sub-agents run concurrently, not one after the other;
//   - inside a sub-agent, APE_SESSION names this task run, the fork gate is
//     off, and `ape` is the binary that started the run (selfpath);
//   - ape's records see the sub-agents: SubagentStart/Stop hook events, and
//     per-session telemetry with each sub-agent's parent session.
//
// Opt-in (APE_CLAUDE_LIVE=1, `make check-task-subagents`): it needs claude,
// auth and network, and costs one short Sonnet session with two Haiku
// sub-agents. A parent that omits run_in_background, or that does the work
// itself, makes the probe invalid; the test says so rather than blaming
// Claude Code, and retries once.
func TestLive_TaskSubagents(t *testing.T) {
	if os.Getenv("APE_CLAUDE_LIVE") != "1" {
		t.Skip("set APE_CLAUDE_LIVE=1 (needs claude on PATH + auth + network) — make check-task-subagents")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not on PATH")
	}
	if os.Getenv(repl.EnvApeSession) != "" {
		t.Skip("inside an ape session (APE_SESSION is set): ape task would refuse to nest; run from a plain shell")
	}
	apeBin := buildApe(t)
	apeVersion := firstLine(t, apeBin, "version")

	var run probeRun
	for attempt := 1; attempt <= 2; attempt++ {
		run = runProbe(t, apeBin)
		if run.valid() {
			break
		}
		t.Logf("attempt %d: probe invalid (%s) — the model did not follow the skill; retrying", attempt, run.invalid)
	}
	require.True(t, run.valid(), "probe invalid after 2 attempts: %s. The parent did not dispatch the way "+
		"framework skills do, so this run cannot speak for ape. Fix the probe skill before trusting any result", run.invalid)

	t.Run("run_completes", func(t *testing.T) {
		require.Equal(t, pipeline.StatusCompleted, run.manifest.Status, "manifest status")
		step := run.step()
		require.NotNil(t, step)
		require.Equal(t, pipeline.StatusCompleted, step.Status)
	})

	t.Run("foreground_results_inline", func(t *testing.T) {
		for _, c := range run.calls {
			require.Equal(t, "completed", c.status,
				"an Agent call with run_in_background:false came back %q under ape task: foreground sub-agents are "+
					"async again, and a skill reading their results gets a launch notice (see %s)", c.status, repl.EnvForkSubagent)
		}
		summary, err := os.ReadFile(filepath.Join(run.project, "_output", "probe", "summary.txt"))
		require.NoError(t, err, "the parent never wrote summary.txt: it finished without acting on its sub-agents' results")
		require.Contains(t, string(summary), "a-done")
		require.Contains(t, string(summary), "b-done")
	})

	t.Run("subagents_concurrent", func(t *testing.T) {
		span := run.lastResult.Sub(run.firstCall)
		t.Logf("two %s sub-agents: first call → last result %s", subSleep, span.Round(time.Second))
		require.Less(t, span, 2*subSleep,
			"the two foreground sub-agents took %s, at least the sum of their work: they ran one after the other, "+
				"so a batch skill now takes N times as long", span.Round(time.Second))
	})

	t.Run("subagent_environment", func(t *testing.T) {
		for _, name := range []string{"a", "b"} {
			data, err := os.ReadFile(filepath.Join(run.project, "_output", "probe", name+".txt"))
			require.NoError(t, err, "sub-agent %s wrote nothing", name)
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			require.GreaterOrEqual(t, len(lines), 4, "sub-agent %s output: %q", name, data)
			require.True(t, strings.HasPrefix(lines[0], "task/"),
				"APE_SESSION in sub-agent %s is %q, not this task run: a nested `ape task` would not be refused", name, lines[0])
			require.Equal(t, "0", lines[1], "%s in sub-agent %s", repl.EnvForkSubagent, name)
			require.Equal(t, apeVersion, lines[2],
				"`ape` inside sub-agent %s is not the binary that started the run (selfpath pin)", name)
		}
	})

	t.Run("records_see_subagents", func(t *testing.T) {
		require.GreaterOrEqual(t, run.hooks["SubagentStart"], 2, "SubagentStart hook events: %v", run.hooks)
		require.GreaterOrEqual(t, run.hooks["SubagentStop"], 2, "SubagentStop hook events: %v", run.hooks)
		subs := 0
		for _, s := range run.step().Sessions {
			if s.ParentSessionID != "" {
				subs++
			}
		}
		require.GreaterOrEqual(t, subs, 2, "the manifest's telemetry holds %d sub-agent sessions: their cost is not "+
			"accounted to the run", subs)
	})
}

// probeRun is what one probe run left behind.
type probeRun struct {
	project               string
	manifest              pipeline.Manifest
	calls                 []agentCall
	firstCall, lastResult time.Time
	hooks                 map[string]int
	invalid               string
}

func (r probeRun) valid() bool { return r.invalid == "" }

func (r probeRun) step() *pipeline.StepRecord {
	if len(r.manifest.Stages) == 0 || len(r.manifest.Stages[0].Steps) == 0 {
		return nil
	}
	return &r.manifest.Stages[0].Steps[0]
}

func runProbe(t *testing.T, apeBin string) probeRun {
	t.Helper()
	project := seedProject(t)
	skillDir := filepath.Join(project, ".claude", "skills", probeSkill)
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	secs := int(subSleep.Seconds())
	body := strings.Replace(strings.Replace(probeSkillBody, "%d", strconv.Itoa(secs), 1), "%d", strconv.Itoa(secs), 1)
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(project, "_output", "probe"), 0o755))

	cmd := exec.Command(apeBin, "task", probeSkill,
		"--cwd", project, "--model", "sonnet", "--effort", "low",
		"--idle-timeout", "5m", "--max-duration", "10m")
	cmd.Dir = project
	out, err := cmd.CombinedOutput()
	t.Logf("ape task exited: %v\n%s", err, tail(string(out), 30))
	require.NoError(t, err, "ape task must exit 0")

	run := probeRun{project: project, hooks: map[string]int{}}
	runDir := latestRunDir(t, project)
	data, err := os.ReadFile(filepath.Join(runDir, "manifest.yaml"))
	require.NoError(t, err)
	require.NoError(t, yaml.Unmarshal(data, &run.manifest))
	run.hooks = countHooks(t, filepath.Join(runDir, "hook-events.jsonl"))
	run.calls, run.firstCall, run.lastResult = agentCalls(t, filepath.Join(runDir, "transcripts"))

	switch {
	case len(run.calls) < 2:
		run.invalid = "the parent made " + strconv.Itoa(len(run.calls)) + " Agent call(s), not 2"
	default:
		for _, c := range run.calls {
			if c.runInBackground == nil || *c.runInBackground {
				run.invalid = "an Agent call omitted run_in_background or set it true"
			}
		}
	}
	return run
}

// agentCall is one Agent tool call in the parent's transcript.
type agentCall struct {
	runInBackground *bool
	status          string
}

// agentCalls reads the parent's transcript(s) in a run's transcripts dir:
// its Agent calls with the status of each result, the time of the first
// call and of the last result.
func agentCalls(t *testing.T, dir string) (calls []agentCall, first, last time.Time) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	require.NoError(t, err)
	index := map[string]int{}
	for _, f := range files {
		if strings.HasPrefix(filepath.Base(f), "agent-") {
			continue
		}
		fh, err := os.Open(f)
		require.NoError(t, err)
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			//nolint:tagliatelle // Claude Code's transcript field names, not ours
			var rec struct {
				Timestamp time.Time `json:"timestamp"`
				Message   struct {
					Content []struct {
						Type      string `json:"type"`
						ID        string `json:"id"`
						Name      string `json:"name"`
						ToolUseID string `json:"tool_use_id"`
						Input     struct {
							RunInBackground *bool `json:"run_in_background"`
						} `json:"input"`
					} `json:"content"`
				} `json:"message"`
				ToolUseResult json.RawMessage `json:"toolUseResult"`
			}
			if json.Unmarshal(sc.Bytes(), &rec) != nil {
				continue
			}
			for _, b := range rec.Message.Content {
				switch {
				case b.Type == "tool_use" && (b.Name == "Agent" || b.Name == "Task"):
					index[b.ID] = len(calls)
					calls = append(calls, agentCall{runInBackground: b.Input.RunInBackground})
					if first.IsZero() || rec.Timestamp.Before(first) {
						first = rec.Timestamp
					}
				case b.Type == "tool_result":
					i, ok := index[b.ToolUseID]
					if !ok {
						continue
					}
					var res struct {
						Status string `json:"status"`
					}
					if json.Unmarshal(rec.ToolUseResult, &res) == nil {
						calls[i].status = res.Status
					}
					if rec.Timestamp.After(last) {
						last = rec.Timestamp
					}
				}
			}
		}
		_ = fh.Close()
	}
	return calls, first, last
}

func countHooks(t *testing.T, path string) map[string]int {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	out := map[string]int{}
	for line := range strings.SplitSeq(string(data), "\n") {
		var ev struct {
			Event string `json:"event"`
		}
		if json.Unmarshal([]byte(line), &ev) == nil && ev.Event != "" {
			out[ev.Event]++
		}
	}
	return out
}

func latestRunDir(t *testing.T, project string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(filepath.Join(project, "_output", "ape", "tasks", probeSkill, "latest"))
	require.NoError(t, err, "no run directory: ape task wrote no record")
	return dir
}

// seedProject copies testdata/apexproject into a temp dir and makes it a
// git repository, as a framework install is.
func seedProject(t *testing.T) string {
	t.Helper()
	src, err := filepath.Abs(filepath.Join("..", "..", "testdata", "apexproject"))
	require.NoError(t, err)
	dst := filepath.Join(t.TempDir(), "project")
	require.NoError(t, os.CopyFS(dst, os.DirFS(src)))
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "-A"},
		{"-c", "user.email=probe@example.com", "-c", "user.name=probe", "-c", "core.hooksPath=/dev/null", "commit", "-q", "-m", "seed"},
	} {
		c := exec.Command("git", args...)
		c.Dir = dst
		out, err := c.CombinedOutput()
		require.NoErrorf(t, err, "git %v: %s", args, out)
	}
	return dst
}

func buildApe(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "ape")
	build := exec.Command("go", "build", "-o", bin, "./cmd/ape")
	build.Dir = filepath.Join("..", "..")
	out, err := build.CombinedOutput()
	require.NoErrorf(t, err, "build ape: %s", out)
	return bin
}

func firstLine(t *testing.T, bin string, args ...string) string {
	t.Helper()
	out, err := exec.Command(bin, args...).Output()
	require.NoError(t, err)
	line, _, _ := strings.Cut(string(out), "\n")
	return strings.TrimSpace(line)
}

func tail(s string, n int) string {
	ls := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(ls) <= n {
		return strings.Join(ls, "\n")
	}
	return "…\n" + strings.Join(ls[len(ls)-n:], "\n")
}
