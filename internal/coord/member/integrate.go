package member

import (
	"context"
	"fmt"

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

func (g ghIntegrator) Enqueue(ctx context.Context, pr apexv1.PRRef) (string, error) {
	return g.c.Enqueue(ctx, &github.PR{Number: pr.Number, NodeID: pr.NodeID, URL: pr.URL})
}

func (g ghIntegrator) PRState(ctx context.Context, n int) (apexv1.PRState, error) {
	s, err := g.c.PRState(ctx, n)
	if err != nil {
		return apexv1.PRState{}, err
	}
	return apexv1.PRState{Merged: s.Merged, Closed: s.Closed, Queue: s.Queue, MergeCommit: s.MergeCommit}, nil
}
