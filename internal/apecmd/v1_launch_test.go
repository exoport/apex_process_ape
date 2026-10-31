package apecmd

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
)

// v1.5 stage 1a: withCredsDeny adds the credential's deny rules to a
// session's inline settings, keeping every other key and any deny rule
// already there, once each, and never changing its input.
func TestWithCredsDeny(t *testing.T) {
	got := withCredsDeny(nil)
	require.Equal(t, map[string]any{"permissions": map[string]any{"deny": apexv1.CredsDenyRules}}, got)

	in := map[string]any{
		"enabledPlugins": map[string]any{"x@y": true},
		"permissions":    map[string]any{"allow": []string{"Bash(ls)"}, "deny": []string{"Read(./.env)", apexv1.CredsDenyRules[0]}},
	}
	got = withCredsDeny(in)
	require.Equal(t, map[string]any{"x@y": true}, got["enabledPlugins"])
	perms, ok := got["permissions"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, []string{"Bash(ls)"}, perms["allow"])
	require.Equal(t, append([]string{"Read(./.env)"}, apexv1.CredsDenyRules...), perms["deny"], "kept, then added once each")
	inPerms, ok := in["permissions"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, []string{"Read(./.env)", apexv1.CredsDenyRules[0]}, inPerms["deny"], "the input is unchanged")
}
