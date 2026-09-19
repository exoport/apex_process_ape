package change

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// oneGoal is a minimal coherent contract: one landed goal, counts that
// agree with it. Tests bend one thing at a time away from this.
const oneGoal = `contract_version: '1'
maintenance_status: landed
goals_total: 1
goals_landed: 1
route: 'none'
blocking_condition: 'none'
goals:
  - goal: 'the CLI reference is stale'
    status: landed
    paths:
      - 'docs/reference/cli.md'
    subject: 'docs(cli): regenerate the reference'
    gates: 'make docs-cli-check'
    evidence: 'development/governance/evidence/20260919-a'
    triage: 'none'
    governance: 'none'
    deferred: []
    findings_patched: 0
    findings_deferred: 0
`

func TestParseContract_ReadsTheFrameworksBlock(t *testing.T) {
	c, err := ParseContract([]byte(oneGoal))
	require.NoError(t, err)
	require.Equal(t, StatusLanded, c.Status)
	require.Equal(t, 1, c.GoalsTotal)
	require.Len(t, c.Goals, 1)
	require.Equal(t, []string{"docs/reference/cli.md"}, c.Goals[0].Paths)
	require.Equal(t, "docs(cli): regenerate the reference", c.Goals[0].Subject)
	require.Len(t, c.LandedGoals(), 1)

	// `none` and an empty value mean the same absent thing, and both have
	// to read that way or a route of "none" would print as a route.
	require.True(t, None(c.Route))
	require.True(t, None(c.BlockingCondition))
	require.True(t, None(c.Goals[0].Triage))
}

// The skill is told to write plain YAML. A model writing a file
// sometimes fences it, and that is the one deviation that is
// unambiguous to undo — failing a finished run over three backticks
// would throw away the work and the contract that describes it.
func TestParseContract_ToleratesAFence(t *testing.T) {
	c, err := ParseContract([]byte("```yaml\n" + oneGoal + "```\n"))
	require.NoError(t, err)
	require.Equal(t, StatusLanded, c.Status)
	require.Len(t, c.Goals, 1)
}

func TestParseContract_RefusesAnIncoherentContract(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(string) string
		want string
	}{
		{
			// The skill stops at the first halt. A landed goal after a
			// halted one means a gate ran over partial edits, so reading
			// this as "commit both" would commit work no gate covered.
			name: "a goal lands after a halt",
			edit: func(s string) string {
				return strings.Replace(s, "goals_total: 1\ngoals_landed: 1",
					"goals_total: 2\ngoals_landed: 1", 1) +
					`  - goal: 'second'
    status: landed
    paths: ['a.txt']
    subject: 'fix(a): a'
    gates: 'none'
    evidence: 'none'
    triage: 'none'
    governance: 'none'
    deferred: []
    findings_patched: 0
    findings_deferred: 0
`
			},
			want: "landed after an earlier goal halted",
		},
		{
			name: "goals_total counts something else",
			edit: func(s string) string { return strings.Replace(s, "goals_total: 1", "goals_total: 3", 1) },
			want: "goals_total is 3 and the contract lists 1 goal",
		},
		{
			name: "goals_landed counts something else",
			edit: func(s string) string { return strings.Replace(s, "goals_landed: 1", "goals_landed: 2", 1) },
			want: "goals_landed is 2",
		},
		{
			// ape writes the deferred records from this list. A count that
			// disagrees with it means one of the two is not what the skill
			// meant, and ape cannot tell which.
			name: "findings_deferred disagrees with the list",
			edit: func(s string) string {
				return strings.Replace(s, "findings_deferred: 0", "findings_deferred: 2", 1)
			},
			want: "findings_deferred: 2 and carries 0 deferred findings",
		},
		{
			name: "an unknown outcome",
			edit: func(s string) string {
				return strings.Replace(s, "maintenance_status: landed", "maintenance_status: mostly", 1)
			},
			want: `maintenance_status is "mostly"`,
		},
		{
			name: "landed as a whole with a goal that did not land",
			edit: func(s string) string {
				// goals_landed moves with it, so the count check passes
				// and the only thing left wrong is the outcome.
				s = strings.Replace(s, "goals_landed: 1", "goals_landed: 0", 1)
				return strings.Replace(s, "    status: landed", "    status: halted", 1)
			},
			want: "maintenance_status is landed but 1 of 1 goals did not land",
		},
		{
			name: "no status at all",
			edit: func(s string) string {
				return strings.Replace(s, "maintenance_status: landed\n", "", 1)
			},
			want: "no maintenance_status",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := tc.edit(oneGoal)
			if tc.name == "a goal lands after a halt" {
				src = strings.Replace(src, "    status: landed", "    status: halted", 1)
			}
			_, err := ParseContract([]byte(src))
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}
}

// A halted run is a COHERENT contract, not a broken one: the goals
// before the halt landed, the halt is one goal, and the rest never
// started. ape has commits to compose from it.
func TestParseContract_AHaltIsCoherent(t *testing.T) {
	src := `contract_version: '1'
maintenance_status: halted
goals_total: 3
goals_landed: 1
route: 'none'
blocking_condition: 'finding too large to patch'
goals:
  - goal: 'first'
    status: landed
    paths: ['a.txt']
    subject: 'fix(a): a'
    gates: 'go test ./...'
    evidence: 'evidence/1'
    triage: 'none'
    governance: 'none'
    deferred: []
    findings_patched: 0
    findings_deferred: 0
  - goal: 'second'
    status: halted
    paths: ['b.txt']
    subject: 'fix(b): b'
    gates: 'none'
    evidence: 'evidence/2'
    triage: 'none'
    governance: 'none'
    deferred:
      - title: 'the other half of this'
        anchors: ['b.txt:12']
        owner: 'maintenance'
        trigger: 'when the parser lands'
        body: |
          A body that spans
          more than one line.
    findings_patched: 1
    findings_deferred: 1
  - goal: 'third'
    status: not-started
    paths: []
    subject: ''
    gates: 'none'
    evidence: 'none'
    triage: 'none'
    governance: 'none'
    deferred: []
    findings_patched: 0
    findings_deferred: 0
`
	c, err := ParseContract([]byte(src))
	require.NoError(t, err)
	require.Equal(t, StatusHalted, c.Status)
	require.Len(t, c.LandedGoals(), 1, "only the goals before the halt are ape's to commit")
	require.Equal(t, "finding too large to patch", c.BlockingCondition)
	require.Contains(t, c.Goals[1].Deferred[0].Body, "more than one line",
		"a deferred body may span lines — it is a finding, not a commit field")
}
