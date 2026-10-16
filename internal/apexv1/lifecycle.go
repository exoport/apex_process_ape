package apexv1

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RunOptions tune the command-check runs.
type RunOptions struct {
	Timeout time.Duration
}

// runTasks runs every command task once, logging under .apex/logs/<id>/.
func (p *Project) runTasks(ctx context.Context, c *Change, id, logPrefix string, opt RunOptions) []CheckResult {
	tasks := c.CommandTasks()
	checks := make([]PlannedCheck, 0, len(tasks))
	for _, t := range tasks {
		checks = append(checks, PlannedCheck{Ref: t.ID, Kind: KindTask, Cmd: t.Check})
	}
	return p.RunChecks(ctx, p.LogDir(id), logPrefix, checks, opt)
}

// requirementConflicts reports the requirements two changes' deltas both
// claim: the same requirement MODIFIED or REMOVED by either, or ADDED by
// both.
func requirementConflicts(a, b *Change) []string {
	var out []string
	for i := range a.Deltas {
		da := &a.Deltas[i]
		for j := range b.Deltas {
			db := &b.Deltas[j]
			if da.key() != db.key() {
				continue
			}
			// Any pairing conflicts: two MODIFIEDs overwrite each other, a
			// REMOVED pulls the ground from under the other, and two ADDEDs
			// of one name would leave the spec with a duplicate heading.
			out = append(out, fmt.Sprintf("[%s] %q (%s here, %s there)", da.Capability, da.Name, da.Op, db.Op))
		}
	}
	return out
}

// StartResult is what `ape change start` reports.
type StartResult struct {
	ID       string        `json:"id"`
	Status   string        `json:"status"`
	BaseSHA  string        `json:"base_sha"`
	Baseline []CheckResult `json:"baseline"`
}

// Start moves a draft (or blocked) change to in-progress: check green,
// milestone approved, no requirement conflict with another active change;
// then records base_sha and the baseline.
func (p *Project) Start(ctx context.Context, id string, opt RunOptions) (*StartResult, error) {
	ci, err := p.requireChange(id)
	if err != nil {
		return nil, err
	}
	st := ci.State
	digest := ci.Doc.PlanDigest()
	var replan bool
	switch st.Status {
	case StatusDraft, StatusBlocked:
		replan = st.PlanDigest != "" && st.PlanDigest != digest
	case StatusInProgress, StatusVerified, StatusReviewed:
		// A started change starts again only to re-baseline a changed plan.
		if st.PlanDigest == "" || st.PlanDigest == digest {
			return nil, &FailError{Msg: "change " + id + " is " + st.Status + " and its ## Tasks and ## Delta are unchanged — nothing to re-baseline"}
		}
		replan = true
	default:
		return nil, &FailError{Msg: "change " + id + " is " + st.Status + "; start applies to draft or blocked changes, or re-baselines a changed plan"}
	}
	if fs := p.CheckChange(id); len(fs) > 0 {
		return nil, &FailError{Msg: "change " + id + " does not pass `ape change check` — fix change.md first", Findings: fs}
	}
	rm := p.roadmap()
	ms := rm.Find(ci.Doc.Meta.Milestone)
	if ms == nil || ms.Status != MilestoneApproved {
		status := "missing"
		if ms != nil {
			status = ms.StatusText
		}
		return nil, &FailError{Msg: fmt.Sprintf("milestone %s is %s, not approved — a person runs `ape milestone approve %s --by <who>`",
			ci.Doc.Meta.Milestone, status, ci.Doc.Meta.Milestone)}
	}
	var conflicts []string
	// Blocked changes claim nothing (D6): ActiveChanges excludes them.
	for _, other := range p.ActiveChanges() {
		if other.ID == id || other.Doc == nil {
			continue
		}
		for _, c := range requirementConflicts(ci.Doc, other.Doc) {
			conflicts = append(conflicts, other.ID+": "+c)
		}
	}
	if len(conflicts) > 0 {
		return nil, &ConflictError{Msg: "change " + id + " claims requirements another active change claims — archive or block that one first, or re-scope", Conflicts: conflicts}
	}

	head := HeadSHA(ctx, p.Root)
	if st.BaseSHA == "" || st.Status == StatusDraft && !replan {
		st.BaseSHA = head
		st.StartedAt = Stamp()
	}
	if replan {
		st.Replans = append(st.Replans, Replan{At: Stamp(), SHA: head, From: st.PlanDigest, To: digest})
		// A verify or review of the old plan proves nothing about this one.
		st.Verify, st.Review = nil, nil
	}
	results := p.rebaseline(ctx, ci, st, head, opt)
	st.PlanDigest = digest
	st.Status = StatusInProgress
	st.BlockedReason = nil
	if err := p.saveAndCommit(ctx, id, st); err != nil {
		return nil, err
	}
	return &StartResult{ID: id, Status: st.Status, BaseSHA: st.BaseSHA, Baseline: results}, nil
}

// rebaseline runs the baseline — every check verify will run: the command
// tasks and the effective spec's scenarios — on a first start, and on a
// re-start only the checks that are new or whose command changed. A check
// keeps the baseline it had before any code was written: the evidence
// green-at-baseline (item 3) is judged by, which a re-plan cannot launder.
func (p *Project) rebaseline(ctx context.Context, ci ChangeInfo, st *State, head string, opt RunOptions) []CheckResult {
	kept := map[string]Baseline{}
	for _, b := range st.Baseline {
		kept[b.Task] = b
	}
	var todo []PlannedCheck
	var baseline []Baseline
	for _, pc := range p.ChangeChecks(ci.Doc) {
		if b, ok := kept[pc.Ref]; ok && b.Cmd == pc.Cmd {
			baseline = append(baseline, b)
			continue
		}
		todo = append(todo, pc)
	}
	out := p.RunChecks(ctx, p.LogDir(ci.ID), "baseline-", todo, opt)
	for i := range out {
		baseline = append(baseline, Baseline{Task: out[i].Task, Exit: out[i].Exit, Cmd: out[i].Cmd, SHA: head})
	}
	st.Baseline = baseline
	return out
}

// checkPlanFrozen refuses a change whose ## Tasks or ## Delta changed
// since start: its baseline, verify and review describe a different plan.
func checkPlanFrozen(ci ChangeInfo) error {
	if ci.State.PlanDigest == "" || ci.State.PlanDigest == ci.Doc.PlanDigest() {
		return nil
	}
	return &FailError{Msg: "change " + ci.ID + ": ## Tasks or ## Delta changed since start (plan drift) — run `ape change start " + ci.ID +
		"` to re-baseline the new plan (recorded), or restore change.md"}
}

// VerifyResult is what `ape change verify` reports.
type VerifyResult struct {
	ID           string        `json:"id"`
	Status       string        `json:"status"`
	SHA          string        `json:"sha"`
	Green        bool          `json:"green"`
	Results      []CheckResult `json:"results"`
	PendingHuman []string      `json:"pending_human"`
}

// Failing returns the red results.
func (v *VerifyResult) Failing() []CheckResult {
	var out []CheckResult
	for i := range v.Results {
		if v.Results[i].Exit != 0 {
			out = append(out, v.Results[i])
		}
	}
	return out
}

// Verify runs every command check at HEAD and records it. The tree must
// hold no uncommitted code: a green over uncommitted work belongs to no
// sha.
func (p *Project) Verify(ctx context.Context, id string, opt RunOptions) (*VerifyResult, error) {
	ci, err := p.requireChange(id)
	if err != nil {
		return nil, err
	}
	switch ci.State.Status {
	case StatusInProgress, StatusVerified, StatusReviewed:
	case StatusDraft:
		return nil, &FailError{Msg: "change " + id + " is a draft — `ape change start " + id + "` first"}
	default:
		return nil, &FailError{Msg: "change " + id + " is " + ci.State.Status + " — verify applies to in-progress, verified or reviewed changes"}
	}
	if err := checkPlanFrozen(ci); err != nil {
		return nil, err
	}
	return p.verify(ctx, ci, opt)
}

func (p *Project) verify(ctx context.Context, ci ChangeInfo, opt RunOptions) (*VerifyResult, error) {
	id, st := ci.ID, ci.State
	dirty, err := p.dirtyCode(ctx)
	if err != nil {
		return nil, &FailError{Msg: "cannot read git status: " + err.Error()}
	}
	if len(dirty) > 0 {
		return nil, &FailError{Msg: "uncommitted changes outside " + p.DevRel + "/: " + strings.Join(limit(dirty, 8), ", ") +
			" — commit them (trailer `Change: " + id + "`) and run verify again; verify records a sha"}
	}
	sha := HeadSHA(ctx, p.Root)
	results := p.RunChecks(ctx, p.LogDir(id), "", p.ChangeChecks(ci.Doc), opt)
	green := true
	for i := range results {
		if results[i].Exit != 0 {
			green = false
		}
	}
	pending := st.PendingHuman(ci.Doc.HumanTasks())
	if pending == nil {
		pending = []string{}
	}
	if !green && st.Status == StatusInProgress {
		// A verify that stays red changes nothing and commits nothing
		// (trial decision D5): the results are in the output and the logs.
		return &VerifyResult{ID: id, Status: st.Status, SHA: sha, Green: false, Results: results, PendingHuman: pending}, nil
	}
	st.Verify = &Verify{SHA: sha, At: Stamp(), Results: results, PendingHuman: pending, Green: green}
	switch {
	case !green:
		st.Status = StatusInProgress
	case st.Status == StatusReviewed && st.Review != nil && st.Review.Verdict == "pass":
		if moved, _, _ := p.codeMovedSince(ctx, st.Review.SHA); moved {
			st.Status = StatusVerified
		}
	default:
		st.Status = StatusVerified
	}
	if err := p.saveAndCommit(ctx, id, st); err != nil {
		return nil, err
	}
	return &VerifyResult{ID: id, Status: st.Status, SHA: sha, Green: green, Results: results, PendingHuman: pending}, nil
}

func limit(xs []string, n int) []string {
	if len(xs) <= n {
		return xs
	}
	return append(append([]string(nil), xs[:n]...), fmt.Sprintf("… %d more", len(xs)-n))
}

// Review records a review verdict. pass needs a fresh green verify;
// block returns the change to in-progress.
func (p *Project) Review(ctx context.Context, id, verdict, findings string) (*State, error) {
	if verdict != "pass" && verdict != "block" {
		return nil, &UsageError{Msg: "--verdict must be pass or block"}
	}
	ci, err := p.requireChange(id)
	if err != nil {
		return nil, err
	}
	st := ci.State
	if st.Status != StatusVerified && st.Status != StatusReviewed {
		return nil, &FailError{Msg: "change " + id + " is " + st.Status + " — review applies to a verified change (`ape change verify " + id + "`)"}
	}
	abs := findings
	if !filepath.IsAbs(abs) {
		if cand := filepath.Join(p.Root, findings); fileExists(cand) {
			abs = cand
		} else if wd, err := os.Getwd(); err == nil {
			abs = filepath.Join(wd, findings)
		}
	}
	if !fileExists(abs) {
		return nil, &UsageError{Msg: "--findings " + findings + " does not exist — write the review to changes/" + id + "/review.md first"}
	}
	if verdict == "pass" {
		if st.Verify == nil || !st.Verify.Green {
			return nil, &FailError{Msg: "no green verify to review — `ape change verify " + id + "`"}
		}
		if moved, files, _ := p.codeMovedSince(ctx, st.Verify.SHA); moved {
			return nil, &FailError{Msg: "code changed since the last verify (" + strings.Join(limit(files, 5), ", ") + ") — `ape change verify " + id + "` and review again"}
		}
	}
	st.Review = &Review{Verdict: verdict, At: Stamp(), SHA: HeadSHA(ctx, p.Root), Findings: p.Rel(abs)}
	if verdict == "pass" {
		st.Status = StatusReviewed
	} else {
		st.Status = StatusInProgress
	}
	if err := p.saveAndCommit(ctx, id, st); err != nil {
		return nil, err
	}
	return st, nil
}

// saveAndCommit writes a change's state.json and commits it alone as
// `apex: <id> <status>` with trailer `Change: <id>` (trial decision D5):
// state.json is tracked, only ape stages it, and the tree stays clean for
// the task driver. Outside a git work tree it only writes.
func (p *Project) saveAndCommit(ctx context.Context, id string, st *State) error {
	if err := SaveState(p.StateFile(id), st); err != nil {
		return err
	}
	if !p.isWorkTree(ctx) {
		return nil
	}
	if _, err := p.commitPaths(ctx, "apex: "+id+" "+st.Status, id, p.Rel(p.StateFile(id))); err != nil {
		return &FailError{Msg: "state.json written but its commit failed: " + err.Error() + " — fix git (identity, lock) and re-run the command"}
	}
	return nil
}

// Accept records a person accepting a human-review task.
func (p *Project) Accept(ctx context.Context, id, task, by string) (*State, error) {
	if strings.TrimSpace(by) == "" {
		return nil, &UsageError{Msg: "--by names the person accepting"}
	}
	ci, err := p.requireChange(id)
	if err != nil {
		return nil, err
	}
	t := ci.Doc.Task(task)
	if t == nil {
		return nil, &UsageError{Msg: "change " + id + " has no task " + task}
	}
	if !t.Human() {
		return nil, &FailError{Msg: "task " + task + " is proven by `" + t.Check + "`, not by a person — `ape change verify " + id + "` runs it"}
	}
	st := ci.State
	accepted := make([]Accept, 0, len(st.Accepted)+1)
	for _, a := range st.Accepted {
		if a.Task != task {
			accepted = append(accepted, a)
		}
	}
	accepted = append(accepted, Accept{Task: task, By: by, At: Stamp()})
	st.Accepted = accepted
	if st.Verify != nil {
		st.Verify.PendingHuman = st.PendingHuman(ci.Doc.HumanTasks())
		if st.Verify.PendingHuman == nil {
			st.Verify.PendingHuman = []string{}
		}
	}
	if err := p.saveAndCommit(ctx, id, st); err != nil {
		return nil, err
	}
	return st, nil
}

// Block sets a change blocked with a reason a person can act on.
func (p *Project) Block(ctx context.Context, id, reason string) (*State, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, &UsageError{Msg: "--reason is required: what blocks it, or the question only a person can answer"}
	}
	ci, err := p.requireChange(id)
	if err != nil {
		return nil, err
	}
	st := ci.State
	st.Status = StatusBlocked
	r := strings.TrimSpace(reason)
	st.BlockedReason = &r
	if err := p.saveAndCommit(ctx, id, st); err != nil {
		return nil, err
	}
	return st, nil
}
