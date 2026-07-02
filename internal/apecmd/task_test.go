package apecmd

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/diegosz/apex_process_ape/internal/repl"
	"github.com/stretchr/testify/require"
)

// TestBuildTaskStep pins the skill-layer --no-commit injection rules:
// agent path gets the flag prefixed into Args; no-agent path is left
// untouched (the PAT-25 convention already carries it).
func TestBuildTaskStep(t *testing.T) {
	cases := []struct {
		name     string
		opts     taskOptions
		wantArgs string
	}{
		{
			name:     "agent + no-commit injects prefix",
			opts:     taskOptions{skill: "apex-create-prd", agent: "apex-agent-pm", args: "--from-status draft", skillNoCommit: true},
			wantArgs: "--no-commit --from-status draft",
		},
		{
			name:     "agent + no-commit with empty args",
			opts:     taskOptions{skill: "apex-create-prd", agent: "apex-agent-pm", skillNoCommit: true},
			wantArgs: "--no-commit",
		},
		{
			name:     "agent without no-commit leaves args alone",
			opts:     taskOptions{skill: "apex-create-prd", agent: "apex-agent-pm", args: "--from-status draft"},
			wantArgs: "--from-status draft",
		},
		{
			name:     "no agent + no-commit is a no-op on args",
			opts:     taskOptions{skill: "apex-shard-doc", args: "--doc prd", skillNoCommit: true},
			wantArgs: "--doc prd",
		},
	}
	for _, tc := range cases {
		step := buildTaskStep(tc.opts)
		require.Equal(t, tc.wantArgs, step.Args, tc.name)
		require.Equal(t, tc.opts.skill, step.Skill, tc.name)
		require.Equal(t, tc.opts.agent, step.Agent, tc.name)
	}
}

// TestTaskExitCode pins the PLAN-11 exit-code table.
func TestTaskExitCode(t *testing.T) {
	require.Equal(t, taskExitOK, taskExitCode(nil))
	require.Equal(t, taskExitRunFailed, taskExitCode(errors.New("interactive step idle for 60m0s without Stop hook")))
	wrapped := errors.Join(
		errors.New("stage wrap"),
		&repl.NotReadyError{Name: "s", Pane: "modal", Err: context.DeadlineExceeded},
	)
	require.Equal(t, taskExitNotReady, taskExitCode(wrapped))
}

// TestTaskEnvelopeShape locks the JSON field names the eval consumes.
func TestTaskEnvelopeShape(t *testing.T) {
	msg := "boom"
	env := taskEnvelope{
		Skill:           "apex-create-prd",
		Agent:           "apex-agent-pm",
		Model:           "opus[1m]",
		Success:         false,
		ExitCode:        1,
		DurationSeconds: 142.3,
		CostUSD:         0.83,
		Usage:           taskUsage{InputTokens: 1, OutputTokens: 2, CacheReadInputTokens: 3, CacheCreationInputTokens: 4, NumTurns: 5},
		Commits:         []string{"SKILL:create-prd"},
		ManifestPath:    "_output/tasks/apex-create-prd/x/manifest.yaml",
		Error:           &msg,
	}
	bs, err := json.Marshal(env)
	require.NoError(t, err)
	for _, key := range []string{
		`"skill"`, `"agent"`, `"model"`, `"success"`, `"exit_code"`,
		`"duration_seconds"`, `"cost_usd"`, `"input_tokens"`, `"output_tokens"`,
		`"cache_read_input_tokens"`, `"cache_creation_input_tokens"`,
		`"num_turns"`, `"commits"`, `"manifest_path"`, `"error"`,
	} {
		require.Contains(t, string(bs), key)
	}

	// error must serialize as JSON null on success, not be omitted —
	// consumers key on its presence.
	env.Error = nil
	bs, err = json.Marshal(env)
	require.NoError(t, err)
	require.Contains(t, string(bs), `"error":null`)
}

// TestGitCommitSubjectsSince exercises the commit-trail helper against
// a real throwaway repo.
func TestGitCommitSubjectsSince(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	ctx := context.Background()
	git := func(args ...string) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	git("init")
	git("commit", "--allow-empty", "-m", "base")

	before := gitHeadFull(ctx, dir)
	require.NotEmpty(t, before)
	require.Empty(t, gitCommitSubjectsSince(ctx, dir, before))

	git("commit", "--allow-empty", "-m", "SKILL:create-prd")
	git("commit", "--allow-empty", "-m", "ape:task/apex-shard-doc")

	subjects := gitCommitSubjectsSince(ctx, dir, before)
	require.Equal(t, []string{"SKILL:create-prd", "ape:task/apex-shard-doc"}, subjects)

	// Empty `before` (no repo at run start) degrades to empty, not error.
	require.Empty(t, gitCommitSubjectsSince(ctx, dir, ""))
}

// TestTaskCommandFlagSurface asserts the command registers the
// documented flags and the bare --task-commit NoOptDefVal sentinel.
func TestTaskCommandFlagSurface(t *testing.T) {
	cmd := newTaskCmd()
	for _, name := range []string{
		"agent", "model", "args", "prompt", "prompt-flag",
		"no-commit", "task-commit", "commit-allow-dirty",
		"idle-timeout", "output-format", "quiet", "manifest-dir",
		"ignore-project-settings", "cwd",
	} {
		require.NotNil(t, cmd.Flags().Lookup(name), "missing flag --%s", name)
	}
	tc := cmd.Flags().Lookup("task-commit")
	require.Equal(t, taskCommitDerivedSentinel, tc.NoOptDefVal)
	require.True(t, strings.HasPrefix(taskCommitDerivedSentinel, "\x01"),
		"sentinel must be non-typeable")
}
