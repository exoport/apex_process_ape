package apecmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/service"
	"github.com/stretchr/testify/require"
)

// D512: project check's notice for a models.apex-orchestrator row is
// models.orchestrator-row, its former name models.operator-row in alias
// (--json) and beside the code on the human notice line; the exit is 0.
func TestCodeAlias_ProjectCheckNotice(t *testing.T) {
	outsideEverySession(t)
	root := v1Repo(t)
	cfgPath := filepath.Join(root, "_apex", "config.yaml")
	data, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	old := strings.Replace(string(data), "  apex-loop: {model: sonnet, effort: high}", "  apex-orchestrator: {model: haiku, effort: low}", 1)
	require.NotEqual(t, string(data), old)
	require.NoError(t, os.WriteFile(cfgPath, []byte(old), 0o644))
	v1Git(t, root, "commit", "-qam", "rc.6e's row")

	stdout, _, _ := v1Exec(t, newProjectCmd, "check", "--json", "--cwd", root)
	var out struct {
		Notices []map[string]any `json:"notices"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &out), stdout)
	var got map[string]any
	for _, n := range out.Notices {
		if n["code"] == "models.orchestrator-row" {
			got = n
		}
	}
	require.NotNil(t, got, stdout)
	require.Equal(t, "models.operator-row", got["alias"])
	require.Equal(t, "notice", got["severity"])
	require.Equal(t, "person", got["action"])
	for _, n := range out.Notices {
		if n["code"] != "models.orchestrator-row" {
			require.NotContains(t, n, "alias", "only a renamed code carries one")
		}
	}

	_, stderr, _ := v1Exec(t, newProjectCmd, "check", "--cwd", root)
	require.Contains(t, stderr, "(code models.orchestrator-row; formerly models.operator-row (deprecated))")
	for line := range strings.SplitSeq(stderr, "\n") {
		if strings.HasPrefix(line, "notice: ") && !strings.Contains(line, "models.apex-orchestrator") {
			require.NotContains(t, line, "(code ", "a notice of a code never renamed is printed as before")
		}
	}
}

// The service's refusal names the former name too, and submit maps a former
// name an older daemon sends onto the new code's exit.
func TestCodeAlias_ServiceRefusal(t *testing.T) {
	r := &service.RefusalError{Code: apexv1.CodeLoopRunning, Action: apexv1.ActionRetry, Msg: "busy"}
	require.Equal(t, "busy (code session.loop-running, action retry; formerly session.orchestrator-running (deprecated))", r.Error())
	require.Equal(t, exitV1Conflict, submitExitCode("session.loop-running"))
	require.Equal(t, exitV1Conflict, submitExitCode("session.orchestrator-running"), "an older daemon's code")
	require.Equal(t, ExitUsage, submitExitCode(apexv1.CodeSessionDepth))
}
