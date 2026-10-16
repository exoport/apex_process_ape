package apexv1

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Backlog item 10: `ape change diff` shows the specs as archive would
// leave them, writing nothing.
func TestPreviewDelta(t *testing.T) {
	p := fixture(t, baseFiles())
	addChange(t, p, "add-wave", changeDoc("add-wave", "greeting, cli",
		addedWave+"\n### REMOVED [greeting] Requirement: Say name\nReason: unused\n\n"+
			"### ADDED [cli] Requirement: Help\nThe system SHALL print help.\n#### Scenario: h\n- check: ./greet.sh --help\n",
		"- T1 x — check: true\n"))
	res, err := p.PreviewDelta("add-wave")
	require.NoError(t, err)
	require.Len(t, res.Specs, 2)
	require.Equal(t, "cli", res.Specs[0].Capability)
	require.True(t, res.Specs[0].Created)
	require.Contains(t, res.Specs[0].Diff, "--- /dev/null")
	require.Contains(t, res.Specs[0].Diff, "+### Requirement: Help")
	g := res.Specs[1].Diff
	require.Contains(t, g, "--- a/development/specs/greeting/spec.md")
	require.Contains(t, g, "+### Requirement: Wave")
	require.Contains(t, g, "-### Requirement: Say name")
	require.Equal(t, 1, res.Specs[1].Added)
	require.Equal(t, 1, res.Specs[1].Removed)
	require.Equal(t, greetingSpec, read(t, p, "development/specs/greeting/spec.md"), "nothing written")
	require.NoDirExists(t, p.SpecFile("cli")[:len(p.SpecFile("cli"))-len("/spec.md")])

	addChange(t, p, "drop-gone", changeDoc("drop-gone", "greeting", "### REMOVED [greeting] Requirement: Gone\nReason: r\n", "- T1 x — check: true\n"))
	_, err = p.PreviewDelta("drop-gone")
	var ce *ConflictError
	require.ErrorAs(t, err, &ce)

	addChange(t, p, "tidy-up", changeDoc("tidy-up", "greeting", "none — chore", "- T1 x — check: true\n"))
	res, err = p.PreviewDelta("tidy-up")
	require.NoError(t, err)
	require.Empty(t, res.Specs)
	require.True(t, strings.HasPrefix(res.DeltaNone, "chore"))
}
