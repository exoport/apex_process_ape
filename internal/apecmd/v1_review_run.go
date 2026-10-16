package apecmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/repl"
	"github.com/spf13/cobra"
)

// The review session (BRIEF D18): `ape change review <id> --run` starts
// apex-review in its own Claude Code session — its own process, empty
// context, its own hooks — rather than as an Agent-tool sub-agent of the
// session that built the change.
//
// It is the one sanctioned exception to the nesting rule (nesting.go). The
// rule exists because an outer run's Stop hook, idle anchor and cost record
// wrapped work they could not see. This session is detached — a separate
// `ape task` under the --detach supervisor, with its own manifest — and
// the caller only polls. There is no variable that switches the rule off:
// only this verb starts such a session, its skill is fixed to apex-review,
// and a review session (APE_REVIEW set) cannot start another (depth 1).

// reviewSkill is the only skill a review session runs.
const reviewSkill = apexv1.ReviewSkill

// envReviewLaunch carries the review session's launch decision (JSON) to
// the `ape task` it starts, which records it in its manifest.
const envReviewLaunch = "APE_REVIEW_LAUNCH"

// defaultReviewWait keeps one call under the Bash tool's 10-minute cap.
const defaultReviewWait = 9 * time.Minute

// reviewEnv is the review session's environment: the caller's, without the
// marker of the session it came from (or ape task would refuse to nest),
// with the review marker that confines its edits.
func reviewEnv(id string, launch apexv1.ReviewLaunch) []string {
	parent := os.Getenv(repl.EnvApeSession)
	out := make([]string, 0, len(os.Environ())+2)
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		if k == repl.EnvApeSession || k == apexv1.EnvReview || k == envReviewParent || k == envDetachedID ||
			k == envReviewLaunch || k == repl.EnvSessionModel || k == repl.EnvSessionEffort ||
			strings.HasPrefix(k, "APE_BRIDGE") {
			continue
		}
		out = append(out, e)
	}
	out = append(out, apexv1.EnvReview+"="+id)
	if data, err := json.Marshal(launch); err == nil {
		out = append(out, envReviewLaunch+"="+string(data))
	}
	if parent != "" {
		out = append(out, envReviewParent+"="+parent)
	}
	return out
}

// envReviewParent carries the requesting session's APE_SESSION into the
// review session, for the record.
const envReviewParent = "APE_REVIEW_PARENT"

// reviewRunResult is what `ape change review --run` reports.
//
//nolint:tagliatelle // the v1 --json payloads are snake_case by contract, like state.json
type reviewRunResult struct {
	ID         string                `json:"id"`
	Running    bool                  `json:"running"`
	DetachedID string                `json:"detached_id"`
	Log        string                `json:"log,omitempty"`
	TaskExit   *int                  `json:"task_exit,omitempty"`
	Launch     apexv1.ReviewLaunch   `json:"launch"`
	Outcome    *apexv1.ReviewOutcome `json:"outcome,omitempty"`
}

func runReviewSession(cmd *cobra.Command, f *v1Flags, p *apexv1.Project, id, model, effortFlag string, wait time.Duration) error {
	ctx := cmd.Context()
	if v := os.Getenv(apexv1.EnvReview); v != "" {
		return v1Fail(cmd, f, &apexv1.UsageError{Code: "review.nested", Msg: "this is a review session (" + apexv1.EnvReview + "=" + v +
			"): a review session cannot start another review"}, nil)
	}
	run := p.LoadReviewRun(id)
	var h *detachedHandle
	if run != nil {
		h, _ = readHandle(handlePath(p.Root, run.DetachedID))
	}
	if run == nil || h == nil {
		var err error
		if run, h, err = startReviewSession(ctx, p, id, model, effortFlag); err != nil {
			return v1Fail(cmd, f, err, nil)
		}
	}
	res := &reviewRunResult{ID: id, DetachedID: run.DetachedID, Log: h.Log, Launch: run.Launch}

	h, ended, lost := awaitHandle(ctx, handlePath(p.Root, run.DetachedID), wait)
	if !ended && !lost {
		res.Running = true
		if f.json {
			_ = v1Emit(cmd.OutOrStdout(), f, res, nil)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "review session %s for %s still running — run `ape change review %s --run` again to keep waiting\n",
				run.DetachedID, id, id)
		}
		return reportedErr(exitCodeStillRunning, fmt.Errorf("review session %s still running", run.DetachedID))
	}
	if h != nil {
		res.TaskExit = h.ExitCode
	}
	outcome, err := p.SettleReviewRun(ctx, run)
	if err != nil {
		return v1Fail(cmd, f, err, res)
	}
	p.ClearReviewRun(id)
	res.Outcome = outcome
	switch {
	case outcome.Voided:
		return v1Fail(cmd, f, &apexv1.FailError{Code: "review.voided", Msg: "review of " + id + " voided: the session changed more than its review.md (" +
			strings.Join(outcome.Violations, ", ") + ") — the change is " + outcome.Status + "; review again"}, res)
	case outcome.Verdict == "":
		why := "the session was lost"
		if res.TaskExit != nil {
			why = fmt.Sprintf("ape task exited %d", *res.TaskExit)
		}
		return v1Fail(cmd, f, &apexv1.FailError{Code: "review.no-verdict", Msg: "the review session for " + id + " ended without a verdict (" + why + "; log " + h.Log +
			") — run `ape change review " + id + " --run` again"}, res)
	case outcome.Verdict == "block":
		return v1Fail(cmd, f, &apexv1.FailError{Code: "review.block", Msg: "review of " + id + ": block — findings in " + outcome.Findings +
			"; the change is back in-progress"}, res)
	}
	return v1Emit(cmd.OutOrStdout(), f, res, func(w io.Writer) {
		fmt.Fprintf(w, "review of %s: pass (findings: %s) — status %s\n", id, outcome.Findings, outcome.Status)
	})
}

// startReviewSession starts the detached review session for a verified
// change, on the model and effort DecideReviewLaunch picks (D22), and
// records it.
func startReviewSession(ctx context.Context, p *apexv1.Project, id, model, effortFlag string) (*apexv1.ReviewRun, *detachedHandle, error) {
	if _, err := p.PrepareReviewRun(ctx, id); err != nil {
		return nil, nil, err
	}
	cfg, _ := apexv1.LoadConfig(p.Root)
	launch := apexv1.DecideLaunch(model, effortFlag, cfg.Models[reviewSkill], cfg.Effort,
		os.Getenv(repl.EnvSessionModel), os.Getenv(repl.EnvSessionEffort))
	argv := []string{"task", reviewSkill, "--args", id, "--output-format", "json", "--quiet", "--cwd", p.Root}
	if launch.Model != "" {
		argv = append(argv, "--model", launch.Model)
	}
	if launch.Effort != "" {
		argv = append(argv, "--effort", launch.Effort)
	}
	run := &apexv1.ReviewRun{
		Change: id, StartedAt: apexv1.Stamp(), HeadSHA: apexv1.HeadSHA(ctx, p.Root),
		Parent: os.Getenv(repl.EnvApeSession), Launch: launch,
	}
	h, err := launchDetached(p.Root, p.Root, argv, reviewEnv(id, launch))
	if err != nil {
		return nil, nil, &apexv1.FailError{Code: "review.start-failed", Msg: "could not start the review session: " + err.Error()}
	}
	run.DetachedID = h.ID
	if err := p.SaveReviewRun(run); err != nil {
		return nil, nil, err
	}
	return run, h, nil
}

// awaitHandle waits up to wait for a detached run to end. lost reports a
// supervisor gone without recording an exit.
func awaitHandle(ctx context.Context, path string, wait time.Duration) (h *detachedHandle, ended, lost bool) {
	deadline := time.Now().Add(wait)
	for {
		h, err := readHandle(path)
		if err != nil {
			return nil, false, true
		}
		if h.ExitCode != nil {
			return h, true, false
		}
		if h.SupervisorPID != 0 && !supervisorIsOurs(ctx, h) {
			if h2, err := readHandle(path); err == nil && h2.ExitCode != nil {
				return h2, true, false
			}
			return h, false, true
		}
		if wait > 0 && time.Now().After(deadline) {
			return h, false, false
		}
		select {
		case <-ctx.Done():
			return h, false, false
		case <-time.After(500 * time.Millisecond):
		}
	}
}
