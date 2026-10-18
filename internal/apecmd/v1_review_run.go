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
// It starts a model session, so the nesting rule applies (D62, nesting.go):
// it counts as starting apex-review. From a terminal (depth 0) it always
// may; from inside an ape session only ape's own apex-orchestrator run may,
// as its one child (session.child-running while another child runs); any
// other session — apex-apply run by a person through `ape task`, say — is
// refused with session.depth. The session is detached — a separate `ape
// task` under the --detach supervisor, with its own manifest, its parent
// run recorded — and the caller only polls. A review session (APE_REVIEW
// set) cannot start another.

// reviewSkill is the only skill a review session runs.
const reviewSkill = apexv1.ReviewSkill

// envReviewLaunch carries the review session's launch decision (JSON) to
// the `ape task` it starts, which records it in its manifest.
const envReviewLaunch = "APE_REVIEW_LAUNCH"

// defaultReviewWait keeps one call under the Bash tool's 10-minute cap:
// the long verbs' wait (v1_longrun.go), whose contract this is.
const defaultReviewWait = defaultLongWait

// reviewEnv is the review session's environment: the caller's, with the
// review marker that confines its edits. The caller's session marker and
// depth (APE_SESSION, APE_DEPTH) stay: the `ape task` it starts applies the
// nesting rule from them, as this command did, and stamps the review
// session with its own.
func reviewEnv(id string, launch apexv1.ReviewLaunch) []string {
	parent := os.Getenv(repl.EnvApeSession)
	out := make([]string, 0, len(os.Environ())+2)
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		if k == apexv1.EnvReview || k == envReviewParent || k == envDetachedID ||
			k == envReviewLaunch || k == repl.EnvSessionModel || k == repl.EnvSessionEffort || k == apexv1.EnvInlineHooks ||
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
	ID         string `json:"id"`
	Running    bool   `json:"running"`
	DetachedID string `json:"detached_id"`
	Log        string `json:"log,omitempty"`
	// Verb, Scope and Since: the fields every still-running (exit 75)
	// payload has (runningPayload).
	Verb     string                `json:"verb"`
	Scope    string                `json:"scope"`
	Since    string                `json:"since"`
	TaskExit *int                  `json:"task_exit,omitempty"`
	Launch   apexv1.ReviewLaunch   `json:"launch"`
	Outcome  *apexv1.ReviewOutcome `json:"outcome,omitempty"`
	// Verify is the verify --run ran first, for a reviewed change whose
	// review was stale (D51 Q16).
	Verify *apexv1.VerifyResult `json:"verify,omitempty"`
}

func runReviewSession(cmd *cobra.Command, f *v1Flags, p *apexv1.Project, id, model, effortFlag string, wait time.Duration) error {
	ctx := cmd.Context()
	if v := os.Getenv(apexv1.EnvReview); v != "" {
		return v1Fail(cmd, f, &apexv1.UsageError{Code: "review.nested", Msg: "this is a review session (" + apexv1.EnvReview + "=" + v +
			"): a review session cannot start another review"}, nil)
	}
	if err := validateWait(wait); err != nil {
		return v1Fail(cmd, f, err, nil)
	}
	// The nesting rule (D62): this starts apex-review.
	launch, err := gateSession("ape change review --run", reviewSkill, p.Root)
	if err != nil {
		return v1Fail(cmd, f, err, nil)
	}
	run := p.LoadReviewRun(id)
	var h *detachedHandle
	if run != nil {
		h, _ = readHandle(handlePath(p.Root, run.DetachedID))
	}
	var verified *apexv1.VerifyResult
	if run == nil || h == nil {
		if run, h, verified, err = startFreshReview(cmd, f, p, id, model, effortFlag, launch); err != nil {
			return err
		}
	}
	res := &reviewRunResult{
		ID: id, DetachedID: run.DetachedID, Log: h.Log, Launch: run.Launch, Verify: verified,
		Verb: "change review", Scope: id, Since: run.StartedAt,
	}

	h, ended, lost := awaitHandle(ctx, handlePath(p.Root, run.DetachedID), wait)
	if !ended && !lost {
		res.Running = true
		return stillRunning(cmd, f, res, fmt.Sprintf("review session %s for %s still running since %s — run `ape change review %s --run` again to keep waiting",
			run.DetachedID, id, run.StartedAt, id))
	}
	if h != nil {
		res.TaskExit = h.ExitCode
	}
	outcome, err := p.SettleReviewRun(ctx, run, childStopBackground(p, h)...)
	if err != nil {
		return v1Fail(cmd, f, err, res)
	}
	p.ClearReviewRun(id)
	res.Outcome = outcome
	switch {
	case outcome.Voided:
		return v1Fail(cmd, f, &apexv1.FailError{Code: "review.voided", Msg: "review of " + id + " voided: the session changed more than its review.md (" +
			strings.Join(outcome.Violations, ", ") + ") — the change is " + outcome.Status + "; review again"}, res)
	case outcome.Verdict == "" && len(outcome.Background) > 0:
		// D88: the session ended its turn while its own work still ran.
		return v1Fail(cmd, f, &apexv1.FailError{Code: "review.no-verdict", Msg: "the review session for " + id + " " + apexv1.LiveBackgroundReason(outcome.Background) +
			" (no verdict; " + sessionEnded(res.TaskExit) + "; log " + res.Log + ") — run `ape change review " + id + " --run` again"}, res)
	case outcome.Verdict == "":
		why := sessionEnded(res.TaskExit)
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

// startFreshReview starts the review session of a change that has none to
// wait on. A non-nil error is the command's result, already reported.
func startFreshReview(cmd *cobra.Command, f *v1Flags, p *apexv1.Project, id, model, effortFlag string,
	launch *sessionLaunch,
) (*apexv1.ReviewRun, *detachedHandle, *apexv1.VerifyResult, error) {
	ctx := cmd.Context()
	// An orchestrator's one child at a time, taken before anything
	// reads the change: a second child is refused as busy
	// (session.child-running) whatever the change's status, as a direct
	// `ape task` is. It is held under the detached run's id, which the
	// review session's `ape task` takes over.
	detachedID := newDetachedID()
	if err := launch.acquire(p.Root, detachedID); err != nil {
		return nil, nil, nil, v1Fail(cmd, f, err, nil)
	}
	// A reviewed change whose review went stale is verified again
	// first, then reviewed as a verified one (D51 Q16) — under the
	// change's run lock, so never beside a detached verify (D58 2.1).
	lock, held, err := p.TakeRunLock(ctx, "change review", id)
	if err != nil {
		launch.release()
		return nil, nil, nil, v1Fail(cmd, f, err, nil)
	}
	if held != nil {
		launch.release()
		return nil, nil, nil, v1Fail(cmd, f, apexv1.RunBusy(held), nil)
	}
	verified, err := p.ReverifyStaleReview(ctx, id, f.runOptions())
	p.ReleaseRunLock(lock)
	if err != nil {
		launch.release()
		return nil, nil, nil, failReverify(cmd, f, id, verified, err)
	}
	run, h, err := startReviewSession(ctx, p, id, model, effortFlag, launch, detachedID)
	if err != nil {
		return nil, nil, nil, v1Fail(cmd, f, err, nil)
	}
	return run, h, verified, nil
}

// failReverify reports the red verify --run ran before reviewing a stale
// review again: its results, then its error.
func failReverify(cmd *cobra.Command, f *v1Flags, id string, verified *apexv1.VerifyResult, err error) error {
	if verified == nil {
		return v1Fail(cmd, f, err, nil)
	}
	if !f.json {
		out := cmd.OutOrStdout()
		printCatchUp(out, id, verified.CatchUp)
		fmt.Fprintf(out, "change %s at %.7s: %s\n", id, verified.SHA, verified.Status)
		printChecks(out, verified.Results, true)
	}
	return v1Fail(cmd, f, err, &reviewRunResult{ID: id, Verify: verified})
}

// startReviewSession starts the detached review session for a verified
// change, on the model and effort DecideReviewLaunch picks (D22), and
// records it. session holds its locks under detachedID; they are released
// when the session is not started.
func startReviewSession(ctx context.Context, p *apexv1.Project, id, model, effortFlag string, session *sessionLaunch, detachedID string) (*apexv1.ReviewRun, *detachedHandle, error) {
	if _, err := p.PrepareReviewRun(ctx, id); err != nil {
		session.release()
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
	h, err := launchDetached(p.Root, p.Root, argv, reviewEnv(id, launch), detachMeta{id: detachedID, launch: session})
	if err != nil {
		session.release()
		return nil, nil, &apexv1.FailError{Code: "review.start-failed", Msg: "could not start the review session: " + err.Error()}
	}
	run.DetachedID = h.ID
	if err := p.SaveReviewRun(run); err != nil {
		return nil, nil, err
	}
	return run, h, nil
}

// awaitHandle waits up to wait for a detached run to end (0: it only
// looks). lost reports a supervisor gone without recording an exit.
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
		if !time.Now().Before(deadline) {
			return h, false, false
		}
		select {
		case <-ctx.Done():
			return h, false, false
		case <-time.After(500 * time.Millisecond):
		}
	}
}
