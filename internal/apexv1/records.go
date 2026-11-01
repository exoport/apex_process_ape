package apexv1

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Record PRs (v1.5 stage 1a, proposal section 5.4): in a project
// integrating through GitHub, the roadmap records others must see — a
// milestone proposed, approved, claimed — reach main as small PRs
// through main's merge queue (their check is the project check, CI's).
// main is protected: ape never pushes to it.

// KindRecord marks an Integration that is a record PR.
const KindRecord = "record"

// The record waits' refusals (FINDING-V15-1A-7 b).
const (
	// CodeRecordEjected: a record PR left main's queue for good.
	CodeRecordEjected = "coord.record-ejected"
	// CodeRecordPending: a record PR had not merged when the wait ended.
	CodeRecordPending = "coord.record-pending"
)

func init() {
	DiagnosticCodes = append(DiagnosticCodes,
		CodeDoc{CodeRecordEjected, 1, ActionPerson, "a coordinated project's record command (milestone new, approve, claim, any roadmap record; v1.5 stage 1a, section 5.4) waits for its record PR to merge into main, and the PR was closed, or removed from main's merge queue for a reason other than invalid_merge_commit, or for invalid_merge_commit after limits of 3 enqueues — the message gives GitHub's reason; the record is committed on this checkout's main but not on the remote's: a person reads why on the PR, then runs the record command again (a new record PR) or lands the branch with `ape ghapp land <branch>` (FINDING-V15-1A-7)"},
		CodeDoc{CodeRecordPending, 1, ActionRetry, "a coordinated project's record command waited 20 minutes for its record PR to merge into main and it had not (still queued, or its queue entry never seen — the message says which; an entry never seen can mean the App's token lacks Merge queues: read) — run the command again: it waits for the same PR rather than send another (FINDING-V15-1A-7)"},
	)
}

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
		in := all[k]
		switch {
		case in.Kind != KindRecord || in.Commit != tip:
		case in.State == IntegrationQueued:
			// Sent by an earlier call that did not see it merge: wait for
			// it now (FINDING-V15-1A-7 b).
			api, err := p.integrator()
			if err != nil {
				return err
			}
			return p.awaitRecord(ctx, api, &in)
		case in.State != IntegrationEjected:
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
	target, env, _, err := p.pushTarget(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := gitWithEnv(ctx, p.Root, env, "push", "--quiet", "--force", "--no-follow-tags", target, commit+":refs/heads/"+head); err != nil {
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
	return &rec, p.awaitRecord(ctx, api, &rec)
}

// A record command waits for its PR to merge (FINDING-V15-1A-7 b): up to
// recordWait, looking every recordPoll; an ejection for invalid_merge_commit
// (GitHub's queue built no merge commit, seen when it judges a PR before
// its mergeability is known) is enqueued again, up to recordAttempts
// enqueues in all. Vars so tests can wait fast.
var (
	recordWait     = 20 * time.Minute
	recordPoll     = 10 * time.Second
	recordAttempts = 3
)

// awaitRecord waits for rec's PR: merged (recorded, nil), or a refusal
// that says why — ejected for another reason or too often
// (coord.record-ejected), closed, or still queued at recordWait
// (coord.record-pending, retry: the command waits for it again).
func (p *Project) awaitRecord(ctx context.Context, api Integrator, rec *Integration) error {
	deadline := time.Now().Add(recordWait)
	seen := false
	for {
		st, err := api.PRState(ctx, rec.PR.Number)
		if err == nil {
			switch {
			case st.Merged:
				rec.State, rec.Merged = IntegrationMerged, st.MergeCommit
				return p.saveIntegration(*rec)
			case st.Closed:
				rec.State = IntegrationEjected
				_ = p.saveIntegration(*rec)
				return &FailError{Code: CodeRecordEjected, Action: ActionPerson, Msg: fmt.Sprintf("the record %s (%s) was closed without merging — a person reopens it, or runs the record command again to send a new one", rec.PR.URL, rec.Head)}
			case st.Queue != "":
				seen = true
			case st.Removed != "":
				if st.Removed != "invalid_merge_commit" || rec.Attempts >= recordAttempts {
					rec.State = IntegrationEjected
					_ = p.saveIntegration(*rec)
					return &FailError{Code: CodeRecordEjected, Action: ActionPerson, Msg: fmt.Sprintf(
						"the record %s (%s) was removed from main's merge queue (%s) after %d enqueue(s), and has not reached main — a person reads why on the PR, then runs the record command again, or lands it with `ape ghapp land %s`",
						rec.PR.URL, rec.Head, st.Removed, rec.Attempts, rec.Head)}
				}
				if _, err := api.Enqueue(ctx, rec.PR); err == nil {
					rec.Attempts++
					_ = p.saveIntegration(*rec)
				}
			}
		}
		if time.Now().After(deadline) {
			why := "it is still in main's merge queue"
			if !seen {
				why = "its merge-queue entry was never seen (the App's token may lack Merge queues: read)"
			}
			return &FailError{Code: CodeRecordPending, Action: ActionRetry, Msg: fmt.Sprintf(
				"the record %s (%s) has not merged into main after %s: %s — run the command again, which waits for this PR rather than send another",
				rec.PR.URL, rec.Head, recordWait, why)}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(recordPoll):
		}
	}
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
	if err := p.fetchCoordinated(ctx, "main"); err != nil {
		return nil, coded(err, func() error {
			return &FailError{Code: CodeFetchFailed, Msg: fmt.Sprintf("the claim record of %s needs main from %s: %v — claim again", ms, remote, err)}
		})
	}
	mainTip := "refs/remotes/" + remote + "/main"
	subject := "apex: " + ms + " claimed by " + holder
	sha, err := git(ctx, p.Root, "commit-tree", mainTip+"^{tree}", "-p", mainTip, "-m", subject+"\n\nMilestone: "+ms+"\n")
	if err != nil {
		return nil, err
	}
	return p.sendRecord(ctx, sha, subject)
}

// LastRecord is the newest record PR for milestone ms, any state, nil for
// none: what a record verb reports (merged, since it waits).
func (p *Project) LastRecord(ms string) *Integration {
	all, err := p.loadIntegrations()
	if err != nil {
		return nil
	}
	var out *Integration
	for k := range all {
		in := all[k]
		if in.Kind == KindRecord && strings.HasPrefix(in.Change, "record-"+ms+"-") && (out == nil || in.At > out.At) {
			out = &in
		}
	}
	return out
}

// PendingRecord is the newest queued record PR for milestone ms, nil for
// none.
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
