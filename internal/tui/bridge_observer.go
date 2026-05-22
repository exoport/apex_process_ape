// Package tui — bridge-event adapter for the unified pipeline TUI.
//
// BridgeObserver implements pipeline.Observer and the small handful
// of bridge-runtime callbacks the interactive exec mode needs. Each
// callback sends a tea.Msg to the program — the unified
// pipelineModel ingests them through the same throttle path as
// stream-json events under PLAN-7 / FC.
package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/diegosz/apex_process_ape/internal/bridge/orchestrator"
	"github.com/diegosz/apex_process_ape/internal/pipeline"
)

// BridgeObserver adapts bridge hook events and pipeline lifecycle
// events into tea.Msgs sent to the unified pipelineModel. Renamed
// from PLAN-6's InteractiveObserver as part of PLAN-7 / FC — the
// rename signals it's no longer interactive-specific; any consumer
// driven by the bridge hook-event source uses it.
type BridgeObserver struct {
	program *tea.Program
}

// NewBridgeObserver wires the observer to a running tea.Program.
func NewBridgeObserver(p *tea.Program) *BridgeObserver {
	return &BridgeObserver{program: p}
}

// OnStageStart sends a stageStartMsg.
func (o *BridgeObserver) OnStageStart(stage string) {
	o.program.Send(stageStartMsg{stage: stage})
}

// OnStageEnd sends a stageEndMsg.
func (o *BridgeObserver) OnStageEnd(stage string, dur time.Duration, err error) {
	o.program.Send(stageEndMsg{stage: stage, dur: dur, err: err})
}

// OnStepStart forwards step starts to the model as stepStartMsg —
// runner-side step boundaries don't depend on the stdout stream, so
// the same forwarding the programmatic observer does is valid here.
// The unified model uses them to stamp per-step startedAt for
// elapsed-time display (PLAN-7 follow-up).
func (o *BridgeObserver) OnStepStart(stage string, idx int, step pipeline.Step) {
	o.program.Send(stepStartMsg{stage: stage, idx: idx, step: step})
}

// OnStepLine is a no-op on the bridge-event source — chain steps share
// the claude session, so the stdout-line stream has no clean per-step
// delineation to observe (PLAN-6 / Phase E rationale). Hook events
// carry the displayable per-step content instead.
func (o *BridgeObserver) OnStepLine(string, int, string) {}

// OnStepEnd forwards step ends to the model as stepEndMsg, paired with
// OnStepStart above so per-step endedAt is stamped and runningStepIdx
// resets cleanly. PLAN-7 follow-up.
func (o *BridgeObserver) OnStepEnd(stage string, idx int, step pipeline.Step, dur time.Duration, output string, err error) {
	o.program.Send(stepEndMsg{stage: stage, idx: idx, step: step, dur: dur, output: output, err: err})
}

// HookEventFromBridge forwards a HookEvent into the tea program as a
// hookEventMsg. The unified model routes it to the correct stage via
// stageFromHookStep, renders it through RenderHookEvent, and queues
// it into the throttle path. PLAN-7 / FC.
func (o *BridgeObserver) HookEventFromBridge(h orchestrator.HookEvent) {
	o.program.Send(hookEventMsg{hook: h})
}

// AwaitPending sends an awaitPendingMsg to the tea program. Called
// from the BridgeRuntime subscriber goroutine when a parked
// await_message tool call surfaces.
func (o *BridgeObserver) AwaitPending() { o.program.Send(awaitPendingMsg{}) }

// AwaitResolved sends an awaitResolvedMsg to the tea program.
func (o *BridgeObserver) AwaitResolved() { o.program.Send(awaitResolvedMsg{}) }

// PipelineDone signals pipeline.Run returned. The unified model's
// pipelineDoneMsg branch drains any pending events, transitions to
// phaseCompleted, and presents the final-report row (PLAN-2 / F7).
func (o *BridgeObserver) PipelineDone(err error) {
	o.program.Send(pipelineDoneMsg{err: err})
}
