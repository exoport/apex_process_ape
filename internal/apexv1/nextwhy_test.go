package apexv1

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Q100: every next comes with its reason, one line, and next stays one
// string.
func TestStatus_NextWhy(t *testing.T) {
	m := func(status string) *MilestoneStatus { return &MilestoneStatus{ID: "M1", Status: status} }
	change := func(status string) []ChangeStatus { return []ChangeStatus{{ID: "c1", Milestone: "M1", Status: status}} }
	for name, c := range map[string]struct {
		r    *StatusReport
		want string
	}{
		"uninitialised": {&StatusReport{}, "no APEX v1 layout"},
		"no milestone":  {&StatusReport{Initialized: true}, "has no milestone"},
		"all done":      {&StatusReport{Initialized: true, Milestones: []MilestoneLine{{ID: "M1", Status: MilestoneDone}}}, "every milestone"},
		"proposed":      {&StatusReport{Initialized: true, Milestone: m(MilestoneProposed)}, "until a person approves"},
		"stopped": {&StatusReport{Initialized: true, Milestone: &MilestoneStatus{
			ID: "M1", Status: MilestoneApproved,
			Diagnostics: []Diagnostic{{Code: CodeBlocksExceeded, Message: "m", Fix: "f"}},
		}}, "milestone.blocks-exceeded"},
		"draft":       {&StatusReport{Initialized: true, Milestone: m(MilestoneApproved), Changes: change(StatusDraft)}, "c1 is planned but not started"},
		"in progress": {&StatusReport{Initialized: true, Milestone: m(MilestoneApproved), Changes: change(StatusInProgress)}, "not green yet"},
		"verified":    {&StatusReport{Initialized: true, Milestone: m(MilestoneApproved), Changes: change(StatusVerified)}, "a review pass"},
		"reviewed":    {&StatusReport{Initialized: true, Milestone: m(MilestoneApproved), Changes: change(StatusReviewed)}, "can land"},
		"blocked":     {&StatusReport{Initialized: true, Milestone: m(MilestoneApproved), Changes: change(StatusBlocked)}, "c1 is blocked"},
		"answered": {&StatusReport{
			Initialized: true, Milestone: m(MilestoneApproved),
			Changes: []ChangeStatus{{ID: "c1", Milestone: "M1", Status: StatusAnswered, Answer: &Answer{By: "p"}}},
		}, "a person answered c1"},
		"awaiting a person": {&StatusReport{
			Initialized: true, Milestone: m(MilestoneApproved),
			Changes: []ChangeStatus{{ID: "c1", Milestone: "M1", Status: StatusReviewed, AwaitingHuman: []string{"T2"}}},
		}, "human-review acceptance of c1"},
		"fix first": {&StatusReport{
			Initialized: true, Milestone: m(MilestoneApproved),
			Changes: []ChangeStatus{{ID: "fix-x", Milestone: MilestoneNone, Lane: LaneFix, Status: StatusDraft, Approved: &Approval{By: "p"}}},
		}, "an approved fix runs ahead"},
		"gate green":   {&StatusReport{Initialized: true, Milestone: &MilestoneStatus{ID: "M1", Status: MilestoneApproved, GateLast: &GateRun{Exit: 0, SHA: "abcdef1234"}}}, "last gate run was green"},
		"nothing open": {&StatusReport{Initialized: true, Milestone: m(MilestoneApproved)}, "no change of it is open"},
	} {
		next, why := nextStepWhy(c.r)
		require.Equal(t, nextStep(c.r), next, name)
		require.Contains(t, why, c.want, name)
		require.NotContains(t, why, "\n", "%s: one line", name)
	}
}

// The JSON carries next_why beside next, next still a string.
func TestStatus_NextWhyInJSON(t *testing.T) {
	p := fixture(t, map[string]string{"development/project.md": projectMD, "development/roadmap.md": "# Roadmap\n", "_apex/config.yaml": "project_name: g\n"})
	r := p.Status()
	require.NotEmpty(t, r.NextWhy)
	data, err := json.Marshal(r)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(data, &out))
	require.IsType(t, "", out["next"])
	require.Equal(t, r.NextWhy, out["next_why"])
	require.True(t, strings.Contains(string(data), `"next_why":`))
}
