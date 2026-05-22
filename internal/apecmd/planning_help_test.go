package apecmd //nolint:testpackage // exercising unexported render helpers

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPlanningDiagram_NoColorIsRaw asserts the colorize=false branch
// matches the raw template byte-for-byte. CI and piped output must
// stay free of ANSI escapes.
func TestPlanningDiagram_NoColorIsRaw(t *testing.T) {
	got := renderPlanningDiagram(false)
	require.Equal(t, planningDiagramRaw, got)
	require.NotContains(t, got, "\x1b[", "no-color output must not carry ANSI escapes")
}

// TestPlanningDiagram_ColorizedWrapsActionsAndAgents asserts the
// colorize=true branch wraps only *action* skill IDs (the node itself,
// left of `←`) in cyan, plus every agent persona in green. Parent IDs
// on the right of `←` stay uncolored so action vs. parent reads at a
// glance.
func TestPlanningDiagram_ColorizedWrapsActionsAndAgents(t *testing.T) {
	got := renderPlanningDiagram(true)
	// Actions in the body — always right after "◉ ".
	require.Contains(t, got, "◉ "+ansiCyan+"PB"+ansiReset, "node ID after ◉ must be cyan")
	require.Contains(t, got, "◉ "+ansiCyan+"CP"+ansiReset)
	require.Contains(t, got, "◉ "+ansiCyan+"SD"+ansiReset)
	// Legend descriptors — the ID is the action label.
	require.Contains(t, got, ansiCyan+"PB"+ansiReset+" product-brief", "legend IDs must be cyan")
	// SD's descriptor contains "dev" — the agent name gets green-wrapped
	// inside it, so check the cyan-wrapped ID + space + descriptor head.
	require.Contains(t, got, ansiCyan+"SD"+ansiReset+" story-batch-")
	// Agent personas wrapped in green.
	require.Contains(t, got, ansiGreen+"analyst"+ansiReset)
	require.Contains(t, got, ansiGreen+"modeler"+ansiReset)
	require.Contains(t, got, ansiGreen+"dev"+ansiReset)
	// Sanity: original layout still readable after stripping escapes.
	stripped := strings.NewReplacer(ansiCyan, "", ansiGreen, "", ansiReset, "").Replace(got)
	require.Equal(t, planningDiagramRaw, stripped,
		"stripping color escapes must reconstitute the raw diagram exactly")
}

// TestPlanningDiagram_ParentIDsStayWhite asserts the action/parent
// distinction: IDs on the right of `←` are parent references and must
// NOT be cyan. The reader uses the absence of color to tell parents
// from the action they fan into.
func TestPlanningDiagram_ParentIDsStayWhite(t *testing.T) {
	got := renderPlanningDiagram(true)
	// "◉ CP←PB" — CP is the action (cyan), PB is the parent (white).
	require.Contains(t, got, "◉ "+ansiCyan+"CP"+ansiReset+"←PB", "parent after ← must stay uncolored")
	// "◉ DA←CA,DM,ES" — DA is the action (cyan); CA, DM, ES are parents.
	require.Contains(t, got, "◉ "+ansiCyan+"DA"+ansiReset+"←CA,DM,ES")
	// "◉ SD←SU,SM" likewise — only SD is the action.
	require.Contains(t, got, "◉ "+ansiCyan+"SD"+ansiReset+"←SU,SM")
	// Defensive: no occurrence of "←<cyan>" anywhere — the regex must
	// never match a parent reference.
	require.NotContains(t, got, "←"+ansiCyan,
		"no skill ID on the right of ← should ever be cyan")
}

// TestPlanningDiagram_LegendDescriptorsIntact asserts the legend's
// hyphenated descriptors aren't corrupted by the action-in-legend
// regex. The regex anchors on `<ID> <lowercase>`, so "screen-inject"
// (where SC isn't followed by a space + lowercase) stays whole.
func TestPlanningDiagram_LegendDescriptorsIntact(t *testing.T) {
	got := renderPlanningDiagram(true)
	require.Contains(t, got, "screen-inject")
	require.Contains(t, got, "event-storming")
	require.Contains(t, got, "story-batch")
}
