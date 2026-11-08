package apecmd

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/apexv1"
)

// D876: `ape project integration-mode` — the 1.5 migration's command and
// its check (0 applied · 1 not applied) — and status's mode line, printed
// for every project, solo included.
func TestProjectIntegrationMode(t *testing.T) {
	root := v1Repo(t)
	stdout, _, err := v1Exec(t, newStatusCmd, "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, stdout, "\nintegration: solo\n")
	stdout, _, err = v1Exec(t, newProjectCmd, "integration-mode", "--check", "--cwd", root)
	require.NoError(t, err, "solo: nothing to write")
	require.Contains(t, stdout, "nothing left to write")

	writeAndCommit(t, root, "_apex/config.yaml", "project_name: notes\ncoordination:\n  ensemble: shop\n  home: h\n  url: nats://127.0.0.1:1\n  github:\n    repo: shop/notes\n", "chore: join the shop ensemble")
	stdout, _, err = v1Exec(t, newStatusCmd, "--cwd", root)
	require.NoError(t, err)
	require.Contains(t, stdout, "\nintegration: none — the coordination: block names no integration")
	require.Contains(t, stdout, "("+apexv1.CodeCoordIntegration+")")

	stdout, _, err = v1Exec(t, newProjectCmd, "integration-mode", "--check", "--json", "--cwd", root)
	require.Equal(t, 1, exitCodeOf(t, err), "--check: 1 not applied")
	require.Contains(t, stdout, `"`+apexv1.CodeIntegrationModePending+`"`)
	require.Contains(t, stdout, `"integration": "queue"`)

	stdout, _, err = v1Exec(t, newProjectCmd, "integration-mode", "--json", "--cwd", root)
	require.NoError(t, err)
	var res apexv1.IntegrationModeResult
	require.NoError(t, json.Unmarshal([]byte(stdout), &res), stdout)
	require.Equal(t, apexv1.IntegrationQueue, res.Integration)
	require.NotEmpty(t, res.Commit)
	require.Equal(t, apexv1.IntegrationModeSubject, v1Git(t, root, "log", "-1", "--format=%s"))

	stdout, _, err = v1Exec(t, newProjectCmd, "integration-mode", "--check", "--cwd", root)
	require.NoError(t, err, "--check: 0 applied")
	require.Contains(t, stdout, "nothing left to write")
	stdout, _, err = v1Exec(t, newStatusCmd, "--json", "--cwd", root)
	require.NoError(t, err)
	var st struct {
		Mode string `json:"integration_mode"` //nolint:tagliatelle // ape status --json's key
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &st), stdout)
	require.Equal(t, apexv1.ModeQueuePoll, st.Mode)
}
