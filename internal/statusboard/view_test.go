package statusboard

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/stretchr/testify/require"
)

// sampleReport is a status report with every section the view renders.
func sampleReport() *apexv1.StatusReport {
	budget := 20.0
	return &apexv1.StatusReport{
		Project: "toy", Root: "/srv/toy", Initialized: true, Branch: "main",
		Milestone: &apexv1.MilestoneStatus{
			ID: "M2", Title: "Waves", Status: apexv1.MilestoneApproved, Gate: "./gate.sh", Budget: &budget,
			SpentUSD: 3.5, ConsecutiveBlocks: 2, ConsecutiveBlocksCap: 2,
			GateLast: &apexv1.GateRun{Exit: 1, At: "2026-10-17T09:00:00Z", SHA: "abcdef1234", Cmd: "./gate.sh"},
			Diagnostics: []apexv1.Diagnostic{{
				Severity: "error", Code: "milestone.blocks-exceeded", Action: "stop",
				Message: "M2: 2 changes blocked in a row",
			}},
		},
		Milestones: []apexv1.MilestoneLine{
			{ID: "M1", Title: "Skeleton", Status: apexv1.MilestoneDone, AcceptRed: &apexv1.AcceptRed{By: "diego", Reason: "flaky CI"}},
			{ID: "M2", Title: "Waves", Status: apexv1.MilestoneApproved},
		},
		Changes: []apexv1.ChangeStatus{
			{
				ID: "fix-tz", Lane: apexv1.LaneFix, Status: apexv1.StatusInProgress, Branch: "change/fix-tz",
				Approved: &apexv1.Approval{By: "diego", At: "2026-10-17"}, AwaitingHuman: []string{}, Attempts: 1, AttemptsCap: 5,
			},
			{
				ID: "add-wave", Lane: apexv1.LaneMilestone, Status: apexv1.StatusReviewed, Branch: "change/add-wave",
				AwaitingHuman: []string{"T3"}, Flaky: []string{"T2"},
			},
			{
				ID: "add-bye", Lane: apexv1.LaneMilestone, Status: apexv1.StatusBlocked, BlockedReason: "spec conflict",
				Failing: []string{"T1 (exit 1)"}, AwaitingHuman: []string{},
				Answer: &apexv1.Answer{By: "diego", Text: "drop the farewell"},
			},
			{ID: "fix-csv", Lane: apexv1.LaneFix, Status: apexv1.StatusDraft, AwaitingHuman: []string{}},
		},
		Archived:    3,
		Fixes:       []string{"fix-tz"},
		RoadmapNext: []string{"2026-10-16 the CSV export ignores --tz (from add-wave)"},
		OutsideFlow: []apexv1.OutsideFlow{{SHA: "0123456789abcdef", Subject: "hotfix", Files: []string{"main.go"}}},
		LatestRun:   &apexv1.LatestRun{Skill: "apex-apply", RunID: "20261017-0900", Status: "failed", Stopped: "usage_limit", Resets: "11pm"},
		Next:        "ape task apex-apply --args fix-tz",
		Spend:       apexv1.Spend{TotalUSD: 12.34, Runs: 7},
	}
}

// emptyReport is a project just initialized: every list empty.
func emptyReport() *apexv1.StatusReport {
	return &apexv1.StatusReport{
		Project: "toy", Root: "/srv/toy", Initialized: true,
		Milestones: []apexv1.MilestoneLine{}, Changes: []apexv1.ChangeStatus{}, Fixes: []string{},
		RoadmapNext: []string{}, OutsideFlow: []apexv1.OutsideFlow{},
	}
}

func viewJSON(t *testing.T, r *apexv1.StatusReport) string {
	t.Helper()
	b, err := json.Marshal(buildState(r, time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)))
	require.NoError(t, err)
	return string(b)
}

// The figures on the tab are the figures `ape status --json` reports: each
// section D60 names is rendered where a person reads it.
func TestTheViewShowsEverySection(t *testing.T) {
	t.Parallel()
	v := viewJSON(t, sampleReport())
	for _, want := range []string{
		// header and Next step
		`"title":"APEX — toy"`, `"value":"ape task apex-apply --args fix-tz"`,
		// current milestone: status, gate result, blocks/cap, diagnostics
		`"title":"M2 — Waves"`, `{"label":"gate","tone":"danger","type":"stat","value":"red"}`,
		`"value":"2/2"`, `"label":"blocks in a row / cap","tone":"danger"`, `milestone.blocks-exceeded (stop):`,
		`"value":"$3.50 / $20.00"`, `gate command exit 1`,
		// roadmap with accept_red
		`"title":"Roadmap"`, `"note":"accepted red by diego: flaky CI"`,
		// changes: id, lane, status, branch, awaiting_human, flaky, answer
		`"title":"Changes — 4"`, `"branch":"change/add-wave"`, `"lane":"fix"`, `"person":"T3"`, `"flaky":"T2"`,
		`blocked: spec conflict · failing: T1 (exit 1) · answered by diego: drop the farewell`, `1/5 attempts`,
		// awaiting a person: an unaccepted human-review task, a fix awaiting approval
		`"title":"Awaiting a person — 2"`, "add-wave: accept T3", "fix-csv: a fix awaiting approval",
		// fixes queue, Next, outside_flow, latest_run
		`"title":"Fixes queue — 1"`, `"fix-tz"`,
		`"title":"Next — 1"`, "the CSV export ignores --tz (from add-wave)",
		`"title":"Outside the change flow — 1"`, `"sha":"0123456"`, `"subject":"hotfix"`,
		`"title":"Latest run"`, `"value":"usage_limit"`, `"value":"11pm"`,
		// freshness, bound from data
		`"data":{"refreshed":"2026-10-17T09:00:00Z"}`,
	} {
		require.Contains(t, v, want)
	}
}

// Empty sections are rendered, never hidden: an ABSENT panel cannot be told
// from one that stopped being computed.
func TestAnEmptyProjectRendersEveryPanel(t *testing.T) {
	t.Parallel()
	v := viewJSON(t, emptyReport())
	for _, want := range []string{
		"No current milestone", "No change in flight.", "Nothing waiting on a person.", "No approved fix waiting.",
		"No milestone in roadmap.md.", "Nothing noticed out of scope.", `"title":"Outside the change flow — 0"`,
		"No ape task run yet.",
	} {
		require.Contains(t, v, want)
	}
	require.NotContains(t, v, "null", "an empty list rendered as null")
}

// Colour is a board TOKEN, never a hex: there are two themes and a project
// may patch either.
func TestTheViewUsesColourTokensOnly(t *testing.T) {
	t.Parallel()
	tokens := map[string]bool{"accent": true, "mark": true, "agent": true, "focus": true, "danger": true, "muted": true, "dim": true}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, c := range x {
				if k == "tone" || k == "accent" {
					s, _ := c.(string)
					require.True(t, tokens[s], "%s %q is not a board colour token", k, s)
				}
				walk(c)
			}
		case []any:
			for _, c := range x {
				walk(c)
			}
		}
	}
	for _, r := range []*apexv1.StatusReport{sampleReport(), emptyReport()} {
		state, err := normalized(buildState(r, time.Now()))
		require.NoError(t, err)
		walk(state)
		require.NotContains(t, viewJSON(t, r), `"#`, "a hex colour")
	}
}

// The view is a function of the report: the same report renders the same
// tree (no map order, no clock outside data), or every refresh would look
// like a change and raise the dot.
func TestTheViewIsDeterministic(t *testing.T) {
	t.Parallel()
	r := sampleReport()
	r.Changes = append(r.Changes,
		apexv1.ChangeStatus{ID: "odd-1", Status: "weird", AwaitingHuman: []string{}},
		apexv1.ChangeStatus{ID: "odd-2", Status: "alsoweird", AwaitingHuman: []string{}})
	first := viewJSON(t, r)
	for range 20 {
		require.Equal(t, first, viewJSON(t, r))
	}
	require.Less(t, strings.Index(first, `"label":"alsoweird"`), strings.Index(first, `"label":"weird"`))
}
