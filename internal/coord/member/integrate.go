package member

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/exoport/apex_process_ape/internal/apexv1"
	"github.com/exoport/apex_process_ape/internal/coord/github"
)

// Integrator is apexv1.NewIntegrator for a member project: ape's own
// GitHub client on coordination.github's repository, with the token from
// hot/github.token.
func Integrator(ctx context.Context, p *apexv1.Project) (apexv1.Integrator, error) {
	m, err := Load(p)
	if err != nil {
		return nil, &apexv1.FailError{Code: apexv1.CodeCoordMembership, Msg: err.Error() +
			" — a person fixes _apex/config.yaml's coordination: block or places the token"}
	}
	tok, err := m.GitHubTokenFor(ctx, "pr")
	if err != nil && m.GitHubApp {
		return nil, &apexv1.FailError{Code: apexv1.CodeCoordNATSUnreachable, Action: apexv1.ActionRetry, Msg: "no pr token from the coordination host's App service (" + m.GitHubRepo + "): " + err.Error() +
			" — check that the App service runs and NATS answers at " + m.URL + ", then run the command again"}
	}
	if err != nil {
		return nil, &apexv1.FailError{Code: apexv1.CodeCoordMembership, Msg: err.Error()}
	}
	return ghIntegrator{github.New(m.GitHubAPI, m.GitHubRepo, tok)}, nil
}

type ghIntegrator struct{ c *github.Client }

// RulesFor is the rule types GitHub applies to branch on the coordinated
// repository (a read token).
func RulesFor(ctx context.Context, p *apexv1.Project, branch string) ([]string, error) {
	m, err := Load(p)
	if err != nil {
		return nil, err
	}
	tok, err := m.GitHubTokenFor(ctx, "read")
	if err != nil {
		return nil, err
	}
	return github.New(m.GitHubAPI, m.GitHubRepo, tok).RulesFor(ctx, branch)
}

// PRStatus is the newest PR from head into base: one line.
func PRStatus(ctx context.Context, p *apexv1.Project, head, base string) (string, error) {
	m, err := Load(p)
	if err != nil {
		return "", err
	}
	tok, err := m.GitHubTokenFor(ctx, "read")
	if err != nil {
		return "", err
	}
	n, u, state, merged, err := github.New(m.GitHubAPI, m.GitHubRepo, tok).LatestFor(ctx, head, base)
	switch {
	case err != nil:
		return "", err
	case n == 0:
		return "no pull request from " + head + " into " + base, nil
	case merged != "":
		return fmt.Sprintf("#%d %s merged as %s", n, u, merged), nil
	}
	line := fmt.Sprintf("#%d %s %s", n, u, state)
	if state != "open" {
		return line, nil
	}
	// An open PR: its place in main's queue and how it got there or left
	// (FINDING-V15-1A-7); a read that fails says so, never "not queued".
	entry, ms, events, err := github.New(m.GitHubAPI, m.GitHubRepo, tok).QueueHistory(ctx, n)
	if err != nil {
		return line + "; its merge-queue entry could not be read (" + err.Error() + "): not judged", nil //nolint:nilerr // the PR's own state stands; the queue's is said to be unread
	}
	if entry == "" {
		entry = "not in the queue"
	}
	var b strings.Builder
	b.WriteString(line + "; queue: " + entry + "; mergeable state: " + ms)
	for _, e := range events {
		b.WriteString("; " + e.Kind + " " + e.At)
		if e.Actor != "" {
			b.WriteString(" by " + e.Actor)
		}
		if e.Reason != "" {
			b.WriteString(": " + e.Reason)
		}
	}
	return b.String(), nil
}

// Dequeue takes the open PR from head into base out of the merge queue.
func Dequeue(ctx context.Context, p *apexv1.Project, head, base string) (string, error) {
	m, err := Load(p)
	if err != nil {
		return "", err
	}
	tok, err := m.GitHubTokenFor(ctx, "pr")
	if err != nil {
		return "", err
	}
	c := github.New(m.GitHubAPI, m.GitHubRepo, tok)
	pr, err := c.FindOpen(ctx, head, base)
	if err != nil {
		return "", err
	}
	if pr == nil {
		return "", fmt.Errorf("no open pull request from %s into %s", head, base)
	}
	return pr.URL, c.Dequeue(ctx, pr)
}

func (g ghIntegrator) OpenPR(ctx context.Context, head, base, title, body string) (apexv1.PRRef, error) {
	pr, err := g.c.OpenPR(ctx, head, base, title, body)
	if err != nil {
		return apexv1.PRRef{}, err
	}
	return apexv1.PRRef{Number: pr.Number, NodeID: pr.NodeID, URL: pr.URL}, nil
}

// Enqueue enqueues the PR once GitHub knows its mergeability
// (FINDING-V15-1A-7 a): enqueued while it is UNKNOWN, the queue may build
// no merge commit and eject it at once (invalid_merge_commit, the mirror's
// PR #9). It looks every enqueueRetryEvery, at most enqueueRetries times,
// then enqueues all the same; GitHub's refusal of an enqueue before then
// (PRs #1 and #2) is retried the same way, and any other refusal returned
// at once.
func (g ghIntegrator) Enqueue(ctx context.Context, pr apexv1.PRRef) (string, error) {
	for range enqueueRetries {
		_, ms, _, err := g.c.QueueHistory(ctx, pr.Number)
		if err != nil || ms != "UNKNOWN" {
			break
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(enqueueRetryEvery):
		}
	}
	for i := 0; ; i++ {
		st, err := g.c.Enqueue(ctx, &github.PR{Number: pr.Number, NodeID: pr.NodeID, URL: pr.URL})
		if err == nil || i == enqueueRetries || !strings.Contains(err.Error(), "has not yet completed") {
			return st, err
		}
		select {
		case <-ctx.Done():
			return st, err
		case <-time.After(enqueueRetryEvery):
		}
	}
}

// Enqueue's wait for GitHub's mergeability check: a minute at most.
var (
	enqueueRetries    = 12
	enqueueRetryEvery = 5 * time.Second
)

func (g ghIntegrator) PRState(ctx context.Context, n int) (apexv1.PRState, error) {
	s, err := g.c.PRState(ctx, n)
	if err != nil {
		return apexv1.PRState{}, err
	}
	out := apexv1.PRState{Merged: s.Merged, Closed: s.Closed, Queue: s.Queue, MergeCommit: s.MergeCommit}
	if !s.Merged && !s.Closed && s.Queue == "" {
		out.Removed = g.removedReason(ctx, n)
	}
	return out, nil
}

// removedReason is why an open PR out of the queue left it, from its
// history (FINDING-V15-1A-7 b): its last queue event's reason when that
// event removed it, else "" (never queued, re-added, or unreadable).
func (g ghIntegrator) removedReason(ctx context.Context, n int) string {
	_, _, events, err := g.c.QueueHistory(ctx, n)
	if err != nil || len(events) == 0 || events[len(events)-1].Kind != "removed" {
		return ""
	}
	if r := events[len(events)-1].Reason; r != "" {
		return r
	}
	return "no reason given"
}
