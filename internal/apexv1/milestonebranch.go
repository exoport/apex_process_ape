package apexv1

import (
	"context"
	"fmt"
	"strings"
)

// MilestoneBranchPrefix starts a coordinated milestone's branch (v1.5
// stage 1a, proposal section 5): milestone/<M>, cut from main at the
// claim; M's changes start from it and their PRs merge into it.
const MilestoneBranchPrefix = "milestone/"

// CodeMilestoneBranchMissing: a coordinated project's change of M needs
// milestone/<M>, and this checkout has none: M was never claimed here.
const CodeMilestoneBranchMissing = "milestone.branch-missing"

// MilestoneBranch is milestone/<M>.
func MilestoneBranch(ms string) string { return MilestoneBranchPrefix + ms }

// MilestoneBase is the branch a change of ms is planned and started on:
// milestone/<M> in a coordinated project (coordination.github), "" for a
// tweak, no milestone, or an uncoordinated project (any branch, D38).
func (p *Project) MilestoneBase(ms string) string {
	if ms == "" || ms == MilestoneNone || !p.coordinated() {
		return ""
	}
	return MilestoneBranch(ms)
}

// trunkRemote is the remote main tracks; origin when it tracks none.
func (p *Project) trunkRemote(ctx context.Context) string {
	if r, _ := git(ctx, p.Root, "config", "branch.main.remote"); r != "" && r != "." {
		return r
	}
	return "origin"
}

// PrepareMilestoneBranch is the claim's branch step in a coordinated
// project (section 5.1): milestone/<M> on the trunk's remote, cut from its
// main and pushed when the remote has none, and a local milestone/<M>
// tracking it. created reports that this call pushed it. The checkout
// stays on its branch. Nothing for an uncoordinated project.
func (p *Project) PrepareMilestoneBranch(ctx context.Context, ms string) (branch string, created bool, err error) {
	branch = p.MilestoneBase(ms)
	if branch == "" {
		return "", false, nil
	}
	remote := p.trunkRemote(ctx)
	env := []string{"GIT_TERMINAL_PROMPT=0"}
	fail := func(what string, err error) error {
		return &FailError{Code: CodeFetchFailed, Msg: fmt.Sprintf("claiming %s needs %s on %s: %s failed: %v — fix the remote or its credentials, then claim again (the lease is held)", ms, branch, remote, what, err)}
	}
	if _, err := gitWithEnv(ctx, p.Root, env, "fetch", "--quiet", "--no-tags", remote,
		"+refs/heads/main:refs/remotes/"+remote+"/main"); err != nil {
		return branch, false, fail("git fetch "+remote+" main", err)
	}
	tracking := "refs/remotes/" + remote + "/" + branch
	if _, err := gitWithEnv(ctx, p.Root, env, "fetch", "--quiet", "--no-tags", remote,
		"+refs/heads/"+branch+":"+tracking); err != nil {
		// The remote has no milestone/<M> yet: cut it from its main.
		if _, err := git(ctx, p.Root, "update-ref", tracking, "refs/remotes/"+remote+"/main"); err != nil {
			return branch, false, fail("git update-ref "+tracking, err)
		}
		if _, err := gitWithEnv(ctx, p.Root, env, "push", "--quiet", "--no-follow-tags", remote,
			tracking+":refs/heads/"+branch); err != nil {
			_, _ = git(ctx, p.Root, "update-ref", "-d", tracking)
			return branch, false, fail("git push "+remote+" "+branch, err)
		}
		created = true
	}
	if !p.branchExists(ctx, branch) {
		if _, err := git(ctx, p.Root, "branch", "--quiet", "--track", branch, remote+"/"+branch); err != nil {
			return branch, created, &FailError{Code: CodeBranchFailed, Msg: "cannot create " + branch + ": " + err.Error()}
		}
	}
	return branch, created, nil
}

// enterMilestoneBase puts a coordinated project's checkout on the branch
// a change of ms is planned or started on (MilestoneBase), switching from
// another base branch when the tree is clean. On a change branch it does
// nothing: the callers' own rules (D38) judge that.
func (p *Project) enterMilestoneBase(ctx context.Context, ms, what string) error {
	base := p.MilestoneBase(ms)
	cur := CurrentBranch(ctx, p.Root)
	if base == "" || cur == base || strings.HasPrefix(cur, ChangeBranchPrefix) || !p.isWorkTree(ctx) {
		return nil
	}
	if !p.branchExists(ctx, base) {
		return &FailError{Code: CodeMilestoneBranchMissing, Msg: fmt.Sprintf("%s needs %s, the branch %s's changes are based on in a coordinated project, and this checkout has none — `ape milestone claim %s` creates it", what, base, ms, ms)}
	}
	if err := p.requireCleanTree(ctx, what); err != nil {
		return err
	}
	return p.switchBranch(ctx, base)
}
