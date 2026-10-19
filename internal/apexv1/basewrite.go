package apexv1

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Records that belong on another branch than the one checked out — a
// base-branch record (`ape next add`, a milestone's block count, an
// archived change's acceptance) written while HEAD is on a change branch,
// or a change's state written from its base — are committed there
// without a checkout: a temporary index read from the branch's tip, the
// files hashed into it, a tree and a commit made from it, and the branch
// moved only if it still points where it did (`git update-ref <ref> <new>
// <old>`). Nothing in the working tree changes.

// A branch checked out in another worktree is refused with
// CodeBaseCheckedOut (branch.go): a commit there would leave that
// worktree's index and tree behind.

// CodeRefMoved: the branch moved while ape wrote its record there.
const CodeRefMoved = "git.ref-moved"

// commitOnBase commits files (project-relative, slash-separated paths →
// content; nil content deletes the file) on the base branch as one commit
// with msg, wherever HEAD is. It returns the new commit, "" when the
// files already hold that content.
//
//nolint:unparam // base is the caller's branch, as in every branch write here; only the tests call it, on main
func (p *Project) commitOnBase(ctx context.Context, base string, files map[string][]byte, msg string) (string, error) {
	return p.commitOnBranch(ctx, base, files, msg)
}

// commitOnBranch commits files on branch as one commit with msg: in this
// worktree, as an ordinary commit of exactly those paths, when branch is
// checked out here; refused (FailError git.base-checked-out) when it is
// checked out in another worktree; else without a checkout, compare-and-
// swapped against the tip it was built on (FailError git.ref-moved when
// the branch moved meanwhile: nothing was written).
func (p *Project) commitOnBranch(ctx context.Context, branch string, files map[string][]byte, msg string) (string, error) {
	tip, err := git(ctx, p.Root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if err != nil {
		return "", &FailError{Code: CodeBranchFailed, Msg: "the branch " + branch + " does not exist — recreate it, or run the command where it does"}
	}
	return p.commitOnBranchAt(ctx, branch, tip, files, msg)
}

// editOnBranch is a read-modify-write of files on branch: edit gets the
// current content of paths at the branch's tip (nil for an absent file)
// and returns what to commit; the commit is compare-and-swapped against
// that tip, so a write made meanwhile is never overwritten.
func (p *Project) editOnBranch(ctx context.Context, branch, msg string, paths []string, edit func(cur map[string][]byte) (map[string][]byte, error)) (string, error) {
	return p.editOnBranchMsg(ctx, branch, paths, func(cur map[string][]byte) (map[string][]byte, string, error) {
		files, err := edit(cur)
		return files, msg, err
	})
}

// editOnBranchMsg is editOnBranch whose edit also decides the message.
func (p *Project) editOnBranchMsg(ctx context.Context, branch string, paths []string, edit func(cur map[string][]byte) (map[string][]byte, string, error)) (string, error) {
	here, err := p.checkedOutHere(ctx, branch)
	if err != nil {
		return "", err
	}
	tip, err := git(ctx, p.Root, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	if err != nil {
		return "", &FailError{Code: CodeBranchFailed, Msg: "the branch " + branch + " does not exist — recreate it, or run the command where it does"}
	}
	cur := map[string][]byte{}
	for _, rel := range paths {
		if here {
			if data, err := os.ReadFile(filepath.Join(p.Root, filepath.FromSlash(rel))); err == nil {
				cur[rel] = data
			}
			continue
		}
		if data, err := p.showAt(ctx, tip, rel); err == nil {
			cur[rel] = []byte(data)
		}
	}
	files, msg, err := edit(cur)
	if err != nil || len(files) == 0 {
		return "", err
	}
	return p.commitOnBranchAt(ctx, branch, tip, files, msg)
}

// readOnBranch is a project file's content as branch holds it: the
// working tree when branch is checked out here, else its tip. ok is false
// when the file is absent.
func (p *Project) readOnBranch(ctx context.Context, branch, rel string) ([]byte, bool) {
	if here, err := p.checkedOutHere(ctx, branch); err == nil && here {
		data, err := os.ReadFile(filepath.Join(p.Root, filepath.FromSlash(rel)))
		return data, err == nil
	}
	data, err := p.showAt(ctx, "refs/heads/"+branch, rel)
	return []byte(data), err == nil
}

// checkedOutHere reports whether branch is checked out in this worktree;
// FailError git.base-checked-out when another worktree has it.
func (p *Project) checkedOutHere(ctx context.Context, branch string) (bool, error) {
	if CurrentBranch(ctx, p.Root) == branch {
		return true, nil
	}
	if wt := p.worktreeOf(ctx, branch); wt != "" {
		return false, &FailError{Code: CodeBaseCheckedOut, Msg: branch + " is checked out in another worktree, " + wt +
			", and ape would commit there behind its index — run the command in that worktree, or switch it off " + branch}
	}
	return false, nil
}

// worktreeOf is the path of the worktree (other than this one) that has
// branch checked out, "" when none has.
func (p *Project) worktreeOf(ctx context.Context, branch string) string {
	out, err := git(ctx, p.Root, "worktree", "list", "--porcelain")
	if err != nil {
		return ""
	}
	self := p.topLevel(ctx)
	path := ""
	for line := range strings.SplitSeq(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			path = strings.TrimPrefix(line, "worktree ")
		case line == "branch refs/heads/"+branch && !samePath(path, self):
			return path
		}
	}
	return ""
}

func (p *Project) topLevel(ctx context.Context) string {
	top, err := git(ctx, p.Root, "rev-parse", "--show-toplevel")
	if err != nil {
		return p.Root
	}
	return top
}

// commitOnBranchAt is commitOnBranch built on tip, the branch's tip the
// caller read files at.
func (p *Project) commitOnBranchAt(ctx context.Context, branch, tip string, files map[string][]byte, msg string) (string, error) {
	here, err := p.checkedOutHere(ctx, branch)
	if err != nil {
		return "", err
	}
	rels := make([]string, 0, len(files))
	for rel := range files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	if here {
		return p.commitHere(ctx, files, rels, msg)
	}
	sha, err := p.commitDetached(ctx, branch, tip, files, rels, msg)
	if err != nil {
		if _, ok := errors.AsType[*FailError](err); ok {
			return "", err
		}
		return "", &FailError{Code: CodeCommitFailed, Msg: "cannot commit " + strings.Join(rels, ", ") + " on " + branch + ": " + err.Error() + " — fix git (identity, lock) and run the command again"}
	}
	return sha, nil
}

// commitHere writes files into the working tree and commits exactly
// those paths, as every other ape record commit does. The content is
// ape's own, so a commit.secret refusal undoes the write (R28).
func (p *Project) commitHere(ctx context.Context, files map[string][]byte, rels []string, msg string) (string, error) {
	own := p.beforeOwnEdit(ctx, rels...)
	for _, rel := range rels {
		path := filepath.Join(p.Root, filepath.FromSlash(rel))
		if files[rel] == nil {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return "", err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		if err := writeFileAtomic(path, string(files[rel])); err != nil {
			return "", err
		}
	}
	sha, err := p.commitOwn(ctx, own, msg, "", rels...)
	if err != nil {
		return "", commitFailed(err, "written but the commit failed: "+err.Error()+" — fix git (identity, lock) and commit "+strings.Join(rels, " "))
	}
	return sha, nil
}

// commitDetached builds the commit in a temporary index and moves the
// branch with a compare-and-swap.
func (p *Project) commitDetached(ctx context.Context, branch, tip string, files map[string][]byte, rels []string, msg string) (string, error) {
	tmp, err := os.MkdirTemp("", "ape-index-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(tmp, "index")}
	prefix, _ := git(ctx, p.Root, "rev-parse", "--show-prefix")
	if _, err := gitWith(ctx, p.Root, env, nil, "read-tree", tip); err != nil {
		return "", err
	}
	for _, rel := range rels {
		path := prefix + rel
		if files[rel] == nil {
			if _, err := gitWith(ctx, p.Root, env, nil, "update-index", "--force-remove", "--", path); err != nil {
				return "", err
			}
			continue
		}
		blob, err := gitWith(ctx, p.Root, env, files[rel], "hash-object", "-w", "--stdin")
		if err != nil {
			return "", err
		}
		mode := "100644"
		if ls, err := git(ctx, p.Root, "ls-tree", tip, "--", path); err == nil && strings.HasPrefix(ls, "100755 ") {
			mode = "100755"
		}
		if _, err := gitWith(ctx, p.Root, env, nil, "update-index", "--add", "--cacheinfo", mode+","+blob+","+path); err != nil {
			return "", err
		}
	}
	tree, err := gitWith(ctx, p.Root, env, nil, "write-tree")
	if err != nil {
		return "", err
	}
	if old, _ := git(ctx, p.Root, "rev-parse", tip+"^{tree}"); old == tree {
		return "", nil
	}
	subject, _, _ := strings.Cut(msg, "\n")
	if err := scanTrees(ctx, p.Root, tip, tree, subject, rels); err != nil {
		return "", err
	}
	commit, err := gitWith(ctx, p.Root, nil, []byte(strings.TrimRight(msg, "\n")+"\n"), "commit-tree", tree, "-p", tip)
	if err != nil {
		return "", err
	}
	if _, err := git(ctx, p.Root, "update-ref", "-m", subject, "refs/heads/"+branch, commit, tip); err != nil {
		return "", &FailError{Code: CodeRefMoved, Msg: branch + " moved while ape wrote its record there (" + err.Error() + ") — nothing was written; run the command again"}
	}
	return commit, nil
}

// recordsOnlyMoved reports a base branch that moved from base to tip only
// by ape's own base-branch records — milestones.json, decisions.json, the
// roadmap, next.md (D217), an archived change's folder, a change's
// accepted.json — which the change's branch has not touched since base: a
// block counted, a `ape next add`, an archived change's acceptance, a
// reviewed change's acceptance from the base (D52 Q18), a gate result.
// Such a move changes no code, so it does not make a change catch up
// (rebase, re-baseline, re-verify); the squash at archive takes the
// records along. Any other file, or one the branch touched too, is a move.
func (p *Project) recordsOnlyMoved(ctx context.Context, base, tip string) bool {
	if base == "" {
		return false
	}
	changed := func(from, to string) ([]string, bool) {
		out, err := git(ctx, p.Root, "diff", "--name-only", "--relative", from, to)
		if err != nil {
			return nil, false
		}
		var files []string
		for f := range strings.SplitSeq(out, "\n") {
			if f != "" {
				files = append(files, f)
			}
		}
		return files, true
	}
	onBase, ok := changed(base, tip)
	if !ok || len(onBase) == 0 {
		return false
	}
	onBranch, ok := changed(base, "HEAD")
	if !ok {
		return false
	}
	touched := map[string]bool{}
	for _, f := range onBranch {
		touched[f] = true
	}
	records := map[string]bool{p.Rel(p.MilestonesJSON()): true, p.Rel(p.decisionsJSON()): true, p.Rel(p.RoadmapMD()): true, p.Rel(p.NextMD()): true}
	archive := p.Rel(p.ArchiveDir()) + "/"
	for _, f := range onBase {
		if touched[f] || !records[f] && !strings.HasPrefix(f, archive) && !p.isAcceptedRecord(f) {
			return false
		}
	}
	return true
}

// gitWith is git with extra environment and stdin; output trimmed.
func gitWith(ctx context.Context, dir string, env []string, stdin []byte, args ...string) (string, error) {
	c := exec.CommandContext(ctx, "git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), env...)
	if stdin != nil {
		c.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	c.Stdout = &out
	c.Stderr = &errb
	if err := c.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(out.String()), nil
}

// baseBranch is the branch base-branch records go on: the checked-out
// branch, or, on a change's branch, the base branch that change records.
func (p *Project) baseBranch(ctx context.Context) (string, error) {
	cur := CurrentBranch(ctx, p.Root)
	id := ChangeOfBranch(cur)
	switch {
	case cur == "":
		return "", &FailError{Code: "branch.detached", Msg: "HEAD is detached — ape writes this record on the base branch, and cannot tell which: `git switch <branch>` first"}
	case id == "":
		return cur, nil
	}
	if st, err := LoadState(p.StateFile(id)); err == nil && st.BaseBranch != "" {
		return st.BaseBranch, nil
	}
	return "", &FailError{Code: CodeBranchFailed, Msg: "HEAD is on " + cur + ", whose state records no base branch — `git switch <base>` and run the command there"}
}
