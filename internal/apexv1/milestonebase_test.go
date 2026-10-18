package apexv1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// D130 Q60: a milestone's start is recorded at its first approval
// (base_sha), kept by a re-approval, passed to every gate ape runs as
// APE_MILESTONE / APE_MILESTONE_BASE, and shown in ape status --json.
func TestMilestoneBase_RecordedPassedAndShown(t *testing.T) {
	envLog := filepath.Join(t.TempDir(), "env.log")
	files := baseFiles()
	files["_apex/state/milestones.json"] = "{}\n" // approved by Status line only: approve records it
	p := fixture(t, files)
	gate := "#!/bin/sh\necho \"$APE_MILESTONE $APE_MILESTONE_BASE\" >> " + envLog + "\nexit 1\n"
	write(t, p, "gate.sh", gate)
	commitAll(t, p, "test: the gate")
	start := HeadSHA(ctx(), p.Root)
	lines := func() []string {
		data, err := os.ReadFile(envLog)
		require.NoError(t, err)
		return strings.Split(strings.TrimSpace(string(data)), "\n")
	}

	_, err := p.ApproveMilestone(ctx(), "M1", "ann", nil)
	require.NoError(t, err)
	require.Equal(t, []string{"M1 " + start}, lines(), "approve's self-check sees the milestone and its start (HEAD)")
	recs, err := p.LoadMilestoneRecords()
	require.NoError(t, err)
	require.Equal(t, start, recs["M1"].Approval.BaseSHA)
	r := p.Status()
	require.Equal(t, start, r.Milestone.BaseSHA)
	require.Equal(t, start, r.Milestones[0].BaseSHA)

	write(t, p, "work.txt", "w\n")
	commitAll(t, p, "feat: work")
	_, err = p.RunGate(ctx(), "M1", RunOptions{})
	require.NoError(t, err)
	require.Equal(t, "M1 "+start, lines()[1], "a later gate run gets the start, not HEAD")

	// A changed gate is re-approved: the start stays the first approval's.
	write(t, p, "gate.sh", gate+"# changed\n")
	commitAll(t, p, "test: the gate changed")
	_, err = p.ApproveMilestone(ctx(), "M1", "ann", nil)
	require.NoError(t, err)
	recs, err = p.LoadMilestoneRecords()
	require.NoError(t, err)
	require.Equal(t, start, recs["M1"].Approval.BaseSHA, "a re-approval keeps the start")
	require.Equal(t, "M1 "+start, lines()[2])
}

// An approval recorded before rc.5 has no base_sha: the self-check's sha
// (the same HEAD, since rc.3) stands in; before rc.3, none.
func TestMilestoneApproval_Base(t *testing.T) {
	var none *MilestoneApproval
	require.Empty(t, none.Base())
	require.Empty(t, (&MilestoneApproval{}).Base())
	require.Equal(t, "abc", (&MilestoneApproval{SelfCheck: &GateSelfCheck{SHA: "abc"}}).Base())
	require.Equal(t, "def", (&MilestoneApproval{BaseSHA: "def", SelfCheck: &GateSelfCheck{SHA: "abc"}}).Base())
	require.Equal(t, []string{"APE_MILESTONE=M2", "APE_MILESTONE_BASE="}, gateEnv("M2", ""))
}
