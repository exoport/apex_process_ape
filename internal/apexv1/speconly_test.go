package apexv1

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// W2 Part 4: gate --spec-only runs the spec's scenario checks alone — no
// Gate: command, no regression — records nothing, and is allowed on a
// proposed milestone.
func TestRunSpecOnly_RunsTheSpecAloneAndRecordsNothing(t *testing.T) {
	files := suiteFiles()
	files["development/roadmap.md"] = strings.Replace(roadmapApproved, "Status: approved (by eval 2026-10-15T12:00:00Z)", "Status: proposed", 1)
	files["gate.sh"] = "#!/bin/sh\ntouch development/ran-gate\nexit 1\n"
	files["suite.sh"] = "#!/bin/sh\ntouch development/ran-regression\n"
	files["development/specs/greeting/spec.md"] = greetingSpec + `
### Requirement: Say bye
The system SHALL say bye.
#### Scenario: bye
- GIVEN nothing WHEN greet runs THEN it says bye
- check: ./greet.sh | grep -q Bye
`
	p := fixture(t, files)
	head := run(t, p.Root, "rev-parse", "HEAD")
	before, beforeErr := os.ReadFile(p.MilestonesJSON())

	r, err := p.RunSpecOnly(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.True(t, r.SpecOnly)
	require.Equal(t, "spec-only", r.Mode)
	require.Equal(t, 3, r.SpecChecks)
	require.False(t, r.Green())
	require.Len(t, r.SpecFailed, 1)
	require.Equal(t, "greeting/Say bye/bye", r.SpecFailed[0].Task)
	require.Equal(t, "_apex/state/logs/gate-M1-spec", r.Log)
	var fe *FailError
	require.ErrorAs(t, r.Err(), &fe)
	require.Equal(t, CodeGateSpecRed, fe.Code)
	require.Contains(t, fe.Msg, "greeting/Say bye/bye")
	require.Equal(t, "replan", ActionFor(CodeGateSpecRed))
	rec := read(t, p, "_apex/state/logs/gate-M1-spec.json")
	require.Contains(t, rec, `"spec_only": true`, "the log's record")
	require.Contains(t, rec, `"mode": "spec-only"`)
	require.Contains(t, rec, `"task": "greeting/Say bye/bye"`)

	require.NoFileExists(t, p.Root+"/development/ran-gate", "no Gate: command")
	require.NoFileExists(t, p.Root+"/development/ran-regression", "no regression command")
	require.NoFileExists(t, p.lastGatePath("M1"), "no gate-<M>.json")
	after, afterErr := os.ReadFile(p.MilestonesJSON())
	require.Equal(t, beforeErr == nil, afterErr == nil)
	require.Equal(t, before, after, "milestones.json untouched")
	require.Equal(t, head, run(t, p.Root, "rev-parse", "HEAD"), "no commit")
	clean(t, p, "spec-only")

	write(t, p, "greet.sh", "#!/bin/sh\necho Hello Bye $1\n")
	r, err = p.RunSpecOnly(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.True(t, r.Green())
	require.NoError(t, r.Err())
	require.NotNil(t, r.SpecFailed, "spec_failed is [] when green")

	_, err = p.RunSpecOnly(ctx(), "M9", RunOptions{})
	require.ErrorAs(t, err, &fe)
	require.Equal(t, CodeMilestoneNotFound, fe.Code)
}
