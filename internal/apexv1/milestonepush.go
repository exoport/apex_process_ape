package apexv1

import (
	"context"
	"errors"
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

// FenceClose, when set, refuses unless this close holds close.<project>
// now (member.FenceClose, FINDING-V15-1A-11): checked right before the
// close PR is enqueued. It returns how many times the lease was taken again
// after lapsing; when that changed since the close last checked main, main
// is checked (and taken in, and gated) again first. Unset, nothing.
var FenceClose func(ctx context.Context, p *Project) (retakes int, err error)

// GitVia, when set, is how ape reaches the coordinated repository for a
// purpose — "push", or "read" for a fetch (PLAN-V15-GHAPP §5): to url, the
// repository over https, with env carrying the App service's installation
// token in http.extraHeader, never argv, a URL or disk. url "" (or GitVia
// unset): main's remote, with git's own credentials.
var GitVia func(ctx context.Context, p *Project, purpose string) (url string, env []string, err error)

// gitTarget is the target and environment for purpose: GitVia's, else the
// trunk's remote; noPromptEnv either way. viaURL reports a URL target,
// after which ape updates remote-tracking refs itself.
func (p *Project) gitTarget(ctx context.Context, purpose string) (target string, env []string, viaURL bool, err error) {
	env = append([]string(nil), noPromptEnv...)
	if GitVia != nil {
		url, extra, err := GitVia(ctx, p, purpose)
		if err != nil {
			return "", nil, false, err
		}
		if url != "" {
			env = append(env, extra...)
			// FINDING-V15-1A-6: the URL git would use must be this one. The
			// person's global and system config are not read (GitVia's
			// env); a rule in the checkout's own config is refused.
			if got, err := gitWithEnv(ctx, p.Root, env, "ls-remote", "--get-url", url); err == nil && got != url {
				return "", nil, false, &FailError{Code: CodeCoordURLRewritten, Action: ActionPerson, Msg: fmt.Sprintf(
					"this checkout's git config rewrites the coordinated repository's URL (url.*.insteadOf in .git/config): %s would go to %s, not where the App's token is for"+
						" — ape refuses rather than reach it as someone else; a person removes the rule (`git config --unset url.<base>.insteadOf`) and runs the command again",
					redactURL(url), redactURL(got))}
			}
			return url, env, true, nil
		}
	}
	return p.trunkRemote(ctx), env, false, nil
}

// coded is err when it is a refusal ape already coded — the App
// project's coord.membership, coord.nats-unreachable or
// coord.url-rewritten (FINDING-V15-1A-6), which name the route — else
// wrap's error.
func coded(err error, wrap func() error) error {
	if fe, ok := errors.AsType[*FailError](err); ok {
		return fe
	}
	return wrap()
}

// redactURL is u for a message: its user (x-access-token) dropped.
func redactURL(u string) string {
	if scheme, rest, ok := strings.Cut(u, "://"); ok {
		if _, host, ok := strings.Cut(rest, "@"); ok {
			return scheme + "://" + host
		}
	}
	return u
}

// pushTarget is gitTarget for a push.
func (p *Project) pushTarget(ctx context.Context) (target string, env []string, viaURL bool, err error) {
	return p.gitTarget(ctx, "push")
}

// fetchCoordinated fetches each branch of the coordinated repository into
// refs/remotes/<trunk remote>/<branch> (forced, as a remote-tracking ref
// is): through GitVia's read token when the project has one, else from
// the trunk's remote.
func (p *Project) fetchCoordinated(ctx context.Context, branches ...string) error {
	target, env, _, err := p.gitTarget(ctx, "read")
	if err != nil {
		return err
	}
	remote := p.trunkRemote(ctx)
	args := []string{"fetch", "--quiet", "--no-tags", target}
	for _, b := range branches {
		args = append(args, "+refs/heads/"+b+":refs/remotes/"+remote+"/"+b)
	}
	_, err = gitWithEnv(ctx, p.Root, env, args...)
	return err
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
	target, env, viaURL, err := p.pushTarget(ctx)
	if err != nil {
		return err
	}
	remote := p.trunkRemote(ctx)
	out, err := gitRawEnv(ctx, p.Root, env, "push", "--quiet", "--porcelain", "--no-follow-tags", target, sha+":refs/heads/"+base)
	if err == nil {
		if viaURL {
			_, _ = git(ctx, p.Root, "update-ref", "refs/remotes/"+remote+"/"+base, sha)
		}
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

// PushHead pushes sha to branch on the coordinated repository through
// pushTarget (the App service's pusher token when the project has one), a
// plain push: never forced. For a branch a person lands through main's
// queue (`ape ghapp land`); milestone/<M> goes through pushMilestone.
func (p *Project) PushHead(ctx context.Context, sha, branch string) error {
	if strings.HasPrefix(branch, MilestoneBranchPrefix) || branch == "main" {
		return &UsageError{Code: "usage.branch", Msg: branch + " is not a head to land: milestone/<M> is ape's (archive, take-in, close) and main takes PRs only"}
	}
	target, env, viaURL, err := p.pushTarget(ctx)
	if err != nil {
		return err
	}
	out, err := gitRawEnv(ctx, p.Root, env, "push", "--quiet", "--porcelain", "--no-follow-tags", target, sha+":refs/heads/"+branch)
	if err != nil {
		return &FailError{Code: CodeIntegrationPush, Action: ActionRetry, Msg: fmt.Sprintf("pushing %s to %s failed: %v", short(sha), branch, strings.TrimSpace(out+" "+err.Error()))}
	}
	if viaURL {
		_, _ = git(ctx, p.Root, "update-ref", "refs/remotes/"+p.trunkRemote(ctx)+"/"+branch, sha)
	}
	return nil
}

// Integrator is the coordinated repository's API (NewIntegrator's), for
// callers outside the archive and close.
func (p *Project) Integrator() (Integrator, error) { return p.integrator() }

// FetchAndSync fetches branches from the coordinated repository (the App's
// read token when the project has one) into their remote-tracking refs,
// and fast-forwards each local branch that is behind only — checked out
// with a clean tree, or not checked out. It returns one line per branch.
func (p *Project) FetchAndSync(ctx context.Context, branches ...string) ([]string, error) {
	if err := p.fetchCoordinated(ctx, branches...); err != nil {
		return nil, coded(err, func() error {
			return &FailError{Code: CodeFetchFailed, Msg: "fetching " + strings.Join(branches, ", ") + " failed: " + err.Error()}
		})
	}
	remote := p.trunkRemote(ctx)
	out := make([]string, 0, len(branches))
	for _, b := range branches {
		tip, _ := git(ctx, p.Root, "rev-parse", "refs/remotes/"+remote+"/"+b)
		local, err := git(ctx, p.Root, "rev-parse", "--verify", "--quiet", "refs/heads/"+b)
		switch {
		case err != nil:
			out = append(out, fmt.Sprintf("%s: %s/%s at %s (no local branch)", b, remote, b, short(tip)))
		case local == tip:
			out = append(out, fmt.Sprintf("%s: up to date at %s", b, short(tip)))
		default:
			if _, err := git(ctx, p.Root, "merge-base", "--is-ancestor", local, tip); err != nil {
				out = append(out, fmt.Sprintf("%s: %s has commits %s/%s lacks: left as it is", b, short(local), remote, b))
				continue
			}
			if CurrentBranch(ctx, p.Root) == b {
				if err := p.requireCleanTree(ctx, "fast-forwarding "+b); err != nil {
					return out, err
				}
				if _, err := git(ctx, p.Root, "merge", "--ff-only", "-q", tip); err != nil {
					return out, &FailError{Code: CodeBranchFailed, Msg: "fast-forwarding " + b + ": " + err.Error()}
				}
			} else if _, err := git(ctx, p.Root, "update-ref", "refs/heads/"+b, tip, local); err != nil {
				return out, &FailError{Code: CodeBranchFailed, Msg: "fast-forwarding " + b + ": " + err.Error()}
			}
			out = append(out, fmt.Sprintf("%s: fast-forwarded %s → %s", b, short(local), short(tip)))
		}
	}
	return out, nil
}
