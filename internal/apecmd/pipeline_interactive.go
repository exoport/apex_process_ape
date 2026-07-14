package apecmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/exoport/apex_process_ape/internal/bridge/config"
	"github.com/exoport/apex_process_ape/internal/bridge/ipc"
	"github.com/exoport/apex_process_ape/internal/bridge/orchestrator"
	"github.com/exoport/apex_process_ape/internal/cost"
	"github.com/exoport/apex_process_ape/internal/eventing"
	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/exoport/apex_process_ape/internal/sessiondriver"
)

// hookEnvelope is the minimal shape ape needs to extract from a
// Claude Code hook payload. UserPromptSubmit, Stop, and Pre/PostToolUse
// events all carry `transcript_path` and `session_id`; we capture them
// for symlink-into-transcripts/ and (later) per-step telemetry parsing.
//
// SubagentStop additionally carries agent_transcript_path + agent_id.
// The sub-agent's OWN transcript is agent_transcript_path (a distinct
// agent-<id>.jsonl); transcript_path on that same envelope is the
// PARENT session. Folding transcript_path re-scans main and
// double-counts it as a phantom sub — the v0.0.34 2×-main bug — so sub
// capture keys on agent_id and reads agent_transcript_path.
type hookEnvelope struct {
	TranscriptPath      string `json:"transcript_path"`
	SessionID           string `json:"session_id"`
	AgentTranscriptPath string `json:"agent_transcript_path"`
	AgentID             string `json:"agent_id"`
}

// extractTranscriptPath pulls transcript_path from a hook payload.
// Returns "" on absent / malformed (rare; the wire shape is stable).
func extractTranscriptPath(payload json.RawMessage) string {
	return parseHookEnvelope(payload).TranscriptPath
}

// parseHookEnvelope decodes the minimal hook payload shape. Zero-value
// fields on absent / malformed payloads.
func parseHookEnvelope(payload json.RawMessage) hookEnvelope {
	var env hookEnvelope
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &env)
	}
	return env
}

// transcriptLinkName converts a step label (`<stage>/<idx>-<skill>`)
// into a filesystem-safe symlink name under transcripts/. Slashes
// become hyphens; the `.jsonl` extension is appended for clarity.
func transcriptLinkName(stepLabel string) string {
	return strings.ReplaceAll(stepLabel, "/", "-") + ".jsonl"
}

// interactiveCore bundles the per-step verification + step-done
// machinery shared by every PLAN-6 interactive runner variant
// (`none` UI, `tui`, `web`). The caller constructs a BridgeRuntime
// (bare or composed inside a Hub) and feeds the OnHook callback into
// FeedHook; the core handles UserPromptSubmit-against-contract,
// runlog write-out, and Stop-hook → step-done signalling.
type interactiveCore struct {
	verifier  *orchestrator.ContractVerifier
	getRunLog func() *runlog.Writer
	runCancel context.CancelFunc

	// driver is the single owner of the activity-anchor + step-done
	// signalling + idle-wait loop (PLAN-19 D5). interactiveCore composes
	// it and delegates: FeedHook records activity + signals step-done on
	// Stop, OnStepStart drains stale signals, and WaitStepDone runs the
	// idle-wait loop. The Driver's own transcript-capture state is unused
	// here — interactiveCore keeps its own richer per-stage telemetry
	// state (cumulative baselines, sub-session sweep) below.
	driver *sessiondriver.Driver

	// stepMu guards activeStep + activeSkill; FeedHook reads on the bridge
	// accept goroutine while OnStepStart/End write on the runner goroutine.
	stepMu      sync.Mutex
	activeStep  string
	activeSkill string // current step's skill, for the step-end event label

	// pub publishes PLAN-13 progress events. Set once via setPublisher when
	// the run dir resolves (the run id is the <id> subject segment). Guarded
	// because FeedHook reads it on the bridge goroutine while setPublisher
	// writes on the runner goroutine. A nil *eventing.Publisher is a no-op,
	// so an unconfigured run (no NATS) publishes nothing.
	pubMu sync.Mutex
	pub   *eventing.Publisher

	// transcriptMu guards the transcript-capture state below. UPS /
	// Subagent hooks (bridge goroutine) write; the telemetry callback
	// (runner goroutine, fired between steps) reads and scans.
	//
	// /clear between steps in claude REPL rotates the session_id —
	// so each interactive step typically has its OWN transcript
	// file, not a shared per-stage one. The cumulative subtraction
	// here matters only when a step opts into `NoClear: true` and
	// keeps writing to the prior step's session. cumulativeFor
	// tracks which path the baselines were computed against; when
	// activeTranscript moves to a new path, the baseline resets to
	// zero so the step's delta equals its absolute usage.
	// OnStageStart clears everything.
	transcriptMu     sync.Mutex
	activeTranscript string // source path from the UPS payload
	activeSessionID  string // step's main claude session id
	cumulativeFor    string
	stageCumulative  cost.Totals
	stageCumByModel  map[string]cost.Totals
	// subSessions collects sub-agent (Agent tool) sessions observed
	// via SubagentStart/Stop hooks for the CURRENT step, keyed by
	// agent_id (NOT session_id — a sub-agent's internal sessionId
	// equals its parent's, so session_id collapses every sub into one
	// phantom pointing at the parent transcript). Cleared in OnStepStart.
	subSessions map[string]*subSessionCapture
	// stepStartedAt anchors the robustness sweep's mtime window so a
	// dropped SubagentStop doesn't lose a sub, and a prior NoClear
	// step's sub in the same dir isn't re-folded. Set in OnStepStart.
	stepStartedAt time.Time
}

// subSessionCapture tracks one sub-agent claude session's transcript
// for per-session usage attribution (Imp2). Identified by agentID —
// the only stable per-sub identifier (the sub's internal sessionId is
// the parent's).
type subSessionCapture struct {
	agentID         string
	parentSessionID string // the main session that spawned it
	transcript      string // agent_transcript_path (a distinct agent-<id>.jsonl)
}

// interactiveStepIdleTimeout is the maximum quiet window between
// hook events before WaitStepDone declares the step hung. Chosen to
// accommodate skills that legitimately do many minutes of work
// between user-visible events (apex-create-architecture's heavier
// branches in particular) while still bounding real stalls.
const interactiveStepIdleTimeout = 60 * time.Minute

func newInteractiveCore(runCancel context.CancelFunc, getRunLog func() *runlog.Writer) *interactiveCore {
	// The Driver owns the activity anchor + step-done channel + idle-wait
	// loop (PLAN-19 D5). Seed it with the pipeline's 60m default and the
	// "interactive step" idle-error noun so a cancelled step reads
	// naturally; runWithInteractive overrides the window from
	// `--idle-timeout` after construction.
	driver := sessiondriver.NewDriver(getRunLog, interactiveStepIdleTimeout)
	driver.SetIdleErrLabel("interactive step")
	c := &interactiveCore{
		verifier:  orchestrator.NewContractVerifier(),
		getRunLog: getRunLog,
		runCancel: runCancel,
		driver:    driver,
	}
	c.verifier.OnViolation = func(v orchestrator.ContractViolation) {
		fmt.Fprintf(
			os.Stderr,
			"❌ assertion:step-contract — stage=%q step=%d: %s\n",
			v.Stage, v.StepIdx, v.Reason,
		)
		runCancel()
	}
	return c
}

// setPublisher installs the PLAN-13 event publisher once the run id is
// known (OnRunDir). Safe to call with nil (NATS off).
func (c *interactiveCore) setPublisher(p *eventing.Publisher) {
	c.pubMu.Lock()
	c.pub = p
	c.pubMu.Unlock()
}

// publisher returns the installed publisher (nil-safe: a nil result is a
// valid no-op publisher).
func (c *interactiveCore) publisher() *eventing.Publisher {
	c.pubMu.Lock()
	defer c.pubMu.Unlock()
	return c.pub
}

// emitStepEnd publishes the step-end event from the just-computed
// telemetry. Called on every StepTelemetry return path (via defer) so a
// step-end always fires, even when telemetry is a diagnostic note.
func (c *interactiveCore) emitStepEnd(stage string, stepIdx int, tele *pipeline.StepTelemetry) {
	pub := c.publisher()
	if pub == nil {
		return
	}
	c.stepMu.Lock()
	skill := c.activeSkill
	c.stepMu.Unlock()
	c.transcriptMu.Lock()
	sessionID := c.activeSessionID
	start := c.stepStartedAt
	c.transcriptMu.Unlock()
	var dur float64
	if !start.IsZero() {
		dur = time.Since(start).Seconds()
	}
	var metrics eventing.StepMetrics
	if tele != nil {
		metrics = stepTelemetryToMetrics(tele)
	}
	pub.StepEnd(stage, stepIdx+1, skill, sessionID, dur, metrics)
}

// FeedHook is the on-hook fan-out target. Call from a
// BridgeRuntimeOptions.OnHook or HubOptions.OnHook callback.
func (c *interactiveCore) FeedHook(h orchestrator.HookEvent) {
	// Every hook event counts as activity for the idle-timeout
	// anchor — Pre/PostToolUse, UserPromptSubmit, Stop, all of it.
	// The anchor lives on the composed Driver (PLAN-19 D5).
	c.driver.RecordActivity()
	step := h.Step
	if step == "" {
		// Interactive mode: `ape notify` cannot populate Step (no
		// step-bind plumbing in the PTY-driven runner — the hook
		// fires under the child claude's environment, not the
		// runner's). Fill it from the active step so
		// hook-events.jsonl records which step each event belongs to
		// instead of "step":null.
		c.stepMu.Lock()
		step = c.activeStep
		c.stepMu.Unlock()
	}
	writer := c.getRunLog()
	switch h.Event {
	case ipc.HookUserPromptSubmit:
		// Record the step's main transcript path + session id. The
		// transcript persists at the normal path now that the spawned
		// claude is a top-level session (v0.0.33 env scrub) —
		// StepTelemetry scans it directly, no copy machinery needed.
		if env := parseHookEnvelope(h.Payload); env.TranscriptPath != "" && step != "" {
			c.transcriptMu.Lock()
			c.activeTranscript = env.TranscriptPath
			if env.SessionID != "" {
				c.activeSessionID = env.SessionID
			}
			c.transcriptMu.Unlock()
		}
	case ipc.HookSubagentStart, ipc.HookSubagentStop:
		// Capture the sub-agent's OWN transcript (agent_transcript_path),
		// keyed by agent_id. transcript_path on this envelope is the
		// PARENT session — folding it re-scans main and double-counts it
		// as a phantom sub (v0.0.34's exact 2×-main signature). agent_id
		// is the only distinct per-sub identifier (the sub's internal
		// sessionId equals the parent's). SubagentStart carries no
		// agent_transcript_path — it is presence only; capture lands on
		// SubagentStop, when the agent-<id>.jsonl is complete on disk.
		env := parseHookEnvelope(h.Payload)
		agentID := h.AgentID
		if agentID == "" {
			agentID = env.AgentID
		}
		if agentID != "" && env.AgentTranscriptPath != "" {
			c.transcriptMu.Lock()
			if c.subSessions == nil {
				c.subSessions = map[string]*subSessionCapture{}
			}
			sub, ok := c.subSessions[agentID]
			if !ok {
				sub = &subSessionCapture{agentID: agentID}
				c.subSessions[agentID] = sub
			}
			sub.transcript = env.AgentTranscriptPath
			if env.SessionID != "" {
				sub.parentSessionID = env.SessionID
			}
			c.transcriptMu.Unlock()
		}
	case ipc.HookStop:
		// The Stop envelope can seed the transcript path when UPS
		// didn't carry one — cheap robustness for the scan below.
		if env := parseHookEnvelope(h.Payload); env.TranscriptPath != "" {
			c.transcriptMu.Lock()
			if c.activeTranscript == "" {
				c.activeTranscript = env.TranscriptPath
				if env.SessionID != "" {
					c.activeSessionID = env.SessionID
				}
			}
			c.transcriptMu.Unlock()
		}
	}
	if writer != nil {
		_ = writer.Hook(runlog.HookEntry{
			Timestamp: h.At,
			Event:     h.Event,
			Step:      step,
			SessionID: h.SessionID,
			AgentID:   h.AgentID,
			Payload:   h.Payload,
		})
	}
	// PLAN-13: mirror the hook onto the progress-event stream (fire-and-
	// forget; nil publisher is a no-op). Consumers follow tool-use live.
	c.publisher().Hook(h.Event, h.SessionID, h.AgentID, step)
	if h.Event == ipc.HookUserPromptSubmit {
		c.verifier.Consume(h.Payload)
	}
	if h.Event == ipc.HookStop {
		// Signal step-done through the Driver's channel; WaitStepDone
		// is the only consumer. Non-blocking buffer + drop avoids any
		// chance of blocking the bridge accept loop.
		c.driver.SignalStepDone()
	}
}

// FeedCall is the OnCall fan-out target — writes the runlog
// bridge-calls.jsonl entry. Wire from the runtime/hub OnCall callback.
func (c *interactiveCore) FeedCall(call orchestrator.ToolCall) {
	if writer := c.getRunLog(); writer != nil {
		_ = writer.Call(runlog.CallEntry{
			Timestamp: call.At,
			Method:    "tools/call",
			Tool:      call.Tool,
			Params:    call.Params,
			Result:    call.Result,
			SessionID: call.SessionID,
			ID:        call.ID,
		})
	}
}

// FeedReply is the OnReply fan-out target — writes the runlog
// checkpoints.jsonl entry.
func (c *interactiveCore) FeedReply(content string) {
	if writer := c.getRunLog(); writer != nil {
		_ = writer.Checkpoint(runlog.CheckpointEntry{
			Kind:    "reply",
			Payload: map[string]any{"content": content},
		})
	}
}

// OnStepStart registers a step's contract with the verifier and
// drains any stale Stop-hook signals so the next WaitStepDone blocks
// on this step, not a previous step's leftover.
func (c *interactiveCore) OnStepStart(info pipeline.InteractiveStepInfo) {
	c.stepMu.Lock()
	// info.StepIdx is 0-based; StepLabel uses 1-based to match the
	// manifest's step numbering.
	c.activeStep = pipeline.StepLabel(info.Stage, info.StepIdx+1, info.Skill)
	c.activeSkill = info.Skill
	c.stepMu.Unlock()
	c.publisher().StepStart(info.Stage, info.StepIdx+1, info.Skill, info.Agent, info.Model)
	// Sub-agent captures are per-step: a fresh step must not re-count
	// the previous step's sub-sessions. stepStartedAt anchors the
	// robustness sweep's mtime window to this step.
	c.transcriptMu.Lock()
	c.subSessions = map[string]*subSessionCapture{}
	c.stepStartedAt = time.Now()
	c.transcriptMu.Unlock()
	// Drain any stale Stop signal so the next WaitStepDone blocks on
	// this step, not a previous step's leftover (PLAN-19 D5: the
	// step-done channel lives on the Driver).
	c.driver.DrainStepDone()
	c.verifier.BeginStep(orchestrator.StepContract{
		Stage:   info.Stage,
		StepIdx: info.StepIdx,
		Skill:   info.Skill,
		Agent:   info.Agent,
		Model:   info.Model,
		NoClear: info.NoClear,
	})
}

// OnStepEnd releases the active contract so a late UserPromptSubmit
// from the previous step doesn't get matched against a fresh contract.
func (c *interactiveCore) OnStepEnd(_ pipeline.InteractiveStepInfo) {
	c.verifier.EndStep()
	c.stepMu.Lock()
	c.activeStep = ""
	c.stepMu.Unlock()
}

// ActiveStep returns the currently-running step label
// ("<stage>/<idx>-<skill>") or "" between steps. PLAN-7 / FC: the
// TUI observer uses this to backfill h.Step on hook events that
// arrived from `ape notify` with the field empty (the PTY-driven
// runner has no step-bind plumbing to populate it on the wire).
// Thread-safe; reads are guarded by stepMu the same way FeedHook
// reads it for its runlog write.
func (c *interactiveCore) ActiveStep() string {
	c.stepMu.Lock()
	defer c.stepMu.Unlock()
	return c.activeStep
}

// ResetStageTelemetry clears the per-stage transcript path and
// cumulative totals. Wire to RunOptions.OnStageStart so a fresh stage
// starts from a zero baseline; the first step's delta then equals
// that step's absolute usage.
func (c *interactiveCore) ResetStageTelemetry(stage string) {
	c.transcriptMu.Lock()
	c.activeTranscript = ""
	c.activeSessionID = ""
	c.cumulativeFor = ""
	c.stageCumulative = cost.Totals{}
	c.stageCumByModel = nil
	c.subSessions = map[string]*subSessionCapture{}
	c.stepStartedAt = time.Time{}
	c.transcriptMu.Unlock()
	// PLAN-13: OnStageStart is where the stage-start event fires (nil-safe).
	c.publisher().StageStart(stage)
}

// transcriptFlushGrace is the wait between Stop-hook receipt and the
// transcript-scan inside StepTelemetry. Claude buffers writes to its
// per-session JSONL; the Stop hook can fire before the last
// assistant turn is flushed. 500ms is far above the observed flush
// latency without meaningfully slowing the pipeline. Variable so
// tests can shorten it.
var transcriptFlushGrace = 500 * time.Millisecond

// StepTelemetry returns the just-completed interactive step's
// transcript-derived telemetry: aggregate + per-model breakdown +
// per-session records (main session delta + full sub-agent sessions).
//
// Single-source design: the spawned claude is a top-level session
// (v0.0.33 env scrub), so its transcript persists at
// ~/.claude/projects/<cwd>/<sid>.jsonl and is scanned directly after a
// brief flush grace. A step that yields no transcript or zero turns
// returns a telemetry value whose Note explains why (stamped on the
// manifest as telemetry_note) and warns on stderr — never a silent
// zero.
//
// Wired into pipeline.RunOptions.StepTelemetryFn for both --tui /
// --no-tui (runWithInteractive*) and --web interactive
// (runWithWeb with core != nil).
func (c *interactiveCore) StepTelemetry(stage string, stepIdx int) (tele *pipeline.StepTelemetry) {
	// PLAN-13: publish step-end from whatever telemetry this returns, on
	// every path (including the diagnostic-note early returns).
	defer func() { c.emitStepEnd(stage, stepIdx, tele) }()
	c.transcriptMu.Lock()
	subs := make([]sessiondriver.SubCapture, 0, len(c.subSessions))
	for _, s := range c.subSessions {
		subs = append(subs, sessiondriver.SubCapture{
			AgentID:         s.agentID,
			ParentSessionID: s.parentSessionID,
			Transcript:      s.transcript,
		})
	}
	params := sessiondriver.ScanParams{
		Source:          c.activeTranscript,
		ParentSessionID: c.activeSessionID,
		PrevPath:        c.cumulativeFor,
		PrevTotals:      c.stageCumulative,
		PrevByModel:     c.stageCumByModel,
		StepStart:       c.stepStartedAt,
		Subs:            subs,
		GetRunLog:       c.getRunLog,
		FlushGrace:      transcriptFlushGrace,
	}
	c.transcriptMu.Unlock()

	// The transcript scan (main delta + sub-agent sessions, double-count
	// guard, dropped-SubagentStop sweep, durable snapshots) is the
	// reusable slice extracted into sessiondriver and shared with
	// `ape prompt`.
	st := sessiondriver.ScanStep(params)
	if st.Advance != nil {
		// Advance the per-stage baseline for the next step's delta.
		c.transcriptMu.Lock()
		c.stageCumulative = st.Advance.Totals
		c.stageCumByModel = st.Advance.ByModel
		c.cumulativeFor = st.Advance.Path
		c.transcriptMu.Unlock()
	}
	if st.Note != "" {
		// Preserve the runner's no-silent-zero stderr breadcrumb.
		fmt.Fprintf(os.Stderr, "⚠ telemetry: %s\n", st.Note)
	}
	return telemetryFromScan(st)
}

// telemetryFromScan adapts the neutral sessiondriver.Telemetry onto the
// pipeline package's StepTelemetry shape (the manifest's contract).
func telemetryFromScan(st *sessiondriver.Telemetry) *pipeline.StepTelemetry {
	agg := totalsToModelUsage(st.Totals)
	tele := &pipeline.StepTelemetry{
		CostUSD:               agg.CostUSD,
		TokensInput:           agg.TokensInput,
		TokensOutput:          agg.TokensOutput,
		TokensCacheRead:       agg.TokensCacheRead,
		TokensCacheCreation:   agg.TokensCacheCreation,
		TokensCacheCreation5m: agg.TokensCacheCreation5m,
		TokensCacheCreation1h: agg.TokensCacheCreation1h,
		NumTurns:              agg.NumTurns,
		ModelUsage:            byModelToPipeline(st.ByModel),
		Note:                  st.Note,
	}
	for _, s := range st.Sessions {
		tele.Sessions = append(tele.Sessions, pipeline.SessionUsage{
			SessionID:       s.SessionID,
			ParentSessionID: s.ParentSessionID,
			Usage:           totalsToModelUsage(s.Totals),
			ModelUsage:      byModelToPipeline(s.ByModel),
		})
	}
	return tele
}

// totalsToModelUsage adapts cost.Totals onto the pipeline package's
// decoupled ModelUsage shape.
func totalsToModelUsage(t cost.Totals) pipeline.ModelUsage {
	return pipeline.ModelUsage{
		CostUSD:               t.CostUSD,
		TokensInput:           t.InputTokens,
		TokensOutput:          t.OutputTokens,
		TokensCacheRead:       t.CacheReadTokens,
		TokensCacheCreation:   t.CacheCreationTokens,
		TokensCacheCreation5m: t.CacheCreation5mTokens,
		TokensCacheCreation1h: t.CacheCreation1hTokens,
		NumTurns:              t.NumTurns,
	}
}

// byModelToPipeline converts a cost.Totals per-model map to the
// pipeline ModelUsage shape. nil in → nil out.
func byModelToPipeline(m map[string]cost.Totals) map[string]pipeline.ModelUsage {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]pipeline.ModelUsage, len(m))
	for model, t := range m {
		out[model] = totalsToModelUsage(t)
	}
	return out
}

// WaitStepDone blocks until the bridge fires a Stop hook for the
// current step, until ctx cancels, or until the idle-timeout window
// elapses without any hook events. PLAN-6 / Phase E wires this into
// RunOptions.
//
// PLAN-19 D5: the wait loop (activity anchor + poll cadence + idle
// backstop) is implemented once on the composed Driver; interactiveCore
// delegates. The idle window resets on every FeedHook call (via
// Driver.RecordActivity), so a busy step (heavy tool use, long
// apex-create-architecture branches) is never killed for being slow —
// only for going silent. A truly hung claude session stops emitting
// Pre/PostToolUse events and trips the timer after the idle window.
func (c *interactiveCore) WaitStepDone(ctx context.Context, _ string, _ int) error {
	return c.driver.WaitStepDone(ctx)
}

// buildInteractivePrepend constructs the --strict-mcp-config /
// --mcp-config / --settings prepend-flags slice used by both the
// no-UI and web interactive variants. ipcPort is the BridgeRuntime
// or Hub's IPC port. Mode picks the settings shape: ModeWeb for
// web mode (legacy hooks-via-Mode path), ModeTUI for everywhere
// else (hooks-via-InjectHooks path).
//
//nolint:unparam // mode is a genuine settings-shape selector; ModeWeb is a supported value even though every current caller passes ModeTUI
func buildInteractivePrepend(apeBin string, ipcPort int, mode config.Mode, ignoreProjectSettings bool) ([]string, error) {
	mcpCfg, err := config.BuildMCPConfig(config.MCPOptions{APEBin: apeBin, IPCPort: ipcPort})
	if err != nil {
		return nil, err
	}
	settings, err := config.BuildSettings(config.SettingsOptions{
		APEBin:      apeBin,
		BridgePort:  ipcPort,
		Mode:        mode,
		InjectHooks: mode != config.ModeWeb, // ModeWeb auto-injects; other modes need the explicit flag
	})
	if err != nil {
		return nil, err
	}
	prepend := []string{
		"--strict-mcp-config",
		"--mcp-config", string(mcpCfg),
		"--settings", string(settings),
	}
	if ignoreProjectSettings {
		prepend = append(prepend, "--setting-sources", "user")
	}
	return prepend, nil
}

// runWithInteractive runs a pipeline in PLAN-6 interactive exec mode
// with **no UI**: plain stdout streaming, BridgeRuntime for hook
// observability, ContractVerifier for the step contract, runlog
// alongside PLAN-3's manifest path. This is the `--no-tui`
// (interactive) variant; the `--tui` variant routes through
// runWithInteractiveTUI in pipeline_interactive_tui.go.
func runWithInteractive(ctx context.Context, spec *pipeline.Spec, projectRoot string, cfg runConfig) error {
	apeBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("ape pipeline --interactive: locate self: %w", err)
	}

	var (
		runLogMu sync.Mutex
		rl       *runlog.Writer
	)
	getRunLog := func() *runlog.Writer {
		runLogMu.Lock()
		defer runLogMu.Unlock()
		return rl
	}

	runCtx, runCancel := context.WithCancel(ctx)

	core := newInteractiveCore(runCancel, getRunLog)
	core.driver.SetIdleTimeout(cfg.idleTimeout)

	// PLAN-13: optional NATS eventing + transcript upload. Fire-and-forget —
	// conn is nil when NATS is off or unreachable, and every publish/upload
	// path is a no-op in that case. The publisher itself is built in
	// onRunDir once the run id (its <id> subject segment) is known.
	eventConn, eventIdentity := startEventing(ctx, cfg)
	defer func() {
		if eventConn != nil {
			_ = eventConn.Drain()
		}
	}()
	var runDir string

	rt := orchestrator.NewBridgeRuntime(orchestrator.BridgeRuntimeOptions{
		OnHook:  core.FeedHook,
		OnCall:  core.FeedCall,
		OnReply: core.FeedReply,
	})
	if err := rt.Listen(runCtx); err != nil {
		runCancel()
		return fmt.Errorf("ape pipeline --interactive: runtime listen: %w", err)
	}
	rt.SetStopFn(runCancel)

	rtErrCh := make(chan error, 1)
	go func() { rtErrCh <- rt.Serve(runCtx) }()
	// Teardown order matters: cancel runCtx first so rt.Serve returns,
	// THEN drain rtErrCh. The earlier shape (defer runCancel; defer
	// <-rtErrCh) ran the drain before the cancel because defers fire
	// LIFO, leaving the process hung after the last stage completed.
	defer func() {
		runCancel()
		<-rtErrCh
		runLogMu.Lock()
		if rl != nil {
			_ = rl.Close()
			rl = nil
		}
		runLogMu.Unlock()
	}()

	prepend, err := buildInteractivePrepend(apeBin, rt.IPCPort(), config.ModeTUI, cfg.ignoreProjectSettings)
	if err != nil {
		return err
	}

	onRunDir := func(dir string) {
		runLogMu.Lock()
		if rl == nil {
			if w, openErr := runlog.New(dir); openErr == nil {
				rl = w
			}
		}
		runLogMu.Unlock()
		// PLAN-13: the run id is now known — build the publisher (nil when
		// NATS is off) and emit run-start. onRunDir runs on the runner
		// goroutine before any stage, so runDir is safely read post-Run.
		runDir = dir
		pub := newEventPublisher(eventConn, eventIdentity, projectRoot, runIDFromDir(dir), cfg)
		core.setPublisher(pub)
		pub.RunStart(spec.Name, len(spec.Stages()))
	}

	progressW := cfg.progressWriter
	if progressW == nil {
		progressW = os.Stdout
	}
	obs := newPlainObserver(progressW, projectRoot, cfg.quiet)
	runErr := pipeline.Run(runCtx, spec, pipeline.RunOptions{
		ProjectRoot:  projectRoot,
		Prompt:       cfg.prompt,
		Observer:     obs,
		ClaudeBin:    cfg.claudeBin,
		ApeVersion:   Version,
		ManifestDir:  cfg.manifestDir,
		FromStage:    cfg.fromStage,
		NoCommit:     cfg.noCommit,
		AllowDirty:   cfg.allowDirty,
		PrependFlags: prepend,
		OnStageStart: core.ResetStageTelemetry,
		OnStageEnd: func(stage string, dur time.Duration, err error) {
			core.publisher().StageEnd(stage, dur.Seconds(), err != nil)
		},
		OnStepCommit: func(stage string, stepIdx int, sha, message string) {
			core.publisher().Commit(stage, stepIdx, sha, message)
		},
		OnRunDir:               onRunDir,
		WaitStepDone:           core.WaitStepDone,
		OnInteractiveStepStart: core.OnStepStart,
		OnInteractiveStepEnd:   core.OnStepEnd,
		StepTelemetryFn:        core.StepTelemetry,
		RunLog:                 &lazyRunLog{getter: getRunLog},
	})

	// PLAN-13: end-of-run eventing — error (on failure), transcript upload +
	// manifest stamp, then run-end with totals. No-op when NATS is off.
	finalizeRun(ctx, core.publisher(), eventConn, eventIdentity, runDir, projectRoot, cfg, runErr)
	core.publisher().Close()

	var pfe *pipeline.PreflightError
	if errors.As(runErr, &pfe) {
		fmt.Fprintf(os.Stderr, "%s\n", pfe.Error())
		runCancel()
		os.Exit(ExitUsage) //nolint:gocritic // explicit runCancel above; mirrors sibling runners
	}
	if runErr == nil && !cfg.suppressSummary {
		printEndOfRunSummary(spec.Name, projectRoot, cfg)
	}
	return runErr
}
