package apexv1

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// wavedWith is startedWave under an _apex/config.yaml limits: block.
func wavedWith(t *testing.T, limits string) *Project {
	t.Helper()
	files := baseFiles()
	files["_apex/config.yaml"] = "limits:\n" + limits
	p := fixture(t, files)
	addChange(t, p, "add-wave", waveChange("add-wave"))
	commitAll(t, p, "plan: add-wave")
	_, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	return p
}

func TestLimits_ReplanCaps(t *testing.T) {
	var l Limits
	require.Equal(t, 15, l.AttemptsTotalCap())
	require.Equal(t, 3, l.ReplansCap())
	require.Equal(t, 2, l.ConsecutiveBlocksCap())
	require.NoError(t, yaml.Unmarshal([]byte("attempts_total: 7\nreplans: 0\nconsecutive_blocks: 4\n"), &l))
	require.Equal(t, 7, l.AttemptsTotalCap())
	require.Equal(t, 0, l.ReplansCap())
	require.Equal(t, 4, l.ConsecutiveBlocksCap())
	for doc, want := range map[string]string{
		"attempts_total: 0\n":     "limits.attempts_total: 0 is not a positive whole number",
		"consecutive_blocks: 0\n": "limits.consecutive_blocks: 0 is not a positive whole number",
		"replans: -1\n":           "limits.replans: -1 is not a whole number",
	} {
		require.ErrorContains(t, yaml.Unmarshal([]byte(doc), &Limits{}), want)
	}
}

// H19: re-planning resets the attempts per start, not attempts_total; a
// person's answer restarts it.
func TestLimits_AttemptsTotal(t *testing.T) {
	p := wavedWith(t, "  attempts: 2\n  attempts_total: 3\n  consecutive_blocks: 9\n")
	_, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	_, err = p.Verify(ctx(), "add-wave", RunOptions{})
	require.Equal(t, "change.attempts-exceeded", failCode(t, err))
	_, err = p.Start(ctx(), "add-wave", RunOptions{}) // the orchestrator re-starts
	require.NoError(t, err)

	v, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.Equal(t, CodeAttemptsTotalExceeded, failCode(t, err))
	require.Equal(t, ActionPerson, Diagnostics(err)[0].Action)
	require.Equal(t, StatusBlocked, v.Status)
	st := state(t, p, "add-wave")
	require.Equal(t, StatusBlocked, st.Status)
	require.Contains(t, *st.BlockedReason, "attempts_total exceeded: 3 red verifies across its starts — T1 (exit 127)")

	// A person answers: the count starts over.
	_, err = p.AnswerChange(ctx(), "add-wave", "wave.sh lives in bin/", "ann")
	require.NoError(t, err)
	_, err = p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	v, err = p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, 4, v.AttemptsTotal, "the total is still every red verify")
}

// H19: limits.replans blocks the re-start past it; a person's answer
// restarts the count.
func TestLimits_Replans(t *testing.T) {
	p := wavedWith(t, "  replans: 1\n  consecutive_blocks: 9\n")
	replan := func(n string) {
		write(t, p, "development/changes/add-wave/change.md", strings.Replace(read(t, p, "development/changes/add-wave/change.md"),
			"- T2 copy reads well", "- T"+n+" also "+n+" — regression: true\n- T2 copy reads well", 1))
		commitAll(t, p, "replan "+n)
	}
	replan("3")
	_, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.Len(t, state(t, p, "add-wave").Replans, 1)

	replan("4")
	_, err = p.Start(ctx(), "add-wave", RunOptions{})
	require.Equal(t, CodeReplansExceeded, failCode(t, err))
	require.Equal(t, ActionPerson, Diagnostics(err)[0].Action)
	st := state(t, p, "add-wave")
	require.Equal(t, StatusBlocked, st.Status)
	require.Contains(t, *st.BlockedReason, "replans exceeded: re-plan 2 of at most 1")
	require.Len(t, st.Replans, 1, "the refused re-plan is not recorded")

	_, err = p.AnswerChange(ctx(), "add-wave", "split it", "ann")
	require.NoError(t, err)
	_, err = p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.Len(t, state(t, p, "add-wave").Replans, 2)
}

// H19, D49 Q13: changes blocked in a row are counted per milestone on the
// base branch; at limits.consecutive_blocks change new, change start and
// the orchestrator refuse (action stop) and status reports it; a person's
// re-approval resets it, and so does an archive.
func TestLimits_ConsecutiveBlocks(t *testing.T) {
	p := startedWave(t)
	_, err := p.Block(ctx(), "add-wave", "which wave?")
	require.NoError(t, err)
	require.Equal(t, "apex: M1 consecutive blocks 1 (add-wave)", run(t, p.Root, "log", "-1", "--format=%s", "main"))
	require.Equal(t, "apex: add-wave blocked", subjects(t, p)[0], "the block itself is on the change's branch")
	_, err = p.Block(ctx(), "add-wave", "which wave, really?")
	require.NoError(t, err)
	require.Equal(t, 1, p.consecutiveBlocks(ctx(), "M1"), "a new reason is not a new block")
	ms := p.Status().Milestone
	require.Equal(t, 1, ms.ConsecutiveBlocks)
	require.Equal(t, 2, ms.ConsecutiveBlocksCap)
	require.Empty(t, ms.Diagnostics)

	_, err = p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.Empty(t, state(t, p, "add-wave").Rebases, "the count on main is no move a change catches up with")
	_, err = p.Block(ctx(), "add-wave", "still")
	require.NoError(t, err)
	require.Equal(t, 2, p.consecutiveBlocks(ctx(), "M1"))

	r := p.Status()
	require.Len(t, r.Milestone.Diagnostics, 1)
	require.Equal(t, CodeBlocksExceeded, r.Milestone.Diagnostics[0].Code)
	require.Equal(t, ActionStop, r.Milestone.Diagnostics[0].Action)
	require.Contains(t, r.Next, "stopped: M1 has 2 changes blocked in a row")
	require.Contains(t, r.Brief(), "2 of 2 changes blocked in a row")

	_, err = p.Start(ctx(), "add-wave", RunOptions{})
	require.Equal(t, CodeBlocksExceeded, failCode(t, err))
	require.Equal(t, ActionStop, Diagnostics(err)[0].Action)
	require.Equal(t, CodeBlocksExceeded, failCode(t, p.OrchestratorLimits(ctx(), "")))
	require.Equal(t, CodeBlocksExceeded, failCode(t, p.OrchestratorLimits(ctx(), "M1")))
	require.NoError(t, p.OrchestratorLimits(ctx(), "M2"), "another milestone runs")
	run(t, p.Root, "switch", "-q", "main")
	_, err = p.NewChange(ctx(), "add-bow", "M1", []string{"greeting"}, nil)
	require.Equal(t, CodeBlocksExceeded, failCode(t, err))
	_, err = p.NewChange(ctx(), "fix-typo", MilestoneNone, []string{"greeting"}, nil)
	require.NoError(t, err, "a fix counts toward no milestone")
	require.NoError(t, p.OrchestratorLimits(ctx(), "fix-typo"))

	// A person's re-approval resets the count.
	_, err = p.ApproveMilestone(ctx(), "M1", "ann")
	require.NoError(t, err)
	require.Equal(t, "apex: M1 re-approved", subjects(t, p)[0])
	require.Equal(t, 0, p.consecutiveBlocks(ctx(), "M1"))
	_, err = p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
}

// An archived change of the milestone ends its run of blocks, in the
// archive's one commit.
func TestLimits_ArchiveResetsBlocks(t *testing.T) {
	p := reviewedWave(t)
	_, err := p.Accept(ctx(), "add-wave", "T2", "ann")
	require.NoError(t, err)
	run(t, p.Root, "switch", "-q", "main")
	recs, err := p.LoadMilestoneRecords()
	require.NoError(t, err)
	recs["M1"].ConsecutiveBlocks = 1
	require.NoError(t, p.saveMilestoneRecords(recs))
	commitAll(t, p, "apex: M1 consecutive blocks 1 (other)")
	_, err = p.Archive(ctx(), "add-wave", false, RunOptions{})
	require.NoError(t, err)
	require.Equal(t, 0, p.consecutiveBlocks(ctx(), "M1"))
	require.Contains(t, run(t, p.Root, "show", "--name-only", "--format=", "HEAD"), "_apex/state/milestones.json")
	require.Empty(t, run(t, p.Root, "status", "--porcelain"))
}
