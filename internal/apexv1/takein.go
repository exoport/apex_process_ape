package apexv1

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Take-in (v1.5 stage 1a, proposal section 5.2, OQ18): when main moves,
// each open milestone takes it in through its own queue — a PR of the
// merge of main into milestone/<M>, merged, never rebased (a rebase would
// rewrite the protected branch). It is a change of its milestone,
// take-in-<n>: a conflict takes the shipped path, change.conflicted, with
// the markers left on change/take-in-<n> for apex-apply.

// TakeInPrefix starts a take-in's id.
const TakeInPrefix = "take-in-"

// KindTakeIn marks an Integration that is a take-in.
const KindTakeIn = "take-in"

// TakeInResult is what `ape milestone take-in` reports.
type TakeInResult struct {
	Milestone string `json:"milestone"`
	Branch    string `json:"branch"`
	// UpToDate: main is already in milestone/<M>; nothing was opened.
	UpToDate bool `json:"up_to_date"`
	// Main is the main tip taken in.
	Main        string       `json:"main,omitempty"`
	ID          string       `json:"id,omitempty"`
	Integration *Integration `json:"integration,omitempty"`
}

// takeInRecord is ms's open take-in: queued, or ejected and to be redone.
func (p *Project) takeInRecord(base string) *Integration {
	all, err := p.loadIntegrations()
	if err != nil {
		return nil
	}
	for k := range all {
		in := all[k]
		if in.Kind == KindTakeIn && in.Base == base && (in.State == IntegrationQueued || in.State == IntegrationEjected) {
			return &in
		}
	}
	return nil
}

// nextTakeInID is take-in-<n>, n one past every take-in this checkout
// recorded.
func (p *Project) nextTakeInID() string {
	all, _ := p.loadIntegrations()
	n := 0
	for id := range all {
		if v, err := strconv.Atoi(strings.TrimPrefix(id, TakeInPrefix)); err == nil && strings.HasPrefix(id, TakeInPrefix) && v > n {
			n = v
		}
	}
	return TakeInPrefix + strconv.Itoa(n+1)
}

// TakeIn takes main into milestone/<M>: fetched both, it opens (or waits
// on) the take-in's PR. Main already in the milestone: up to date. A
// queued take-in is read: merged, the local milestone/<M> moves to the
// merged tip; still queued, it says so; ejected, it is redone, an attempt
// (limits.attempts blocks it: integration.ejections-exceeded). A merge that
// conflicts is laid out on change/take-in-<n> (change.conflicted, exit 3);
// once apex-apply committed the resolution there, TakeIn again pushes it.
func (p *Project) TakeIn(ctx context.Context, ms string) (*TakeInResult, error) {
	base := p.MilestoneBase(ms)
	if base == "" {
		return nil, &UsageError{Code: "usage.not-coordinated", Msg: "take-in is for a project integrating through GitHub (coordination.github): its milestones live on milestone/<M> branches; elsewhere main is the only trunk"}
	}
	res := &TakeInResult{Milestone: ms, Branch: base}
	remote := p.trunkRemote(ctx)
	if err := p.fetchUpstreams(ctx, base, "take-in of "+ms); err != nil {
		return res, err
	}
	p.syncCoordinatedBase(ctx, base)
	mainTip, err := git(ctx, p.Root, "rev-parse", "refs/remotes/"+remote+"/main")
	if err != nil {
		return res, &FailError{Code: CodeFetchFailed, Msg: "take-in of " + ms + " needs main's upstream on " + remote + ": " + err.Error()}
	}
	baseTip, err := git(ctx, p.Root, "rev-parse", "refs/remotes/"+remote+"/"+base)
	if err != nil {
		return res, &FailError{Code: CodeMilestoneBranchMissing, Msg: base + " is not on " + remote + " — `ape milestone claim " + ms + "` creates it"}
	}
	res.Main = mainTip

	attempts := 1
	id := ""
	if rec := p.takeInRecord(base); rec != nil {
		done, err := p.resolveTakeIn(ctx, rec, res)
		if done || err != nil {
			return res, err
		}
		id, attempts = rec.Change, rec.Attempts+1
	}
	if _, err := git(ctx, p.Root, "merge-base", "--is-ancestor", mainTip, baseTip); err == nil {
		res.UpToDate = true
		return res, nil
	}
	if id == "" {
		id = p.nextTakeInID()
	}
	res.ID = id
	merge, err := p.takeInMerge(ctx, id, base, baseTip, mainTip)
	if err != nil {
		return res, err
	}
	head := PRBranchPrefix + id
	if _, err := gitWithEnv(ctx, p.Root, noPromptEnv, "push", "--quiet", "--force", "--no-follow-tags", remote, merge+":refs/heads/"+head); err != nil {
		return res, &FailError{Code: CodeIntegrationPush, Action: ActionRetry, Msg: fmt.Sprintf("pushing take-in %s to %s on %s failed: %v — run the take-in again", id, head, remote, err)}
	}
	api, err := p.integrator()
	if err != nil {
		return res, err
	}
	pr, err := api.OpenPR(ctx, head, base, "Take in main ("+short(mainTip)+") into "+base, "Take-in: "+id+"\n\nmain merged into "+base+" (APEX v1.5, section 5.2): merged, never rebased.")
	if err != nil {
		return res, &FailError{Code: CodeIntegrationPR, Action: ActionRetry, Msg: fmt.Sprintf("pushed %s, but opening its pull request into %s failed: %v — run the take-in again", head, base, err)}
	}
	if _, err := api.Enqueue(ctx, pr); err != nil {
		return res, &FailError{Code: CodeIntegrationPR, Action: ActionRetry, Msg: fmt.Sprintf("opened %s, but enqueuing it failed: %v — run the take-in again", pr.URL, err)}
	}
	rec := Integration{Change: id, Kind: KindTakeIn, Head: head, Base: base, Commit: merge, BaseSHA: baseTip, PR: pr, Attempts: attempts, State: IntegrationQueued}
	if err := p.saveIntegration(rec); err != nil {
		return res, err
	}
	res.Integration = &rec
	return res, nil
}

// takeInMerge is the take-in's merge commit, main into the milestone:
// apex-apply's resolution when change/<id> holds one (a merge of mainTip
// onto the milestone, committed), else made without a checkout
// (git merge-tree). A conflict is laid out on change/<id>: the markers in
// the tree, HEAD on the branch.
func (p *Project) takeInMerge(ctx context.Context, id, base, baseTip, mainTip string) (string, error) {
	branch := ChangeBranch(id)
	if tip, err := git(ctx, p.Root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		if _, err := git(ctx, p.Root, "rev-parse", "--verify", "--quiet", "MERGE_HEAD"); err == nil && CurrentBranch(ctx, p.Root) == branch {
			return "", &ConflictError{Code: CodeConflicted, Action: ActionApply, Msg: "take-in " + id + " is still being resolved on " + branch + " — resolve the markers, `git add` and `git commit --no-edit`, then run the take-in again"}
		}
		parents, _ := git(ctx, p.Root, "rev-list", "--parents", "-n", "1", tip)
		if f := strings.Fields(parents); len(f) == 3 && f[2] == mainTip {
			if _, err := git(ctx, p.Root, "merge-base", "--is-ancestor", baseTip, tip); err == nil {
				return tip, nil
			}
		}
	}
	out, err := gitRaw(ctx, p.Root, "merge-tree", "--write-tree", "--name-only", "--no-messages", baseTip, mainTip)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if err == nil && len(lines) > 0 && lines[0] != "" {
		msg := "apex: " + id + " take in main into " + base + "\n\nTake-in: " + id + "\n"
		return git(ctx, p.Root, "commit-tree", lines[0], "-p", baseTip, "-p", mainTip, "-m", msg)
	}
	conflicts := lines[1:]
	if err := p.requireCleanTree(ctx, "take-in "+id); err != nil {
		return "", err
	}
	if _, err := git(ctx, p.Root, "switch", "-q", "-C", branch, baseTip); err != nil {
		return "", &FailError{Code: CodeBranchFailed, Msg: "cannot create " + branch + ": " + err.Error()}
	}
	_, _ = git(ctx, p.Root, "merge", "--no-ff", "--no-edit", "-q", mainTip)
	return "", &ConflictError{Code: CodeConflicted, Action: ActionApply, Msg: fmt.Sprintf(
		"main does not merge cleanly into %s: take-in %s is laid out on %s with the conflict markers in %s — apex-apply resolves them, `git add` and `git commit --no-edit`, then run the take-in again",
		base, id, branch, strings.Join(limit(conflicts, 8), ", "))}
}

// resolveTakeIn reads a recorded take-in's PR: done when it merged (the
// local milestone/<M> brought to the merged tip, its change branch gone)
// or is still queued; an ejected one is to be redone (done false), unless
// it reached limits.attempts.
func (p *Project) resolveTakeIn(ctx context.Context, rec *Integration, res *TakeInResult) (done bool, err error) {
	res.ID, res.Integration = rec.Change, rec
	if rec.State == IntegrationQueued {
		if done, err := p.readQueuedTakeIn(ctx, rec); done || err != nil {
			return done, err
		}
	}
	if capN := p.limits().AttemptsCap(); rec.Attempts >= capN {
		return true, &FailError{Code: CodeIntegrationEjections, Msg: fmt.Sprintf(
			"take-in %s was ejected %d time(s) from %s's merge queue (%s), reaching limits.attempts (%d) — a person looks at why main does not merge into the milestone",
			rec.Change, rec.Attempts, rec.Base, rec.PR.URL, capN)}
	}
	res.Integration = nil
	return false, nil
}

// readQueuedTakeIn reads a queued take-in's PR: done when it merged (the
// local milestone/<M> brought to the merged tip, change/take-in-<n> gone)
// or waits in the queue; else it is recorded ejected.
func (p *Project) readQueuedTakeIn(ctx context.Context, rec *Integration) (done bool, err error) {
	api, err := p.integrator()
	if err != nil {
		return true, err
	}
	s, err := api.PRState(ctx, rec.PR.Number)
	if err != nil {
		return true, &FailError{Code: CodeIntegrationPR, Action: ActionRetry, Msg: fmt.Sprintf("reading %s failed: %v — run the take-in again", rec.PR.URL, err)}
	}
	switch {
	case s.Merged:
		rec.State = IntegrationMerged
		if err := p.saveIntegration(*rec); err != nil {
			return true, err
		}
		if err := p.fetchUpstreams(ctx, rec.Base, "take-in "+rec.Change); err != nil {
			return true, err
		}
		p.syncCoordinatedBase(ctx, rec.Base)
		if CurrentBranch(ctx, p.Root) == ChangeBranch(rec.Change) {
			_ = p.switchBranch(ctx, rec.Base)
		}
		_, _ = git(ctx, p.Root, "branch", "-q", "-D", ChangeBranch(rec.Change))
		return true, nil
	case s.Queue != "" && !s.Closed:
		return true, nil
	}
	rec.State = IntegrationEjected
	return false, p.saveIntegration(*rec)
}

// TakeInUntilMerged is TakeIn, waiting while its PR is queued as
// ArchiveUntilMerged waits.
func (p *Project) TakeInUntilMerged(ctx context.Context, ms string, progress func(string), wake <-chan struct{}) (*TakeInResult, error) {
	for {
		res, err := p.TakeIn(ctx, ms)
		if err != nil || res.UpToDate || res.Integration == nil || res.Integration.State != IntegrationQueued {
			return res, err
		}
		if progress != nil {
			progress(fmt.Sprintf("queued: %s (attempt %d) in %s's merge queue; waiting for merged or ejected", res.Integration.PR.URL, res.Integration.Attempts, res.Branch))
		}
		t := time.NewTimer(QueuePoll)
		select {
		case <-ctx.Done():
			t.Stop()
			return res, nil
		case <-wake:
		case <-t.C:
		}
		t.Stop()
	}
}
