package migration

import (
	"context"
	"fmt"
	"io"
	"strings"
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
	// Rejudged is set when the run asked the entry's check again after an
	// earlier entry applied: the state the up-front plan gave it, the state
	// the check gives it now, and the entry whose run prompted the question
	// ("cannot-tell → pending, after v1.0.0_seq-01 applied").
	Rejudged string `json:"rejudged,omitempty" yaml:"rejudged,omitempty"`
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
// The plan's verdicts are taken before anything runs, so they describe
// the project as the run FOUND it. Once an entry has applied, that
// project is gone: the lift turns a v0 layout into v1, and an entry whose
// check could not tell on v0 (or found nothing to do) may have a plain
// answer on what the lift left (E-R7-1). So after the first success,
// every remaining derivable row the ledger has not settled is judged
// again, by its check, right before it would run — and the plan row is
// replaced by the new verdict, so BlockingUnapplied reads the same one.
// The ledger stays final: a recorded entry is never re-judged.
//
// stamp issues the `applied_at` value; it is called once per success so
// the ledger's ordering matches its stamps.
func Apply(ctx context.Context, w io.Writer, projectRoot string, plan *Plan,
	runner Runner, stamp func() string,
) *ApplyResult {
	res := &ApplyResult{}
	stopped := false
	// lastApplied names the latest entry this run applied; empty until
	// one has, and until then the up-front plan stands.
	lastApplied := ""

	for i := range plan.Rows {
		r := &plan.Rows[i]

		rejudged := ""
		if !stopped && lastApplied != "" && rejudgeable(plan, r) {
			before := r.State
			*r = resolveRow(ctx, projectRoot, &r.Entry, nil, nil, plan.checker, true)
			rejudged = before + " → " + r.State + ", after " + lastApplied + " applied"
			fmt.Fprintf(w, "migration %s: judged again (%s)\n", r.Label(), rejudged)
			if !r.Runnable {
				res.Outcomes = append(res.Outcomes, Outcome{
					ID: r.Label(), Command: r.Command, Skipped: rejudgedReason(r), Rejudged: rejudged,
				})
				continue
			}
		}

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
		out := Outcome{ID: r.Label(), Command: r.Command, Ran: true, ExitCode: code, Rejudged: rejudged}
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
			lastApplied = r.Label()
		}
		res.Outcomes = append(res.Outcomes, out)
	}
	return res
}

// rejudgeable is a row whose up-front verdict a run may revisit: derivable,
// runnable in principle, not superseded, and not settled by the ledger.
// Judged rows are listed, never run, so there is nothing to decide again
// for them; a ledger row is what makes a second run a no-op.
func rejudgeable(plan *Plan, r *Row) bool {
	return plan.checker != nil && !r.IsJudged() && !r.Unrunnable &&
		r.State != StateSuperseded && r.Source != SourceLedger
}

// rejudgedReason says why a row judged again is still not run.
func rejudgedReason(r *Row) string {
	switch r.State {
	case StateApplied:
		return "not run — judged again, its check now says it is already applied"
	case StateCannotTell:
		return "not attempted — judged again, and its check could not tell whether it is needed (" +
			r.CheckDetail + ")"
	default:
		return "not run — judged again: " + r.State
	}
}

func judgedReason(r *Row) string {
	if r.Skill != "" {
		return "judged — dispatch " + r.Skill
	}
	return "judged — never executed, and the entry names no skill to dispatch"
}

// Blocked is a blocking derivable entry a run left unapplied.
type Blocked struct {
	ID      string `json:"id"                yaml:"id"`
	Command string `json:"command,omitempty" yaml:"command,omitempty"`
	Summary string `json:"summary,omitempty" yaml:"summary,omitempty"`
	// Reason says what happened: failed, or not attempted and why.
	Reason string `json:"reason" yaml:"reason"`
}

// BlockingUnapplied lists the entries the install cannot stand without
// (D162-4): blocking, derivable, owed (pending or cannot-tell, as the
// plan last judged it — Apply writes a re-judged verdict back into the
// plan), and not applied by the run — failed, not attempted after an
// earlier failure, unrunnable, or unverifiable. res is nil when nothing
// ran. A judged blocking entry is never listed: it is a dispatch the
// person owes after the install, not a step the install itself needs.
func BlockingUnapplied(plan *Plan, res *ApplyResult) []Blocked {
	applied := map[string]bool{}
	outcome := map[string]Outcome{}
	if res != nil {
		for _, a := range res.Applied {
			applied[a.ID] = true
		}
		for _, o := range res.Outcomes {
			outcome[o.ID] = o
		}
	}
	var out []Blocked
	for i := range plan.Rows {
		r := &plan.Rows[i]
		if !r.Blocking || r.IsJudged() || applied[r.ID] ||
			(r.State != StatePending && r.State != StateCannotTell) {
			continue
		}
		b := Blocked{ID: r.Label(), Command: r.Command, Summary: r.Summary}
		o, ran := outcome[r.Label()]
		switch {
		case ran && o.Error != "":
			b.Reason = "failed (" + o.Error + ")"
		case ran && o.Skipped != "":
			b.Reason = o.Skipped
		case r.State == StateCannotTell:
			b.Reason = "not attempted — its check could not tell whether it is needed (" + r.CheckDetail + ")"
		case r.Unrunnable:
			b.Reason = "not attempted — the entry cannot be run: " + strings.Join(r.Findings, "; ")
		default:
			b.Reason = "not attempted"
		}
		out = append(out, b)
	}
	return out
}
