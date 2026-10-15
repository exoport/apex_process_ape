package apexv1

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

func git(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := gitRaw(ctx, dir, args...)
	return strings.TrimSpace(out), err
}

// gitRaw is git without trimming: porcelain output's leading column is data.
func gitRaw(ctx context.Context, dir string, args ...string) (string, error) {
	c := exec.CommandContext(ctx, "git", args...)
	c.Dir = dir
	var out, errb bytes.Buffer
	c.Stdout = &out
	c.Stderr = &errb
	if err := c.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return out.String(), nil
}

// HeadSHA is HEAD's full sha, or "" in a repo with no commits or outside one.
func HeadSHA(ctx context.Context, dir string) string {
	sha, err := git(ctx, dir, "rev-parse", "--verify", "--quiet", "HEAD")
	if err != nil {
		return ""
	}
	return sha
}

// CurrentBranch is the checked-out branch, "" when detached or unknown.
func CurrentBranch(ctx context.Context, dir string) string {
	b, err := git(ctx, dir, "symbolic-ref", "--short", "--quiet", "HEAD")
	if err != nil {
		return ""
	}
	return b
}

// codeMovedSince reports whether anything outside the development folder
// differs between sha and HEAD. Commits that only record planning
// artifacts (review.md, state.json, the next change's draft) do not make a
// verify or a review stale; a commit to code does.
func (p *Project) codeMovedSince(ctx context.Context, sha string) (bool, []string, error) {
	head := HeadSHA(ctx, p.Root)
	if sha == "" || head == "" {
		return sha != head, nil, nil
	}
	if sha == head {
		return false, nil, nil
	}
	out, err := git(ctx, p.Root, "diff", "--name-only", sha, head)
	if err != nil {
		return true, nil, err
	}
	var moved []string
	for _, f := range strings.Split(out, "\n") {
		if f == "" || p.inDev(f) {
			continue
		}
		moved = append(moved, f)
	}
	return len(moved) > 0, moved, nil
}

// inDev reports whether a project-relative path is inside the development
// folder.
func (p *Project) inDev(rel string) bool {
	rel = strings.TrimPrefix(rel, "./")
	return rel == p.DevRel || strings.HasPrefix(rel, p.devPrefix())
}

// dirtyCode lists uncommitted paths (tracked changes and untracked files
// git does not ignore) outside the development folder. A verify over such
// a tree did not run "at a sha": its green belongs to no commit.
func (p *Project) dirtyCode(ctx context.Context) ([]string, error) {
	out, err := gitRaw(ctx, p.Root, "status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil {
		return nil, err
	}
	var dirty []string
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		path := line[3:]
		if i := strings.Index(path, " -> "); i >= 0 {
			path = path[i+4:]
		}
		path = strings.Trim(path, "\"")
		if p.inDev(path) {
			continue
		}
		dirty = append(dirty, path)
	}
	return dirty, nil
}
