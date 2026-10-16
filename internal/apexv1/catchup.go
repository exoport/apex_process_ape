package apexv1

import (
	"context"
	"errors"
	"strings"
)

// Resume after the base moved (trial decision D40). A change's base
// branch moves while the change is worked on — a fix lands, another change
// is archived. ape, never the model, brings the change up to date: at
// `ape change start` on a change that has its branch, at `ape change
// verify`, and at `ape change archive` — one path, catchUp. When the base
// branch holds commits change/<id> does not, ape
//
//  1. needs a clean tree (uncommitted files in the change's own folder are
//     committed on its branch as "apex: <id> notes"; anything else refuses,
//     tree.dirty);
//  2. rebases change/<id> onto the base branch — a conflict aborts the
//     rebase and blocks the change naming the files (committed on its
//     branch), ConflictError change.rebase-conflict, exit 3, nothing else
//     written;
//  3. re-runs the baseline at the NEW BASE TIP, in place (the checkout
//     detached there, then back — see baselineAt): the baseline means "before this change's code", so taking
//     it at the rebased HEAD would show the change's own checks green at
//     baseline. Every check verify runs (task checks and the effective
//     spec's scenarios, as the change's plan has them) and the regression
//     command; a check whose files do not exist on the base is red there,
//     as a new test should be. The new baseline replaces the old one;
//  4. records the catch-up (rebases: [{at, from, to, head, rebased}]),
//     sets base_sha to the base tip, marks the last verify stale (kept, with
//     stale_reason), and puts a verified or reviewed change back to
//     in-progress — except inside archive, which keeps it reviewed and
//     re-verifies at once: a green re-verify keeps the review, which judged
//     the change's own code (D38);
//  5. commits the state on change/<id> as "apex: <id> rebased onto <base>".
//
// A change branch that already holds the base's new commits (a person
// rebased it, after a conflict) is not rebased again: only steps 3–5 run.

// CodeRebaseConflict: change/<id> does not rebase onto its base branch.
const CodeRebaseConflict = "change.rebase-conflict"

// CatchUp is one catch-up with a moved base branch, as start, verify and
// archive report it.
type CatchUp struct {
	BaseBranch string `json:"base_branch"`
	From       string `json:"from"`
	To         string `json:"to"`
	// Rebased: ape ran the rebase (false when the branch already held the
	// base's commits).
	Rebased bool `json:"rebased"`
	// Baseline is the new baseline, taken at To.
	Baseline   []CheckResult `json:"baseline"`
	Regression *CheckResult  `json:"regression,omitempty"`
	// VerifyStale: a verify was recorded, and is now stale.
	VerifyStale bool   `json:"verify_stale"`
	Status      string `json:"status"`
}

// isAncestor reports whether a is an ancestor of (or equal to) b.
func (p *Project) isAncestor(ctx context.Context, a, b string) bool {
	_, err := git(ctx, p.Root, "merge-base", "--is-ancestor", a, b)
	return err == nil
}

// baseMoved reports whether the change's base branch moved: its tip, and
// whether change/<id> (HEAD) lacks it (a rebase is needed). moved is
// false when the tip is base_sha, or when the base went backwards and the
// branch holds its tip already.
func (p *Project) baseMoved(ctx context.Context, st *State) (tip string, moved, needRebase bool) {
	if st.BaseBranch == "" || !p.isWorkTree(ctx) {
		return "", false, false
	}
	tip, err := git(ctx, p.Root, "rev-parse", "--verify", "--quiet", "refs/heads/"+st.BaseBranch)
	if err != nil || tip == "" || tip == st.BaseSHA {
		return tip, false, false
	}
	needRebase = !p.isAncestor(ctx, tip, "HEAD")
	ahead := st.BaseSHA == "" || p.isAncestor(ctx, st.BaseSHA, tip)
	return tip, needRebase || ahead, needRebase
}

// catchUp brings a started change, HEAD on its branch, up to date with its
// base branch when that moved (see the comment above). what names the
// command, for messages ("start", "verify", "archive"); inArchive keeps a
// reviewed change reviewed. It returns the change as read afterwards, and
// nil when the base had not moved.
func (p *Project) catchUp(ctx context.Context, ci ChangeInfo, what string, opt RunOptions, inArchive bool) (ChangeInfo, *CatchUp, error) {
	st := ci.State
	if CurrentBranch(ctx, p.Root) != ChangeBranch(ci.ID) {
		return ci, nil, nil
	}
	tip, moved, needRebase := p.baseMoved(ctx, st)
	if !moved {
		return ci, nil, nil
	}
	id, base := ci.ID, st.BaseBranch
	if err := p.cleanForCatchUp(ctx, id, base, what); err != nil {
		return ci, nil, err
	}
	if needRebase {
		if err := p.rebaseOrBlock(ctx, ci, what); err != nil {
			return ci, nil, err
		}
	}
	ci, err := checkLoaded(p.loadChange(id))
	if err != nil {
		return ci, nil, err
	}
	st = ci.State
	baseline, regression, err := p.baselineAt(ctx, ci, tip, opt)
	if err != nil {
		return ci, nil, err
	}
	cu := &CatchUp{BaseBranch: base, From: st.BaseSHA, To: tip, Rebased: needRebase, Baseline: baseline, Regression: regression}
	st.Rebases = append(st.Rebases, Rebase{At: Stamp(), From: st.BaseSHA, To: tip, Head: HeadSHA(ctx, p.Root), Rebased: needRebase})
	st.BaseSHA = tip
	st.Baseline = make([]Baseline, 0, len(baseline))
	for i := range baseline {
		st.Baseline = append(st.Baseline, Baseline{Task: baseline[i].Task, Exit: baseline[i].Exit, Cmd: baseline[i].Cmd, SHA: tip})
	}
	if regression != nil {
		st.Regression = &RegressionBaseline{Cmd: regression.Cmd, Exit: regression.Exit, OutputSHA256: regression.OutputSHA256, SHA: tip, At: Stamp()}
	}
	if st.Verify != nil {
		st.Verify.Stale = true
		st.Verify.StaleReason = "base moved: " + base + " " + short(cu.From) + ".." + short(tip) + "; the change was rebased onto it"
		cu.VerifyStale = true
	}
	if !inArchive && (st.Status == StatusVerified || st.Status == StatusReviewed) {
		st.Status = StatusInProgress
	}
	cu.Status = st.Status
	if err := p.saveAndCommitSubject(ctx, id, st, "apex: "+id+" rebased onto "+base); err != nil {
		return ci, cu, err
	}
	return ci, cu, nil
}

// cleanForCatchUp is the clean tree a rebase needs: uncommitted files in
// the change's own folder are committed on its branch; anything else
// refuses.
func (p *Project) cleanForCatchUp(ctx context.Context, id, base, what string) error {
	err := p.commitChangeFolder(ctx, id, what)
	if fe, ok := errors.AsType[*FailError](err); ok && fe.Code == "tree.dirty" {
		_, list, _ := strings.Cut(fe.Msg, "uncommitted changes: ")
		list, _, _ = strings.Cut(list, " — ")
		return &FailError{Code: "tree.dirty", Msg: base + " moved ahead of " + ChangeBranch(id) + ", and ape rebases the change onto it before " + what +
			"; the tree has uncommitted changes: " + list + " — commit them on " + ChangeBranch(id) + " (trailer `Change: " + id + "`) and run `ape change " + what + " " + id + "` again"}
	}
	return err
}

// rebaseOrBlock rebases the change's branch (checked out) onto its base
// branch as it is now. On a conflict the rebase is aborted and the change
// blocked, the reason naming the conflicting files (committed on its
// branch): ConflictError change.rebase-conflict.
func (p *Project) rebaseOrBlock(ctx context.Context, ci ChangeInfo, what string) error {
	id, base, branch := ci.ID, ci.State.BaseBranch, ChangeBranch(ci.ID)
	conflicts, err := p.rebaseOnto(ctx, base)
	if err == nil {
		return nil
	}
	if len(conflicts) == 0 {
		return &FailError{Code: CodeBranchFailed, Msg: "rebasing " + branch + " onto " + base + " failed: " + err.Error() + " — the rebase was aborted; fix git and run `ape change " + what + " " + id + "` again"}
	}
	reason := what + ": " + base + " moved, and rebasing " + branch + " onto it conflicts in " + strings.Join(limit(conflicts, 8), ", ") +
		" — a person rebases it (`git switch " + branch + " && git rebase " + base + "`, resolving those files), or the change is re-planned or abandoned; then `ape change start " + id + "`"
	st := ci.State
	st.Status = StatusBlocked
	st.BlockedReason = &reason
	if err := p.saveAndCommit(ctx, id, st); err != nil {
		return err
	}
	return &ConflictError{Code: CodeRebaseConflict, Msg: "change " + id + " does not rebase cleanly onto " + base +
		" — the rebase was aborted, nothing else was written, and the change is blocked with the conflicting files (a person resolves them)", Conflicts: conflicts}
}

// rebaseOnto rebases the checked-out branch onto base. On a conflict it
// aborts the rebase and returns the conflicting files with the error.
func (p *Project) rebaseOnto(ctx context.Context, base string) ([]string, error) {
	_, err := git(ctx, p.Root, "rebase", "-q", base)
	if err == nil {
		return nil, nil
	}
	out, _ := git(ctx, p.Root, "diff", "--name-only", "--diff-filter=U")
	var conflicts []string
	for f := range strings.SplitSeq(out, "\n") {
		if f != "" {
			conflicts = append(conflicts, f)
		}
	}
	_, _ = git(ctx, p.Root, "rebase", "--abort")
	return conflicts, err
}

// baselineAt runs the change's baseline — every check verify runs, and the
// regression command — at sha, IN PLACE: the project's checkout is
// detached at sha for the run and switched back to change/<id> after it.
// In place rather than in a separate worktree, because a project's
// ignored files (node_modules, build caches, generated code) belong to the
// checkout: a fresh worktree lacks them, so a brownfield regression suite
// would be red there, and every later red would read as pre-existing —
// hiding real regressions. The caller has made the tree clean and holds
// the change's state in memory; the state is written only after the
// switch back. The checks are the change's as its plan has them now;
// output is logged under _apex/state/logs/<id>/ (baseline-*), which
// survives the checkout (ignored).
func (p *Project) baselineAt(ctx context.Context, ci ChangeInfo, sha string, opt RunOptions) (results []CheckResult, regression *CheckResult, err error) {
	branch := ChangeBranch(ci.ID)
	if _, err := git(ctx, p.Root, "checkout", "-q", "--detach", sha); err != nil {
		return nil, nil, &FailError{Code: CodeBranchFailed, Msg: "cannot check out " + short(sha) + " for the baseline: " + err.Error() + " — fix git and run the command again"}
	}
	defer func() {
		if _, berr := git(context.WithoutCancel(ctx), p.Root, "checkout", "-q", branch); berr != nil && err == nil {
			results, regression = nil, nil
			err = &FailError{Code: CodeBranchFailed, Msg: "the baseline ran at " + short(sha) + " but switching back to " + branch + " failed: " + berr.Error() +
				" — run `git switch " + branch + "`, then the command again"}
		}
	}()
	logDir := p.LogDir(ci.ID)
	results = p.RunChecks(ctx, logDir, "baseline-", p.ChangeChecks(ci.Doc), opt)
	for i := range results {
		results[i].StrayFiles = nil
	}
	if cmd := p.regressionCmd(); cmd != "" {
		r := p.runRegression(ctx, cmd, logDir, "baseline-", opt)
		r.StrayFiles = nil
		regression = &r
	}
	return results, regression, nil
}
