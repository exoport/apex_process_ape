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
// and _apex/state/ differs between sha and HEAD. Commits that only record
// planning artifacts (review.md, state.json, the next change's draft) or
// ape's own records (milestones.json, decisions.json) do not make a verify
// or a review stale; a commit to code does.
func (p *Project) codeMovedSince(ctx context.Context, sha string) (moved bool, files []string, err error) {
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
	for f := range strings.SplitSeq(out, "\n") {
		if f == "" || p.notCode(f) {
			continue
		}
		files = append(files, f)
	}
	return len(files) > 0, files, nil
}

// notCode reports whether a project-relative path is planning or ape's own
// record rather than code: inside the development folder, or inside
// _apex/state/.
func (p *Project) notCode(rel string) bool {
	rel = strings.TrimPrefix(rel, "./")
	state := p.StateRel()
	return rel == p.DevRel || strings.HasPrefix(rel, p.devPrefix()) ||
		rel == state || strings.HasPrefix(rel, state+"/")
}

// dirtyCode lists uncommitted paths (tracked changes and untracked files
// git does not ignore) outside the development folder and _apex/state/. A
// verify over such a tree did not run "at a sha": its green belongs to no
// commit.
func (p *Project) dirtyCode(ctx context.Context) ([]string, error) {
	out, err := gitRaw(ctx, p.Root, "status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil {
		return nil, err
	}
	// Porcelain v1: two status columns, a space, then the path.
	const pathAt = 3
	var dirty []string
	for line := range strings.SplitSeq(out, "\n") {
		if len(line) <= pathAt {
			continue
		}
		path := line[pathAt:]
		if i := strings.Index(path, " -> "); i >= 0 {
			path = path[i+4:]
		}
		path = strings.Trim(path, "\"")
		if p.notCode(path) {
			continue
		}
		dirty = append(dirty, path)
	}
	return dirty, nil
}

// isWorkTree reports whether the project is inside a git work tree.
func (p *Project) isWorkTree(ctx context.Context) bool {
	_, err := git(ctx, p.Root, "rev-parse", "--is-inside-work-tree")
	return err == nil
}

// commitPaths stages exactly paths and commits only them (pre-commit
// hooks skipped: these are ape's own records), with trailer
// `Change: <changeID>`. Other staged or dirty files are left as they are.
// A commit with nothing to record is skipped.
func (p *Project) commitPaths(ctx context.Context, subject, changeID string, paths ...string) (string, error) {
	return p.commitWithTrailer(ctx, subject, "Change: "+changeID, paths...)
}

// commitRecord commits ape's own record files outside any change (the
// roadmap, milestones.json, an ADR skeleton, the init skeleton). Outside a
// git work tree it does nothing.
func (p *Project) commitRecord(ctx context.Context, subject, trailer string, paths ...string) (string, error) {
	if !p.isWorkTree(ctx) {
		return "", nil
	}
	return p.commitWithTrailer(ctx, subject, trailer, paths...)
}

func (p *Project) commitWithTrailer(ctx context.Context, subject, trailer string, paths ...string) (string, error) {
	if _, err := git(ctx, p.Root, append([]string{"add", "-A", "--"}, paths...)...); err != nil {
		return "", err
	}
	changed, err := git(ctx, p.Root, append([]string{"diff", "--cached", "--name-only", "--"}, paths...)...)
	if err != nil {
		return "", err
	}
	if changed == "" {
		return "", nil
	}
	msg := subject + "\n"
	if trailer != "" {
		msg += "\n" + trailer + "\n"
	}
	if _, err := git(ctx, p.Root, append([]string{"commit", "--no-verify", "-q", "-m", msg, "--"}, paths...)...); err != nil {
		return "", err
	}
	return HeadSHA(ctx, p.Root), nil
}
