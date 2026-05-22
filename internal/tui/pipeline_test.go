//nolint:testpackage // exercising the unexported pipelineModel state machine; trade-off is deliberate
package tui

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/diegosz/apex_process_ape/internal/bridge/orchestrator"
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
	m := NewPipelineModel(spec, func() { cancelled = true }, "")

	m, cmd := pressKey(t, &m, "q")
	require.Equal(t, modalQuitConfirm, m.modal, "q should open the quit-confirmation modal")
	require.Nil(t, cmd, "q while running should not quit immediately")
	require.False(t, cancelled, "q must not cancel the runner without confirmation")
}

func TestQuitModal_YConfirmCancelsAndQuits(t *testing.T) {
	spec := fakeSpec(t)
	cancelled := false
	m := NewPipelineModel(spec, func() { cancelled = true }, "")

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
	m := NewPipelineModel(spec, func() { cancelled = true }, "")

	m, _ = pressKey(t, &m, "q")    // open modal
	m, cmd := pressKey(t, &m, "n") // dismiss
	require.Equal(t, modalNone, m.modal, "n must dismiss the modal")
	require.Nil(t, cmd, "n must not quit")
	require.False(t, cancelled, "n must never cancel the runner")
}

func TestQuitModal_EscDismissesModal(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")

	m, _ = pressKey(t, &m, "q")
	m, _ = pressKey(t, &m, keyEsc)
	require.Equal(t, modalNone, m.modal)
}

func TestQuitModal_DoubleCtrlCForceQuits(t *testing.T) {
	spec := fakeSpec(t)
	cancelled := false
	m := NewPipelineModel(spec, func() { cancelled = true }, "")

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
	m := NewPipelineModel(spec, func() { cancelled = true }, "")

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
	m := NewPipelineModel(spec, func() { cancelled = true }, "")
	m.phase = phaseCompleted

	_, cmd := pressKey(t, &m, "q")
	require.NotNil(t, cmd, "q after finish must quit immediately")
	// No confirmation modal — nothing to cancel.
	require.False(t, cancelled, "cancel must not fire when pipeline already finished")
}

func TestQuitModal_NilCancelIsSafe(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")

	m, _ = pressKey(t, &m, "q")
	_, cmd := pressKey(t, &m, "y")
	// Should not panic; should still emit tea.Quit.
	require.NotNil(t, cmd)
}

func TestQuitModal_SummarizesRunStateForOverlay(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")

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

// ─────────── PLAN-1 / I4 navigation tests ───────────

func TestNav_StageStartUpdatesCursorInLiveMode(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")
	require.Equal(t, modeLive, m.mode)
	require.Equal(t, 0, m.cursorIdx)

	// Simulate alpha starting then beta starting.
	res, _ := m.Update(stageStartMsg{stage: "alpha"})
	m, _ = res.(pipelineModel)
	require.Equal(t, 0, m.cursorIdx, "cursor follows the first started stage")

	res, _ = m.Update(stageStartMsg{stage: "beta"})
	m, _ = res.(pipelineModel)
	require.Equal(t, 1, m.cursorIdx, "cursor follows when next stage starts in live mode")
}

func TestNav_PinFreezesCursor(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")
	// Run alpha; cursor follows.
	res, _ := m.Update(stageStartMsg{stage: "alpha"})
	m, _ = res.(pipelineModel)
	// User pins.
	m, _ = pressKey(t, &m, "enter")
	require.Equal(t, modePinned, m.mode)
	pinned := m.cursorIdx

	// beta starts — cursor MUST NOT follow (we're pinned).
	res, _ = m.Update(stageStartMsg{stage: "beta"})
	m, _ = res.(pipelineModel)
	require.Equal(t, pinned, m.cursorIdx, "cursor must stay pinned while modePinned")
}

func TestNav_LReturnsToLiveAndFollowsActive(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")

	// Bring alpha running, beta still pending.
	res, _ := m.Update(stageStartMsg{stage: "alpha"})
	m, _ = res.(pipelineModel)
	// Pin to alpha.
	m, _ = pressKey(t, &m, "enter")
	// Manually move cursor down (shouldn't matter — we'll snap to active).
	m, _ = pressKey(t, &m, "down")
	// Press L to return to Live.
	m, _ = pressKey(t, &m, "l")
	require.Equal(t, modeLive, m.mode)
	require.Equal(t, 0, m.cursorIdx, "Live mode snaps cursor back to the running stage")
}

func TestNav_ArrowMovesCursor(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")
	m, _ = pressKey(t, &m, "down")
	require.Equal(t, 1, m.cursorIdx)
	m, _ = pressKey(t, &m, "up")
	require.Equal(t, 0, m.cursorIdx)
	// Can't move past either end.
	m, _ = pressKey(t, &m, "up")
	require.Equal(t, 0, m.cursorIdx)
}

func TestEvents_AppendDisplayableOnly(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")
	res, _ := m.Update(stageStartMsg{stage: "alpha"})
	m, _ = res.(pipelineModel)

	displayable := `{"type":"assistant","message":{"content":[{"type":"text","text":"Drafting"}]}}`
	noise := `{"type":"user","message":{"content":[{"type":"tool_result","is_error":false,"content":"File created successfully at /tmp/foo"}]}}`
	malformed := `not json at all`

	for _, line := range []string{displayable, noise, malformed} {
		res, _ = m.Update(stepLineMsg{stage: "alpha", idx: 0, line: line})
		m, _ = res.(pipelineModel)
	}
	// PLAN-2 / F2: drain the throttle queue before asserting.
	res, _ = m.Update(throttleTickMsg{})
	m, _ = res.(pipelineModel)
	// Expect 2 entries: the displayable text + the malformed fall-through
	// (rendered as EventUnknown).
	require.Len(t, m.stages[0].events, 2)
	require.Equal(t, EventText, m.stages[0].events[0].Kind)
	require.Equal(t, EventUnknown, m.stages[0].events[1].Kind)
}

func TestEvents_RunningStepIdxTracksLifecycle(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")
	require.Equal(t, -1, m.stages[0].runningStepIdx)

	res, _ := m.Update(stepStartMsg{stage: "alpha", idx: 0})
	m, _ = res.(pipelineModel)
	require.Equal(t, 0, m.stages[0].runningStepIdx)

	res, _ = m.Update(stepEndMsg{stage: "alpha", idx: 0})
	m, _ = res.(pipelineModel)
	require.Equal(t, -1, m.stages[0].runningStepIdx, "step end clears runningStepIdx")
}

func TestRenderHeader_TracksLiveSkill(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")
	res, _ := m.Update(stageStartMsg{stage: "alpha"})
	m, _ = res.(pipelineModel)
	res, _ = m.Update(stepStartMsg{stage: "alpha", idx: 0})
	m, _ = res.(pipelineModel)
	require.Equal(t, "apex-fake-skill-a", m.renderHeader())
}

func TestRenderHeader_PinnedShowsStageName(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")
	m, _ = pressKey(t, &m, "enter")
	require.Equal(t, modePinned, m.mode)
	require.Equal(t, "pinned: alpha", m.renderHeader())
}

// populateEvents fills the cursor stage's events slice with n
// distinct EventText entries so scroll tests can observe viewport
// movement without rendering real stream-json. Pointer receiver
// matches pressKey's convention — pipelineModel is heavy enough that
// gocritic flags by-value parameters.
//
// PLAN-2 / F2: stepLineMsg now queues into pendingLines; this helper
// dispatches a synthetic throttleTickMsg after the burst so the
// scroll tests can observe the flushed events slice directly.
//
//nolint:unparam // stage is parameterized for future multi-stage scroll tests; unused-value warning is intentional now
func populateEvents(t *testing.T, m *pipelineModel, stage string, n int) pipelineModel {
	t.Helper()
	cur := *m
	for i := range n {
		line := `{"type":"assistant","message":{"content":[{"type":"text","text":"line ` +
			strconv.Itoa(i) + `"}]}}`
		res, _ := cur.Update(stepLineMsg{stage: stage, idx: 0, line: line})
		var ok bool
		cur, ok = res.(pipelineModel)
		require.True(t, ok)
	}
	// Drain the F2 throttle queue so callers see the flushed state.
	res, _ := cur.Update(throttleTickMsg{})
	var ok bool
	cur, ok = res.(pipelineModel)
	require.True(t, ok)
	return cur
}

// setWindowSize delivers a tea.WindowSizeMsg so the model can size
// its rendered panels — F8's tailScrollOffset depends on m.height.
// The width parameter now varies across tests (90 for the PLAN-7
// smoke cases), so the nolint:unparam directive that previously
// pinned it at 120 is no longer applicable.
func setWindowSize(t *testing.T, m *pipelineModel, w, h int) pipelineModel {
	t.Helper()
	res, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	pm, ok := res.(pipelineModel)
	require.True(t, ok)
	return pm
}

// TestScroll_PgUpInLiveModeBeginsUserScroll asserts the F8 fix: PgUp
// in the default modeLive (with userScrolled=false) is no longer a
// no-op — it seeds the scrollOffset to the current tail-anchored
// position, sets userScrolled, and moves the viewport back by
// pageStep events.
func TestScroll_PgUpInLiveModeBeginsUserScroll(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")
	m = setWindowSize(t, &m, 120, 30) // eventPanelHeightFor(30) = 24
	m = populateEvents(t, &m, "alpha", 50)
	require.False(t, m.userScrolled, "live mode starts auto-tailing")

	// Tail window currently anchors at 50-24 = 26. PgUp moves it
	// back by pageStep (10) to 16.
	m, _ = pressKey(t, &m, "pgup")
	require.True(t, m.userScrolled, "PgUp must enable userScrolled")
	require.Equal(t, 16, m.scrollOffset)

	// Second PgUp: 16 - 10 = 6.
	m, _ = pressKey(t, &m, "pgup")
	require.Equal(t, 6, m.scrollOffset)

	// Third PgUp clamps at 0.
	m, _ = pressKey(t, &m, "pgup")
	require.Equal(t, 0, m.scrollOffset)
}

// TestScroll_PgDnRestoresAutoTailAtBottom asserts that paging back
// down past the tail clears userScrolled so subsequent live events
// resume auto-tailing.
func TestScroll_PgDnRestoresAutoTailAtBottom(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")
	m = setWindowSize(t, &m, 120, 30)
	m = populateEvents(t, &m, "alpha", 50)
	m, _ = pressKey(t, &m, "pgup")
	require.True(t, m.userScrolled)

	// Page back down. Tail offset is 26 → one PgDn brings us to 26
	// which is the tail, so userScrolled clears.
	m, _ = pressKey(t, &m, "pgdown")
	require.False(t, m.userScrolled, "PgDn to tail must clear userScrolled")
	require.Equal(t, 0, m.scrollOffset)
}

// TestScroll_NewEventHoldsUserScroll asserts the auto-tail-suspend
// contract from F8: while userScrolled is true, new events arriving
// at the current cursor stage do not move the viewport.
func TestScroll_NewEventHoldsUserScroll(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")
	m = setWindowSize(t, &m, 120, 30)
	m = populateEvents(t, &m, "alpha", 50)
	m, _ = pressKey(t, &m, "pgup")
	scrollBefore := m.scrollOffset
	require.True(t, m.userScrolled)

	// New event arrives at the current cursor stage.
	m = populateEvents(t, &m, "alpha", 5)
	require.Equal(t, scrollBefore, m.scrollOffset, "scroll offset must not move while userScrolled")
	require.True(t, m.userScrolled)
}

// TestScroll_EndKeyReturnsToAutoTail asserts End rejoins the tail
// regardless of where the user scrolled to.
func TestScroll_EndKeyReturnsToAutoTail(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")
	m = setWindowSize(t, &m, 120, 30)
	m = populateEvents(t, &m, "alpha", 50)
	m, _ = pressKey(t, &m, "pgup")
	m, _ = pressKey(t, &m, "pgup")
	require.NotEqual(t, 0, m.scrollOffset)

	m, _ = pressKey(t, &m, "end")
	require.False(t, m.userScrolled, "End must clear userScrolled")
	require.Equal(t, 0, m.scrollOffset)
}

// TestScroll_LKeyReturnsToLive asserts that L resets both mode and
// userScrolled, so the panel rejoins auto-tail on the active stage.
func TestScroll_LKeyReturnsToLive(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")
	m = setWindowSize(t, &m, 120, 30)
	m = populateEvents(t, &m, "alpha", 50)
	m, _ = pressKey(t, &m, "enter")
	require.True(t, m.userScrolled, "pin seeds userScrolled")

	m, _ = pressKey(t, &m, "l")
	require.False(t, m.userScrolled)
	require.Equal(t, 0, m.scrollOffset)
	require.Equal(t, modeLive, m.mode)
}

// TestScroll_EnterPinSeedsTailOffset asserts that pinning a stage
// opens the viewport on the tail of that stage's events — so the
// user sees the latest output before scrolling back into history.
func TestScroll_EnterPinSeedsTailOffset(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")
	m = setWindowSize(t, &m, 120, 30)
	m = populateEvents(t, &m, "alpha", 50)

	m, _ = pressKey(t, &m, "enter")
	require.Equal(t, modePinned, m.mode)
	require.True(t, m.userScrolled)
	require.Equal(t, 26, m.scrollOffset, "Enter must seed scrollOffset to tail = len(events) - panelHeight")
}

// ─────────── PLAN-2 / F7 final-report + linger after completion ───────────

// TestFinalReport_PipelineDoneEntersCompletedPhase asserts the
// post-completion lifecycle: pipelineDoneMsg no longer quits; the
// model transitions to phaseCompleted and the cursor moves to the
// synthetic final-report row.
func TestFinalReport_PipelineDoneEntersCompletedPhase(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")
	require.Equal(t, phaseRunning, m.phase)

	res, cmd := m.Update(pipelineDoneMsg{err: nil})
	m, _ = res.(pipelineModel)
	require.Nil(t, cmd, "pipelineDoneMsg must NOT auto-quit (F7)")
	require.Equal(t, phaseCompleted, m.phase)
	require.Equal(t, len(m.stages), m.cursorIdx, "cursor moves to synthetic final-report row")
}

// TestFinalReport_QQuitsAfterCompletion asserts that q exits
// directly in phaseCompleted (no confirmation modal — there's
// nothing left to cancel).
func TestFinalReport_QQuitsAfterCompletion(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")
	res, _ := m.Update(pipelineDoneMsg{err: nil})
	m, _ = res.(pipelineModel)

	_, cmd := pressKey(t, &m, "q")
	require.NotNil(t, cmd, "q must quit directly in phaseCompleted")
	require.Equal(t, modalNone, m.modal, "no confirmation modal after completion")
}

// TestFinalReport_NavigationStillWorks asserts that ↑↓ still moves
// the cursor among stages + the report row after completion.
func TestFinalReport_NavigationStillWorks(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")
	res, _ := m.Update(pipelineDoneMsg{err: nil})
	m, _ = res.(pipelineModel)
	require.Equal(t, len(m.stages), m.cursorIdx, "starts on report row")

	// Up moves back into the stage list.
	m, _ = pressKey(t, &m, "up")
	require.Equal(t, len(m.stages)-1, m.cursorIdx)

	// Down returns to the report row.
	m, _ = pressKey(t, &m, "down")
	require.Equal(t, len(m.stages), m.cursorIdx)

	// Down again is clamped.
	m, _ = pressKey(t, &m, "down")
	require.Equal(t, len(m.stages), m.cursorIdx, "cursor must clamp at report row")
}

// TestFinalReport_BannerReflectsVerdict asserts the completion banner
// summarizes pass / fail counts.
func TestFinalReport_BannerReflectsVerdict(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")
	m.stages[0].state = stateDone
	m.stages[1].state = stateFailed
	res, _ := m.Update(pipelineDoneMsg{err: nil})
	m, _ = res.(pipelineModel)

	banner := m.renderCompletionBanner()
	require.Contains(t, banner, "1/2 FAILED", "banner must show failure count")
}

// TestFinalReport_FinalReportContents asserts that selecting the
// synthetic report row renders per-stage summary lines.
func TestFinalReport_FinalReportContents(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")
	m = setWindowSize(t, &m, 120, 30)
	m.stages[0].state = stateDone
	m.stages[1].state = stateDone
	res, _ := m.Update(pipelineDoneMsg{err: nil})
	m, _ = res.(pipelineModel)

	body := m.renderFinalReport(80, 10)
	require.Contains(t, body, "alpha")
	require.Contains(t, body, "beta")
	require.Contains(t, body, "event(s)")
}

// TestFinalReport_StageListAppendsReportRow asserts the right-side
// stage list renders the synthetic 📊 final report row after
// completion.
func TestFinalReport_StageListAppendsReportRow(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")
	res, _ := m.Update(pipelineDoneMsg{err: nil})
	m, _ = res.(pipelineModel)

	list := m.renderStageList(0)
	require.Contains(t, list, "📊 final report")
}

// ─────────── PLAN-2 / F3 render-style cycling ───────────

// TestRenderStyle_RKeyCyclesHumanRawBoth asserts pressing `r`
// advances the style enum cyclically.
func TestRenderStyle_RKeyCyclesHumanRawBoth(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")
	require.Equal(t, styleHuman, m.renderStyle)

	m, _ = pressKey(t, &m, "r")
	require.Equal(t, styleRawJSON, m.renderStyle)

	m, _ = pressKey(t, &m, "r")
	require.Equal(t, styleBoth, m.renderStyle)

	m, _ = pressKey(t, &m, "r")
	require.Equal(t, styleHuman, m.renderStyle, "cycle wraps back to human")
}

// TestRenderStyle_RawJSONRendersOriginalLine asserts that in
// styleRawJSON the event panel shows the raw NDJSON line instead of
// the parsed summary.
func TestRenderStyle_RawJSONRendersOriginalLine(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")
	m = setWindowSize(t, &m, 200, 30)
	res, _ := m.Update(stageStartMsg{stage: "alpha"})
	m, _ = res.(pipelineModel)
	raw := `{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`
	res, _ = m.Update(stepLineMsg{stage: "alpha", idx: 0, line: raw})
	m, _ = res.(pipelineModel)
	// PLAN-2 / F2: drain the throttle queue.
	res, _ = m.Update(throttleTickMsg{})
	m, _ = res.(pipelineModel)

	// Default style (human) shows parsed body.
	humanBody := m.renderEventPanel(180, 5)
	require.Contains(t, humanBody, "hello")
	require.NotContains(t, humanBody, `"type":"assistant"`)

	// After `r`, raw JSON appears.
	m, _ = pressKey(t, &m, "r")
	rawBody := m.renderEventPanel(180, 5)
	require.Contains(t, rawBody, `"type":"assistant"`)

	// One more `r` is both: contains parsed body AND raw line.
	m, _ = pressKey(t, &m, "r")
	bothBody := m.renderEventPanel(180, 5)
	require.Contains(t, bothBody, "hello")
	require.Contains(t, bothBody, `"type":"assistant"`)
}

// TestRenderStyle_RawCarriedFromRenderer asserts that the renderer
// populates Raw on RenderedEvent so styleRawJSON / styleBoth can
// surface it without re-parsing.
func TestRenderStyle_RawCarriedFromRenderer(t *testing.T) {
	raw := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"foo.md"}}]}}`
	r := RenderEventWithRoot(raw, "")
	require.Equal(t, raw, r.Raw, "RenderedEvent.Raw must mirror the original NDJSON line")
}

// ─────────── PLAN-2 / F4 narrow-terminal fallback ───────────

// TestNarrowLayout_BelowThresholdUsesStepper asserts that the
// horizontal stepper appears in the rendered View() when width <
// narrowLayoutThreshold.
func TestNarrowLayout_BelowThresholdUsesStepper(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")
	m = setWindowSize(t, &m, 80, 30)
	require.Less(t, m.width, narrowLayoutThreshold, "test must run in narrow regime")
	view := m.View()
	// The narrow stepper is a single line containing both stages
	// inline (alpha · beta) with the cursor bracketed.
	require.Contains(t, view, "[")
	require.Contains(t, view, "alpha")
	require.Contains(t, view, "beta")
}

// TestNarrowLayout_WideRegimeKeepsRightColumn asserts that the
// wide layout still renders the right-side stages column when
// width >= narrowLayoutThreshold.
func TestNarrowLayout_WideRegimeKeepsRightColumn(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")
	m = setWindowSize(t, &m, 200, 30)
	view := m.View()
	// In wide mode the "stages" header text appears (it's the
	// right-column heading); the narrow layout doesn't render it.
	require.Contains(t, view, "stages")
}

// TestNarrowLayout_StepperShowsCursorBracket asserts the F4 stepper
// renders the cursor stage wrapped in [ ] for visibility.
func TestNarrowLayout_StepperShowsCursorBracket(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")
	m.cursorIdx = 1
	strip := m.renderStageStepper(80)
	require.Contains(t, strip, "[")
	require.Contains(t, strip, "beta")
}

// TestNarrowLayout_StepperIncludesReportRow asserts the final-report
// row appears in the stepper after pipeline completion (F4 × F7).
func TestNarrowLayout_StepperIncludesReportRow(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")
	res, _ := m.Update(pipelineDoneMsg{err: nil})
	m, _ = res.(pipelineModel)

	strip := m.renderStageStepper(80)
	require.Contains(t, strip, "📊 report")
}

// ─────────── PLAN-2 / F2 render throttle ───────────

// TestThrottle_StepLineQueuesIntoPendingLines asserts stepLineMsg
// goes into the pendingLines queue, not directly into the stage's
// events slice. The events surface only on the next throttle tick.
func TestThrottle_StepLineQueuesIntoPendingLines(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")
	res, _ := m.Update(stepLineMsg{
		stage: "alpha", idx: 0,
		line: `{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`,
	})
	m, _ = res.(pipelineModel)

	require.Empty(t, m.stages[0].events, "events must not surface until throttle tick fires")
	require.Len(t, m.pendingLines, 1, "displayable event must enter the F2 queue")

	// One throttle tick drains everything.
	res, _ = m.Update(throttleTickMsg{})
	m, _ = res.(pipelineModel)
	require.Empty(t, m.pendingLines, "tick drains the queue")
	require.Len(t, m.stages[0].events, 1)
}

// TestThrottle_TickBatchesMultipleEvents asserts that a burst of
// stepLineMsgs between two ticks all land in a single flush.
func TestThrottle_TickBatchesMultipleEvents(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")
	for i := range 50 {
		line := `{"type":"assistant","message":{"content":[{"type":"text","text":"line ` +
			strconv.Itoa(i) + `"}]}}`
		res, _ := m.Update(stepLineMsg{stage: "alpha", idx: 0, line: line})
		m, _ = res.(pipelineModel)
	}
	require.Len(t, m.pendingLines, 50)
	require.Empty(t, m.stages[0].events, "throttling holds events until the tick")

	res, _ := m.Update(throttleTickMsg{})
	m, _ = res.(pipelineModel)
	require.Empty(t, m.pendingLines)
	require.Len(t, m.stages[0].events, 50, "all 50 events flush in one tick")
}

// ─────────── PLAN-7 / FA unified-model source flag ───────────

// fakeAlphaStep is the step-tag the bridge would emit for stage
// "alpha" step 0 against the fakeSpec test pipeline. Defined as a
// constant because several tests synthesize it onto fixture
// HookEvents whose own Step strings target real-world stage names.
const fakeAlphaStep = "alpha/0-apex-fake-skill-a"

// TestPipelineModelHookEventSource asserts that a model built with
// WithEventSource(SourceHookEvents) ingests hookEventMsg into the
// per-stage events slice through the same throttle queue, and drops
// stream-json lines (which shouldn't arrive on this source).
func TestPipelineModelHookEventSource(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "", WithEventSource(SourceHookEvents))
	require.Equal(t, SourceHookEvents, m.source)

	h := loadHookFixture(t, "pretooluse_read.json")
	// The fixture's step ("pattern-governance/...") doesn't match the
	// fakeSpec stages — rewrite to one that does.
	h.Step = fakeAlphaStep

	res, _ := m.Update(hookEventMsg{hook: h})
	m, _ = res.(pipelineModel)
	require.Len(t, m.pendingLines, 1, "hookEventMsg must enter the throttle queue")

	res, _ = m.Update(throttleTickMsg{})
	m, _ = res.(pipelineModel)
	require.Empty(t, m.pendingLines)
	require.Len(t, m.stages[0].events, 1)
	require.Equal(t, EventTool, m.stages[0].events[0].Kind)

	// Stream-json lines under SourceHookEvents are silently dropped.
	res, _ = m.Update(stepLineMsg{
		stage: "alpha", idx: 0,
		line: `{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`,
	})
	m, _ = res.(pipelineModel)
	require.Empty(t, m.pendingLines, "stream-json must be ignored under SourceHookEvents")
}

// TestPipelineModelStreamJSONIgnoresHookEvents is the mirror: under
// the default SourceStreamJSON, hookEventMsg drops silently.
func TestPipelineModelStreamJSONIgnoresHookEvents(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "") // default source
	require.Equal(t, SourceStreamJSON, m.source)

	h := loadHookFixture(t, "pretooluse_read.json")
	h.Step = fakeAlphaStep
	res, _ := m.Update(hookEventMsg{hook: h})
	m, _ = res.(pipelineModel)
	require.Empty(t, m.pendingLines, "hookEventMsg must be ignored under SourceStreamJSON")
}

// TestAwaitModalRequiresSender asserts that awaitPendingMsg is a
// no-op when no sender is wired (programmatic mode never reaches it).
func TestAwaitModalRequiresSender(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "") // no WithAwaitReplySender
	res, _ := m.Update(awaitPendingMsg{})
	m, _ = res.(pipelineModel)
	require.False(t, m.awaitActive, "no sender ⇒ modal stays closed")
}

// TestAwaitModalOpensWithSender asserts that the modal lights up
// when a sender is wired and awaitPendingMsg arrives.
func TestAwaitModalOpensWithSender(t *testing.T) {
	spec := fakeSpec(t)
	called := false
	m := NewPipelineModel(spec, nil, "", WithAwaitReplySender(func(string) { called = true }))
	res, _ := m.Update(awaitPendingMsg{})
	m, _ = res.(pipelineModel)
	require.True(t, m.awaitActive)

	// Simulate user typing then pressing Enter — sender must fire.
	m.replyInput.SetValue("approved")
	m, _ = pressKey(t, &m, "enter")
	require.True(t, called, "Enter must invoke awaitReplySender")
	require.False(t, m.awaitActive, "submitting closes the modal locally")
}

// TestAwaitModalResolvedClosesIt asserts that an upstream
// awaitResolvedMsg closes the modal idempotently.
func TestAwaitModalResolvedClosesIt(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "", WithAwaitReplySender(func(string) {}))
	res, _ := m.Update(awaitPendingMsg{})
	m, _ = res.(pipelineModel)
	require.True(t, m.awaitActive)

	res, _ = m.Update(awaitResolvedMsg{})
	m, _ = res.(pipelineModel)
	require.False(t, m.awaitActive)
}

// TestFinalReportHookEventSource asserts that completion lifecycle
// works identically under the hook-event source. The synthetic
// final-report row populates with per-stage event counts (sourced
// from hook events, not stream-json) and q quits cleanly. PLAN-7 / FD.
func TestFinalReportHookEventSource(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "", WithEventSource(SourceHookEvents))
	m = setWindowSize(t, &m, 200, 30)

	// Land two hook events for stage alpha, then mark both stages done.
	h := loadHookFixture(t, "pretooluse_read.json")
	h.Step = fakeAlphaStep
	for range 2 {
		res, _ := m.Update(hookEventMsg{hook: h})
		m, _ = res.(pipelineModel)
	}
	res, _ := m.Update(throttleTickMsg{})
	m, _ = res.(pipelineModel)
	m.stages[0].state = stateDone
	m.stages[1].state = stateDone

	res, cmd := m.Update(pipelineDoneMsg{err: nil})
	m, _ = res.(pipelineModel)
	require.Nil(t, cmd, "pipelineDoneMsg must not auto-quit under hook-event source either")
	require.Equal(t, phaseCompleted, m.phase)
	require.Equal(t, len(m.stages), m.cursorIdx, "cursor lands on final-report row")

	body := m.renderFinalReport(80, 10)
	require.Contains(t, body, "alpha")
	require.Contains(t, body, "2 event(s)", "per-stage event count reflects hook-event ingestion")

	_, cmd = pressKey(t, &m, "q")
	require.NotNil(t, cmd, "q quits in phaseCompleted")
}

// TestPrePostCorrelation asserts that a Pre/Post pair on the same
// tool_use_id renders as two adjacent rows in the events slice. The
// model preserves arrival order; the bridge delivers Pre and Post
// adjacently for the same tool, so this is the natural outcome —
// no explicit reordering is performed. PLAN-7 / FB+FC.
func TestPrePostCorrelation(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "", WithEventSource(SourceHookEvents))

	pre := loadHookFixture(t, "pretooluse_read.json")
	pre.Step = fakeAlphaStep
	post := loadHookFixture(t, "posttooluse_read_ok.json")
	post.Step = fakeAlphaStep

	for _, h := range []orchestrator.HookEvent{pre, post} {
		res, _ := m.Update(hookEventMsg{hook: h})
		m, _ = res.(pipelineModel)
	}
	res, _ := m.Update(throttleTickMsg{})
	m, _ = res.(pipelineModel)
	require.Len(t, m.stages[0].events, 2, "both Pre and Post must land")
	require.Equal(t, EventTool, m.stages[0].events[0].Kind, "Pre is first")
	require.Equal(t, EventToolResult, m.stages[0].events[1].Kind, "Post immediately follows")
}

// TestOrphanPostStillRenders asserts that a PostToolUse arriving
// with no matching Pre still produces a renderable row (defensive
// path for buffer-overflow recovery). PLAN-7 / FB risk R1.
func TestOrphanPostStillRenders(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "", WithEventSource(SourceHookEvents))

	post := loadHookFixture(t, "posttooluse_read_ok.json")
	post.Step = fakeAlphaStep
	res, _ := m.Update(hookEventMsg{hook: post})
	m, _ = res.(pipelineModel)
	res, _ = m.Update(throttleTickMsg{})
	m, _ = res.(pipelineModel)
	require.Len(t, m.stages[0].events, 1, "orphan Post must still render")
	require.Equal(t, EventToolResult, m.stages[0].events[0].Kind)
}

// TestStageFromHookStep covers the step-tag → stage-name parse.
func TestStageFromHookStep(t *testing.T) {
	require.Equal(t, "alpha", stageFromHookStep("alpha/1-apex-skill"))
	require.Equal(t, "pattern-governance", stageFromHookStep("pattern-governance/1-apex-pattern-reconciliation"))
	require.Equal(t, "bare", stageFromHookStep("bare"))
	require.Empty(t, stageFromHookStep(""))
}

// TestStepIdxFromHookStep covers the step-tag → 0-based step idx parse.
// On-wire labels are 1-based per pipeline.StepLabel; the helper subtracts
// one so it can index the model's 0-based steps slice directly.
func TestStepIdxFromHookStep(t *testing.T) {
	idx, ok := stepIdxFromHookStep("alpha/1-apex-skill")
	require.True(t, ok)
	require.Equal(t, 0, idx)
	idx, ok = stepIdxFromHookStep("pattern-governance/3-apex-adr-adoption")
	require.True(t, ok)
	require.Equal(t, 2, idx)
	_, ok = stepIdxFromHookStep("bare")
	require.False(t, ok, "no slash ⇒ no idx")
	_, ok = stepIdxFromHookStep("alpha/0-apex-skill")
	require.False(t, ok, "0 idx is out-of-band per StepLabel's 1-based convention")
	_, ok = stepIdxFromHookStep("alpha/nope-skill")
	require.False(t, ok, "non-numeric idx prefix rejected")
	_, ok = stepIdxFromHookStep("")
	require.False(t, ok)
}

// TestStatusStrip_RunningStepShowsElapsed asserts (a) — the running
// step line in renderStatusStrip includes the step's elapsed time
// once stepStartMsg has stamped startedAt. PLAN-7 follow-up.
func TestStatusStrip_RunningStepShowsElapsed(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")
	res, _ := m.Update(stageStartMsg{stage: "alpha"})
	m, _ = res.(pipelineModel)
	res, _ = m.Update(stepStartMsg{stage: "alpha", idx: 0})
	m, _ = res.(pipelineModel)
	// Backdate the step start so elapsedFor returns a non-empty value
	// without sleeping in the test.
	m.stages[0].steps[0].startedAt = time.Now().Add(-2 * time.Second)
	m.tick = time.Now()
	strip := m.renderStatusStrip()
	require.Contains(t, strip, "▸ step 1/1 (apex-fake-skill-a)")
	require.Regexp(t, `▸ step 1/1 \(apex-fake-skill-a\) · \d`, strip,
		"running-step label must carry a `· <elapsed>` suffix")
}

// TestHookEvent_StopAnnotatesElapsed asserts (b) — when a Stop hook
// lands on a stage whose matching step has a startedAt timestamp, the
// rendered event body is suffixed with the step's elapsed time.
// PLAN-7 follow-up.
func TestHookEvent_StopAnnotatesElapsed(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "", WithEventSource(SourceHookEvents))
	res, _ := m.Update(stepStartMsg{stage: "alpha", idx: 0})
	m, _ = res.(pipelineModel)
	m.stages[0].steps[0].startedAt = time.Now().Add(-3 * time.Second)

	h := loadHookFixture(t, "stop.json")
	h.Step = "alpha/1-apex-fake-skill-a"
	res, _ = m.Update(hookEventMsg{hook: h})
	m, _ = res.(pipelineModel)
	res, _ = m.Update(throttleTickMsg{})
	m, _ = res.(pipelineModel)

	require.Len(t, m.stages[0].events, 1)
	ev := m.stages[0].events[0]
	require.Equal(t, EventSuccess, ev.Kind)
	require.Contains(t, ev.Body, "skill complete")
	require.Regexp(t, ` · \d`, ev.Body,
		"Stop body must be suffixed with the step's elapsed duration")
}

// TestHookEvent_StopWithoutStartedAtSkipsAnnotation asserts the
// elapsed annotation is a no-op when no matching startedAt was
// recorded — Stop rows for unknown steps still render their base body.
func TestHookEvent_StopWithoutStartedAtSkipsAnnotation(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "", WithEventSource(SourceHookEvents))

	h := loadHookFixture(t, "stop.json")
	h.Step = "alpha/1-apex-fake-skill-a" // no preceding stepStartMsg
	res, _ := m.Update(hookEventMsg{hook: h})
	m, _ = res.(pipelineModel)
	res, _ = m.Update(throttleTickMsg{})
	m, _ = res.(pipelineModel)

	require.Len(t, m.stages[0].events, 1)
	require.NotContains(t, m.stages[0].events[0].Body, " · 0ms",
		"missing startedAt must not produce a zero-elapsed suffix")
}

// ─────────── PLAN-7 / F0 row-budget invariant ───────────

// TestPanelRowBudget_ComposerExactLines asserts composePanelBody
// produces exactly budget rows regardless of how many newlines the
// header or body string carries. Padding fills short input; long
// input is truncated. The trailing newline is removed so lipgloss
// doesn't grow the rendered box by one row.
func TestPanelRowBudget_ComposerExactLines(t *testing.T) {
	cases := []struct {
		name   string
		header string
		body   string
		budget int
	}{
		{"empty body", "header", "", 5},
		{"trailing newline body", "header", "a\nb\nc\n", 5},
		{"short body padded", "header", "a", 8},
		{"long body truncated", "header", "a\nb\nc\nd\ne\nf\ng", 4},
		{"header trailing margin", "header\n", "body\n", 4},
		{"budget one", "h", "b", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := composePanelBody(tc.header, tc.body, tc.budget)
			// Line count = newline count + 1 when string is non-empty.
			lineCount := 1
			if out != "" {
				for i := range len(out) {
					if out[i] == '\n' {
						lineCount++
					}
				}
			}
			require.Equal(t, tc.budget, lineCount,
				"budget=%d but rendered to %d lines: %q", tc.budget, lineCount, out)
		})
	}
}

// TestPanelRowBudget_EventPanelHonorsHeight asserts renderEventPanel
// never returns more than `height` lines, across all three render
// styles. Regression guard for the F0 root cause — styleBoth was
// emitting 2 lines per event while the windowing math assumed 1,
// so 24 events at panelHeight=20 produced 48 lines and grew the
// box past the right panel.
func TestPanelRowBudget_EventPanelHonorsHeight(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")
	m = setWindowSize(t, &m, 200, 30)
	m = populateEvents(t, &m, "alpha", 100)

	const height = 20
	for _, style := range []renderStyle{styleHuman, styleRawJSON, styleBoth} {
		t.Run(strconv.Itoa(int(style)), func(t *testing.T) {
			m.renderStyle = style
			body := m.renderEventPanel(180, height)
			lineCount := 1
			for i := range len(body) {
				if body[i] == '\n' {
					lineCount++
				}
			}
			require.LessOrEqual(t, lineCount, height,
				"renderEventPanel emitted %d lines for style=%d, budget=%d",
				lineCount, style, height)
		})
	}
}

// TestPanelRowBudget_LeftRightAlign asserts the rendered View() emits
// left and right panels of identical height so their bottom borders
// stay aligned. Counts the newlines in the composed body strings the
// View passes to lipgloss; equality on those guarantees the
// resulting box heights match. (We don't render the lipgloss box
// itself in tests because Render output is platform/terminal-noisy.)
func TestPanelRowBudget_LeftRightAlign(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")
	m = setWindowSize(t, &m, 200, 30)
	// Fill the left panel past the visible window; right panel is
	// short (just the 2 stages). Pre-F0 this was the misalignment
	// trigger because the left would grow by 1 row.
	m = populateEvents(t, &m, "alpha", 100)
	m.renderStyle = styleBoth

	rightWidth := max(m.width/3, stageListWidthMin)
	leftWidth := max(m.width-rightWidth-panelBorderOverhead, eventPanelWidthMin)
	panelHeight := max(m.height-statusRowReserve, panelHeightMin)

	leftBody := composePanelBody(
		pipelineHeaderStyle.Render(m.renderHeader()),
		m.renderEventPanel(leftWidth-panelBorderOverhead, panelHeight-headerRowReserve),
		panelHeight,
	)
	rightBody := composePanelBody(
		pipelineHeaderStyle.Render("stages"),
		m.renderStageList(0),
		panelHeight,
	)
	leftLines := 1
	for i := range len(leftBody) {
		if leftBody[i] == '\n' {
			leftLines++
		}
	}
	rightLines := 1
	for i := range len(rightBody) {
		if rightBody[i] == '\n' {
			rightLines++
		}
	}
	require.Equal(t, leftLines, rightLines,
		"left + right panel bodies must have identical line counts: left=%d right=%d",
		leftLines, rightLines)
	require.Equal(t, panelHeight, leftLines,
		"composed body must equal the requested panelHeight")
}

// TestThrottle_PipelineDoneMsgDrainsQueue asserts that the F7
// completion transition drains any leftover pendingLines so the
// final-report row's per-stage event count is accurate.
func TestThrottle_PipelineDoneMsgDrainsQueue(t *testing.T) {
	spec := fakeSpec(t)
	m := NewPipelineModel(spec, nil, "")
	res, _ := m.Update(stepLineMsg{
		stage: "alpha", idx: 0,
		line: `{"type":"assistant","message":{"content":[{"type":"text","text":"final"}]}}`,
	})
	m, _ = res.(pipelineModel)
	require.Len(t, m.pendingLines, 1, "event queued, not yet flushed")

	res, _ = m.Update(pipelineDoneMsg{err: nil})
	m, _ = res.(pipelineModel)
	require.Empty(t, m.pendingLines, "pipelineDoneMsg must drain the queue")
	require.Len(t, m.stages[0].events, 1, "the queued event must reach the stage on completion")
}
