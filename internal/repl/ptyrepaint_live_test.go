package repl

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/exoport/apex_process_ape/internal/cost"
)

// ptyRepaintBudget is the largest PTY quiet gap this gate tolerates while a
// tool call is visibly running. claude repaints its spinner and elapsed
// timer about once a second; 15 s is slack for a loaded machine, and still
// two orders of magnitude below the 60m idle window the signal feeds.
const ptyRepaintBudget = 15 * time.Second

// liveePTYRepaintsDuringTool proves the `ape prompt` idle anchor still has
// something to anchor ON while a tool call runs.
//
// `ape prompt` installs SetPTYProbe (apecmd/prompt.go) and it is the ONLY
// progress signal alive during a single silent tool call: measured here,
// the transcript does not grow by a byte for the whole command, and no hook
// fires between PreToolUse and PostToolUse. If claude stopped repainting
// while a tool ran, that path's idle timeout would start cancelling healthy
// sessions at the window — silently, and only for long tool calls, which is
// the hardest kind of report to act on.
//
// It asserts BOTH halves, because either alone would mislead: PTY staying
// fresh is only interesting while the transcript is proven static, and a
// static transcript is only a problem if PTY is what carries the anchor.
//
// Deliberately not run on the `ape task` / pipeline path: those watch hooks
// and transcript growth and NOT PTY, by the argument recorded at
// apecmd/pipeline_interactive.go's OnStepStart.
func livePTYRepaintsDuringTool(t *testing.T, claudeBin string) {
	t.Helper()
	if os.Getenv("APE_CLAUDE_LIVE_TOKENS") == "0" {
		t.Skip("APE_CLAUDE_LIVE_TOKENS=0 — skipping: this subtest submits a turn")
	}
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	// Well under claude's 120 s Bash default timeout, so the command is
	// still running for the whole sampled window.
	const sleepFor = 25 * time.Second

	dir, token := uniqueWorkdir(t)
	name := sessionName(t, "ptyrepaint")
	t.Cleanup(func() { removeScratchTranscripts(t, home, token) })
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	argv := []string{claudeBin, "--dangerously-skip-permissions", "--model", cost.ResolveFamilyAlias("haiku")}
	require.NoError(t, NewSessionWithEnv(ctx, name, dir, argv, EffortEnv("medium")))
	t.Cleanup(func() { _ = KillSession(context.Background(), name) })
	require.NoError(t, WaitForReady(ctx, name))

	// Explicit about NOT delegating: a first version of this seed said only
	// "run it with the Bash tool", and claude launched a background
	// sub-agent instead, ended its turn, and left an idle pane with no
	// foreground tool row to measure at all.
	require.NoError(t, SendCommand(ctx, name,
		"Run this command YOURSELF, directly, with the Bash tool: sleep 25. "+
			"Do NOT use the Agent tool, do NOT delegate to a sub-agent, and do NOT run it in the "+
			"background — call the Bash tool in this turn and wait for it. Run no other tool, write no "+
			"files, and print nothing while it runs. When it returns, reply with the single word DONE."))

	path := waitForTranscript(ctx, t, home, token)
	require.NotEmpty(t, path, "no transcript appeared, so the turn never started — the seed is broken, "+
		"which is not the same as the contract holding")

	// Wait for the tool row to appear: sampling before the command starts
	// would measure the reply stream instead of the silent window.
	var running bool
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); {
		if toolRowRunning(paneOf(ctx, name)) {
			running = true
			break
		}
		time.Sleep(2 * time.Second)
	}
	require.True(t, running, "claude never showed a running Bash row, so the seed produced no silent "+
		"tool call to measure — fix the seed rather than reading this as a pass.\nPane:\n%s", paneOf(ctx, name))

	sizeAtStart := statSize(path)
	var (
		worstPTY  time.Duration
		samples   int
		grewWhile bool
	)
	for deadline := time.Now().Add(sleepFor - 5*time.Second); time.Now().Before(deadline); {
		time.Sleep(2 * time.Second)
		if !toolRowRunning(paneOf(ctx, name)) {
			break // the command finished early; stop before the reply stream
		}
		samples++
		at, ok := LastOutputAt(name)
		require.True(t, ok, "the PTY reader reports no output timestamp at all for a live session, so "+
			"LastOutputAt can no longer feed the prompt path's progress anchor")
		if age := time.Since(at); age > worstPTY {
			worstPTY = age
		}
		if statSize(path) != sizeAtStart {
			grewWhile = true
		}
	}

	require.NotZero(t, samples, "the tool call never stayed running across a single sample — nothing measured")
	require.Less(t, worstPTY, ptyRepaintBudget,
		"claude went quiet on the PTY for %v while a tool call was visibly running (budget %v, %d samples).\n"+
			"PTY output is the ONLY progress signal `ape prompt` has during a silent tool call — the "+
			"transcript does not grow and no hook fires between PreToolUse and PostToolUse — so a claude "+
			"that no longer repaints turns that path's --idle-timeout into a cancel button for healthy "+
			"long tool calls.", worstPTY, ptyRepaintBudget, samples)

	// The half that makes the above meaningful. If the transcript DID grow
	// throughout, PTY is no longer load-bearing and the whole argument in
	// pipeline_interactive.go's OnStepStart comment needs re-measuring.
	require.False(t, grewWhile,
		"the transcript GREW during a silent tool call (%d samples). That is not a failure of claude — it "+
			"means transcript growth now anchors this window on its own, so PTY is no longer the only "+
			"signal here and the reasoning recorded at pipeline_interactive.go's OnStepStart should be "+
			"re-measured rather than trusted.", samples)

	t.Logf("PTY stayed within %v of live across %d samples while `sleep 25` ran; transcript static at %d bytes",
		worstPTY.Round(100*time.Millisecond), samples, sizeAtStart)
}

// toolRowRunning reports whether the pane shows a FOREGROUND tool call in
// flight, using the two markers claude draws while one runs, both read off a
// live 2.1.275 pane rather than assumed:
//
//	⎿  Running…
//	(ctrl+b ctrl+b (twice) to run in background)
//
// Read off a live pane because the obvious guesses are wrong here. There is
// no "esc to interrupt" in this version, and emptyPromptRe matches the whole
// time a turn is working — the prompt line goes empty again the moment the
// input is submitted — so neither is a busy/idle discriminator.
//
// The turn-level spinner is deliberately NOT the marker: it also runs while
// claude is thinking, and the transcript does grow then, which would fail
// the static-transcript half for a reason that has nothing to do with the
// contract. These two markers bracket the tool call itself.
//
// If claude restyles them, this gate fails as a broken SEED ("never showed a
// running tool row") with the pane attached, not as drift — the message says
// so, because a seed that stopped provoking the state is not evidence that
// the contract broke.
func toolRowRunning(pane string) bool {
	return strings.Contains(pane, "to run in background") || strings.Contains(pane, "Running…")
}

// statSize is the file's size, or -1 when it cannot be read.
func statSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return fi.Size()
}
