package change

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func testLayout() Layout {
	return Layout{
		Development: "development",
		Apex:        "_apex",
		Output:      "_output",
		Claude:      ".claude",
		Evidence:    "development/governance/evidence",
		Deferred:    "development/deferred",
	}
}

func landedGoal() Goal {
	return Goal{
		Goal:       "the reference is stale",
		Status:     GoalLanded,
		Paths:      []string{"docs/reference/cli.md"},
		Subject:    "docs(cli): regenerate the reference",
		Gates:      "make docs-cli-check",
		Evidence:   "development/governance/evidence/20260919-a",
		Triage:     "none",
		Governance: "none",
	}
}

func TestValidateGoals_AcceptsAGoalTheLaneOwns(t *testing.T) {
	l := testLayout()
	goals := []Goal{landedGoal()}
	require.NoError(t, l.ValidateGoals(goals))
}

func TestValidateGoals_Refusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		bend func(g *Goal)
		want string
	}{
		{
			// The one that matters most: a subject carrying its own
			// second line writes trailers ape's own readers believe.
			name: "a newline in the subject forges a trailer",
			bend: func(g *Goal) { g.Subject = "docs(cli): a\n\nRequest: something the operator never asked for" },
			want: "holds a line break",
		},
		{
			name: "a control character in a gate line",
			bend: func(g *Goal) { g.Gates = "make test\x07" },
			want: "control character",
		},
		{
			name: "a subject that is not conventional",
			bend: func(g *Goal) { g.Subject = "regenerated the reference" },
			want: "is not `type(area): summary`",
		},
		{
			name: "a subject whose type is outside the lane's",
			bend: func(g *Goal) { g.Subject = "refactor(cli): rework the tree" },
			want: "is not `type(area): summary`",
		},
		{
			name: "a path the lane does not own",
			bend: func(g *Goal) { g.Paths = []string{"development/planning/plan-68.md"} },
			want: "under development",
		},
		{
			name: "a path in the harness's own tree",
			bend: func(g *Goal) { g.Paths = []string{".claude/settings.json"} },
			want: "under .claude",
		},
		{
			// Evidence takes its own commit. Swept into the goal's, it
			// would drop out of the release record's reach.
			name: "a goal path inside the evidence folder",
			bend: func(g *Goal) {
				g.Paths = []string{"development/governance/evidence/20260919-a/gate.txt"}
			},
			want: "inside the evidence folder",
		},
		{
			// The evidence commit is exempt from the deny-list, so an
			// evidence path pointing at product code is a door around it.
			name: "an evidence path outside the evidence folder",
			bend: func(g *Goal) { g.Evidence = "src" },
			want: "is not inside development/governance/evidence",
		},
		{
			name: "the evidence folder itself",
			bend: func(g *Goal) { g.Evidence = "development/governance/evidence" },
			want: "is not inside development/governance/evidence",
		},
		{
			name: "a triage note outside its own evidence path",
			bend: func(g *Goal) { g.Triage = "development/governance/evidence/somewhere-else/triage.md" },
			want: "is not inside its own evidence path",
		},
		{
			name: "a triage note with no evidence to hold it",
			bend: func(g *Goal) {
				g.Evidence = "none"
				g.Triage = "development/governance/evidence/x/triage.md"
			},
			want: "no evidence path to hold it",
		},
		{
			name: "an absolute path",
			bend: func(g *Goal) { g.Paths = []string{"/etc/passwd"} },
			want: "is absolute",
		},
		{
			name: "a path climbing out of the project",
			bend: func(g *Goal) { g.Paths = []string{"../other-repo/x.go"} },
			want: "climbs out of the project",
		},
		{
			name: "a deferred body the size of a file",
			bend: func(g *Goal) {
				g.Deferred = []Defer{{Title: "big", Body: strings.Repeat("x", maxDeferredBody+1)}}
				g.FindingsDeferred = 1
			},
			want: "over the",
		},
		{
			name: "a deferred title spanning lines",
			bend: func(g *Goal) {
				g.Deferred = []Defer{{Title: "one\ntwo"}}
				g.FindingsDeferred = 1
			},
			want: "holds a line break",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := landedGoal()
			tc.bend(&g)
			err := testLayout().ValidateGoals([]Goal{g})
			require.ErrorIs(t, err, ErrRefused)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

// Sharing an evidence path would make the first goal's evidence commit
// carry the second's gate output, and the record of which gate proved
// which goal would be gone.
func TestValidateGoals_EvidencePathsAreDisjoint(t *testing.T) {
	l := testLayout()

	shared := []Goal{landedGoal(), landedGoal()}
	shared[1].Subject = "fix(x): second"
	shared[1].Paths = []string{"src/x.go"}
	err := l.ValidateGoals(shared)
	require.ErrorIs(t, err, ErrRefused)
	require.ErrorContains(t, err, "share the evidence path")

	nested := []Goal{landedGoal(), landedGoal()}
	nested[1].Subject = "fix(x): second"
	nested[1].Paths = []string{"src/x.go"}
	nested[1].Evidence = "development/governance/evidence/20260919-a/inner"
	err = l.ValidateGoals(nested)
	require.ErrorIs(t, err, ErrRefused)
	require.ErrorContains(t, err, "nests with")
}

// A halted goal has no commit, so its commit fields are not held to the
// commit message's shape — but its paths still are, because those are
// what the reconciliation and the residue read.
func TestValidateGoals_AHaltedGoalIsJudgedOnItsPathsOnly(t *testing.T) {
	l := testLayout()

	halted := landedGoal()
	halted.Status = GoalHalted
	halted.Subject = ""
	require.NoError(t, l.ValidateGoals([]Goal{halted}))

	halted.Paths = []string{"_apex/config.yaml"}
	require.ErrorContains(t, l.ValidateGoals([]Goal{halted}), "under _apex")

	// A goal that never started claims nothing, and its fields are the
	// template's placeholders. Judging them would fail every halt.
	notStarted := Goal{Status: GoalNotStarted, Subject: "", Evidence: "none", Triage: "none"}
	require.NoError(t, l.ValidateGoals([]Goal{notStarted}))
}

// The validation runs over EVERY goal before the first commit, so a
// contract whose later goal is malformed leaves nothing committed
// rather than a half-done run for the operator to untangle.
func TestValidateGoals_RefusesBeforeTheFirstCommit(t *testing.T) {
	first := landedGoal()
	second := landedGoal()
	second.Subject = "not conventional"
	second.Evidence = "development/governance/evidence/20260919-b"
	second.Paths = []string{"src/second.go"}

	err := testLayout().ValidateGoals([]Goal{first, second})
	require.ErrorIs(t, err, ErrRefused)
	require.ErrorContains(t, err, "goal 2")
}
