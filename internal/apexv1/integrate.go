package apexv1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/exoport/apex_process_ape/internal/atomicfile"
	"github.com/exoport/apex_process_ape/internal/runlog"
)

// Coordinated integration (v1.5 stage 1a, proposal section 5.1): in a
// project with coordination.github, archive does not land the squash on
// the base branch itself. It pushes the squash commit to apex/<id>, opens
// a pull request into milestone/<M>, and enqueues it; the merge queue's
// check merges it, or ejects it.

// PRBranchPrefix starts the branch a change's squash is pushed to.
const PRBranchPrefix = "apex/"

// Integration codes.
const (
	// CodeIntegrationPush: pushing the squash to apex/<id> failed.
	CodeIntegrationPush = "integration.push-failed"
	// CodeIntegrationPR: opening or enqueuing the pull request failed.
	CodeIntegrationPR = "integration.pr-failed"
	// CodeIntegrationEjections: the queue ejected the change's PR as many
	// times as limits.attempts allows; the change is blocked.
	CodeIntegrationEjections = "integration.ejections-exceeded"
)

func init() {
	DiagnosticCodes = append(DiagnosticCodes,
		CodeDoc{CodeIntegrationPush, 1, ActionRetry, "a coordinated archive (coordination.github) could not push the squash commit to apex/<id> on main's remote: nothing was committed, the base is unchanged and change/<id> is checked out again — fix the remote or its credentials, then archive again (v1.5 stage 1a, section 5.1)"},
		CodeDoc{CodeIntegrationEjections, 1, ActionReplan, "a coordinated archive found its pull request ejected from milestone/<M>'s merge queue (its check red on the combined tree, or a conflict) for the limits.attempts-th time; the change is now blocked — re-plan it, or a person answers what blocks it (v1.5 stage 1a, section 5.1)"},
		CodeDoc{CodeIntegrationPR, 1, ActionRetry, "a coordinated archive pushed apex/<id> but could not open its pull request into milestone/<M>, or enqueue it (GitHub's message is given; a rate limit says when it lifts): the base is unchanged and change/<id> is kept — archive again, which reuses the open pull request"},
	)
}

// PRRef is a pull request as ape records it.
type PRRef struct {
	Number int    `json:"number"`
	NodeID string `json:"node_id"`
	URL    string `json:"url"`
}

// PRState is a pull request's state as archive reads it.
type PRState struct {
	Merged, Closed bool
	// Queue is the merge queue entry's state, "" when not in a queue.
	Queue       string
	MergeCommit string
}

// Integrator is the coordinated repository's API, as archive uses it
// (internal/coord/github behind it).
type Integrator interface {
	OpenPR(ctx context.Context, head, base, title, body string) (PRRef, error)
	Enqueue(ctx context.Context, pr PRRef) (queue string, err error)
	PRState(ctx context.Context, number int) (PRState, error)
}

// NewIntegrator, when set, is the coordinated project's Integrator; ape
// sets it (apecmd), tests set a fake. Unset, a coordinated archive
// refuses: nothing can open its pull request.
var NewIntegrator func(p *Project) (Integrator, error)

// Integration states.
const (
	IntegrationQueued  = "queued"
	IntegrationMerged  = "merged"
	IntegrationEjected = "ejected"
)

// Integration is one change's pull request, in this checkout's own state
// (_apex/state/coord/prs.json): never in git, which the PR itself is.
type Integration struct {
	Change string `json:"change"`
	// Kind is "" for a change's PR, KindTakeIn for a take-in's.
	Kind string `json:"kind,omitempty"`
	// Head is apex/<id>; Base the branch the PR merges into.
	Head string `json:"head"`
	Base string `json:"base"`
	// Commit is the squash pushed to Head; BaseSHA the base tip it sits on.
	Commit  string `json:"commit"`
	BaseSHA string `json:"base_sha"`
	PR      PRRef  `json:"pr"`
	// Attempts counts the enqueues: the first, and one per re-enqueue
	// after an ejection.
	Attempts int    `json:"attempts"`
	State    string `json:"state"`
	At       string `json:"at"`
}

func (p *Project) integrationsPath() string { return runlog.CoordPRsPath(p.Root) }

func (p *Project) loadIntegrations() (map[string]Integration, error) {
	all := map[string]Integration{}
	data, err := os.ReadFile(p.integrationsPath())
	if errors.Is(err, fs.ErrNotExist) {
		return all, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, fmt.Errorf("%s: %w", p.integrationsPath(), err)
	}
	return all, nil
}

// LoadIntegration is id's pull request record, nil when it has none.
func (p *Project) LoadIntegration(id string) *Integration {
	all, err := p.loadIntegrations()
	if err != nil {
		return nil
	}
	if in, ok := all[id]; ok {
		return &in
	}
	return nil
}

func (p *Project) saveIntegration(in Integration) error {
	all, err := p.loadIntegrations()
	if err != nil {
		return err
	}
	in.At = now().UTC().Format("2006-01-02T15:04:05Z")
	all[in.Change] = in
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p.integrationsPath()), 0o755); err != nil {
		return err
	}
	return atomicfile.Write(p.integrationsPath(), append(data, '\n'))
}

// integrator is the coordinated project's Integrator, or the refusal.
func (p *Project) integrator() (Integrator, error) {
	if NewIntegrator == nil {
		return nil, &FailError{Code: CodeIntegrationPR, Msg: "this project integrates through GitHub (coordination.github), and this ape has no GitHub client wired — run the archive with ape itself"}
	}
	return NewIntegrator(p)
}

// integrateSquash is a coordinated archive's last step, with the squash
// commit at HEAD on base: the commit is pushed to apex/<id>, base goes
// back to baseSHA (the change lands there through its PR), and the PR is
// opened and enqueued. The change's branch is kept for an ejection's
// catch-up. undo is the squash's own rollback, for a push that fails.
func (p *Project) integrateSquash(ctx context.Context, ci ChangeInfo, res *ArchiveResult, baseSHA string, attempts int, undo func(error) error) error {
	id, base := ci.ID, ci.State.BaseBranch
	commit := HeadSHA(ctx, p.Root)
	head := PRBranchPrefix + id
	remote := p.trunkRemote(ctx)
	if _, err := gitWithEnv(ctx, p.Root, noPromptEnv, "push", "--quiet", "--force", "--no-follow-tags", remote, commit+":refs/heads/"+head); err != nil {
		return undo(&FailError{Code: CodeIntegrationPush, Action: ActionRetry, Msg: fmt.Sprintf("pushing the squash of %s to %s on %s failed: %v", id, head, remote, err)})
	}
	if _, err := git(ctx, p.Root, "reset", "-q", "--hard", baseSHA); err != nil {
		return &FailError{Code: CodeBranchFailed, Msg: "pushed " + head + ", but resetting " + base + " to " + short(baseSHA) + " failed: " + err.Error() + " — `git reset --hard " + short(baseSHA) + "` on " + base + ", then archive again"}
	}
	res.Commit, res.BranchTip = commit, HeadSHA(ctx, p.Root)
	in, err := p.integrator()
	if err != nil {
		return err
	}
	pr, err := in.OpenPR(ctx, head, base, res.Subject, "Change: "+id+"\n\nOpened by ape change archive (APEX v1.5, coordinated integration).")
	if err != nil {
		return &FailError{Code: CodeIntegrationPR, Action: ActionRetry, Msg: fmt.Sprintf("pushed %s, but opening its pull request into %s failed: %v — archive again", head, base, err)}
	}
	if _, err := in.Enqueue(ctx, pr); err != nil {
		return &FailError{Code: CodeIntegrationPR, Action: ActionRetry, Msg: fmt.Sprintf("opened %s, but enqueuing it in %s's merge queue failed: %v — archive again", pr.URL, base, err)}
	}
	rec := Integration{Change: id, Head: head, Base: base, Commit: commit, BaseSHA: baseSHA, PR: pr, Attempts: attempts, State: IntegrationQueued}
	if err := p.saveIntegration(rec); err != nil {
		return err
	}
	res.Integration = &rec
	return nil
}

// resolveIntegration is a re-run archive of a change whose PR is queued:
// it reads the PR. Still queued: archive redoes nothing (a re-push would
// drop the queue entry), done. Merged: the change is finished locally
// (finishMerged), done. Ejected — out of the queue, not merged — it is
// recorded, and archive goes on (done false) to catch up with the moved
// base and enqueue again, counted as an attempt; the attempt that reaches
// limits.attempts blocks the change instead.
func (p *Project) resolveIntegration(ctx context.Context, id string) (res *ArchiveResult, done bool, err error) {
	in := p.LoadIntegration(id)
	if in == nil || in.Kind != "" || in.State != IntegrationQueued || !p.coordinated() {
		return nil, false, nil
	}
	api, err := p.integrator()
	if err != nil {
		return nil, true, err
	}
	s, err := api.PRState(ctx, in.PR.Number)
	if err != nil {
		return nil, true, &FailError{Code: CodeIntegrationPR, Action: ActionRetry, Msg: fmt.Sprintf("reading %s failed: %v — archive again", in.PR.URL, err)}
	}
	res = &ArchiveResult{ID: id, BaseBranch: in.Base, Commit: in.Commit, Integration: in}
	switch {
	case s.Merged:
		return res, true, p.finishMerged(ctx, in, s, res)
	case s.Queue != "" && !s.Closed:
		return res, true, nil
	}
	in.State = IntegrationEjected
	if err := p.saveIntegration(*in); err != nil {
		return res, true, err
	}
	if capN := p.limits().AttemptsCap(); in.Attempts >= capN {
		ci, err := p.requireChange(ctx, id)
		if err != nil {
			return res, true, err
		}
		reason := fmt.Sprintf("ejected %d time(s) from %s's merge queue (%s)", in.Attempts, in.Base, in.PR.URL)
		if err := p.blockAndCount(ctx, ci, reason); err != nil {
			return res, true, err
		}
		return res, true, &FailError{Code: CodeIntegrationEjections, Msg: fmt.Sprintf(
			"change %s: its pull request was %s, reaching limits.attempts (%d), so it is blocked — re-plan it (change.md, on %s) and start it again, or a person answers what blocks it",
			id, reason, capN, ChangeBranch(id))}
	}
	return nil, false, nil
}

// finishMerged ends a change whose PR merged: the base's remote-tracking
// ref is fetched; the local base, still where the squash was cut, moves to
// the merged tip (a change's records rode the PR, section 5.4); the change
// branch's tip is kept as refs/apex/archive/<id> and the branch deleted;
// the checkout ends on the base.
func (p *Project) finishMerged(ctx context.Context, in *Integration, s PRState, res *ArchiveResult) error {
	base, branch := in.Base, ChangeBranch(in.Change)
	remote := p.trunkRemote(ctx)
	tracking := "refs/remotes/" + remote + "/" + base
	if _, err := gitWithEnv(ctx, p.Root, noPromptEnv, "fetch", "--quiet", "--no-tags", remote, "+refs/heads/"+base+":"+tracking); err != nil {
		return &FailError{Code: CodeFetchFailed, Msg: fmt.Sprintf("%s merged, but fetching %s from %s failed: %v — archive again once the remote answers", in.PR.URL, base, remote, err)}
	}
	tip, err := git(ctx, p.Root, "rev-parse", tracking)
	if err != nil {
		return err
	}
	local, _ := git(ctx, p.Root, "rev-parse", "--verify", "--quiet", "refs/heads/"+base)
	if local != in.BaseSHA && local != tip {
		return &FailError{Code: CodeBranchFailed, Msg: fmt.Sprintf("%s merged, but %s moved locally since the squash was cut on %s (now %s) — move those commits onto %s/%s by hand (`git rebase --onto %s/%s %s %s`), then archive again",
			in.PR.URL, base, short(in.BaseSHA), short(local), remote, base, remote, base, short(in.BaseSHA), base)}
	}
	if CurrentBranch(ctx, p.Root) != base {
		if err := p.requireCleanTree(ctx, "archive "+in.Change); err != nil {
			return err
		}
		if err := p.switchBranch(ctx, base); err != nil {
			return err
		}
	}
	if local != tip {
		if err := p.requireCleanTree(ctx, "archive "+in.Change); err != nil {
			return err
		}
		if _, err := git(ctx, p.Root, "reset", "-q", "--hard", tip); err != nil {
			return &FailError{Code: CodeBranchFailed, Msg: "moving " + base + " to the merged " + short(tip) + " failed: " + err.Error()}
		}
	}
	if btip, err := git(ctx, p.Root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		if err := p.keepTip(ctx, archiveRef(in.Change), btip, "merged as "+short(s.MergeCommit)+" into "+base, branch); err != nil {
			return err
		}
		if _, err := git(ctx, p.Root, "branch", "-q", "-D", branch); err != nil {
			return &FailError{Code: CodeBranchFailed, Msg: "merged into " + base + ", but deleting " + branch + " failed: " + err.Error() + " — `git branch -D " + branch + "`"}
		}
		res.BranchTip, res.TipRef = btip, archiveRef(in.Change)
	}
	in.State = IntegrationMerged
	if err := p.saveIntegration(*in); err != nil {
		return err
	}
	res.Commit = s.MergeCommit
	return nil
}

// QueuePoll is how often a coordinated archive waiting on the queue reads
// its PR when no event woke it (a member's NATS events wake it sooner:
// CI publishes merged and ejected, OQ10).
var QueuePoll = time.Minute

// ArchiveUntilMerged is a coordinated archive that waits for the queue:
// Archive, then, while its PR is queued, Archive again at each wake or
// every QueuePoll, until it merged, was blocked, failed, or ctx ends (the
// queued result then). An ejection is archived again inside the loop (an
// attempt). An uncoordinated archive is Archive alone.
func (p *Project) ArchiveUntilMerged(ctx context.Context, id string, allowPendingHuman bool, opt RunOptions, wake <-chan struct{}) (*ArchiveResult, error) {
	for {
		res, err := p.Archive(ctx, id, allowPendingHuman, opt)
		if err != nil || res == nil || res.Integration == nil || res.Integration.State != IntegrationQueued {
			return res, err
		}
		if opt.Progress != nil {
			fmt.Fprintf(opt.Progress, "queued: %s (attempt %d) in %s's merge queue; waiting for merged or ejected\n", res.Integration.PR.URL, res.Integration.Attempts, res.Integration.Base)
		}
		if !WaitQueue(ctx, wake) {
			return res, nil
		}
	}
}

// WaitQueue waits for the next look at a queued PR: a wake, or QueuePoll.
// false when ctx ended first.
func WaitQueue(ctx context.Context, wake <-chan struct{}) bool {
	t := time.NewTimer(QueuePoll)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-wake:
	case <-t.C:
	}
	return true
}
