package apexv1

import (
	"context"
	"strings"
)

// The fix lane (trial decision D40). A fix is a change a person asked
// for outside the roadmap: `milestone: none` in its frontmatter. It is
// approved on its own — `ape change approve <id> --by <who>`, the
// approval a milestone change gets from its milestone — and otherwise
// lives like any change: its branch, baseline, checks, a review in a
// separate session, the delta merged at archive. It counts toward no
// milestone: not toward the gate's changes in flight, not toward the
// gate's regression baseline, not toward a milestone's spend or budget.
// An approved fix runs ahead of milestone work, at the orchestrator's next
// change boundary, never in parallel; `ape status` lists approved fixes
// first and points `next` at the first of them.

// MilestoneNone is a fix's milestone.
const MilestoneNone = "none"

// Lanes of a change, as `ape status` reports them.
const (
	LaneFix       = "fix"
	LaneMilestone = "milestone"
)

// CodeNotApproved: start of a fix nobody approved, or whose plan changed
// since its approval.
const CodeNotApproved = "change.not-approved"

// IsFix reports a change of the fix lane: `milestone: none`.
func (c *Change) IsFix() bool { return c != nil && c.Meta.Milestone == MilestoneNone }

// ApproveResult is what `ape change approve` reports.
type ApproveResult struct {
	ID       string    `json:"id"`
	Status   string    `json:"status"`
	Approved *Approval `json:"approved"`
	// Already: the fix was approved for this plan before; nothing was
	// written.
	Already bool   `json:"already,omitempty"`
	Commit  string `json:"commit,omitempty"`
}

// ApproveFix records a person approving a fix (D40): its approval, like a
// milestone's, is a record on the base branch — committed there as
// "apex: <id> approved", refused while HEAD is on a change branch. Only a
// fix (`milestone: none`) is approved this way; a milestone change is
// approved through its milestone. The fix must pass `ape change check` and
// not be started. Re-approving the same plan is a no-op; a plan changed
// since its approval is approved again (recorded anew).
func (p *Project) ApproveFix(ctx context.Context, id, by string) (*ApproveResult, error) {
	by = strings.TrimSpace(by)
	if by == "" {
		return nil, &UsageError{Code: "usage.by", Msg: "--by names the person approving the fix"}
	}
	if err := p.refuseOnChangeBranch(ctx, "a fix's approval"); err != nil {
		return nil, err
	}
	ci, err := p.requireChange(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ci.Doc.IsFix() {
		m := ci.Doc.Meta.Milestone
		return nil, &FailError{Code: "change.approve-milestone", Msg: "change " + id + " belongs to milestone " + m +
			", and a milestone change is approved through its milestone — a person runs `ape milestone approve " + m + " --by <who>`"}
	}
	st := ci.State
	started := ci.Branch != "" || st.BaseSHA != "" || p.isWorkTree(ctx) && p.branchExists(ctx, ChangeBranch(id))
	if (st.Status != StatusDraft && st.Status != StatusBlocked) || started {
		return nil, &FailError{Code: CodeWrongStatus, Msg: "fix " + id + " is " + st.Status + " and was started — approve applies to a fix not started yet (a draft)"}
	}
	if fs := p.CheckChange(id); len(fs) > 0 {
		return nil, &FailError{Code: CodeCheckFailed, Msg: "fix " + id + " does not pass `ape change check` — fix change.md first, then approve", Findings: fs}
	}
	digest := ci.Doc.PlanDigest()
	if a := st.Approved; a != nil && a.PlanDigest == digest {
		return &ApproveResult{ID: id, Status: st.Status, Approved: a, Already: true}, nil
	}
	st.Approved = &Approval{By: by, At: Stamp(), PlanDigest: digest}
	if err := p.saveAndCommitSubject(ctx, id, st, "apex: "+id+" approved"); err != nil {
		return nil, err
	}
	return &ApproveResult{ID: id, Status: st.Status, Approved: st.Approved, Commit: HeadSHA(ctx, p.Root)}, nil
}

// requireFixApproved is start's gate for a fix, in place of an approved
// milestone: a person's approval of the plan it now has.
func requireFixApproved(ci ChangeInfo) error {
	id, a := ci.ID, ci.State.Approved
	switch {
	case a == nil:
		return &FailError{Code: CodeNotApproved, Msg: "fix " + id + " (milestone: none) is not approved — a person runs `ape change approve " + id + " --by <who>`"}
	case a.PlanDigest != "" && a.PlanDigest != ci.Doc.PlanDigest() && ci.State.PlanDigest == "":
		return &FailError{Code: CodeNotApproved, Msg: "fix " + id + "'s plan changed since " + a.By + " approved it — a person approves it again: `ape change approve " + id + " --by <who>`"}
	}
	return nil
}

// fixIDs is every fix's id, unarchived and archived: whose runs count
// toward no milestone.
func (p *Project) fixIDs(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	for _, c := range append(p.changes(ctx), p.archivedChanges()...) {
		if c.Doc.IsFix() {
			out[c.ID] = true
		}
	}
	return out
}

// RunChangeArg is the change a run's --args names: its first word that is
// not an option ("" when none). `ape task <skill> --args <id>` is how a
// change's sessions are started.
func RunChangeArg(args string) string {
	for w := range strings.FieldsSeq(args) {
		if !strings.HasPrefix(w, "-") {
			return w
		}
	}
	return ""
}

// IsFixRun reports a run whose --args names a fix: it counts toward no
// milestone's spend or budget.
func (p *Project) IsFixRun(ctx context.Context, args string) bool {
	id := RunChangeArg(args)
	return id != "" && p.fixIDs(ctx)[id]
}
