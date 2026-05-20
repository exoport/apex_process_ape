package apecmd

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/diegosz/apex_process_ape/internal/bridge/config"
	"github.com/diegosz/apex_process_ape/internal/bridge/orchestrator"
	"github.com/diegosz/apex_process_ape/internal/pipeline"
	"github.com/diegosz/apex_process_ape/internal/runlog"
)

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
}

// interactiveStepIdleTimeout is the maximum quiet window between
// hook events before WaitStepDone declares the step hung. Chosen to
// accommodate skills that legitimately do many minutes of work
// between user-visible events (apex-create-architecture's heavier
// branches in particular) while still bounding real stalls.
const interactiveStepIdleTimeout = 15 * time.Minute

// interactiveStepIdlePoll is the frequency at which WaitStepDone
// rechecks the idle window. Small enough to keep tail latency near
// the configured timeout; large enough that the runtime cost is
// trivial even across long steps.
const interactiveStepIdlePoll = 30 * time.Second

func newInteractiveCore(runCancel context.CancelFunc, getRunLog func() *runlog.Writer) *interactiveCore {
	c := &interactiveCore{
		verifier:   orchestrator.NewContractVerifier(),
		stepDoneCh: make(chan struct{}, 64), //nolint:mnd // ample headroom; one Stop hook per step plus a margin for nested skill subagents
		getRunLog:  getRunLog,
		runCancel:  runCancel,
	}
	c.verifier.OnViolation = func(v orchestrator.ContractViolation) {
		fmt.Fprintf(os.Stderr,
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
		// step-bind plumbing in the tmux-driven runner). Fill it
		// from the active step so hook-events.jsonl records which
		// step each event belongs to instead of "step":null.
		c.stepMu.Lock()
		step = c.activeStep
		c.stepMu.Unlock()
	}
	if writer := c.getRunLog(); writer != nil {
		_ = writer.Hook(runlog.HookEntry{
			Timestamp: h.At,
			Event:     h.Event,
			Step:      step,
			SessionID: h.SessionID,
			AgentID:   h.AgentID,
			Payload:   h.Payload,
		})
	}
	if h.Event == "UserPromptSubmit" {
		c.verifier.Consume(h.Payload)
	}
	if h.Event == "Stop" {
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
	c.activeStep = info.Stage + "/" + info.Skill
	c.stepMu.Unlock()
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
	ticker := time.NewTicker(interactiveStepIdlePoll)
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
			if idle > interactiveStepIdleTimeout {
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

	rt := orchestrator.NewBridgeRuntime(orchestrator.BridgeRuntimeOptions{
		OnHook:  core.FeedHook,
		OnCall:  core.FeedCall,
		OnReply: core.FeedReply,
	})
	if err := rt.Listen(); err != nil {
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

	obs := newPlainObserver(os.Stdout, projectRoot, false)
	runErr := pipeline.Run(runCtx, spec, pipeline.RunOptions{
		ProjectRoot:            projectRoot,
		Prompt:                 cfg.prompt,
		Observer:               obs,
		ApeVersion:             Version,
		ManifestDir:            cfg.manifestDir,
		NoCommit:               cfg.noCommit,
		AllowDirty:             cfg.allowDirty,
		PrependFlags:           prepend,
		OnRunDir:               onRunDir,
		Interactive:            true,
		WaitStepDone:           core.WaitStepDone,
		OnInteractiveStepStart: core.OnStepStart,
		OnInteractiveStepEnd:   core.OnStepEnd,
		RunLog:                 &lazyRunLog{getter: getRunLog},
	})

	if runErr == nil {
		printEndOfRunSummary(spec.Name, projectRoot, cfg)
	}
	return runErr
}

