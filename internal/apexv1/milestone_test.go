package apexv1

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMilestoneLifecycle(t *testing.T) {
	files := baseFiles()
	files["development/roadmap.md"] = "# Roadmap\n"
	p := fixture(t, files)

	_, err := p.NewMilestone("Skeleton", "", nil, "")
	require.ErrorContains(t, err, "--gate is required")
	budget := 30.0
	id, err := p.NewMilestone("Skeleton", "./gate.sh", &budget, "A greeter greets.")
	require.NoError(t, err)
	require.Equal(t, "M1", id)
	id2, err := p.NewMilestone("Second", "./gate2.sh", nil, "")
	require.NoError(t, err)
	require.Equal(t, "M2", id2)
	rm := p.roadmap()
	require.Equal(t, MilestoneProposed, rm.Find("M1").Status)
	require.InDelta(t, 30, *rm.Find("M1").Budget, 0.001)
	require.Contains(t, findingMessages(ValidateRoadmap("r", read(t, p, "development/roadmap.md"))), "M2 has no `Goal:`",
		"a placeholder goal is not a goal")

	_, err = p.ApproveMilestone("M2", "ann")
	require.ErrorContains(t, err, "not well-formed")
	m, err := p.ApproveMilestone("M1", "ann")
	require.NoError(t, err)
	require.Equal(t, MilestoneApproved, m.Status)
	require.True(t, strings.HasPrefix(m.StatusText, "approved (by ann "))
	again, err := p.ApproveMilestone("M1", "bob")
	require.NoError(t, err)
	require.Equal(t, m.StatusText, again.StatusText, "approving twice is a no-op")

	// Gate red: recorded, status unchanged.
	g, err := p.RunGate(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, 127, g.Run.Exit)
	recs, err := p.LoadMilestoneRecords()
	require.NoError(t, err)
	require.Equal(t, 1, recs["M1"].Runs)
	require.Equal(t, 0, recs["M1"].Green)

	_, err = p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.ErrorContains(t, err, "gate is red")
	require.Equal(t, MilestoneApproved, p.roadmap().Find("M1").Status)

	// A change in flight blocks the close even with a green gate.
	write(t, p, "gate.sh", "#!/bin/sh\nexit 0\n")
	addChange(t, p, "add-wave", waveChange("add-wave"))
	require.NoError(t, SaveState(p.StateFile("add-wave"), &State{ID: "add-wave", Status: StatusVerified}))
	_, err = p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.ErrorContains(t, err, "changes in flight: add-wave (verified)")

	require.NoError(t, SaveState(p.StateFile("add-wave"), &State{ID: "add-wave", Status: StatusBlocked}))
	g, err = p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, MilestoneDone, g.Status)
	done := p.roadmap().Find("M1")
	require.Equal(t, MilestoneDone, done.Status)
	require.Contains(t, done.StatusText, g.Run.SHA)
	require.Equal(t, "M2", p.roadmap().Current().ID)

	_, err = p.ApproveMilestone("M1", "ann")
	require.ErrorContains(t, err, "already done")
}

func TestNewDecision(t *testing.T) {
	p := fixture(t, baseFiles())
	res, err := p.NewDecision("use-sh", "")
	require.NoError(t, err)
	require.Equal(t, "ADR-0001", res.ID)
	require.Equal(t, "development/decisions/0001-use-sh.md", res.Path)
	require.Empty(t, ValidateDecision(res.Path, "0001-use-sh.md", read(t, p, res.Path)))
	res, err = p.NewDecision("store-in-files", "Store tenants in files")
	require.NoError(t, err)
	require.Equal(t, "ADR-0002", res.ID)
	require.Contains(t, read(t, p, res.Path), "# ADR-0002: Store tenants in files")
	_, err = p.NewDecision("use-sh", "")
	require.ErrorContains(t, err, "already uses slug")
	_, err = p.NewDecision("Use SH", "")
	require.ErrorContains(t, err, "kebab-case")
	require.True(t, p.decisionExists("ADR-0002"))
}
