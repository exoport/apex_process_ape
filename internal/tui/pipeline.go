package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/diegosz/apex_process_ape/internal/pipeline"
)

// doubleCtrlCWindow is how long a second Ctrl+C must arrive within
// to bypass the quit-confirmation modal and force-quit immediately.
// Long enough to catch a deliberate double-tap; short enough that an
// accidental Ctrl+C followed by a slower re-press still goes through
// the modal.
const doubleCtrlCWindow = time.Second

// Bubble Tea two-panel TUI for `ape pipeline`.
//
// Left panel: ordered list of pipeline stages, each row showing the
// stage name, status glyph (⏳ pending, ▶ running, ✓ done, ✗ failed),
// and (when running/done) elapsed time. Stages with multiple chained
// steps render their steps as nested rows with the same status set.
//
// Right panel: most recent step's output. Per PLAN-7 the panel updates
// only when a claude invocation returns — no live tail. The output is
// truncated to the last N lines to keep the display bounded.
//
// The TUI runs as a Bubble Tea program; the pipeline.Run goroutine
// emits Observer events that are forwarded into the program via Send.

const maxOutputLines = 200

// Pipeline-TUI layout constants. View() composes the screen from a
// stages panel + an events panel + a status strip + a keybind hint
// footer; these constants describe how many rows / columns each
// reserves so PLAN-2 / F8's scroll seeding can recover the same
// eventPanelHeight value outside the View() function. The set is
// duplicated below as locals inside View() to keep that path
// allocation-free, but the canonical definition lives here so the
// scroll path stays in sync.
const (
	panelBorderOverhead = 4
	stageListWidthMin   = 28
	eventPanelWidthMin  = 30
	panelHeightMin      = 6
	statusRowReserve    = 4
	headerRowReserve    = 2
)

// PLAN-2 / F8: PgUp / PgDn move the event-panel viewport by pageStep
// events. When the model knows its rendered panel height (set on
// tea.WindowSizeMsg), scrolls page by one panel-height; otherwise the
// constant keeps the keybind useful in tests + pre-resize states.
const pageStep = 10

// throttleTickInterval is the F2 render-throttle interval. Incoming
// stepLineMsg events queue into pendingLines; every interval the
// queue is drained into per-stage event slices in a single Update
// pass, capping perceptible refresh rate at ~30 Hz regardless of
// incoming line rate. 33ms ≈ 30 Hz; comfortable for human eyes and
// avoids per-event re-render cost on bursty streams.
const throttleTickInterval = 33 * time.Millisecond

// PLAN-2 / F4: terminals narrower than narrowLayoutThreshold drop the
// right-side stage list and use a horizontal stepper above the event
// panel instead. The threshold matches the sum of the two columns'
// width floors (28 + 30 + border overhead) — once we can't fit both
// columns comfortably, the single-column layout is more readable.
const narrowLayoutThreshold = 90

// eventPanelHeightFor returns the number of rendered event rows the
// events panel will show given the current terminal height. Mirrors
// the formula in View() so the scroll path can clamp scrollOffset to
// a valid window without re-deriving the layout.
func eventPanelHeightFor(termHeight int) int {
	h := termHeight - statusRowReserve - headerRowReserve
	if h < 1 {
		return 1
	}
	return h
}

var (
	pipelineHeaderStyle = lipgloss.NewStyle().Bold(true).Underline(true).MarginBottom(1)
	pipelinePanelStyle  = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				Padding(0, 1).
				BorderForeground(lipgloss.Color("62"))
	stagePendingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	stageRunningStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)
	stageDoneStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	stageFailedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	stepStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("7"))
	dimStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	modalStyle        = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				Padding(1, 3). //nolint:mnd // (vertical=1, horizontal=3) padding is a deliberate cosmetic choice
				BorderForeground(lipgloss.Color("11")).
				Bold(true)
)

// modalState tracks whether the pipeline TUI is displaying a blocking
// confirmation overlay. Today only quit-confirmation exists; the
// enum reserves room for future overlays (e.g. retry-stage, view-raw).
type modalState int

const (
	modalNone modalState = iota
	modalQuitConfirm
)

// pipelinePhase tracks high-level pipeline lifecycle inside the TUI.
// PLAN-2 / F7: the TUI no longer auto-quits when the pipeline finishes;
// it transitions to phaseCompleted and presents a final-report row so
// the user can review per-stage results before pressing q.
type pipelinePhase int

const (
	phaseRunning pipelinePhase = iota
	phaseCompleted
)

type stageState int

const (
	statePending stageState = iota
	stateRunning
	stateDone
	stateFailed
)

type stepRow struct {
	skill     string
	agent     string
	state     stageState
	startedAt time.Time
	endedAt   time.Time
	output    string
	err       error
}

type stageRow struct {
	name      string
	state     stageState
	startedAt time.Time
	endedAt   time.Time
	steps     []stepRow
	err       error
	// events is the per-stage feed of human-readable rendered events
	// (one entry per displayable claude stream-json line, across all
	// of this stage's chained steps in order).
	events []RenderedEvent
	// runningStepIdx is the index of the step currently running in
	// this stage (-1 when no step is in progress).
	runningStepIdx int
}

// viewMode controls what the event panel shows and how it scrolls.
// modeLive (default while pipeline is running): event panel
// auto-follows the active stage and auto-scrolls.
// modePinned: panel shows the cursor-selected stage's events;
// PgUp/PgDn scrolls; auto-follow disabled.
type viewMode int

const (
	modeLive viewMode = iota
	modePinned
)

// renderStyle controls how event-panel rows render — toggled by the
// `r` key (PLAN-2 / F3). styleHuman (default) renders the parsed
// summary like "🔧 Read foo.md"; styleRawJSON renders the original
// NDJSON line; styleBoth shows both, one per line.
type renderStyle int

const (
	styleHuman renderStyle = iota
	styleRawJSON
	styleBoth
)

type pipelineModel struct {
	pipelineName string
	// projectRoot is the absolute path of the user's project (the
	// cwd the pipeline runner spawns claude under). The event
	// renderer strips this prefix from tool-call path arguments so
	// the displayed path keeps its informative tail (PLAN-2 / F6).
	// Empty string disables relativization.
	projectRoot string
	stages      []stageRow
	stageIdx    map[string]int
	// phase tracks high-level lifecycle. phaseRunning while the
	// pipeline goroutine is still executing; phaseCompleted after
	// pipelineDoneMsg arrives — at which point the synthetic
	// final-report row appears in the stage list and q quits
	// directly (PLAN-2 / F7).
	phase    pipelinePhase
	finalErr error
	width    int
	height   int
	tick     time.Time

	// modal overlays the underlying view. modalNone means no overlay.
	modal modalState
	// lastCtrlC records the timestamp of the most recent Ctrl+C
	// keypress; a second Ctrl+C within doubleCtrlCWindow bypasses the
	// quit modal and force-quits.
	lastCtrlC time.Time
	// cancelRun is invoked when the user confirms quit mid-run. It
	// cancels the runner's context, which exec.CommandContext
	// propagates to the spawned claude subprocess as SIGKILL. May be
	// nil in tests or in --no-tui paths that wire cancellation
	// differently; nil-checked at every call site.
	cancelRun context.CancelFunc

	// mode controls panel behavior — Live auto-follows the active
	// stage; Pinned shows the cursor's stage and freezes auto-scroll.
	mode viewMode
	// cursorIdx is the stage-list index the user is browsing. In
	// modeLive it tracks the active stage; in modePinned it's the
	// pinned stage (read-only).
	cursorIdx int
	// scrollOffset is the number of events skipped from the top of
	// the current stage's event log when rendering. Honored only
	// when userScrolled is true — otherwise the panel auto-tails so
	// the latest event stays visible regardless of mode (PLAN-2 / F8).
	scrollOffset int
	// userScrolled is set when the user has actively scrolled (PgUp /
	// PgDn / Home / pin via Enter). New events arriving in live mode
	// then keep the scroll position stable instead of auto-tailing.
	// Cleared by L (return-to-live), End, or moving the cursor to
	// another stage.
	userScrolled bool
	// renderStyle cycles through human / raw-JSON / both forms when
	// the user presses `r` (PLAN-2 / F3). Default is styleHuman.
	renderStyle renderStyle
	// pendingLines queues displayable events between throttle ticks
	// (PLAN-2 / F2). stepLineMsg appends to this slice; the periodic
	// throttleTickMsg drains it into each stage's events. Caps the
	// effective render rate at ~30 Hz even on 1000-event-per-second
	// bursts.
	pendingLines []pendingEvent
}

// pendingEvent is one queued displayable event waiting to be flushed
// into its target stage's events slice on the next throttle tick.
type pendingEvent struct {
	stageIdx int
	ev       RenderedEvent
}

// ─────────── Bubble Tea messages ───────────

type (
	stageStartMsg struct{ stage string }
	stageEndMsg   struct {
		stage string
		dur   time.Duration
		err   error
	}
)

type stepStartMsg struct {
	stage string
	idx   int
	step  pipeline.Step
}
type stepLineMsg struct {
	stage string
	idx   int
	line  string
}
type stepEndMsg struct {
	stage  string
	idx    int
	step   pipeline.Step
	dur    time.Duration
	output string
	err    error
}
type (
	pipelineDoneMsg struct{ err error }
	tickMsg         struct{}
	throttleTickMsg struct{}
)

// NewPipelineModel returns a tea.Model wired to a pipeline spec. The
// model starts with every stage in the pending state; stages and their
// chains transition as Observer messages arrive.
//
// cancel is invoked when the user confirms quit while the pipeline is
// running; it should cancel the context that runner.Run is using, so
// exec.CommandContext can tear down the in-flight claude subprocess.
// A nil cancel is tolerated (e.g. for tests) — the quit modal still
// renders, but confirmed quit just exits the TUI without touching the
// subprocess.
//
// projectRoot is the absolute path of the user's project — used by
// the event renderer to display tool-call file paths relative to the
// project root (PLAN-2 / F6). Empty string disables relativization.
func NewPipelineModel(spec *pipeline.Spec, cancel context.CancelFunc, projectRoot string) pipelineModel { //nolint:revive // returning unexported type is intentional; callers receive tea.Model via assignment
	rows := make([]stageRow, len(spec.Stages()))
	idx := make(map[string]int, len(rows))
	for i, st := range spec.Stages() {
		steps := make([]stepRow, len(st.Chain))
		for j, s := range st.Chain {
			steps[j] = stepRow{skill: s.Skill, agent: s.Agent}
		}
		rows[i] = stageRow{name: st.Name, steps: steps}
		idx[st.Name] = i
	}
	for i := range rows {
		rows[i].runningStepIdx = -1
	}
	return pipelineModel{
		pipelineName: spec.Name,
		projectRoot:  projectRoot,
		stages:       rows,
		stageIdx:     idx,
		cancelRun:    cancel,
		mode:         modeLive,
	}
}

func (m pipelineModel) Init() tea.Cmd { //nolint:gocritic // Bubble Tea requires value receivers on tea.Model
	return tea.Batch(tickCmd(), throttleTickCmd())
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(_ time.Time) tea.Msg { return tickMsg{} })
}

// throttleTickCmd schedules the next F2 render-throttle tick.
func throttleTickCmd() tea.Cmd {
	return tea.Tick(throttleTickInterval, func(_ time.Time) tea.Msg { return throttleTickMsg{} })
}

// Update dispatches one Bubble Tea message and returns the next-state
// model + any commands. The function is deliberately a long switch
// across every event type — gocyclo flags it, but extracting helpers
// just moves the same branching one call frame deeper and obscures
// the linear flow.
//
//nolint:gocritic,gocyclo,maintidx // Bubble Tea requires value receivers on tea.Model; Update is intrinsically a wide message switch
func (m pipelineModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		key := msg.String()
		// PLAN-2 / F7: pipeline finished — q (or Ctrl+C) quits
		// directly without the confirmation modal (nothing to
		// cancel); other keys still navigate so the user can read
		// the final report. Pre-F7 behavior was "any key quits"
		// which terminated the TUI before the user could review.
		if m.phase == phaseCompleted {
			switch key {
			case "q", "Q", keyCtrlC:
				return m, tea.Quit
			}
			// Fall through to normal navigation handling so ↑↓ /
			// Enter / PgUp / PgDn / Home / End still work.
		}
		// Modal handling takes precedence over normal navigation. Per
		// PLAN-1 / I2: y confirms (cancels subprocess + quits); n / Esc
		// dismisses. Ctrl+C in the modal still respects the double-tap
		// force-quit window so users can always escape a stuck modal.
		if m.modal == modalQuitConfirm {
			switch key {
			case "y", "Y":
				m.invokeCancel()
				return m, tea.Quit
			case "n", "N", keyEsc:
				m.modal = modalNone
				return m, nil
			case keyCtrlC:
				if !m.lastCtrlC.IsZero() && time.Since(m.lastCtrlC) <= doubleCtrlCWindow {
					m.invokeCancel()
					return m, tea.Quit
				}
				m.lastCtrlC = time.Now()
				return m, nil
			}
			return m, nil
		}
		// No modal: navigation keybindings first; q / Ctrl+C fall
		// through to the quit-confirmation modal.
		switch key {
		case "up", "k":
			if m.cursorIdx > 0 {
				m.cursorIdx--
				m.scrollOffset = 0
				m.userScrolled = false
			}
			return m, nil
		case "down", "j":
			// In phaseCompleted, cursor can reach the synthetic
			// final-report row at index len(m.stages) (PLAN-2 / F7).
			maxIdx := len(m.stages) - 1
			if m.phase == phaseCompleted {
				maxIdx = len(m.stages)
			}
			if m.cursorIdx < maxIdx {
				m.cursorIdx++
				m.scrollOffset = 0
				m.userScrolled = false
			}
			return m, nil
		case keyEnter:
			m.mode = modePinned
			// Seed scrollOffset so the pinned view opens on the
			// tail of the pinned stage's events; PgUp from here
			// scrolls back into history while the cursor stays on
			// this stage even after a new stage starts.
			m.scrollOffset = m.tailScrollOffset()
			m.userScrolled = true
			return m, nil
		case "l", "L", keyEsc:
			m.mode = modeLive
			m.scrollOffset = 0
			m.userScrolled = false
			m = m.followActive()
			return m, nil
		case "pgup":
			return m.scrollUp(), nil
		case "pgdown":
			return m.scrollDown(), nil
		case "home":
			m.scrollOffset = 0
			m.userScrolled = true
			return m, nil
		case "end":
			return m.scrollToBottom(), nil
		case "r", "R":
			// PLAN-2 / F3: cycle human → raw → both → human.
			m.renderStyle = (m.renderStyle + 1) % 3 //nolint:mnd // 3 = number of styles in the renderStyle enum (styleHuman / styleRawJSON / styleBoth)
			return m, nil
		case "q":
			m.modal = modalQuitConfirm
			return m, nil
		case keyCtrlC:
			if !m.lastCtrlC.IsZero() && time.Since(m.lastCtrlC) <= doubleCtrlCWindow {
				m.invokeCancel()
				return m, tea.Quit
			}
			m.lastCtrlC = time.Now()
			m.modal = modalQuitConfirm
			return m, nil
		}

	case tickMsg:
		m.tick = time.Now()
		if m.phase == phaseCompleted {
			return m, nil
		}
		return m, tickCmd()

	case stageStartMsg:
		i, ok := m.stageIdx[msg.stage]
		if ok {
			m.stages[i].state = stateRunning
			m.stages[i].startedAt = time.Now()
			if m.mode == modeLive {
				m.cursorIdx = i
				m.scrollOffset = 0
			}
		}
	case stageEndMsg:
		i, ok := m.stageIdx[msg.stage]
		if ok {
			m.stages[i].endedAt = time.Now()
			m.stages[i].runningStepIdx = -1
			if msg.err != nil {
				m.stages[i].state = stateFailed
				m.stages[i].err = msg.err
			} else {
				m.stages[i].state = stateDone
			}
		}
	case stepStartMsg:
		if i, ok := m.stageIdx[msg.stage]; ok && msg.idx < len(m.stages[i].steps) {
			m.stages[i].steps[msg.idx].state = stateRunning
			m.stages[i].steps[msg.idx].startedAt = time.Now()
			m.stages[i].runningStepIdx = msg.idx
		}
	case stepLineMsg:
		// Per PLAN-1 / I4b: parse the raw stream-json line. PLAN-2 /
		// F2 then queues the rendered event into pendingLines rather
		// than appending directly — the next throttleTickMsg (≤33ms)
		// drains the queue into each stage's events slice. Suppressed
		// events (noisy successful tool_results, system pings) are
		// dropped before they hit the queue.
		i, ok := m.stageIdx[msg.stage]
		if !ok {
			return m, nil
		}
		ev := RenderEventWithRoot(msg.line, m.projectRoot)
		if !ev.IsDisplayable() {
			return m, nil
		}
		m.pendingLines = append(m.pendingLines, pendingEvent{stageIdx: i, ev: ev})
	case throttleTickMsg:
		// PLAN-2 / F2: flush queued events into their target stages
		// in a single Update pass, then schedule the next tick. The
		// per-event work here is identical to pre-F2's inline append;
		// the throttle simply batches it.
		for _, p := range m.pendingLines {
			if p.stageIdx < 0 || p.stageIdx >= len(m.stages) {
				continue
			}
			m.stages[p.stageIdx].events = append(m.stages[p.stageIdx].events, p.ev)
			// PLAN-2 / F8 auto-tail: keep the user's scroll offset
			// pinned only when they manually scrolled; otherwise the
			// rendered window's start auto-advances when more events
			// land (see renderEventPanel).
			if !m.userScrolled && p.stageIdx == m.cursorIdx {
				m.scrollOffset = 0
			}
		}
		m.pendingLines = nil
		if m.phase == phaseRunning {
			return m, throttleTickCmd()
		}
		return m, nil
	case stepEndMsg:
		if i, ok := m.stageIdx[msg.stage]; ok && msg.idx < len(m.stages[i].steps) {
			step := &m.stages[i].steps[msg.idx]
			step.endedAt = time.Now()
			step.output = truncateOutput(msg.output, maxOutputLines)
			step.err = msg.err
			if msg.err != nil {
				step.state = stateFailed
			} else {
				step.state = stateDone
			}
			if m.stages[i].runningStepIdx == msg.idx {
				m.stages[i].runningStepIdx = -1
			}
		}
	case pipelineDoneMsg:
		// PLAN-2 / F7: don't auto-quit. Transition to phaseCompleted
		// so the final-report row appears in the stage list, the
		// banner replaces the keybind hint, and the user can scroll
		// through stage history before pressing q.
		//
		// PLAN-2 / F2: drain any pending throttled events before
		// transitioning — the final report's per-stage event counts
		// must reflect the full stream, not whatever was sitting in
		// the queue at the moment the pipeline goroutine finished.
		for _, p := range m.pendingLines {
			if p.stageIdx >= 0 && p.stageIdx < len(m.stages) {
				m.stages[p.stageIdx].events = append(m.stages[p.stageIdx].events, p.ev)
			}
		}
		m.pendingLines = nil
		m.phase = phaseCompleted
		m.finalErr = msg.err
		// Move the cursor to the synthetic final-report row so the
		// event panel opens on the summary by default. cursorIdx ==
		// len(m.stages) addresses the report slot.
		m.cursorIdx = len(m.stages)
		m.scrollOffset = 0
		m.userScrolled = false
	}
	return m, nil
}

func (m pipelineModel) View() string { //nolint:gocritic // Bubble Tea requires value receivers on tea.Model
	if m.width == 0 {
		return "initializing…"
	}
	// Three-panel layout (PLAN-1 / I4):
	//   row 1: left = live event panel (~70%); right = stage list (~30%)
	//   row 2: bottom status strip
	//   row 3: keybind hint footer
	// Borders + padding eat 4 columns total across the horizontal pair.
	// Layout reservation constants are package-level (top of file) so
	// the F8 scroll path can recover eventPanelHeight without
	// duplicating the formula.
	//
	// PLAN-2 / F4: narrow terminals (< narrowLayoutThreshold cols) drop
	// the right column entirely; the stage list collapses to a single
	// horizontal stepper row above the event panel.
	if m.width < narrowLayoutThreshold {
		return m.viewNarrow()
	}
	rightWidth := max(m.width/3, stageListWidthMin) //nolint:mnd // 1/3 split: events panel keeps the lion's share
	leftWidth := max(m.width-rightWidth-panelBorderOverhead, eventPanelWidthMin)
	panelHeight := max(m.height-statusRowReserve, panelHeightMin)

	header := m.renderHeader()
	leftPanel := pipelinePanelStyle.Width(leftWidth).Height(panelHeight).Render(
		pipelineHeaderStyle.Render(header) + "\n" + m.renderEventPanel(leftWidth-panelBorderOverhead, panelHeight-headerRowReserve),
	)
	rightPanel := pipelinePanelStyle.Width(rightWidth).Height(panelHeight).Render(
		pipelineHeaderStyle.Render("stages") + "\n" + m.renderStageList(),
	)

	view := lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, rightPanel)
	view += "\n" + m.renderStatusStrip()
	if m.phase == phaseCompleted {
		view += "\n" + m.renderCompletionBanner()
	} else {
		view += "\n" + m.renderKeybindHint()
	}
	if m.modal == modalQuitConfirm {
		view = m.overlayQuitModal(view)
	}
	return view
}

// renderHeader is the event-panel title row. In modeLive it shows the
// currently-active skill + step ("apex-create-architecture · step-04");
// in modePinned it shows "pinned: <stage>"; on the F7 final-report
// row (cursorIdx == len(stages) in phaseCompleted) it shows
// "final report".
func (m pipelineModel) renderHeader() string { //nolint:gocritic // Bubble Tea value receivers
	if m.cursorIdx == len(m.stages) && m.phase == phaseCompleted {
		return "final report"
	}
	if m.cursorIdx < 0 || m.cursorIdx >= len(m.stages) {
		return "Pipeline: " + m.pipelineName
	}
	st := m.stages[m.cursorIdx]
	if m.mode == modePinned {
		return "pinned: " + st.name
	}
	if st.runningStepIdx >= 0 && st.runningStepIdx < len(st.steps) {
		return st.steps[st.runningStepIdx].skill
	}
	if len(st.steps) > 0 {
		return st.steps[0].skill
	}
	return st.name
}

// renderEventPanel shows the per-stage rendered events for the
// cursor's stage. PLAN-2 / F8: when the user hasn't actively scrolled
// (userScrolled=false) the panel auto-tails so the latest event stays
// visible regardless of view mode; once the user presses PgUp / Home /
// pin via Enter, scrollOffset takes over and the view is held until
// L (return-to-live) or End rejoins the tail. PLAN-2 / F7: when the
// cursor sits on the synthetic final-report row, the per-stage summary
// table replaces the events stream.
func (m pipelineModel) renderEventPanel(width, height int) string { //nolint:gocritic // Bubble Tea value receivers
	if m.cursorIdx == len(m.stages) && m.phase == phaseCompleted {
		return m.renderFinalReport(width, height)
	}
	if m.cursorIdx < 0 || m.cursorIdx >= len(m.stages) {
		return dimStyle.Render("waiting for first stage…")
	}
	if height < 1 {
		height = 1
	}
	events := m.stages[m.cursorIdx].events
	if len(events) == 0 {
		return dimStyle.Render("…")
	}
	start := 0
	if len(events) > height {
		if m.userScrolled {
			start = max(min(m.scrollOffset, len(events)-height), 0)
		} else {
			start = len(events) - height
		}
	}
	end := min(start+height, len(events))
	var sb strings.Builder
	for i := start; i < end; i++ {
		ev := events[i]
		style := eventKindStyle(ev.Kind)
		switch m.renderStyle {
		case styleRawJSON:
			sb.WriteString(style.Render(truncateForWidth(ev.Raw, width)))
			sb.WriteString("\n")
		case styleBoth:
			// Both: human row, then a dim raw row beneath it. Each
			// counts as one rendered line toward the height budget,
			// which can compress the visible span — accepted as a
			// per-style cost. PLAN-2 / F3 calls this out.
			sb.WriteString(style.Render(truncateForWidth(ev.Glyph+" "+ev.Body, width)))
			sb.WriteString("\n")
			sb.WriteString(dimStyle.Render(truncateForWidth(ev.Raw, width)))
			sb.WriteString("\n")
		default: // styleHuman
			sb.WriteString(style.Render(truncateForWidth(ev.Glyph+" "+ev.Body, width)))
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// viewNarrow renders the single-column F4 layout for terminals
// under narrowLayoutThreshold columns. The right-side stage panel is
// gone; a one-row horizontal stepper sits above the full-width event
// panel. Cursor, scroll, modal handling are all unchanged — only the
// rendering layout differs.
func (m pipelineModel) viewNarrow() string { //nolint:gocritic // Bubble Tea value receivers
	stepperRow := m.renderStageStepper(m.width)
	panelWidth := max(m.width, eventPanelWidthMin)
	// Reserve the stepper row plus its blank separator from the
	// event-panel height budget.
	panelHeight := max(m.height-statusRowReserve-2, panelHeightMin) //nolint:mnd // 2 = stepper row + spacer separator inside the narrow layout

	header := m.renderHeader()
	panel := pipelinePanelStyle.Width(panelWidth).Height(panelHeight).Render(
		pipelineHeaderStyle.Render(header) + "\n" + m.renderEventPanel(panelWidth-panelBorderOverhead, panelHeight-headerRowReserve),
	)
	view := stepperRow + "\n" + panel
	view += "\n" + m.renderStatusStrip()
	if m.phase == phaseCompleted {
		view += "\n" + m.renderCompletionBanner()
	} else {
		view += "\n" + m.renderKeybindHint()
	}
	if m.modal == modalQuitConfirm {
		view = m.overlayQuitModal(view)
	}
	return view
}

// renderStageStepper produces the one-row F4 stage strip:
//
//	✓ design  ✓ shard  ▸ ux  · arch  · …
//
// The cursor stage is wrapped in [ ] for visibility. The 📊 final-
// report row participates when phase==phaseCompleted.
func (m pipelineModel) renderStageStepper(_ int) string { //nolint:gocritic // Bubble Tea value receivers
	var sb strings.Builder
	for i := range m.stages {
		st := &m.stages[i]
		glyph, style := glyphForState(st.state)
		row := glyph + " " + st.name
		if i == m.cursorIdx {
			row = "[" + row + "]"
		}
		if i > 0 {
			sb.WriteString("  ")
		}
		sb.WriteString(style.Render(row))
	}
	if m.phase == phaseCompleted {
		row := "📊 report"
		if m.cursorIdx == len(m.stages) {
			row = "[" + row + "]"
		}
		sb.WriteString("  ")
		sb.WriteString(stepStyle.Render(row))
	}
	return sb.String()
}

// renderStageList draws the right-side stage list with status glyph,
// stage name, and elapsed time. The cursor row is marked with ">".
// PLAN-2 / F7: in phaseCompleted, a synthetic "📊 final report" row
// appends to the list; selecting it (cursor at len(stages)) populates
// the event panel with the per-stage summary table.
func (m pipelineModel) renderStageList() string { //nolint:gocritic // Bubble Tea value receivers
	const cursorMark, noCursorMark = "> ", "  "
	var sb strings.Builder
	for i := range m.stages {
		st := &m.stages[i]
		cursor := noCursorMark
		if i == m.cursorIdx {
			cursor = cursorMark
		}
		glyph, style := glyphForState(st.state)
		row := cursor + glyph + " " + st.name
		if dur := elapsedFor(st.state, st.startedAt, st.endedAt, m.tick); dur != "" {
			row += " " + dimStyle.Render(dur)
		}
		sb.WriteString(style.Render(row))
		sb.WriteString("\n")
	}
	if m.phase == phaseCompleted {
		cursor := noCursorMark
		if m.cursorIdx == len(m.stages) {
			cursor = cursorMark
		}
		row := cursor + "📊 final report"
		sb.WriteString(stepStyle.Render(row))
		sb.WriteString("\n")
	}
	return sb.String()
}

// renderStatusStrip is the bottom-row summary of the cursor's stage:
// stage name · running step (if any) · elapsed time · final verdict.
// PLAN-2 / F7: on the final-report row, summarizes the aggregate result.
func (m pipelineModel) renderStatusStrip() string { //nolint:gocritic // Bubble Tea value receivers
	if m.cursorIdx == len(m.stages) && m.phase == phaseCompleted {
		ok, failed, _ := m.tallyStages()
		verdict := "✓ all stages passed"
		if failed > 0 {
			verdict = fmt.Sprintf("✗ %d stage(s) failed", failed)
		}
		return dimStyle.Render(fmt.Sprintf("status: final report · %d ok · %s", ok, verdict))
	}
	if m.cursorIdx < 0 || m.cursorIdx >= len(m.stages) {
		return dimStyle.Render("status: waiting")
	}
	st := m.stages[m.cursorIdx]
	parts := []string{st.name}
	if st.state == stateRunning && st.runningStepIdx >= 0 && st.runningStepIdx < len(st.steps) {
		parts = append(parts, fmt.Sprintf("▸ step %d/%d (%s)", st.runningStepIdx+1, len(st.steps), st.steps[st.runningStepIdx].skill))
	}
	if dur := elapsedFor(st.state, st.startedAt, st.endedAt, m.tick); dur != "" {
		parts = append(parts, dur+" elapsed")
	}
	switch st.state {
	case stateDone:
		parts = append(parts, "✓ pass")
	case stateFailed:
		parts = append(parts, "✗ fail")
	case stateRunning, statePending:
		// no verdict yet
	}
	return dimStyle.Render("status: " + strings.Join(parts, " · "))
}

// renderKeybindHint is the bottom-most row when the pipeline is still
// running. Lists the canonical keybindings so the user sees them
// without consulting docs.
func (m pipelineModel) renderKeybindHint() string { //nolint:gocritic // Bubble Tea value receivers
	mode := "live"
	if m.mode == modePinned {
		mode = "pinned"
	}
	style := "human"
	switch m.renderStyle {
	case styleRawJSON:
		style = "raw"
	case styleBoth:
		style = "both"
	case styleHuman:
		style = "human"
	}
	return dimStyle.Render(fmt.Sprintf(
		" [mode: %s] [style: %s] [↑↓ stage] [Enter pin] [L live] [PgUp/PgDn scroll] [r style] [q quit] ",
		mode, style,
	))
}

// followActive moves the cursor back to the running stage when the
// user returns to modeLive. If no stage is running, the cursor goes
// to the most recently active row. Returns the modified model so
// it composes with the Bubble Tea value-receiver Update idiom.
func (m pipelineModel) followActive() pipelineModel { //nolint:gocritic // Bubble Tea value receivers
	for i := range m.stages {
		if m.stages[i].state == stateRunning {
			m.cursorIdx = i
			return m
		}
	}
	for i := len(m.stages) - 1; i >= 0; i-- {
		if m.stages[i].state != statePending {
			m.cursorIdx = i
			return m
		}
	}
	return m
}

// scrollUp / scrollDown / scrollToBottom move the event-panel
// viewport. PLAN-2 / F8: dropped the modePinned guard so PgUp / PgDn
// work in any mode. The first PgUp from auto-tail (userScrolled=false)
// seeds scrollOffset to the current tail-window so the user sees
// continuous backwards motion instead of jumping to the top.
func (m pipelineModel) scrollUp() pipelineModel { //nolint:gocritic // Bubble Tea value receivers
	if !m.userScrolled {
		m.scrollOffset = m.tailScrollOffset()
		m.userScrolled = true
	}
	if m.scrollOffset >= pageStep {
		m.scrollOffset -= pageStep
	} else {
		m.scrollOffset = 0
	}
	return m
}

func (m pipelineModel) scrollDown() pipelineModel { //nolint:gocritic // Bubble Tea value receivers
	if !m.userScrolled {
		// Already auto-tailing; PgDn from tail re-confirms tail.
		return m
	}
	tail := m.tailScrollOffset()
	m.scrollOffset += pageStep
	if m.scrollOffset >= tail {
		// Reached the tail — drop back into auto-tail so future
		// events flow into the panel automatically.
		m.scrollOffset = 0
		m.userScrolled = false
	}
	return m
}

func (m pipelineModel) scrollToBottom() pipelineModel { //nolint:gocritic // Bubble Tea value receivers
	m.scrollOffset = 0
	m.userScrolled = false
	return m
}

// tailScrollOffset returns the scrollOffset value that anchors the
// rendered window at the tail of the cursor stage's events — i.e.,
// the latest event is on the bottom row. Falls back to 0 when the
// terminal hasn't been sized yet (eventPanelHeightFor floors at 1) or
// when the cursor is out of range.
func (m pipelineModel) tailScrollOffset() int { //nolint:gocritic // Bubble Tea value receivers
	if m.cursorIdx < 0 || m.cursorIdx >= len(m.stages) {
		return 0
	}
	total := len(m.stages[m.cursorIdx].events)
	h := eventPanelHeightFor(m.height)
	if total <= h {
		return 0
	}
	return total - h
}

// truncateForWidth shortens a single rendered line if it exceeds the
// panel width. Byte-counted; one trailing "…" replaces the tail. The
// event renderer already enforces per-event ceilings, so this is a
// belt-and-suspenders for narrow terminals.
func truncateForWidth(s string, w int) string {
	const minWidthForEllipsis = 2
	if w <= 0 || len(s) <= w {
		return s
	}
	if w < minWidthForEllipsis {
		return s[:w]
	}
	return s[:w-1] + "…"
}

// eventKindStyle maps a RenderedEvent.Kind to its lipgloss style.
// Kept separate from the renderer so the renderer stays
// presentation-agnostic and unit-testable.
func eventKindStyle(k EventKind) lipgloss.Style {
	switch k {
	case EventText:
		return stepStyle
	case EventTool:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("14")) // cyan
	case EventToolResult:
		return dimStyle
	case EventToolError:
		return stageFailedStyle
	case EventSuccess:
		return stageDoneStyle
	case EventFailure:
		return stageFailedStyle
	case EventSystem, EventUnknown:
		return dimStyle
	case EventSuppressed:
		return dimStyle
	}
	return stepStyle
}

// invokeCancel calls the cancellation function for the in-flight
// pipeline run, if one was provided to NewPipelineModel. Safe to call
// when the pipeline is already finished or the cancel was never set;
// the function is idempotent because context.CancelFunc is.
func (m pipelineModel) invokeCancel() { //nolint:gocritic // Bubble Tea value receivers
	if m.cancelRun != nil {
		m.cancelRun()
	}
}

// overlayQuitModal renders the quit-confirmation overlay centered on
// top of the underlying view. lipgloss.Place fills the terminal area
// with the modal box centered against a whitespace background, which
// visually replaces the underlying view for the duration of the
// modal; the underlying string is the caller's responsibility to
// suppress.
func (m pipelineModel) overlayQuitModal(_ string) string { //nolint:gocritic // Bubble Tea value receivers
	running, completed := m.summarizeRunState()
	body := "Stop pipeline?\n\n"
	if running != "" {
		body += dimStyle.Render(running) + "\n"
	}
	if completed != "" {
		body += dimStyle.Render(completed) + "\n"
	}
	body += "\nPressing y will cancel the in-flight skill\n"
	body += "subprocess and abort the run.\n\n"
	body += "  [y] yes   [n] no  "
	box := modalStyle.Render(body)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box,
		lipgloss.WithWhitespaceChars(" "),
	)
}

// summarizeRunState returns a short description of the active stage
// (if any) and a count of completed stages — used inside the quit
// modal so the user knows what's at risk.
func (m pipelineModel) summarizeRunState() (running, completed string) { //nolint:gocritic // Bubble Tea value receivers
	doneCount := 0
	for i := range m.stages {
		st := &m.stages[i]
		switch st.state {
		case stateRunning:
			running = fmt.Sprintf("%s in progress (%s)", st.name, elapsedFor(st.state, st.startedAt, st.endedAt, m.tick))
		case stateDone, stateFailed:
			doneCount++
		case statePending:
			// not yet started — irrelevant to the modal
		}
	}
	if doneCount > 0 {
		completed = fmt.Sprintf("%d stage(s) already completed", doneCount)
	}
	return running, completed
}

func glyphForState(s stageState) (string, lipgloss.Style) {
	switch s {
	case stateRunning:
		return "▶", stageRunningStyle
	case stateDone:
		return "✓", stageDoneStyle
	case stateFailed:
		return "✗", stageFailedStyle
	case statePending:
		return "⏳", stagePendingStyle
	}
	return "⏳", stagePendingStyle
}

func elapsedFor(state stageState, started, ended, now time.Time) string {
	switch state {
	case stateRunning:
		if started.IsZero() {
			return ""
		}
		return formatDur(now.Sub(started))
	case stateDone, stateFailed:
		if started.IsZero() || ended.IsZero() {
			return ""
		}
		return formatDur(ended.Sub(started))
	case statePending:
		return ""
	}
	return ""
}

func formatDur(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60) //nolint:mnd // 60 is seconds-per-minute, a well-known constant
}

// renderCompletionBanner replaces the keybind-hint footer once the
// pipeline finishes (PLAN-2 / F7). The keybind hint is still useful
// in spirit, but the dominant call to action is "you can quit now"
// and the dominant fact is "did it pass?" — so a single line carries
// both.
func (m pipelineModel) renderCompletionBanner() string { //nolint:gocritic // Bubble Tea requires value receivers on tea.Model helper methods
	ok, failed, total := m.tallyStages()
	verdict := stageDoneStyle.Render(fmt.Sprintf("✓ pipeline complete: %d/%d stages OK", ok, total))
	if failed > 0 {
		verdict = stageFailedStyle.Render(fmt.Sprintf("✗ pipeline failed: %d/%d FAILED", failed, total))
	}
	hint := dimStyle.Render("  · [↑↓] stage · [PgUp/PgDn] scroll · [q] quit")
	return verdict + hint
}

// renderFinalReport produces the per-stage summary table shown in the
// event panel when the user selects the synthetic final-report row
// (PLAN-2 / F7). Layout: one row per stage with status glyph, name,
// wall-clock duration, displayable event count, and the first line
// of the failing error if any.
func (m pipelineModel) renderFinalReport(width, _ int) string { //nolint:gocritic // Bubble Tea value receivers
	if len(m.stages) == 0 {
		return dimStyle.Render("(no stages ran)")
	}
	var sb strings.Builder
	for i := range m.stages {
		st := &m.stages[i]
		glyph, style := glyphForState(st.state)
		dur := elapsedFor(st.state, st.startedAt, st.endedAt, m.tick)
		if dur == "" {
			dur = "—"
		}
		line := fmt.Sprintf("%s %s · %s · %d event(s)", glyph, st.name, dur, len(st.events))
		if st.err != nil {
			line += " · ⚠ " + firstLineTruncated(st.err.Error(), maxToolErrorLen)
		}
		sb.WriteString(style.Render(truncateForWidth(line, width)))
		sb.WriteString("\n")
	}
	if m.finalErr != nil {
		sb.WriteString("\n")
		sb.WriteString(stageFailedStyle.Render(truncateForWidth("pipeline error: "+m.finalErr.Error(), width)))
		sb.WriteString("\n")
	}
	return sb.String()
}

// tallyStages counts passed / failed / total stages for the
// completion banner and status strip. Pending-but-never-ran stages
// (a halt-on-failure scenario) count toward total but not toward
// ok or failed.
func (m pipelineModel) tallyStages() (ok, failed, total int) { //nolint:gocritic // Bubble Tea value receivers
	total = len(m.stages)
	for i := range m.stages {
		switch m.stages[i].state {
		case stateDone:
			ok++
		case stateFailed:
			failed++
		case stateRunning, statePending:
			// not counted toward either tally
		}
	}
	return ok, failed, total
}

// truncateOutput keeps the last n non-empty lines of s, prefixed with
// an ellipsis line if anything was dropped.
func truncateOutput(s string, n int) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return s
	}
	return "…\n" + strings.Join(lines[len(lines)-n:], "\n")
}

// wrapForWidth wraps each line of s at the given width, returning the
// joined result. Soft wrap only — does not split words across lines
// since that would corrupt structured output.
//
//nolint:unused // retained for future use by the event panel; called nowhere today
func wrapForWidth(s string, width int) string {
	if width <= 0 {
		return s
	}
	out := make([]string, 0, strings.Count(s, "\n")+1)
	for line := range strings.SplitSeq(s, "\n") {
		for len(line) > width {
			cut := width
			if sp := strings.LastIndexByte(line[:cut], ' '); sp > width/2 {
				cut = sp
			}
			out = append(out, line[:cut])
			line = strings.TrimLeft(line[cut:], " ")
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// ─────────── Observer that forwards events to a tea.Program ───────────

// PipelineTUIObserver implements pipeline.Observer by sending tea.Msgs
// to the program. Returned by NewPipelineTUIObserver. The program
// owner is responsible for calling Done() once Run() returns so the
// model receives pipelineDoneMsg and quits.
type PipelineTUIObserver struct {
	program *tea.Program
}

func NewPipelineTUIObserver(p *tea.Program) *PipelineTUIObserver {
	return &PipelineTUIObserver{program: p}
}

func (o *PipelineTUIObserver) Done(err error) {
	o.program.Send(pipelineDoneMsg{err: err})
}

func (o *PipelineTUIObserver) OnStageStart(stage string) {
	o.program.Send(stageStartMsg{stage: stage})
}

func (o *PipelineTUIObserver) OnStageEnd(stage string, dur time.Duration, err error) {
	o.program.Send(stageEndMsg{stage: stage, dur: dur, err: err})
}

func (o *PipelineTUIObserver) OnStepStart(stage string, idx int, step pipeline.Step) { //nolint:gocritic // Step is passed by value to match the Observer interface signature
	o.program.Send(stepStartMsg{stage: stage, idx: idx, step: step})
}

func (o *PipelineTUIObserver) OnStepLine(stage string, idx int, line string) {
	o.program.Send(stepLineMsg{stage: stage, idx: idx, line: line})
}

func (o *PipelineTUIObserver) OnStepEnd(stage string, idx int, step pipeline.Step, dur time.Duration, output string, err error) { //nolint:gocritic // Step is passed by value to match the Observer interface signature
	o.program.Send(stepEndMsg{stage: stage, idx: idx, step: step, dur: dur, output: output, err: err})
}
