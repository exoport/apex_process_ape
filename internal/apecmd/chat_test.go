package apecmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/exoport/apex_process_ape/internal/repl"
	"github.com/exoport/apex_process_ape/internal/selfpath"
	"github.com/stretchr/testify/require"
)

// `ape chat` had no test file at all, which is how the `ape` pin reached
// this path verified only by the fact that it compiled. These assert the
// composition the command hands claude.

func TestChatSpawnEnv_ScrubsNestingMarkersAndKeepsTmux(t *testing.T) {
	env, unpin, notice := chatSpawnEnv([]string{
		"CLAUDECODE=1",
		"CLAUDE_CODE_SESSION_ID=abc",
		"CLAUDE_CODE_EFFORT_LEVEL=low",
		"TMUX=/tmp/tmux-1000/default,123,0",
		"TMUX_PANE=%7",
		"HOME=/h",
		"PATH=/usr/bin",
	}, "")
	defer unpin()
	require.Empty(t, notice)

	joined := strings.Join(env, "\n")
	require.NotContains(t, joined, "CLAUDECODE=")
	require.NotContains(t, joined, "CLAUDE_CODE_SESSION_ID=")
	require.NotContains(t, joined, "CLAUDE_CODE_EFFORT_LEVEL=",
		"the inherited effort is scrubbed; only an explicit one is re-injected")
	require.Contains(t, joined, "HOME=/h")

	// The asymmetry with the PTY path, asserted rather than only
	// commented: chat hands claude the user's REAL terminal, so the
	// inherited pane address describes this child correctly.
	require.Contains(t, joined, "TMUX=/tmp/tmux-1000/default,123,0")
	require.Contains(t, joined, "TMUX_PANE=%7")
}

// TestChatSpawnEnv_PinsThisBinaryAsApe: a skill run inside `ape chat`
// shells out to `ape` exactly as one inside a dispatch does.
func TestChatSpawnEnv_PinsThisBinaryAsApe(t *testing.T) {
	env, unpin, notice := chatSpawnEnv([]string{"PATH=/stale/bin:/usr/bin"}, "")
	defer unpin()
	require.Empty(t, notice)

	var pathValue string
	for _, kv := range env {
		if after, ok := strings.CutPrefix(kv, "PATH="); ok {
			pathValue = after
		}
	}
	first, rest, _ := strings.Cut(pathValue, string(os.PathListSeparator))
	require.Equal(t, "/stale/bin:/usr/bin", rest, "the operator's PATH survives behind the pin")

	pinned := filepath.Join(first, selfpath.Name)
	require.FileExists(t, pinned)

	unpin()
	require.NoFileExists(t, pinned, "the shadow goes when the session ends")
}

// TestChatSpawnEnv_EffortIsInjectedAfterTheScrub — the order is the whole
// point. Injected before the scrub, the value it sets is the value the
// scrub then removes.
func TestChatSpawnEnv_EffortIsInjectedAfterTheScrub(t *testing.T) {
	env, unpin, _ := chatSpawnEnv([]string{"CLAUDE_CODE_EFFORT_LEVEL=low"}, "xhigh")
	defer unpin()

	var seen []string
	for _, kv := range env {
		if strings.HasPrefix(kv, repl.EnvClaudeEffortLevel+"=") {
			seen = append(seen, kv)
		}
	}
	require.Equal(t, []string{repl.EnvClaudeEffortLevel + "=xhigh"}, seen,
		"exactly one, and it is the caller's — not the inherited one")
}

// TestChatSpawnEnv_NoEffortKeepsClaudesNative is the interactive
// default, and the one thing chat does differently from task/pipeline.
func TestChatSpawnEnv_NoEffortKeepsClaudesNative(t *testing.T) {
	env, unpin, _ := chatSpawnEnv([]string{"HOME=/h"}, "")
	defer unpin()
	require.NotContains(t, strings.Join(env, "\n"), repl.EnvClaudeEffortLevel+"=")
}
