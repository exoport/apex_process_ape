package apecmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/diegosz/apex_process_ape/internal/bridge/config"
	"github.com/diegosz/apex_process_ape/internal/bridge/orchestrator"
	"github.com/diegosz/apex_process_ape/internal/pipeline"
	"github.com/diegosz/apex_process_ape/internal/runlog"
	"github.com/diegosz/apex_process_ape/internal/tui"
)

// runWithInteractiveTUI runs the pipeline in PLAN-6 interactive exec
// mode with the Bubble Tea TUI on stdout. Pipeline lifecycle events
// drive the stage panel via InteractiveObserver; bridge hook events
// drive the throttled hooks panel via PushHook (the throttle tick
// drains the queue at ~30 Hz). Ctrl+C twice (or once + y in the
// confirm modal) cancels the run via runCancel.
//
// PLAN-6 / Phase E follow-up — the `tui + interactive` cell of the
// invocation matrix. Other cells: the `none + interactive` case
// routes to runWithInteractive; web variants route to runWithWeb.
func runWithInteractiveTUI(ctx context.Context, spec *pipeline.Spec, projectRoot string, cfg runConfig) error {
	apeBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("ape pipeline (tui + interactive): locate self: %w", err)
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

	// Construct the BridgeRuntime first so the model can capture
	// rt.SendMessage as its reply sender (the await modal's submit
	// path round-trips through TypeMessage IPC frame back to the
	// parked await_message MCP tool call).
	//
	// PLAN-7 / FC: interactive mode now builds the unified
	// pipelineModel — same TUI as `--tui -P` — parameterised with
	// the bridge-hook event source and the reply-sender callback.
	var rt *orchestrator.BridgeRuntime
	model := tui.NewPipelineModel(
		spec, runCancel, projectRoot,
		tui.WithEventSource(tui.SourceHookEvents),
		tui.WithAwaitReplySender(func(content string) {
			if rt != nil {
				rt.SendMessage(content)
			}
		}),
	)
	program := tea.NewProgram(model, tea.WithAltScreen())
	obs := tui.NewBridgeObserver(program)

	rt = orchestrator.NewBridgeRuntime(orchestrator.BridgeRuntimeOptions{
		OnHook: func(h orchestrator.HookEvent) {
			core.FeedHook(h)
			// `ape notify` cannot populate h.Step in interactive mode
			// (no step-bind plumbing under the PTY-driven runner), so
			// the TUI observer would see h.Step=="" and drop the
			// event when routing by stage. Backfill from
			// interactiveCore.ActiveStep — the same value FeedHook
			// already used for its runlog write.
			if h.Step == "" {
				h.Step = core.ActiveStep()
			}
			obs.HookEventFromBridge(h)
		},
		OnCall:  core.FeedCall,
		OnReply: core.FeedReply,
	})
	if err := rt.Listen(runCtx); err != nil {
		runCancel()
		return fmt.Errorf("ape pipeline (tui + interactive): runtime listen: %w", err)
	}
	rt.SetStopFn(runCancel)

	// Subscribe to runtime events so the bridge's await-pending /
	// await-resolved signals reach the tea program. Hook events go
	// through the direct OnHook callback above (throttled inside the
	// model); await events are one-shots and route through tea.Msg.
	awaitEvents := rt.Subscribe()
	go func() {
		for evt := range awaitEvents {
			switch evt.Kind {
			case orchestrator.RuntimeEventAwaitPending:
				obs.AwaitPending()
			case orchestrator.RuntimeEventAwaitResolved:
				obs.AwaitResolved()
			default:
				// Reply/Call/Hook/BufferOverflow flow through the
				// direct OnHook + OnCall/OnReply callbacks; this
				// subscriber only consumes the await one-shots.
			}
		}
	}()

	rtErrCh := make(chan error, 1)
	go func() { rtErrCh <- rt.Serve(runCtx) }()
	// Cancel runCtx before waiting for rt.Serve, otherwise the wait
	// hangs (defers fire LIFO; the cancel needs to come first).
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

	runErrCh := make(chan error, 1)
	go func() {
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
		obs.PipelineDone(runErr)
		runErrCh <- runErr
	}()

	if _, err := program.Run(); err != nil {
		return fmt.Errorf("TUI: %w", err)
	}
	runErr := <-runErrCh
	var pfe *pipeline.PreflightError
	if errors.As(runErr, &pfe) {
		fmt.Fprintf(os.Stderr, "%s\n", pfe.Error())
		runCancel()
		os.Exit(exitCodePreflightFailed) //nolint:gocritic // explicit runCancel above neutralizes defer-skip
	}
	if runErr == nil {
		printEndOfRunSummary(spec.Name, projectRoot, cfg)
	}
	return runErr
}
