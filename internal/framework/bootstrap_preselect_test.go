package framework //nolint:testpackage // white-box: the checklist's selection state is unexported

import (
	"testing"

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
