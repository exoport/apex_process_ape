package apexv1

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Backlog item 11: a new capability's Purpose comes from a `Purpose:` line
// in its ADDED block — what the capability is for — rather than from the
// change's Why, which describes the work. Why stays the fallback.
func TestNewCapabilityPurpose(t *testing.T) {
	added := "### ADDED [cli] Requirement: Help\nPurpose: Lets people discover the greeter's options.\nThe system SHALL print help.\n#### Scenario: h\n- check: ./greet.sh --help\n"
	p := fixture(t, baseFiles())
	c := ParseChange("", changeDoc("add-cli", "cli", added, "- T1 x — check: true\n"))
	require.Equal(t, "Lets people discover the greeter's options.", c.Deltas[0].Purpose)
	require.NotContains(t, c.Deltas[0].Req.Text, "Purpose:", "the Purpose line is not requirement text")
	updates, conflicts := p.MergeDelta(c)
	require.Empty(t, conflicts)
	require.Contains(t, updates[0].content, "# Cli\nPurpose: Lets people discover the greeter's options.\n")
	require.Empty(t, ValidateSpec(updates[0].Path, updates[0].content, DefaultCaps().Spec))

	noPurpose := ParseChange("", changeDoc("add-cli", "cli",
		"### ADDED [cli] Requirement: Help\nThe system SHALL print help.\n#### Scenario: h\n- check: true\n", "- T1 x — check: true\n"))
	updates, _ = p.MergeDelta(noPurpose)
	require.Contains(t, updates[0].content, "Purpose: Because the greeter must change.", "falls back to Why")

	for name, tc := range map[string]struct{ delta, want string }{
		"existing capability": {"### ADDED [greeting] Requirement: Wave\nPurpose: Greets.\nx\n#### Scenario: w\n- check: true\n", "already has one"},
		"empty":               {"### ADDED [cli] Requirement: Help\nPurpose:\nx\n#### Scenario: h\n- check: true\n", "`Purpose:` is empty"},
	} {
		t.Run(name, func(t *testing.T) {
			p := fixture(t, baseFiles())
			addChange(t, p, "add-x", changeDoc("add-x", "greeting, cli", tc.delta, "- T1 x — check: true\n"))
			require.Contains(t, findingMessages(p.CheckChange("add-x")), tc.want)
		})
	}
}
