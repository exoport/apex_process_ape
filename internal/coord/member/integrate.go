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
func Integrator(p *apexv1.Project) (apexv1.Integrator, error) {
	m, err := Load(p)
	if err != nil {
		return nil, &apexv1.FailError{Code: apexv1.CodeCoordMembership, Msg: err.Error() +
			" — a person fixes _apex/config.yaml's coordination: block or places the token"}
	}
	tok, err := m.GitHubTokenFor(context.Background(), "pr")
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
	return fmt.Sprintf("#%d %s %s", n, u, state), nil
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

// Enqueue enqueues the PR, waiting out GitHub's mergeability computation
// on a PR just opened or pushed (it refuses an enqueue until then, seen on
// the mirror for PRs #1 and #2): retried every enqueueRetryEvery, at most
// enqueueRetries times; any other refusal is returned at once.
func (g ghIntegrator) Enqueue(ctx context.Context, pr apexv1.PRRef) (string, error) {
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
	return apexv1.PRState{Merged: s.Merged, Closed: s.Closed, Queue: s.Queue, MergeCommit: s.MergeCommit}, nil
}
