package sessiondriver

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/exoport/apex_process_ape/internal/bridge/ipc"
	"github.com/exoport/apex_process_ape/internal/bridge/orchestrator"
	"github.com/exoport/apex_process_ape/internal/runlog"
)

// parseHookEnvelope decodes the minimal hook payload shape. Zero-value
// fields on absent / malformed payloads (the wire shape is stable).
func parseHookEnvelope(payload json.RawMessage) hookEnvelope {
	var env hookEnvelope
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &env)
	}
	return env
}

// hookEnvelope is the minimal shape needed from a Claude Code hook
// payload. UserPromptSubmit, Stop, and Pre/PostToolUse carry
// transcript_path + session_id; SubagentStop additionally carries
// agent_transcript_path + agent_id (the sub's OWN transcript — folding
// transcript_path there re-scans main and double-counts it as a
// phantom sub, the v0.0.34 2×-main signature).
//
//nolint:tagliatelle // decodes Claude Code's snake_case hook payload; the wire shape is fixed
type hookEnvelope struct {
	TranscriptPath      string `json:"transcript_path"`
	SessionID           string `json:"session_id"`
	AgentTranscriptPath string `json:"agent_transcript_path"`
	AgentID             string `json:"agent_id"`
}

// DefaultIdleTimeout is the maximum quiet window WaitStepDone tolerates
// before declaring the session hung. Matches the pipeline runner's
// interactiveStepIdleTimeout so a one-shot prompt session behaves like
// a single pipeline step.
const DefaultIdleTimeout = 60 * time.Minute

// idlePoll is the base recheck frequency; idlePollDivisor scales it
// down for short configured idle timeouts so tail latency stays
// proportional.
const (
	idlePoll        = 30 * time.Second
	idlePollDivisor = 4
)

// Driver drives a single standalone Claude session end-to-end: it fans
// hook / call / reply events out to the runlog, binds the session's
// transcript (main + sub-agents), signals step-done on the Stop hook
// with an idle-timeout backstop, and derives transcript telemetry via
// ScanStep. `ape prompt` owns one Driver per session.
type Driver struct {
	getRunLog   func() *runlog.Writer
	idleTimeout time.Duration

	stepDoneCh chan struct{}

	activityMu   sync.Mutex
	lastActivity time.Time

	mu               sync.Mutex
	activeTranscript string
	activeSessionID  string
	subSessions      map[string]*SubCapture
	startedAt        time.Time

	// flushGrace overrides DefaultFlushGrace when non-zero (tests only).
	flushGrace time.Duration
}

// NewDriver builds a Driver writing to the runlog returned by getRunLog
// (may return nil). idleTimeout ≤ 0 falls back to DefaultIdleTimeout.
func NewDriver(getRunLog func() *runlog.Writer, idleTimeout time.Duration) *Driver {
	if idleTimeout <= 0 {
		idleTimeout = DefaultIdleTimeout
	}
	return &Driver{
		getRunLog:   getRunLog,
		idleTimeout: idleTimeout,
		stepDoneCh:  make(chan struct{}, 64),
		subSessions: map[string]*SubCapture{},
	}
}

// SetFlushGrace overrides the Stop→scan flush window. Test seam.
func (d *Driver) SetFlushGrace(g time.Duration) { d.flushGrace = g }

// Begin marks the session's start: it anchors the sub-agent sweep's
// mtime window and drains any stale Stop signals so WaitStepDone blocks
// on this session's Stop, not a leftover.
func (d *Driver) Begin() {
	now := time.Now()
	d.mu.Lock()
	d.startedAt = now
	d.subSessions = map[string]*SubCapture{}
	d.mu.Unlock()
	for {
		select {
		case <-d.stepDoneCh:
		default:
			return
		}
	}
}

// FeedHook is the OnHook fan-out target: it records activity for the
// idle-timeout anchor, captures the main/sub transcript paths, writes
// the runlog hook entry, and signals step-done on Stop.
func (d *Driver) FeedHook(h orchestrator.HookEvent) {
	d.activityMu.Lock()
	d.lastActivity = time.Now()
	d.activityMu.Unlock()

	env := parseHookEnvelope(h.Payload)
	switch h.Event {
	case ipc.HookUserPromptSubmit:
		if env.TranscriptPath != "" {
			d.mu.Lock()
			d.activeTranscript = env.TranscriptPath
			if env.SessionID != "" {
				d.activeSessionID = env.SessionID
			}
			d.mu.Unlock()
		}
	case ipc.HookSubagentStart, ipc.HookSubagentStop:
		agentID := h.AgentID
		if agentID == "" {
			agentID = env.AgentID
		}
		if agentID != "" && env.AgentTranscriptPath != "" {
			d.mu.Lock()
			if d.subSessions == nil {
				d.subSessions = map[string]*SubCapture{}
			}
			sub, ok := d.subSessions[agentID]
			if !ok {
				sub = &SubCapture{AgentID: agentID}
				d.subSessions[agentID] = sub
			}
			sub.Transcript = env.AgentTranscriptPath
			if env.SessionID != "" {
				sub.ParentSessionID = env.SessionID
			}
			d.mu.Unlock()
		}
	case ipc.HookStop:
		if env.TranscriptPath != "" {
			d.mu.Lock()
			if d.activeTranscript == "" {
				d.activeTranscript = env.TranscriptPath
				if env.SessionID != "" {
					d.activeSessionID = env.SessionID
				}
			}
			d.mu.Unlock()
		}
	}

	if writer := d.getRunLog(); writer != nil {
		_ = writer.Hook(runlog.HookEntry{
			Timestamp: h.At,
			Event:     h.Event,
			Step:      h.Step,
			SessionID: h.SessionID,
			AgentID:   h.AgentID,
			Payload:   h.Payload,
		})
	}
	if h.Event == ipc.HookStop {
		// Non-blocking send; WaitStepDone is the only consumer. Buffer +
		// drop avoids blocking the bridge accept loop.
		select {
		case d.stepDoneCh <- struct{}{}:
		default:
		}
	}
}

// FeedCall is the OnCall fan-out target — writes bridge-calls.jsonl.
func (d *Driver) FeedCall(c orchestrator.ToolCall) {
	if writer := d.getRunLog(); writer != nil {
		_ = writer.Call(runlog.CallEntry{
			Timestamp: c.At,
			Method:    "tools/call",
			Tool:      c.Tool,
			Params:    c.Params,
			Result:    c.Result,
			SessionID: c.SessionID,
			ID:        c.ID,
		})
	}
}

// FeedReply is the OnReply fan-out target — writes checkpoints.jsonl.
func (d *Driver) FeedReply(content string) {
	if writer := d.getRunLog(); writer != nil {
		_ = writer.Checkpoint(runlog.CheckpointEntry{
			Kind:    "reply",
			Payload: map[string]any{"content": content},
		})
	}
}

// WaitStepDone blocks until the bridge fires a Stop hook, until ctx
// cancels, or until the idle-timeout window elapses without any hook
// events. The idle window resets on every FeedHook call, so a busy
// session is never killed for being slow — only for going silent.
func (d *Driver) WaitStepDone(ctx context.Context) error {
	d.activityMu.Lock()
	d.lastActivity = time.Now()
	d.activityMu.Unlock()
	poll := idlePoll
	if quarter := d.idleTimeout / idlePollDivisor; quarter < poll {
		poll = max(quarter, time.Second)
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		select {
		case <-d.stepDoneCh:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			d.activityMu.Lock()
			idle := time.Since(d.lastActivity)
			d.activityMu.Unlock()
			if idle > d.idleTimeout {
				return fmt.Errorf("session idle for %v without Stop hook", idle.Round(time.Second))
			}
		}
	}
}

// SessionID returns the main claude session id captured from a hook, or
// "" if none was seen.
func (d *Driver) SessionID() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.activeSessionID
}

// Telemetry scans the session's captured transcripts and returns the
// aggregate + per-model + per-session usage (baseline zero — a
// standalone session is a single step). Never nil.
func (d *Driver) Telemetry() *Telemetry {
	d.mu.Lock()
	source := d.activeTranscript
	parentSID := d.activeSessionID
	start := d.startedAt
	subs := make([]SubCapture, 0, len(d.subSessions))
	for _, s := range d.subSessions {
		subs = append(subs, *s)
	}
	d.mu.Unlock()

	return ScanStep(ScanParams{
		Source:          source,
		ParentSessionID: parentSID,
		StepStart:       start,
		Subs:            subs,
		GetRunLog:       d.getRunLog,
		FlushGrace:      d.flushGrace,
	})
}
