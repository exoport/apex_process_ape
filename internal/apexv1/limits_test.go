package apexv1

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
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
	require.Nil(t, tl.Check(SessionUse{USD: 19.99}))
	hit := tl.Check(SessionUse{USD: 20})
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
	require.Contains(t, brief, "spent 0 tokens · 0s session time · $30.00 of $30.00 (budget enforced)")
	require.Contains(t, brief, "stopped at budget")
	require.Contains(t, brief, "apex-apply run 20261015-140000-cccc")

	// A gate run keeps the recorded stops.
	write(t, p, "gate.sh", "#!/bin/sh\nexit 1\n")
	commitAll(t, p, "gate")
	_, err = p.ApproveMilestone(ctx(), "M1", "ann", nil) // the new gate file, approved
	require.NoError(t, err)
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

// writeRunUse writes a finished task run manifest with its tokens (the
// four kinds) and its duration.
func writeRunUse(t *testing.T, p *Project, skill, runID, startedAt string, input, output, cacheRead, cacheCreation int, secs float64) {
	t.Helper()
	dir := filepath.Join(runlog.TasksRoot(p.Root), skill, runID)
	write(t, p, p.Rel(filepath.Join(dir, "manifest.yaml")), fmt.Sprintf(
		"run_id: %s\nstarted_at: %s\nstatus: completed\nduration_seconds: %g\ntotals:\n  cost_usd: 1\n  tokens_input: %d\n  tokens_output: %d\n  tokens_cache_read: %d\n  tokens_cache_creation: %d\n",
		runID, startedAt, secs, input, output, cacheRead, cacheCreation))
}

// v1.4.2 D1 L2: Budget-tokens: and Budget-wall: are enforced as Budget:
// is — refused at the start once reached, the running session stopped at
// it with the unit recorded — and the milestone's wall is its runs' summed
// session time. The control: the same use under a USD budget alone is not
// stopped.
func TestTaskLimits_TokensAndWall(t *testing.T) {
	files := baseFiles()
	files["_apex/config.yaml"] = "limits:\n  budget: enforce\n"
	files["development/roadmap.md"] = strings.Replace(roadmapApproved, "Budget: 30\n", "Budget: 30\nBudget-tokens: 1M\nBudget-wall: 2h\n", 1)
	p := fixture(t, files)
	writeRunUse(t, p, "apex-apply", "20261014-120000-aaaa", "2026-10-14T12:00:00Z", 9e6, 0, 0, 0, 9000) // before approval
	writeRunUse(t, p, "apex-apply", "20261015-130000-bbbb", "2026-10-15T13:00:00Z", 100_000, 50_000, 500_000, 50_000, 1800)
	writeRunUse(t, p, "apex-apply", "20261015-140000-cccc", "2026-10-15T14:00:00Z", 0, 0, 0, 0, 1800)

	tl, err := p.TaskLimits("apex-apply")
	require.NoError(t, err)
	require.Equal(t, int64(700_000), tl.SpentTokens, "the four kinds, since the approval")
	require.Equal(t, int64(1_000_000), tl.BudgetTokens)
	require.InDelta(t, 3600, tl.SpentWallSecs, 1e-9, "summed session time, not the calendar")
	require.InDelta(t, 7200, tl.BudgetWallSecs, 1e-9)

	require.Nil(t, tl.Check(SessionUse{Tokens: 299_999, Wall: 59 * time.Minute}))
	hit := tl.Check(SessionUse{Tokens: 300_000})
	require.NotNil(t, hit)
	require.Equal(t, StopBudget, hit.Reason)
	require.Equal(t, UnitTokens, hit.Stop.Unit)
	require.Equal(t, int64(1_000_000), hit.Stop.SpentTokens)
	require.Equal(t, int64(300_000), hit.Stop.SessionTokens)
	require.Contains(t, hit.Message, "M1 token budget reached: 1.0M of 1.0M tokens")
	require.Contains(t, hit.Message, "raises Budget-tokens:")
	hit = tl.Check(SessionUse{Tokens: 1, Wall: time.Hour})
	require.NotNil(t, hit)
	require.Equal(t, UnitWall, hit.Stop.Unit)
	require.Contains(t, hit.Message, "M1 wall budget reached: 2h of 2h")

	r := p.Status()
	require.Equal(t, int64(700_000), r.Milestone.SpentTokens)
	require.Equal(t, int64(200_000), r.Milestone.SpentTokensNonCache)
	require.InDelta(t, 3600, r.Milestone.SpentWallSecs, 1e-9)
	require.Contains(t, r.Brief(), "spent 700k tokens (29% non-cache) of 1.0M · 1h session time of 2h · $2.00 of $30.00 (budget enforced)")

	// A stop records the session's measured tokens; the next start is
	// refused on the tokens.
	hit = tl.Check(SessionUse{Tokens: 300_000})
	hit.Stop.Run = "20261015-140000-cccc"
	require.NoError(t, p.RecordLimitStop("M1", hit.Stop))
	_, err = p.TaskLimits("apex-apply")
	var fe *FailError
	require.ErrorAs(t, err, &fe)
	require.Equal(t, "milestone.budget-exceeded", fe.Code)
	require.Contains(t, fe.Msg, "M1 has used 1.0M of its 1.0M-token budget")
	require.Contains(t, fe.Msg, "raises Budget-tokens:")
	require.Contains(t, p.Status().Brief(), "stopped at token budget")

	// The control: a USD budget alone does not stop on tokens or time.
	write(t, p, "development/roadmap.md", roadmapApproved)
	tl, err = p.TaskLimits("apex-apply")
	require.NoError(t, err)
	require.Nil(t, tl.Check(SessionUse{Tokens: 9e9, Wall: 99 * time.Hour, USD: 1}))
}

// v1.4.2 D1 L3: limits.session caps each session in tokens and wall time,
// with or without a milestone budget; a cap stop is the session's, never
// the milestone's (max_tokens, max_duration).
func TestTaskLimits_SessionCaps(t *testing.T) {
	files := baseFiles()
	files["_apex/config.yaml"] = "limits:\n  session:\n    tokens: 2M\n    wall: 90m\n"
	p := fixture(t, files)
	tl, err := p.TaskLimits("apex-apply")
	require.NoError(t, err)
	require.NotNil(t, tl, "session caps apply with no budget enforced")
	require.Empty(t, tl.Milestone)
	require.Equal(t, SessionCaps{Tokens: 2_000_000, Wall: 90 * time.Minute}, tl.Session)
	require.Nil(t, tl.Check(SessionUse{Tokens: 1_999_999, Wall: 89 * time.Minute, USD: 1e6}))
	hit := tl.Check(SessionUse{Tokens: 2_000_000})
	require.Equal(t, StopMaxTokens, hit.Reason)
	require.Contains(t, hit.Message, "session token cap reached: 2.0M of 2.0M tokens")
	hit = tl.Check(SessionUse{Wall: 90 * time.Minute})
	require.Equal(t, StopMaxDuration, hit.Reason)
	require.Contains(t, hit.Message, "session wall cap reached: 1h30m of 1h30m")

	tl, err = p.TaskLimitsWith(ctx(), "apex-apply", "", SessionCaps{Tokens: 10})
	require.NoError(t, err)
	require.Equal(t, StopMaxTokens, tl.Check(SessionUse{Tokens: 10}).Reason, "--max-tokens over the config")

	for _, bad := range []string{"tokens: lots", "tokens: 0", "wall: soon", "wall: -1h"} {
		var c Config
		require.Error(t, yaml.Unmarshal([]byte("limits:\n  session:\n    "+bad+"\n"), &c), bad)
	}
}

func TestParseTokens(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int64
	}{{"60M", 60e6}, {"1.5G", 1.5e9}, {"500k", 500e3}, {"2000000", 2e6}, {"2_000_000", 2e6}, {"\t20m ", 20e6}, {"3K", 3000}} {
		n, err := ParseTokens(c.in)
		require.NoError(t, err, c.in)
		require.Equal(t, c.want, n, c.in)
	}
	for _, in := range []string{"", "0", "-5M", "1.5", "lots", "M", "1e400"} {
		_, err := ParseTokens(in)
		require.Error(t, err, in)
	}
	doc := "# Roadmap\n\n## M1 — x\nStatus: proposed\nGate: ./g.sh\nGoal: x\n"
	msgs := findingMessages(ValidateRoadmap("r", doc+"Budget-tokens: many\nBudget-wall: soon\n"))
	require.Contains(t, msgs, "Budget-tokens \"many\" is not a token count")
	require.Contains(t, msgs, "Budget-wall \"soon\" is not a duration")
}

// v1.4.2 D1 L2: `ape milestone new --budget-tokens --budget-wall` writes
// the lines before Goal:, and they parse back; a value ape cannot read is
// refused before anything is written.
func TestNewMilestone_BudgetLines(t *testing.T) {
	p := fixture(t, baseFiles())
	id, err := p.NewMilestoneWith(ctx(), "Users", "./gate.sh", "users sign in", MilestoneBudgets{Tokens: "60M", Wall: "4h"})
	require.NoError(t, err)
	m := p.roadmap().Find(id)
	require.NotNil(t, m)
	require.Equal(t, int64(60e6), *m.BudgetTokens)
	require.InDelta(t, 4*3600, *m.BudgetWallSecs, 1e-9)
	require.Nil(t, m.Budget)
	require.Equal(t, "users sign in", m.Goal)
	data, err := os.ReadFile(p.RoadmapMD())
	require.NoError(t, err)
	require.Contains(t, string(data), "Gate: ./gate.sh\nBudget-tokens: 60M\nBudget-wall: 4h\nGoal: users sign in\n")

	head := run(t, p.Root, "rev-parse", "HEAD")
	for _, b := range []MilestoneBudgets{{Tokens: "lots"}, {Wall: "soon"}} {
		_, err := p.NewMilestoneWith(ctx(), "More", "./gate.sh", "", b)
		require.Error(t, err)
	}
	require.Equal(t, head, run(t, p.Root, "rev-parse", "HEAD"), "nothing written")
}
