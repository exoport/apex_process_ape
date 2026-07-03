package apecmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/diegosz/apex_process_ape/internal/bridge/config"
	"github.com/diegosz/apex_process_ape/internal/bridge/ipc"
	"github.com/diegosz/apex_process_ape/internal/bridge/orchestrator"
	"github.com/diegosz/apex_process_ape/internal/cost"
	"github.com/diegosz/apex_process_ape/internal/pipeline"
	"github.com/diegosz/apex_process_ape/internal/runlog"
)

// hookEnvelope is the minimal shape ape needs to extract from a
// Claude Code hook payload. UserPromptSubmit, Stop, and Pre/PostToolUse
// events all carry `transcript_path` and `session_id`; we capture them
// for symlink-into-transcripts/ and (later) per-step telemetry parsing.
type hookEnvelope struct {
	TranscriptPath string `json:"transcript_path"`
	SessionID      string `json:"session_id"`
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
	verifier   *orchestrator.ContractVerifier
	stepDoneCh chan struct{}
	getRunLog  func() *runlog.Writer
	runCancel  context.CancelFunc

	// stepMu guards activeStep; FeedHook reads on the bridge accept
	// goroutine while OnStepStart/End write on the runner goroutine.
	stepMu     sync.Mutex
	activeStep string

	// activityMu guards lastActivity, the timestamp of the most
	// recent hook event seen for the current step. WaitStepDone
	// uses it as an idle-timeout anchor — a step that has been
	// silent for interactiveStepIdleTimeout is presumed hung.
	activityMu   sync.Mutex
	lastActivity time.Time

	// idleTimeout is the maximum quiet window WaitStepDone tolerates
	// before declaring the step hung. Defaults to
	// interactiveStepIdleTimeout; `ape task --idle-timeout` overrides.
	idleTimeout time.Duration

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
	// session_id. Cleared in OnStepStart.
	subSessions map[string]*subSessionCapture
}

// subSessionCapture tracks one sub-agent claude session's transcript
// for per-session usage attribution (Imp2).
type subSessionCapture struct {
	sessionID  string
	transcript string // source path from the hook payload
}

// interactiveStepIdleTimeout is the maximum quiet window between
// hook events before WaitStepDone declares the step hung. Chosen to
// accommodate skills that legitimately do many minutes of work
// between user-visible events (apex-create-architecture's heavier
// branches in particular) while still bounding real stalls.
const interactiveStepIdleTimeout = 60 * time.Minute

// interactiveStepIdlePoll is the frequency at which WaitStepDone
// rechecks the idle window. Small enough to keep tail latency near
// the configured timeout; large enough that the runtime cost is
// trivial even across long steps.
const interactiveStepIdlePoll = 30 * time.Second

// idlePollDivisor scales the poll interval down for short configured
// idle timeouts (`ape task --idle-timeout`): poll at a quarter of the
// window so tail latency stays proportional.
const idlePollDivisor = 4

func newInteractiveCore(runCancel context.CancelFunc, getRunLog func() *runlog.Writer) *interactiveCore {
	c := &interactiveCore{
		verifier:    orchestrator.NewContractVerifier(),
		stepDoneCh:  make(chan struct{}, 64),
		getRunLog:   getRunLog,
		runCancel:   runCancel,
		idleTimeout: interactiveStepIdleTimeout,
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

// FeedHook is the on-hook fan-out target. Call from a
// BridgeRuntimeOptions.OnHook or HubOptions.OnHook callback.
func (c *interactiveCore) FeedHook(h orchestrator.HookEvent) {
	// Every hook event counts as activity for the idle-timeout
	// anchor — Pre/PostToolUse, UserPromptSubmit, Stop, all of it.
	c.activityMu.Lock()
	c.lastActivity = time.Now()
	c.activityMu.Unlock()
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
		// Sub-agent sessions carry their own session_id +
		// transcript_path (same envelope shape as UPS). Track each so
		// StepTelemetry emits per-session usage records (Imp2).
		if env := parseHookEnvelope(h.Payload); env.SessionID != "" && env.TranscriptPath != "" {
			c.transcriptMu.Lock()
			if c.subSessions == nil {
				c.subSessions = map[string]*subSessionCapture{}
			}
			sub, ok := c.subSessions[env.SessionID]
			if !ok {
				sub = &subSessionCapture{sessionID: env.SessionID}
				c.subSessions[env.SessionID] = sub
			}
			sub.transcript = env.TranscriptPath
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
	if h.Event == ipc.HookUserPromptSubmit {
		c.verifier.Consume(h.Payload)
	}
	if h.Event == ipc.HookStop {
		// Non-blocking send; WaitStepDone is the only consumer and
		// is expected to drain promptly. Buffer + drop avoids any
		// chance of blocking the bridge accept loop.
		select {
		case c.stepDoneCh <- struct{}{}:
		default:
		}
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
	c.stepMu.Unlock()
	// Sub-agent captures are per-step: a fresh step must not re-count
	// the previous step's sub-sessions.
	c.transcriptMu.Lock()
	c.subSessions = map[string]*subSessionCapture{}
	c.transcriptMu.Unlock()
	for {
		select {
		case <-c.stepDoneCh:
		default:
			c.verifier.BeginStep(orchestrator.StepContract{
				Stage:   info.Stage,
				StepIdx: info.StepIdx,
				Skill:   info.Skill,
				Agent:   info.Agent,
				Model:   info.Model,
				NoClear: info.NoClear,
			})
			return
		}
	}
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
func (c *interactiveCore) ResetStageTelemetry(_ string) {
	c.transcriptMu.Lock()
	c.activeTranscript = ""
	c.activeSessionID = ""
	c.cumulativeFor = ""
	c.stageCumulative = cost.Totals{}
	c.stageCumByModel = nil
	c.subSessions = map[string]*subSessionCapture{}
	c.transcriptMu.Unlock()
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
func (c *interactiveCore) StepTelemetry(_ string, _ int) *pipeline.StepTelemetry {
	c.transcriptMu.Lock()
	source := c.activeTranscript
	parentSID := c.activeSessionID
	prevPath := c.cumulativeFor
	prev := c.stageCumulative
	prevByModel := c.stageCumByModel
	subs := make([]*subSessionCapture, 0, len(c.subSessions))
	for _, s := range c.subSessions {
		subs = append(subs, s)
	}
	c.transcriptMu.Unlock()

	if source == "" {
		return telemetryNote("no transcript captured for step (no hook carried a transcript_path)")
	}
	// When `/clear` between steps rotates the session_id, the new
	// step's UPS payload carries a different transcript_path. The
	// previous cumulative was computed against a different file —
	// useless as a baseline — so reset to zero. The step's delta
	// then equals its absolute usage in the new transcript.
	if source != prevPath {
		prev = cost.Totals{}
		prevByModel = nil
	}
	// Brief flush window so the claude session writer can flush the
	// final assistant turn into the session JSONL.
	time.Sleep(transcriptFlushGrace)

	if !fileExists(source) {
		return telemetryNote(fmt.Sprintf("transcript missing at scan time (path %q)", source))
	}
	res, err := cost.ScanSession(source)
	if err != nil {
		return telemetryNote(fmt.Sprintf("transcript scan failed: %v (path %q)", err, source))
	}
	// Durable artifact: copy the scanned transcript into the run dir
	// so the run's record survives ~/.claude/projects/ rotation.
	// Local read of a persistent file — best-effort, once per step.
	if c.getRunLog != nil {
		if writer := c.getRunLog(); writer != nil {
			_, _ = writer.SnapshotTranscript(filepath.Base(source), source)
		}
	}

	// Main-session step delta against the stage baseline.
	c.transcriptMu.Lock()
	c.stageCumulative = res.Totals
	c.stageCumByModel = res.ByModel
	c.cumulativeFor = source
	c.transcriptMu.Unlock()
	mainUsage := totalsToModelUsage(subTotals(res.Totals, prev))
	mainByModel := byModelDelta(res.ByModel, prevByModel)

	tele := &pipeline.StepTelemetry{
		CostUSD:             mainUsage.CostUSD,
		TokensInput:         mainUsage.TokensInput,
		TokensOutput:        mainUsage.TokensOutput,
		TokensCacheRead:     mainUsage.TokensCacheRead,
		TokensCacheCreation: mainUsage.TokensCacheCreation,
		NumTurns:            mainUsage.NumTurns,
		ModelUsage:          mainByModel,
		Sessions: []pipeline.SessionUsage{{
			SessionID:  parentSID,
			Usage:      mainUsage,
			ModelUsage: mainByModel,
		}},
	}

	// Sub-agent sessions: separate transcripts, scanned whole, folded
	// into the step's aggregate + model breakdown (Imp2). Sorted for
	// deterministic manifest output.
	sort.Slice(subs, func(i, j int) bool { return subs[i].sessionID < subs[j].sessionID })
	for _, sub := range subs {
		if sub.transcript == "" || !fileExists(sub.transcript) {
			continue
		}
		subRes, subErr := cost.ScanSession(sub.transcript)
		if subErr != nil {
			continue
		}
		subUsage := totalsToModelUsage(subRes.Totals)
		subByModel := byModelDelta(subRes.ByModel, nil)
		tele.Sessions = append(tele.Sessions, pipeline.SessionUsage{
			SessionID:       sub.sessionID,
			ParentSessionID: parentSID,
			Usage:           subUsage,
			ModelUsage:      subByModel,
		})
		tele.CostUSD += subUsage.CostUSD
		tele.TokensInput += subUsage.TokensInput
		tele.TokensOutput += subUsage.TokensOutput
		tele.TokensCacheRead += subUsage.TokensCacheRead
		tele.TokensCacheCreation += subUsage.TokensCacheCreation
		tele.NumTurns += subUsage.NumTurns
		if tele.ModelUsage == nil {
			tele.ModelUsage = map[string]pipeline.ModelUsage{}
		}
		for model, u := range subByModel {
			tele.ModelUsage[model] = addModelUsage(tele.ModelUsage[model], u)
		}
	}

	if tele.NumTurns == 0 {
		// Distinguish a partial file (lines but no complete assistant
		// turn) from an empty one.
		tele.Note = fmt.Sprintf(
			"transcript scan processed zero assistant turns (path %q, %d line(s))",
			source, countLines(source),
		)
		fmt.Fprintf(os.Stderr, "⚠ telemetry: %s\n", tele.Note)
	}
	return tele
}

// countLines returns the newline count of path, or -1 when it can't be
// read. Used only to enrich a zero-turn telemetry note, so a partial
// capture is distinguishable from an empty one.
func countLines(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return -1
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		n++
	}
	if sc.Err() != nil {
		return -1
	}
	return n
}

// telemetryNote warns on stderr and returns a zeroed StepTelemetry
// carrying the diagnosability breadcrumb — the manifest records it as
// telemetry_note so a zeroed step is explainable, never silent.
func telemetryNote(note string) *pipeline.StepTelemetry {
	fmt.Fprintf(os.Stderr, "⚠ telemetry: %s\n", note)
	return &pipeline.StepTelemetry{Note: note}
}

// fileExists reports whether path exists and is a regular file (or a
// resolvable symlink to one).
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// subTotals returns a-b field-wise.
func subTotals(a, b cost.Totals) cost.Totals {
	return cost.Totals{
		CostUSD:             a.CostUSD - b.CostUSD,
		InputTokens:         a.InputTokens - b.InputTokens,
		OutputTokens:        a.OutputTokens - b.OutputTokens,
		CacheReadTokens:     a.CacheReadTokens - b.CacheReadTokens,
		CacheCreationTokens: a.CacheCreationTokens - b.CacheCreationTokens,
		NumTurns:            a.NumTurns - b.NumTurns,
	}
}

// totalsToModelUsage adapts cost.Totals onto the pipeline package's
// decoupled ModelUsage shape.
func totalsToModelUsage(t cost.Totals) pipeline.ModelUsage {
	return pipeline.ModelUsage{
		CostUSD:             t.CostUSD,
		TokensInput:         t.InputTokens,
		TokensOutput:        t.OutputTokens,
		TokensCacheRead:     t.CacheReadTokens,
		TokensCacheCreation: t.CacheCreationTokens,
		NumTurns:            t.NumTurns,
	}
}

// byModelDelta subtracts the per-model baseline from the fresh scan
// and drops all-zero entries. baseline nil means "no baseline".
func byModelDelta(current, baseline map[string]cost.Totals) map[string]pipeline.ModelUsage {
	if len(current) == 0 {
		return nil
	}
	out := map[string]pipeline.ModelUsage{}
	for model, cur := range current {
		d := cur
		if base, ok := baseline[model]; ok {
			d = subTotals(cur, base)
		}
		u := totalsToModelUsage(d)
		if u == (pipeline.ModelUsage{}) {
			continue
		}
		out[model] = u
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// addModelUsage sums two ModelUsage values field-wise.
func addModelUsage(a, b pipeline.ModelUsage) pipeline.ModelUsage {
	return pipeline.ModelUsage{
		CostUSD:             a.CostUSD + b.CostUSD,
		TokensInput:         a.TokensInput + b.TokensInput,
		TokensOutput:        a.TokensOutput + b.TokensOutput,
		TokensCacheRead:     a.TokensCacheRead + b.TokensCacheRead,
		TokensCacheCreation: a.TokensCacheCreation + b.TokensCacheCreation,
		NumTurns:            a.NumTurns + b.NumTurns,
	}
}

// WaitStepDone blocks until the bridge fires a Stop hook for the
// current step, until ctx cancels, or until the idle-timeout window
// elapses without any hook events. PLAN-6 / Phase E wires this into
// RunOptions.
//
// The idle window resets on every FeedHook call, so a busy step
// (heavy tool use, long apex-create-architecture branches) is never
// killed for being slow — only for going silent. A truly hung claude
// session stops emitting Pre/PostToolUse events and trips the timer
// after interactiveStepIdleTimeout of quiet.
func (c *interactiveCore) WaitStepDone(ctx context.Context, _ string, _ int) error {
	c.activityMu.Lock()
	c.lastActivity = time.Now()
	c.activityMu.Unlock()
	// Poll at a quarter of the idle window when the caller configured
	// one shorter than the default poll would resolve — a small
	// `ape task --idle-timeout` must not gain 30s of tail latency.
	poll := interactiveStepIdlePoll
	if quarter := c.idleTimeout / idlePollDivisor; quarter < poll {
		poll = max(quarter, time.Second)
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		select {
		case <-c.stepDoneCh:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			c.activityMu.Lock()
			idle := time.Since(c.lastActivity)
			c.activityMu.Unlock()
			if idle > c.idleTimeout {
				return fmt.Errorf("interactive step idle for %v without Stop hook", idle.Round(time.Second))
			}
		}
	}
}

// buildInteractivePrepend constructs the --strict-mcp-config /
// --mcp-config / --settings prepend-flags slice used by both the
// no-UI and web interactive variants. ipcPort is the BridgeRuntime
// or Hub's IPC port. Mode picks the settings shape: ModeWeb for
// web mode (legacy hooks-via-Mode path), ModeTUI for everywhere
// else (hooks-via-InjectHooks path).
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
	if cfg.idleTimeout > 0 {
		core.idleTimeout = cfg.idleTimeout
	}

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
		defer runLogMu.Unlock()
		if rl == nil {
			if w, openErr := runlog.New(dir); openErr == nil {
				rl = w
			}
		}
	}

	progressW := cfg.progressWriter
	if progressW == nil {
		progressW = os.Stdout
	}
	obs := newPlainObserver(progressW, projectRoot, cfg.quiet)
	runErr := pipeline.Run(runCtx, spec, pipeline.RunOptions{
		ProjectRoot:            projectRoot,
		Prompt:                 cfg.prompt,
		Observer:               obs,
		ApeVersion:             Version,
		ManifestDir:            cfg.manifestDir,
		FromStage:              cfg.fromStage,
		NoCommit:               cfg.noCommit,
		AllowDirty:             cfg.allowDirty,
		PrependFlags:           prepend,
		OnStageStart:           core.ResetStageTelemetry,
		OnRunDir:               onRunDir,
		Interactive:            true,
		WaitStepDone:           core.WaitStepDone,
		OnInteractiveStepStart: core.OnStepStart,
		OnInteractiveStepEnd:   core.OnStepEnd,
		StepTelemetryFn:        core.StepTelemetry,
		RunLog:                 &lazyRunLog{getter: getRunLog},
	})

	var pfe *pipeline.PreflightError
	if errors.As(runErr, &pfe) {
		fmt.Fprintf(os.Stderr, "%s\n", pfe.Error())
		runCancel()
		os.Exit(exitCodePreflightFailed) //nolint:gocritic // explicit runCancel above; mirrors sibling runners
	}
	if runErr == nil && !cfg.suppressSummary {
		printEndOfRunSummary(spec.Name, projectRoot, cfg)
	}
	return runErr
}
