package apexv1

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func git(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := gitRaw(ctx, dir, args...)
	return strings.TrimSpace(out), err
}

// gitRaw is git without trimming: porcelain output's leading column is data.
func gitRaw(ctx context.Context, dir string, args ...string) (string, error) {
	return gitRawEnv(ctx, dir, nil, args...)
}

// gitWithEnv is git with extra environment (GIT_INDEX_FILE, say), trimmed.
func gitWithEnv(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	out, err := gitRawEnv(ctx, dir, env, args...)
	return strings.TrimSpace(out), err
}

func gitRawEnv(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	c := exec.CommandContext(ctx, "git", args...)
	c.Dir = dir
	if env != nil {
		c.Env = append(os.Environ(), env...)
	}
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
// A commit with nothing to record is skipped; one whose added lines hold a
// secret is refused (commit.secret, ScanCommit) with the paths staged, and
// the refusal says how to unstage them.
func (p *Project) commitPaths(ctx context.Context, subject, changeID string, paths ...string) (string, error) {
	return p.commitWithTrailer(ctx, subject, "Change: "+changeID, paths...)
}

// commitOwnPaths is commitPaths for files ape wrote itself in this
// command, as they were before (beforeOwnEdit): a refusal undoes ape's
// edit (R28).
func (p *Project) commitOwnPaths(ctx context.Context, own ownEdits, subject, changeID string, paths ...string) (string, error) {
	return p.commitOwn(ctx, own, subject, "Change: "+changeID, paths...)
}

// commitRecord commits ape's own record files outside any change (the
// roadmap, milestones.json, an ADR skeleton, the init skeleton). Outside a
// git work tree it does nothing.
func (p *Project) commitRecord(ctx context.Context, subject, trailer string, paths ...string) (string, error) {
	return p.commitOwnRecord(ctx, nil, subject, trailer, paths...)
}

// commitOwnRecord is commitRecord for files ape wrote itself in this
// command, as they were before (beforeOwnEdit): a refusal undoes ape's
// edit (R28).
func (p *Project) commitOwnRecord(ctx context.Context, own ownEdits, subject, trailer string, paths ...string) (string, error) {
	if !p.isWorkTree(ctx) {
		return "", nil
	}
	return p.commitOwn(ctx, own, subject, trailer, paths...)
}

func (p *Project) commitWithTrailer(ctx context.Context, subject, trailer string, paths ...string) (string, error) {
	return p.commitOwn(ctx, nil, subject, trailer, paths...)
}

// commitOwn stages exactly paths and commits them; own (nil: none) is
// what the paths ape wrote itself were before it wrote them.
func (p *Project) commitOwn(ctx context.Context, own ownEdits, subject, trailer string, paths ...string) (string, error) {
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
	if err := ScanCommit(ctx, p.Root, subject, paths...); err != nil {
		return "", p.undoRefused(ctx, err, own, paths)
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

// R28 (rc.6c): a commit.secret refusal of a record commit ape makes from
// content it wrote itself in this command (a Next line, a decision, a
// milestone record, a state.json, a new change's skeleton) undoes that
// edit, so the secret is neither left staged nor blocks the next record
// commit to the same file. What a path was before ape wrote it is taken
// first (beforeOwnEdit): one clean in the index and the working tree is
// restored to HEAD (a file ape created is removed); one that was not keeps
// the working tree, and only ape's staging is taken back — the index
// entries are put back as they were — with the one command that clears the
// rest. A record commit that carries anyone else's work (a change's notes,
// moved folders, the archive squash, the lift) is never undone: its
// refusal names the `git restore --staged` that unstages it.

// ownEdits is what each path ape is about to write was before it wrote
// it; nil outside a work tree.
type ownEdits map[string]ownBefore

type ownBefore struct {
	// clean: nothing uncommitted under the path, in the index or the
	// working tree (an absent path is clean).
	clean bool
	// index is `git ls-files -s --full-name` for the path: its index
	// entries, as update-index --index-info reads them back.
	index string
}

// beforeOwnEdit records rels (project-relative files or folders) before
// ape writes them, for commitOwn's undo.
func (p *Project) beforeOwnEdit(ctx context.Context, rels ...string) ownEdits {
	if !p.isWorkTree(ctx) {
		return nil
	}
	own := ownEdits{}
	for _, rel := range rels {
		st, err := gitRaw(ctx, p.Root, "status", "--porcelain=v1", "--untracked-files=all", "--", rel)
		if err != nil {
			continue // unknown: treated as not ape's
		}
		idx, err := gitRaw(ctx, p.Root, "ls-files", "-s", "--full-name", "--", rel)
		if err != nil {
			continue
		}
		own[rel] = ownBefore{clean: strings.TrimSpace(st) == "", index: idx}
	}
	return own
}

// undoRefused is a refused record commit's error: a commit.secret refusal
// with each staged path undone (own) or named with the command that
// unstages it; any other error as it is.
func (p *Project) undoRefused(ctx context.Context, err error, own ownEdits, paths []string) error {
	fe, ok := errors.AsType[*FailError](err)
	if !ok || fe.Code != CodeCommitSecret {
		return err
	}
	from := "HEAD"
	if HeadSHA(ctx, p.Root) == "" {
		empty, err := gitWith(ctx, p.Root, nil, []byte{}, "hash-object", "-t", "tree", "--stdin")
		if err != nil {
			return fe
		}
		from = empty
	}
	var undone, unstaged, staged []string
	for _, rel := range paths {
		if out, err := git(ctx, p.Root, "diff", "--cached", "--name-only", from, "--", rel); err != nil || out == "" {
			continue // nothing of it staged
		}
		before, ours := own[rel]
		switch {
		case ours && before.clean:
			if _, err := git(ctx, p.Root, "restore", "--source="+from, "--staged", "--worktree", "--", rel); err == nil {
				undone = append(undone, rel)
				continue
			}
		case ours:
			if p.restoreIndex(ctx, rel, before.index) == nil {
				unstaged = append(unstaged, rel)
				continue
			}
		}
		staged = append(staged, rel)
	}
	var said []string
	if len(undone) > 0 {
		said = append(said, "ape's edit to "+strings.Join(undone, ", ")+" was undone")
	}
	for _, rel := range unstaged {
		said = append(said, "ape's edit to "+rel+" was unstaged and left in the working tree, which held uncommitted edits before it: take it out, or `git restore --worktree -- "+rel+"` drops every unstaged edit to "+rel)
	}
	if len(staged) > 0 {
		said = append(said, strings.Join(staged, ", ")+" stays staged: `git restore --staged -- "+strings.Join(staged, " ")+"` unstages it")
	}
	if len(said) > 0 {
		fe.Msg += ". " + strings.Join(said, "; ") + "; nothing was committed"
	}
	return fe
}

// restoreIndex puts a path's index entries back as index (ls-files -s
// output) holds them, leaving the working tree alone.
func (p *Project) restoreIndex(ctx context.Context, rel, index string) error {
	if _, err := git(ctx, p.Root, "rm", "-r", "-q", "--cached", "-f", "--ignore-unmatch", "--", rel); err != nil {
		return err
	}
	if strings.TrimSpace(index) == "" {
		return nil
	}
	_, err := gitWith(ctx, p.Root, nil, []byte(index), "update-index", "--index-info")
	return err
}
