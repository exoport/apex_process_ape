package apecmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
)

// v1.4.2 A1: orders show / accept / revoke through the CLI: unaccepted until
// accepted; accept and revoke refused in a model session; show --json is
// what the conductor reads.
func TestV1Commands_Orders(t *testing.T) {
	root := v1Repo(t)
	v1Git(t, root, "config", "user.email", "dana@example.com")
	_, _, err := v1Exec(t, newOrdersCmd, "show", "--cwd", root)
	require.Equal(t, 1, exitCodeOf(t, err), "no orders yet")

	path := filepath.Join(root, filepath.FromSlash(apexv1.OrdersRel("dana@example.com")))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("---\nperson: dana@example.com\nacts: [answer, milestone-approve]\n---\nRelay pricing questions to me.\n"), 0o644))
	v1Git(t, root, "add", "-A")
	v1Git(t, root, "commit", "-q", "-m", "my orders")

	show := func() map[string]any {
		t.Helper()
		out, _, err := v1Exec(t, newOrdersCmd, "show", "--json", "--cwd", root)
		require.NoError(t, err, out)
		var v struct {
			Orders map[string]any `json:"orders"`
			Tiers  map[string]int `json:"tiers"`
		}
		require.NoError(t, json.Unmarshal([]byte(out), &v), out)
		require.Equal(t, map[string]int{"answer": 1, "milestone-approve": 2}, v.Tiers)
		return v.Orders
	}
	require.Equal(t, "unaccepted", show()["state"])

	t.Setenv("CLAUDECODE", "1")
	_, _, err = v1Exec(t, newOrdersCmd, "accept", "--by", "dana@example.com", "--cwd", root)
	require.Equal(t, ExitUsage, exitCodeOf(t, err), "never from a model session")
	t.Setenv("CLAUDECODE", "")

	out, _, err := v1Exec(t, newOrdersCmd, "accept", "--by", "dana@example.com", "--cwd", root)
	require.NoError(t, err, out)
	require.Contains(t, out, "orders of dana@example.com: accepted")
	require.Equal(t, "accepted", show()["state"])

	out, _, err = v1Exec(t, newOrdersCmd, "revoke", "--by", "dana-example-com", "--cwd", root)
	require.NoError(t, err, out)
	require.Equal(t, "revoked", show()["state"])
	require.Equal(t, "apex: orders revoked by dana-example-com", v1Git(t, root, "log", "-1", "--format=%s"))
}
