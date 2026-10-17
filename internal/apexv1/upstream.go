package apexv1

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// Scope is what v1.0 supports (D45, operator Q1): one operator per
// checkout, on a local trunk. `ape project init` states it.
const Scope = "v1.0: one operator per checkout, on a local trunk; PR mode comes later."

// CodeBehindUpstream: the base branch is behind its upstream, and a change
// would be started from, or archived onto, a stale trunk (H6, D45).
const CodeBehindUpstream = "base.behind-upstream"

// BaseUpstream is a base branch measured against its upstream, as the
// local remote-tracking ref has it: ape never fetches.
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
	return s + " (the local " + u.Upstream + "; ape never fetches)"
}

// refuseBehindUpstream refuses starting a change from, or archiving one
// onto, a base branch behind its upstream: v1.0 works on a local trunk
// that is up to date (D45). No upstream configured, no check.
func (p *Project) refuseBehindUpstream(ctx context.Context, base, what string) error {
	u := p.upstreamOf(ctx, base)
	if u == nil || u.Behind == 0 {
		return nil
	}
	return &FailError{Code: CodeBehindUpstream, Msg: fmt.Sprintf("%s is %d commit(s) behind its upstream %s, and %s would build on the stale %s — pull %s (`git switch %s && git pull`), then run this again; nothing was written",
		base, u.Behind, u.Upstream, what, base, base, base)}
}
