package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/diegosz/apex_process_ape/internal/tmux"
)

// runStagesInteractive drives a pipeline in PLAN-6 interactive exec
// mode: one `claude` process per stage running inside its own tmux
// session, prompts delivered as real keystrokes via `tmux send-keys`,
// the bridge's `Stop` hook signalling step done. Within a stage,
// steps share the same claude session and use `/clear` between them
// to reset the model's working context.
//
// Step contract (PLAN-6 / C4, tmux-driven):
//
//   - The skill prompt follows PAT-25:
//     "/{agent} --autonomous -- {skill} --autonomous {args} {promptFlag prompt}"
//     or "/{skill} --autonomous --no-commit {args} {promptFlag prompt}"
//     when agent is unset.
//   - Between steps within a stage, the runner sends `/clear` so the
//     next step starts with a fresh model context.
//   - The bridge's UserPromptSubmit hook fires with the literal slash
//     command (claude's REPL forwards it); the ContractVerifier
//     enforces the expected agent + skill prefix.
//
// Context isolation between stages comes from a fresh tmux session
// + claude process spawn; within a stage, `/clear` provides the
// per-step reset (skills are written assuming a clean working
// context). A step may set `NoClear` to opt out — used for skills
// that need to see the previous step's context (rare).
func runStagesInteractive(ctx context.Context, spec *Spec, opts RunOptions, mw *manifestWriter) error { //nolint:gocritic // RunOptions mirrors Run's parameter shape; see Run's nolint rationale
	for _, stage := range spec.Stages() {
		stageStart := time.Now()
		var stageIdx int
		if mw != nil {
			stageIdx = mw.BeginStage(stage.Name, stageStart)
		}
		notify(opts.Observer, func(o Observer) { o.OnStageStart(stage.Name) })
		if opts.OnStageStart != nil {
			opts.OnStageStart(stage.Name)
		}
		runLog(opts.RunLog, "stage-start", stage.Name, nil)

		stageStatus, stageErr := runStageInteractive(ctx, spec, stage, opts, mw, stageIdx)

		if mw != nil {
			_ = mw.EndStage(stageIdx, stageStatus, time.Now())
		}
		notify(opts.Observer, func(o Observer) { o.OnStageEnd(stage.Name, time.Since(stageStart), stageErr) })
		if opts.OnStageEnd != nil {
			opts.OnStageEnd(stage.Name, time.Since(stageStart), stageErr)
		}
		if stageErr != nil {
			runLog(opts.RunLog, "stage-end", stage.Name, map[string]any{"error": errMessage(stageErr)})
			return stageErr
		}
		runLog(opts.RunLog, "stage-end", stage.Name, nil)
	}
	return nil
}

// interactiveReadyTimeout bounds the wait for claude's REPL to come
// up inside the tmux session. The previous PTY design used 30s for
// the bridge-ready handshake; tmux + claude REPL is typically ready
// in under 2s, so 30s is plenty of headroom while still bounding the
// failure mode.
const interactiveReadyTimeout = 30 * time.Second

// interactiveClearSettle is how long we wait after sending `/clear`
// before the next slash command. Empirically claude redraws within
// ~200ms; doubling to 500ms keeps the prompt from racing the redraw.
const interactiveClearSettle = 500 * time.Millisecond

// runStageInteractive spawns one claude inside a tmux session for the
// stage, sends each step's prompt as a real REPL slash command via
// tmux send-keys, waits for the bridge's Stop hook between steps,
// emits per-step manifest records, and tears the session down at the
// end.
//
//nolint:funlen,gocyclo // single-spawn stage orchestration intentionally lives in one function; the keystroke / wait / capture interaction is clearer in one place than fragmented across helpers.
func runStageInteractive(ctx context.Context, spec *Spec, stage Stage, opts RunOptions, mw *manifestWriter, stageIdx int) (RunStatus, error) {
	if len(stage.Chain) == 0 {
		return StatusCompleted, nil
	}

	firstStep := stage.Chain[0]
	firstModel, _, _, err := spec.Effective(stage.Name, 0)
	if err != nil {
		return StatusFailed, fmt.Errorf("stage %q: resolve effective values: %w", stage.Name, err)
	}
	if firstModel == "" {
		firstModel = firstStep.Model
	}

	plan, planErr := spec.PlanStageCommits(stage.Name)
	if planErr != nil {
		return StatusFailed, fmt.Errorf("stage %q: plan commits: %w", stage.Name, planErr)
	}

	argv, argvErr := buildInteractiveArgv(opts.ClaudeBin, firstModel, opts.PrependFlags)
	if argvErr != nil {
		return StatusFailed, argvErr
	}

	sessionName := fmt.Sprintf("ape-%s-%d", sanitizeSessionName(stage.Name), os.Getpid())
	// Ensure no stale session by that name; ignore not-found error.
	_ = tmux.KillSession(sessionName)
	if err := tmux.NewSession(sessionName, opts.ProjectRoot, argv); err != nil {
		return StatusFailed, fmt.Errorf("stage %q: %w", stage.Name, err)
	}
	defer func() { _ = tmux.KillSession(sessionName) }()

	readyCtx, cancelReady := context.WithTimeout(ctx, interactiveReadyTimeout)
	if err := tmux.WaitForReady(readyCtx, sessionName); err != nil {
		cancelReady()
		return StatusFailed, fmt.Errorf("stage %q: claude REPL not ready in tmux session: %w", stage.Name, err)
	}
	cancelReady()

	// Optional debug mirror — captures the pane state to stderr after
	// each step so a failing run leaves a trace. Replaces the PTY's
	// raw stream mirror.
	debugInteractive := os.Getenv("APE_INTERACTIVE_DEBUG") != ""

	stageStatus := StatusCompleted
	var stageErr error
	for i, step := range stage.Chain {
		stepStart := time.Now()
		notify(opts.Observer, func(o Observer) { o.OnStepStart(stage.Name, i, step) })

		effModel, effAgent, _, effErr := spec.Effective(stage.Name, i)
		if effErr != nil {
			stageErr = fmt.Errorf("stage %q step %d: resolve effective values: %w", stage.Name, i, effErr)
			stageStatus = StatusFailed
			break
		}
		if effModel == "" {
			effModel = step.Model
		}
		if effAgent == "" {
			effAgent = step.Agent
		}

		eventLog, eventsRel := openStepLog(mw, stageIdx, i+1, stage.Name, step.Skill)

		stepInfo := InteractiveStepInfo{
			Stage:   stage.Name,
			StepIdx: i,
			Skill:   step.Skill,
			Agent:   effAgent,
			Model:   effModel,
			NoClear: step.NoClear,
		}

		// Between steps within a stage: send `/clear` so the next
		// step starts with a fresh model context. Must fire BEFORE
		// OnInteractiveStepStart (and therefore before the verifier's
		// BeginStep) — otherwise the `/clear` UserPromptSubmit hook
		// would arrive against the new step's contract and trip a
		// spurious agent-prefix violation. The first step gets a
		// clean session by construction. NoClear opts out for skills
		// that need the previous step's context.
		if i > 0 && !step.NoClear {
			if err := tmux.SendCommand(sessionName, "/clear"); err != nil {
				stageErr = fmt.Errorf("stage %q step %d: send /clear: %w", stage.Name, i, err)
				stageStatus = StatusFailed
				closeStepLog(eventLog)
				break
			}
			select {
			case <-ctx.Done():
				stageErr = ctx.Err()
				stageStatus = StatusFailed
				closeStepLog(eventLog)
				return stageStatus, stageErr
			case <-time.After(interactiveClearSettle):
			}
		}

		if opts.OnInteractiveStepStart != nil {
			opts.OnInteractiveStepStart(stepInfo)
		}

		// Capture the pane state BEFORE we send the prompt so we can
		// diff against it after WaitStepDone returns and lift just
		// this step's output into the manifest record.
		beforeSnap, _ := tmux.CapturePane(sessionName)

		prompt := assembleInteractivePromptLine(effAgent, step, opts.Prompt)
		writeInteractiveStepEvent(eventLog, "step-start", map[string]any{
			"stage":   stage.Name,
			"step":    i + 1,
			"skill":   step.Skill,
			"agent":   effAgent,
			"model":   effModel,
			"prompt":  prompt,
			"no_clear": step.NoClear,
		})
		if err := tmux.SendCommand(sessionName, prompt); err != nil {
			stageErr = fmt.Errorf("stage %q step %d: send prompt: %w", stage.Name, i, err)
			stageStatus = StatusFailed
			if opts.OnInteractiveStepEnd != nil {
				opts.OnInteractiveStepEnd(stepInfo)
			}
			closeStepLog(eventLog)
			break
		}

		// Wait for the bridge's Stop hook to signal step done.
		waitErr := waitStepDone(ctx, opts, stage.Name, i)
		if opts.OnInteractiveStepEnd != nil {
			opts.OnInteractiveStepEnd(stepInfo)
		}
		if waitErr != nil {
			stageErr = fmt.Errorf("stage %q step %d: wait done: %w", stage.Name, i, waitErr)
			stageStatus = StatusFailed
			closeStepLog(eventLog)
			break
		}

		afterSnap, _ := tmux.CapturePane(sessionName)
		stepOut := diffPaneSnapshot(beforeSnap, afterSnap)
		// Emit per-line for any observer that cares.
		for _, line := range strings.Split(strings.TrimRight(stepOut, "\n"), "\n") {
			if line == "" {
				continue
			}
			notify(opts.Observer, func(o Observer) { o.OnStepLine(stage.Name, i, line) })
		}
		if debugInteractive {
			fmt.Fprintf(os.Stderr, "[tmux/%s/step%d]\n%s\n[/tmux]\n", sessionName, i, stepOut)
		}

		writeInteractiveStepEvent(eventLog, "step-end", map[string]any{
			"stage":         stage.Name,
			"step":          i + 1,
			"skill":         step.Skill,
			"duration_secs": time.Since(stepStart).Seconds(),
		})
		closeStepLog(eventLog)

		exitCode := 0
		// In interactive mode the pane snapshot never carries the
		// stream-json `result` event (claude REPL emits no stream-json
		// on stdout). Pull telemetry from the session transcript via
		// the apecmd-supplied callback instead.
		var ev *resultEvent
		if opts.StepTelemetryFn != nil {
			if tele := opts.StepTelemetryFn(stage.Name, i); tele != nil {
				ev = stepTelemetryToResultEvent(tele)
			}
		}
		recordStep(mw, stageIdx, i+1, step, opts.Prompt, stepStart, time.Now(), StatusCompleted, exitCode, eventsRel, ev)

		// Commit boundary: same semantics as runStages (PLAN-6 / C2).
		// Runs only after the step's run-state is recorded so the
		// manifest reflects both the run outcome and the commit
		// outcome atomically. Commit failures abort the stage.
		isLastStep := i == len(stage.Chain)-1
		commitErr := performStepCommit(ctx, opts, mw, plan, stageIdx, i+1, isLastStep, nil)
		if commitErr == nil {
			runLog(opts.RunLog, "commit-made", StepLabel(stage.Name, i+1, step.Skill), nil)
		}

		notify(opts.Observer, func(o Observer) { o.OnStepEnd(stage.Name, i, step, time.Since(stepStart), stepOut, nil) })

		if commitErr != nil {
			stageErr = fmt.Errorf("stage %q step %d (%s) commit: %w", stage.Name, i, step.Skill, commitErr)
			stageStatus = StatusFailed
			break
		}
	}

	return stageStatus, stageErr
}

// stepTelemetryToResultEvent adapts the apecmd-side StepTelemetry
// (transcript-derived) onto the unexported resultEvent shape so the
// existing recordStep / manifestWriter API stays uniform across
// programmatic (stream-json source) and interactive (transcript scan
// source) exec modes.
func stepTelemetryToResultEvent(t *StepTelemetry) *resultEvent {
	if t == nil {
		return nil
	}
	ev := &resultEvent{
		Type:         "result",
		Subtype:      "success",
		NumTurns:     t.NumTurns,
		TotalCostUSD: t.CostUSD,
	}
	ev.Usage.InputTokens = t.TokensInput
	ev.Usage.OutputTokens = t.TokensOutput
	ev.Usage.CacheReadInputTokens = t.TokensCacheRead
	ev.Usage.CacheCreationInputTokens = t.TokensCacheCreation
	return ev
}

// writeInteractiveStepEvent appends one JSON line to a per-step ndjson
// file. Best-effort: a nil writer or marshal failure drops silently
// because the authoritative hook stream lives in hook-events.jsonl.
func writeInteractiveStepEvent(w io.Writer, kind string, fields map[string]any) {
	if w == nil {
		return
	}
	if fields == nil {
		fields = map[string]any{}
	}
	fields["type"] = kind
	fields["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	b, err := json.Marshal(fields)
	if err != nil {
		return
	}
	_, _ = w.Write(append(b, '\n'))
}

// sanitizeSessionName replaces characters tmux doesn't like in session
// names (whitespace, `:`, `.`, `'`) with `-` so the stage name can be
// used directly without surprises.
func sanitizeSessionName(s string) string {
	r := strings.NewReplacer(
		" ", "-",
		":", "-",
		".", "-",
		"'", "-",
		"\"", "-",
		"/", "-",
	)
	return r.Replace(s)
}

// diffPaneSnapshot returns the lines present in after but not in
// before, preserving order. tmux capture-pane returns the full
// scrollback, so after is a superset of before for the parts the
// REPL hasn't scrolled out. A line-set diff is good enough for the
// manifest output buffer — the runlog has the authoritative bridge
// calls / hooks; capture-pane output is just for human inspection.
func diffPaneSnapshot(before, after string) string {
	if before == "" {
		return after
	}
	// Trim trailing blank lines from before so the comparison anchor
	// is the last meaningful line, not the empty bottom of the
	// terminal grid.
	bTrim := strings.TrimRight(before, "\n \t")
	idx := strings.LastIndex(after, bTrim)
	if idx < 0 {
		// Snapshot moved (scrollback overflow) — return the whole
		// after-snap. Coarse but never wrong.
		return after
	}
	tail := after[idx+len(bTrim):]
	return strings.TrimLeft(tail, "\n")
}

// buildInteractiveArgv builds the argv for a per-stage claude
// invocation. claude runs in true REPL mode inside a tmux session —
// no `-p`, no `--system-prompt`. Prompts arrive as real keystrokes
// from `tmux send-keys`, so the model parses slash commands exactly
// as it would for a human user. The MCP bridge is still wired
// (`--mcp-config`, `--settings`) for hook observability — the
// runner reads UserPromptSubmit / Stop hooks but no longer drives
// prompts through `await_message`.
func buildInteractiveArgv(claudeBin, model string, prependFlags []string) ([]string, error) {
	if claudeBin == "" {
		return nil, errors.New("empty claude bin")
	}
	argv := []string{claudeBin}
	argv = append(argv, prependFlags...)
	argv = append(argv, "--dangerously-skip-permissions")
	if model != "" {
		argv = append(argv, "--model", model)
	}
	return argv, nil
}

// assembleInteractivePromptLine returns the PAT-25 slash command the
// runner types into claude's REPL via tmux:
//
//	"/<agent> --autonomous -- <skill> --autonomous {args} {prompt}"
//	"/<skill> --autonomous --no-commit {args} {prompt}"   (no agent)
//
// The leading `/` makes claude's CLI parse it as a slash command,
// load the matching skill, and execute it under the named agent.
func assembleInteractivePromptLine(effAgent string, step Step, prompt string) string {
	var promptParts []string
	if effAgent != "" {
		promptParts = []string{"/" + effAgent, flagAutonomous, "--", step.Skill, flagAutonomous}
	} else {
		promptParts = []string{"/" + step.Skill, flagAutonomous, "--no-commit"}
	}
	if step.Args != "" {
		promptParts = append(promptParts, strings.Fields(step.Args)...)
	}
	if step.PromptFlag != "" && prompt != "" {
		promptParts = append(promptParts, step.PromptFlag, prompt)
	}
	return strings.Join(promptParts, " ")
}

// waitStepDone blocks until the current step has finished responding,
// either via the caller-supplied WaitStepDone callback (production
// path, bridge Stop hook) or via a best-effort timeout fallback (used
// by smoke tests that wire no bridge).
func waitStepDone(ctx context.Context, opts RunOptions, stage string, stepIdx int) error { //nolint:gocritic // RunOptions matches the runner-wide convention
	if opts.WaitStepDone != nil {
		return opts.WaitStepDone(ctx, stage, stepIdx)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(opts.InteractiveStepGrace):
		return nil
	}
}
