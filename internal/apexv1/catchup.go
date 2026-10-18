package apexv1

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
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
//  2. rebases change/<id> onto the base branch;
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
// A rebase that conflicts (H8, D47 Q3; supersedes D43) is aborted, and ape
// leaves the conflict on the change's branch for apex-apply to resolve:
// the baseline is taken at the base tip as above (the tree is still
// clean), the old tip is kept as refs/apex/pre-rebase/<id>/<n>, change/<id>
// is recreated at the base tip with its own folder as the old tip had it
// and the state recorded — status in-progress, the verify stale, the
// rebase recorded with old_tip_ref and conflicts — committed as "apex:
// <id> conflicted rebasing onto <base>"; then the branch's net diff
// (merge-base..old tip, the change folder aside) is cherry-picked
// --no-commit on top, which leaves conflict markers and unmerged paths in
// the tree with HEAD on change/<id>. ConflictError change.conflicted, exit
// 3, lists the files. apex-apply edits them, `git add`s and commits; verify
// refuses until no unmerged path and no marker remains
// (change.unresolved-conflict), and the review runs again. ape still owns
// the rebase: the model never runs git rebase, merge or cherry-pick.
//
// A change branch that already holds the base's new commits (a person
// rebased it) is not rebased again: only steps 3–5 run.

// CodeConflicted: change/<id> did not rebase onto its moved base; the
// conflict is left on the branch for apex-apply.
const CodeConflicted = "change.conflicted"

// CodeUnresolvedConflict: verify on a change whose conflict is not
// resolved yet.
const CodeUnresolvedConflict = "change.unresolved-conflict"

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
	// OldTipRef keeps the branch's tip from before a conflicted rebase;
	// Conflicts are the files left with conflict markers (H8).
	OldTipRef string   `json:"old_tip_ref,omitempty"`
	Conflicts []string `json:"conflicts,omitempty"`
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
	if !moved || p.recordsOnlyMoved(ctx, st.BaseSHA, tip) {
		return ci, nil, nil
	}
	id, base := ci.ID, st.BaseBranch
	// A re-entry catches up with the local base: refused when that is
	// behind its upstream (archive checks it up front itself).
	if what == "start" {
		if err := p.refuseBehindUpstream(ctx, base, "catching "+id+" up"); err != nil {
			return ci, nil, err
		}
	}
	if err := p.cleanForCatchUp(ctx, id, base, what); err != nil {
		return ci, nil, err
	}
	var conflicts []string
	if needRebase {
		var err error
		if conflicts, err = p.rebaseOrConflict(ctx, id, base, what); err != nil {
			return ci, nil, err
		}
	}
	ci, err := checkLoaded(p.loadChange(id))
	if err != nil {
		return ci, nil, err
	}
	// Taken while the tree is clean, a conflicted rebase included: the
	// conflict is laid out only after.
	baseline, regression, err := p.baselineAt(ctx, ci, tip, opt)
	if err != nil {
		return ci, nil, err
	}
	// Interrupted: the rebase stands, unrecorded — the next catch-up finds
	// the branch holding the base and re-takes only the baseline.
	if err := interrupted(ctx, "the catch-up of "+id+" (its baseline at "+short(tip)+")"); err != nil {
		return ci, nil, err
	}
	ctx = context.WithoutCancel(ctx)
	cu := &CatchUp{BaseBranch: base, From: ci.State.BaseSHA, To: tip, Rebased: needRebase, Baseline: baseline, Regression: regression}
	if len(conflicts) > 0 {
		cu.Conflicts = conflicts
		return p.layOutConflict(ctx, ci, cu, inArchive)
	}
	ci, err = p.recordCatchUp(ctx, ci, cu, HeadSHA(ctx, p.Root), inArchive, "apex: "+id+" rebased onto "+base)
	return ci, cu, err
}

// recordCatchUp writes a catch-up into the change's state — the rebase
// record, base_sha, the new baseline, the verify stale, a verified or
// reviewed change back to in-progress unless inArchive — and commits it
// with the change's folder under subject.
func (p *Project) recordCatchUp(ctx context.Context, ci ChangeInfo, cu *CatchUp, head string, inArchive bool, subject string) (ChangeInfo, error) {
	st, tip, base := ci.State, cu.To, cu.BaseBranch
	st.Rebases = append(st.Rebases, Rebase{At: Stamp(), From: st.BaseSHA, To: tip, Head: head, Rebased: cu.Rebased, OldTipRef: cu.OldTipRef, Conflicts: cu.Conflicts})
	st.BaseSHA = tip
	st.Baseline = make([]Baseline, 0, len(cu.Baseline))
	for i := range cu.Baseline {
		st.Baseline = append(st.Baseline, Baseline{Task: cu.Baseline[i].Task, Exit: cu.Baseline[i].Exit, Cmd: cu.Baseline[i].Cmd, SHA: tip})
	}
	if r := cu.Regression; r != nil {
		st.Regression = p.regressionBaseline(r, tip)
	}
	if st.Verify != nil {
		st.Verify.Stale = true
		st.Verify.StaleReason = "base moved: " + base + " " + short(cu.From) + ".." + short(tip) + "; the change was rebased onto it"
		cu.VerifyStale = true
	}
	switch {
	case len(cu.Conflicts) > 0:
		// The resolution is new code: verify and review again.
		st.Status = StatusInProgress
		st.BlockedReason = nil
	case !inArchive && (st.Status == StatusVerified || st.Status == StatusReviewed):
		st.Status = StatusInProgress
	}
	cu.Status = st.Status
	if err := SaveState(p.StateFile(ci.ID), st); err != nil {
		return ci, err
	}
	if _, err := p.commitPaths(ctx, subject, ci.ID, p.Rel(p.ChangeDir(ci.ID))); err != nil {
		return ci, commitFailed(err, "state.json written but its commit failed: "+err.Error()+" — fix git (identity, lock) and re-run the command")
	}
	return ci, nil
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

// rebaseOrConflict rebases the change's branch (checked out) onto its
// base branch as it is now. conflicts are the files a rebase that
// conflicted stopped on; it was aborted, and the branch and the tree are
// as they were.
func (p *Project) rebaseOrConflict(ctx context.Context, id, base, what string) (conflicts []string, err error) {
	conflicts, err = p.rebaseOnto(ctx, base)
	if err == nil {
		return nil, nil
	}
	if len(conflicts) == 0 {
		return nil, &FailError{Code: CodeBranchFailed, Msg: "rebasing " + ChangeBranch(id) + " onto " + base + " failed: " + err.Error() + " — the rebase was aborted; fix git and run `ape change " + what + " " + id + "` again"}
	}
	return conflicts, nil
}

// layOutConflict leaves a conflicted rebase on the change's branch (see
// the comment above): the old tip kept under refs/apex/pre-rebase/<id>/<n>,
// change/<id> recreated at the base tip with its folder as the old tip had
// it and the state committed, then the net diff cherry-picked --no-commit.
// A net diff that applies cleanly after all is committed as the rebased
// code, and the catch-up ends as a clean one would.
func (p *Project) layOutConflict(ctx context.Context, ci ChangeInfo, cu *CatchUp, inArchive bool) (ChangeInfo, *CatchUp, error) {
	id, base, branch, tip := ci.ID, cu.BaseBranch, ChangeBranch(ci.ID), cu.To
	oldTip := HeadSHA(ctx, p.Root)
	fail := func(step string, err error) error {
		return &FailError{Code: CodeBranchFailed, Msg: "laying out the rebase conflict of " + branch + " onto " + base + " failed at " + step + ": " + err.Error() +
			" — the branch's old tip is " + oldTip + "; restore it (`git reset --hard " + short(oldTip) + "` on " + branch + ") and run the command again"}
	}
	mergeBase, err := git(ctx, p.Root, "merge-base", tip, oldTip)
	if err != nil {
		return ci, cu, fail("merge-base", err)
	}
	ref, err := p.keepPreRebaseTip(ctx, id, oldTip)
	if err != nil {
		return ci, cu, fail("update-ref", err)
	}
	cu.OldTipRef = ref
	folder := p.Rel(p.ChangeDir(id))
	if _, err := git(ctx, p.Root, "reset", "-q", "--hard", tip); err != nil {
		return ci, cu, fail("reset", err)
	}
	if _, err := git(ctx, p.Root, "checkout", oldTip, "--", folder); err != nil {
		return ci, cu, fail("restoring "+folder, err)
	}
	ci, err = checkLoaded(p.loadChange(id))
	if err != nil {
		return ci, cu, err
	}
	// Committed with the files the rebase stopped on; the net diff's
	// unmerged paths, what is left to resolve, replace them once known.
	status, blocked := ci.State.Status, ci.State.BlockedReason
	if ci, err = p.recordCatchUp(ctx, ci, cu, tip, inArchive, "apex: "+id+" conflicted rebasing onto "+base); err != nil {
		return ci, cu, err
	}
	unmerged, err := p.cherryPickNetDiff(ctx, id, mergeBase, oldTip)
	if err != nil {
		return ci, cu, fail("cherry-pick", err)
	}
	if len(unmerged) == 0 {
		// The net diff applies after all: commit it as the rebased code.
		return ci, cu, p.commitAppliedNetDiff(ctx, ci, cu, oldTip, status, blocked, inArchive, fail)
	}
	st := ci.State
	last := &st.Rebases[len(st.Rebases)-1]
	// The state's conflict list, in the tree: committed with the
	// resolution (the index holds unmerged paths, so ape cannot commit).
	cu.Conflicts, last.Conflicts = unmerged, unmerged
	if err := SaveState(p.StateFile(id), st); err != nil {
		return ci, cu, err
	}
	if _, err := git(ctx, p.Root, "add", "--", p.Rel(p.StateFile(id))); err != nil {
		return ci, cu, fail("staging state.json", err)
	}
	return ci, cu, &ConflictError{Code: CodeConflicted, Msg: base + " moved, and change " + id + " does not rebase onto it: the conflict is left on " + branch +
		" (markers in the tree, HEAD on the branch, the change in-progress) — apex-apply resolves the markers (edit, `git add`, commit with trailer `Change: " + id +
		"`), then `ape change verify " + id + "` and the review run again; the old tip is kept as " + ref, Conflicts: unmerged}
}

// commitAppliedNetDiff commits a net diff that cherry-picked cleanly as
// the rebased code, and ends the catch-up as a clean one would: the state
// back to status and blocked, its last rebase without conflicts. fail is
// layOutConflict's step error.
func (p *Project) commitAppliedNetDiff(ctx context.Context, ci ChangeInfo, cu *CatchUp, oldTip, status string, blocked *string, inArchive bool, fail func(string, error) error) error {
	id, base, branch := ci.ID, cu.BaseBranch, ChangeBranch(ci.ID)
	st := ci.State
	last := &st.Rebases[len(st.Rebases)-1]
	subject := "apex: " + id + " re-applied onto " + base + " as one commit"
	if err := ScanCommit(ctx, p.Root, subject); err != nil {
		if fe, ok := errors.AsType[*FailError](err); ok {
			fe.Msg += "; the branch's old tip is " + oldTip + ": restore it (`git reset --hard " + short(oldTip) + "` on " + branch + "), take the secret out there, and run the command again"
		}
		return err
	}
	if _, err := git(ctx, p.Root, "commit", "--no-verify", "-q", "-m", subject+"\n\nChange: "+id+"\n"); err != nil {
		return fail("commit", err)
	}
	cu.Conflicts, last.Conflicts, last.Head = nil, nil, HeadSHA(ctx, p.Root)
	// recordCatchUp put it in-progress for a conflict there was not.
	st.Status, st.BlockedReason = status, blocked
	if !inArchive && (status == StatusVerified || status == StatusReviewed) {
		st.Status = StatusInProgress
	}
	cu.Status = st.Status
	return p.saveAndCommitSubject(ctx, id, st, "apex: "+id+" rebased onto "+base)
}

// keepPreRebaseTip records a branch tip a conflicted rebase replaces as
// refs/apex/pre-rebase/<id>/<n>, n one past the last; it returns the ref.
func (p *Project) keepPreRebaseTip(ctx context.Context, id, sha string) (string, error) {
	prefix := "refs/apex/pre-rebase/" + id + "/"
	out, _ := git(ctx, p.Root, "for-each-ref", "--format=%(refname)", prefix)
	n := 0
	for ref := range strings.SplitSeq(out, "\n") {
		if k, err := strconv.Atoi(strings.TrimPrefix(ref, prefix)); err == nil && k > n {
			n = k
		}
	}
	ref := prefix + strconv.Itoa(n+1)
	_, err := git(ctx, p.Root, "update-ref", ref, sha)
	return ref, err
}

// cherryPickNetDiff applies change/<id>'s net diff — mergeBase..oldTip,
// the change's own folder left out (HEAD has it already) — to the tree
// with `git cherry-pick --no-commit` of a temporary commit, then clears
// any sequencer state, so HEAD stays on the branch. It returns the
// unmerged paths (project-relative).
func (p *Project) cherryPickNetDiff(ctx context.Context, id, mergeBase, oldTip string) ([]string, error) {
	tmp, err := os.MkdirTemp("", "ape-netdiff-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(tmp, "index")}
	prefix, err := git(ctx, p.Root, "rev-parse", "--show-prefix")
	if err != nil {
		return nil, err
	}
	folder := p.Rel(p.ChangeDir(id))
	// treeOf is rev's tree with the change's folder taken from HEAD, so
	// the folder is in neither side of the diff.
	treeOf := func(rev string) (string, error) {
		for _, args := range [][]string{
			{"read-tree", rev},
			{"rm", "-r", "-q", "-f", "--cached", "--ignore-unmatch", "--", folder},
			{"read-tree", "--prefix=" + prefix + folder + "/", "HEAD:" + prefix + folder},
		} {
			if _, err := gitWithEnv(ctx, p.Root, env, args...); err != nil {
				return "", err
			}
		}
		return gitWithEnv(ctx, p.Root, env, "write-tree")
	}
	from, err := treeOf(mergeBase)
	if err != nil {
		return nil, err
	}
	to, err := treeOf(oldTip)
	if err != nil {
		return nil, err
	}
	parent, err := git(ctx, p.Root, "commit-tree", from, "-m", "apex: "+id+" net diff base")
	if err != nil {
		return nil, err
	}
	pick, err := git(ctx, p.Root, "commit-tree", to, "-p", parent, "-m", "apex: "+id+" net diff")
	if err != nil {
		return nil, err
	}
	_, pickErr := git(ctx, p.Root, "cherry-pick", "--no-commit", pick)
	_, _ = git(ctx, p.Root, "cherry-pick", "--quit")
	unmerged := p.unmergedPaths(ctx)
	if pickErr != nil && len(unmerged) == 0 {
		return nil, pickErr
	}
	return unmerged, nil
}

// unmergedPaths lists the index's unmerged paths, project-relative.
func (p *Project) unmergedPaths(ctx context.Context) []string {
	out, _ := git(ctx, p.Root, "diff", "--name-only", "--relative", "--diff-filter=U")
	var paths []string
	for f := range strings.SplitSeq(out, "\n") {
		if f != "" {
			paths = append(paths, f)
		}
	}
	return paths
}

// unresolvedConflicts is what keeps a conflicted catch-up unresolved: an
// unmerged path in the index, or a conflict marker left in a file the last
// rebase conflicted in.
func (p *Project) unresolvedConflicts(ctx context.Context, st *State) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range p.unmergedPaths(ctx) {
		seen[f] = true
		out = append(out, f+" (unmerged)")
	}
	if n := len(st.Rebases); n > 0 {
		for _, f := range st.Rebases[n-1].Conflicts {
			if !seen[f] && hasConflictMarkers(filepath.Join(p.Root, filepath.FromSlash(f))) {
				out = append(out, f+" (conflict markers)")
			}
		}
	}
	return out
}

// hasConflictMarkers reports a line that opens or closes a conflict
// region (`<<<<<<< ` / `>>>>>>> `, as git writes them).
func hasConflictMarkers(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		for _, m := range []string{"<<<<<<<", ">>>>>>>"} {
			if rest, ok := strings.CutPrefix(line, m); ok && (rest == "" || rest[0] == ' ' || rest[0] == '\r') {
				return true
			}
		}
	}
	return false
}

// rebaseOnto rebases the checked-out branch onto base. On a conflict it
// aborts the rebase and returns the conflicting files with the error.
func (p *Project) rebaseOnto(ctx context.Context, base string) ([]string, error) {
	_, err := git(ctx, p.Root, "rebase", "-q", base)
	if err == nil {
		return nil, nil
	}
	conflicts := p.unmergedPaths(ctx)
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
