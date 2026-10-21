package apexv1

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Rehearsals 2026-10-20 (D277 item 9): right after `milestone approve`,
// whose self-check (D70 B) had just run the gate red, status said the
// gate was "not run yet". It shows the self-check until the milestone's
// own first gate run, and the JSON carries it.
func TestStatus_JustApprovedShowsTheApprovalSelfCheck(t *testing.T) {
	sc := &GateSummary{SHA: "0123456789abcdef", Exit: 1, At: "2026-10-20T10:00:00Z", Log: "_apex/state/logs/approve-M1.log"}
	m := &MilestoneStatus{ID: "M1", Gate: "./gate.sh", ApprovalSelfCheck: sc}
	require.Contains(t, m.briefLines()[1], "gate `./gate.sh` — not run since approval; the approval's self-check was red (exit 1, as required) at 2026-10-20T10:00:00Z (0123456), log _apex/state/logs/approve-M1.log")
	m.GateLast = &GateRun{Exit: 1, At: "2026-10-20T11:00:00Z", SHA: "fedcba9876543210", Cmd: "./gate.sh"}
	require.NotContains(t, strings.Join(m.briefLines(), "\n"), "since approval", "a run of its own wins")
	m.GateLast, m.ApprovalSelfCheck = nil, nil
	require.Contains(t, strings.Join(m.briefLines(), "\n"), "— not run yet")

	require.Nil(t, approvalSelfCheck(nil))
	require.Nil(t, approvalSelfCheck(&MilestoneRecord{}))
	got := approvalSelfCheck(&MilestoneRecord{Approval: &MilestoneApproval{SelfCheck: &GateSelfCheck{SHA: sc.SHA, Exit: 1, At: sc.At, Log: sc.Log}}})
	require.Equal(t, sc, got)
}
