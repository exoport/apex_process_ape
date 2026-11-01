package apexv1

import (
	"context"
	"fmt"
	"strings"
)

// ape-handled milestone branches (D702): in a project integrating through
// GitHub, ape lands a change's squash and a take-in's merge on
// milestone/<M> itself, with one push; GitHub's merge queue is main's
// only. The single writer is the milestone's lease holder: before each
// push the lease is fenced (FenceMilestone), and the push is a plain
// fast-forward, which the remote refuses when the branch moved since the
// fetch — so a second writer's push fails rather than lands.

// CodeMilestoneMoved: milestone/<M> moved on the remote since ape fetched
// it, so the push (a fast-forward only) was refused and nothing landed.
const CodeMilestoneMoved = "milestone.moved"

func init() {
	DiagnosticCodes = append(DiagnosticCodes,
		CodeDoc{CodeMilestoneMoved, 1, ActionRetry, "a coordinated archive or take-in (coordination.github): milestone/<M> moved on the remote between ape's fetch and its push — a push only fast-forwards, so it was refused and nothing landed (the local branch is back where it was); run the command again, which fetches and catches up first (D702: one writer per milestone branch, so this means another writer, or a lost lease)"},
	)
}

// FenceMilestone, when set, refuses unless this checkout holds milestone
// ms's lease now (member.FenceMilestone): checked right before every push
// of milestone/<M>. Unset (a project in no ensemble, tests), nothing is
// checked.
var FenceMilestone func(ctx context.Context, p *Project, ms string) error

// PushEnv, when set, is the environment a push to the coordinated
// repository runs with (the GitHub App service's installation token in
// http.extraHeader, never argv; ghapp). Unset, git's own credentials push.
var PushEnv func(ctx context.Context, p *Project) ([]string, error)

// pushEnv is the push's environment: noPromptEnv, plus PushEnv's.
func (p *Project) pushEnv(ctx context.Context) ([]string, error) {
	env := append([]string(nil), noPromptEnv...)
	if PushEnv == nil {
		return env, nil
	}
	extra, err := PushEnv(ctx, p)
	if err != nil {
		return nil, err
	}
	return append(env, extra...), nil
}

// pushMilestone lands sha on the remote's milestone branch base: the lease
// fenced, then a fast-forward push (never forced). A refusal because the
// branch moved is CodeMilestoneMoved; any other failure CodeIntegrationPush.
func (p *Project) pushMilestone(ctx context.Context, base, sha string) error {
	ms := strings.TrimPrefix(base, MilestoneBranchPrefix)
	if FenceMilestone != nil {
		if err := FenceMilestone(ctx, p, ms); err != nil {
			return err
		}
	}
	env, err := p.pushEnv(ctx)
	if err != nil {
		return err
	}
	remote := p.trunkRemote(ctx)
	out, err := gitRawEnv(ctx, p.Root, env, "push", "--quiet", "--porcelain", "--no-follow-tags", remote, sha+":refs/heads/"+base)
	if err == nil {
		return nil
	}
	if moved(out + err.Error()) {
		return &FailError{Code: CodeMilestoneMoved, Action: ActionRetry, Msg: fmt.Sprintf("%s moved on %s since it was fetched, so the push of %s (a fast-forward only) was refused and nothing landed — run the command again, which fetches and catches up first", base, remote, short(sha))}
	}
	return &FailError{Code: CodeIntegrationPush, Action: ActionRetry, Msg: fmt.Sprintf("pushing %s to %s on %s failed: %v — fix the remote or its credentials, then run the command again", short(sha), base, remote, strings.TrimSpace(out+" "+err.Error()))}
}

// moved reports git push's refusal of a non-fast-forward.
func moved(s string) bool {
	for _, w := range []string{"non-fast-forward", "fetch first", "[rejected]", "stale info"} {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}
