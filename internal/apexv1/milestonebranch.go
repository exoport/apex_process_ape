package apexv1

import (
	"context"
	"errors"
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
	branch, created, _, err = p.PrepareMilestoneBranchAdopt(ctx, ms, false)
	return branch, created, err
}

// CodeMilestoneBranchExists: a claim found milestone/<M> on the remote
// with commits main lacks, and no claim of M recorded on main (D736).
const CodeMilestoneBranchExists = "milestone.branch-exists"

func init() {
	DiagnosticCodes = append(DiagnosticCodes, CodeDoc{
		CodeMilestoneBranchExists, 1, ActionPerson,
		"ape milestone claim <M> in a project integrating through GitHub: milestone/<M> already exists on the remote with commits main lacks, and no claim of M is recorded on main (an earlier milestone's work, or a leftover) — ape does not adopt it silently: a person prunes it (milestone branches are protected from deletion: an admin lifts that rule briefly), or claims it knowingly with `ape milestone claim <M> --adopt`. A leftover with nothing of its own (an ancestor of main) is fast-forwarded to main instead (v1.5 stage 1a, D736)",
	})
}

// PrepareMilestoneBranchAdopt is PrepareMilestoneBranch judging a
// milestone/<M> the remote already has (D736): adopted as it is when a
// claim of M is recorded on main (a re-claim, a handover) or adopt is
// set; else, with nothing of its own (an ancestor of main — a leftover
// such as an R3 probe), fast-forwarded to main's tip, M cut from main as a
// fresh claim is (note says so); else refused, milestone.branch-exists.
func (p *Project) PrepareMilestoneBranchAdopt(ctx context.Context, ms string, adopt bool) (branch string, created bool, note string, err error) {
	branch = p.MilestoneBase(ms)
	if branch == "" {
		return "", false, "", nil
	}
	remote := p.trunkRemote(ctx)
	fail := func(what string, err error) error {
		return &FailError{Code: CodeFetchFailed, Msg: fmt.Sprintf("claiming %s needs %s on %s: %s failed: %v — fix the remote or its credentials, then claim again (the lease is held)", ms, branch, remote, what, err)}
	}
	if err := p.fetchCoordinated(ctx, "main"); err != nil {
		return branch, false, "", coded(err, func() error { return fail("git fetch "+remote+" main", err) })
	}
	tracking := "refs/remotes/" + remote + "/" + branch
	if err := p.fetchCoordinated(ctx, branch); err != nil {
		if fe, ok := errors.AsType[*FailError](err); ok {
			return branch, false, "", fe // refused, not absent (FINDING-V15-1A-6)
		}
		// The remote has no milestone/<M> yet: cut it from its main.
		if err := p.cutMilestoneBranch(ctx, branch, tracking, remote, fail); err != nil {
			return branch, false, "", err
		}
		created = true
	} else if note, err = p.judgeMilestoneBranch(ctx, ms, branch, tracking, remote, adopt); err != nil {
		return branch, false, "", err
	}
	if !p.branchExists(ctx, branch) {
		if _, err := git(ctx, p.Root, "branch", "--quiet", "--track", branch, remote+"/"+branch); err != nil {
			return branch, created, note, &FailError{Code: CodeBranchFailed, Msg: "cannot create " + branch + ": " + err.Error()}
		}
	}
	return branch, created, note, nil
}

// cutMilestoneBranch cuts branch from the remote's main and pushes it: R1,
// only apex-pusher creates milestone/* (the App service's token, when the
// project has one: pushTarget). A failure leaves no tracking ref.
func (p *Project) cutMilestoneBranch(ctx context.Context, branch, tracking, remote string, fail func(string, error) error) error {
	if _, err := git(ctx, p.Root, "update-ref", tracking, "refs/remotes/"+remote+"/main"); err != nil {
		return fail("git update-ref "+tracking, err)
	}
	target, penv, _, err := p.pushTarget(ctx)
	if err != nil {
		_, _ = git(ctx, p.Root, "update-ref", "-d", tracking)
		return err
	}
	if _, err := gitWithEnv(ctx, p.Root, penv, "push", "--quiet", "--no-follow-tags", target, tracking+":refs/heads/"+branch); err != nil {
		_, _ = git(ctx, p.Root, "update-ref", "-d", tracking)
		return fail("git push "+remote+" "+branch, err)
	}
	return nil
}

// judgeMilestoneBranch is the claim's judgement of a milestone/<M> the
// remote already has (D736); note is "" when it is adopted as it is.
func (p *Project) judgeMilestoneBranch(ctx context.Context, ms, branch, tracking, remote string, adopt bool) (string, error) {
	mainRef := "refs/remotes/" + remote + "/main"
	if adopt || p.claimRecorded(ctx, ms, mainRef) {
		return "", nil
	}
	old, err := git(ctx, p.Root, "rev-parse", tracking)
	if err != nil {
		return "", err
	}
	mainTip, err := git(ctx, p.Root, "rev-parse", mainRef)
	if err != nil {
		return "", err
	}
	if old == mainTip {
		return "", nil
	}
	if _, err := git(ctx, p.Root, "merge-base", "--is-ancestor", tracking, mainRef); err != nil {
		n, _ := git(ctx, p.Root, "rev-list", "--count", mainRef+".."+tracking)
		return "", &FailError{Code: CodeMilestoneBranchExists, Action: ActionPerson, Msg: fmt.Sprintf(
			"%s on %s has %s commit(s) main lacks, and no claim of %s is recorded on main (an earlier milestone's work, or a leftover): ape does not adopt it silently — a person prunes it (milestone branches are protected from deletion: an admin lifts that rule briefly), or claims it knowingly with `ape milestone claim %s --adopt`",
			branch, remote, n, ms, ms)}
	}
	// Nothing of its own: brought to main's tip, a fast-forward (R1's
	// writer, the App's token when the project has one).
	target, env, _, err := p.pushTarget(ctx)
	if err != nil {
		return "", err
	}
	if _, err := gitWithEnv(ctx, p.Root, env, "push", "--quiet", "--no-follow-tags", target, mainTip+":refs/heads/"+branch); err != nil {
		return "", &FailError{Code: CodeIntegrationPush, Action: ActionRetry, Msg: fmt.Sprintf("%s on %s was a leftover at %s with nothing of its own, but fast-forwarding it to main failed: %v — claim again", branch, remote, short(old), err)}
	}
	if _, err := git(ctx, p.Root, "update-ref", tracking, mainTip); err != nil {
		return "", err
	}
	if p.branchExists(ctx, branch) && CurrentBranch(ctx, p.Root) != branch {
		if _, err := git(ctx, p.Root, "merge-base", "--is-ancestor", "refs/heads/"+branch, mainTip); err == nil {
			_, _ = git(ctx, p.Root, "update-ref", "refs/heads/"+branch, mainTip)
		}
	}
	return fmt.Sprintf("%s existed on %s at %s with nothing of its own (an ancestor of main): fast-forwarded to main %s, so %s starts from main", branch, remote, short(old), short(mainTip), ms), nil
}

// claimRecorded reports a claim of ms recorded on ref ("apex: <M> claimed
// by …", the claim record PR, section 5.4).
func (p *Project) claimRecorded(ctx context.Context, ms, ref string) bool {
	out, err := git(ctx, p.Root, "log", "--format=%s", "--fixed-strings", "--grep=apex: "+ms+" claimed by ", ref)
	if err != nil {
		return false
	}
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, "apex: "+ms+" claimed by ") {
			return true
		}
	}
	return false
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
