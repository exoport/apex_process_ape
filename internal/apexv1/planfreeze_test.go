package apexv1

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Backlog item 1: a MODIFIED block may not drop a scenario or change a
// scenario's check — the living spec would keep "specifying" a behaviour
// while losing its proof.
func TestCheckChange_ScenarioLoss(t *testing.T) {
	plain := "#### Scenario: plain run\n- GIVEN nothing WHEN greet runs THEN it prints Hello\n- check: ./greet.sh | grep -q Hello\n"
	for name, tc := range map[string]struct {
		body string
		want []string
	}{
		"keeps the scenario, adds one": {"The system SHALL print Hello.\n" + plain + "#### Scenario: loud\n- check: true\n", nil},
		"drops the scenario":           {"The system SHALL print Hi.\n#### Scenario: hi\n- check: true\n", []string{`drops scenario "plain run"`}},
		"changes the check": {
			"The system SHALL print Hi.\n#### Scenario: plain run\n- check: ./greet.sh | grep -q Hi\n",
			[]string{"changes the check of scenario \"plain run\" from `./greet.sh | grep -q Hello` to `./greet.sh | grep -q Hi`"},
		},
		"weakens the check to human-review": {
			"The system SHALL print Hi.\n#### Scenario: plain run\n- check: human-review\n",
			[]string{"changes the check of scenario \"plain run\""},
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := fixture(t, baseFiles())
			addChange(t, p, "rework-hello", changeDoc("rework-hello", "greeting",
				"### MODIFIED [greeting] Requirement: Say hello\n"+tc.body, "- T1 x — check: true\n"))
			got := findingMessages(p.CheckChange("rework-hello"))
			if tc.want == nil {
				require.Empty(t, got)
				return
			}
			for _, w := range tc.want {
				require.Contains(t, got, w)
			}
			require.Contains(t, got, "changed behaviour is REMOVED + ADDED under a new name")
		})
	}
}

// The spec moved under a checked change: another change's archive added a
// scenario this MODIFIED block does not carry. Archive refuses with a
// conflict and writes nothing.
func TestMergeDelta_ScenarioAddedUnderneathIsAConflict(t *testing.T) {
	p := fixture(t, baseFiles())
	c := ParseChange("", changeDoc("rework-hello", "greeting",
		"### MODIFIED [greeting] Requirement: Say hello\nThe system SHALL print Hello.\n#### Scenario: plain run\n- check: ./greet.sh | grep -q Hello\n",
		"- T1 x — check: true\n"))
	updates, conflicts := p.MergeDelta(c)
	require.Empty(t, conflicts)
	require.Len(t, updates, 1)

	write(t, p, "development/specs/greeting/spec.md", strings.Replace(greetingSpec,
		"- check: ./greet.sh | grep -q Hello\n",
		"- check: ./greet.sh | grep -q Hello\n#### Scenario: twice\n- check: ./greet.sh; ./greet.sh\n", 1))
	updates, conflicts = p.MergeDelta(c)
	require.Nil(t, updates)
	require.Len(t, conflicts, 1)
	require.Contains(t, conflicts[0], `drops scenario "twice" of the current spec`)
}

// The plan freezes at start: verify and archive refuse while ## Tasks or
// ## Delta differ from it, and a re-start re-baselines, recorded.
func TestPlanDrift(t *testing.T) {
	p := startedWave(t)
	st := state(t, p, "add-wave")
	frozen := st.PlanDigest
	require.NotEmpty(t, frozen)
	firstBaseline := st.Baseline

	// Prose outside the plan may change freely.
	doc := read(t, p, "development/changes/add-wave/change.md")
	write(t, p, "development/changes/add-wave/change.md", strings.Replace(doc, "Because the greeter must change.", "Because waving is friendly.", 1))
	_, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err, "a Why edit is not drift")
	_, err = p.Start(ctx(), "add-wave", RunOptions{})
	require.ErrorContains(t, err, "nothing to re-baseline")

	// Make it verified, then change the plan.
	write(t, p, "wave.sh", "#!/bin/sh\necho wave\n")
	commitAll(t, p, "feat: wave")
	v, err := p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.True(t, v.Green)
	doc = read(t, p, "development/changes/add-wave/change.md")
	write(t, p, "development/changes/add-wave/change.md", strings.Replace(doc,
		"- T2 copy reads well — check: human-review\n",
		"- T2 copy reads well — check: human-review\n- T3 wave twice — check: ./wave.sh && ./wave.sh\n", 1))
	commitAll(t, p, "plan: add T3")

	_, err = p.Verify(ctx(), "add-wave", RunOptions{})
	require.ErrorContains(t, err, "plan drift")
	write(t, p, "development/changes/add-wave/review.md", "ok\n")
	_, err = p.Review(ctx(), "add-wave", "pass", "development/changes/add-wave/review.md")
	require.NoError(t, err, "review records a verdict; archive is the gate")
	_, err = p.Archive(ctx(), "add-wave", true, RunOptions{})
	require.ErrorContains(t, err, "plan drift")

	res, err := p.Start(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.Len(t, res.Baseline, 1, "only the new task is baselined")
	require.Equal(t, "T3", res.Baseline[0].Task)
	st = state(t, p, "add-wave")
	require.Equal(t, StatusInProgress, st.Status)
	require.Nil(t, st.Verify, "a verify of the old plan is dropped")
	require.Nil(t, st.Review, "and so is its review")
	require.Len(t, st.Replans, 1)
	require.Equal(t, frozen, st.Replans[0].From)
	require.Equal(t, st.PlanDigest, st.Replans[0].To)
	require.Equal(t, firstBaseline[0], st.Baseline[0], "T1 keeps the baseline from before any code")
	require.Equal(t, 0, st.Baseline[1].Exit, "T3 baselined at the re-plan, after the code")
	require.Equal(t, st.Replans[0].SHA, st.Baseline[1].SHA)

	v, err = p.Verify(ctx(), "add-wave", RunOptions{})
	require.NoError(t, err)
	require.True(t, v.Green)
}

func TestPlanDigest_IgnoresTrailingWhitespace(t *testing.T) {
	a := ParseChange("", changeDoc("x-y-z", "greeting", "none — chore", "- T1 x — check: true\n"))
	b := ParseChange("", changeDoc("x-y-z", "greeting", "none — chore   ", "- T1 x — check: true\n\n\n"))
	c := ParseChange("", changeDoc("x-y-z", "greeting", "none — chore", "- T1 x — check: false\n"))
	require.Equal(t, a.PlanDigest(), b.PlanDigest())
	require.NotEqual(t, a.PlanDigest(), c.PlanDigest())
}
