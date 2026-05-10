// Tests live in package tui (not tui_test) so they can drive the
// unexported pipelineModel.Update state machine without leaking
// modal/state internals into the public API. The testpackage lint
// rule is suppressed at the directive below; the trade-off is
// deliberate.
//
//nolint:testpackage // exercising unexported model state machine
package tui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/diegosz/apex_process_ape/internal/pipeline"
	"github.com/stretchr/testify/require"
)

// fakeSpec builds a pipeline.Spec sufficient for model construction.
// We don't run anything; we just need the stage names so
// NewPipelineModel can pre-populate its rows.
func fakeSpec(t *testing.T) *pipeline.Spec {
	t.Helper()
	dir := t.TempDir()
	pipelinesDir := filepath.Join(dir, "_apex", "pipelines")
	require.NoError(t, os.MkdirAll(pipelinesDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(pipelinesDir, "test.yaml"),
		[]byte(`name: test
stages:
  alpha:
    chain:
      - skill: apex-fake-skill-a
  beta:
    chain:
      - skill: apex-fake-skill-b
`),
		0o644,
	))
	spec, err := pipeline.LoadSpec("test", dir)
	require.NoError(t, err)
	return spec
}

// pressKey simulates a single keypress against the model and returns
// the next-state model + any tea.Cmd produced. The model is passed
// as a pointer because lint flags the by-value form as a hugeParam;
// callers reassign *m after each press, matching Bubble Tea's
// model-mutation idiom.
func pressKey(t *testing.T, m *pipelineModel, key string) (pipelineModel, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(keyMsg(key))
	pm, ok := next.(pipelineModel)
	require.True(t, ok, "Update returned non-pipelineModel: %T", next)
	return pm, cmd
}

// keyMsg wraps the string-form key under the tea.KeyMsg interface.
// Bubble Tea's tea.KeyMsg is a struct whose String() method drives
// switch statements; constructing one directly with the right Type is
// fiddly across versions, so we use the test-friendly tea.KeyRunes
// path with a single rune (plus a name-mapped specialization for
// non-rune keys like ctrl+c / esc).
func keyMsg(key string) tea.KeyMsg {
	switch key {
	case keyCtrlC:
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case keyEsc:
		return tea.KeyMsg{Type: tea.KeyEsc}
	default:
		if len(key) == 1 {
			return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{rune(key[0])}}
		}
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
}

func TestQuitModal_QPressOpensModal(t *testing.T) {
	spec := fakeSpec(t)
	cancelled := false
	m := NewPipelineModel(spec, func() { cancelled = true })

	m, cmd := pressKey(t, &m, "q")
	require.Equal(t, modalQuitConfirm, m.modal, "q should open the quit-confirmation modal")
	require.Nil(t, cmd, "q while running should not quit immediately")
	require.False(t, cancelled, "q must not cancel the runner without confirmation")
}

func TestQuitModal_YConfirmCancelsAndQuits(t *testing.T) {
	spec := fakeSpec(t)
	cancelled := false
	m := NewPipelineModel(spec, func() { cancelled = true })

	m, _ = pressKey(t, &m, "q")    // open modal
	_, cmd := pressKey(t, &m, "y") // confirm
	require.True(t, cancelled, "confirmed quit must invoke the cancel function")
	require.NotNil(t, cmd, "confirmed quit must emit tea.Quit")
	// tea.Quit is a function returning a tea.QuitMsg; invoke it to verify.
	msg := cmd()
	_, isQuit := msg.(tea.QuitMsg)
	require.True(t, isQuit, "expected tea.QuitMsg, got %T", msg)
}

func TestQuitModal_NDismissesModal(t *testing.T) {
	spec := fakeSpec(t)
	cancelled := false
	m := NewPipelineModel(spec, func() { cancelled = true })

	m, _ = pressKey(t, &m, "q")    // open modal
	m, cmd := pressKey(t, &m, "n") // dismiss
	require.Equal(t, modalNone, m.modal, "n must dismiss the modal")
	require.Nil(t, cmd, "n must not quit")
	require.False(t, cancelled, "n must never cancel the runner")
}

func TestQuitModal_EscDismissesModal(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil)

	m, _ = pressKey(t, &m, "q")
	m, _ = pressKey(t, &m, keyEsc)
	require.Equal(t, modalNone, m.modal)
}

func TestQuitModal_DoubleCtrlCForceQuits(t *testing.T) {
	spec := fakeSpec(t)
	cancelled := false
	m := NewPipelineModel(spec, func() { cancelled = true })

	// First Ctrl+C opens the modal (and records the timestamp).
	m, _ = pressKey(t, &m, keyCtrlC)
	require.Equal(t, modalQuitConfirm, m.modal)
	require.False(t, cancelled, "single Ctrl+C must not cancel without confirmation")
	require.False(t, m.lastCtrlC.IsZero(), "lastCtrlC must be recorded")

	// Second Ctrl+C within the window force-quits (bypasses modal).
	_, cmd := pressKey(t, &m, keyCtrlC)
	require.True(t, cancelled, "double-Ctrl+C must force-cancel")
	require.NotNil(t, cmd, "double-Ctrl+C must quit")
	msg := cmd()
	_, isQuit := msg.(tea.QuitMsg)
	require.True(t, isQuit)
}

func TestQuitModal_SlowSecondCtrlCStaysInModal(t *testing.T) {
	spec := fakeSpec(t)
	cancelled := false
	m := NewPipelineModel(spec, func() { cancelled = true })

	m, _ = pressKey(t, &m, keyCtrlC)
	// Backdate the recorded timestamp to outside the double-tap window.
	m.lastCtrlC = time.Now().Add(-2 * doubleCtrlCWindow)

	m, cmd := pressKey(t, &m, keyCtrlC)
	require.Equal(t, modalQuitConfirm, m.modal, "slow second Ctrl+C must keep the modal open")
	require.Nil(t, cmd, "slow second Ctrl+C must not quit")
	require.False(t, cancelled, "slow second Ctrl+C must not cancel")
}

func TestQuitModal_FinishedPipelineQuitsImmediately(t *testing.T) {
	spec := fakeSpec(t)
	cancelled := false
	m := NewPipelineModel(spec, func() { cancelled = true })
	m.finished = true

	_, cmd := pressKey(t, &m, "q")
	require.NotNil(t, cmd, "q after finish must quit immediately")
	// No confirmation modal — nothing to cancel.
	require.False(t, cancelled, "cancel must not fire when pipeline already finished")
}

func TestQuitModal_NilCancelIsSafe(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil)

	m, _ = pressKey(t, &m, "q")
	_, cmd := pressKey(t, &m, "y")
	// Should not panic; should still emit tea.Quit.
	require.NotNil(t, cmd)
}

func TestQuitModal_SummarizesRunStateForOverlay(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil)

	// Mark stage 1 as running, no stages done.
	m.stages[0].state = stateRunning
	m.stages[0].startedAt = time.Now().Add(-30 * time.Second)
	m.tick = time.Now()

	running, completed := m.summarizeRunState()
	require.Contains(t, running, "alpha", "summary must name the running stage")
	require.Empty(t, completed, "no stages completed yet")

	// Mark stage 1 done, stage 2 running.
	m.stages[0].state = stateDone
	m.stages[0].endedAt = time.Now()
	m.stages[1].state = stateRunning
	m.stages[1].startedAt = time.Now().Add(-5 * time.Second)

	running, completed = m.summarizeRunState()
	require.Contains(t, running, "beta")
	require.Contains(t, completed, "1 stage", "1 done stage")
}
