package framework_test

import (
	"testing"

	"github.com/diegosz/apex_process_ape/internal/framework"
	"github.com/stretchr/testify/require"
)

func TestExtensionIDs_HasAllFour(t *testing.T) {
	ids := framework.ExtensionIDs()
	require.ElementsMatch(t, []string{"ext-adrs", "ext-patterns", "ext-capabilities", "ext-features"}, ids)
}

func TestIsKnownExtension(t *testing.T) {
	require.True(t, framework.IsKnownExtension("ext-adrs"))
	require.True(t, framework.IsKnownExtension("ext-features"))
	require.False(t, framework.IsKnownExtension("ext-bogus"))
	require.False(t, framework.IsKnownExtension(""))
}
