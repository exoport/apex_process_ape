package apecmd

import (
	"encoding/json"
	"testing"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// Backlog 38: in an APEX v0 project, the v1 commands that need a project
// and `ape task` refuse, naming the pinned v0 ape and the lift; the lift
// itself still runs there.
func TestV0Project_V1CommandsRefuse(t *testing.T) {
	root := v0Repo(t)
	t.Setenv("APE_SESSION", "")
	t.Setenv("CLAUDECODE", "")
	for _, run := range []struct {
		cmd  func() *cobra.Command
		args []string
	}{
		{newStatusCmd, nil},
		{newProjectCmd, []string{"check"}},
		{newProjectCmd, []string{"init"}},
		{newMilestoneCmd, []string{"new", "x", "--gate", "true"}},
		{newChangeCmd, []string{"check", "some-change"}},
		{newDecisionCmd, []string{"new", "use-go"}},
	} {
		args := append(append([]string{}, run.args...), "--cwd", root)
		_, stderr, err := v1Exec(t, run.cmd, args...)
		require.Error(t, err, "%v", args)
		require.Equal(t, ExitUsage, exitCodeOf(t, err), "%v", args)
		require.Contains(t, stderr, apexv1.V0ProjectMsg, "%v", args)
	}

	// --json carries the code and its action.
	stdout, _, err := v1Exec(t, newStatusCmd, "--json", "--cwd", root)
	require.Error(t, err)
	var out struct {
		Diagnostics []apexv1.Diagnostic `json:"diagnostics"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &out), stdout)
	require.Equal(t, "project.v0", out.Diagnostics[0].Code)
	require.Equal(t, apexv1.ActionPerson, out.Diagnostics[0].Action)

	// ape task refuses before anything spawns.
	_, _, err = v1Exec(t, newTaskCmd, "apex-dev-story", "--cwd", root)
	require.Error(t, err)
	require.Equal(t, ExitUsage, exitCodeOf(t, err))
	require.Contains(t, err.Error(), apexv1.V0ProjectMsg)

	// The lift is how the project leaves v0: it still plans here.
	_, _, err = v1Exec(t, newProjectCmd, "lift", "--dry-run", "--json", "--cwd", root)
	require.NoError(t, err)
}

func TestIsV0Project(t *testing.T) {
	require.True(t, apexv1.IsV0Project(v0Repo(t)))
	require.False(t, apexv1.IsV0Project(v1Repo(t)))
	require.False(t, apexv1.IsV0Project(t.TempDir()), "no APEX at all is not v0")
}
