package apecmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/repl"
	"github.com/spf13/cobra"
)

// toolRunResult is what `ape change tool` reports.
//
//nolint:tagliatelle // the v1 --json payloads are snake_case by contract, like state.json
type toolRunResult struct {
	ID         string        `json:"id"`
	Tool       string        `json:"tool"`
	Slot       string        `json:"slot"`
	Skill      string        `json:"skill"`
	Task       string        `json:"task,omitempty"`
	Args       string        `json:"args"`
	Running    bool          `json:"running"`
	DetachedID string        `json:"detached_id"`
	Log        string        `json:"log,omitempty"`
	Stdout     string        `json:"stdout,omitempty"`
	TaskExit   *int          `json:"task_exit,omitempty"`
	Launch     apexv1.Launch `json:"launch"`
	// Result and ResultTruncated are the session's last reply (D74 Q21),
	// filled where `ape run wait --json` fills them (rc3-b).
	Result          string `json:"result,omitempty"`
	ResultTruncated bool   `json:"result_truncated,omitempty"`
}

func newChangeToolCmd() *cobra.Command {
	var f v1Flags
	var name, task string
	var wait time.Duration
	cmd := v1ChangeLeaf(
		"tool <id> <name> [--task T<n>] [--wait 9m]",
		"Run a tools.design or tools.review entry for a change, in its own detached session (D74, D77)",
		`Run the skill of a tools: entry in _apex/config.yaml for change <id>, in its own Claude
Code session: ape starts `+"`ape task <skill> --args \"<args filled>\"`"+` detached, as
`+"`ape change review --run`"+` does, and waits for it.

    tools:
      design:
        - {name: ui-mockup, skill: frontend-design:mockup, when: "…",
           args: "{change} --out {assets}", model: sonnet, effort: high}
      review:
        - {name: security, skill: security-lens, when: "…"}

  - <name> is a tools.design or tools.review entry (else tool.unknown, action replan).
    A design tool runs for the task whose line names it (`+"`- T<n> <what> — tool: <name> —\ncheck: …`"+`), given with --task; a review tool, for a change whose review_tools:
    name it, before the review
  - args (D77): the entry's args: template, else "--change {id}" (plus " --task {task}"
    with --task). Placeholders: {id} the change id, {task} the task, {assets} the
    change's assets folder (development/changes/<id>/assets), {change} its change.md,
    both project-relative; a template using {task} needs --task (usage.task)
  - model and effort: the entry's model:/effort:, else models.<skill>, else the asking
    session's, else _apex/config.yaml effort:, else claude's
  - the session writes what its skill writes (a mock-up, a report under assets/): no
    read-only rule and no verdict; its exit is the result (non-zero or lost:
    tool.failed, action retry)
  - --wait (default 9m) bounds one call; exit 75 means still running: call it again —
    it attaches to the running session for that change and tool
  - JSON: {id, tool, slot, skill, task, args, running, detached_id, log, stdout,
    task_exit, launch}; stdout holds the session's `+"`ape task --output-format json`"+`
    envelope

Exit codes: 0 the session ended 0 · 1 tool.unknown, tool.args, tool.failed, a change not
found · 2 usage (no such task, {task} without --task) · 75 still running, call again.`,
		"  ape change tool add-tenant-delete ui-mockup --task T3 --wait 9m --json\n  ape change tool add-tenant-delete security --json",
		&f,
		func(cmd *cobra.Command, p *apexv1.Project, id string) error {
			return runToolSession(cmd, &f, p, id, name, task, wait)
		})
	cmd.Args = cobra.ExactArgs(2)
	cmd.PreRunE = func(_ *cobra.Command, args []string) error {
		name = args[1]
		return nil
	}
	cmd.Flags().StringVar(&task, "task", "", "the task the tool runs for, e.g. T3 (a design tool's)")
	cmd.Flags().DurationVar(&wait, "wait", defaultReviewWait, "how long this call waits for the session (exit 75 when it is still running)")
	return cmd
}

func runToolSession(cmd *cobra.Command, f *v1Flags, p *apexv1.Project, id, name, task string, wait time.Duration) error {
	ctx := cmd.Context()
	run := p.LoadToolRun(id, name)
	var h *detachedHandle
	if run != nil {
		h, _ = readHandle(handlePath(p.Root, run.DetachedID))
	}
	if run == nil || h == nil {
		var err error
		if run, h, err = startToolSession(ctx, p, id, name, task); err != nil {
			return v1Fail(cmd, f, err, nil)
		}
	}
	res := &toolRunResult{
		ID: id, Tool: run.Name, Slot: run.Slot, Skill: run.Skill, Task: run.Task, Args: run.Args,
		DetachedID: run.DetachedID, Log: h.Log, Stdout: h.Stdout, Launch: run.Launch,
	}
	h, ended, lost := awaitHandle(ctx, handlePath(p.Root, run.DetachedID), wait)
	if !ended && !lost {
		res.Running = true
		if f.json {
			_ = v1Emit(cmd.OutOrStdout(), f, res, nil)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "tool %s (%s) for %s still running as %s — run `ape change tool %s %s` again to keep waiting\n",
				name, run.Skill, id, run.DetachedID, id, name)
		}
		return reportedErr(exitCodeStillRunning, fmt.Errorf("tool session %s still running", run.DetachedID))
	}
	p.ClearToolRun(id, name)
	if h != nil {
		res.TaskExit = h.ExitCode
	}
	if lost || res.TaskExit == nil || *res.TaskExit != 0 {
		why := "the session was lost"
		if res.TaskExit != nil {
			why = fmt.Sprintf("ape task exited %d", *res.TaskExit)
		}
		return v1Fail(cmd, f, &apexv1.FailError{Code: "tool.failed", Msg: "tool " + name + " (" + run.Skill + ") for " + id + " did not finish: " + why +
			" (log " + res.Log + ") — run `ape change tool " + id + " " + name + "` again"}, res)
	}
	return v1Emit(cmd.OutOrStdout(), f, res, func(w io.Writer) {
		fmt.Fprintf(w, "tool %s (%s) for %s: done (run %s)\n", name, run.Skill, id, run.DetachedID)
	})
}

// startToolSession starts the detached session of tool name for change id
// and records it.
func startToolSession(ctx context.Context, p *apexv1.Project, id, name, task string) (*apexv1.ToolRun, *detachedHandle, error) {
	plan, err := p.PrepareTool(ctx, id, name, task)
	if err != nil {
		return nil, nil, err
	}
	cfg, _ := apexv1.LoadConfig(p.Root)
	launch := apexv1.DecideLaunch("", "", plan.Model, cfg.Effort, os.Getenv(repl.EnvSessionModel), os.Getenv(repl.EnvSessionEffort))
	// The nesting rule (D62): this starts the tool's skill.
	session, err := gateSession("ape change tool", plan.Skill, p.Root)
	if err != nil {
		return nil, nil, err
	}
	detachedID := newDetachedID()
	if err := session.acquire(p.Root, detachedID); err != nil {
		return nil, nil, err
	}
	run := &apexv1.ToolRun{
		Change: id, Name: name, Slot: plan.Slot, Skill: plan.Skill, Task: plan.Task, Args: plan.Args,
		StartedAt: apexv1.Stamp(), Parent: os.Getenv(repl.EnvApeSession), Launch: launch,
	}
	h, err := launchDetached(p.Root, p.Root, childTaskArgv(p.Root, plan.Skill, plan.Args, launch), childSessionEnv(), detachMeta{id: detachedID, launch: session})
	if err != nil {
		session.release()
		return nil, nil, &apexv1.FailError{Code: "tool.start-failed", Msg: "could not start the session of tool " + name + ": " + err.Error()}
	}
	run.DetachedID = h.ID
	if err := p.SaveToolRun(run); err != nil {
		return nil, nil, err
	}
	return run, h, nil
}
