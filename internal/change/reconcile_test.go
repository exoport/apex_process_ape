package change

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReconcile_ClaimsEveryChangedPath(t *testing.T) {
	l := testLayout()
	goals := []Goal{landedGoal()}
	require.NoError(t, l.ValidateGoals(goals))

	r := l.Reconcile([]string{
		"docs/reference/cli.md",
		"development/governance/evidence/20260919-a/gate.txt",
	}, goals)

	require.Empty(t, r.Unclaimed)
	require.Empty(t, r.Unmatched)
	require.Equal(t, 1, r.ClaimedBy["docs/reference/cli.md"])
	require.Equal(t, []string{"development/governance/evidence/20260919-a/gate.txt"},
		r.EvidenceFiles[1], "the evidence commit carries what landed under the evidence path")
	require.NoError(t, r.RefusalError(&Contract{Status: StatusLanded, Goals: goals}))
}

// The failure the step exists to catch: an edit nobody declared, which
// would otherwise ride into a commit under a subject that does not
// describe it.
func TestReconcile_AnUndeclaredEditRefusesTheRun(t *testing.T) {
	l := testLayout()
	goals := []Goal{landedGoal()}
	require.NoError(t, l.ValidateGoals(goals))

	r := l.Reconcile([]string{
		"docs/reference/cli.md",
		"src/quietly-edited.go",
	}, goals)

	require.Equal(t, []string{"src/quietly-edited.go"}, r.Unclaimed)
	err := r.RefusalError(&Contract{Status: StatusLanded, Goals: goals})
	require.ErrorIs(t, err, ErrRefused)
	require.ErrorContains(t, err, "src/quietly-edited.go")
	require.ErrorContains(t, err, "the contract claims:", "both sides are listed, or it is not diagnosable")
	require.ErrorContains(t, err, "docs/reference/cli.md")
}

// A claim covers what lies beneath it, because that is what the
// pathspec ape commits with does. Matching more narrowly would refuse
// runs whose commits would have been exactly right.
func TestReconcile_ADirectoryClaimCoversWhatIsBeneathIt(t *testing.T) {
	l := testLayout()
	g := landedGoal()
	g.Paths = []string{"docs/reference"}
	goals := []Goal{g}
	require.NoError(t, l.ValidateGoals(goals))

	r := l.Reconcile([]string{"docs/reference/cli.md", "docs/reference/config.md"}, goals)
	require.Empty(t, r.Unclaimed)
	require.Equal(t, 1, r.ClaimedBy["docs/reference/config.md"])
}

// Over-declaring is recorded, not refused: the commit ape makes is
// still exactly the set of real changes, and refusing would throw away
// finished work over a list that was too long.
func TestReconcile_AClaimedPathThatDidNotChangeIsRecorded(t *testing.T) {
	l := testLayout()
	g := landedGoal()
	g.Paths = []string{"docs/reference/cli.md", "docs/reference/never-touched.md"}
	goals := []Goal{g}
	require.NoError(t, l.ValidateGoals(goals))

	r := l.Reconcile([]string{"docs/reference/cli.md"}, goals)
	require.Empty(t, r.Unclaimed, "nothing rode along")
	require.Equal(t, []string{
		"development/governance/evidence/20260919-a",
		"docs/reference/never-touched.md",
	}, r.Unmatched[1], "recorded per goal: it is a diagnostic about that goal")
	require.NoError(t, r.RefusalError(&Contract{Status: StatusLanded, Goals: goals}),
		"an over-long claim list is not a refusal")
}

// A halted goal's files are claimed — so they never refuse the run —
// and uncommitted, so they land in the residue.
func TestReconcile_AHaltedGoalClaimsItsPaths(t *testing.T) {
	l := testLayout()
	landed := landedGoal()
	halted := landedGoal()
	halted.Status = GoalHalted
	halted.Subject = ""
	halted.Paths = []string{"src/half-done.go"}
	halted.Evidence = "development/governance/evidence/20260919-b"
	goals := []Goal{landed, halted}
	require.NoError(t, l.ValidateGoals(goals))

	r := l.Reconcile([]string{
		"docs/reference/cli.md",
		"development/governance/evidence/20260919-a/gate.txt",
		"src/half-done.go",
	}, goals)

	require.Empty(t, r.Unclaimed)
	require.Equal(t, 2, r.ClaimedBy["src/half-done.go"])
}
