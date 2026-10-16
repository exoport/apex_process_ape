package apexv1

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/stretchr/testify/require"
)

func TestLimits_ConfigDefaultsAndValidation(t *testing.T) {
	p := fixture(t, baseFiles())
	lim := p.limits()
	require.False(t, lim.BudgetEnforced(), "the budget is off by default")
	require.Equal(t, DefaultAttempts, lim.AttemptsCap())

	write(t, p, "_apex/config.yaml", "limits:\n  budget: enforce\n  attempts: 3\n")
	cfg, err := LoadConfig(p.Root)
	require.NoError(t, err)
	require.True(t, cfg.Limits.BudgetEnforced())
	require.Equal(t, 3, cfg.Limits.AttemptsCap())

	write(t, p, "_apex/config.yaml", "limits:\n  budget: off\n")
	cfg, err = LoadConfig(p.Root)
	require.NoError(t, err)
	require.False(t, cfg.Limits.BudgetEnforced())
	require.Equal(t, DefaultAttempts, cfg.Limits.AttemptsCap())

	for body, msg := range map[string]string{
		"limits:\n  budget: yes-please\n": "is not enforce or off",
		"limits:\n  attempts: 0\n":        "is not a positive whole number",
		"limits:\n  attempts: many\n":     "cannot unmarshal",
	} {
		write(t, p, "_apex/config.yaml", body)
		_, err := Open(p.Root)
		require.ErrorContains(t, err, msg, body)
	}
}

// The roadmap's Budget: stays optional: an absent one is no finding, one
// that is not a number still is.
func TestLimits_BudgetLineOptional(t *testing.T) {
	doc := "# Roadmap\n\n## M1 — x\nStatus: proposed\nGate: ./g.sh\nGoal: x\n"
	require.Empty(t, findingMessages(ValidateRoadmap("r", doc)))
	require.Contains(t, findingMessages(ValidateRoadmap("r", doc+"Budget: lots\n")), "not a number")
}

// writeRun writes a finished task run manifest as ape task leaves one.
func writeRun(t *testing.T, p *Project, skill, runID, startedAt string, costUSD float64) {
	t.Helper()
	dir := filepath.Join(runlog.TasksRoot(p.Root), skill, runID)
	write(t, p, p.Rel(filepath.Join(dir, "manifest.yaml")),
		"run_id: "+runID+"\nstarted_at: "+startedAt+"\nstatus: completed\ntotals:\n  cost_usd: "+strconv.FormatFloat(costUSD, 'f', -1, 64)+"\n")
}

func TestTaskLimits_Budget(t *testing.T) {
	p := fixture(t, baseFiles())                                                      // M1 approved 2026-10-15T12:00:00Z, Budget: 30
	writeRun(t, p, "apex-apply", "20261014-120000-aaaa", "2026-10-14T12:00:00Z", 100) // before approval
	writeRun(t, p, "apex-apply", "20261015-130000-bbbb", "2026-10-15T13:00:00Z", 10)

	// Off by default: nothing applies, whatever the spend.
	tl, err := p.TaskLimits("apex-apply")
	require.NoError(t, err)
	require.Nil(t, tl)
	require.False(t, p.Status().Milestone.BudgetEnforced)

	write(t, p, "_apex/config.yaml", "limits:\n  budget: enforce\n")
	tl, err = p.TaskLimits("apex-apply")
	require.NoError(t, err)
	require.NotNil(t, tl)
	require.Equal(t, "M1", tl.Milestone)
	require.InDelta(t, 30, tl.Budget, 1e-9)
	require.InDelta(t, 10, tl.SpentUSD, 1e-9, "only runs started since the approval")
	require.Nil(t, tl.Check(19.99))
	hit := tl.Check(20)
	require.NotNil(t, hit)
	require.Equal(t, StopBudget, hit.Reason)
	require.InDelta(t, 30, hit.Stop.SpentUSD, 1e-9)
	require.InDelta(t, 20, hit.Stop.SessionUSD, 1e-9)
	require.Contains(t, hit.Message, "M1 budget reached: $30.00 of $30.00")

	// The stopped run's manifest records nothing (its one step never
	// completed); the stop's measured cost is what counts.
	writeRun(t, p, "apex-apply", "20261015-140000-cccc", "2026-10-15T14:00:00Z", 0)
	hit.Stop.Run = "20261015-140000-cccc"
	head := run(t, p.Root, "rev-parse", "HEAD")
	require.NoError(t, p.RecordLimitStop("M1", hit.Stop))
	require.Equal(t, head, run(t, p.Root, "rev-parse", "HEAD"), "a stop commits nothing (D38)")
	require.FileExists(t, p.LimitStopsJSON())
	require.Empty(t, run(t, p.Root, "status", "--porcelain", "--", "_apex/state"), "limit-stops.json is ignored")

	_, err = p.TaskLimits("apex-apply")
	var fe *FailError
	require.ErrorAs(t, err, &fe)
	require.Equal(t, "milestone.budget-exceeded", fe.Code)
	require.Contains(t, fe.Msg, "M1 has spent $30.00 of its $30.00 budget")

	// ape status: the spend counts the stop, and the stop is a recorded reason.
	r := p.Status()
	require.InDelta(t, 30, r.Milestone.SpentUSD, 1e-9)
	require.True(t, r.Milestone.BudgetEnforced)
	require.Len(t, r.Milestone.Stops, 1)
	require.Equal(t, StopBudget, r.Milestone.Stops[0].Reason)
	brief := r.Brief()
	require.Contains(t, brief, "spent $30.00 of $30.00 budget (enforced)")
	require.Contains(t, brief, "stopped at budget")
	require.Contains(t, brief, "apex-apply run 20261015-140000-cccc")

	// A gate run keeps the recorded stops.
	write(t, p, "gate.sh", "#!/bin/sh\nexit 1\n")
	commitAll(t, p, "gate")
	_, err = p.RunGate(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	recs, err := p.LoadMilestoneRecords()
	require.NoError(t, err)
	require.Equal(t, GateRed, recs["M1"].Result)
	require.Len(t, p.milestoneStops("M1"), 1)

	// A raised budget lets a session start again.
	write(t, p, "development/roadmap.md", strings.Replace(roadmapApproved, "Budget: 30", "Budget: 50", 1))
	tl, err = p.TaskLimits("apex-apply")
	require.NoError(t, err)
	require.InDelta(t, 30, tl.SpentUSD, 1e-9)
}

// No approved milestone, or no Budget: line: no budget applies.
func TestTaskLimits_NoBudgetApplies(t *testing.T) {
	files := baseFiles()
	files["_apex/config.yaml"] = "limits:\n  budget: enforce\n"
	files["development/roadmap.md"] = strings.Replace(roadmapApproved, "Budget: 30\n", "", 1)
	p := fixture(t, files)
	tl, err := p.TaskLimits("apex-apply")
	require.NoError(t, err)
	require.Nil(t, tl)

	write(t, p, "development/roadmap.md", "# Roadmap\n\n## M1 — x\nStatus: proposed\nGate: ./g.sh\nBudget: 1\nGoal: x\n")
	tl, err = p.TaskLimits("apex-apply")
	require.NoError(t, err)
	require.Nil(t, tl)
}
