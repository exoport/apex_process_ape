package apexv1

import (
	"encoding/json"
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

// F8 (atomix M5's path): a re-approval after a red gate run showed the
// older run, "last red at <old sha>", though the re-approval's self-check
// ran later. The newer of the two is shown.
func TestStatus_ReapprovalShowsNewerSelfCheck(t *testing.T) {
	m := &MilestoneStatus{
		ID: "M1", Gate: "./gate.sh", BaseSHA: "1111111111111111",
		GateLast:          &GateRun{Exit: 1, At: "2026-10-20T11:00:00Z", SHA: "aaaaaaaaaaaaaaaa", Cmd: "./gate.sh"},
		ApprovalSelfCheck: &GateSummary{SHA: "bbbbbbbbbbbbbbbb", Exit: 1, At: "2026-10-20T12:00:00Z", Log: "_apex/state/logs/gate-M1-approve.log"},
	}
	require.True(t, m.SelfCheckLatest())
	require.Equal(t, "  gate `./gate.sh` — re-approved at 2026-10-20T12:00:00Z (bbbbbbb), not run since; its self-check was red (exit 1, as required), log _apex/state/logs/gate-M1-approve.log",
		m.briefLines()[1])
	require.NotContains(t, m.briefLines()[1], "aaaaaaa", "the older run is not the gate's newest result")

	// A gate run after the re-approval wins again.
	m.GateLast.At = "2026-10-20T13:00:00Z"
	require.False(t, m.SelfCheckLatest())
	require.Contains(t, m.briefLines()[1], "last red (gate command exit 1) at 2026-10-20T13:00:00Z (aaaaaaa)")

	// A self-check newer than a green gate run takes the close step back:
	// the re-approved gate is red, so the work is not done.
	m.GateLast = &GateRun{Exit: 0, At: "2026-10-20T11:00:00Z", SHA: "aaaaaaaaaaaaaaaa", Cmd: "./gate.sh"}
	next := nextStep(&StatusReport{Initialized: true, Milestone: &MilestoneStatus{ID: "M1", Status: MilestoneApproved, GateLast: m.GateLast, ApprovalSelfCheck: m.ApprovalSelfCheck}})
	require.NotContains(t, next, "ape milestone close", "the green run is older than the red re-approval")
	require.Contains(t, next, "apex-change")

	// A first approval (self-check at the base) after a gate run of a
	// proposed milestone is no re-approval.
	m.BaseSHA = m.ApprovalSelfCheck.SHA
	require.Contains(t, m.briefLines()[1], "— not run since approval; the approval's self-check was red (exit 1, as required)")
}

// F8: an --accept-green re-approval's self-check is green on purpose
// (Q145); the brief said "was red (exit 0, as required)".
func TestStatus_AcceptGreenSelfCheckText(t *testing.T) {
	ag := &AcceptGreen{Reason: "fixed after the work", By: "diego", At: "2026-10-20T12:00:00Z"}
	m := &MilestoneStatus{
		ID: "M1", Gate: "./gate.sh", BaseSHA: "1111111111111111",
		ApprovalSelfCheck: &GateSummary{SHA: "bbbbbbbbbbbbbbbb", Exit: 0, At: "2026-10-20T12:00:00Z", Log: "_apex/state/logs/gate-M1-approve.log", AcceptGreen: ag},
	}
	line := m.briefLines()[1]
	require.Equal(t, "  gate `./gate.sh` — re-approved at 2026-10-20T12:00:00Z (bbbbbbb), not run since; its self-check was green (exit 0), accepted by diego: \"fixed after the work\", log _apex/state/logs/gate-M1-approve.log", line)
	require.NotContains(t, line, "red")

	// The same after a red gate run older than the re-approval.
	m.GateLast = &GateRun{Exit: 1, At: "2026-10-20T11:00:00Z", SHA: "aaaaaaaaaaaaaaaa", Cmd: "./gate.sh"}
	require.Equal(t, line, m.briefLines()[1])

	// The JSON carries the acceptance beside the self-check (additive).
	rec := &MilestoneRecord{Approval: &MilestoneApproval{SelfCheck: &GateSelfCheck{SHA: "bbbbbbbbbbbbbbbb", Exit: 0, At: ag.At}, AcceptGreen: ag}}
	require.Equal(t, ag, approvalSelfCheck(rec).AcceptGreen)
	b, err := json.Marshal(approvalSelfCheck(rec))
	require.NoError(t, err)
	require.Contains(t, string(b), `"accept_green":{"reason":"fixed after the work","by":"diego","at":"2026-10-20T12:00:00Z"}`)
	b, err = json.Marshal(&GateSummary{SHA: "x", Exit: 1})
	require.NoError(t, err)
	require.NotContains(t, string(b), "accept_green", "absent unless accepted")
}

// F8: after an --accept-green re-approval the next step said to plan a
// change or run the gate, which was already green; close is next.
func TestStatus_NextStepAfterAcceptGreen(t *testing.T) {
	sc := &GateSummary{SHA: "bbbbbbbbbbbbbbbb", Exit: 0, At: "2026-10-20T12:00:00Z", AcceptGreen: &AcceptGreen{Reason: "fixed", By: "diego"}}
	m := &MilestoneStatus{ID: "M1", Status: MilestoneApproved, ApprovalSelfCheck: sc}
	next, why := nextStepWhy(&StatusReport{Initialized: true, Milestone: m})
	require.Equal(t, "M1 gate was green at its re-approval at bbbbbbb, accepted by diego — `ape milestone close M1` (it runs the gate in full)", next)
	require.Contains(t, why, "--accept-green")
	m.GateLast = &GateRun{Exit: 1, At: "2026-10-20T11:00:00Z", SHA: "aaaaaaaaaaaaaaaa"}
	require.Contains(t, nextStep(&StatusReport{Initialized: true, Milestone: m}), "ape milestone close M1", "the older red run does not count")
	// An open change of the milestone still comes first.
	r := &StatusReport{Initialized: true, Milestone: m, Changes: []ChangeStatus{{ID: "c1", Milestone: "M1", Status: StatusInProgress}}}
	require.Contains(t, nextStep(r), "apex-apply")
	// A gate run after the re-approval is the newest result again.
	m.GateLast.At = "2026-10-20T13:00:00Z"
	require.NotContains(t, nextStep(&StatusReport{Initialized: true, Milestone: m}), "ape milestone close")
}
