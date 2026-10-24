package apexv1

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// liveSpec is greetingSpec with a live scenario that leaves a marker (and
// its evidence) under development/ each time it runs.
const liveSpec = greetingSpec + `
### Requirement: Serve
The system SHALL serve greetings over the network.
#### Scenario: served
- GIVEN the service WHEN it is called THEN it greets
- check (live): ./svc.sh
`

func liveFiles() map[string]string {
	files := suiteFiles()
	files["development/specs/greeting/spec.md"] = liveSpec
	files["svc.sh"] = "#!/bin/sh\nmkdir -p development/evidence\necho run >> development/evidence/live.txt\n"
	return files
}

// liveRuns is how many times svc.sh ran.
func liveRuns(t *testing.T, p *Project) int {
	t.Helper()
	data, err := os.ReadFile(p.Root + "/development/evidence/live.txt")
	if os.IsNotExist(err) {
		return 0
	}
	require.NoError(t, err)
	return strings.Count(string(data), "run")
}

// W2 Part 2: `check (live):` parses beside human-review, on a scenario
// and a task; any other kind is check.bad-kind.
func TestLiveCheck_Parse(t *testing.T) {
	s := ParseSpec(liveSpec)
	served := s.Find("Serve").Scenarios[0]
	require.True(t, served.Full())
	require.Equal(t, []string{"./svc.sh"}, served.Checks)
	require.False(t, s.Find("Say hello").Scenarios[0].Full())
	require.Empty(t, findingMessages(ValidateSpec("s.md", liveSpec, 1<<20)))

	codes := func(fs []Finding) []string {
		out := make([]string, 0, len(fs))
		for _, f := range ClassifyFindings(fs) {
			out = append(out, f.Code)
		}
		return out
	}
	for _, bad := range []string{"- check (lvie): ./svc.sh", "- check (Live): ./svc.sh", "- check (): ./svc.sh", "- check (live): human-review"} {
		doc := strings.Replace(liveSpec, "- check (live): ./svc.sh", bad, 1)
		fs := ValidateSpec("s.md", doc, 1<<20)
		require.Equal(t, []string{CodeCheckBadKind}, codes(fs), bad)
	}

	c := ParseChange("c.md", changeDoc("add-svc", "greeting", addedWave,
		"- T1 serve — check (live): ./svc.sh\n- T2 add wave — check: ./wave.sh\n"))
	require.True(t, c.Tasks[0].Full)
	require.True(t, c.Tasks[0].Live, "the deprecated mirror")
	require.Equal(t, "./svc.sh", c.Tasks[0].Check)
	require.Equal(t, "serve", c.Tasks[0].What)
	require.False(t, c.Tasks[1].Full)
	require.False(t, c.Tasks[1].Live)
	require.Empty(t, c.parse)
	for _, bad := range []string{"- T1 serve — check (slow): ./svc.sh\n", "- T1 serve — regression (live): ./svc.sh\n", "- T1 serve — check (live): human-review\n"} {
		c := ParseChange("c.md", changeDoc("add-svc", "greeting", addedWave, bad))
		require.Equal(t, []string{CodeCheckBadKind}, codes(c.parse), bad)
	}
}

// W2 Part 2: verify runs the change's own live checks and the live
// scenarios red at the last gate, and no other.
func TestLiveCheck_VerifyRunsOnlyOwnOrRedAtGate(t *testing.T) {
	p := fixture(t, liveFiles())
	addChange(t, p, "add-wave", waveChange("add-wave"))
	commitAll(t, p, "plan: add-wave")
	checks, skipped := p.changeChecks(p.mustChange(t, "add-wave"))
	require.Equal(t, 1, skipped, "the live scenario of a touched capability, green at no gate")
	for _, pc := range checks {
		require.NotEqual(t, "greeting/Serve/served", pc.Ref)
	}
	_, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	write(t, p, "wave.sh", "#!/bin/sh\necho wave\n")
	commitAll(t, p, "feat: wave")
	v, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.True(t, v.Green)
	require.Equal(t, 1, v.FullSkipped)
	require.Equal(t, 1, v.LiveSkipped, "the deprecated v1.2 name carries the same count")
	require.Equal(t, 1, state(t, p, "add-wave").Verify.FullSkipped)
	require.Equal(t, 1, state(t, p, "add-wave").Verify.LiveSkipped)
	vj, err := json.Marshal(v)
	require.NoError(t, err)
	require.Contains(t, string(vj), `"full_skipped":1`)
	require.Contains(t, string(vj), `"live_skipped":1`)
	raw, err := os.ReadFile(p.StateFile("add-wave"))
	require.NoError(t, err)
	require.Contains(t, string(raw), `"full_skipped": 1`, "state.json records both names")
	require.Contains(t, string(raw), `"live_skipped": 1`)
	require.Zero(t, liveRuns(t, p))

	// Red at the last gate: verify runs it.
	run := GateRun{At: Stamp(), Exit: 0, SpecFailed: []CheckResult{{Task: "greeting/Serve/served"}}}
	data, err := json.Marshal(run)
	require.NoError(t, err)
	write(t, p, "_apex/state/logs/gate-M1.json", string(data))
	checks, skipped = p.changeChecks(p.mustChange(t, "add-wave"))
	require.Zero(t, skipped)
	require.Contains(t, refs(checks), "greeting/Serve/served")

	// The change's own: a live task, and a live scenario of its delta.
	own := changeDoc("add-svc", "greeting",
		"### ADDED [greeting] Requirement: Pong\nThe system SHALL pong.\n#### Scenario: pong\n- GIVEN x WHEN y THEN z\n- check (live): ./pong.sh\n",
		"- T1 serve — check (live): ./svc2.sh\n")
	c := ParseChange("c.md", own)
	require.Empty(t, c.parse)
	checks, _ = p.changeChecks(c)
	require.Contains(t, refs(checks), "T1")
	require.Contains(t, refs(checks), "greeting/Pong/pong")
}

func (p *Project) mustChange(t *testing.T, id string) *Change {
	t.Helper()
	return ParseChange("development/changes/"+id+"/change.md", read(t, p, "development/changes/"+id+"/change.md"))
}

// W2 Part 2: a gate whose Gate: command is red skips the live checks and
// the regression; a green one runs everything, and so does close. A live
// check's evidence under development/ is never undone as a stray (Q-W2c).
func TestLiveCheck_GateSkipsOnARedGateCommand(t *testing.T) {
	files := liveFiles()
	files["gate.sh"] = "#!/bin/sh\n[ -z \"$GATE_RED\" ]\n"
	files["suite.sh"] = "#!/bin/sh\necho suite >> development/evidence/suite.txt\n"
	p := fixture(t, files)
	t.Setenv("GATE_RED", "1")
	g, err := p.RunGate(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.False(t, g.Run.Green())
	require.Equal(t, 1, g.Run.FullSkipped)
	require.Equal(t, 1, g.Run.LiveSkipped, "the deprecated v1.2 name carries the same count")
	require.Equal(t, 2, g.Run.SpecChecks, "the two scenarios that are not full ran")
	require.Equal(t, &RegressionVerdict{Cmd: "./suite.sh", Skipped: RegressionSkippedGateRed}, g.Run.Regression)
	require.Zero(t, liveRuns(t, p))
	require.NoFileExists(t, p.Root+"/development/evidence/suite.txt", "no regression run")
	require.Equal(t, 1, p.LastGateRun("M1").FullSkipped, "ape status carries it")
	require.Equal(t, 1, p.LastGateRun("M1").LiveSkipped)
	out, err := json.Marshal(g)
	require.NoError(t, err)
	require.Contains(t, string(out), `"full_skipped":1`)
	require.Contains(t, string(out), `"live_skipped":1`)
	require.Contains(t, string(out), `"skipped":"gate-red"`)

	t.Setenv("GATE_RED", "")
	g, err = p.RunGate(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.True(t, g.Run.Green(), g.Run.Why())
	require.Zero(t, g.Run.FullSkipped)
	require.Zero(t, g.Run.LiveSkipped)
	require.Equal(t, 3, g.Run.SpecChecks)
	require.Equal(t, 1, liveRuns(t, p))
	require.Empty(t, g.Run.StrayFiles, "evidence under development/ is not a stray (Q-W2c)")
	require.FileExists(t, p.Root+"/development/evidence/live.txt")

	// Close runs everything even with the Gate: command red.
	t.Setenv("GATE_RED", "1")
	_, err = p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.Error(t, err, "red: not closed")
	require.Equal(t, 2, liveRuns(t, p), "close ran the live check")
	require.FileExists(t, p.Root+"/development/evidence/suite.txt", "and the regression")
	require.Zero(t, p.LastGateRun("M1").FullSkipped)

	// --spec-only never runs a live check.
	r, err := p.RunSpecOnly(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, r.FullSkipped)
	require.Equal(t, 1, r.LiveSkipped)
	require.Equal(t, 2, r.SpecChecks)
	require.Equal(t, 2, liveRuns(t, p))
}

// W2 Parts 2, 3: a slow scenario check that is not live is a
// check.full-suggested notice; scenarios sharing a full first command are
// checks.duplicate-run without any timing.
func TestLiveCheck_Notices(t *testing.T) {
	files := liveFiles()
	files["development/specs/greeting/spec.md"] = liveSpec + `#### Scenario: served twice
- GIVEN the service WHEN it is called twice THEN it greets twice
- check (live): ./svc.sh && ./svc.sh
`
	p := fixture(t, files)
	byCode := func(code string) []Diagnostic {
		var out []Diagnostic
		for _, d := range p.Notices() {
			if d.Code == code {
				out = append(out, d)
			}
		}
		return out
	}
	dup := byCode(CodeChecksDuplicateRun)
	require.Len(t, dup, 1, "live: no timing needed")
	require.Contains(t, dup[0].Message, "start with `./svc.sh`")
	require.Contains(t, dup[0].Message, "not timed at a gate yet, full")
	require.Empty(t, byCode(CodeCheckFullSuggested))

	run := GateRun{At: Stamp(), ScenarioSecs: map[string]float64{
		"greeting/Say hello/plain run": 75, "greeting/Say name/named": 10, "greeting/Serve/served": 300,
	}}
	data, err := json.Marshal(run)
	require.NoError(t, err)
	write(t, p, "_apex/state/logs/gate-M1.json", string(data))
	ls := byCode(CodeCheckFullSuggested)
	require.Len(t, ls, 1, "the slow one that is not live")
	require.Equal(t, "development/specs/greeting/spec.md", ls[0].File)
	require.Equal(t, 6, ls[0].Line)
	require.Equal(t, "replan", ls[0].Action)
	require.Contains(t, ls[0].Message, "scenario greeting/Say hello/plain run's check took 1m15s at M1's last gate")
	require.Contains(t, ls[0].Fix, "`- check (full): ./greet.sh | grep -q Hello`")
	// v1.3 (D512): the code is check.full-suggested, its v1.2 name in alias.
	require.Equal(t, "check.full-suggested", ls[0].Code)
	nj, err := json.Marshal(ls[0])
	require.NoError(t, err)
	require.Contains(t, string(nj), `"code":"check.full-suggested"`)
	require.Contains(t, string(nj), `"alias":"check.live-suggested"`)
	require.Equal(t, "; formerly check.live-suggested", FormerlyNote(ls[0].Code))
	require.Equal(t, CodeCheckFullSuggested, CanonicalCode("check.live-suggested"))
}
