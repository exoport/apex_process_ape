package apecmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
)

// v1.4.2 through the CLI, from the person's Claude Code session (the
// conductor): act refuses with the right code and exit, note and log keep
// the holds open, --project is --cwd.
func TestV1Commands_Conductor(t *testing.T) {
	root := v1Repo(t)
	v1Git(t, root, "config", "user.email", "dana@example.com")
	path := filepath.Join(root, filepath.FromSlash(apexv1.OrdersRel("dana@example.com")))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("---\nperson: dana@example.com\nacts: [answer]\n---\n"), 0o644))
	v1Git(t, root, "add", "-A")
	v1Git(t, root, "commit", "-q", "-m", "orders")
	_, _, err := v1Exec(t, newOrdersCmd, "accept", "--by", "dana@example.com", "--cwd", root)
	require.NoError(t, err)
	t.Setenv("CLAUDECODE", "1")

	code := func(args ...string) (int, string) {
		t.Helper()
		out, _, err := v1Exec(t, newConductorCmd, append(args, "--json", "--project", root)...)
		var v struct {
			Diagnostics []struct {
				Code string `json:"code"`
			} `json:"diagnostics"`
		}
		_ = json.Unmarshal([]byte(out), &v)
		c := ""
		if len(v.Diagnostics) > 0 {
			c = v.Diagnostics[0].Code
		}
		return exitCodeOf(t, err), c
	}
	exit, c := code("act", "milestone-approve", "M1")
	require.Equal(t, ExitUsage, exit)
	require.Equal(t, apexv1.CodeOrdersNoReason, c)
	exit, c = code("act", "accept-red", "M1", "--reason", "x")
	require.Equal(t, 1, exit)
	require.Equal(t, apexv1.CodeOrdersNonDelegable, c)
	exit, c = code("act", "milestone-approve", "M1", "--reason", "x")
	require.Equal(t, 1, exit)
	require.Equal(t, apexv1.CodeOrdersNotNamed, c)

	out, _, err := v1Exec(t, newConductorCmd, "note", "--hold", "M2 waits for the pricing call", "--project", root)
	require.NoError(t, err, out)
	require.Contains(t, out, "[1] hold noted")
	out, _, err = v1Exec(t, newConductorCmd, "log", "--open", "--json", "--project", root)
	require.NoError(t, err)
	var l apexv1.DecisionLog
	require.NoError(t, json.Unmarshal([]byte(out), &l), out)
	require.Len(t, l.Entries, 1)
	require.Equal(t, "M2 waits for the pricing call", l.Entries[0].Text)
	_, _, err = v1Exec(t, newConductorCmd, "note", "--resolved", "1", "call done: go", "--project", root)
	require.NoError(t, err)
	out, _, err = v1Exec(t, newConductorCmd, "log", "--open", "--project", root)
	require.NoError(t, err)
	require.Contains(t, out, "nothing")
}
