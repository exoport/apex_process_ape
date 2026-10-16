package apexv1

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Backlog item 3: verify reports the change's own checks that already
// passed at baseline. Regression scenarios are meant to be green and are
// not listed.
func TestVerify_GreenAtBaseline(t *testing.T) {
	p := fixture(t, baseFiles())
	addChange(t, p, "add-wave", changeDoc("add-wave", "greeting", addedWave,
		"- T1 add wave — check: ./wave.sh\n- T2 greet still works — check: ./greet.sh | grep -q Hello\n"))
	commitAll(t, p, "plan")
	_, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	write(t, p, "wave.sh", "#!/bin/sh\necho wave\n")
	commitAll(t, p, "feat: wave")

	v, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.True(t, v.Green)
	require.Len(t, v.GreenAtBaseline, 1, "T1 and the delta scenario were red at baseline; regression scenarios are not listed")
	g := v.GreenAtBaseline[0]
	require.Equal(t, "T2", g.Task)
	require.Equal(t, KindTask, g.Kind)
	require.False(t, g.AtReplan)
	require.Equal(t, v.GreenAtBaseline, state(t, p, "add-wave").Verify.GreenAtBaseline, "recorded for review")

	// A task added at a re-plan, after the code, is green at its baseline
	// and says so.
	doc := read(t, p, "development/changes/add-wave/change.md")
	write(t, p, "development/changes/add-wave/change.md", strings.Replace(doc,
		"- T2 greet still works", "- T3 wave again — check: ./wave.sh && ./wave.sh\n- T2 greet still works", 1))
	commitAll(t, p, "plan: T3")
	_, err = p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	v, err = p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	byTask := map[string]GreenAtBaseline{}
	for _, g := range v.GreenAtBaseline {
		byTask[g.Task] = g
	}
	require.Contains(t, byTask, "T3")
	require.True(t, byTask["T3"].AtReplan)
	require.False(t, byTask["T2"].AtReplan)
	require.NotContains(t, byTask, "T1")
}

// The field is always present, so a consumer reads [] rather than null.
func TestVerify_GreenAtBaselineIsNeverNull(t *testing.T) {
	p := startedWave(t)
	v, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.NotNil(t, v.GreenAtBaseline)
	require.Empty(t, v.GreenAtBaseline)
}
