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

// D662.1: accept --from shows the parsed orders and the diff, asks y/N, and
// on y writes the file and its acceptance in one commit; a strict-parse
// failure names the line (D662+).
func TestV1Commands_OrdersAcceptFrom(t *testing.T) {
	root := v1Repo(t)
	v1Git(t, root, "config", "user.email", "dana@example.com")
	draft := filepath.Join(root, "_output", "standing-orders-draft.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(draft), 0o755))
	require.NoError(t, os.WriteFile(draft, []byte("---\nperson: dana@example.com\nacts: [answer, milestone-approve]\nconditions:\n  milestone-approve: {max_changes: 4}\n---\nBring pricing to me.\n"), 0o644))
	head := v1Git(t, root, "rev-parse", "HEAD")

	_, stderr, err := v1ExecIn(t, newOrdersCmd, "n\n", "accept", "--by", "dana@example.com", "--from", draft, "--cwd", root)
	require.Equal(t, 1, exitCodeOf(t, err), "declined")
	require.Contains(t, stderr, "milestone-approve    2     yes (named)")
	require.Contains(t, stderr, "at most 4 changes")
	require.Contains(t, stderr, "Free text: 1 line(s)")
	require.Contains(t, stderr, "these would be your first")
	require.Equal(t, head, v1Git(t, root, "rev-parse", "HEAD"), "nothing written")

	out, _, err := v1ExecIn(t, newOrdersCmd, "y\n", "accept", "--by", "dana@example.com", "--from", draft, "--cwd", root)
	require.NoError(t, err, out)
	require.Contains(t, out, "orders of dana@example.com: accepted")
	require.Equal(t, "apex: orders accepted by dana-example-com", v1Git(t, root, "log", "-1", "--format=%s"))
	out, _, err = v1Exec(t, newOrdersCmd, "show", "--json", "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, out, `"state": "accepted"`)

	// A changed draft shows its diff against the orders in force.
	require.NoError(t, os.WriteFile(draft, []byte("---\nperson: dana@example.com\nacts: [answer]\n---\nBring pricing to me.\n"), 0o644))
	_, stderr, err = v1ExecIn(t, newOrdersCmd, "\n", "accept", "--by", "dana@example.com", "--from", draft, "--cwd", root)
	require.Equal(t, 1, exitCodeOf(t, err), "an empty answer is no")
	require.Contains(t, stderr, "-acts: [answer, milestone-approve]")
	require.Contains(t, stderr, "+acts: [answer]")

	require.NoError(t, os.WriteFile(draft, []byte("---\nperson: dana@example.com\nacts: [answer, push]\n---\n"), 0o644))
	_, stderr, err = v1ExecIn(t, newOrdersCmd, "y\n", "accept", "--by", "dana@example.com", "--from", draft, "--cwd", root)
	require.Equal(t, 1, exitCodeOf(t, err))
	require.Contains(t, stderr, "orders line 3: push is never delegated")

	t.Setenv("CLAUDECODE", "1")
	_, _, err = v1ExecIn(t, newOrdersCmd, "y\n", "accept", "--by", "dana@example.com", "--from", draft, "--cwd", root)
	require.Equal(t, ExitUsage, exitCodeOf(t, err), "never from a model session")
}
