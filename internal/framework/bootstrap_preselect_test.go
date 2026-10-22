package framework //nolint:testpackage // white-box: the checklist's selection state is unexported

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"
)

// The setup checklist opens on the framework's standard configuration,
// so pressing Enter gives ADRs, capabilities and features — not none.
func TestBootstrapModel_PreselectsStandardExtensions(t *testing.T) {
	m := newBootstrapModel("demo", Extensions)
	var picked []string
	for i, on := range m.selected {
		if on {
			picked = append(picked, m.extensions[i].ID)
		}
	}
	require.Equal(t, []string{"ext-adrs", "ext-capabilities", "ext-features"}, picked)
}

// The form on Bubble Tea v2 (W8b) answers the keys it did on v1: typing
// edits the name, Enter moves to the checklist, ↓ and Space toggle, Enter
// submits; Esc cancels. It draws on the alternate screen, as v1's
// WithAltScreen did.
func TestBootstrapModel_Keys(t *testing.T) {
	m := newBootstrapModel("demo", Extensions)
	require.True(t, m.View().AltScreen)
	require.Contains(t, m.View().Content, "APEX project bootstrap")
	require.Contains(t, m.View().Content, "Project name:")

	press := func(k tea.KeyPressMsg) tea.Cmd {
		_, cmd := m.Update(k)
		return cmd
	}
	press(tea.KeyPressMsg{Code: tea.KeyBackspace})
	press(tea.KeyPressMsg{Code: 'X', Text: "X"})
	press(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.Equal(t, phaseExtensions, m.phase)
	require.Contains(t, m.View().Content, "Project name: demX")
	press(tea.KeyPressMsg{Code: tea.KeyDown})
	press(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	require.NotNil(t, press(tea.KeyPressMsg{Code: tea.KeyEnter}), "Enter on the checklist quits")
	require.True(t, m.submitted)
	want := []string{}
	for i, e := range Extensions {
		if e.Standard != (i == 1) {
			want = append(want, e.ID)
		}
	}
	require.Equal(t, "demX", m.Values().ProjectName)
	require.ElementsMatch(t, want, m.Values().Extensions)

	c := newBootstrapModel("demo", Extensions)
	_, cmd := c.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.NotNil(t, cmd)
	require.True(t, c.cancelled)
}
