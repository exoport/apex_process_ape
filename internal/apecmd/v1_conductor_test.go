package apecmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/natstest"

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

// V8g (FINDING-V15-1C-5, ANSWER-D784 b, c): `ape conductor ack` took a
// token whose $JS a shell had expanded away (.ACK.…) as a watcher id,
// wrote a file for it and exited 0, and nothing was acknowledged. Now a
// stripped token is refused (exit 2) naming the quoting, and the local
// branch acks only an id its watcher recorded; neither writes a file.
// The controls: a recorded watcher id is acknowledged locally, and a
// whole $JS.ACK token goes over NATS.
func TestConductorAck_RefusesAStrippedTokenAndAnUnknownID(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	dir := filepath.Join(state, "ape", "conductor")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "watcher.json"),
		[]byte(`{"cursors":{},"deliveries":[{"id":"notes.42","project":"notes","event":"block","at":"2026-11-03T19:00:00Z"}]}`), 0o600))
	acks := func() []string {
		t.Helper()
		es, _ := os.ReadDir(filepath.Join(dir, "acks"))
		names := make([]string, 0, len(es))
		for _, e := range es {
			names = append(names, e.Name())
		}
		return names
	}

	for _, id := range []string{".ACK.APE_EVENTS.ape-conductor-ann-shop.1.466.27.1762198506000000000.3", "APE_EVENTS.ACK.APE_EVENTS.x.1.2"} {
		_, stderr, err := v1Exec(t, newConductorCmd, "ack", id)
		require.Equal(t, ExitUsage, exitCodeOf(t, err), "%s: refused", id)
		require.Contains(t, err.Error()+stderr, "single quotes")
	}
	_, stderr, err := v1Exec(t, newConductorCmd, "ack", "notes.99")
	require.Equal(t, ExitUsage, exitCodeOf(t, err), "never delivered: refused")
	require.Contains(t, err.Error()+stderr, "no delivery")
	require.Empty(t, acks(), "nothing acknowledged, no file written")

	stdout, _, err := v1Exec(t, newConductorCmd, "ack", "notes.42")
	require.NoError(t, err, "the control: a delivered id")
	require.Contains(t, stdout, "acknowledged notes.42")
	require.Equal(t, []string{"notes.42"}, acks())

	url := natstest.RunServer(t)
	stdout, _, err = v1Exec(t, newConductorCmd, "ack", "$JS.ACK.APE_EVENTS.c.1.466.27.1762198506000000000.3", "--nats-url", url)
	require.NoError(t, err, "the control: a whole token goes over NATS")
	require.Equal(t, "acknowledged\n", stdout)
	require.Equal(t, []string{"notes.42"}, acks(), "the NATS branch writes no file")
}
