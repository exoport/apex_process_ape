package apexv1

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
)

// Loop limits a re-plan cannot reset (H19, D47 Q9). limits.attempts caps
// red verifies per start, and every re-start resets it; these cap what
// re-planning itself does:
//
//   - limits.attempts_total: a change's red verifies across its starts;
//   - limits.replans: a change's re-plans;
//
// both counted since a person last answered the change (`ape change
// answer`, which restarts them). Reaching either blocks the change with
// action person: re-planning is what did not converge, so another
// automatic re-plan is exactly what the cap is there to stop — a person
// answers, abandons, or raises the limit.
//
//   - limits.consecutive_blocks: a milestone's changes blocked in a row,
//     counted in milestones.json (consecutive_blocks, on the base branch)
//     — one more at every block of one of its changes, back to 0 when one
//     of its changes archives. At the cap, change new and change start of
//     the milestone's changes and `ape task apex-orchestrator` for it
//     refuse (milestone.blocks-exceeded, action stop: the run stops), and
//     `ape status --json` reports it on the milestone. A person resets it
//     by approving the milestone again (`ape milestone approve`), after
//     dealing with the blocked changes. Fixes (milestone: none) count
//     toward no milestone.

// Codes of the loop limits.
const (
	CodeAttemptsTotalExceeded = "change.attempts-total-exceeded"
	CodeReplansExceeded       = "change.replans-exceeded"
	CodeBlocksExceeded        = "milestone.blocks-exceeded"
)

// noteBlocked counts a change just blocked toward its milestone's
// consecutive blocks, in milestones.json on the change's base branch,
// from whichever branch HEAD is on (editOnBranch). A fix counts toward
// no milestone; outside git nothing is counted.
func (p *Project) noteBlocked(ctx context.Context, ci ChangeInfo) error {
	if ci.Doc == nil || ci.Doc.IsFix() || ci.Doc.Meta.Milestone == "" || !p.isWorkTree(ctx) {
		return nil
	}
	m := ci.Doc.Meta.Milestone
	base := ci.State.BaseBranch
	if base == "" || !p.branchExists(ctx, base) {
		b, err := p.baseBranch(ctx)
		if err != nil {
			return err
		}
		base = b
	}
	rel := p.Rel(p.MilestonesJSON())
	_, err := p.editOnBranchMsg(ctx, base, []string{rel}, func(cur map[string][]byte) (map[string][]byte, string, error) {
		recs := MilestoneRecords{}
		if cur[rel] != nil {
			if err := json.Unmarshal(cur[rel], &recs); err != nil {
				return nil, "", fmt.Errorf("%s on %s does not parse: %w", rel, base, err)
			}
		}
		if recs[m] == nil {
			recs[m] = &MilestoneRecord{}
		}
		recs[m].ConsecutiveBlocks++
		data, err := json.MarshalIndent(recs, "", "  ")
		if err != nil {
			return nil, "", err
		}
		msg := "apex: " + m + " consecutive blocks " + strconv.Itoa(recs[m].ConsecutiveBlocks) + " (" + ci.ID + ")\n\nMilestone: " + m
		return map[string][]byte{rel: append(data, '\n')}, msg, nil
	})
	return err
}

// blockAndCount blocks a change with reason — committed on its branch as
// "apex: <id> blocked" — and counts the block toward its milestone.
func (p *Project) blockAndCount(ctx context.Context, ci ChangeInfo, reason string) error {
	st := ci.State
	st.Status = StatusBlocked
	st.BlockedReason = &reason
	if err := p.saveAndCommit(ctx, ci.ID, st); err != nil {
		return err
	}
	return p.noteBlocked(ctx, ci)
}

// resetBlocks sets a milestone's consecutive blocks back to 0 in the
// working tree's milestones.json and stages it: archive does, on the
// base branch, inside its one commit. Nothing when there is nothing to
// reset.
func (p *Project) resetBlocks(ctx context.Context, c *Change) error {
	if c == nil || c.IsFix() {
		return nil
	}
	recs, err := p.LoadMilestoneRecords()
	if err != nil {
		return err
	}
	rec := recs[c.Meta.Milestone]
	if rec == nil || rec.ConsecutiveBlocks == 0 {
		return nil
	}
	rec.ConsecutiveBlocks = 0
	if err := p.saveMilestoneRecords(recs); err != nil {
		return err
	}
	_, err = git(ctx, p.Root, "add", "--", p.Rel(p.MilestonesJSON()))
	return err
}

// consecutiveBlocks is a milestone's count as the base branch records it:
// the working tree's when HEAD is not on a change branch, else the
// change's base branch's.
func (p *Project) consecutiveBlocks(ctx context.Context, m string) int {
	recs := MilestoneRecords{}
	if p.HeadChange(ctx) == "" {
		if r, err := p.LoadMilestoneRecords(); err == nil {
			recs = r
		}
	} else if base, err := p.baseBranch(ctx); err == nil {
		if data, ok := p.readOnBranch(ctx, base, p.Rel(p.MilestonesJSON())); ok {
			_ = json.Unmarshal(data, &recs)
		}
	}
	if rec := recs[m]; rec != nil {
		return rec.ConsecutiveBlocks
	}
	return 0
}

// blocksExceeded is FailError milestone.blocks-exceeded when milestone m
// has reached limits.consecutive_blocks, else nil.
func (p *Project) blocksExceeded(ctx context.Context, m string) error {
	if m == "" || m == MilestoneNone {
		return nil
	}
	n, capN := p.consecutiveBlocks(ctx, m), p.limits().ConsecutiveBlocksCap()
	if n < capN {
		return nil
	}
	return &FailError{Code: CodeBlocksExceeded, Msg: fmt.Sprintf(
		"%s has %d changes blocked in a row (limits.consecutive_blocks %d): no change of it is planned or started, and no orchestrator runs it"+
			" — the run stops; a person deals with the blocked changes (`ape status`: answer or abandon them) and approves %s again to reset the count: `ape milestone approve %s --by <who>`",
		m, n, capN, m, m)}
}

var milestoneIDRe = regexp.MustCompile(`^M\d+$`)

// OrchestratorLimits refuses an orchestrator session for a milestone
// past limits.consecutive_blocks (milestone.blocks-exceeded): the
// milestone args names (`ape task apex-orchestrator --args M2`), else
// the current one. A fix's run (args names a fix) is not refused.
func (p *Project) OrchestratorLimits(ctx context.Context, args string) error {
	if p.IsFixRun(ctx, args) {
		return nil
	}
	m := RunChangeArg(args)
	if !milestoneIDRe.MatchString(m) {
		m = ""
		if rm := p.roadmap(); rm != nil {
			if cur := rm.Current(); cur != nil {
				m = cur.ID
			}
		}
	}
	return p.blocksExceeded(ctx, m)
}
