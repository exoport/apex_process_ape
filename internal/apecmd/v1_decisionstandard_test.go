package apecmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/stretchr/testify/require"
)

// D210: `ape decision standard [--check] [--json]` — the verb the
// framework's seq-08 and each release that changes the standard run.
func TestDecisionStandard_CLI(t *testing.T) {
	root := v1Repo(t)
	stdout, _, err := v1Exec(t, newDecisionCmd, "standard", "--check", "--json", "--cwd", root)
	require.NoError(t, err, stdout)
	var res apexv1.StandardADRResult
	require.NoError(t, json.Unmarshal([]byte(stdout), &res), stdout)
	require.Equal(t, apexv1.StandardKept, res.Action, "init wrote the standard")

	// A release that changes the standard.
	require.NoError(t, os.MkdirAll(filepath.Join(root, "_apex", "standards"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, filepath.FromSlash(apexv1.StandardADRPayload)), []byte(
		"# ADR-0001: Record architecture decisions\nStatus: accepted\nDate: 2026-10-21\n\n## Context\nc\n\n## Decision\nd\n\n## Consequences\nq\n\nEnforced by: ape project check\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "_apex", "framework.yaml"), []byte("framework:\n  version_tag: v1.0.0-rc.9\n"), 0o644))
	v1Git(t, root, "add", "-A")
	v1Git(t, root, "commit", "-q", "-m", "chore(apex): update APEX framework to v1.0.0-rc.9")

	stdout, _, err = v1Exec(t, newDecisionCmd, "standard", "--check", "--json", "--cwd", root)
	require.Equal(t, ExitRunFailed, exitCodeOf(t, err), stdout)
	require.Contains(t, stdout, `"code": "decision.standard-pending"`)
	require.Contains(t, stdout, `"action": "replaced"`)

	stdout, _, err = v1Exec(t, newDecisionCmd, "standard", "--cwd", root)
	require.NoError(t, err, stdout)
	require.Contains(t, stdout, "development/decisions/0001-record-architecture-decisions.md replaced by APEX's standard ADR")
	require.Equal(t, "apex: ADR-0001 standard v1.0.0-rc.9", v1Git(t, root, "log", "-1", "--format=%s"))
	stdout, _, err = v1Exec(t, newDecisionCmd, "standard", "--check", "--cwd", root)
	require.NoError(t, err, stdout)
	require.Contains(t, stdout, "nothing due")
}
