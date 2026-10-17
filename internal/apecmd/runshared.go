package apecmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/bridge/config"
	"github.com/exoport/apex_process_ape/internal/effort"
	"github.com/exoport/apex_process_ape/internal/eventing"
	"github.com/exoport/apex_process_ape/internal/pipeline"
	"github.com/exoport/apex_process_ape/internal/runlog"
	"github.com/exoport/apex_process_ape/internal/tui"
	"github.com/spf13/cobra"
)

// The run-output helpers the task, prompt and chat runners share. They
// lived beside `ape pipeline`, which was retired with APEX v0; the runner
// they serve (internal/pipeline) is the one `ape task` still drives.

// addNatsFlags registers the PLAN-13 NATS eventing + transcript-blob flags
// shared by the run commands. All are opt-in and resolve flags → env
// (APE_NATS_URL / APE_NATS_CREDS / …) with no project-config layer.
func addNatsFlags(cmd *cobra.Command, url, creds, prefix *string, upload *bool, store *string) {
	cmd.Flags().StringVar(url, "nats-url", "", "NATS server URL for progress events + transcript upload (env APE_NATS_URL). Empty disables both.")
	cmd.Flags().StringVar(creds, "nats-creds", "", "NATS .creds file; its user identity is baked into every subject (env APE_NATS_CREDS).")
	cmd.Flags().StringVar(prefix, "events-subject-prefix", "ape.evt", "Subject root for progress events.")
	cmd.Flags().BoolVar(upload, "upload-transcripts", false, "At run end, upload the transcript set as content-addressed blobs (env APE_UPLOAD_TRANSCRIPTS=1).")
	cmd.Flags().StringVar(store, "transcript-store", "nats-object", "Transcript blob backend: nats-object|uri-offload (env APE_TRANSCRIPT_STORE).")
}

// printEndOfRunSummary emits the post-run pointer lines:
//   - 📊 report: <path>   (PLAN-3 / M6 — always when manifest written)
//   - 📌 commits: N (...) (PLAN-4 / C8 — only when commits were made)
//
// Path is rendered relative to the project root when possible.
func printEndOfRunSummary(pipelineName, projectRoot string, cfg runConfig) {
	reportPath := pipeline.ReportPathFor(projectRoot, pipelineName, cfg.manifestDir)
	if reportPath == "" {
		return
	}
	displayPath := reportPath
	if rel, err := filepath.Rel(projectRoot, reportPath); err == nil {
		displayPath = rel
	}
	fmt.Fprintf(os.Stdout, "📊 report: %s\n", displayPath)
	if !cfg.noCommit {
		if n := pipeline.CommitsMadeFor(projectRoot, pipelineName, cfg.manifestDir); n > 0 {
			fmt.Fprintf(os.Stdout, "📌 commits: %d (run `git log --oneline --grep '^ape:%s/'` to inspect)\n", n, pipelineName)
		}
	}
}

// plainObserver writes one status line per state transition. Used when
// stdout is non-TTY or --no-tui is set. Per PLAN-1 / I4b, this
// observer also streams parsed stream-json events through the shared
// tui.RenderEvent function so log captures and CI runs see the same
// human-friendly progress feed as the interactive TUI.
type plainObserver struct {
	w            io.Writer
	t0           time.Time
	currentStage string
	currentSkill string
	// projectRoot is forwarded to the event renderer so tool-call
	// file paths display relative to the project (PLAN-2 / F6).
	projectRoot string
	// quiet suppresses the per-event stream that OnStepLine emits
	// (PLAN-2 / F5). Stage / step start+end markers and per-stage
	// summaries still print so failures and timings remain visible.
	quiet bool
}

func newPlainObserver(w io.Writer, projectRoot string, quiet bool) *plainObserver {
	return &plainObserver{w: w, t0: time.Now(), projectRoot: projectRoot, quiet: quiet}
}

func (p *plainObserver) OnStageStart(stage string) {
	p.currentStage = stage
	fmt.Fprintf(p.w, "[%s] stage start: %s\n", elapsed(p.t0), stage)
}

func (p *plainObserver) OnStageEnd(stage string, dur time.Duration, err error) {
	if err != nil {
		fmt.Fprintf(p.w, "[%s] stage FAIL: %s (%s) — %v\n", elapsed(p.t0), stage, fmtDuration(dur), err)
		return
	}
	fmt.Fprintf(p.w, "[%s] stage done: %s (%s)\n", elapsed(p.t0), stage, fmtDuration(dur))
}

// OnStageSkipped and OnStepSkipped make plainObserver a
// pipeline.SkipObserver: a skipped stage gets a line where its start
// would have been, so a log never goes quiet about one.
func (p *plainObserver) OnStageSkipped(stage string, skip pipeline.Skip) {
	fmt.Fprintf(p.w, "[%s] stage skipped: %s (%s: needs %s)\n",
		elapsed(p.t0), stage, skip.Reason, strings.Join(skip.Missing, ", "))
}

func (p *plainObserver) OnStepSkipped(_ string, idx int, step pipeline.Step, skip pipeline.Skip) {
	fmt.Fprintf(p.w, "[%s]   step %d skipped: %s (%s: needs %s)\n",
		elapsed(p.t0), idx+1, step.Skill, skip.Reason, strings.Join(skip.Missing, ", "))
}

func (p *plainObserver) OnStepStart(stage string, idx int, step pipeline.Step) {
	p.currentStage = stage
	p.currentSkill = step.Skill
	tag := step.Skill
	if step.Agent != "" {
		tag = step.Agent + " -> " + step.Skill
	}
	fmt.Fprintf(p.w, "[%s]   step %d: %s\n", elapsed(p.t0), idx+1, tag)
}

// OnStepLine renders each stream-json event the spawned claude
// subprocess emits as a single timestamped, prefixed line on stdout.
// Suppressed events (noisy successful tool_results, etc.) are
// dropped. Same renderer that powers the interactive TUI lives in
// internal/tui/event_renderer.go.
func (p *plainObserver) OnStepLine(stage string, _ int, line string) {
	if p.quiet {
		// PLAN-2 / F5: --quiet suppresses the per-event stream. The
		// stage/step markers in OnStepStart/OnStepEnd still print,
		// so failure summaries (with captured stdout) remain visible.
		return
	}
	r := tui.RenderEventWithRoot(line, p.projectRoot)
	if !r.IsDisplayable() {
		return
	}
	// Use the runner-supplied stage when we have it (covers race-y
	// cases where OnStageStart hasn't recorded yet); fall back to
	// the observer's tracked stage.
	stageName := stage
	if stageName == "" {
		stageName = p.currentStage
	}
	fmt.Fprintf(p.w, "[%s] %s · %s · %s %s\n",
		elapsed(p.t0), stageName, p.currentSkill, r.Glyph, r.Body)
}

func (p *plainObserver) OnStepEnd(_ string, idx int, step pipeline.Step, dur time.Duration, stepOutput string, err error) {
	if err != nil {
		fmt.Fprintf(p.w, "[%s]   step %d FAIL: %s (%s)\n", elapsed(p.t0), idx+1, step.Skill, fmtDuration(dur))
		if stepOutput != "" {
			fmt.Fprintf(p.w, "%s\n", stepOutput)
		}
		return
	}
	fmt.Fprintf(p.w, "[%s]   step %d done: %s (%s)\n", elapsed(p.t0), idx+1, step.Skill, fmtDuration(dur))
}

func elapsed(t0 time.Time) string {
	return fmtDuration(time.Since(t0))
}

func fmtDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
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

// pluralY renders the English "entry"/"entries" suffix, matching the
// message shape render-index-update.py printed so the calling prose in
// the framework reads identically.
func pluralY(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// runConfig bundles CLI-derived knobs passed to the interactive
// runners (runWithInteractive / runWithInteractiveTUI / runWithWeb) and
// to `ape task`. Grouped to keep call-sites stable as new flags land.
type runConfig struct {
	// stopGate, when set, consults an APEX v1 project's stop-hook verdict
	// on every main-session Stop (v1 trial D1). `ape task` sets it.
	stopGate *stopGateRecorder
	// budgetWatch, when set, stops the session at its APEX v1 milestone's
	// enforced budget (backlog 30). `ape task` sets it.
	budgetWatch *v1BudgetWatch
	prompt      string
	manifestDir string
	fromStage   string
	// ignoreExtensions is --ignore-extensions; extensions is the set the
	// command resolved before the UI started (nil: the runner resolves).
	ignoreExtensions      bool
	extensions            *pipeline.Extensions
	noCommit              bool
	allowDirty            bool
	ignoreProjectSettings bool
	// outputStyle pins the spawned session's Claude Code output style.
	// Empty pins config.DefaultOutputStyle; config.InheritOutputStyle
	// leaves the machine's own style alone.
	outputStyle string
	// outputStyleSet records that the operator typed --output-style, so
	// an explicit flag outranks the spec's `output-style:` declarations
	// (see buildSpecPrepends). Empty-string is not a usable sentinel:
	// `--output-style Default` and an absent flag look identical.
	outputStyleSet bool

	// progressWriter redirects the plain observer's progress stream.
	// nil keeps the default (os.Stdout). `ape task --output-format
	// json` routes progress to stderr so stdout carries only the
	// result envelope. PLAN-11.
	progressWriter io.Writer
	// quiet suppresses the per-event stream in the plain observer
	// (same semantics as `ape pipeline --quiet` under --no-tui).
	quiet bool
	// suppressSummary skips printEndOfRunSummary — `ape task` prints
	// its own result envelope / summary instead. PLAN-11.
	suppressSummary bool
	// idleTimeout overrides the interactive step idle-without-Stop
	// backstop (default interactiveStepIdleTimeout). PLAN-11:
	// `ape task --idle-timeout`; PLAN-19 D3: `ape pipeline --idle-timeout`.
	idleTimeout time.Duration
	// maxDuration is the hard wall-clock per-step ceiling (PLAN-19 D2).
	// Flag default 3h; 0 disables. A zero value here (e.g. an apescript
	// run that never set it) is resolved to the 3h default by the caller,
	// so the cap is only disabled by an explicit --max-duration 0.
	maxDuration time.Duration
	// effort is the run-level reasoning effort (--effort). Threaded onto
	// RunOptions.Effort; sits below the pipeline files' `effort:` and above
	// the framework effort table in the resolution chain. Empty means "no run-level
	// override".
	effort string
	// effortTable is the project's `_apex/effort-defaults.yaml`, loaded by
	// prepareEffort before anything spawns; nil when the project has none.
	effortTable *effort.Defaults
	// effortAsArg is the APEX v1 effort rule (pipeline.RunOptions.
	// EffortAsArg): effort is `claude --effort <level>`, and with none
	// nothing is passed — no table, no legacy default. Set by `ape task`
	// in a v1 project.
	effortAsArg bool
	// effortSource is the v1 launch decision's effort source, recorded on
	// the step (pipeline.RunOptions.EffortSource).
	effortSource string
	// spawnSettings are added to the spawn's --settings beside ape's own,
	// and spawnEnv entries to its environment: `ape task` in a v1 project
	// passes the project's own hooks inline (apexv1.InlineHooks), marks the
	// session with apexv1.EnvInlineHooks, and passes the configured plugins
	// (apexv1.PluginSettings).
	spawnSettings spawnSettings
	spawnEnv      []string

	// NATS progress-eventing + transcript-blob upload (PLAN-13). All
	// strictly opt-in and resolved flags → env (natsconn.Resolve); with no
	// URL configured everything below silently disables and a run behaves
	// exactly as before.
	natsURL           string
	natsCreds         string
	eventsPrefix      string // subject root override (default ape.evt)
	uploadTranscripts bool
	transcriptStore   string // nats-object | uri-offload
	// kind is the <kind> subject segment for events (pipeline/task/…).
	// Empty defaults to pipeline in newEventPublisher.
	kind eventing.Kind
	// sessionKind names the owning command in the APE_SESSION marker
	// stamped on every spawned session. Empty means kind, which is right
	// for pipeline and task; change and script dispatch through the task
	// runner and set their own.
	sessionKind string

	// claudeBin overrides the claude executable the interactive runner
	// spawns. Empty defaults to "claude" (pipeline.RunOptions default). A
	// test seam — `apescript` orchestration runs set it to a bash stand-in
	// so a RunTask can be exercised without a real claude.
	claudeBin string
}

// spawnSettings is what a caller adds to a spawn's --settings: hooks beside
// ape's bridge hooks, and top-level keys (config.SettingsOptions).
type spawnSettings struct {
	hooks    []config.ExtraHook
	settings map[string]any
}

// ANSI color escapes for colorized output (doctor). 16-color palette only,
// so the output stays legible on minimal terminals.
const (
	ansiReset = "\x1b[0m"
	ansiBlue  = "\x1b[34m"
	ansiGreen = "\x1b[92m"
)

// Shared flag help.
const (
	helpCwd    = "Project root (default: current working dir)"
	helpFormat = "Output format: human|json|yaml"
)
