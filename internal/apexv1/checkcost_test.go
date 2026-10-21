package apexv1

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFirstSimpleCommand(t *testing.T) {
	for cmd, want := range map[string]string{
		"make e2e":                            "make e2e",
		"  make   e2e  && grep -q ok out.txt": "make e2e",
		"./e2e.sh | grep -q Hello":            "./e2e.sh",
		"./e2e.sh || true":                    "./e2e.sh",
		"./e2e.sh |& tee log":                 "./e2e.sh",
		`grep -q "a && b" f && echo`:          `grep -q "a && b" f`,
		"test $(echo a|wc -c) -gt 1 && x":     "test $(echo a|wc -c) -gt 1",
		"a; b":                                "",
		"(cd x && make) && y":                 "",
		"a\nb":                                "",
		`echo "unbalanced`:                    "",
		"":                                    "",
	} {
		require.Equal(t, want, firstSimpleCommand(cmd), cmd)
	}
}

func TestSharedFirstCommands(t *testing.T) {
	pcs := []PlannedCheck{
		{Ref: "a", Cmd: "./e2e.sh && grep -q A out"},
		{Ref: "b", Cmd: "./e2e.sh | grep -q B"},
		{Ref: "c", Cmd: "./other.sh"},
		{Ref: "d", Cmd: "./same.sh && x"},
		{Ref: "e", Cmd: "./same.sh && x"},
	}
	g := sharedFirstCommands(pcs)
	require.Len(t, g, 1, "an identical command runs once: no duplicate")
	require.Equal(t, "./e2e.sh", g[0].first)
	require.Len(t, g[0].checks, 2)
}

const e2eSpec = `# Greeting
Purpose: Say things to people.

### Requirement: End to end
The system SHALL pass end to end.
#### Scenario: hello
- GIVEN x WHEN y THEN z
- check: ./e2e.sh && grep -q Hello development/e2e.out
#### Scenario: bye
- GIVEN x WHEN y THEN z
- check: ./e2e.sh | grep -q Bye
#### Scenario: alone
- GIVEN x WHEN y THEN z
- check: ./greet.sh | grep -q Hello
`

// W2 Part 3: scenario checks sharing a slow first command are a notice;
// the gate keeps the timings in its gitignored record only.
func TestDuplicateRunNotice(t *testing.T) {
	files := baseFiles()
	files["development/specs/greeting/spec.md"] = e2eSpec
	files["e2e.sh"] = "#!/bin/sh\necho Hello Bye > development/e2e.out\ncat development/e2e.out\n"
	files["gate.sh"] = "#!/bin/sh\nexit 0\n"
	p := fixture(t, files)
	dups := func() []Diagnostic {
		var out []Diagnostic
		for _, d := range p.Notices() {
			if d.Code == CodeChecksDuplicateRun {
				out = append(out, d)
			}
		}
		return out
	}
	require.Empty(t, dups(), "no gate ran: nothing measured")

	g, err := p.RunGate(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.True(t, g.Run.Green(), g.Run.Why())
	require.Nil(t, g.Run.ScenarioSecs, "not in the gate's output")
	require.Nil(t, p.LastGateRun("M1").ScenarioSecs, "nor in ape status")
	recs, err := p.LoadMilestoneRecords()
	require.NoError(t, err)
	require.Nil(t, recs["M1"].Since.ScenarioSecs, "nor in milestones.json")
	rec := p.lastGateRecord("M1")
	require.Len(t, rec.ScenarioSecs, 3, "the gitignored record keeps every scenario's time")
	require.Contains(t, rec.ScenarioSecs, "greeting/End to end/hello")
	require.Empty(t, dups(), "fast checks: no notice")

	rec.ScenarioSecs["greeting/End to end/hello"] = 95
	rec.ScenarioSecs["greeting/End to end/bye"] = 90
	data, err := json.Marshal(rec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(p.lastGatePath("M1"), data, 0o644))
	ds := dups()
	require.Len(t, ds, 1)
	d := ds[0]
	require.Equal(t, "replan", d.Action)
	require.Equal(t, SeverityNotice, d.Severity)
	require.Equal(t, "development/specs/greeting/spec.md", d.File)
	require.Equal(t, 6, d.Line)
	require.Contains(t, d.Message, "2 scenario checks start with `./e2e.sh` (greeting/End to end/bye, greeting/End to end/hello), so a gate runs it 2 times: 3m5s of scenario time at M1's last gate")

	rec.ScenarioSecs["greeting/End to end/bye"] = 30
	data, err = json.Marshal(rec)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(p.lastGatePath("M1"), data, 0o644))
	require.Empty(t, dups(), "one of them is fast")
}
