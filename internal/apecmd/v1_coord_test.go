package apecmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
)

// v1.5 OQ5 through the CLI: in a project of an ensemble whose NATS is
// unreachable, change start refuses with coord.nats-unreachable before any
// work, and ape status then shows the paused line (§4.5). The same
// project without the block starts as before (the v1.4-shaped control).
func TestV1Commands_CoordinationNeedsNATS(t *testing.T) {
	root := v1Repo(t)
	head := v1Git(t, root, "rev-parse", "HEAD")
	stdout, _, err := v1Exec(t, newChangeCmd, "start", "add-nothing", "--json", "--cwd", root)
	require.Error(t, err)
	require.NotContains(t, stdout, apexv1.CodeCoordNATSUnreachable, "no block: no coordination gate")

	cfg, err := os.ReadFile(apexv1.ConfigPath(root))
	require.NoError(t, err)
	block := "coordination:\n  ensemble: shop\n  home: git@example.invalid:shop/home.git\n  url: nats://127.0.0.1:1\n"
	require.NoError(t, os.WriteFile(apexv1.ConfigPath(root), append(cfg, []byte(block)...), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "hot"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "hot", "ape-nats.creds"), []byte("unused: nothing listens"), 0o600))

	// D800+: --wait 0 refuses at once; with a --wait, the run waits for
	// NATS within it (in this process here), then refuses the same way.
	start := time.Now()
	_, stderr, err := v1Exec(t, newChangeCmd, "start", "add-nothing", "--json", "--wait", "1s", "--cwd", root)
	require.Equal(t, 1, exitCodeOf(t, err), stderr)
	require.GreaterOrEqual(t, time.Since(start), time.Second, "it waited within --wait")
	require.Contains(t, stderr, "change start: waiting for NATS")
	stdout, _, err = v1Exec(t, newChangeCmd, "start", "add-nothing", "--json", "--wait", "0", "--cwd", root)
	require.Equal(t, 1, exitCodeOf(t, err), stdout)
	var out struct {
		Diagnostics []struct {
			Code   string `json:"code"`
			Action string `json:"action"`
		} `json:"diagnostics"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &out), stdout)
	require.Equal(t, apexv1.CodeCoordNATSUnreachable, out.Diagnostics[0].Code)
	require.Equal(t, "retry", out.Diagnostics[0].Action)
	require.Equal(t, head, v1Git(t, root, "rev-parse", "HEAD"), "nothing done")

	stdout, _, err = v1Exec(t, newStatusCmd, "--json", "--cwd", root)
	require.NoError(t, err, stdout)
	var st struct {
		Coordination string `json:"coordination"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &st), stdout)
	require.Contains(t, st.Coordination, "coordination: paused — NATS unreachable since ")
	stdout, _, err = v1Exec(t, newStatusCmd, "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, stdout, "(in-flight work continues; new claims and merges wait)")
}
