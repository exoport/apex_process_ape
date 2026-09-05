package migration

import (
	"context"
	"fmt"
	"io"
)

// Outcome is what happened to one entry during a run.
type Outcome struct {
	ID      string `json:"id"                yaml:"id"`
	Command string `json:"command,omitempty" yaml:"command,omitempty"`
	// Ran is false for every entry the runner declined to execute — a
	// judged one, an already-applied one, a cannot-tell one, and every
	// entry after a failure.
	Ran      bool   `json:"ran"               yaml:"ran"`
	ExitCode int    `json:"exit_code"         yaml:"exit_code"`
	Error    string `json:"error,omitempty"   yaml:"error,omitempty"`
	Skipped  string `json:"skipped,omitempty" yaml:"skipped,omitempty"`
	// AppliedAt is the stamp recorded in the ledger, on success only.
	AppliedAt string `json:"applied_at,omitempty" yaml:"applied_at,omitempty"`
}

// ApplyResult is one run of the list.
type ApplyResult struct {
	Outcomes []Outcome `json:"outcomes" yaml:"outcomes"`
	// Applied is the ledger rows to append, in the order they succeeded.
	Applied []Applied `json:"applied" yaml:"applied"`
	// Failed names the entry that stopped the run, if one did.
	Failed string `json:"failed,omitempty" yaml:"failed,omitempty"`
	// Judged names the judged entries still owed, with the skill each
	// routes to. The runner never executes these under any flag.
	Judged []Outcome `json:"judged,omitempty" yaml:"judged,omitempty"`
}

// Apply runs every runnable entry in plan order.
//
// Three properties the framework made binding, and where each lives:
//
//   - **Idempotent.** The ledger is what makes a second run a no-op: an
//     entry already recorded is not re-run at all, whatever its command
//     does. The command's own idempotency is the framework's to guarantee
//     and is not relied on here.
//   - **Resumable.** Ledger rows are returned in success order and the
//     caller persists them, so a run that dies half-way leaves the
//     completed prefix recorded and the next run starts after it.
//   - **Runs between dispatches.** That is the caller's placement —
//     `ape framework update`, outside the build loop — not this
//     function's to enforce.
//
// A failure STOPS the sequence. `after:` ordering means a later entry may
// depend on an earlier one, so continuing past a failure would run an
// entry whose precondition is known not to hold. The remaining entries
// are reported as not attempted rather than silently dropped.
//
// stamp issues the `applied_at` value; it is called once per success so
// the ledger's ordering matches its stamps.
func Apply(ctx context.Context, w io.Writer, projectRoot string, plan *Plan,
	runner Runner, stamp func() string,
) *ApplyResult {
	res := &ApplyResult{}
	stopped := false

	for i := range plan.Rows {
		r := &plan.Rows[i]

		if r.State == StatePending && r.IsJudged() {
			// Listed, never executed, under any flag. The skill is named
			// so the operator or the orchestrator can dispatch it.
			res.Judged = append(res.Judged, Outcome{
				ID:      r.Label(),
				Skipped: judgedReason(r),
			})
			continue
		}
		if !r.Runnable {
			continue
		}
		if stopped {
			res.Outcomes = append(res.Outcomes, Outcome{
				ID:      r.Label(),
				Command: r.Command,
				Skipped: "not attempted — an earlier migration failed and later entries may depend on it",
			})
			continue
		}

		fmt.Fprintf(w, "migration %s: running %s\n", r.Label(), r.Command)
		code, err := runner.Run(ctx, projectRoot, r.Command)
		out := Outcome{ID: r.Label(), Command: r.Command, Ran: true, ExitCode: code}
		switch {
		case err != nil:
			out.Ran, out.Error = false, err.Error()
			res.Failed, stopped = r.Label(), true
		case code != 0:
			out.Error = fmt.Sprintf("exit %d", code)
			res.Failed, stopped = r.Label(), true
		default:
			out.AppliedAt = stamp()
			res.Applied = append(res.Applied, Applied{
				ID: r.ID, Version: r.Version, AppliedAt: out.AppliedAt,
			})
		}
		res.Outcomes = append(res.Outcomes, out)
	}
	return res
}

func judgedReason(r *Row) string {
	if r.Skill != "" {
		return "judged — dispatch " + r.Skill
	}
	return "judged — never executed, and the entry names no skill to dispatch"
}
