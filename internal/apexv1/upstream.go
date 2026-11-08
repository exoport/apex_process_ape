package apexv1

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Scope is what v1.0 supports (D45, operator Q1): one operator per
// checkout, on a local trunk. `ape project init` states it. A coordinated
// project (v1.5) fetches its trunk at start, archive and catch-up.
const Scope = "v1.0: one operator per checkout, on a local trunk; PR mode comes later."

// CoordinatedScope is a coordinated project's (v1.5, FINDING-V15-1A-5):
// what init says there instead of Scope.
const CoordinatedScope = "v1.5 (in development): a coordinated project — main changes only through its merge queue, milestone/<M> branches are ape's, and members fetch before they judge a base."

// ScopeLine is the scope init states: CoordinatedScope in a coordinated
// project, else Scope.
func (p *Project) ScopeLine() string {
	if p.queued() {
		return CoordinatedScope
	}
	return Scope
}

// CodeBehindUpstream: the base branch is behind its upstream, and a change
// would be started from, or archived onto, a stale trunk (H6, D45).
const CodeBehindUpstream = "base.behind-upstream"

// CodeFetchFailed: a coordinated project's start, archive or catch-up could
// not fetch the branches it judges staleness by (v1.5, section 5.5).
const CodeFetchFailed = "base.fetch-failed"

// BaseUpstream is a base branch measured against its upstream, as the
// local remote-tracking ref has it: status never fetches; start, archive
// and catch-up fetch first only in a coordinated project
// (fetchesBeforeJudging).
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

// queued: the project integrates through a coordinated repository's merge
// queue (coordination.github, v1.5 stage 1a; mode queue/*, D876): main
// changes only through the queue, milestone/<M> branches are ape's, and
// records reach main by pull request. Read from github.repo alone, so a
// block from before 1.5 (no integration yet, which member.Load refuses)
// keeps the queue's behaviour rather than falling back to a local merge.
func (p *Project) queued() bool {
	cfg, err := LoadConfig(p.Root)
	return err == nil && cfg.Coordination != nil && cfg.Coordination.GitHub != nil && strings.TrimSpace(cfg.Coordination.GitHub.Repo) != ""
}

// fetchesBeforeJudging: the project has a coordination: block — trunk or
// queue (D876) — so someone else may have moved its trunk, and a start,
// archive or catch-up fetches it before judging the base. The one reader:
// refuseBehindUpstream. A solo project never fetches (D45).
func (p *Project) fetchesBeforeJudging() bool {
	cfg, err := LoadConfig(p.Root)
	return err == nil && cfg.Coordination != nil
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
		if err := p.fetchUpstream(ctx, remote, merge); err != nil {
			if fe, ok := errors.AsType[*FailError](err); ok {
				return fe // the App project's refusal (FINDING-V15-1A-6)
			}
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

// fetchUpstream fetches one branch's upstream (merge, refs/heads/X on
// remote) into its remote-tracking ref: the coordinated repository through
// GitVia when the project has it, else remote itself.
func (p *Project) fetchUpstream(ctx context.Context, remote, merge string) error {
	b, ok := strings.CutPrefix(merge, "refs/heads/")
	if GitVia != nil && ok && remote == p.trunkRemote(ctx) {
		return p.fetchCoordinated(ctx, b)
	}
	_, err := gitWithEnv(ctx, p.Root, noPromptEnv, "fetch", "--quiet", "--no-tags", remote, merge)
	return err
}

// refuseBehindUpstream refuses starting a change from, or archiving one
// onto, a base branch behind its upstream: v1.0 works on a local trunk
// that is up to date (D45). No upstream configured, no check. A
// coordinated project (trunk or queue) fetches first; a solo one never
// fetches.
func (p *Project) refuseBehindUpstream(ctx context.Context, base, what string) error {
	if p.fetchesBeforeJudging() {
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
