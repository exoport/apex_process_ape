package apexv1

import (
	"context"
	"fmt"
)

// The close PR (v1.5 stage 1a, proposal sections 5.3 and 7): in a project
// integrating through GitHub, a milestone closes into main through one PR
// of milestone/<M>, enqueued in main's queue, whose required check is the
// milestone gate on the merge with the current main (`ape ci check`, in CI).
// The close record, `apex: <M> done`, is the last commit on milestone/<M>,
// so it lands on main with the PR; main's queue merges with a merge
// commit, the release point (OQ17, OQ20).

// KindClose marks an Integration that is a milestone's close PR.
const KindClose = "close"

// CodeTakeInNeeded: main moved since milestone/<M> last took it in; a
// close (and a close PR redone after an ejection) needs it taken in.
const CodeTakeInNeeded = "milestone.take-in-needed"

func init() {
	DiagnosticCodes = append(DiagnosticCodes,
		CodeDoc{CodeTakeInNeeded, 1, ActionRetry, "a coordinated close (coordination.github): main moved again since the take-in the close made itself, so milestone/<M> still lacks main's tip and its gate would run on a tree main will not have — close again, which takes it in first (v1.5 stage 1a, section 5.2; D702)"},
	)
}

func closeID(ms string) string { return "close-" + ms }

// requireTakenIn refuses a close whose milestone lacks main's tip.
func (p *Project) requireTakenIn(ctx context.Context, ms string) error {
	base, remote := MilestoneBranch(ms), p.trunkRemote(ctx)
	if _, err := git(ctx, p.Root, "merge-base", "--is-ancestor", "refs/remotes/"+remote+"/main", "refs/heads/"+base); err != nil {
		return &FailError{Code: CodeTakeInNeeded, Action: ActionRetry, Msg: fmt.Sprintf("main moved since %s last took it in — close %s again, which takes it in first", base, ms)}
	}
	return nil
}

// prepareClose is a coordinated close's preamble: on milestone/<M>,
// fetched and synced, with main taken in — by the close itself when main
// moved (TakeIn, landed on milestone/<M>): its conflict or red regression
// is the close's refusal, laid out for apex-apply.
func (p *Project) prepareClose(ctx context.Context, ms string, opt RunOptions, progress func(string)) error {
	if err := p.enterMilestoneBase(ctx, ms, "close "+ms); err != nil {
		return err
	}
	if err := p.refuseBehindUpstream(ctx, MilestoneBranch(ms), "close "+ms); err != nil {
		return err
	}
	if p.requireTakenIn(ctx, ms) == nil {
		return nil
	}
	if progress != nil {
		progress("main moved since " + MilestoneBranch(ms) + " took it in: taking it in first")
	}
	if _, err := p.TakeIn(ctx, ms, opt); err != nil {
		return err
	}
	if err := p.switchBranch(ctx, MilestoneBranch(ms)); err != nil {
		return err
	}
	return p.requireTakenIn(ctx, ms)
}

// openClosePR lands milestone/<M>'s tip, close record last, on the
// remote's milestone/<M> (the fenced fast-forward push, D702) and opens
// and enqueues the PR milestone/<M> → main.
func (p *Project) openClosePR(ctx context.Context, ms string, attempts int) (*Integration, error) {
	base, remote := MilestoneBranch(ms), p.trunkRemote(ctx)
	tip, err := git(ctx, p.Root, "rev-parse", "refs/heads/"+base)
	if err != nil {
		return nil, err
	}
	head := base
	if err := p.pushMilestone(ctx, base, tip); err != nil {
		return nil, err
	}
	api, err := p.integrator()
	if err != nil {
		return nil, err
	}
	title := "Close " + ms
	if rm := p.roadmap(); rm != nil {
		if m := rm.Find(ms); m != nil && m.Title != "" {
			title += ": " + m.Title
		}
	}
	pr, err := api.OpenPR(ctx, head, "main", title, "Milestone: "+ms+"\n\nThe close PR of "+base+" (APEX v1.5, section 5.3): its required check is the milestone gate on the merge with the current main; it merges with a merge commit, the release point.")
	if err != nil {
		return nil, &FailError{Code: CodeIntegrationPR, Action: ActionRetry, Msg: fmt.Sprintf("pushed %s, but opening its pull request into main failed: %v — close again", head, err)}
	}
	if _, err := api.Enqueue(ctx, pr); err != nil {
		return nil, &FailError{Code: CodeIntegrationPR, Action: ActionRetry, Msg: fmt.Sprintf("opened %s, but enqueuing it in main's merge queue failed: %v — close again", pr.URL, err)}
	}
	baseSHA, _ := git(ctx, p.Root, "rev-parse", "refs/remotes/"+remote+"/"+base)
	rec := Integration{Change: closeID(ms), Kind: KindClose, Head: head, Base: "main", Commit: tip, BaseSHA: baseSHA, PR: pr, Attempts: attempts, State: IntegrationQueued}
	if err := p.saveIntegration(rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// resolveClose reads a recorded close PR: done when it merged (main
// fetched and fast-forwarded, milestone/<M> deleted here and on the
// remote, milestone-done emitted with the release point) or is queued;
// ejected, it is pushed and enqueued again — main taken in first — an
// attempt, up to limits.attempts. done false: no close PR recorded.
func (p *Project) resolveClose(ctx context.Context, ms string, opt RunOptions, progress func(string)) (*GateResult, bool, error) {
	rec := p.LoadIntegration(closeID(ms))
	if rec == nil || rec.Kind != KindClose || !p.coordinated() {
		return nil, false, nil
	}
	g := &GateResult{Milestone: ms, Status: MilestoneDone, Integration: rec}
	if rec.State == IntegrationMerged {
		return g, true, nil
	}
	if rec.State == IntegrationQueued {
		api, err := p.integrator()
		if err != nil {
			return g, true, err
		}
		s, err := api.PRState(ctx, rec.PR.Number)
		if err != nil {
			return g, true, &FailError{Code: CodeIntegrationPR, Action: ActionRetry, Msg: fmt.Sprintf("reading %s failed: %v — close again", rec.PR.URL, err)}
		}
		switch {
		case s.Merged:
			return g, true, p.finishClose(ctx, ms, rec, s, g)
		case s.Queue != "" && !s.Closed:
			return g, true, nil
		}
		rec.State = IntegrationEjected
		if err := p.saveIntegration(*rec); err != nil {
			return g, true, err
		}
	}
	if capN := p.limits().AttemptsCap(); rec.Attempts >= capN {
		return g, true, &FailError{Code: CodeIntegrationEjections, Msg: fmt.Sprintf(
			"the close PR of %s was ejected %d time(s) from main's merge queue (%s), reaching limits.attempts (%d) — a person looks at why its gate is red on the merge with main",
			ms, rec.Attempts, rec.PR.URL, capN)}
	}
	if err := p.prepareClose(ctx, ms, opt, progress); err != nil {
		return g, true, err
	}
	in, err := p.openClosePR(ctx, ms, rec.Attempts+1)
	g.Integration = in
	return g, true, err
}

// checkoutMain switches to main, created from the remote's when this
// checkout has none; the tree must be clean.
func (p *Project) checkoutMain(ctx context.Context, remote, what string) error {
	if CurrentBranch(ctx, p.Root) == "main" {
		return nil
	}
	if err := p.requireCleanTree(ctx, what); err != nil {
		return err
	}
	if p.branchExists(ctx, "main") {
		return p.switchBranch(ctx, "main")
	}
	if _, err := git(ctx, p.Root, "switch", "-q", "--track", "-c", "main", remote+"/main"); err != nil {
		return &FailError{Code: CodeBranchFailed, Msg: "cannot check out main: " + err.Error()}
	}
	return nil
}

// finishClose ends a milestone whose close PR merged.
func (p *Project) finishClose(ctx context.Context, ms string, rec *Integration, s PRState, g *GateResult) error {
	base, remote := MilestoneBranch(ms), p.trunkRemote(ctx)
	if err := p.fetchCoordinated(ctx, "main"); err != nil {
		return &FailError{Code: CodeFetchFailed, Msg: fmt.Sprintf("%s merged, but fetching main from %s failed: %v — close again once the remote answers", rec.PR.URL, remote, err)}
	}
	if err := p.checkoutMain(ctx, remote, "close "+ms); err != nil {
		return err
	}
	if _, err := git(ctx, p.Root, "merge", "--ff-only", "-q", "refs/remotes/"+remote+"/main"); err != nil {
		return &FailError{Code: CodeBranchFailed, Msg: "the close merged, but main does not fast-forward to " + remote + "/main: " + err.Error() + " — local commits on main are outside the flow; move them, then close again"}
	}
	_, _ = git(ctx, p.Root, "branch", "-q", "-D", base)
	if target, env, _, err := p.pushTarget(ctx); err == nil {
		_, _ = gitWithEnv(ctx, p.Root, env, "push", "--quiet", "--no-follow-tags", target, "--delete", base)
	}
	rec.State = IntegrationMerged
	if err := p.saveIntegration(*rec); err != nil {
		return err
	}
	g.Commit = s.MergeCommit
	p.Emit(ctx, Event{Event: EventMilestoneDone, Milestone: ms, Reason: fmt.Sprintf(
		"close PR %s merged into main as %s: the release point (OQ20) — apex-release proposes the version, a person tags it", rec.PR.URL, short(s.MergeCommit))}, nil)
	return nil
}
