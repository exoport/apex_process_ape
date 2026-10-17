package apexv1

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// clean asserts the D11 rule: no v1 command leaves the tree dirty.
func clean(t *testing.T, p *Project, after string) {
	t.Helper()
	require.Empty(t, run(t, p.Root, "status", "--porcelain"), "the tree is dirty after %s", after)
}

func TestMilestoneLifecycle(t *testing.T) {
	files := baseFiles()
	files["development/roadmap.md"] = "# Roadmap\n"
	p := fixture(t, files)

	_, err := p.NewMilestone(ctx(), "Skeleton", "", nil, "")
	require.ErrorContains(t, err, "--gate is required")
	budget := 30.0
	id, err := p.NewMilestone(ctx(), "Skeleton", "./gate.sh", &budget, "A greeter greets.")
	require.NoError(t, err)
	require.Equal(t, "M1", id)
	require.Equal(t, "apex: M1 proposed", subjects(t, p)[0])
	clean(t, p, "milestone new")
	id2, err := p.NewMilestone(ctx(), "Second", "./gate2.sh", nil, "")
	require.NoError(t, err)
	require.Equal(t, "M2", id2)
	rm := p.roadmap()
	require.Equal(t, MilestoneProposed, rm.Find("M1").Status)
	require.InDelta(t, 30, *rm.Find("M1").Budget, 0.001)
	require.Contains(t, findingMessages(ValidateRoadmap("r", read(t, p, "development/roadmap.md"))), "M2 has no `Goal:`",
		"a placeholder goal is not a goal")

	_, err = p.ApproveMilestone(ctx(), "M2", "ann")
	require.ErrorContains(t, err, "not well-formed")
	m, err := p.ApproveMilestone(ctx(), "M1", "ann")
	require.NoError(t, err)
	require.Equal(t, MilestoneApproved, m.Status)
	require.True(t, strings.HasPrefix(m.StatusText, "approved (by ann "))
	require.Equal(t, "apex: M1 approved", subjects(t, p)[0])
	require.Contains(t, run(t, p.Root, "log", "-1", "--format=%b"), "Milestone: M1")
	require.ElementsMatch(t, []string{"development/roadmap.md", "_apex/state/milestones.json"},
		strings.Split(run(t, p.Root, "show", "--name-only", "--format=", "HEAD"), "\n"), "the approval is recorded with the Status line")
	clean(t, p, "milestone approve")
	recs, err := p.LoadMilestoneRecords()
	require.NoError(t, err)
	require.Equal(t, "ann", recs["M1"].Approval.By)
	require.Equal(t, "./gate.sh", recs["M1"].Approval.GateCmd)
	require.Empty(t, recs["M1"].Approval.GateFiles, "gate.sh does not exist yet")
	head := HeadSHA(ctx(), p.Root)
	again, err := p.ApproveMilestone(ctx(), "M1", "bob")
	require.NoError(t, err)
	require.Equal(t, m.StatusText, again.StatusText, "approving twice is a no-op")
	require.Equal(t, head, HeadSHA(ctx(), p.Root))

	// The first gate run records a result and commits it.
	g, err := p.RunGate(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, 127, g.Run.Exit)
	require.True(t, g.ResultChanged)
	require.Equal(t, "apex: M1 gate red", subjects(t, p)[0])
	require.Equal(t, "_apex/state/milestones.json", run(t, p.Root, "show", "--name-only", "--format=", "HEAD"))
	clean(t, p, "the first gate run")
	recs, err = p.LoadMilestoneRecords()
	require.NoError(t, err)
	require.Equal(t, GateRed, recs["M1"].Result)
	require.Equal(t, "ann", recs["M1"].Approval.By, "a gate run keeps the approval")

	// Same result again: nothing committed, nothing dirty, but status
	// sees the newer run.
	head = HeadSHA(ctx(), p.Root)
	g2, err := p.RunGate(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.False(t, g2.ResultChanged)
	require.Equal(t, head, HeadSHA(ctx(), p.Root))
	clean(t, p, "a gate run with an unchanged result")
	require.Equal(t, g2.Run.At, p.LastGateRun("M1").At)

	_, err = p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.ErrorContains(t, err, "gate is red")
	require.Equal(t, MilestoneApproved, p.roadmap().Find("M1").Status)
	require.Equal(t, head, HeadSHA(ctx(), p.Root), "still red: nothing to commit")
	clean(t, p, "a red close")

	// A gate file that appeared since the approval refuses gate and close
	// until a person approves the gate again (H2).
	write(t, p, "gate.sh", "#!/bin/sh\nexit 0\n")
	commitAll(t, p, "feat: gate")
	_, err = p.RunGate(ctx(), "M1", RunOptions{})
	require.ErrorContains(t, err, "M1's gate changed since ann approved it: gate.sh is new")
	_, err = p.CloseMilestone(ctx(), "M1", RunOptions{})
	fe, ok := errors.AsType[*FailError](err)
	require.True(t, ok)
	require.Equal(t, CodeGateChanged, fe.Code)
	require.Contains(t, findingMessages(p.Check()), "M1 gate changed since it was approved (gate.sh is new)")
	statusLine := p.roadmap().Find("M1").StatusText
	_, err = p.ApproveMilestone(ctx(), "M1", "bob")
	require.NoError(t, err)
	require.Equal(t, "apex: M1 re-approved", subjects(t, p)[0])
	require.Equal(t, statusLine, p.roadmap().Find("M1").StatusText, "the Status line keeps the first approval")
	require.NotContains(t, findingMessages(p.Check()), "M1 ")

	// A change in flight blocks the close even with a green gate.
	addChange(t, p, "add-wave", waveChange("add-wave"))
	require.NoError(t, SaveState(p.StateFile("add-wave"), &State{ID: "add-wave", Status: StatusVerified}))
	commitAll(t, p, "wip")
	_, err = p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.ErrorContains(t, err, "changes in flight: add-wave (verified)")

	_, err = p.Block(ctx(), "add-wave", "parked")
	require.NoError(t, err)
	g, err = p.CloseMilestone(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, MilestoneDone, g.Status)
	require.Equal(t, "apex: M1 done", subjects(t, p)[0])
	require.ElementsMatch(t, []string{"development/roadmap.md", "_apex/state/milestones.json"},
		strings.Split(run(t, p.Root, "show", "--name-only", "--format=", "HEAD"), "\n"),
		"the done status and the green result land in one commit")
	clean(t, p, "milestone close")
	done := p.roadmap().Find("M1")
	require.Equal(t, MilestoneDone, done.Status)
	require.Contains(t, done.StatusText, g.Run.SHA)
	require.Equal(t, "M2", p.roadmap().Current().ID)

	_, err = p.ApproveMilestone(ctx(), "M1", "ann")
	require.ErrorContains(t, err, "already done")
}

func TestNewDecision(t *testing.T) {
	p := fixture(t, baseFiles())
	res, err := p.NewDecision(ctx(), "use-sh", "", "")
	require.NoError(t, err)
	require.Equal(t, "ADR-0001", res.ID)
	require.Equal(t, "apex: ADR-0001 proposed", subjects(t, p)[0])
	clean(t, p, "decision new")
	require.Equal(t, "development/decisions/0001-use-sh.md", res.Path)
	require.Empty(t, ValidateDecision(res.Path, "0001-use-sh.md", read(t, p, res.Path)))
	res, err = p.NewDecision(ctx(), "store-in-files", "Store tenants in files", "")
	require.NoError(t, err)
	require.Equal(t, "ADR-0002", res.ID)
	require.Contains(t, read(t, p, res.Path), "# ADR-0002: Store tenants in files")
	_, err = p.NewDecision(ctx(), "use-sh", "", "")
	require.ErrorContains(t, err, "already uses slug")
	_, err = p.NewDecision(ctx(), "Use SH", "", "")
	require.ErrorContains(t, err, "kebab-case")
	require.True(t, p.decisionExists("ADR-0002"))
}
