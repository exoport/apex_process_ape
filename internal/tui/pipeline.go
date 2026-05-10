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
}

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
	return pipelineModel{
		pipelineName: spec.Name,
		stages:       rows,
		stageIdx:     idx,
		cancelRun:    cancel,
	}
}

func (m pipelineModel) Init() tea.Cmd { //nolint:gocritic // Bubble Tea requires value receivers on tea.Model
	return tickCmd()
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(_ time.Time) tea.Msg { return tickMsg{} })
}

func (m pipelineModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) { //nolint:gocritic // Bubble Tea requires value receivers on tea.Model
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
		// No modal: q / Ctrl+C open the quit-confirmation modal.
		// A second Ctrl+C within doubleCtrlCWindow bypasses the modal
		// and force-quits (emergency escape).
		switch key {
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
		}
	case stageEndMsg:
		i, ok := m.stageIdx[msg.stage]
		if ok {
			m.stages[i].endedAt = time.Now()
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
	leftWidth := m.width / 2              //nolint:mnd // split layout evenly at midpoint
	rightWidth := m.width - leftWidth - 4 //nolint:mnd // 4 = border+padding overhead of two lipgloss panels
	rightWidth = max(rightWidth, 30)      //nolint:mnd // 30 columns is the minimum usable right-panel width

	leftBody := m.renderStageTree()
	rightBody := m.renderOutputPanel(rightWidth)

	leftPanel := pipelinePanelStyle.Width(leftWidth).Render(
		pipelineHeaderStyle.Render("Pipeline: "+m.pipelineName) + "\n" + leftBody,
	)
	rightPanel := pipelinePanelStyle.Width(rightWidth).Render(
		pipelineHeaderStyle.Render("Output") + "\n" + rightBody,
	)

	view := lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, rightPanel)
	if m.finished {
		view += "\n" + m.renderFooter()
	} else {
		view += "\n" + dimStyle.Render("press q or ctrl+c to quit")
	}
	if m.modal == modalQuitConfirm {
		view = m.overlayQuitModal(view)
	}
	return view
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

func (m pipelineModel) renderStageTree() string { //nolint:gocritic // Bubble Tea requires value receivers on tea.Model helper methods
	var sb strings.Builder
	for _, st := range m.stages {
		sb.WriteString(renderStageLine(st, m.tick))
		sb.WriteString("\n")
		for j, step := range st.steps {
			isLast := j == len(st.steps)-1
			sb.WriteString(renderStepLine(step, isLast, m.tick))
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func renderStageLine(st stageRow, now time.Time) string { //nolint:gocritic // stageRow is a snapshot passed by value for rendering; pointer would not be safe across concurrent updates
	glyph, style := glyphForState(st.state)
	elapsed := elapsedFor(st.state, st.startedAt, st.endedAt, now)
	line := fmt.Sprintf("%s %s", glyph, st.name)
	if elapsed != "" {
		line += "  " + dimStyle.Render(elapsed)
	}
	return style.Render(line)
}

func renderStepLine(step stepRow, isLast bool, now time.Time) string { //nolint:gocritic // stepRow is a snapshot passed by value for rendering
	branch := "├─"
	if isLast {
		branch = "└─"
	}
	glyph, _ := glyphForState(step.state)
	tag := step.skill
	if step.agent != "" {
		tag = step.agent + " → " + step.skill
	}
	elapsed := elapsedFor(step.state, step.startedAt, step.endedAt, now)
	out := fmt.Sprintf("    %s %s %s", branch, glyph, tag)
	if elapsed != "" {
		out += "  " + dimStyle.Render(elapsed)
	}
	return stepStyle.Render(out)
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

// renderOutputPanel finds the most recent step that has produced output
// (running with no output yet → "running…"; done/failed with output →
// the truncated output). PLAN-7 § Open issues: claude does not flush
// per-line, so the output appears all at once when each invocation
// returns.
func (m pipelineModel) renderOutputPanel(width int) string { //nolint:gocritic // Bubble Tea requires value receivers on tea.Model helper methods
	var latest *stepRow
	var latestStage string
	for i := range m.stages {
		st := &m.stages[i]
		for j := range st.steps {
			s := &st.steps[j]
			if s.state == statePending {
				break
			}
			latest = s
			latestStage = st.name
		}
	}
	if latest == nil {
		return dimStyle.Render("waiting for first step…")
	}
	header := fmt.Sprintf("%s / %s", latestStage, latest.skill)
	body := latest.output
	if body == "" {
		switch latest.state {
		case stateRunning:
			body = dimStyle.Render("running…")
		case stateDone:
			body = dimStyle.Render("(no output)")
		case stateFailed:
			if latest.err != nil {
				body = stageFailedStyle.Render(latest.err.Error())
			} else {
				body = stageFailedStyle.Render("failed (no output)")
			}
		case statePending:
			// pending steps have no output yet
		}
	}
	body = wrapForWidth(body, width-2) //nolint:mnd // 2 = subtract lipgloss panel border width
	return dimStyle.Render(header) + "\n" + body
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

func (o *PipelineTUIObserver) OnStepEnd(stage string, idx int, step pipeline.Step, dur time.Duration, output string, err error) { //nolint:gocritic // Step is passed by value to match the Observer interface signature
	o.program.Send(stepEndMsg{stage: stage, idx: idx, step: step, dur: dur, output: output, err: err})
}
