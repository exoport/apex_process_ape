package apecmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/diegosz/apex_process_ape/internal/bridge/broker"
	"github.com/diegosz/apex_process_ape/internal/bridge/config"
	"github.com/diegosz/apex_process_ape/internal/bridge/ipc"
	"github.com/diegosz/apex_process_ape/internal/bridge/orchestrator"
	"github.com/diegosz/apex_process_ape/internal/cost"
	"github.com/diegosz/apex_process_ape/internal/pipeline"
	"github.com/diegosz/apex_process_ape/internal/runlog"
	"github.com/diegosz/apex_process_ape/internal/sessions"
	"github.com/diegosz/apex_process_ape/internal/web"
)

// webHookStepTracker is the programmatic-web analogue of the
// interactiveCore.activeStep state — a thread-safe `<stage>/<skill>`
// label tracking the runner's current step so the hub's OnHook
// callback can tag hook events that arrive with an empty Step
// (which is every event — `ape notify` cannot populate Step, and
// only the interactive core handles tagging on its own). The
// interactive web path doesn't use this; its core.FeedHook tags
// via core.activeStep.
type webHookStepTracker struct {
	mu    sync.Mutex
	label string
}

func (t *webHookStepTracker) set(label string) {
	t.mu.Lock()
	t.label = label
	t.mu.Unlock()
}

func (t *webHookStepTracker) clear() {
	t.mu.Lock()
	t.label = ""
	t.mu.Unlock()
}

func (t *webHookStepTracker) get() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.label
}

// stepTaggingObserver wraps a pipeline.Observer and updates a
// webHookStepTracker on OnStepStart / OnStepEnd. Used to plumb the
// runner's current-step state into the hub's OnHook callback in
// programmatic web mode (`--web -P`). All other observer methods
// pass through unchanged.
type stepTaggingObserver struct {
	child   pipeline.Observer
	tracker *webHookStepTracker
}

func (s *stepTaggingObserver) OnStageStart(stage string) { s.child.OnStageStart(stage) }
func (s *stepTaggingObserver) OnStageEnd(stage string, dur time.Duration, err error) {
	s.child.OnStageEnd(stage, dur, err)
}

func (s *stepTaggingObserver) OnStepStart(stage string, idx int, step pipeline.Step) {
	// idx is 0-based per the Observer contract; StepLabel uses 1-based
	// to match the manifest's step numbering.
	s.tracker.set(pipeline.StepLabel(stage, idx+1, step.Skill))
	s.child.OnStepStart(stage, idx, step)
}

func (s *stepTaggingObserver) OnStepLine(stage string, idx int, line string) {
	s.child.OnStepLine(stage, idx, line)
}

func (s *stepTaggingObserver) OnStepEnd(stage string, idx int, step pipeline.Step, dur time.Duration, out string, err error) {
	s.tracker.clear()
	s.child.OnStepEnd(stage, idx, step, dur, out, err)
}

// runWithWeb runs a pipeline with the bridged web UI. Starts a hub
// (broker + IPC listener), builds inline configs, prepends them to
// every per-step claude argv, and routes IPC frames to the runlog
// writer alongside PLAN-3's manifest path. PLAN-5 / C1 + C3 + C6.
//
//nolint:gocyclo,maintidx // single-spawn web orchestration: hub setup, runlog wiring, runner integration, and shutdown all need to share state; splitting harms readability more than it helps.
func runWithWeb(ctx context.Context, spec *pipeline.Spec, projectRoot string, cfg runConfig, interactive bool) error {
	apeBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("ape pipeline --web: locate self: %w", err)
	}

	// Templates + page once.
	tpl := web.MustTemplates()

	// Seed the page with one pending card per stage so the user
	// sees the whole pipeline shape from the first paint, not just
	// the running one. SSE stage-start/end will replace these by id.
	stages := spec.Stages()
	stageSeeds := make([]web.StageSeed, 0, len(stages))
	for _, st := range stages {
		stageSeeds = append(stageSeeds, web.StageSeed{Slug: slugify(st.Name), Name: st.Name})
	}
	pageHTML := web.RenderPage(tpl, web.PageData{
		Title:    "ape pipeline " + spec.Name,
		Subtitle: projectRoot,
		Mode:     "pipeline",
		Stages:   stageSeeds,
	})

	// Hub state. RunLog binds after the runner picks the run id.
	var (
		runLogMu sync.Mutex
		runLog   *runlog.Writer
	)
	getRunLog := func() *runlog.Writer {
		runLogMu.Lock()
		defer runLogMu.Unlock()
		return runLog
	}

	// Latest published stage fragment per slug + insertion order. Replayed
	// on every new SSE subscription so a browser that opens after a stage
	// has already started (or that refreshes mid-run) restores the live
	// state instead of seeing a stale "pending" card. The very first
	// stage's stage-start fires within milliseconds of pipeline.Run and
	// would otherwise be lost to zero subscribers.
	var (
		stageStateMu    sync.Mutex
		stageStateOrder []string
		stageStateByKey = map[string]broker.Event{}
	)
	rememberStage := func(slug string, ev broker.Event) {
		stageStateMu.Lock()
		defer stageStateMu.Unlock()
		if _, seen := stageStateByKey[slug]; !seen {
			stageStateOrder = append(stageStateOrder, slug)
		}
		stageStateByKey[slug] = ev
	}
	replayStages := func() []broker.Event {
		stageStateMu.Lock()
		defer stageStateMu.Unlock()
		out := make([]broker.Event, 0, len(stageStateOrder))
		for _, slug := range stageStateOrder {
			out = append(out, stageStateByKey[slug])
		}
		return out
	}

	mountExtras := func(mux *http.ServeMux) {
		if err := web.MountAssets(mux); err != nil {
			fmt.Fprintf(os.Stderr, "ape pipeline --web: mount assets: %v\n", err)
		}
		mux.HandleFunc("/dashboard", func(w http.ResponseWriter, _ *http.Request) {
			r, err := cost.LoadRollup(projectRoot)
			if err != nil {
				http.Error(w, "load rollup: "+err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(r)
		})
	}

	// runCtx is constructed below for the stop-button cancellation
	// path; under interactive exec we also need it for the contract
	// verifier's OnViolation hook (which cancels the run). Construct
	// the core early so the Hub's OnHook can feed it.
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	var core *interactiveCore
	var stepTracker *webHookStepTracker
	if interactive {
		core = newInteractiveCore(runCancel, getRunLog)
	} else {
		stepTracker = &webHookStepTracker{}
	}

	hub := orchestrator.NewHub(orchestrator.HubOptions{
		PageHTML:         pageHTML,
		MountExtras:      mountExtras,
		FragmentRenderer: newWebRenderer(tpl, projectRoot),
		ReplayEvents:     replayStages,
		OnHook: func(h orchestrator.HookEvent) {
			// When the interactive core is active it writes to runlog
			// itself (and applies activeStep tagging). The direct write
			// below is the non-interactive web path (`--web -P`).
			if core != nil {
				core.FeedHook(h)
				return
			}
			if rl := getRunLog(); rl != nil {
				step := h.Step
				if step == "" && stepTracker != nil {
					// `ape notify` cannot populate Step; the runner-side
					// stepTracker (updated via stepTaggingObserver from
					// the pipeline's OnStepStart / OnStepEnd) supplies
					// the active `<stage>/<idx>-<skill>` label.
					step = stepTracker.get()
				}
				// Symlink the claude session transcript into transcripts/
				// on the first UPS for this step. Per-step sessions in
				// --web -P mean each link points to a unique target.
				if h.Event == ipc.HookUserPromptSubmit && step != "" {
					if tp := extractTranscriptPath(h.Payload); tp != "" {
						_ = rl.LinkTranscript(transcriptLinkName(step), tp)
					}
				}
				_ = rl.Hook(runlog.HookEntry{
					Timestamp: h.At,
					Event:     h.Event,
					Step:      step,
					SessionID: h.SessionID,
					AgentID:   h.AgentID,
					Payload:   h.Payload,
				})
			}
		},
		OnCall: func(c orchestrator.ToolCall) {
			if rl := getRunLog(); rl != nil {
				_ = rl.Call(runlog.CallEntry{
					Timestamp: c.At,
					Method:    "tools/call",
					Tool:      c.Tool,
					Params:    c.Params,
					Result:    c.Result,
					SessionID: c.SessionID,
					ID:        c.ID,
				})
			}
		},
		OnReply: func(content string) {
			if rl := getRunLog(); rl != nil {
				_ = rl.Checkpoint(runlog.CheckpointEntry{
					Kind:    "reply",
					Payload: map[string]any{"content": content},
				})
			}
		},
	})
	url, err := hub.Listen(ctx)
	if err != nil {
		return fmt.Errorf("ape pipeline --web: hub listen: %w", err)
	}

	// Two contexts on purpose:
	//   - runCtx: the context pipeline.Run uses. Stop button cancels
	//     this, which propagates SIGKILL to the active `claude`
	//     subprocess via exec.CommandContext, unwinding the runner
	//     to publish a terminal banner.
	//   - hubCtx: the broker's lifetime. Kept separate so the
	//     "stopping…" banner can flush over SSE before the broker
	//     shuts down. Cancelled after the runner returns (defer).
	// runCtx is constructed earlier (above the Hub) so the
	// interactive core can wire OnViolation -> runCancel.
	hubCtx, hubCancel := context.WithCancel(ctx)
	defer hubCancel()
	stopReq := func() {
		// Immediate visual feedback even before the next stage end.
		hub.Publish("pipeline-end", `<div id="status" class="disconnected" hx-swap-oob="outerHTML:#status">stopping…</div>`)
		runCancel()
	}
	hub.SetStopFn(stopReq)

	hubErrCh := make(chan error, 1)
	go func() { hubErrCh <- hub.Serve(hubCtx) }()
	defer func() {
		hubCancel()
		<-hubErrCh
	}()

	fmt.Fprintf(os.Stderr, "web ui: %s\n", url)
	if cfg.openOnStart {
		_ = openBrowser(ctx, url)
	}

	// PLAN-5 / C5 — register the session.
	regRow := sessions.Session{
		PID:       os.Getpid(),
		CWD:       projectRoot,
		Command:   "ape " + strings.Join(os.Args[1:], " "),
		Port:      hub.BrokerPort(),
		URL:       url,
		StartedAt: time.Now().UTC(),
	}
	regPath := sessions.DefaultPath()
	_ = sessions.Register(regPath, regRow)
	defer func() { _ = sessions.Deregister(regPath, regRow.PID) }()

	// Inline configs — once for the whole run. Every step's claude
	// invocation gets these prepended via opts.PrependFlags.
	mcpCfg, err := config.BuildMCPConfig(config.MCPOptions{APEBin: apeBin, IPCPort: hub.IPCPort()})
	if err != nil {
		return err
	}
	snapDir := newHookSnapshotDir()
	if snapDir != "" {
		if core != nil {
			core.snapshotDir = snapDir
		}
		defer os.RemoveAll(snapDir)
	}
	settings, err := config.BuildSettings(config.SettingsOptions{
		APEBin:      apeBin,
		BridgePort:  hub.IPCPort(),
		Mode:        config.ModeWeb,
		SnapshotDir: snapDir,
	})
	if err != nil {
		return err
	}
	prepend := []string{
		"--strict-mcp-config",
		"--mcp-config", string(mcpCfg),
		"--settings", string(settings),
	}
	if cfg.ignoreProjectSettings {
		prepend = append(prepend, "--setting-sources", "user")
	}

	// runlog binds after the runner picks the run id. We open it
	// lazily on the first stage-start callback — the runner has
	// resolved the run dir by then via manifestWriter, and we can
	// derive the matching path from <manifestBase>/<pipeline>/latest.
	// Every stage card is pre-rendered in the page seed, so every
	// stage event is a straight outerHTML replace by id.
	publishStageCard := func(stage, statusClass, glyph, line string) {
		slug := slugify(stage)
		frag := fmt.Sprintf(
			`<div id="stage-%s" class="stage %s" hx-swap-oob="outerHTML:#stage-%s"><div class="stage-head"><span class="stage-glyph">%s</span><span class="stage-name">%s</span></div><div class="stage-last-hook">%s</div></div>`,
			slug, statusClass, slug,
			htmlEscape(glyph), htmlEscape(stage), htmlEscape(line),
		)
		event := "stage-update"
		switch statusClass {
		case "running":
			event = "stage-start"
		case "done", "failed", "stopped":
			event = "stage-end"
		}
		rememberStage(slug, broker.Event{Name: event, Data: frag})
		hub.Publish(event, frag)
	}
	onStageStart := func(stage string) {
		publishStageCard(stage, "running", "⟳", "running…")
		if core != nil {
			// Interactive web: reset per-stage transcript scan baseline
			// so the first step's telemetry delta equals its own
			// absolute usage. Same hook the no-UI / TUI variants wire.
			core.ResetStageTelemetry(stage)
		}
	}
	onRunDir := func(dir string) {
		runLogMu.Lock()
		defer runLogMu.Unlock()
		if runLog == nil {
			if rl, err := runlog.New(dir); err == nil {
				runLog = rl
			}
		}
	}
	onStageEnd := func(stage string, dur time.Duration, err error) {
		statusClass := "done"
		glyph := "✓"
		line := fmt.Sprintf("done in %s", dur.Round(time.Second))
		if err != nil {
			statusClass = "failed"
			glyph = "✗"
			line = "failed: " + err.Error()
		}
		publishStageCard(stage, statusClass, glyph, line)
	}

	runStart := time.Now()
	var observer pipeline.Observer = newPlainObserver(os.Stdout, projectRoot, true)
	if stepTracker != nil {
		// Programmatic web: wrap so OnStepStart/End updates the
		// tracker the hub's OnHook callback reads. Interactive web
		// uses the core's activeStep instead; no wrapper needed.
		observer = &stepTaggingObserver{child: observer, tracker: stepTracker}
	}
	runOptions := pipeline.RunOptions{
		ProjectRoot:  projectRoot,
		Prompt:       cfg.prompt,
		Observer:     observer,
		ApeVersion:   Version,
		ManifestDir:  cfg.manifestDir,
		FromStage:    cfg.fromStage,
		NoCommit:     cfg.noCommit,
		AllowDirty:   cfg.allowDirty,
		PrependFlags: prepend,
		OnStageStart: onStageStart,
		OnStageEnd:   onStageEnd,
		OnRunDir:     onRunDir,
		// RunLog is attached lazily via the closure inside OnStageStart;
		// we cannot pass *runlog.Writer here because it doesn't exist
		// until the runner has resolved the run dir.
		RunLog: &lazyRunLog{getter: getRunLog},
	}
	if core != nil {
		runOptions.Interactive = true
		runOptions.WaitStepDone = core.WaitStepDone
		runOptions.StepTelemetryFn = core.StepTelemetry
		runOptions.OnInteractiveStepStart = core.OnStepStart
		runOptions.OnInteractiveStepEnd = core.OnStepEnd
	}
	runErr := pipeline.Run(runCtx, spec, runOptions)

	// Publish a terminal banner so the page shows 'completed' (or
	// 'failed') rather than the htmx 'disconnected' that would fire
	// when we tear the broker down. Sleep briefly so SSE flush
	// reaches the browser before hubCancel below shuts the server.
	dur := time.Since(runStart).Round(time.Second)
	var (
		statusClass string
		statusGlyph string
		statusText  string
		bannerClass string
	)
	switch {
	case runErr == nil:
		statusClass, statusGlyph = "connected", "✓"
		statusText = fmt.Sprintf("completed in %s", dur)
		bannerClass = "summary-ok"
	case errors.Is(runErr, context.Canceled):
		statusClass, statusGlyph = "disconnected", "⏸"
		statusText = fmt.Sprintf("stopped after %s", dur)
		bannerClass = "summary-stopped"
	default:
		statusClass, statusGlyph = "disconnected", "✗"
		statusText = "failed: " + runErr.Error()
		bannerClass = "summary-fail"
	}
	// Three OOB updates in one event: header banner, page-level
	// summary block above stages, and a final row in the activity
	// feed. Multiple top-level OOB elements in one SSE payload all
	// fire — htmx processes each in turn.
	finalFrag := fmt.Sprintf(
		`<div id="status" class="%s" hx-swap-oob="outerHTML:#status">%s %s</div>`+
			`<div id="pipeline-summary" class="pipeline-summary %s" hx-swap-oob="outerHTML:#pipeline-summary">%s %s</div>`+
			`<button id="stop-btn" hx-swap-oob="outerHTML:#stop-btn" hidden></button>`+
			`<div hx-swap-oob="beforeend:#hooks"><div class="hook-row hook-summary"><span class="ts">%s</span><span class="event">%s</span><span class="tool"></span><span class="summary">%s</span></div></div>`,
		statusClass, statusGlyph, htmlEscape(statusText),
		bannerClass, statusGlyph, htmlEscape(statusText),
		time.Now().Local().Format("15:04:05"), //nolint:gosmopolitan // intentional: status banner shows the user's wall-clock time
		statusGlyph+" pipeline",
		htmlEscape(statusText),
	)
	hub.Publish("pipeline-end", finalFrag)
	time.Sleep(300 * time.Millisecond)

	// Fold this run's totals into cost-rollup. Easiest path: rebuild
	// from on-disk artefacts so the rollup also reconciles any prior
	// runs that didn't fold on exit (crashes, kill -9, etc.).
	if _, err := cost.RebuildRollup(projectRoot); err != nil {
		fmt.Fprintf(os.Stderr, "ape pipeline --web: rebuild cost rollup: %v\n", err)
	}

	if rl := getRunLog(); rl != nil {
		_ = rl.Close()
	}

	var pfe *pipeline.PreflightError
	if errors.As(runErr, &pfe) {
		fmt.Fprintf(os.Stderr, "%s\n", pfe.Error())
		os.Exit(exitCodePreflightFailed) //nolint:gocritic // explicit exit
	}
	if runErr != nil {
		// Match the convention used elsewhere in apecmd (see
		// pipeline.go's LoadSpec handling): print the error to
		// stderr explicitly because rootCmd has SilenceErrors=true.
		// Without this, the dirty-tree gate's actionable message
		// (and any other non-Preflight error from pipeline.Run)
		// gets swallowed and ape exits 1 with no explanation.
		fmt.Fprintf(os.Stderr, "Error: %s\n", runErr.Error())
	}
	if runErr == nil {
		printEndOfRunSummary(spec.Name, projectRoot, cfg)
	}
	return runErr
}

// lazyRunLog wraps a *runlog.Writer accessor so the runner can call
// CheckpointKindStep before the underlying writer is opened. Methods
// no-op until getter returns non-nil.
type lazyRunLog struct {
	getter func() *runlog.Writer
}

func (l *lazyRunLog) CheckpointKindStep(kind, step string, payload any, at time.Time) {
	w := l.getter()
	if w == nil {
		return
	}
	w.CheckpointKindStep(kind, step, payload, at)
}

// slugify is the small id-safe slug helper used in stage-card OOB swap
// targets. Lowercases, replaces non-alphanumerics with dashes. Kept
// duplicated from the web package's intent so this file doesn't need
// to expose another helper across the boundary.
func slugify(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := b.String()
	out = strings.TrimRight(out, "-")
	if out == "" {
		out = "stage"
	}
	return out
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
