package e2e

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/repl"
	"github.com/stretchr/testify/require"
)

// agentsMDQuestion can only be answered from this repository's loaded
// instructions: every tool is disabled, so the model cannot read the file,
// and the rule it asks for exists nowhere else it could have seen.
const agentsMDQuestion = "Your project instructions contain a rule about commit messages and Claude attribution. " +
	"Quote that rule's sentence verbatim and nothing else. If you were given no project instructions, " +
	"reply exactly NO-INSTRUCTIONS."

// agentsMDMarker is a phrase of that rule, as AGENTS.md words it, and
// deliberately NOT a phrase of the question: a model that did not load the
// file echoes the question back ("…does not include a rule about Claude
// attribution"), which an earlier marker taken from the question counted as
// a pass. The negative control with a stray CLAUDE.md caught it.
const agentsMDMarker = "Generated with Claude Code"

// TestLive_AgentsMD proves the installed Claude Code still loads this
// repository's AGENTS.md. The repo carries no CLAUDE.md on purpose (see
// AGENTS.md), so the whole set of repo instructions rests on Claude Code's
// native AGENTS.md loading — a behaviour an update can change without any
// error, which is why it is checked rather than assumed.
//
// The same question asked in an empty directory is the negative control: it
// must NOT produce the rule, or the check would pass on a model that
// guessed rather than one that read.
func TestLive_AgentsMD(t *testing.T) {
	if os.Getenv("APE_CLAUDE_LIVE") != "1" {
		t.Skip("APE_CLAUDE_LIVE=1 not set — this gate needs claude, auth and network (make check-agents-md)")
	}
	claudeBin, err := exec.LookPath("claude")
	require.NoError(t, err, "claude not on PATH")

	got := askNoTools(t, claudeBin, repoRoot(t))
	t.Logf("repo root answered: %s", got)
	require.Contains(t, got, agentsMDMarker,
		"Claude Code, started in the repo root, could not quote a rule that only AGENTS.md contains: it did not "+
			"load AGENTS.md. Check that no CLAUDE.md exists here or in any parent directory, then whether this "+
			"Claude Code version changed how AGENTS.md is loaded")

	control := askNoTools(t, claudeBin, t.TempDir())
	t.Logf("empty directory answered: %s", control)
	require.NotContains(t, control, agentsMDMarker,
		"the negative control quoted the rule with no AGENTS.md present, so the question does not prove "+
			"anything was loaded — pick a rule the model cannot produce unprompted")
}

// askNoTools runs one `claude -p` turn in dir with every tool disabled.
func askNoTools(t *testing.T, claudeBin, dir string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, claudeBin, "-p", agentsMDQuestion,
		"--model", "haiku", "--tools", "", "--strict-mcp-config", "--output-format", "text")
	cmd.Dir = dir
	cmd.Env = append(repl.ScrubClaudeCodeEnv(os.Environ()), repl.SpawnDefaultEnv()...)
	out, err := cmd.Output()
	require.NoError(t, err, "claude -p in %s", dir)
	return string(out)
}
