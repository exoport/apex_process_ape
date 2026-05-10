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

type pipelineModel struct {
	pipelineName string
	stages       []stageRow
	stageIdx     map[string]int
	finished     bool
	finalErr     error
	width        int
	height       int
	tick         time.Time

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
	// the pinned stage's event log when rendering (PgUp / PgDn).
	// Always 0 in modeLive (auto-scroll keeps the latest visible).
	scrollOffset int
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
func NewPipelineModel(spec *pipeline.Spec, cancel context.CancelFunc) pipelineModel { //nolint:revive // returning unexported type is intentional; callers receive tea.Model via assignment
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
		stages:       rows,
		stageIdx:     idx,
		cancelRun:    cancel,
		mode:         modeLive,
	}
}

func (m pipelineModel) Init() tea.Cmd { //nolint:gocritic // Bubble Tea requires value receivers on tea.Model
	return tickCmd()
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(_ time.Time) tea.Msg { return tickMsg{} })
}

// Update dispatches one Bubble Tea message and returns the next-state
// model + any commands. The function is deliberately a long switch
// across every event type — gocyclo flags it, but extracting helpers
// just moves the same branching one call frame deeper and obscures
// the linear flow.
//
//nolint:gocritic,gocyclo // Bubble Tea requires value receivers on tea.Model; Update is intrinsically a wide message switch
func (m pipelineModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		key := msg.String()
		// Finished pipeline: any key dismisses. No confirmation needed —
		// nothing to cancel.
		if m.finished {
			return m, tea.Quit
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
			}
			return m, nil
		case "down", "j":
			if m.cursorIdx < len(m.stages)-1 {
				m.cursorIdx++
				m.scrollOffset = 0
			}
			return m, nil
		case keyEnter:
			m.mode = modePinned
			m.scrollOffset = 0
			return m, nil
		case "l", "L", keyEsc:
			m.mode = modeLive
			m.scrollOffset = 0
			m = m.followActive()
			return m, nil
		case "pgup":
			return m.scrollUp(), nil
		case "pgdown":
			return m.scrollDown(), nil
		case "home":
			m.scrollOffset = 0
			return m, nil
		case "end":
			return m.scrollToBottom(), nil
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
		if m.finished {
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
		// Per PLAN-1 / I4b: parse the raw stream-json line and
		// append the rendered event to the stage's per-stage feed.
		// Suppressed events (noisy successful tool_results, system
		// pings) are dropped at this layer.
		i, ok := m.stageIdx[msg.stage]
		if !ok {
			return m, nil
		}
		ev := RenderEvent(msg.line)
		if !ev.IsDisplayable() {
			return m, nil
		}
		m.stages[i].events = append(m.stages[i].events, ev)
		// Live mode auto-scrolls so the latest event is always
		// visible; pinned mode keeps the user's scroll position
		// stable so reviewing history isn't disrupted by new lines.
		if m.mode == modeLive && i == m.cursorIdx {
			m.scrollOffset = 0
		}
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
		m.finished = true
		m.finalErr = msg.err
		return m, tea.Quit
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
	const panelBorderOverhead = 4
	const (
		stageListWidthMin  = 28 // floor on the right column
		eventPanelWidthMin = 30 // floor on the left column
		panelHeightMin     = 6
		statusRowReserve   = 4 // status strip + footer + borders
		headerRowReserve   = 2 // event-panel inner header + spacing
	)
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
	if m.finished {
		view += "\n" + m.renderFooter()
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
// in modePinned it shows "pinned: <stage>".
func (m pipelineModel) renderHeader() string { //nolint:gocritic // Bubble Tea value receivers
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
// cursor's stage. In modeLive it auto-scrolls so the last visible row
// is the latest event; in modePinned PgUp/PgDn moves scrollOffset.
func (m pipelineModel) renderEventPanel(width, height int) string { //nolint:gocritic // Bubble Tea value receivers
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
	// Compute the window to display: in Live mode anchor the bottom
	// at len(events); in Pinned mode use scrollOffset.
	start := 0
	if len(events) > height {
		if m.mode == modeLive {
			start = len(events) - height
		} else {
			start = max(min(m.scrollOffset, len(events)-height), 0)
		}
	}
	end := min(start+height, len(events))
	var sb strings.Builder
	for i := start; i < end; i++ {
		ev := events[i]
		line := ev.Glyph + " " + ev.Body
		style := eventKindStyle(ev.Kind)
		sb.WriteString(style.Render(truncateForWidth(line, width)))
		sb.WriteString("\n")
	}
	return sb.String()
}

// renderStageList draws the right-side stage list with status glyph,
// stage name, and elapsed time. The cursor row is marked with ">".
func (m pipelineModel) renderStageList() string { //nolint:gocritic // Bubble Tea value receivers
	var sb strings.Builder
	for i := range m.stages {
		st := &m.stages[i]
		cursor := "  "
		if i == m.cursorIdx {
			cursor = "> "
		}
		glyph, style := glyphForState(st.state)
		row := cursor + glyph + " " + st.name
		if dur := elapsedFor(st.state, st.startedAt, st.endedAt, m.tick); dur != "" {
			row += " " + dimStyle.Render(dur)
		}
		sb.WriteString(style.Render(row))
		sb.WriteString("\n")
	}
	return sb.String()
}

// renderStatusStrip is the bottom-row summary of the cursor's stage:
// stage name · running step (if any) · elapsed time · final verdict.
func (m pipelineModel) renderStatusStrip() string { //nolint:gocritic // Bubble Tea value receivers
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
	return dimStyle.Render(fmt.Sprintf(
		" [mode: %s] [↑↓ stage] [Enter pin] [L live] [PgUp/PgDn scroll] [q quit] ",
		mode,
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

// scrollUp / scrollDown / scrollToBottom move the event panel
// viewport when the user is in modePinned. In modeLive they're
// no-ops — auto-scroll keeps the latest visible.
func (m pipelineModel) scrollUp() pipelineModel { //nolint:gocritic // Bubble Tea value receivers
	if m.mode != modePinned {
		return m
	}
	const pageStep = 5
	if m.scrollOffset >= pageStep {
		m.scrollOffset -= pageStep
	} else {
		m.scrollOffset = 0
	}
	return m
}

func (m pipelineModel) scrollDown() pipelineModel { //nolint:gocritic // Bubble Tea value receivers
	if m.mode != modePinned {
		return m
	}
	const pageStep = 5
	m.scrollOffset += pageStep
	if m.cursorIdx >= 0 && m.cursorIdx < len(m.stages) {
		if total := len(m.stages[m.cursorIdx].events); m.scrollOffset > total {
			m.scrollOffset = total
		}
	}
	return m
}

func (m pipelineModel) scrollToBottom() pipelineModel { //nolint:gocritic // Bubble Tea value receivers
	if m.mode != modePinned || m.cursorIdx < 0 || m.cursorIdx >= len(m.stages) {
		return m
	}
	m.scrollOffset = len(m.stages[m.cursorIdx].events)
	return m
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

func (m pipelineModel) renderFooter() string { //nolint:gocritic // Bubble Tea requires value receivers on tea.Model helper methods
	if m.finalErr != nil {
		return stageFailedStyle.Render("✗ pipeline failed: ") + m.finalErr.Error() + "  " + dimStyle.Render("press any key to exit")
	}
	return stageDoneStyle.Render("✓ pipeline complete") + "  " + dimStyle.Render("press any key to exit")
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
