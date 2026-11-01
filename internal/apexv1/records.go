package apexv1

import (
	"context"
	"fmt"
	"strings"
)

// Record PRs (v1.5 stage 1a, proposal section 5.4): in a project
// integrating through GitHub, the roadmap records others must see — a
// milestone proposed, approved, claimed — reach main as small PRs
// through main's merge queue (their check is the project check, CI's).
// main is protected: ape never pushes to it.

// KindRecord marks an Integration that is a record PR.
const KindRecord = "record"

// prepareRecordOnMain puts a coordinated project's checkout on main,
// fetched and synced, for a milestone record, and sends any record
// commit of an earlier call that did not reach its PR. Nothing for an
// uncoordinated project.
func (p *Project) prepareRecordOnMain(ctx context.Context, what string) error {
	if !p.coordinated() || !p.isWorkTree(ctx) {
		return nil
	}
	remote := p.trunkRemote(ctx)
	if err := p.checkoutMain(ctx, remote, what); err != nil {
		return err
	}
	if err := p.refuseBehindUpstream(ctx, "main", what); err != nil {
		return err
	}
	return p.sendPendingRecords(ctx)
}

// sendPendingRecords sends main's tip in a record PR when main holds
// commits its remote lacks and no record PR carries that tip yet.
func (p *Project) sendPendingRecords(ctx context.Context) error {
	if !p.coordinated() || !p.isWorkTree(ctx) {
		return nil
	}
	u := p.upstreamOf(ctx, "main")
	if u == nil || u.Ahead == 0 {
		return nil
	}
	tip, err := git(ctx, p.Root, "rev-parse", "refs/heads/main")
	if err != nil {
		return err
	}
	all, err := p.loadIntegrations()
	if err != nil {
		return err
	}
	for k := range all {
		if in := all[k]; in.Kind == KindRecord && in.Commit == tip && in.State != IntegrationEjected {
			return nil
		}
	}
	subject, _ := git(ctx, p.Root, "log", "-1", "--format=%s", tip)
	_, err = p.sendRecord(ctx, tip, subject)
	return err
}

// sendRecord pushes commit to apex/record-<slug> and opens and enqueues
// its PR into main.
func (p *Project) sendRecord(ctx context.Context, commit, subject string) (*Integration, error) {
	remote := p.trunkRemote(ctx)
	slug := recordSlug(subject) + "-" + short(commit)
	id := "record-" + slug
	head := PRBranchPrefix + id
	if _, err := gitWithEnv(ctx, p.Root, noPromptEnv, "push", "--quiet", "--force", "--no-follow-tags", remote, commit+":refs/heads/"+head); err != nil {
		return nil, &FailError{Code: CodeIntegrationPush, Action: ActionRetry, Msg: fmt.Sprintf("the record %q is committed on main, but pushing it to %s on %s failed: %v — the next milestone record sends it, or run the command again", subject, head, remote, err)}
	}
	api, err := p.integrator()
	if err != nil {
		return nil, err
	}
	pr, err := api.OpenPR(ctx, head, "main", subject, "A roadmap record (APEX v1.5, section 5.4): it reaches main through main's merge queue; its check is the project check.")
	if err != nil {
		return nil, &FailError{Code: CodeIntegrationPR, Action: ActionRetry, Msg: fmt.Sprintf("pushed the record %q to %s, but opening its pull request into main failed: %v — run the command again", subject, head, err)}
	}
	if _, err := api.Enqueue(ctx, pr); err != nil {
		return nil, &FailError{Code: CodeIntegrationPR, Action: ActionRetry, Msg: fmt.Sprintf("opened %s, but enqueuing it in main's merge queue failed: %v — run the command again", pr.URL, err)}
	}
	rec := Integration{Change: id, Kind: KindRecord, Head: head, Base: "main", Commit: commit, PR: pr, Attempts: 1, State: IntegrationQueued}
	if err := p.saveIntegration(rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

// recordSlug is a record subject as a branch name: "apex: M2 approved"
// is "M2-approved".
func recordSlug(subject string) string {
	s := strings.TrimPrefix(subject, "apex: ")
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			return r
		}
		return '-'
	}, s)
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.Trim(s, "-")
}

// RecordClaim is the claim record (section 5.4, D566): "apex: <M> claimed
// by <holder>", information for people that decides no lease. It is a
// commit with no change on the remote's main, sent as a record PR; the
// checkout is untouched. Nothing for an uncoordinated project.
func (p *Project) RecordClaim(ctx context.Context, ms, holder string) (*Integration, error) {
	if !p.coordinated() {
		return nil, nil //nolint:nilnil // no record PRs
	}
	remote := p.trunkRemote(ctx)
	if _, err := gitWithEnv(ctx, p.Root, noPromptEnv, "fetch", "--quiet", "--no-tags", remote, "+refs/heads/main:refs/remotes/"+remote+"/main"); err != nil {
		return nil, &FailError{Code: CodeFetchFailed, Msg: fmt.Sprintf("the claim record of %s needs main from %s: %v — claim again", ms, remote, err)}
	}
	mainTip := "refs/remotes/" + remote + "/main"
	subject := "apex: " + ms + " claimed by " + holder
	sha, err := git(ctx, p.Root, "commit-tree", mainTip+"^{tree}", "-p", mainTip, "-m", subject+"\n\nMilestone: "+ms+"\n")
	if err != nil {
		return nil, err
	}
	return p.sendRecord(ctx, sha, subject)
}

// PendingRecord is the newest queued record PR for milestone ms, nil for
// none: what a record verb reports.
func (p *Project) PendingRecord(ms string) *Integration {
	all, err := p.loadIntegrations()
	if err != nil {
		return nil
	}
	var out *Integration
	for k := range all {
		in := all[k]
		if in.Kind == KindRecord && in.State == IntegrationQueued && strings.HasPrefix(in.Change, "record-"+ms+"-") && (out == nil || in.At > out.At) {
			out = &in
		}
	}
	return out
}
