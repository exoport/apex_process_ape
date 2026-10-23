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

// gateRecallRepo is v1Repo with M1's gate a script that counts its runs
// in a file outside the repo, sleeps as long as gateSleepFile says, and is
// red unless gateGreenFile is there — none of which
// moves the working tree — approved as such (its self-check counts one),
// and a spec scenario that counts its own runs the same way.
func gateRecallRepo(t *testing.T) (root string, gateRuns, specRuns func() int) {
	t.Helper()
	root = v1Repo(t)
	dir := t.TempDir()
	gateCount, specCount := filepath.Join(dir, "gate.count"), filepath.Join(dir, "spec.count")
	script := "#!/bin/sh\necho run >> '" + gateCount + "'\n" +
		"[ -f '" + gateSleepFile(root) + "' ] && sleep \"$(cat '" + gateSleepFile(root) + "')\"\n" +
		"[ -f '" + gateGreenFile(root) + "' ] && { echo green; exit 0; }\necho red\nexit 1\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "gate.sh"), []byte(script), 0o755))
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

// gateSleepFile and gateGreenFile are gateRecallRepo's switches: beside
// the repo, in its test's temp dir, never in its working tree.
func gateSleepFile(root string) string { return root + ".gate-sleep" }
func gateGreenFile(root string) string { return root + ".gate-green" }

// gateGreen turns root's gate green (true) or red.
func gateGreen(t *testing.T, root string, green bool) {
	t.Helper()
	if green {
		require.NoError(t, os.WriteFile(gateGreenFile(root), nil, 0o644))
		return
	}
	require.NoError(t, os.Remove(gateGreenFile(root)))
}

// unprinted marks M1's last run result as no call has printed it: what a
// call whose --wait ran out (exit 75) leaves when the run then ends. The
// detached sequence itself is TestGateRecall_DetachedAttachAfter75.
func unprinted(t *testing.T, root string) {
	t.Helper()
	p, err := apexv1.Open(root)
	require.NoError(t, err)
	r := p.LoadRunResult("M1")
	require.NotNil(t, r)
	require.True(t, r.Reported, "printed by the call that ran it")
	r.Reported = false
	require.NoError(t, p.SaveRunResult(r))
}

// gateCurrent is ape status --json's milestone.gate_last.current.
func gateCurrent(t *testing.T, root string) *bool {
	t.Helper()
	stdout, _, err := v1Exec(t, newStatusCmd, "--json", "--cwd", root)
	require.NoError(t, err)
	var st struct {
		Milestone struct {
			GateLast struct {
				Current *bool `json:"current"`
			} `json:"gate_last"` //nolint:tagliatelle // the v1 --json payloads are snake_case by contract
		} `json:"milestone"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &st), stdout)
	return st.Milestone.GateLast.Current
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

// F1 narrowed (D455), (a): the call that saw only exit 75 gets the
// finished run once — its output, its exit code, "recorded": true and a
// note on stderr — and the gate does not run again. Once printed, the next
// call runs the gate again; status still says the run is current.
func TestGateRecall_UnprintedReturnedOnce(t *testing.T) {
	root, gateRuns, _ := gateRecallRepo(t)
	exit, recorded, _ := gateJSON(t, root)
	require.Equal(t, ExitRunFailed, exit)
	require.False(t, recorded, "a new run")
	require.Equal(t, 2, gateRuns())

	unprinted(t, root)
	exit, recorded, stderr := gateJSON(t, root)
	require.Equal(t, ExitRunFailed, exit, "the recorded run's exit")
	require.True(t, recorded)
	require.Contains(t, stderr, "the finished run's result, not a new run — the next call runs the gate again; `ape milestone gate M1 --rerun` runs it now")
	require.Equal(t, 2, gateRuns(), "the gate did not run again")
	cur := gateCurrent(t, root)
	require.NotNil(t, cur)
	require.True(t, *cur, "the run still describes the tree")

	exit, recorded, _ = gateJSON(t, root)
	require.Equal(t, ExitRunFailed, exit)
	require.False(t, recorded, "returned once: printed now, so the gate runs again")
	require.Equal(t, 3, gateRuns())

	// Human output says it on stderr.
	unprinted(t, root)
	stdout, stderr, err := v1Exec(t, newMilestoneCmd, "gate", "M1", "--cwd", root)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err))
	require.Contains(t, stdout, "M1 gate RED (gate command exit 1)")
	require.Contains(t, stderr, "unchanged since ")
	require.Equal(t, 3, gateRuns())
}

// (b) A second call after a reported red, nothing changed, runs the gate
// again (as v1.1.0 did) — also the atomix M5 shape (D419): the call that
// got the result had piped it through `tail -c`, and re-called to read it.
func TestGateRecall_ReportedRedRunsAgain(t *testing.T) {
	root, gateRuns, _ := gateRecallRepo(t)
	require.Equal(t, ExitRunFailed, exitOf(gateJSON(t, root)))
	require.Equal(t, 2, gateRuns())
	exit, recorded, stderr := gateJSON(t, root)
	require.Equal(t, ExitRunFailed, exit)
	require.False(t, recorded)
	require.NotContains(t, stderr, "unchanged since")
	require.Equal(t, 3, gateRuns(), "a new run")
	cur := gateCurrent(t, root)
	require.NotNil(t, cur)
	require.True(t, *cur)
}

// (c) The flake sequence clears: a red reported to the loop, a check that
// passes now, and the loop's next gate call runs it — green.
func TestGateRecall_FlakeClears(t *testing.T) {
	root, gateRuns, _ := gateRecallRepo(t)
	require.Equal(t, ExitRunFailed, exitOf(gateJSON(t, root)), "red once")
	gateGreen(t, root, true)
	exit, recorded, _ := gateJSON(t, root)
	require.Equal(t, 0, exit, "the re-run is green")
	require.False(t, recorded)
	require.Equal(t, 3, gateRuns())
	require.Equal(t, "apex: M1 gate green", v1Git(t, root, "log", "-1", "--format=%s"))
}

// (d) --rerun runs the gate even when an unprinted result would be
// returned; its own run is printed, so the next call runs again too.
func TestGateRecall_RerunFlag(t *testing.T) {
	root, gateRuns, _ := gateRecallRepo(t)
	require.Equal(t, ExitRunFailed, exitOf(gateJSON(t, root)))
	unprinted(t, root)
	exit, recorded, _ := gateJSON(t, root, "--rerun")
	require.Equal(t, ExitRunFailed, exit)
	require.False(t, recorded)
	require.Equal(t, 3, gateRuns())
	_, recorded, _ = gateJSON(t, root)
	require.False(t, recorded)
	require.Equal(t, 4, gateRuns())
}

// (e) An unprinted result is not returned once anything in the working
// tree outside _apex/state/ changed — a new file here; status says the
// run is not current.
func TestGateRecall_UnprintedTreeChangedRunsAgain(t *testing.T) {
	root, gateRuns, _ := gateRecallRepo(t)
	require.Equal(t, ExitRunFailed, exitOf(gateJSON(t, root)))
	unprinted(t, root)
	require.NoError(t, os.WriteFile(filepath.Join(root, "new.txt"), []byte("x\n"), 0o644))
	cur := gateCurrent(t, root)
	require.NotNil(t, cur)
	require.False(t, *cur)
	_, recorded, _ := gateJSON(t, root)
	require.False(t, recorded)
	require.Equal(t, 3, gateRuns())
}

// The run's own "apex: M1 gate red" commit, and ape's state written since,
// keep an unprinted result returnable; another commit does not.
func TestGateRecall_StateCommitKeepsIt(t *testing.T) {
	root, gateRuns, _ := gateRecallRepo(t)
	before := v1Git(t, root, "rev-parse", "HEAD")
	require.Equal(t, ExitRunFailed, exitOf(gateJSON(t, root)))
	require.NotEqual(t, before, v1Git(t, root, "rev-parse", "HEAD"))
	require.Equal(t, "apex: M1 gate red", v1Git(t, root, "log", "-1", "--format=%s"))
	unprinted(t, root)
	require.NoError(t, os.WriteFile(filepath.Join(root, "_apex", "state", "scratch.txt"), []byte("x\n"), 0o644))
	_, recorded, _ := gateJSON(t, root)
	require.True(t, recorded)
	require.Equal(t, 2, gateRuns())

	unprinted(t, root)
	v1Git(t, root, "commit", "-q", "--allow-empty", "-m", "chore: nothing")
	_, recorded, _ = gateJSON(t, root)
	require.False(t, recorded, "HEAD moved")
	require.Equal(t, 3, gateRuns())
}

// A result ape v1.1 recorded has no tree: unprinted, it is delivered as
// v1.1.0 delivered it (no "recorded"); printed, the gate runs again.
func TestGateRecall_V11Record(t *testing.T) {
	root, gateRuns, _ := gateRecallRepo(t)
	require.Equal(t, ExitRunFailed, exitOf(gateJSON(t, root)))
	p, err := apexv1.Open(root)
	require.NoError(t, err)
	r := p.LoadRunResult("M1")
	require.NotNil(t, r)
	require.NotEmpty(t, r.Tree)
	r.Tree, r.Reported = "", false
	require.NoError(t, p.SaveRunResult(r))
	exit, recorded, stderr := gateJSON(t, root)
	require.Equal(t, ExitRunFailed, exit)
	require.False(t, recorded)
	require.NotContains(t, stderr, "unchanged since")
	require.Equal(t, 2, gateRuns(), "delivered, not run")
	_, recorded, _ = gateJSON(t, root)
	require.False(t, recorded)
	require.Equal(t, 3, gateRuns())
}

// --spec-only always runs and never replaces the gate's recorded run; a
// full gate call never returns a spec-only run.
func TestGateRecall_ModeMismatchRunsAgain(t *testing.T) {
	root, gateRuns, specRuns := gateRecallRepo(t)
	require.Equal(t, ExitRunFailed, exitOf(gateJSON(t, root)))
	require.Equal(t, 2, gateRuns())
	unprinted(t, root)
	specs := specRuns()
	_, _, err := v1Exec(t, newMilestoneCmd, "gate", "M1", "--spec-only", "--cwd", root)
	require.NoError(t, err)
	require.Equal(t, specs+1, specRuns(), "spec-only ran")
	_, _, err = v1Exec(t, newMilestoneCmd, "gate", "M1", "--spec-only", "--cwd", root)
	require.NoError(t, err)
	require.Equal(t, specs+2, specRuns(), "and runs every time")
	_, recorded, _ := gateJSON(t, root)
	require.True(t, recorded, "the full gate's unprinted run is still the answer")
	require.Equal(t, 2, gateRuns())
	require.Equal(t, specs+2, specRuns())
}

// A person act's run is never a plain gate call's answer: approve's
// self-check is not returned to gate (another verb), and an unprinted gate
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
	r.Reported = false
	require.NoError(t, p.SaveRunResult(r))
	_, recorded, _ = gateJSON(t, root)
	require.False(t, recorded)
	require.Equal(t, 3, gateRuns())

	require.True(t, samePersonAct(nil, nil))
	require.False(t, samePersonAct(nil, &apexv1.PersonAct{Act: "x"}))
	require.False(t, samePersonAct(&apexv1.PersonCall{Act: "x"}, nil))
	require.True(t, samePersonAct(&apexv1.PersonCall{Act: "x", Target: "M1"}, &apexv1.PersonAct{Act: "x", Target: "M1"}))
}

// A gate changed since its approval is refused, never answered from an
// unprinted record — even when nothing in the tree's digest moved: here
// the approval record (under _apex/state/, left out of it) names another
// command.
func TestGateRecall_GateChangedStillRefused(t *testing.T) {
	root, gateRuns, _ := gateRecallRepo(t)
	require.Equal(t, ExitRunFailed, exitOf(gateJSON(t, root)))
	unprinted(t, root)
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
