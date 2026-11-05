package apexv1

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// Take-in (v1.5 stage 1a, proposal section 5.2, OQ18; ape-handled branches,
// D702): when main moves, each open milestone takes it in — the merge of
// main into milestone/<M>, merged, never rebased — as take-in-<n>, a change
// of its milestone. ape makes the merge, runs the regression on it, and
// lands it on milestone/<M> with the fenced fast-forward push the archive
// uses. A conflict, or a red regression, is laid out on change/take-in-<n>
// for apex-apply (change.conflicted, milestone.take-in-red); its committed
// fix is what the next take-in lands.

// TakeInPrefix starts a take-in's id.
const TakeInPrefix = "take-in-"

// CodeTakeInRed: the take-in's merge does not pass the regression.
const CodeTakeInRed = "milestone.take-in-red"

func init() {
	DiagnosticCodes = append(DiagnosticCodes,
		CodeDoc{CodeTakeInRed, 1, ActionApply, "ape milestone take-in (v1.5 stage 1a, section 5.2): main merges into milestone/<M> cleanly, but the merge fails the regression command; nothing landed — the merge is on change/take-in-<n>, HEAD there: apex-apply fixes it and commits, and the next take-in lands that"},
	)
}

// TakeInResult is what `ape milestone take-in` reports.
type TakeInResult struct {
	Milestone string `json:"milestone"`
	Branch    string `json:"branch"`
	// UpToDate: main is already in milestone/<M>; nothing was made.
	UpToDate bool `json:"up_to_date"`
	// Main is the main tip taken in.
	Main string `json:"main,omitempty"`
	ID   string `json:"id,omitempty"`
	// Commit is the merge (or apex-apply's fix on top of it) that landed
	// on milestone/<M>.
	Commit     string       `json:"commit,omitempty"`
	Regression *CheckResult `json:"regression,omitempty"`
}

// nextTakeInID is take-in-<n>, n one past every Take-in: trailer on base
// (the same in every checkout of the milestone).
func (p *Project) nextTakeInID(ctx context.Context, base string) string {
	out, _ := git(ctx, p.Root, "log", "--format=%(trailers:key=Take-in,valueonly)", base)
	n := 0
	for l := range strings.FieldsSeq(out) {
		if v, err := strconv.Atoi(strings.TrimPrefix(l, TakeInPrefix)); err == nil && v > n {
			n = v
		}
	}
	return TakeInPrefix + strconv.Itoa(n+1)
}

// pendingTakeIn is the id of a take-in laid out on its change branch, ""
// for none.
func (p *Project) pendingTakeIn(ctx context.Context) string {
	// A glob: for-each-ref matches a bare prefix only at a / boundary, so
	// "refs/heads/change/take-in-" alone matched nothing (V8m found it).
	out, _ := git(ctx, p.Root, "for-each-ref", "--format=%(refname:short)", "refs/heads/"+ChangeBranchPrefix+TakeInPrefix+"*")
	if f := strings.Fields(out); len(f) > 0 {
		return strings.TrimPrefix(f[0], ChangeBranchPrefix)
	}
	return ""
}

// TakeIn takes main into milestone/<M> (fetched both, synced): up to date
// when main is already in it; else the merge — or apex-apply's committed
// fix on change/take-in-<n> — is checked by the regression in this
// checkout and landed on milestone/<M> (the lease fenced, a fast-forward
// push). A conflict or a red regression is laid out on change/take-in-<n>,
// HEAD there; nothing lands.
func (p *Project) TakeIn(ctx context.Context, ms string, opt RunOptions) (*TakeInResult, error) {
	base := p.MilestoneBase(ms)
	if base == "" {
		return nil, &UsageError{Code: "usage.not-coordinated", Msg: "take-in is for a project integrating through GitHub (coordination.github): its milestones live on milestone/<M> branches; elsewhere main is the only trunk"}
	}
	res := &TakeInResult{Milestone: ms, Branch: base}
	// Taken in on milestone/<M>, switched to on a clean tree, as the gate
	// and close are (FINDING-V15-1B-2).
	if err := p.enterMilestoneBase(ctx, ms, "take-in "+ms); err != nil {
		return res, err
	}
	remote := p.trunkRemote(ctx)
	if err := p.fetchUpstreams(ctx, base, "take-in of "+ms); err != nil {
		return res, err
	}
	p.syncCoordinatedBase(ctx, base)
	mainTip, err := git(ctx, p.Root, "rev-parse", "refs/remotes/"+remote+"/main")
	if err != nil {
		return res, &FailError{Code: CodeFetchFailed, Msg: "take-in of " + ms + " needs main's upstream on " + remote + ": " + err.Error()}
	}
	baseTip, err := git(ctx, p.Root, "rev-parse", "refs/heads/"+base)
	if err != nil {
		return res, &FailError{Code: CodeMilestoneBranchMissing, Msg: base + " is not here — `ape milestone claim " + ms + "` creates it"}
	}
	res.Main = mainTip
	if _, err := git(ctx, p.Root, "merge-base", "--is-ancestor", mainTip, baseTip); err == nil {
		res.UpToDate = true
		return res, nil
	}
	id := p.pendingTakeIn(ctx)
	if id == "" {
		id = p.nextTakeInID(ctx, base)
	}
	res.ID = id
	merge, err := p.takeInMerge(ctx, id, base, baseTip, mainTip)
	if err != nil {
		return res, err
	}
	if err := p.landTakeIn(ctx, id, base, baseTip, merge, opt, res); err != nil {
		return res, err
	}
	res.Commit = merge
	return res, nil
}

// landTakeIn checks merge with the regression on base (fast-forwarded to
// it in this checkout) and pushes it; red, base goes back and the merge
// is laid out on change/<id>; a refused push puts base back too.
func (p *Project) landTakeIn(ctx context.Context, id, base, baseTip, merge string, opt RunOptions, res *TakeInResult) error {
	if err := p.requireCleanTree(ctx, "take-in "+id); err != nil {
		return err
	}
	// V8l: the lease is fenced before the regression too, so a lease lost,
	// or NATS waited for, is met before minutes of checks, not after them.
	if FenceMilestone != nil {
		if err := FenceMilestone(ctx, p, strings.TrimPrefix(base, MilestoneBranchPrefix)); err != nil {
			return err
		}
	}
	if err := p.switchBranch(ctx, base); err != nil {
		return err
	}
	back := func() { _, _ = git(ctx, p.Root, "reset", "-q", "--hard", baseTip) }
	if _, err := git(ctx, p.Root, "merge", "--ff-only", "-q", merge); err != nil {
		return &FailError{Code: CodeBranchFailed, Msg: "moving " + base + " to take-in " + id + " (" + short(merge) + ") failed: " + err.Error()}
	}
	if cmd := p.regressionCmd(); cmd != "" {
		r := p.runRegression(ctx, cmd, filepath.Join(p.LogsDir(), id), "", opt)
		res.Regression = &r
		if r.Exit != 0 {
			back()
			if _, err := git(ctx, p.Root, "switch", "-q", "-C", ChangeBranch(id), merge); err != nil {
				return &FailError{Code: CodeBranchFailed, Msg: "cannot lay the take-in out on " + ChangeBranch(id) + ": " + err.Error()}
			}
			return &FailError{Code: CodeTakeInRed, Action: ActionApply, Msg: fmt.Sprintf(
				"main merges into %s cleanly, but take-in %s fails the regression (%s exited %d; log %s); nothing landed — it is laid out on %s: apex-apply fixes it and commits, then run the take-in again",
				base, id, cmd, r.Exit, r.Log, ChangeBranch(id))}
		}
	}
	if err := p.pushMilestone(ctx, base, merge); err != nil {
		back()
		return err
	}
	_, _ = git(ctx, p.Root, "branch", "-q", "-D", ChangeBranch(id))
	return nil
}

// takeInMerge is the take-in's commit: apex-apply's committed work on
// change/<id> when it holds main's tip and the milestone's (a resolved
// conflict, a fixed regression), else the merge of main into the milestone
// made without a checkout (git merge-tree). A conflict is laid out on
// change/<id>: the markers in the tree, HEAD on the branch.
func (p *Project) takeInMerge(ctx context.Context, id, base, baseTip, mainTip string) (string, error) {
	branch := ChangeBranch(id)
	if tip, err := git(ctx, p.Root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		if _, err := git(ctx, p.Root, "rev-parse", "--verify", "--quiet", "MERGE_HEAD"); err == nil && CurrentBranch(ctx, p.Root) == branch {
			return "", &ConflictError{
				Code: CodeConflicted, Action: ActionApply, Msg: "take-in " + id + " is still being resolved on " + branch + " — resolve the markers, `git add` and `git commit --no-edit`, then run the take-in again",
				Change: id, Branch: branch, Kind: "take-in", Files: p.unmergedPaths(ctx),
			}
		}
		_, e1 := git(ctx, p.Root, "merge-base", "--is-ancestor", mainTip, tip)
		_, e2 := git(ctx, p.Root, "merge-base", "--is-ancestor", baseTip, tip)
		if e1 == nil && e2 == nil {
			return tip, nil
		}
	}
	out, err := gitRaw(ctx, p.Root, "merge-tree", "--write-tree", "--name-only", "--no-messages", baseTip, mainTip)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	msg := "apex: " + id + " take in main into " + base + "\n\nTake-in: " + id + "\n"
	if err == nil && len(lines) > 0 && lines[0] != "" {
		return git(ctx, p.Root, "commit-tree", lines[0], "-p", baseTip, "-p", mainTip, "-m", msg)
	}
	conflicts := lines[1:]
	if err := p.requireCleanTree(ctx, "take-in "+id); err != nil {
		return "", err
	}
	if _, err := git(ctx, p.Root, "switch", "-q", "-C", branch, baseTip); err != nil {
		return "", &FailError{Code: CodeBranchFailed, Msg: "cannot create " + branch + ": " + err.Error()}
	}
	_, _ = git(ctx, p.Root, "merge", "--no-ff", "-q", "-m", msg, mainTip)
	return "", &ConflictError{
		Code: CodeConflicted, Action: ActionApply, Msg: fmt.Sprintf(
			"main does not merge cleanly into %s: take-in %s is laid out on %s with the conflict markers in %s — apex-apply resolves them, `git add` and `git commit --no-edit`, then run the take-in again",
			base, id, branch, strings.Join(limit(conflicts, 8), ", ")),
		Change: id, Branch: branch, Kind: "take-in", Files: conflicts,
	}
}
