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

	"github.com/diegosz/apex_process_ape/internal/bridge/config"
	"github.com/diegosz/apex_process_ape/internal/bridge/orchestrator"
	"github.com/diegosz/apex_process_ape/internal/cost"
	"github.com/diegosz/apex_process_ape/internal/pipeline"
	"github.com/diegosz/apex_process_ape/internal/runlog"
	"github.com/diegosz/apex_process_ape/internal/sessions"
	"github.com/diegosz/apex_process_ape/internal/web"
)

// runWithWeb runs a pipeline with the bridged web UI. Starts a hub
// (broker + IPC listener), builds inline configs, prepends them to
// every per-step claude argv, and routes IPC frames to the runlog
// writer alongside PLAN-3's manifest path. PLAN-5 / C1 + C3 + C6.
func runWithWeb(ctx context.Context, spec *pipeline.Spec, projectRoot string, cfg runConfig) error {
	apeBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("ape pipeline --web: locate self: %w", err)
	}


	// Templates + page once.
	tpl := web.MustTemplates()
	pageHTML := web.RenderPage(tpl, web.PageData{
		Title:    "ape pipeline " + spec.Name,
		Subtitle: projectRoot,
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

	mountExtras := func(mux *http.ServeMux) {
		if err := web.MountAssets(mux); err != nil {
			fmt.Fprintf(os.Stderr, "ape pipeline --web: mount assets: %v\n", err)
		}
		mux.HandleFunc("/dashboard", func(w http.ResponseWriter, _ *http.Request) {
			r, err := cost.LoadRollup(projectRoot)
			if err != nil {
				http.Error(w, "load rollup: "+err.Error(), 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(r)
		})
	}

	hub := orchestrator.NewHub(orchestrator.HubOptions{
		PageHTML:         pageHTML,
		MountExtras:      mountExtras,
		FragmentRenderer: newWebRenderer(tpl),
		OnHook: func(h orchestrator.HookEvent) {
			if rl := getRunLog(); rl != nil {
				_ = rl.Hook(runlog.HookEntry{
					Timestamp: h.At,
					Event:     h.Event,
					Step:      h.Step,
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
	url, err := hub.Listen()
	if err != nil {
		return fmt.Errorf("ape pipeline --web: hub listen: %w", err)
	}

	// Stop button → cancel run context.
	hubCtx, hubCancel := context.WithCancel(ctx)
	defer hubCancel()
	hub.SetStopFn(hubCancel)

	hubErrCh := make(chan error, 1)
	go func() { hubErrCh <- hub.Serve(hubCtx) }()
	defer func() {
		hubCancel()
		<-hubErrCh
	}()

	fmt.Fprintf(os.Stderr, "web ui: %s\n", url)
	if cfg.openOnStart {
		_ = openBrowser(url)
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
	settings, err := config.BuildSettings(config.SettingsOptions{
		APEBin:     apeBin,
		BridgePort: hub.IPCPort(),
		Mode:       config.ModeWeb,
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
	// Track which stages we've already published so the first emission
	// of a stage appends a new card; subsequent emissions replace it.
	// HTMX hx-swap-oob silently drops fragments whose target #id is
	// missing, so we must seed the target on first sight.
	var (
		seenMu sync.Mutex
		seen   = map[string]bool{}
	)
	publishStageCard := func(stage, statusClass, glyph, line string) {
		slug := slugify(stage)
		seenMu.Lock()
		first := !seen[slug]
		seen[slug] = true
		seenMu.Unlock()
		card := fmt.Sprintf(
			`<div id="stage-%s" class="stage %s"%s><div class="stage-head"><span class="stage-glyph">%s</span><span class="stage-name">%s</span></div><div class="stage-last-hook">%s</div></div>`,
			slug, statusClass,
			"", // OOB attribute placeholder (set below)
			htmlEscape(glyph), htmlEscape(stage), htmlEscape(line),
		)
		if first {
			// Append a new card to #stages.
			frag := fmt.Sprintf(`<div hx-swap-oob="beforeend:#stages">%s</div>`, card)
			hub.Publish("stage-start", frag)
		} else {
			// Replace existing card by id. hx-swap-oob="true" =
			// outerHTML by matching id attribute.
			frag := strings.Replace(card, `id="stage-`+slug+`" class="stage `+statusClass+`"`,
				`id="stage-`+slug+`" class="stage `+statusClass+`" hx-swap-oob="true"`, 1)
			hub.Publish("stage-update", frag)
		}
	}
	onStageStart := func(stage string) {
		publishStageCard(stage, "running", "⟳", "running…")
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

	runErr := pipeline.Run(ctx, spec, pipeline.RunOptions{
		ProjectRoot:  projectRoot,
		Prompt:       cfg.prompt,
		Observer:     newPlainObserver(os.Stdout, projectRoot, true),
		ApeVersion:   Version,
		ManifestDir:  cfg.manifestDir,
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
	})

	// Fold totals into cost-rollup.
	if r, err := cost.LoadRollup(projectRoot); err == nil {
		r.FoldChat("pipeline-"+spec.Name, time.Now().UTC(), cost.Totals{})
		_ = cost.SaveRollup(projectRoot, r)
	}

	if rl := getRunLog(); rl != nil {
		_ = rl.Close()
	}

	var pfe *pipeline.PreflightError
	if errors.As(runErr, &pfe) {
		fmt.Fprintf(os.Stderr, "%s\n", pfe.Error())
		os.Exit(exitCodePreflightFailed) //nolint:gocritic // explicit exit
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
