package apecmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/repl"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// Every session-starting command refuses under APE_SESSION, with exit 2
// and the owning run named — before it resolves a project, reads a flag's
// meaning, or spawns anything. The args would each start real work if the
// guard were missing, which is what makes this a test of the guard.
func TestRefuseNested_EverySessionStartingCommand(t *testing.T) {
	t.Setenv(repl.EnvApeSession, "task/20260926-120146-556be67")
	root := projectFor(t, allExtensionsConfig)
	for name, run := range map[string]struct {
		cmd  func() *cobra.Command
		args []string
	}{
		"pipeline": {newPipelineCmd, []string{"design", "--cwd", root}},
		"task":     {newTaskCmd, []string{"apex-dev-story", "--cwd", root}},
		"change":   {newChangeCmd, []string{"fix the typo", "--cwd", root}},
		"prompt":   {newPromptCmd, []string{"hello", "--cwd", root}},
		"chat":     {newChatCmd, []string{"--cwd", root}},
		"script":   {newScriptCmd, []string{"run.star", "--cwd", root}},
	} {
		t.Run(name, func(t *testing.T) {
			cmd := run.cmd()
			cmd.SetArgs(run.args)
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			err := cmd.Execute()
			require.Equal(t, ExitUsage, exitCodeOf(t, err))
			require.Contains(t, err.Error(), "task/20260926-120146-556be67", "the refusal names the owning run")
			require.Contains(t, err.Error(), "never runs inside an ape-spawned session")
		})
	}
}

// Queueing a change starts nothing, and project-data commands are what
// skills run inside sessions all day: neither may refuse.
func TestRefuseNested_LeavesNonSessionCommandsAlone(t *testing.T) {
	t.Setenv(repl.EnvApeSession, "pipeline/fx")
	root := projectFor(t, allExtensionsConfig)

	cmd := newConfigCmd()
	cmd.SetArgs([]string{"effort", "--cwd", root})
	cmd.SetOut(&bytes.Buffer{})
	require.NoError(t, cmd.Execute())

	cmd = newChangeCmd()
	cmd.SetArgs([]string{"--queue", "fix the typo", "--cwd", root})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		require.NotContains(t, err.Error(), "ape-spawned session", "--queue is not refused for nesting")
	}
}

func TestRefuseNested_NoMarkerNoRefusal(t *testing.T) {
	t.Setenv(repl.EnvApeSession, "")
	require.NoError(t, refuseNested("ape task"))
}

// The orchestrator conducts ape runs, so running it in an ape session is
// ape inside ape one level up. Refused at preflight, before any spawn, with
// the plain-session instruction.
func TestPrompt_RefusesTheOrchestratorAgent(t *testing.T) {
	_, code, err := runPromptCore(context.Background(), promptOptions{
		agent: "apex-orchestrator", text: "build it", projectRoot: t.TempDir(),
	})
	require.Equal(t, ExitUsage, code)
	require.Error(t, err)
	require.Contains(t, err.Error(), "/apex-orchestrator --autonomous -- <request>")
}

func TestChatSpawnEnv_StampsTheSessionMarker(t *testing.T) {
	env, unpin, _ := chatSpawnEnv([]string{"HOME=/h"}, "", "20260926T120000Z")
	defer unpin()
	var got []string
	for _, kv := range env {
		if strings.HasPrefix(kv, repl.EnvApeSession+"=") {
			got = append(got, kv)
		}
	}
	require.Equal(t, []string{"APE_SESSION=chat/20260926T120000Z"}, got)
}
