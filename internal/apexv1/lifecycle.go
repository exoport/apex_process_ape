package apexv1

import (
	"context"
	"errors"
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

// runTasks runs every command task once, logging under _apex/state/logs/<id>/.
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
	ID      string `json:"id"`
	Status  string `json:"status"`
	BaseSHA string `json:"base_sha"`
	// BaseBranch is the branch change/<id> was created from; Branch the
	// branch now checked out — change/<id> (D38).
	BaseBranch string        `json:"base_branch,omitempty"`
	Branch     string        `json:"branch,omitempty"`
	Baseline   []CheckResult `json:"baseline"`
	// Resumed: start only checked out the branch of a started change that
	// needed no re-baselining.
	Resumed bool `json:"resumed,omitempty"`
	// Regression is the regression command's baseline run, when this
	// start took one (a first start, or the command changed).
	Regression *CheckResult `json:"regression,omitempty"`
	// CatchUp: start found the base branch moved ahead of the change and
	// caught it up — rebased, re-baselined at the base tip (D40).
	CatchUp *CatchUp `json:"catch_up,omitempty"`
}

// Start moves a draft (or blocked) change to in-progress: check green,
// milestone approved, no requirement conflict with another active change;
// then creates and checks out change/<id> from the current branch (D38),
// records base_branch, base_sha and the baseline, and commits the state
// there. On a change whose branch exists it checks that branch out first;
// a started change that needs no re-baselining is then only resumed.
func (p *Project) Start(ctx context.Context, id string, opt RunOptions) (*StartResult, error) {
	if !p.isWorkTree(ctx) {
		return p.start(ctx, id, opt, nil)
	}
	branch := ChangeBranch(id)
	cur := CurrentBranch(ctx, p.Root)
	switch {
	case cur == branch:
		return p.reenter(ctx, id, opt, false)
	case ChangeOfBranch(cur) != "":
		return nil, p.otherChangeBranch(cur, "start "+id)
	case cur == "":
		return nil, &FailError{Code: "branch.detached", Msg: "HEAD is detached — start creates " + branch + " from a branch and records that branch as the change's base: `git switch <branch>` first"}
	case p.branchExists(ctx, branch):
		if err := p.requireCleanTree(ctx, "start"); err != nil {
			return nil, err
		}
		if err := p.switchBranch(ctx, branch); err != nil {
			return nil, err
		}
		return p.reenter(ctx, id, opt, true)
	}
	entered := false
	enter := func(st *State) error {
		if err := p.requireCleanTree(ctx, "start"); err != nil {
			return err
		}
		if _, err := git(ctx, p.Root, "switch", "-q", "-c", branch); err != nil {
			return &FailError{Code: CodeBranchFailed, Msg: "cannot create " + branch + ": " + err.Error() + " — fix git and start again"}
		}
		entered = true
		st.BaseBranch = cur
		return nil
	}
	res, err := p.start(ctx, id, opt, enter)
	if err != nil && entered {
		// A refused start writes nothing: back to where it began.
		_, _ = git(ctx, p.Root, "reset", "-q", "--hard")
		_, _ = git(ctx, p.Root, "switch", "-q", cur)
		_, _ = git(ctx, p.Root, "branch", "-q", "-D", branch)
	}
	return res, err
}

// reenter is start on a change whose branch is checked out: first the
// catch-up with a base branch that moved (D40), then start's own work — a
// re-baseline when the plan, the spec it modifies or the regression
// command changed, else only a resume. A start run on the change's branch
// that had nothing to do (no catch-up, nothing to re-baseline) is refused
// (change.nothing-to-rebaseline); switched says start checked the branch
// out, which is a resume in itself.
func (p *Project) reenter(ctx context.Context, id string, opt RunOptions, switched bool) (*StartResult, error) {
	ci, err := p.requireChange(ctx, id)
	if err != nil {
		return nil, err
	}
	_, cu, err := p.catchUp(ctx, ci, "start", opt, false)
	if err != nil {
		return nil, err
	}
	res, err := p.start(ctx, id, opt, nil)
	if fe, ok := errors.AsType[*FailError](err); ok && fe.Code == CodeNothingToRebaseline && (switched || cu != nil) {
		st, lerr := LoadState(p.StateFile(id))
		if lerr != nil {
			return nil, err
		}
		return &StartResult{
			ID: id, Status: st.Status, BaseSHA: st.BaseSHA, BaseBranch: st.BaseBranch, Branch: ChangeBranch(id),
			Resumed: true, Baseline: []CheckResult{}, CatchUp: cu,
		}, nil
	}
	if res != nil {
		res.CatchUp = cu
	}
	return res, err
}

// otherChangeBranch refuses a command run on another change's branch.
func (p *Project) otherChangeBranch(cur, what string) error {
	base := "its base branch"
	if st, err := LoadState(p.StateFile(ChangeOfBranch(cur))); err == nil && st.BaseBranch != "" {
		base = st.BaseBranch
	}
	return &FailError{Code: CodeWrongBranch, Msg: "HEAD is on " + cur + ", another change's branch — `git switch " + base + "`, then " + what}
}

// CodeNothingToRebaseline: start on a started change that needs no
// re-baselining.
const CodeNothingToRebaseline = "change.nothing-to-rebaseline"

// start is Start in the checked-out tree. enter, when set, runs once every
// check has passed and before anything is written: it creates the change's
// branch.
func (p *Project) start(ctx context.Context, id string, opt RunOptions, enter func(*State) error) (*StartResult, error) {
	ci, err := p.requireChange(ctx, id)
	if err != nil {
		return nil, err
	}
	st := ci.State
	digest := ci.Doc.PlanDigest()
	var replan, rebaseline bool
	switch st.Status {
	case StatusDraft, StatusBlocked:
		replan = st.PlanDigest != "" && st.PlanDigest != digest
	case StatusInProgress, StatusVerified, StatusReviewed:
		// A started change starts again only to re-baseline: a changed
		// plan, a spec its MODIFIED/REMOVED blocks were based on that
		// another change has since changed (item 9), or a regression
		// command with no baseline for it (backlog 32).
		planChanged := st.PlanDigest != "" && st.PlanDigest != digest
		specChanged := len(p.fingerprintConflicts(st.BaseFingerprints, ci.Doc)) > 0
		if !planChanged && !specChanged && !p.regressionStale(st) {
			return nil, &FailError{Code: CodeNothingToRebaseline, Msg: "change " + id + " is " + st.Status + " and neither its plan (change.md outside ## Why and ## Notes), the spec it modifies, nor the regression command changed — nothing to re-baseline"}
		}
		replan, rebaseline = planChanged || specChanged, true
	default:
		return nil, &FailError{Code: CodeWrongStatus, Msg: "change " + id + " is " + st.Status + "; start applies to draft or blocked changes, or re-baselines a changed plan"}
	}
	if fs := p.CheckChange(id); len(fs) > 0 {
		return nil, &FailError{Code: CodeCheckFailed, Msg: "change " + id + " does not pass `ape change check` — fix change.md first", Findings: fs}
	}
	if err := p.requireApproved(ci); err != nil {
		return nil, err
	}
	var conflicts []string
	// Blocked changes claim nothing (D6): ActiveChanges excludes them.
	for _, other := range p.ActiveChanges(ctx) {
		if other.ID == id || other.Doc == nil {
			continue
		}
		for _, c := range requirementConflicts(ci.Doc, other.Doc) {
			conflicts = append(conflicts, other.ID+": "+c)
		}
	}
	if len(conflicts) > 0 {
		return nil, &ConflictError{Code: "change.requirement-conflict", Msg: "change " + id + " claims requirements another active change claims — archive or block that one first, or re-scope", Conflicts: conflicts}
	}
	if enter != nil {
		if err := enter(st); err != nil {
			return nil, err
		}
	}

	head := HeadSHA(ctx, p.Root)
	if st.BaseSHA == "" || st.Status == StatusDraft && !replan {
		st.BaseSHA = head
		st.StartedAt = Stamp()
	}
	if replan {
		st.Replans = append(st.Replans, Replan{At: Stamp(), SHA: head, From: st.PlanDigest, To: digest})
	}
	if replan || rebaseline {
		// A verify or review of the old plan, or judged by another
		// regression command, proves nothing about this one.
		st.Verify, st.Review = nil, nil
	}
	results := p.rebaseline(ctx, ci, st, head, opt)
	regression := p.rebaselineRegression(ctx, id, st, head, opt)
	st.PlanDigest = digest
	st.BaseFingerprints = p.baseFingerprints(ci.Doc)
	st.Status = StatusInProgress
	st.BlockedReason = nil
	// A (re-)start is a new plan or a person's answer: its attempts start
	// over, or a re-planned change would block again at once (backlog 30).
	if _, err := p.updateAttempts(id, func(a *Attempts) { a.Since = 0 }); err != nil {
		return nil, err
	}
	if err := p.saveAndCommit(ctx, id, st); err != nil {
		return nil, err
	}
	return &StartResult{
		ID: id, Status: st.Status, BaseSHA: st.BaseSHA, BaseBranch: st.BaseBranch, Branch: CurrentBranch(ctx, p.Root),
		Baseline: results, Regression: regression,
	}, nil
}

// requireApproved is start's approval gate: a fix's own approval (D40),
// else the change's milestone approved.
func (p *Project) requireApproved(ci ChangeInfo) error {
	if ci.Doc.IsFix() {
		return requireFixApproved(ci)
	}
	var ms *Milestone
	if rm := p.roadmap(); rm != nil {
		ms = rm.Find(ci.Doc.Meta.Milestone)
	}
	if ms == nil || ms.Status != MilestoneApproved {
		status := "missing"
		if ms != nil {
			status = ms.StatusText
		}
		return &FailError{Code: CodeMilestoneNotApproved, Msg: fmt.Sprintf("milestone %s is %s, not approved — a person runs `ape milestone approve %s --by <who>`",
			ci.Doc.Meta.Milestone, status, ci.Doc.Meta.Milestone)}
	}
	// The Status line alone is text anyone can write: ape's record of the
	// approval is what start trusts (H2).
	return p.requireApprovalRecord(ms, false)
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
	return &FailError{Code: "change.plan-drift", Msg: "change " + ci.ID + ": change.md changed outside ## Why and ## Notes since start (plan drift) — run `ape change start " + ci.ID +
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
	// GreenAtBaseline: the change's own checks (tasks, delta scenarios)
	// that already passed at baseline — review must justify each.
	GreenAtBaseline []GreenAtBaseline `json:"green_at_baseline"`
	// StrayFiles: what the checks left in the tree and ape undid (item 19).
	StrayFiles []string `json:"stray_files"`
	// Regression judges the regression command against the change's
	// baseline (backlog 32); absent when none is configured.
	Regression *RegressionVerdict `json:"regression,omitempty"`
	// Attempts is the change's red verifies since it started, this one
	// included, AttemptsTotal every red verify of it, and AttemptsCap
	// limits.attempts (backlog 30). Kept in _apex/state/attempts.json.
	Attempts      int `json:"attempts"`
	AttemptsTotal int `json:"attempts_total"`
	AttemptsCap   int `json:"attempts_cap"`
	// CatchUp: verify found the base branch moved and caught the change
	// up first (D40).
	CatchUp *CatchUp `json:"catch_up,omitempty"`
}

// greenAtBaseline lists the change's own checks — tasks and the scenarios
// of its delta, not regression scenarios, which are meant to be green —
// whose recorded baseline exit was 0.
func (p *Project) greenAtBaseline(c *Change, st *State) []GreenAtBaseline {
	base := map[string]Baseline{}
	for _, b := range st.Baseline {
		base[b.Task] = b
	}
	out := []GreenAtBaseline{}
	for _, pc := range p.ChangeChecks(c) {
		if pc.Kind == KindScenario && !pc.Delta {
			continue
		}
		b, ok := base[pc.Ref]
		if !ok || b.Exit != 0 || (b.Cmd != "" && b.Cmd != pc.Cmd) {
			continue
		}
		out = append(out, GreenAtBaseline{
			Task: pc.Ref, Kind: pc.Kind, Cmd: pc.Cmd, SHA: b.SHA,
			AtReplan: b.SHA != "" && st.BaseSHA != "" && b.SHA != st.BaseSHA,
		})
	}
	return out
}

// Failing returns the red results: a regression command red at baseline
// too is not one.
func (v *VerifyResult) Failing() []CheckResult {
	var out []CheckResult
	for i := range v.Results {
		if v.Results[i].Red() {
			out = append(out, v.Results[i])
		}
	}
	return out
}

// Verify runs every command check at HEAD and records it. The tree must
// hold no uncommitted code: a green over uncommitted work belongs to no
// sha.
func (p *Project) Verify(ctx context.Context, id string, opt RunOptions) (*VerifyResult, error) {
	ci, err := p.requireChange(ctx, id)
	if err != nil {
		return nil, err
	}
	switch ci.State.Status {
	case StatusInProgress, StatusVerified, StatusReviewed:
	case StatusDraft:
		return nil, &FailError{Code: CodeWrongStatus, Msg: "change " + id + " is a draft — `ape change start " + id + "` first"}
	default:
		return nil, &FailError{Code: CodeWrongStatus, Msg: "change " + id + " is " + ci.State.Status + " — verify applies to in-progress, verified or reviewed changes"}
	}
	if err := p.requireOnBranch(ctx, id); err != nil {
		return nil, err
	}
	if err := checkPlanFrozen(ci); err != nil {
		return nil, err
	}
	// The base moved: ape rebases the change onto it and re-baselines
	// first (D40), then verifies the rebased code.
	ci, cu, err := p.catchUp(ctx, ci, "verify", opt, false)
	if err != nil {
		return nil, err
	}
	res, err := p.verify(ctx, ci, opt, false)
	if res != nil {
		res.CatchUp = cu
	}
	return res, err
}

// verify runs and records a verify. keepReview is archive's re-verify
// after rebasing onto a moved base: the review was judged against the
// change's own code before the rebase, so a green re-verify keeps a
// reviewed change reviewed.
func (p *Project) verify(ctx context.Context, ci ChangeInfo, opt RunOptions, keepReview bool) (*VerifyResult, error) {
	id, st := ci.ID, ci.State
	dirty, err := p.dirtyCode(ctx)
	if err != nil {
		return nil, &FailError{Code: CodeGitStatusFailed, Msg: "cannot read git status: " + err.Error()}
	}
	if len(dirty) > 0 {
		return nil, &FailError{Code: "tree.dirty", Msg: "uncommitted changes outside " + p.DevRel + "/: " + strings.Join(limit(dirty, 8), ", ") +
			" — commit them (trailer `Change: " + id + "`) and run verify again; verify records a sha"}
	}
	sha := HeadSHA(ctx, p.Root)
	checks := p.ChangeChecks(ci.Doc)
	regressionCmd := p.regressionCmd()
	if regressionCmd != "" {
		checks = append(checks, PlannedCheck{Ref: regressionRef, Kind: KindRegression, Cmd: regressionCmd})
	}
	results := p.RunChecks(ctx, p.LogDir(id), "", checks, opt)
	var regression *RegressionVerdict
	if regressionCmd != "" {
		regression = judgeRegression(&results[len(results)-1], st.Regression)
	}
	green := true
	for i := range results {
		if results[i].Red() {
			green = false
		}
	}
	pending := st.PendingHuman(ci.Doc.HumanTasks())
	if pending == nil {
		pending = []string{}
	}
	gab := p.greenAtBaseline(ci.Doc, st)
	if !green {
		return p.redAttempt(ctx, ci, &VerifyResult{ID: id, Status: st.Status, SHA: sha, Green: false, Results: results, PendingHuman: pending, GreenAtBaseline: gab, StrayFiles: StrayFiles(results), Regression: regression})
	}
	st.Verify = &Verify{SHA: sha, At: Stamp(), Results: results, PendingHuman: pending, Green: green, GreenAtBaseline: gab}
	switch {
	case st.Status == StatusReviewed && st.Review != nil && st.Review.Verdict == "pass":
		if moved, _, _ := p.codeMovedSince(ctx, st.Review.SHA); moved && !keepReview {
			st.Status = StatusVerified
		}
	default:
		st.Status = StatusVerified
	}
	if err := p.saveAndCommit(ctx, id, st); err != nil {
		return nil, err
	}
	a := p.attempts(id)
	return &VerifyResult{
		ID: id, Status: st.Status, SHA: sha, Green: green, Results: results, PendingHuman: pending, GreenAtBaseline: gab, StrayFiles: StrayFiles(results), Regression: regression,
		Attempts: a.Since, AttemptsTotal: a.Total, AttemptsCap: p.limits().AttemptsCap(),
	}, nil
}

// redAttempt records a red verify as an attempt (backlog 30) in the
// ignored _apex/state/attempts.json — it commits nothing for that (D38
// restores D5). An in-progress change's state.json is left as it was; a
// verified or reviewed one goes back to in-progress with this verify
// recorded, a status change committed like any other ("apex: <id>
// in-progress", on the change's branch). The red verify that brings the
// count to limits.attempts blocks the change instead — status blocked,
// the reason naming the count and the last failing checks, committed as
// "apex: <id> blocked" — and returns FailError change.attempts-exceeded
// beside the result.
func (p *Project) redAttempt(ctx context.Context, ci ChangeInfo, res *VerifyResult) (*VerifyResult, error) {
	id, st := ci.ID, ci.State
	a, err := p.updateAttempts(id, func(a *Attempts) { a.Since++; a.Total++ })
	if err != nil {
		return nil, err
	}
	capN := p.limits().AttemptsCap()
	res.Attempts, res.AttemptsTotal, res.AttemptsCap = a.Since, a.Total, capN
	if a.Since < capN {
		if st.Status != StatusInProgress {
			st.Verify = &Verify{SHA: res.SHA, At: Stamp(), Results: res.Results, PendingHuman: res.PendingHuman, Green: false, GreenAtBaseline: res.GreenAtBaseline}
			st.Status = StatusInProgress
			if err := p.saveAndCommit(ctx, id, st); err != nil {
				return nil, err
			}
		}
		res.Status = st.Status
		return res, nil
	}
	var failing []string
	red := res.Failing()
	for i := range red {
		failing = append(failing, fmt.Sprintf("%s (exit %d)", red[i].Task, red[i].Exit))
	}
	reason := fmt.Sprintf("attempts exceeded: %d red verifies — %s", a.Since, strings.Join(limit(failing, 5), ", "))
	if st.Status != StatusInProgress {
		st.Verify = &Verify{SHA: res.SHA, At: Stamp(), Results: res.Results, PendingHuman: res.PendingHuman, Green: false, GreenAtBaseline: res.GreenAtBaseline}
	}
	st.Status = StatusBlocked
	st.BlockedReason = &reason
	if err := p.saveAndCommit(ctx, id, st); err != nil {
		return nil, err
	}
	res.Status = st.Status
	return res, &FailError{Code: "change.attempts-exceeded", Msg: fmt.Sprintf(
		"change %s: %d red verifies since it started reach limits.attempts (%d), so it is blocked — re-plan it (change.md, on %s) and `ape change start %s`, or a person answers what blocks it",
		id, a.Since, capN, ChangeBranch(id), id)}
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
		return nil, &UsageError{Code: "usage.verdict", Msg: "--verdict must be pass or block"}
	}
	ci, err := p.requireChange(ctx, id)
	if err != nil {
		return nil, err
	}
	st := ci.State
	if st.Status != StatusVerified && st.Status != StatusReviewed {
		return nil, &FailError{Code: CodeWrongStatus, Msg: "change " + id + " is " + st.Status + " — review applies to a verified change (`ape change verify " + id + "`)"}
	}
	if err := p.requireOnBranch(ctx, id); err != nil {
		return nil, err
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
		return nil, &UsageError{Code: "usage.findings-missing", Msg: "--findings " + findings + " does not exist — write the review to changes/" + id + "/review.md first"}
	}
	if verdict == "pass" {
		if st.Verify == nil || !st.Verify.Green {
			return nil, &FailError{Code: "verify.missing", Msg: "no green verify to review — `ape change verify " + id + "`"}
		}
		if moved, files, _ := p.codeMovedSince(ctx, st.Verify.SHA); moved {
			return nil, &FailError{Code: "verify.stale", Msg: "code changed since the last verify (" + strings.Join(limit(files, 5), ", ") + ") — `ape change verify " + id + "` and review again"}
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
	return p.saveAndCommitSubject(ctx, id, st, "apex: "+id+" "+st.Status)
}

// saveAndCommitSubject is saveAndCommit with its own subject.
func (p *Project) saveAndCommitSubject(ctx context.Context, id string, st *State, subject string) error {
	if err := SaveState(p.StateFile(id), st); err != nil {
		return err
	}
	if !p.isWorkTree(ctx) {
		return nil
	}
	if _, err := p.commitPaths(ctx, subject, id, p.Rel(p.StateFile(id))); err != nil {
		return &FailError{Code: CodeCommitFailed, Msg: "state.json written but its commit failed: " + err.Error() + " — fix git (identity, lock) and re-run the command"}
	}
	return nil
}

// Accept records a person accepting a human-review task.
func (p *Project) Accept(ctx context.Context, id, task, by string) (*State, error) {
	if strings.TrimSpace(by) == "" {
		return nil, &UsageError{Code: "usage.by", Msg: "--by names the person accepting"}
	}
	ci, err := p.requireChange(ctx, id)
	if err != nil {
		return nil, err
	}
	t := ci.Doc.Task(task)
	if t == nil {
		return nil, &UsageError{Code: "usage.no-such-task", Msg: "change " + id + " has no task " + task}
	}
	if !t.Human() {
		return nil, &FailError{Code: "accept.not-human-review", Msg: "task " + task + " is proven by `" + t.Check + "`, not by a person — `ape change verify " + id + "` runs it"}
	}
	if err := p.requireOnBranch(ctx, id); err != nil {
		return nil, err
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
		return nil, &UsageError{Code: "usage.reason", Msg: "--reason is required: what blocks it, or the question only a person can answer"}
	}
	ci, err := p.requireChange(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := p.requireBranchForWrite(ctx, ci); err != nil {
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
