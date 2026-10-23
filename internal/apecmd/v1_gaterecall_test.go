package apecmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/stretchr/testify/require"
)

// gateRecallRepo is v1Repo with M1's gate a red script that counts its
// runs in a file outside the repo, approved as such (its self-check counts
// one), and a spec scenario that counts its own runs the same way.
func gateRecallRepo(t *testing.T) (root string, gateRuns, specRuns func() int) {
	t.Helper()
	root = v1Repo(t)
	dir := t.TempDir()
	gateCount, specCount := filepath.Join(dir, "gate.count"), filepath.Join(dir, "spec.count")
	require.NoError(t, os.WriteFile(filepath.Join(root, "gate.sh"), []byte("#!/bin/sh\necho run >> '"+gateCount+"'\necho red\nexit 1\n"), 0o755))
	spec := "# Greeting\nPurpose: Say things.\n\n### Requirement: Hello\nThe system SHALL say hello.\n" +
		"#### Scenario: hello\n- GIVEN x WHEN y THEN z\n- check: echo run >> '" + specCount + "'\n"
	require.NoError(t, os.MkdirAll(filepath.Join(root, "development", "specs", "greeting"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "development", "specs", "greeting", "spec.md"), []byte(spec), 0o644))
	v1Git(t, root, "add", "-A")
	v1Git(t, root, "commit", "-q", "-m", "counting gate")
	_, _, err := v1Exec(t, newMilestoneCmd, "approve", "M1", "--by", "eval", "--cwd", root)
	require.NoError(t, err)
	count := func(path string) func() int {
		return func() int {
			data, err := os.ReadFile(path)
			if os.IsNotExist(err) {
				return 0
			}
			require.NoError(t, err)
			return strings.Count(string(data), "run\n")
		}
	}
	gateRuns, specRuns = count(gateCount), count(specCount)
	require.Equal(t, 1, gateRuns(), "approve's self-check ran the gate")
	require.Empty(t, v1Git(t, root, "status", "--porcelain"))
	return root, gateRuns, specRuns
}

// gateJSON runs `ape milestone gate M1 --json` (plus args) and returns its
// exit, whether it says "recorded", and its stderr.
func gateJSON(t *testing.T, root string, args ...string) (exit int, recorded bool, stderr string) {
	t.Helper()
	stdout, stderr, err := v1Exec(t, newMilestoneCmd, append([]string{"gate", "M1", "--json", "--cwd", root}, args...)...)
	var out struct {
		Recorded    bool `json:"recorded"`
		Diagnostics []struct {
			Code string `json:"code"`
		} `json:"diagnostics"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &out), stdout)
	exit = exitCodeOf(t, err)
	if exit == ExitRunFailed {
		require.Equal(t, "gate.red", out.Diagnostics[0].Code, stdout)
	}
	return exit, out.Recorded, stderr
}

// exitOf is gateJSON's exit alone.
func exitOf(exit int, _ bool, _ string) int { return exit }

// F1 (D419, D432): a gate call at an unchanged HEAD and tree returns the
// finished run — its output, its exit code, "recorded": true — and never
// runs the gate again; human output says so on stderr.
func TestGateRecall_UnchangedReturnsFinishedRun(t *testing.T) {
	root, gateRuns, _ := gateRecallRepo(t)
	exit, recorded, _ := gateJSON(t, root)
	require.Equal(t, ExitRunFailed, exit)
	require.False(t, recorded, "a new run")
	require.Equal(t, 2, gateRuns())

	exit, recorded, stderr := gateJSON(t, root)
	require.Equal(t, ExitRunFailed, exit, "the recorded run's exit")
	require.True(t, recorded)
	require.Contains(t, stderr, "the finished run's result, not a new run — `ape milestone gate M1 --rerun` runs it again")
	require.Equal(t, 2, gateRuns(), "the gate did not run again")

	stdout, stderr, err := v1Exec(t, newMilestoneCmd, "gate", "M1", "--cwd", root)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err))
	require.Contains(t, stdout, "M1 gate RED (gate command exit 1)")
	require.Contains(t, stderr, "unchanged since ")
	require.Equal(t, 2, gateRuns())

	// status --json says a call would return it.
	stdout, _, err = v1Exec(t, newStatusCmd, "--json", "--cwd", root)
	require.NoError(t, err)
	var st struct {
		Milestone struct {
			GateLast struct {
				Current *bool `json:"current"`
			} `json:"gate_last"` //nolint:tagliatelle // the v1 --json payloads are snake_case by contract
		} `json:"milestone"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &st), stdout)
	require.NotNil(t, st.Milestone.GateLast.Current)
	require.True(t, *st.Milestone.GateLast.Current)
}

// Anything new in the working tree outside _apex/state/ is a new run, and
// status says the last one is not current.
func TestGateRecall_UntrackedFileRunsAgain(t *testing.T) {
	root, gateRuns, _ := gateRecallRepo(t)
	require.Equal(t, ExitRunFailed, exitOf(gateJSON(t, root)))
	require.NoError(t, os.WriteFile(filepath.Join(root, "new.txt"), []byte("x\n"), 0o644))
	stdout, _, err := v1Exec(t, newStatusCmd, "--json", "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, stdout, `"current": false`)
	_, recorded, _ := gateJSON(t, root)
	require.False(t, recorded)
	require.Equal(t, 3, gateRuns())
	_, recorded, _ = gateJSON(t, root)
	require.True(t, recorded, "and that run is the answer now")
	require.Equal(t, 3, gateRuns())
}

// The run's own "apex: M1 gate red" commit, and ape's state written since,
// keep its result current; another commit does not.
func TestGateRecall_StateCommitKeepsIt(t *testing.T) {
	root, gateRuns, _ := gateRecallRepo(t)
	before := v1Git(t, root, "rev-parse", "HEAD")
	require.Equal(t, ExitRunFailed, exitOf(gateJSON(t, root)))
	require.NotEqual(t, before, v1Git(t, root, "rev-parse", "HEAD"))
	require.Equal(t, "apex: M1 gate red", v1Git(t, root, "log", "-1", "--format=%s"))
	require.NoError(t, os.WriteFile(filepath.Join(root, "_apex", "state", "scratch.txt"), []byte("x\n"), 0o644))
	_, recorded, _ := gateJSON(t, root)
	require.True(t, recorded)
	require.Equal(t, 2, gateRuns())

	v1Git(t, root, "commit", "-q", "--allow-empty", "-m", "chore: nothing")
	_, recorded, _ = gateJSON(t, root)
	require.False(t, recorded, "HEAD moved")
	require.Equal(t, 3, gateRuns())
}

// --rerun runs the gate whatever is recorded; its run is then the answer.
func TestGateRecall_RerunFlag(t *testing.T) {
	root, gateRuns, _ := gateRecallRepo(t)
	require.Equal(t, ExitRunFailed, exitOf(gateJSON(t, root)))
	exit, recorded, _ := gateJSON(t, root, "--rerun")
	require.Equal(t, ExitRunFailed, exit)
	require.False(t, recorded)
	require.Equal(t, 3, gateRuns())
	_, recorded, _ = gateJSON(t, root)
	require.True(t, recorded)
	require.Equal(t, 3, gateRuns())
}

// A result ape v1.1 recorded has no tree: it is never returned again.
func TestGateRecall_V11RecordRunsAgain(t *testing.T) {
	root, gateRuns, _ := gateRecallRepo(t)
	require.Equal(t, ExitRunFailed, exitOf(gateJSON(t, root)))
	p, err := apexv1.Open(root)
	require.NoError(t, err)
	r := p.LoadRunResult("M1")
	require.NotNil(t, r)
	require.NotEmpty(t, r.Tree)
	require.True(t, r.Reported)
	r.Tree = ""
	require.NoError(t, p.SaveRunResult(r))
	_, recorded, _ := gateJSON(t, root)
	require.False(t, recorded)
	require.Equal(t, 3, gateRuns())
}

// --spec-only always runs and never replaces the gate's recorded run; a
// full gate call never returns a spec-only run.
func TestGateRecall_ModeMismatchRunsAgain(t *testing.T) {
	root, gateRuns, specRuns := gateRecallRepo(t)
	require.Equal(t, ExitRunFailed, exitOf(gateJSON(t, root)))
	require.Equal(t, 2, gateRuns())
	specs := specRuns()
	_, _, err := v1Exec(t, newMilestoneCmd, "gate", "M1", "--spec-only", "--cwd", root)
	require.NoError(t, err)
	require.Equal(t, specs+1, specRuns(), "spec-only ran")
	_, _, err = v1Exec(t, newMilestoneCmd, "gate", "M1", "--spec-only", "--cwd", root)
	require.NoError(t, err)
	require.Equal(t, specs+2, specRuns(), "and runs every time")
	_, recorded, _ := gateJSON(t, root)
	require.True(t, recorded, "the full gate's run is still the answer")
	require.Equal(t, 2, gateRuns())
	require.Equal(t, specs+2, specRuns())
}

// A person act's run is never a plain gate call's answer: approve's
// self-check is not returned to gate (another verb), and a recorded gate
// run carrying an act is not returned to a call without it.
func TestGateRecall_PersonActOnlyItsOwn(t *testing.T) {
	root, gateRuns, _ := gateRecallRepo(t)
	p, err := apexv1.Open(root)
	require.NoError(t, err)
	require.Equal(t, "milestone approve", p.LoadRunResult("M1").Verb)
	_, recorded, _ := gateJSON(t, root)
	require.False(t, recorded, "approve's self-check is not the gate's run")
	require.Equal(t, 2, gateRuns())

	r := p.LoadRunResult("M1")
	r.PersonAct = &apexv1.PersonAct{Act: "milestone approve", Target: "M1"}
	require.NoError(t, p.SaveRunResult(r))
	_, recorded, _ = gateJSON(t, root)
	require.False(t, recorded)
	require.Equal(t, 3, gateRuns())

	require.True(t, samePersonAct(nil, nil))
	require.False(t, samePersonAct(nil, &apexv1.PersonAct{Act: "x"}))
	require.False(t, samePersonAct(&apexv1.PersonCall{Act: "x"}, nil))
	require.True(t, samePersonAct(&apexv1.PersonCall{Act: "x", Target: "M1"}, &apexv1.PersonAct{Act: "x", Target: "M1"}))
}

// A gate changed since its approval is refused, never answered from the
// record — even when nothing in the tree's digest moved: here the approval
// record (under _apex/state/, left out of it) names another command.
func TestGateRecall_GateChangedStillRefused(t *testing.T) {
	root, gateRuns, _ := gateRecallRepo(t)
	require.Equal(t, ExitRunFailed, exitOf(gateJSON(t, root)))
	recs := filepath.Join(root, "_apex", "state", "milestones.json")
	data, err := os.ReadFile(recs)
	require.NoError(t, err)
	require.Contains(t, string(data), `"gate_cmd": "./gate.sh"`)
	require.NoError(t, os.WriteFile(recs, []byte(strings.Replace(string(data), `"gate_cmd": "./gate.sh"`, `"gate_cmd": "./other.sh"`, 1)), 0o644))
	stdout, _, err := v1Exec(t, newMilestoneCmd, "gate", "M1", "--json", "--cwd", root)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err))
	require.Contains(t, stdout, `"code": "milestone.gate-changed"`)
	require.NotContains(t, stdout, `"recorded"`)
	require.Equal(t, 2, gateRuns())
}

// The JSON a recorded run prints is the run's own with one key added.
func TestWithRecorded(t *testing.T) {
	require.Equal(t, "{\n  \"a\": 1,\n  \"recorded\": true\n}\n", withRecorded("{\n  \"a\": 1\n}\n"))
	require.Equal(t, "not json\n", withRecorded("not json\n"))
}
