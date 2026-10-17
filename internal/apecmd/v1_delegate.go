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

// The human-review delegate (D75, D76): `ape change accept <id> <T>
// --delegate` starts _apex/config.yaml's human_review.delegate skill
// (apex-accept by default) in its own detached session, as `ape change
// review --run` starts apex-review, waits for it, and settles it: ape
// records a pass as the acceptance, by delegate:<skill> with the run id.
// The session records its verdict with `ape change accept <id> <T>
// --verdict …`, which only that session may run (APE_DELEGATE).
//
// `ape change tool <id> <name>` (v1_tool.go) starts a tools: entry's skill
// the same way. Both are model starts.

// envDelegateParent carries the requesting session's APE_SESSION into the
// delegate session, for the record.
const envDelegateParent = "APE_DELEGATE_PARENT"

// childSessionEnv is the environment of a session a v1 verb starts
// detached (a delegate, a tool): the caller's, without the markers of the
// session it came from — the same filter as reviewEnv, which a change to
// the nesting rule changes in both places.
func childSessionEnv() []string {
	out := make([]string, 0, len(os.Environ())+2)
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		if k == repl.EnvApeSession || k == apexv1.EnvReview || k == envReviewParent || k == envDetachedID ||
			k == envReviewLaunch || k == repl.EnvSessionModel || k == repl.EnvSessionEffort || k == apexv1.EnvInlineHooks ||
			k == apexv1.EnvDelegate || k == envDelegateParent || strings.HasPrefix(k, "APE_BRIDGE") {
			continue
		}
		out = append(out, e)
	}
	return out
}

// delegateEnv is the delegate session's environment: childSessionEnv with
// the marker that lets it record its verdict and confines its edits.
func delegateEnv(id, task string) []string {
	out := append(childSessionEnv(), apexv1.EnvDelegate+"="+id+"/"+task)
	if parent := os.Getenv(repl.EnvApeSession); parent != "" {
		out = append(out, envDelegateParent+"="+parent)
	}
	return out
}

// delegateRunResult is what `ape change accept --delegate` reports.
//
//nolint:tagliatelle // the v1 --json payloads are snake_case by contract, like state.json
type delegateRunResult struct {
	ID         string                  `json:"id"`
	Task       string                  `json:"task"`
	Skill      string                  `json:"skill"`
	Args       string                  `json:"args"`
	Running    bool                    `json:"running"`
	DetachedID string                  `json:"detached_id"`
	Log        string                  `json:"log,omitempty"`
	TaskExit   *int                    `json:"task_exit,omitempty"`
	Launch     apexv1.Launch           `json:"launch"`
	Outcome    *apexv1.DelegateOutcome `json:"outcome,omitempty"`
	// Result and ResultTruncated are the session's last reply (D74 Q21),
	// filled where `ape run wait --json` fills them (rc3-b).
	Result          string `json:"result,omitempty"`
	ResultTruncated bool   `json:"result_truncated,omitempty"`
}

func runDelegateSession(cmd *cobra.Command, f *v1Flags, p *apexv1.Project, id, task string, wait time.Duration) error {
	ctx := cmd.Context()
	if v := os.Getenv(apexv1.EnvDelegate); v != "" {
		return v1Fail(cmd, f, &apexv1.UsageError{Code: "delegate.nested", Msg: "this is a delegate session (" + apexv1.EnvDelegate + "=" + v +
			"): a delegate cannot start another delegate"}, nil)
	}
	run := p.LoadDelegateRun(id, task)
	var h *detachedHandle
	if run != nil {
		h, _ = readHandle(handlePath(p.Root, run.DetachedID))
	}
	if run == nil || h == nil {
		var err error
		if run, h, err = startDelegateSession(ctx, p, id, task); err != nil {
			return v1Fail(cmd, f, err, nil)
		}
	}
	res := &delegateRunResult{ID: id, Task: task, Skill: run.Skill, Args: run.Args, DetachedID: run.DetachedID, Log: h.Log, Launch: run.Launch}

	h, ended, lost := awaitHandle(ctx, handlePath(p.Root, run.DetachedID), wait)
	if !ended && !lost {
		res.Running = true
		if f.json {
			_ = v1Emit(cmd.OutOrStdout(), f, res, nil)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "delegate session %s (%s) for %s of %s still running — run `ape change accept %s %s --delegate` again to keep waiting\n",
				run.DetachedID, run.Skill, task, id, id, task)
		}
		return reportedErr(exitCodeStillRunning, fmt.Errorf("delegate session %s still running", run.DetachedID))
	}
	if h != nil {
		res.TaskExit = h.ExitCode
	}
	outcome, err := p.SettleDelegateRun(ctx, run)
	res.Outcome = outcome
	if err != nil {
		// The run stays recorded: --delegate again settles it again.
		return v1Fail(cmd, f, err, res)
	}
	p.ClearDelegateRun(id, task)
	switch {
	case outcome.Voided:
		return v1Fail(cmd, f, &apexv1.FailError{Code: "delegate.voided", Msg: "the delegate's verdict on " + task + " of " + id + " is voided: its session changed more than its accept-* notes (" +
			strings.Join(outcome.Violations, ", ") + ") — nothing is accepted; a person looks at it"}, res)
	case outcome.Verdict != "pass":
		why := outcome.Reason
		if why == apexv1.NoVerdictReason {
			// D78: a session that recorded no verdict failed the task.
			ended := "the session was lost"
			if res.TaskExit != nil {
				ended = fmt.Sprintf("ape task exited %d", *res.TaskExit)
			}
			why += " (" + ended + "; log " + res.Log + ")"
		}
		return v1Fail(cmd, f, &apexv1.FailError{Code: "delegate.fail", Msg: "the delegate failed " + task + " of " + id + ": " + why +
			" — the task still awaits a person (`ape change accept " + id + " --task " + task + " --by <who>`)"}, res)
	}
	return v1Emit(cmd.OutOrStdout(), f, res, func(w io.Writer) {
		fmt.Fprintf(w, "change %s: %s accepted by %s (run %s): %s\n", id, task, outcome.Accepted.By, outcome.Accepted.Run, outcome.Reason)
	})
}

// startDelegateSession starts the detached delegate session for an
// awaiting task, on the model and effort DecideLaunch picks from
// human_review's (else models.<skill>'s), and records it.
func startDelegateSession(ctx context.Context, p *apexv1.Project, id, task string) (*apexv1.DelegateRun, *detachedHandle, error) {
	plan, err := p.PrepareDelegate(ctx, id, task)
	if err != nil {
		return nil, nil, err
	}
	cfg, _ := apexv1.LoadConfig(p.Root)
	launch := apexv1.DecideLaunch("", "", plan.Model, cfg.Effort, os.Getenv(repl.EnvSessionModel), os.Getenv(repl.EnvSessionEffort))
	// D62 starter: rc3-b's depth and allow-list check for starting
	// plan.Skill as a child session is called here.
	argv := childTaskArgv(p.Root, plan.Skill, plan.Args, launch)
	run := &apexv1.DelegateRun{
		Change: id, Task: task, Skill: plan.Skill, Args: plan.Args, Folder: plan.Folder,
		StartedAt: apexv1.Stamp(), HeadSHA: apexv1.HeadSHA(ctx, p.Root), Branch: apexv1.CurrentBranch(ctx, p.Root), ChangeTip: plan.ChangeTip,
		Parent: os.Getenv(repl.EnvApeSession), Launch: launch,
	}
	h, err := launchDetached(p.Root, p.Root, argv, delegateEnv(id, task))
	if err != nil {
		return nil, nil, &apexv1.FailError{Code: "delegate.start-failed", Msg: "could not start the delegate session: " + err.Error()}
	}
	run.DetachedID = h.ID
	if err := p.SaveDelegateRun(run); err != nil {
		return nil, nil, err
	}
	return run, h, nil
}

// childTaskArgv is the `ape task` a v1 verb starts detached for skill.
func childTaskArgv(root, skill, args string, launch apexv1.Launch) []string {
	argv := []string{"task", skill, "--args", args, "--output-format", "json", "--quiet", "--cwd", root}
	if launch.Model != "" {
		argv = append(argv, "--model", launch.Model)
	}
	if launch.Effort != "" {
		argv = append(argv, "--effort", launch.Effort)
	}
	return argv
}

// recordDelegateVerdict is `ape change accept <id> <T> --verdict …`, run
// inside the delegate session.
func recordDelegateVerdict(cmd *cobra.Command, f *v1Flags, p *apexv1.Project, id, task, verdict, reason string, evidence []string) error {
	v, err := p.RecordDelegateVerdict(cmd.Context(), id, task, verdict, reason, evidence)
	if err != nil {
		return v1Fail(cmd, f, err, nil)
	}
	return v1Emit(cmd.OutOrStdout(), f, v, func(w io.Writer) {
		fmt.Fprintf(w, "change %s: delegate verdict %s on %s recorded (run %s) — ape records the acceptance when this session ends, never this session\n",
			id, v.Verdict, task, v.Run)
	})
}
