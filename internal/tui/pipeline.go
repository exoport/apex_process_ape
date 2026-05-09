package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/diegosz/apex_process_ape/internal/pipeline"
)

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
func NewPipelineModel(spec *pipeline.Spec) pipelineModel { //nolint:revive // returning unexported type is intentional; callers receive tea.Model via assignment
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
		switch msg.String() {
		case keyCtrlC, "q":
			if m.finished {
				return m, tea.Quit
			}
			// Mid-run cancel: leave the cleanup to the runner; just quit
			// the TUI. The runner's context cancellation is wired by the
			// caller (cmd.Context()).
			return m, tea.Quit
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
	return view
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
