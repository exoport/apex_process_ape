package apexv1

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Scope is what v1.0 supports (D45, operator Q1): one operator per
// checkout, on a local trunk. `ape project init` states it. A coordinated
// project (v1.5) fetches its trunk at start, archive and catch-up.
const Scope = "v1.0: one operator per checkout, on a local trunk; PR mode comes later."

// CodeBehindUpstream: the base branch is behind its upstream, and a change
// would be started from, or archived onto, a stale trunk (H6, D45).
const CodeBehindUpstream = "base.behind-upstream"

// CodeFetchFailed: a coordinated project's start, archive or catch-up could
// not fetch the branches it judges staleness by (v1.5, section 5.5).
const CodeFetchFailed = "base.fetch-failed"

// BaseUpstream is a base branch measured against its upstream, as the
// local remote-tracking ref has it: status never fetches; start, archive
// and catch-up fetch first only in a coordinated project (coordinated).
type BaseUpstream struct {
	Branch   string `json:"branch"`
	Upstream string `json:"upstream"`
	// Ahead: commits on Branch the upstream lacks; Behind: the reverse.
	Ahead  int `json:"ahead"`
	Behind int `json:"behind"`
}

// upstreamOf measures branch against its configured upstream; nil when it
// has none, or when the remote-tracking ref does not exist locally.
func (p *Project) upstreamOf(ctx context.Context, branch string) *BaseUpstream {
	if branch == "" || !p.isWorkTree(ctx) {
		return nil
	}
	up, err := git(ctx, p.Root, "rev-parse", "--abbrev-ref", "--symbolic-full-name", branch+"@{upstream}")
	if err != nil || up == "" {
		return nil
	}
	counts, err := git(ctx, p.Root, "rev-list", "--left-right", "--count", "refs/heads/"+branch+"..."+branch+"@{upstream}")
	if err != nil {
		return nil
	}
	f := strings.Fields(counts)
	if len(f) != 2 {
		return nil
	}
	ahead, err1 := strconv.Atoi(f[0])
	behind, err2 := strconv.Atoi(f[1])
	if err1 != nil || err2 != nil {
		return nil
	}
	return &BaseUpstream{Branch: branch, Upstream: up, Ahead: ahead, Behind: behind}
}

// Describe is the upstream in one line, for `ape status`.
func (u *BaseUpstream) Describe() string {
	s := fmt.Sprintf("%s vs %s: %d ahead, %d behind", u.Branch, u.Upstream, u.Ahead, u.Behind)
	if u.Behind > 0 {
		s += " — pull before starting or archiving a change"
	}
	return s + " (the local " + u.Upstream + ", as last fetched)"
}

// noPromptEnv keeps git from asking for credentials: ape's fetches and
// pushes run unattended, and fail instead.
var noPromptEnv = []string{"GIT_TERMINAL_PROMPT=0"}

// coordinated: the project integrates through a coordinated repository
// (coordination.github, v1.5 stage 1a), so other members push to its
// trunk and ape must fetch before judging the base.
func (p *Project) coordinated() bool {
	cfg, err := LoadConfig(p.Root)
	return err == nil && cfg.Coordination != nil && cfg.Coordination.GitHub != nil && strings.TrimSpace(cfg.Coordination.GitHub.Repo) != ""
}

// fetchUpstreams fetches the upstream of base and of main (section 5.5:
// "ape fetches main and its milestone branch"), each from its own
// remote; a branch with no upstream is skipped. Only the remote-tracking
// refs move: no local branch is touched. Never prompts for credentials.
func (p *Project) fetchUpstreams(ctx context.Context, base, what string) error {
	seen := map[string]bool{}
	for _, b := range []string{base, "main"} {
		if b == "" || seen[b] {
			continue
		}
		seen[b] = true
		remote, _ := git(ctx, p.Root, "config", "branch."+b+".remote")
		merge, _ := git(ctx, p.Root, "config", "branch."+b+".merge")
		if remote == "" || merge == "" || remote == "." {
			continue
		}
		if _, err := gitWithEnv(ctx, p.Root, noPromptEnv, "fetch", "--quiet", "--no-tags", remote, merge); err != nil {
			return &FailError{Code: CodeFetchFailed, Msg: fmt.Sprintf("this project is coordinated, and %s needs %s's upstream fetched first: git fetch %s %s failed: %v — fix the remote or its credentials, then run this again; nothing was written",
				what, b, remote, merge, err)}
		}
	}
	return nil
}

// syncCoordinatedBase brings a coordinated project's base up to its
// fetched upstream, which only merges move (section 5.1): behind only, it
// fast-forwards; behind and ahead — this checkout's own unpushed records,
// a planned change's commits, which ride that change's PR (5.4) — it
// replays them onto the upstream tip. A replay that conflicts is aborted
// and leaves the base as it was, for refuseBehindUpstream to refuse. The
// tree must be clean, and the base free of other worktrees.
func (p *Project) syncCoordinatedBase(ctx context.Context, base string) {
	u := p.upstreamOf(ctx, base)
	if u == nil || u.Behind == 0 || p.requireCleanTree(ctx, "sync") != nil || p.requireBranchFree(ctx, base, "sync") != nil {
		return
	}
	cur := CurrentBranch(ctx, p.Root)
	up := "refs/remotes/" + u.Upstream
	if u.Ahead == 0 {
		if cur == base {
			_, _ = git(ctx, p.Root, "merge", "--ff-only", "-q", up)
			return
		}
		old, _ := git(ctx, p.Root, "rev-parse", "refs/heads/"+base)
		_, _ = git(ctx, p.Root, "update-ref", "refs/heads/"+base, up, old)
		return
	}
	if _, err := git(ctx, p.Root, "rebase", "-q", up, base); err != nil {
		_, _ = git(ctx, p.Root, "rebase", "--abort")
	}
	if cur != "" && CurrentBranch(ctx, p.Root) != cur {
		_, _ = git(ctx, p.Root, "switch", "-q", cur)
	}
}

// refuseBehindUpstream refuses starting a change from, or archiving one
// onto, a base branch behind its upstream: v1.0 works on a local trunk
// that is up to date (D45). No upstream configured, no check. A
// coordinated project fetches first; an uncoordinated one never fetches.
func (p *Project) refuseBehindUpstream(ctx context.Context, base, what string) error {
	if p.coordinated() {
		if err := p.fetchUpstreams(ctx, base, what); err != nil {
			return err
		}
		p.syncCoordinatedBase(ctx, base)
	}
	u := p.upstreamOf(ctx, base)
	if u == nil || u.Behind == 0 {
		return nil
	}
	return &FailError{Code: CodeBehindUpstream, Msg: fmt.Sprintf("%s is %d commit(s) behind its upstream %s, and %s would build on the stale %s — pull %s (`git switch %s && git pull`), then run this again; nothing was written",
		base, u.Behind, u.Upstream, what, base, base, base)}
}
