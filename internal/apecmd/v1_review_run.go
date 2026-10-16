package apecmd

import (
	"context"
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
const reviewSkill = "apex-review"

// defaultReviewWait keeps one call under the Bash tool's 10-minute cap.
const defaultReviewWait = 9 * time.Minute

// reviewEnv is the review session's environment: the caller's, without the
// marker of the session it came from (or ape task would refuse to nest),
// with the review marker that confines its edits.
func reviewEnv(id string) []string {
	parent := os.Getenv(repl.EnvApeSession)
	out := make([]string, 0, len(os.Environ())+2)
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		if k == repl.EnvApeSession || k == apexv1.EnvReview || k == envReviewParent || k == envDetachedID ||
			strings.HasPrefix(k, "APE_BRIDGE") {
			continue
		}
		out = append(out, e)
	}
	out = append(out, apexv1.EnvReview+"="+id)
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
	Outcome    *apexv1.ReviewOutcome `json:"outcome,omitempty"`
}

func runReviewSession(cmd *cobra.Command, f *v1Flags, p *apexv1.Project, id, model string, wait time.Duration) error {
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
		if _, err := p.PrepareReviewRun(ctx, id); err != nil {
			return v1Fail(cmd, f, err, nil)
		}
		argv := []string{"task", reviewSkill, "--args", id, "--output-format", "json", "--quiet", "--cwd", p.Root}
		if model != "" {
			argv = append(argv, "--model", model)
		}
		run = &apexv1.ReviewRun{
			Change: id, StartedAt: apexv1.Stamp(), HeadSHA: apexv1.HeadSHA(ctx, p.Root),
			Parent: os.Getenv(repl.EnvApeSession), Model: model,
		}
		var err error
		h, err = launchDetached(p.Root, p.Root, argv, reviewEnv(id))
		if err != nil {
			return v1Fail(cmd, f, &apexv1.FailError{Code: "review.start-failed", Msg: "could not start the review session: " + err.Error()}, nil)
		}
		run.DetachedID = h.ID
		if err := p.SaveReviewRun(run); err != nil {
			return v1Fail(cmd, f, err, nil)
		}
	}
	res := &reviewRunResult{ID: id, DetachedID: run.DetachedID, Log: h.Log}

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
