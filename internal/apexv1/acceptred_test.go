package apexv1

import (
	"encoding/json"
	"errors"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// H22, D45: a person closes a milestone whose gate is red, recorded in
// milestones.json and on the done line; project check accepts it.
func TestClose_AcceptRed(t *testing.T) {
	p := fixture(t, baseFiles()) // ./gate.sh is missing: the gate is red
	head := HeadSHA(ctx(), p.Root)
	t.Setenv("APE_SESSION", "task/x")
	_, err := p.CloseMilestoneAcceptRed(ctx(), "M1", "known defect", "ann", RunOptions{})
	ue, ok := errors.AsType[*UsageError](err)
	require.True(t, ok, "%v", err)
	require.Equal(t, CodePersonOnly, ue.Code)
	t.Setenv("APE_SESSION", "")
	_, err = p.CloseMilestoneAcceptRed(ctx(), "M1", " ", "ann", RunOptions{})
	require.ErrorContains(t, err, "one-line reason")
	_, err = p.CloseMilestoneAcceptRed(ctx(), "M1", "known defect", "", RunOptions{})
	require.ErrorContains(t, err, "--by")
	require.Equal(t, head, HeadSHA(ctx(), p.Root))

	g, err := p.CloseMilestoneAcceptRed(ctx(), "M1", "ships with the known RF drift", "Ann Lee", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, MilestoneDone, g.Status)
	require.NotNil(t, g.AcceptRed)
	require.Equal(t, "apex: M1 done (accepted red)", subjects(t, p)[0])
	clean(t, p, "close --accept-red")
	m := p.roadmap().Find("M1")
	require.Equal(t, MilestoneDone, m.Status)
	require.Regexp(t, regexp.MustCompile(`^done \(accepted red by Ann Lee \S+ [0-9a-f]{40}\)$`), m.StatusText)
	require.Empty(t, p.Check(), "project check accepts the accepted-red done line")

	recs, err := p.LoadMilestoneRecords()
	require.NoError(t, err)
	a := recs["M1"].AcceptRed
	require.Equal(t, "ships with the known RF drift", a.Reason)
	require.Equal(t, "Ann Lee", a.By)
	require.NotEmpty(t, a.At)
	require.Equal(t, 127, a.Gate.Exit)
	data, err := json.Marshal(recs["M1"])
	require.NoError(t, err)
	require.Contains(t, string(data), `"accept_red":{"reason":"ships with the known RF drift","by":"Ann Lee","at":"`)

	r := p.Status()
	require.Equal(t, "Ann Lee", r.Milestones[0].AcceptRed.By)
	require.Contains(t, r.Brief(), "M1 was closed with its gate red (gate command exit 127), accepted by Ann Lee: ships with the known RF drift")
}

// A green gate under --accept-red closes as usual: nothing to accept.
func TestClose_AcceptRedWithAGreenGate(t *testing.T) {
	files := baseFiles()
	files["gate.sh"] = "#!/bin/sh\nexit 0\n"
	p := fixture(t, files)
	g, err := p.CloseMilestoneAcceptRed(ctx(), "M1", "just in case", "ann", RunOptions{})
	require.NoError(t, err)
	require.Nil(t, g.AcceptRed)
	require.Equal(t, "apex: M1 done", subjects(t, p)[0])
	recs, err := p.LoadMilestoneRecords()
	require.NoError(t, err)
	require.Nil(t, recs["M1"].AcceptRed)
	require.NotContains(t, p.roadmap().Find("M1").StatusText, "accepted red")
}
