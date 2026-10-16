package apexv1

import (
	"context"
	"strings"
)

// Branch per change (trial decision D38). `ape change start` creates and
// checks out change/<id> from the branch it runs on (recorded as
// base_branch and base_sha); the change's work and ape's status commits
// for it happen there; `ape change archive` rebases it onto the base
// branch, re-verifies when that moved the code, and squash-merges it as
// one commit; archive and abandon delete it. The base branch only ever
// receives verified, reviewed changes, and the records that are not a
// change's — milestones, decisions accepted, new change plans — land on
// it directly (D11).

// ChangeBranchPrefix prefixes every change branch.
const ChangeBranchPrefix = "change/"

// ChangeBranch is the branch a change is worked on.
func ChangeBranch(id string) string { return ChangeBranchPrefix + id }

// ChangeOfBranch is the change id a branch belongs to, "" for a branch
// that is not a change branch.
func ChangeOfBranch(branch string) string {
	id, ok := strings.CutPrefix(branch, ChangeBranchPrefix)
	if !ok {
		return ""
	}
	return id
}

// HeadChange is the id of the change whose branch is checked out, "" when
// HEAD is on another branch, detached, or outside git.
func (p *Project) HeadChange(ctx context.Context) string {
	return ChangeOfBranch(CurrentBranch(ctx, p.Root))
}

// branchExists reports whether a local branch exists.
func (p *Project) branchExists(ctx context.Context, branch string) bool {
	_, err := git(ctx, p.Root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// changeBranches is the ids of every change/<id> branch.
func (p *Project) changeBranches(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	refs, err := git(ctx, p.Root, "for-each-ref", "--format=%(refname:short)", "refs/heads/"+ChangeBranchPrefix)
	if err != nil {
		return out
	}
	for ref := range strings.SplitSeq(refs, "\n") {
		if id := ChangeOfBranch(ref); id != "" {
			out[id] = true
		}
	}
	return out
}

// showAt is a project file's content at a revision; rel is
// project-relative (the project may sit below the repository's root).
func (p *Project) showAt(ctx context.Context, rev, rel string) (string, error) {
	return gitRaw(ctx, p.Root, "show", rev+":./"+rel)
}

// loadChangeAt reads a change's state.json and change.md from a branch
// other than the checked-out one, so `ape status` on the base branch shows
// each started change's real status. ok is false when the branch does not
// hold the change.
func (p *Project) loadChangeAt(ctx context.Context, id, branch string) (ChangeInfo, bool) {
	data, err := p.showAt(ctx, branch, p.Rel(p.StateFile(id)))
	if err != nil {
		return ChangeInfo{}, false
	}
	ci := ChangeInfo{ID: id, Branch: branch}
	ci.State, ci.Err = parseState(p.Rel(p.StateFile(id))+" on "+branch, []byte(data))
	if doc, err := p.showAt(ctx, branch, p.Rel(p.ChangeMD(id))); err == nil {
		ci.Doc = ParseChange(p.Rel(p.ChangeMD(id)), doc)
	}
	return ci, true
}

// readChange reads one change from where its state lives: the working
// tree when HEAD is on its branch or it has none, else its branch.
func (p *Project) readChange(ctx context.Context, id, head string, branches map[string]bool) ChangeInfo {
	if id != head && branches[id] {
		if ci, ok := p.loadChangeAt(ctx, id, ChangeBranch(id)); ok {
			return ci
		}
	}
	return p.loadChange(id)
}

// branchName names a branch for a message: "" is a detached HEAD.
func branchName(b string) string {
	if b == "" {
		return "a detached HEAD"
	}
	return b
}

// requireOnBranch refuses a command that records a change's state unless
// HEAD is on the change's branch: its state commits belong there (D38).
// Outside git there are no branches, and nothing to refuse.
func (p *Project) requireOnBranch(ctx context.Context, id string) error {
	if !p.isWorkTree(ctx) {
		return nil
	}
	cur := CurrentBranch(ctx, p.Root)
	if cur == ChangeBranch(id) {
		return nil
	}
	return &FailError{Code: CodeWrongBranch, Msg: "change " + id + " is worked on its branch " + ChangeBranch(id) + ", and HEAD is on " + branchName(cur) +
		" — `git switch " + ChangeBranch(id) + "` (or `ape change start " + id + "`), then run this again"}
}

// requireBranchForWrite is requireOnBranch for block, which also applies
// to a change that has no branch (a draft never started, or one started
// before D38): that is recorded on the branch HEAD is on, unless it is
// another change's.
func (p *Project) requireBranchForWrite(ctx context.Context, ci ChangeInfo) error {
	if !p.isWorkTree(ctx) {
		return nil
	}
	if !p.branchExists(ctx, ChangeBranch(ci.ID)) {
		if cur := CurrentBranch(ctx, p.Root); ChangeOfBranch(cur) != "" {
			return p.otherChangeBranch(cur, "run this again")
		}
		return nil
	}
	return p.requireOnBranch(ctx, ci.ID)
}

// refuseOnChangeBranch refuses a command whose record belongs on the base
// branch (a new change's plan, a milestone, an accepted decision) while
// HEAD is on a change branch.
func (p *Project) refuseOnChangeBranch(ctx context.Context, what string) error {
	id := p.HeadChange(ctx)
	if id == "" {
		return nil
	}
	base := "the base branch"
	if st, err := LoadState(p.StateFile(id)); err == nil && st.BaseBranch != "" {
		base = st.BaseBranch
	}
	return &FailError{Code: CodeChangeBranch, Msg: what + " is recorded on the base branch, and HEAD is on " + ChangeBranch(id) +
		" — `git switch " + base + "` and run it again"}
}

// requireCleanTree refuses when anything is uncommitted (ignored files
// aside): switching branches, rebasing and squash-merging need a clean
// tree, and must not carry someone's work along.
func (p *Project) requireCleanTree(ctx context.Context, what string) error {
	out, err := gitRaw(ctx, p.Root, "status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil {
		return &FailError{Code: CodeGitStatusFailed, Msg: "cannot read git status: " + err.Error()}
	}
	if strings.TrimSpace(out) == "" {
		return nil
	}
	var paths []string
	for line := range strings.SplitSeq(out, "\n") {
		if len(line) > 3 {
			paths = append(paths, strings.Trim(line[3:], "\""))
		}
	}
	return &FailError{Code: "tree.dirty", Msg: what + " switches branches, and the tree has uncommitted changes: " + strings.Join(limit(paths, 8), ", ") +
		" — commit or discard them first"}
}

// commitChangeFolder prepares the tree for a branch switch: uncommitted
// files inside the change's own folder (review.md, which a review may
// leave uncommitted) are committed on its branch as "apex: <id> notes";
// anything else uncommitted, or a dirty change folder while HEAD is not on
// the change's branch, refuses (tree.dirty).
func (p *Project) commitChangeFolder(ctx context.Context, id, what string) error {
	out, err := gitRaw(ctx, p.Root, "status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil {
		return &FailError{Code: CodeGitStatusFailed, Msg: "cannot read git status: " + err.Error()}
	}
	folder := p.Rel(p.ChangeDir(id)) + "/"
	own := false
	for line := range strings.SplitSeq(out, "\n") {
		if len(line) <= 3 {
			continue
		}
		path := strings.Trim(line[3:], "\"")
		if !strings.HasPrefix(path, folder) {
			return p.requireCleanTree(ctx, what)
		}
		own = true
	}
	if !own {
		return nil
	}
	if CurrentBranch(ctx, p.Root) != ChangeBranch(id) {
		return p.requireCleanTree(ctx, what)
	}
	if _, err := p.commitPaths(ctx, "apex: "+id+" notes", id, p.Rel(p.ChangeDir(id))); err != nil {
		return &FailError{Code: CodeCommitFailed, Msg: "cannot commit " + folder + " before " + what + ": " + err.Error() + " — commit it yourself"}
	}
	return nil
}

// switchBranch checks out an existing branch.
func (p *Project) switchBranch(ctx context.Context, branch string) error {
	if _, err := git(ctx, p.Root, "switch", "-q", branch); err != nil {
		return &FailError{Code: CodeBranchFailed, Msg: "cannot switch to " + branch + ": " + err.Error() + " — fix git and run the command again"}
	}
	return nil
}
