package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/diegosz/apex_process_ape/internal/bridge/orchestrator"
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
				Padding(1, 3).
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

// EventSource selects which input the pipeline model ingests for its
// per-stage event feed. PLAN-7 / FA — single-model unification.
//
//   - SourceStreamJSON consumes stepLineMsg from a `claude -p`
//     stream-json subprocess (the programmatic exec mode).
//   - SourceHookEvents consumes hookEventMsg from the bridge's hook
//     fan-out (the interactive REPL exec mode), routed through
//     RenderHookEvent.
//
// Both sources produce the same RenderedEvent shape, so all panel /
// scroll / render-style machinery is source-agnostic. The default is
// SourceStreamJSON — existing call sites are unchanged.
type EventSource int

const (
	// SourceStreamJSON is the PLAN-1 / I4b stream-json line source —
	// the default for `--tui -P` and `--web -P`.
	SourceStreamJSON EventSource = iota
	// SourceHookEvents is the PLAN-7 / FC bridge-hook source — used
	// by `--tui` interactive mode where no stream-json is available.
	SourceHookEvents
)

// awaitReplySender is the callback the unified model invokes when the
// user submits a reply in the await-message modal. Wired by
// runWithInteractiveTUI to BridgeRuntime.SendMessage. Nil disables the
// modal entirely — awaitPendingMsg becomes a no-op (PLAN-7 / FA).
type awaitReplySender func(content string)

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

	// source selects which input feeds the per-stage events slice.
	// PLAN-7 / FA. Default SourceStreamJSON keeps the existing
	// programmatic-mode behavior; SourceHookEvents enables the
	// hookEventMsg path used by interactive mode.
	source EventSource
	// awaitReplySender is invoked when the user submits text in the
	// await-message modal. Nil disables the modal entirely (PLAN-7 /
	// FA — programmatic mode never reaches it).
	awaitReplySender awaitReplySender
	// replyInput is the text input rendered inside the await modal.
	// Zero-value when awaitReplySender is nil — the field is never
	// touched.
	replyInput textinput.Model
	// awaitActive is set when the bridge reports a parked
	// await_message MCP call and cleared on user submit / bridge
	// resolve. Mutually exclusive with the quit-confirm modal via
	// Ctrl+C bypass — Ctrl+C still wins even with the modal open.
	awaitActive bool
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

// hookEventMsg carries a bridge HookEvent into the unified model
// (PLAN-7 / FA). The model parses h.Step ("stagename/idx-skill") to
// route the rendered event to the correct stage. Only consumed when
// source == SourceHookEvents; under SourceStreamJSON it is dropped.
type hookEventMsg struct {
	hook orchestrator.HookEvent
}

// awaitPendingMsg signals the bridge parked an `await_message` MCP
// tool call — a skill is waiting on user input mid-step. No-op if
// awaitReplySender is nil. PLAN-7 / FA.
type awaitPendingMsg struct{}

// awaitResolvedMsg signals the parked await_message was flushed
// (either by this TUI's reply submit or upstream). PLAN-7 / FA.
type awaitResolvedMsg struct{}

// PipelineModelOption applies an optional configuration to
// NewPipelineModel. The default constructor (no opts) reproduces the
// PLAN-2 behavior exactly — SourceStreamJSON, no await modal —
// keeping every pre-PLAN-7 call site unchanged.
type PipelineModelOption func(*pipelineModel)

// WithEventSource selects the event-ingestion source. Default is
// SourceStreamJSON. PLAN-7 / FA.
func WithEventSource(s EventSource) PipelineModelOption {
	return func(m *pipelineModel) { m.source = s }
}

// WithAwaitReplySender wires the await-message reply path. When set,
// the model accepts awaitPendingMsg and renders the textinput modal;
// pressing Enter inside the modal invokes the sender with the input
// content. Nil (the default) keeps the modal unreachable — the path
// is dead code in programmatic mode. PLAN-7 / FA.
func WithAwaitReplySender(fn awaitReplySender) PipelineModelOption {
	return func(m *pipelineModel) {
		m.awaitReplySender = fn
		if fn != nil {
			ti := textinput.New()
			ti.Placeholder = "type reply, then Enter (Esc to clear)"
			ti.CharLimit = 4096
			m.replyInput = ti
		}
	}
}

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
//
// opts customize the model further — typically WithEventSource and
// WithAwaitReplySender for the interactive-mode path (PLAN-7 / FA).
func NewPipelineModel(spec *pipeline.Spec, cancel context.CancelFunc, projectRoot string, opts ...PipelineModelOption) pipelineModel { //nolint:revive // returning unexported type is intentional; callers receive tea.Model via assignment
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
	m := pipelineModel{
		pipelineName: spec.Name,
		projectRoot:  projectRoot,
		stages:       rows,
		stageIdx:     idx,
		cancelRun:    cancel,
		mode:         modeLive,
		source:       SourceStreamJSON,
	}
	for _, opt := range opts {
		opt(&m)
	}
	return m
}

func (m pipelineModel) Init() tea.Cmd {
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
//nolint:gocyclo,maintidx // Bubble Tea requires value receivers on tea.Model; Update is intrinsically a wide message switch
func (m pipelineModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		key := msg.String()
		// PLAN-7 / FA: await-message modal is the highest-priority
		// overlay when active. Ctrl+C still wins (handled inside the
		// branch); Enter submits the reply; Esc clears the input but
		// leaves the modal up — the user can either type something
		// else or wait for the bridge to time out the parked call.
		if m.awaitActive {
			if next, cmd, handled := m.handleAwaitKey(msg, key); handled {
				return next, cmd
			}
		}
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
			m.renderStyle = (m.renderStyle + 1) % 3
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
		if m.source != SourceStreamJSON {
			// Defensive: under a hook-event source, stream-json lines
			// shouldn't arrive. Dropping them keeps the assumption
			// "events feed is single-source per run" honest.
			return m, nil
		}
		i, ok := m.stageIdx[msg.stage]
		if !ok {
			return m, nil
		}
		ev := RenderEventWithRoot(msg.line, m.projectRoot)
		if !ev.IsDisplayable() {
			return m, nil
		}
		m.pendingLines = append(m.pendingLines, pendingEvent{stageIdx: i, ev: ev})
	case hookEventMsg:
		// PLAN-7 / FA: hook-event source. Parse the rendered event,
		// route to the stage named by msg.hook.Step
		// ("stagename/idx-skill"), and queue into pendingLines so
		// the throttle (F2) drains it on the next tick alongside
		// stream-json events under the SourceStreamJSON path.
		if m.source != SourceHookEvents {
			return m, nil
		}
		stageName := stageFromHookStep(msg.hook.Step)
		i, ok := m.stageIdx[stageName]
		if !ok {
			return m, nil
		}
		ev := RenderHookEvent(msg.hook, m.projectRoot)
		if !ev.IsDisplayable() {
			return m, nil
		}
		// PLAN-7 follow-up: annotate Stop / SubagentStop rows with the
		// just-completed step's elapsed time. Stop arrives before
		// OnStepEnd fires (the runner waits on stepDoneCh which Stop
		// itself signals), so step.endedAt may not be set yet; compute
		// from time.Since(startedAt) at receive time and freeze the
		// annotation into the rendered body.
		if msg.hook.Event == "Stop" || msg.hook.Event == "SubagentStop" {
			if sIdx, ok := stepIdxFromHookStep(msg.hook.Step); ok && sIdx >= 0 && sIdx < len(m.stages[i].steps) {
				if started := m.stages[i].steps[sIdx].startedAt; !started.IsZero() {
					ev.Body += " · " + formatDur(time.Since(started))
				}
			}
		}
		m.pendingLines = append(m.pendingLines, pendingEvent{stageIdx: i, ev: ev})
	case awaitPendingMsg:
		// No-op if no sender is wired — programmatic mode never
		// reaches this branch.
		if m.awaitReplySender == nil {
			return m, nil
		}
		m.awaitActive = true
		m.replyInput.SetValue("")
		m.replyInput.Focus()
		return m, textinput.Blink
	case awaitResolvedMsg:
		if m.awaitReplySender == nil {
			return m, nil
		}
		m.awaitActive = false
		m.replyInput.Blur()
		return m, nil
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

func (m pipelineModel) View() string {
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
	rightWidth := max(m.width/3, stageListWidthMin)
	leftWidth := max(m.width-rightWidth-panelBorderOverhead, eventPanelWidthMin)
	panelHeight := max(m.height-statusRowReserve, panelHeightMin)

	header := m.renderHeader()
	// PLAN-7 / F0 follow-up: MaxHeight matches Height so any stray
	// visual wrap that escapes the per-row truncation can't grow the
	// box past panelHeight content lines. Width-2 accounts for the
	// horizontal Padding(0, 1) — the content area lipgloss lays out
	// each row inside.
	leftPanel := pipelinePanelStyle.Width(leftWidth).Height(panelHeight).MaxHeight(panelHeight + 2).Render(
		composePanelBody(
			pipelineHeaderStyle.Render(header),
			m.renderEventPanel(leftWidth-panelBorderOverhead, panelHeight-headerRowReserve),
			panelHeight,
		),
	)
	rightPanel := pipelinePanelStyle.Width(rightWidth).Height(panelHeight).MaxHeight(panelHeight + 2).Render(
		composePanelBody(
			pipelineHeaderStyle.Render("stages"),
			m.renderStageList(rightWidth-2),
			panelHeight,
		),
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
	if m.awaitActive {
		view = m.overlayAwaitModal(view)
	}
	return view
}

// renderHeader is the event-panel title row. In modeLive it shows the
// currently-active skill + step ("apex-create-architecture · step-04");
// in modePinned it shows "pinned: <stage>"; on the F7 final-report
// row (cursorIdx == len(stages) in phaseCompleted) it shows
// "final report".
func (m pipelineModel) renderHeader() string {
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
func (m pipelineModel) renderEventPanel(width, height int) string {
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
	// PLAN-7 / F0: linesPerEvent governs how many output rows a single
	// event consumes — styleBoth emits two (human + raw). The window
	// math derives maxEvents from height so the rendered line count
	// never exceeds the budget. scrollOffset and tailScrollOffset
	// remain in event-index space (PLAN-2 idiom); styleBoth therefore
	// shows fewer events per page, which is intentional.
	linesPerEvent := 1
	if m.renderStyle == styleBoth {
		linesPerEvent = 2
	}
	maxEvents := max(height/linesPerEvent, 1)
	start := 0
	if len(events) > maxEvents {
		if m.userScrolled {
			start = max(min(m.scrollOffset, len(events)-maxEvents), 0)
		} else {
			start = len(events) - maxEvents
		}
	}
	end := min(start+maxEvents, len(events))
	lines := make([]string, 0, maxEvents*linesPerEvent)
	for i := start; i < end; i++ {
		ev := events[i]
		style := eventKindStyle(ev.Kind)
		switch m.renderStyle {
		case styleRawJSON:
			lines = append(lines, style.Render(truncateForWidth(ev.Raw, width)))
		case styleBoth:
			// Both: human row, then a dim raw row beneath it.
			lines = append(lines,
				style.Render(truncateForWidth(ev.Glyph+" "+ev.Body, width)),
				dimStyle.Render(truncateForWidth(ev.Raw, width)),
			)
		default: // styleHuman
			lines = append(lines, style.Render(truncateForWidth(ev.Glyph+" "+ev.Body, width)))
		}
	}
	// Belt-and-suspenders: cap line count even if the per-event branch
	// emitted more than the slice arithmetic predicted.
	if len(lines) > height {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}

// viewNarrow renders the single-column F4 layout for terminals
// under narrowLayoutThreshold columns. The right-side stage panel is
// gone; a one-row horizontal stepper sits above the full-width event
// panel. Cursor, scroll, modal handling are all unchanged — only the
// rendering layout differs.
func (m pipelineModel) viewNarrow() string {
	stepperRow := m.renderStageStepper(m.width)
	panelWidth := max(m.width, eventPanelWidthMin)
	// Reserve the stepper row plus its blank separator from the
	// event-panel height budget.
	panelHeight := max(m.height-statusRowReserve-2, panelHeightMin)

	header := m.renderHeader()
	panel := pipelinePanelStyle.Width(panelWidth).Height(panelHeight).MaxHeight(panelHeight + 2).Render(
		composePanelBody(
			pipelineHeaderStyle.Render(header),
			m.renderEventPanel(panelWidth-panelBorderOverhead, panelHeight-headerRowReserve),
			panelHeight,
		),
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
	if m.awaitActive {
		view = m.overlayAwaitModal(view)
	}
	return view
}

// renderStageStepper produces the one-row F4 stage strip:
//
//	✓ design  ✓ shard  ▸ ux  · arch  · …
//
// The cursor stage is wrapped in [ ] for visibility. The 📊 final-
// report row participates when phase==phaseCompleted.
func (m pipelineModel) renderStageStepper(_ int) string {
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
//
// PLAN-7 / F0 follow-up: `width` is the visual-cell budget per row
// (content area inside the panel — leftWidth/rightWidth minus
// padding). Rows wider than that get truncated; otherwise lipgloss
// soft-wraps them into a second visual line and the rendered box
// grows by one row, breaking left/right border alignment. Pass <=0
// to disable (only used by tests).
func (m pipelineModel) renderStageList(width int) string {
	const cursorMark, noCursorMark = "> ", "  "
	var sb strings.Builder
	emit := func(row string, style lipgloss.Style) {
		// Style after truncation: ANSI escapes don't count toward
		// the visual width lipgloss sees when laying out the panel.
		sb.WriteString(style.Render(truncateForVisualWidth(row, width)))
		sb.WriteString("\n")
	}
	for i := range m.stages {
		st := &m.stages[i]
		cursor := noCursorMark
		if i == m.cursorIdx {
			cursor = cursorMark
		}
		glyph, style := glyphForState(st.state)
		row := cursor + glyph + " " + st.name
		if dur := elapsedFor(st.state, st.startedAt, st.endedAt, m.tick); dur != "" {
			// Apply the dim style INLINE on the duration suffix —
			// but emit() truncates the un-styled string first, then
			// applies the row style. To preserve the dim duration
			// after truncation, render the row in two pieces only
			// when the un-styled row fits the budget.
			full := row + " " + dur
			if width <= 0 || lipgloss.Width(full) <= width {
				sb.WriteString(style.Render(row + " " + dimStyle.Render(dur)))
				sb.WriteString("\n")
				continue
			}
			row = full
		}
		emit(row, style)
	}
	if m.phase == phaseCompleted {
		cursor := noCursorMark
		if m.cursorIdx == len(m.stages) {
			cursor = cursorMark
		}
		emit(cursor+"📊 final report", stepStyle)
	}
	return sb.String()
}

// truncateForVisualWidth truncates s to at most w visual cells (not
// bytes) so lipgloss's panel-rendering soft-wrap doesn't grow the
// box. Counterpart to truncateForWidth, which is byte-counted; this
// one is visual-cell-counted so emoji + CJK + combining marks are
// handled correctly. PLAN-7 / F0 follow-up.
func truncateForVisualWidth(s string, w int) string {
	if w <= 0 || lipgloss.Width(s) <= w {
		return s
	}
	// Walk runes, accumulating visual width until adding the next
	// rune would exceed the budget; reserve 1 cell for the ellipsis.
	reserve := 1
	if w < reserve+1 {
		// Pathologically narrow — just hard-cut by rune count.
		out := make([]rune, 0, w)
		for _, r := range s {
			if len(out) >= w {
				break
			}
			out = append(out, r)
		}
		return string(out)
	}
	budget := w - reserve
	out := make([]rune, 0, len(s))
	used := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if used+rw > budget {
			break
		}
		out = append(out, r)
		used += rw
	}
	return string(out) + "…"
}

// renderStatusStrip is the bottom-row summary of the cursor's stage:
// stage name · running step (if any) · elapsed time · final verdict.
// PLAN-2 / F7: on the final-report row, summarizes the aggregate result.
func (m pipelineModel) renderStatusStrip() string {
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
		step := st.steps[st.runningStepIdx]
		label := fmt.Sprintf("▸ step %d/%d (%s)", st.runningStepIdx+1, len(st.steps), step.skill)
		if dur := elapsedFor(step.state, step.startedAt, step.endedAt, m.tick); dur != "" {
			label += " · " + dur
		}
		parts = append(parts, label)
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
func (m pipelineModel) renderKeybindHint() string {
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
func (m pipelineModel) followActive() pipelineModel {
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
func (m pipelineModel) scrollUp() pipelineModel {
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

func (m pipelineModel) scrollDown() pipelineModel {
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

func (m pipelineModel) scrollToBottom() pipelineModel {
	m.scrollOffset = 0
	m.userScrolled = false
	return m
}

// tailScrollOffset returns the scrollOffset value that anchors the
// rendered window at the tail of the cursor stage's events — i.e.,
// the latest event is on the bottom row. Falls back to 0 when the
// terminal hasn't been sized yet (eventPanelHeightFor floors at 1) or
// when the cursor is out of range.
func (m pipelineModel) tailScrollOffset() int {
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

// handleAwaitKey processes one keystroke while the await-message
// modal is open. Returns the next-state model, any tea.Cmd, and a
// `handled` flag — when false, Update falls through to the normal
// keybind switch (so Ctrl+C double-tap force-quit and the q quit
// modal still work even with the await modal up). PLAN-7 / FA.
func (m pipelineModel) handleAwaitKey(msg tea.KeyMsg, key string) (pipelineModel, tea.Cmd, bool) {
	switch key {
	case keyCtrlC:
		// Let the outer Update path handle Ctrl+C (quit-confirm /
		// double-tap force-quit). Returning handled=false routes to
		// the normal switch below.
		return m, nil, false
	case keyEnter:
		content := strings.TrimRight(m.replyInput.Value(), "\r\n")
		if content == "" {
			return m, nil, true
		}
		if m.awaitReplySender != nil {
			m.awaitReplySender(content)
		}
		m.awaitActive = false
		m.replyInput.Blur()
		m.replyInput.SetValue("")
		return m, nil, true
	case keyEsc:
		m.replyInput.SetValue("")
		return m, nil, true
	}
	var cmd tea.Cmd
	m.replyInput, cmd = m.replyInput.Update(msg)
	return m, cmd, true
}

// stageFromHookStep extracts the stage name from a hook event's Step
// label. The bridge tags hooks with "<stage>/<idx>-<skill>" (e.g.
// "pattern-governance/1-apex-pattern-reconciliation"); the
// post-slash suffix is the chain-internal step index + skill name and
// is irrelevant for stage routing. PLAN-7 / FA.
func stageFromHookStep(step string) string {
	if i := strings.IndexByte(step, '/'); i >= 0 {
		return step[:i]
	}
	return step
}

// stepIdxFromHookStep extracts the 0-based step index from a hook
// event's Step label ("<stage>/<idx>-<skill>"). The on-wire idx is
// 1-based per pipeline.StepLabel; the model's steps slice is 0-based,
// so we subtract one before returning. Returns (-1, false) when the
// label lacks a suffix or the idx isn't parsable.
func stepIdxFromHookStep(step string) (int, bool) {
	slash := strings.IndexByte(step, '/')
	if slash < 0 || slash >= len(step)-1 {
		return -1, false
	}
	suffix := step[slash+1:]
	dash := strings.IndexByte(suffix, '-')
	if dash <= 0 {
		return -1, false
	}
	n, err := strconv.Atoi(suffix[:dash])
	if err != nil || n < 1 {
		return -1, false
	}
	return n - 1, true
}

// composePanelBody assembles header + body as exactly `budget` lines
// so the rendered lipgloss box at Height(budget) renders at exactly
// that height — no border-misalignment growth from a stray trailing
// newline or styleBoth's 2-lines-per-event overflow.
//
// budget is the lipgloss content-height the caller passed to
// Height() (excludes border + padding rows since lipgloss adds those
// separately). The body string is concatenated to the header with a
// single newline; any trailing newline noise from
// pipelineHeaderStyle.MarginBottom or renderEventPanel is trimmed,
// then the line list is padded with blank rows if short and
// truncated if long. PLAN-7 / F0 — was the root cause of the
// left/right border drift visible once the event panel filled past
// its visible height.
func composePanelBody(header, body string, budget int) string {
	if budget < 1 {
		budget = 1
	}
	combined := header + "\n" + body
	combined = strings.TrimRight(combined, "\n\r")
	lines := strings.Split(combined, "\n")
	if len(lines) > budget {
		lines = lines[:budget]
	}
	for len(lines) < budget {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
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
func (m pipelineModel) invokeCancel() {
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
func (m pipelineModel) overlayQuitModal(_ string) string {
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

// overlayAwaitModal renders the await-message reply modal centered
// on top of the underlying view. PLAN-7 / FA — interactive mode's
// only modal type that programmatic mode lacks. The textinput
// component handles cursor blink + cursor positioning; the surrounding
// box delivers the framing.
func (m pipelineModel) overlayAwaitModal(_ string) string {
	body := "Skill is waiting for input (await_message)\n\n"
	body += m.replyInput.View() + "\n\n"
	body += dimStyle.Render("  [Enter] send · [Esc] clear · [Ctrl+C] cancel run  ")
	box := modalStyle.Render(body)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box,
		lipgloss.WithWhitespaceChars(" "),
	)
}

// summarizeRunState returns a short description of the active stage
// (if any) and a count of completed stages — used inside the quit
// modal so the user knows what's at risk.
func (m pipelineModel) summarizeRunState() (running, completed string) {
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
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// renderCompletionBanner replaces the keybind-hint footer once the
// pipeline finishes (PLAN-2 / F7). The keybind hint is still useful
// in spirit, but the dominant call to action is "you can quit now"
// and the dominant fact is "did it pass?" — so a single line carries
// both.
func (m pipelineModel) renderCompletionBanner() string {
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
func (m pipelineModel) renderFinalReport(width, _ int) string {
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
func (m pipelineModel) tallyStages() (ok, failed, total int) {
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

func (o *PipelineTUIObserver) OnStepStart(stage string, idx int, step pipeline.Step) {
	o.program.Send(stepStartMsg{stage: stage, idx: idx, step: step})
}

func (o *PipelineTUIObserver) OnStepLine(stage string, idx int, line string) {
	o.program.Send(stepLineMsg{stage: stage, idx: idx, line: line})
}

func (o *PipelineTUIObserver) OnStepEnd(stage string, idx int, step pipeline.Step, dur time.Duration, output string, err error) {
	o.program.Send(stepEndMsg{stage: stage, idx: idx, step: step, dur: dur, output: output, err: err})
}
