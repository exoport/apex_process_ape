// Package tui — interactive Bubble Tea model.
//
// PLAN-6 / Phase E follow-up: minimal interactive-mode TUI focused on
// the surfaces that don't exist in the plainObserver path:
//
//   - per-stage status overview (top panel)
//   - throttled hooks activity feed (middle panel; ~30fps tea.Tick)
//   - per-step output tail (bottom panel)
//
// Stop is reuse of the existing double-Ctrl+C convention from the
// programmatic PipelineModel. Await modal is deferred — the bridge
// runtime emits the events (RuntimeEventAwaitPending / Resolved) but
// only `apex-create-story` and a couple of `lift-project` skills
// surface them in practice; the modal lands in a focused follow-up.

package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/diegosz/apex_process_ape/internal/pipeline"
)

// interactiveStageState mirrors the lifecycle subset the TUI needs to
// render the stage list. PLAN-6 / Phase E TUI variant — distinct from
// the programmatic PipelineModel's stageRow because interactive mode
// has no per-step intra-stage line tail (chain runs in one claude).
type interactiveStageState struct {
	name   string
	status string // "pending", "running", "done", "failed"
	start  time.Time
	dur    time.Duration
	lastErr error
}

// interactiveHookRow is one line in the throttled hooks activity feed.
type interactiveHookRow struct {
	ts    time.Time
	event string
	step  string
}

// maxHookRows caps the hooks panel buffer. Older rows scroll out the
// top; cap matches the programmatic TUI's per-stage line tail bound.
const maxHookRows = 200

// hookFlushMsg fires on each throttle tick to drain pendingHooks into
// the visible hookRows slice. The tick interval matches the existing
// programmatic-TUI throttle (≈30 Hz).
type hookFlushMsg struct{}

// interactiveDoneMsg signals pipeline.Run returned.
type interactiveDoneMsg struct{ err error }

// awaitPendingMsg signals the bridge parked an `await_message` MCP
// tool call — a skill is waiting on user input mid-step.
type awaitPendingMsg struct{}

// awaitResolvedMsg signals a parked `await_message` was just flushed,
// either by an upstream caller (web) or by this TUI's reply submit.
type awaitResolvedMsg struct{}

// awaitReplySender is the callback invoked when the user submits the
// reply input. Wired by the caller (apecmd) to BridgeRuntime.SendMessage
// so the typed reply round-trips through the bridge IPC frame back to
// the parked MCP tool call.
type awaitReplySender func(content string)

// InteractiveModel is the Bubble Tea model for PLAN-6 interactive
// mode. Constructed by NewInteractiveModel and driven by an
// InteractiveObserver that adapts pipeline.Observer + bridge hook
// events into tea.Msg.
type InteractiveModel struct {
	stages       []interactiveStageState
	stageIdx     map[string]int
	currentStage string

	hookRows     []interactiveHookRow
	pendingHooks []interactiveHookRow
	pendingMu    sync.Mutex

	termWidth  int
	termHeight int

	phase     pipelinePhase
	finalErr  error
	runCancel context.CancelFunc

	lastCtrlC time.Time
	modal     modalState

	// await modal state — populated when the bridge emits
	// RuntimeEventAwaitPending; cleared on RuntimeEventAwaitResolved
	// or when the user submits a reply.
	awaitActive bool
	replyInput  textinput.Model
	sendReply   awaitReplySender
}

// NewInteractiveModel constructs an interactive TUI with the given
// pipeline spec (for stage seeds), a cancel func that fires when the
// user confirms quit, and a reply-sender invoked when the user
// submits text in the await modal. Pass a no-op sendReply when the
// caller doesn't intend to wire await_message support (the modal
// then never opens — RuntimeEventAwaitPending arrives, the model
// displays the input box, but submit goes nowhere; in practice the
// no-op only makes sense for tests). PLAN-6 / Phase E.
func NewInteractiveModel(spec *pipeline.Spec, runCancel context.CancelFunc, sendReply awaitReplySender) *InteractiveModel {
	ti := textinput.New()
	ti.Placeholder = "type reply, then Enter (Esc to cancel)"
	ti.CharLimit = 4096 //nolint:mnd // upper bound for a reply line; matches the IPC TypeMessage frame budget
	m := &InteractiveModel{
		stageIdx:   make(map[string]int),
		runCancel:  runCancel,
		phase:      phaseRunning,
		replyInput: ti,
		sendReply:  sendReply,
	}
	for _, st := range spec.Stages() {
		m.stageIdx[st.Name] = len(m.stages)
		m.stages = append(m.stages, interactiveStageState{name: st.Name, status: "pending"})
	}
	return m
}

// Init satisfies tea.Model; kicks off the throttle ticker.
func (m *InteractiveModel) Init() tea.Cmd { return tickHookFlush() }

func tickHookFlush() tea.Cmd {
	return tea.Tick(throttleTickInterval, func(time.Time) tea.Msg { return hookFlushMsg{} })
}

// Update routes tea.Msgs. Stage / step events come from
// InteractiveObserver; hook events come from PushHook (called from
// the BridgeRuntime fan-out goroutine).
//
//nolint:gocyclo // single dispatch on tea.Msg type; refactor would obscure
func (m *InteractiveModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case stageStartMsg:
		if i, ok := m.stageIdx[msg.stage]; ok {
			m.stages[i].status = "running"
			m.stages[i].start = time.Now()
			m.currentStage = msg.stage
		}
		return m, nil

	case stageEndMsg:
		if i, ok := m.stageIdx[msg.stage]; ok {
			m.stages[i].dur = msg.dur
			if msg.err != nil {
				m.stages[i].status = "failed"
				m.stages[i].lastErr = msg.err
			} else {
				m.stages[i].status = "done"
			}
		}
		return m, nil

	case hookFlushMsg:
		m.pendingMu.Lock()
		if len(m.pendingHooks) > 0 {
			m.hookRows = append(m.hookRows, m.pendingHooks...)
			if len(m.hookRows) > maxHookRows {
				m.hookRows = m.hookRows[len(m.hookRows)-maxHookRows:]
			}
			m.pendingHooks = m.pendingHooks[:0]
		}
		m.pendingMu.Unlock()
		if m.phase == phaseRunning {
			return m, tickHookFlush()
		}
		return m, nil

	case interactiveDoneMsg:
		m.phase = phaseCompleted
		m.finalErr = msg.err
		return m, nil

	case awaitPendingMsg:
		m.awaitActive = true
		m.replyInput.SetValue("")
		m.replyInput.Focus()
		return m, textinput.Blink

	case awaitResolvedMsg:
		// Upstream (or our own submit) flushed the parked call. Clear
		// the modal state. If our own submit already cleared it, this
		// is idempotent.
		m.awaitActive = false
		m.replyInput.Blur()
		return m, nil
	}
	return m, nil
}

func (m *InteractiveModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	keyStr := msg.String()
	// Ctrl+C always wins — even with the await modal open, the user
	// gets a stop confirmation.
	if keyStr == "ctrl+c" {
		now := time.Now()
		if m.modal == modalQuitConfirm || now.Sub(m.lastCtrlC) < doubleCtrlCWindow {
			if m.runCancel != nil {
				m.runCancel()
			}
			return m, tea.Quit
		}
		m.lastCtrlC = now
		m.modal = modalQuitConfirm
		return m, nil
	}

	// Await modal: Enter submits, Esc cancels (leaves the modal up
	// but blanks the input — the user can either type something else
	// or wait for the bridge to time out the parked call). Other keys
	// route into the textinput component.
	if m.awaitActive {
		switch keyStr {
		case "enter":
			content := strings.TrimRight(m.replyInput.Value(), "\r\n")
			if content == "" {
				return m, nil
			}
			if m.sendReply != nil {
				m.sendReply(content)
			}
			// Clear the modal locally; the bridge will also send a
			// RuntimeEventAwaitResolved via the runtime subscription
			// once it processes the reply, which is idempotent.
			m.awaitActive = false
			m.replyInput.Blur()
			m.replyInput.SetValue("")
			return m, nil
		case "esc":
			m.replyInput.SetValue("")
			return m, nil
		}
		var cmd tea.Cmd
		m.replyInput, cmd = m.replyInput.Update(msg)
		return m, cmd
	}

	switch keyStr {
	case "y", "enter":
		if m.modal == modalQuitConfirm {
			if m.runCancel != nil {
				m.runCancel()
			}
			return m, tea.Quit
		}
	case "n", "esc":
		if m.modal == modalQuitConfirm {
			m.modal = modalNone
		}
	case "q":
		if m.phase == phaseCompleted {
			return m, tea.Quit
		}
	}
	return m, nil
}

// PushHook enqueues a hook for the next throttle flush. Called from
// the BridgeRuntime fan-out goroutine (no tea-program goroutine
// coupling), so it must be safe for concurrent calls. The throttle
// tick drains the queue into hookRows on the tea goroutine.
func (m *InteractiveModel) PushHook(at time.Time, event, step string) {
	m.pendingMu.Lock()
	m.pendingHooks = append(m.pendingHooks, interactiveHookRow{ts: at, event: event, step: step})
	m.pendingMu.Unlock()
}

// View renders the screen. Layout: stage list (top) + hooks panel
// (middle) + status footer.
func (m *InteractiveModel) View() string {
	if m.termWidth == 0 || m.termHeight == 0 {
		return "initializing..."
	}

	var stageRows []string
	for _, st := range m.stages {
		glyph := "⏳"
		styled := stagePendingStyle
		switch st.status {
		case "running":
			glyph, styled = "▶", stageRunningStyle
		case "done":
			glyph, styled = "✓", stageDoneStyle
		case "failed":
			glyph, styled = "✗", stageFailedStyle
		}
		line := fmt.Sprintf("%s %s", glyph, st.name)
		if st.status == "done" || st.status == "failed" {
			line += fmt.Sprintf("  %s", st.dur.Round(time.Millisecond))
		}
		stageRows = append(stageRows, styled.Render(line))
	}
	stagePanel := pipelineHeaderStyle.Render("stages") + "\n" + strings.Join(stageRows, "\n")

	var hookRows []string
	maxShow := m.termHeight - len(stageRows) - statusRowReserve - headerRowReserve - 4 //nolint:mnd // reserved rows for stage list + footer + hooks panel header
	if maxShow < 3 {
		maxShow = 3
	}
	start := 0
	if len(m.hookRows) > maxShow {
		start = len(m.hookRows) - maxShow
	}
	for _, hr := range m.hookRows[start:] {
		hookRows = append(hookRows, fmt.Sprintf("%s %s %s",
			dimStyle.Render(hr.ts.Local().Format("15:04:05")),
			hr.event,
			dimStyle.Render(hr.step),
		))
	}
	hooksPanel := pipelineHeaderStyle.Render("hooks") + "\n" + strings.Join(hookRows, "\n")

	body := pipelinePanelStyle.Render(stagePanel) + "\n" + pipelinePanelStyle.Render(hooksPanel)

	var footer string
	switch m.phase {
	case phaseRunning:
		footer = fmt.Sprintf("running %s · ctrl+c to stop", m.currentStage)
	case phaseCompleted:
		if m.finalErr != nil {
			footer = stageFailedStyle.Render(fmt.Sprintf("failed: %s · q to quit", m.finalErr.Error()))
		} else {
			footer = stageDoneStyle.Render("completed · q to quit")
		}
	}

	if m.modal == modalQuitConfirm {
		modal := modalStyle.Render("Stop run? (y/n)")
		body = lipgloss.JoinVertical(lipgloss.Left, body, modal)
	}

	if m.awaitActive {
		modal := modalStyle.Render(
			"Skill is waiting for input (await_message)\n" + m.replyInput.View(),
		)
		body = lipgloss.JoinVertical(lipgloss.Left, body, modal)
	}

	return body + "\n" + footer
}

// Done is called by the runner once pipeline.Run returns.
func (m *InteractiveModel) Done(err error) {
	// Synchronously enqueue a tea.Msg through the program; safer than
	// mutating model fields from outside the tea goroutine.
}

// InteractiveObserver adapts pipeline.Observer + bridge hook events
// into tea.Msg sends. Constructed by NewInteractiveObserver with the
// tea.Program; the runner passes it to pipeline.Run.
type InteractiveObserver struct {
	program *tea.Program
	model   *InteractiveModel
}

// NewInteractiveObserver wires the observer to the program. The model
// reference is used for the direct PushHook channel (which doesn't go
// through tea.Msg to avoid spamming the tea goroutine at hook burst
// rates; the throttle tick drains the queue instead).
func NewInteractiveObserver(p *tea.Program, m *InteractiveModel) *InteractiveObserver {
	return &InteractiveObserver{program: p, model: m}
}

// OnStageStart sends a stageStartMsg.
func (o *InteractiveObserver) OnStageStart(stage string) {
	o.program.Send(stageStartMsg{stage: stage})
}

// OnStageEnd sends a stageEndMsg.
func (o *InteractiveObserver) OnStageEnd(stage string, dur time.Duration, err error) {
	o.program.Send(stageEndMsg{stage: stage, dur: dur, err: err})
}

// OnStepStart, OnStepLine, OnStepEnd are no-ops in interactive mode —
// per-step lifecycle is observed via bridge hooks (UserPromptSubmit,
// PreToolUse, etc.). Per-line streaming is intentionally suppressed
// because chain steps share the claude session and there's no clean
// per-step delineation in the stdout stream.
func (o *InteractiveObserver) OnStepStart(string, int, pipeline.Step)             {}
func (o *InteractiveObserver) OnStepLine(string, int, string)                     {}
func (o *InteractiveObserver) OnStepEnd(string, int, pipeline.Step, time.Duration, string, error) {
}

// HookEventFromBridge is called from the BridgeRuntime fan-out
// goroutine when a hook arrives. The observer enqueues into the
// model's throttle buffer (no tea.Msg round-trip).
func (o *InteractiveObserver) HookEventFromBridge(at time.Time, event, step string) {
	o.model.PushHook(at, event, step)
}

// AwaitPending sends an awaitPendingMsg to the tea program. Called
// from the BridgeRuntime subscriber goroutine when a parked
// await_message tool call surfaces.
func (o *InteractiveObserver) AwaitPending() { o.program.Send(awaitPendingMsg{}) }

// AwaitResolved sends an awaitResolvedMsg to the tea program.
func (o *InteractiveObserver) AwaitResolved() { o.program.Send(awaitResolvedMsg{}) }

// PipelineDone signals pipeline.Run returned.
func (o *InteractiveObserver) PipelineDone(err error) {
	o.program.Send(interactiveDoneMsg{err: err})
}
