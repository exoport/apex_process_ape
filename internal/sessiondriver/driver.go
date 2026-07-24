package sessiondriver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// DefaultMaxDuration is the hard wall-clock ceiling WaitStepDone enforces
// regardless of progress (PLAN-19 D2). The cap ships ON; a value of 0
// disables it. Bounds a genuinely stuck-but-noisy step that the activity
// anchor would otherwise keep alive forever.
//
// The ceiling clock is NOT measured from step start unconditionally: it
// resets on each sub-agent boundary (SubagentStart/SubagentStop). A
// sequential batch skill (apex-story-batch-dev/-create/-review,
// apex-lift-project) spawns one sub-agent per item, so the cap bounds each
// individual item rather than the whole multi-item batch. A step that
// spawns no sub-agents sees a flat wall-clock cap from step start, exactly
// as before.
const DefaultMaxDuration = 3 * time.Hour

// idlePoll is the base recheck frequency; idlePollDivisor scales it
// down for short configured idle timeouts so tail latency stays
// proportional. longRunThreshold/longRunPoll implement the PLAN-19 D6
// two-phase cadence: tight (30s) early polling for responsive stall
// detection, relaxed (60s) once a step is clearly long-lived.
const (
	idlePoll         = 30 * time.Second
	idlePollDivisor  = 4
	longRunThreshold = 60 * time.Minute
	longRunPoll      = 60 * time.Second
)

// Progress-source labels for the D4 termination diagnostic. Each names a
// signal WaitStepDone watches to keep a step alive (PLAN-19 D1).
const (
	progressHook       = "hook"
	progressTranscript = "transcript"
	progressPTY        = "pty"
	progressNone       = "none"
)

// IdleTimeoutError reports that WaitStepDone tripped the idle backstop:
// no progress across any watched signal (hook / transcript / PTY) for a
// full idle window. Diagnostic carries the per-source ages + child
// liveness (PLAN-19 D4).
type IdleTimeoutError struct {
	Label      string        // "interactive step" / "session"
	Idle       time.Duration // time since the last progress signal
	Window     time.Duration // the configured idle window
	LastSource string        // most-recently-advanced source, or "none"
	Diagnostic string        // per-source ages + child liveness
}

func (e *IdleTimeoutError) Error() string {
	return fmt.Sprintf("%s idle for %v without progress (window %v): %s → stopping",
		e.Label, e.Idle.Round(time.Second), e.Window.Round(time.Second), e.Diagnostic)
}

// MaxDurationError reports that WaitStepDone hit the hard wall-clock cap
// (PLAN-19 D2) — a distinct termination from the idle path: the step may
// still have been making progress when the ceiling tripped.
type MaxDurationError struct {
	Label      string        // "interactive step" / "session"
	Elapsed    time.Duration // wall-clock since the last item boundary (== step start when none)
	Max        time.Duration // the configured ceiling
	Diagnostic string        // last progress source + child liveness
}

func (e *MaxDurationError) Error() string {
	return fmt.Sprintf("%s exceeded max-duration %v (ran %v): %s → stopping",
		e.Label, e.Max.Round(time.Second), e.Elapsed.Round(time.Second), e.Diagnostic)
}

// Driver drives a single standalone Claude session end-to-end: it fans
// hook / call / reply events out to the runlog, binds the session's
// transcript (main + sub-agents), signals step-done on the Stop hook
// with an idle-timeout backstop, and derives transcript telemetry via
// ScanStep. `ape prompt` owns one Driver per session.
type Driver struct {
	getRunLog   func() *runlog.Writer
	idleTimeout time.Duration
	// maxDuration is the hard wall-clock per-step ceiling (PLAN-19 D2).
	// 0 disables the cap. Defaults to DefaultMaxDuration.
	maxDuration time.Duration
	// idleErrLabel is the noun WaitStepDone uses in its termination
	// diagnostics ("<label> idle for …" / "<label> exceeded max-duration
	// …"). Defaults to "session"; the pipeline runner sets
	// "interactive step".
	idleErrLabel string

	// childAliveProbe, when set, reports the child claude process's pid
	// and liveness for the D4 termination diagnostic (PLAN-19 D4). nil →
	// "child liveness unknown".
	childAliveProbe func() (pid int, alive bool)
	// ptyProbe, when set, reports the timestamp of the last PTY output
	// byte seen — a progress signal orthogonal to hooks + transcript
	// (PLAN-19 D1, optional). nil → the PTY signal is not watched.
	ptyProbe func() (at time.Time, ok bool)

	stepDoneCh chan struct{}

	activityMu   sync.Mutex
	lastActivity time.Time
	// maxDurationAnchor is the reset point for the hard wall-clock ceiling
	// (WaitStepDone). It advances to now on each sub-agent boundary
	// (RecordItemBoundary) so the cap bounds an individual batch ITEM — a
	// sequential batch skill spawns one sub-agent per item — rather than the
	// whole multi-item step. Written on the bridge goroutine, read by
	// WaitStepDone; guarded by activityMu alongside lastActivity.
	maxDurationAnchor time.Time

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
		getRunLog:    getRunLog,
		idleTimeout:  idleTimeout,
		maxDuration:  DefaultMaxDuration,
		idleErrLabel: "session",
		stepDoneCh:   make(chan struct{}, 64),
		subSessions:  map[string]*SubCapture{},
	}
}

// SetFlushGrace overrides the Stop→scan flush window. Test seam.
func (d *Driver) SetFlushGrace(g time.Duration) { d.flushGrace = g }

// SetIdleTimeout overrides the idle-timeout window when t > 0. A value
// ≤ 0 is ignored so the constructor's default (or the NewDriver
// argument) stands.
func (d *Driver) SetIdleTimeout(t time.Duration) {
	if t > 0 {
		d.idleTimeout = t
	}
}

// SetIdleErrLabel customizes the noun WaitStepDone reports in its
// idle-timeout error. Defaults to "session"; the pipeline runner sets
// "interactive step" so a cancelled pipeline step reads naturally.
func (d *Driver) SetIdleErrLabel(label string) { d.idleErrLabel = label }

// SetMaxDuration sets the hard wall-clock per-step ceiling (PLAN-19 D2).
// A value of 0 disables the cap; any positive value overrides the
// DefaultMaxDuration the constructor installs.
func (d *Driver) SetMaxDuration(t time.Duration) { d.maxDuration = t }

// SetChildAliveProbe installs the child-liveness reporter used in the D4
// termination diagnostic. nil (the default) yields "child liveness
// unknown".
func (d *Driver) SetChildAliveProbe(fn func() (pid int, alive bool)) { d.childAliveProbe = fn }

// SetPTYProbe installs the optional PTY-output progress signal (PLAN-19
// D1): fn reports the timestamp of the last output byte the PTY reader
// saw. nil (the default) leaves the PTY signal unwatched — transcript
// growth + hooks then carry the anchor alone.
func (d *Driver) SetPTYProbe(fn func() (at time.Time, ok bool)) { d.ptyProbe = fn }

// SetActiveTranscript binds the session's active transcript path so
// WaitStepDone's transcript-growth anchor (PLAN-19 D1) can stat it. The
// prompt path's FeedHook sets this from the UserPromptSubmit payload;
// the pipeline runner (interactiveCore, which keeps its own richer
// transcript state) calls this to mirror the path onto the Driver. An
// empty path is ignored so a hookless Stop doesn't clear a live path.
func (d *Driver) SetActiveTranscript(path string) {
	if path == "" {
		return
	}
	d.mu.Lock()
	d.activeTranscript = path
	d.mu.Unlock()
}

// RecordActivity resets the idle-timeout anchor to now. Every observed
// hook event counts as activity, so a busy session is never killed for
// being slow — only for going silent for a full idleTimeout window.
func (d *Driver) RecordActivity() {
	d.activityMu.Lock()
	d.lastActivity = time.Now()
	d.activityMu.Unlock()
}

// RecordItemBoundary advances the hard max-duration anchor to now. A
// sub-agent lifecycle event (SubagentStart/SubagentStop) is unambiguous
// real progress — a batch item just started or finished — and a stronger
// signal than the transcript-growth bytes the idle anchor already trusts.
// Resetting the ceiling on it bounds the NEXT item rather than the whole
// batch, so a sequential batch skill runs as long as each item completes
// within max-duration. Both driver paths call it: the prompt path from
// Driver.FeedHook, the pipeline/task path from interactiveCore.FeedHook.
func (d *Driver) RecordItemBoundary() { d.resetMaxDurationAnchor(time.Now()) }

// resetMaxDurationAnchor sets the ceiling reset point under activityMu.
func (d *Driver) resetMaxDurationAnchor(t time.Time) {
	d.activityMu.Lock()
	d.maxDurationAnchor = t
	d.activityMu.Unlock()
}

// SignalStepDone posts a non-blocking step-done signal (the Stop hook).
// WaitStepDone is the only consumer; the buffered channel + drop-on-full
// avoids any chance of blocking the bridge accept loop.
func (d *Driver) SignalStepDone() {
	select {
	case d.stepDoneCh <- struct{}{}:
	default:
	}
}

// DrainStepDone discards any buffered step-done signals so the next
// WaitStepDone blocks on a fresh Stop, not a stale one left over from a
// prior step.
func (d *Driver) DrainStepDone() {
	for {
		select {
		case <-d.stepDoneCh:
		default:
			return
		}
	}
}

// Begin marks the session's start: it anchors the sub-agent sweep's
// mtime window and drains any stale Stop signals so WaitStepDone blocks
// on this session's Stop, not a leftover.
func (d *Driver) Begin() {
	now := time.Now()
	d.mu.Lock()
	d.startedAt = now
	d.subSessions = map[string]*SubCapture{}
	d.mu.Unlock()
	d.DrainStepDone()
}

// FeedHook is the OnHook fan-out target: it records activity for the
// idle-timeout anchor, captures the main/sub transcript paths, writes
// the runlog hook entry, and signals step-done on Stop.
func (d *Driver) FeedHook(h orchestrator.HookEvent) {
	d.RecordActivity()

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
		// A sub-agent boundary is a batch-item boundary — reset the hard
		// ceiling so it bounds each item, not the whole batch (unconditional:
		// SubagentStart is presence-only and carries no transcript, but it
		// still marks a fresh item beginning).
		d.RecordItemBoundary()
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
		d.SignalStepDone()
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
// cancels, until the idle window elapses with no progress across ANY
// watched signal, or until the hard max-duration ceiling trips.
//
// PLAN-19 D1: the idle anchor resets on real progress — a hook event
// (FeedHook → RecordActivity), the active transcript growing (size /
// mtime, or the transcript directory's mtime for /clear rotation), and
// (when a probe is installed) PTY output bytes. A step that is genuinely
// working is never killed for being slow; only silence across every
// signal for a full idle window trips it.
//
// PLAN-19 D2: an independent wall-clock ceiling (maxDuration) bounds a
// stuck-but-noisy step and returns a distinct MaxDurationError. The
// ceiling clock resets on each sub-agent boundary (RecordItemBoundary),
// so a sequential batch skill — one sub-agent per item — is bounded per
// item; a step spawning no sub-agents sees a flat cap from step start.
//
// PLAN-19 D6: polls at idlePoll (30s) for the first hour, then longRunPoll
// (60s), still honouring the idlePollDivisor scaling for short windows.
func (d *Driver) WaitStepDone(ctx context.Context) error {
	d.RecordActivity()
	stepStart := time.Now()
	// Seed the ceiling anchor to step start. It advances on each sub-agent
	// boundary (RecordItemBoundary) so the cap bounds an individual batch
	// item; with no sub-agents it never moves and the cap is a flat
	// wall-clock ceiling from step start (PLAN-19 D2 behaviour).
	d.resetMaxDurationAnchor(stepStart)

	// Progress baselines. Only this goroutine touches them, so they need
	// no lock (the hook anchor lastActivity is the exception — it is read
	// under activityMu since FeedHook writes it on the bridge goroutine).
	lastTranscript := stepStart
	lastPTY := stepStart
	tSize, tMtime, tDir, _ := d.transcriptSig()
	ptySeen, _ := d.ptyOutputAt()

	poll := d.pollInterval(0)
	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	for {
		select {
		case <-d.stepDoneCh:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case now := <-ticker.C:
			// Transcript growth (size, file mtime, or dir mtime for a
			// /clear-driven session rotation) counts as activity.
			if s, m, dir, ok := d.transcriptSig(); ok {
				if s != tSize || m.After(tMtime) || dir.After(tDir) {
					lastTranscript = now
					tSize, tMtime, tDir = s, m, dir
				}
			}
			// PTY output bytes (optional signal).
			if at, ok := d.ptyOutputAt(); ok && at.After(ptySeen) {
				ptySeen = at
				lastPTY = now
			}
			d.activityMu.Lock()
			lastHook := d.lastActivity
			capAnchor := d.maxDurationAnchor
			d.activityMu.Unlock()

			// Hard ceiling first: it is the absolute stop even for a step
			// that is still making progress (a stalled step would have
			// tripped the idle path far earlier). Measured since the last
			// sub-agent boundary (== step start when the step spawns none),
			// so a sequential batch is bounded per item, not per batch.
			if capElapsed := now.Sub(capAnchor); d.maxDuration > 0 && capElapsed > d.maxDuration {
				_, diag := d.diagnose(now, stepStart, lastHook, lastTranscript, lastPTY)
				return &MaxDurationError{
					Label:      d.idleErrLabel,
					Elapsed:    capElapsed,
					Max:        d.maxDuration,
					Diagnostic: diag,
				}
			}
			lastProgress := latest(lastHook, lastTranscript, lastPTY)
			if idle := now.Sub(lastProgress); idle > d.idleTimeout {
				src, diag := d.diagnose(now, stepStart, lastHook, lastTranscript, lastPTY)
				return &IdleTimeoutError{
					Label:      d.idleErrLabel,
					Idle:       idle,
					Window:     d.idleTimeout,
					LastSource: src,
					Diagnostic: diag,
				}
			}
			// Relax / tighten the cadence as the step crosses the long-run
			// threshold (D6) — on TOTAL step time, not the per-item anchor,
			// so a long batch still relaxes to the cheap 60s poll.
			if np := d.pollInterval(now.Sub(stepStart)); np != poll {
				poll = np
				ticker.Reset(poll)
			}
		}
	}
}

// pollInterval selects the recheck cadence for a step that has been
// running for elapsed (PLAN-19 D6): idlePoll (30s) for the first hour,
// longRunPoll (60s) thereafter. A short configured idle window still
// polls at a quarter of the window (idlePollDivisor), floored at 1s, so
// tail latency stays proportional for small --idle-timeout values.
func (d *Driver) pollInterval(elapsed time.Duration) time.Duration {
	poll := idlePoll
	if elapsed >= longRunThreshold {
		poll = longRunPoll
	}
	if quarter := d.idleTimeout / idlePollDivisor; quarter < poll {
		poll = max(quarter, time.Second)
	}
	return poll
}

// transcriptSig returns the active transcript's size + mtime and its
// parent directory's mtime, the trio WaitStepDone diffs across polls to
// detect progress. ok is false when no transcript path is bound yet. The
// dir mtime covers /clear rotation: the live path stops growing while a
// new session .jsonl appears in the same dir, bumping the dir mtime.
func (d *Driver) transcriptSig() (size int64, mtime, dirMtime time.Time, ok bool) {
	d.mu.Lock()
	path := d.activeTranscript
	d.mu.Unlock()
	if path == "" {
		return 0, time.Time{}, time.Time{}, false
	}
	if fi, err := os.Stat(path); err == nil {
		size, mtime, ok = fi.Size(), fi.ModTime(), true
	}
	if di, err := os.Stat(filepath.Dir(path)); err == nil {
		dirMtime = di.ModTime()
		ok = true
	}
	return size, mtime, dirMtime, ok
}

// ptyOutputAt proxies the installed PTY probe (nil → not watched).
func (d *Driver) ptyOutputAt() (time.Time, bool) {
	if d.ptyProbe == nil {
		return time.Time{}, false
	}
	return d.ptyProbe()
}

// diagnose builds the D4 termination diagnostic: which source advanced
// most recently (and how long ago), each watched source's age, and the
// child claude process's liveness. It reports whether transcript / PTY
// are even monitored so an operator can tell "silent" from "unwatched".
func (d *Driver) diagnose(now, stepStart, lastHook, lastTranscript, lastPTY time.Time) (source, diagnostic string) {
	d.mu.Lock()
	transcriptWatched := d.activeTranscript != ""
	d.mu.Unlock()
	ptyWatched := d.ptyProbe != nil

	best := stepStart
	source = progressNone
	if lastHook.After(best) {
		best, source = lastHook, progressHook
	}
	if transcriptWatched && lastTranscript.After(best) {
		best, source = lastTranscript, progressTranscript
	}
	if ptyWatched && lastPTY.After(best) {
		best, source = lastPTY, progressPTY
	}

	var b strings.Builder
	if source == progressNone {
		b.WriteString("no progress across any signal")
	} else {
		fmt.Fprintf(&b, "last progress %s %v ago", source, now.Sub(best).Round(time.Second))
	}
	fmt.Fprintf(&b, " (hook %s; transcript %s; pty %s)",
		sourceAge(now, stepStart, lastHook, true),
		sourceAge(now, stepStart, lastTranscript, transcriptWatched),
		sourceAge(now, stepStart, lastPTY, ptyWatched))
	if d.childAliveProbe != nil {
		pid, alive := d.childAliveProbe()
		state := "alive"
		if !alive {
			state = "exited"
		}
		fmt.Fprintf(&b, "; child pid %d %s", pid, state)
	} else {
		b.WriteString("; child liveness unknown")
	}
	return source, b.String()
}

// sourceAge renders one progress source's age for the diagnostic:
// "n/a" when the source is not watched, "none for <elapsed>" when it is
// watched but never advanced since the step started, else "<age> ago".
func sourceAge(now, stepStart, last time.Time, watched bool) string {
	if !watched {
		return "n/a"
	}
	if !last.After(stepStart) {
		return fmt.Sprintf("none for %v", now.Sub(stepStart).Round(time.Second))
	}
	return fmt.Sprintf("%v ago", now.Sub(last).Round(time.Second))
}

// latest returns the most recent of the given times.
func latest(ts ...time.Time) time.Time {
	var m time.Time
	for _, t := range ts {
		if t.After(m) {
			m = t
		}
	}
	return m
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
