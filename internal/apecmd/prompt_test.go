package apecmd //nolint:testpackage // white-box: exercises unexported prompt-assembly + record helpers

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/exoport/apex_process_ape/internal/cost"
	"github.com/exoport/apex_process_ape/internal/repl"
	"github.com/exoport/apex_process_ape/internal/sessiondriver"
	"github.com/stretchr/testify/require"
)

func TestAssemblePromptLine(t *testing.T) {
	require.Equal(t, "hello world", assemblePromptLine("", "hello world"),
		"no agent → plain message")
	require.Equal(t, "/apex-agent-dev --autonomous -- hello world",
		assemblePromptLine("apex-agent-dev", "hello world"),
		"agent → PAT-25 slash prefix")
}

func TestResolveDeliveredPrompt(t *testing.T) {
	// Positional text, undecorated.
	got, err := resolveDeliveredPrompt(promptOptions{text: "do the thing"})
	require.NoError(t, err)
	require.Equal(t, "do the thing", got)

	// --ultracode prepends the keyword.
	got, err = resolveDeliveredPrompt(promptOptions{text: "do the thing", ultracode: true})
	require.NoError(t, err)
	require.Equal(t, "ultracode do the thing", got)

	// --workflow appends the directive.
	got, err = resolveDeliveredPrompt(promptOptions{text: "do the thing", workflow: true})
	require.NoError(t, err)
	require.Equal(t, "do the thing "+workflowDirective, got)

	// Both compose: ultracode prefix + workflow suffix.
	got, err = resolveDeliveredPrompt(promptOptions{text: "do the thing", ultracode: true, workflow: true})
	require.NoError(t, err)
	require.Equal(t, "ultracode do the thing "+workflowDirective, got)
}

func TestResolveDeliveredPrompt_Handoff(t *testing.T) {
	dir := t.TempDir()
	handoff := filepath.Join(dir, "resume.md")
	require.NoError(t, os.WriteFile(handoff, []byte("# resume\n"), 0o600))

	got, err := resolveDeliveredPrompt(promptOptions{handoff: handoff})
	require.NoError(t, err)
	abs, _ := filepath.Abs(handoff)
	require.Equal(t, "Read the handoff document at "+abs+" and continue the work it describes.", got)

	// Handoff composes with the agent wrapper.
	line := assemblePromptLine("apex-agent-dev", got)
	require.True(t, strings.HasPrefix(line, "/apex-agent-dev --autonomous -- Read the handoff document at "))
}

func TestResolveDeliveredPrompt_HandoffMissing(t *testing.T) {
	_, err := resolveDeliveredPrompt(promptOptions{handoff: "/no/such/handoff.md"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

func TestPromptStatus(t *testing.T) {
	s, code := promptStatus(nil)
	require.Equal(t, "completed", s)
	require.Equal(t, ExitOK, code)

	s, code = promptStatus(errClaudeDied)
	require.Equal(t, "claude_died", s)
	require.Equal(t, ExitClaudeDied, code)

	s, code = promptStatus(context.DeadlineExceeded)
	require.Equal(t, "failed", s)
	require.Equal(t, ExitRunFailed, code)

	// PLAN-19 D2/D4: idle vs max-duration terminations surface distinctly
	// on the record, both mapping to ExitRunFailed. errors.As unwraps a
	// wrapped diagnostic too (the pipeline path wraps with %w).
	s, code = promptStatus(&sessiondriver.IdleTimeoutError{Label: "session", Idle: time.Hour})
	require.Equal(t, promptStatusIdleTimeout, s)
	require.Equal(t, ExitRunFailed, code)

	s, code = promptStatus(fmt.Errorf("wrap: %w", &sessiondriver.MaxDurationError{Label: "session", Max: 3 * time.Hour}))
	require.Equal(t, promptStatusMaxDuration, s)
	require.Equal(t, ExitRunFailed, code)
}

// TestPromptRecordAndRollup writes a prompt.yaml session record and
// asserts it folds into the cost rollup's Prompts bucket and reads back
// via FindPromptSession.
func TestPromptRecordAndRollup(t *testing.T) {
	proj := t.TempDir()
	promptID := "20260713-120102-abcdef0"
	runDir := filepath.Join(proj, "_output", "ape", "prompts", promptID)
	require.NoError(t, os.MkdirAll(runDir, 0o755))

	tele := &sessiondriver.Telemetry{
		Totals: cost.Totals{CostUSD: 1.25, InputTokens: 400, OutputTokens: 300, NumTurns: 5},
		ByModel: map[string]cost.Totals{
			"claude-opus-4-7": {CostUSD: 1.25, InputTokens: 400, OutputTokens: 300, NumTurns: 5},
		},
	}
	perModel := perModelTotals(tele)
	writePromptRecord(runDir, promptID, promptOptions{model: "opus[1m]"}, "sess-xyz", "completed", time.Now(), tele, perModel)

	require.FileExists(t, filepath.Join(runDir, "prompt.yaml"))

	ps, ok := cost.FindPromptSession(proj, promptID)
	require.True(t, ok)
	require.InDelta(t, 1.25, ps.Totals.CostUSD, 1e-9)
	require.Equal(t, 5, ps.Totals.NumTurns)
	require.Contains(t, ps.PerModel, "claude-opus-4-7")

	r, err := cost.RebuildRollup(proj)
	require.NoError(t, err)
	require.Contains(t, r.Prompts.Runs, promptID)
	require.InDelta(t, 1.25, r.Prompts.Totals.CostUSD, 1e-9)
}

// --- bash-PTY stand-in (mirrors internal/repl's pattern) ---

func requireBash(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed; skipping")
	}
	if runtime.GOOS == goosWindows {
		t.Skip("POSIX termios behavior; skipping on Windows")
	}
}

// TestPrompt_DeliversAssembledLine spins up a bash REPL as a claude
// stand-in (PS1='❯ ' so WaitForReady's glyph matches), delivers the
// assembled agent-fronted prompt line the way runPrompt does, and
// asserts the exact PAT-25 line reaches the REPL through the PTY.
func TestPrompt_DeliversAssembledLine(t *testing.T) {
	requireBash(t)
	name := "ape-prompt-test-deliver"
	_ = repl.KillSession(t.Context(), name)
	require.NoError(t, repl.NewSession(t.Context(), name, "/tmp", []string{
		"bash", "--noprofile", "--norc", "-c", "PS1='❯ '; export PS1; exec bash --noprofile --norc",
	}))
	t.Cleanup(func() { _ = repl.KillSession(t.Context(), name) })

	readyCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, repl.WaitForReady(readyCtx, name))

	delivered, err := resolveDeliveredPrompt(promptOptions{text: "create FILE.md", ultracode: true})
	require.NoError(t, err)
	line := assemblePromptLine("apex-agent-dev", delivered)
	require.NoError(t, repl.SendCommand(t.Context(), name, line))
	time.Sleep(200 * time.Millisecond)

	pane, err := repl.CapturePane(t.Context(), name)
	require.NoError(t, err)
	require.Contains(t, pane, "/apex-agent-dev --autonomous -- ultracode create FILE.md",
		"assembled PAT-25 line must reach the REPL verbatim")
}
